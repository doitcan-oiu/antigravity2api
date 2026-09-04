package cloudcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/convert"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/outbound"
)

var bases = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal",
	"https://daily-cloudcode-pa.googleapis.com/v1internal",
	"https://cloudcode-pa.googleapis.com/v1internal",
}

type Client struct {
	cfg       config.Config
	out       *outbound.Manager
	machineID string
	sessionID string
	version   string
}

func New(cfg config.Config, out *outbound.Manager) *Client {
	if out == nil {
		out = outbound.New()
	}
	return &Client{
		cfg:       cfg,
		out:       out,
		machineID: loadOrCreateMachineID(cfg.DataDir),
		sessionID: uuid.NewString(),
		version:   parseAntigravityVersion(cfg.UserAgent),
	}
}

func (c *Client) client() *http.Client {
	return c.out.Client(0)
}

func (c *Client) doJSON(ctx context.Context, method, accessToken string, payload any, query string) (*http.Response, []byte, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
	}
	resp, err := c.call(ctx, method, accessToken, payload, query, false)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp, data, nil
}

func (c *Client) Stream(ctx context.Context, method, accessToken string, payload any, query string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return c.call(ctx, method, accessToken, payload, query, true)
}

func (c *Client) call(ctx context.Context, method, accessToken string, payload any, query string, stream bool) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	stripProject := false
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		for i, base := range bases {
			url := base + ":" + method
			if query != "" {
				url += "?" + query
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, requestBody(body, stream && method == "streamGenerateContent"))
			if err != nil {
				return nil, err
			}
			if stream && method == "streamGenerateContent" {
				req.ContentLength = -1
				req.Header.Del("Content-Length")
			}
			c.applyHeaders(req, accessToken, method, payload)
			if stripProject {
				req.Header.Del("x-goog-user-project")
			}
			resp, err := c.out.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			if shouldFallback(resp.StatusCode) && i < len(bases)-1 {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				lastErr = fmt.Errorf("%s returned %d", url, resp.StatusCode)
				continue
			}
			if resp.StatusCode == 403 && !stripProject && req.Header.Get("x-goog-user-project") != "" {
				peek, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
				resp.Body.Close()
				if isServiceDisabled(peek) {
					stripProject = true
					lastErr = fmt.Errorf("%s returned 403 SERVICE_DISABLED", url)
					break
				}
				resp.Body = io.NopCloser(bytes.NewReader(peek))
				resp.ContentLength = int64(len(peek))
				return resp, nil
			}
			return resp, nil
		}
		if !stripProject {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all cloudcode endpoints failed")
	}
	return nil, lastErr
}

func (c *Client) applyHeaders(req *http.Request, accessToken, method string, payload any) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("x-client-name", "antigravity")
	if c.version != "" {
		req.Header.Set("x-client-version", c.version)
	}
	if c.machineID != "" {
		req.Header.Set("x-machine-id", c.machineID)
	}
	if c.sessionID != "" {
		req.Header.Set("x-vscode-sessionid", c.sessionID)
	}
	if method != "generateContent" && method != "streamGenerateContent" {
		if proj := payloadProject(payload); proj != "" {
			req.Header.Set("x-goog-user-project", proj)
		}
	}
	if model := payloadModel(payload); strings.Contains(strings.ToLower(model), "claude") {
		req.Header.Set("anthropic-beta", "claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
	}
}

func shouldFallback(status int) bool {
	return status == 404 || status == 408 || status >= 500
}

func (c *Client) LoadCodeAssist(accessToken string) (projectID, tier string, err error) {
	payload := map[string]any{
		"metadata": map[string]any{"ideType": "ANTIGRAVITY"},
	}
	resp, data, err := c.doJSON(nil, "loadCodeAssist", accessToken, payload, "")
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("loadCodeAssist failed (%d): %s", resp.StatusCode, clip(data))
	}
	var parsed struct {
		ProjectID   string    `json:"cloudaicompanionProject"`
		CurrentTier *tierInfo `json:"currentTier"`
		PaidTier    *tierInfo `json:"paidTier"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", "", err
	}
	if parsed.PaidTier != nil {
		tier = firstNonEmpty(parsed.PaidTier.Name, parsed.PaidTier.ID)
	}
	if tier == "" && parsed.CurrentTier != nil {
		tier = firstNonEmpty(parsed.CurrentTier.Name, parsed.CurrentTier.ID)
	}
	return parsed.ProjectID, tier, nil
}

type tierInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) FetchQuota(accessToken, projectID string) (*models.QuotaData, error) {
	payload := map[string]any{}
	if projectID != "" {
		payload["project"] = projectID
	}
	resp, data, err := c.doJSON(nil, "fetchAvailableModels", accessToken, payload, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 403 && projectID != "" {
		resp, data, err = c.doJSON(nil, "fetchAvailableModels", accessToken, map[string]any{}, "")
		if err != nil {
			return nil, err
		}
	}
	q := &models.QuotaData{LastUpdated: time.Now().Unix(), ForwardingRules: map[string]string{}}
	if resp.StatusCode == 403 {
		q.IsForbidden = true
		q.ForbiddenReason = clip(data)
		return q, nil
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetchAvailableModels failed (%d): %s", resp.StatusCode, clip(data))
	}
	var parsed struct {
		Models map[string]struct {
			QuotaInfo *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"quotaInfo"`
			DisplayName      string `json:"displayName"`
			SupportsImages   *bool  `json:"supportsImages"`
			SupportsThinking *bool  `json:"supportsThinking"`
			ThinkingBudget   *int   `json:"thinkingBudget"`
			Recommended      *bool  `json:"recommended"`
		} `json:"models"`
		Deprecated map[string]struct {
			NewModelID string `json:"newModelId"`
		} `json:"deprecatedModelIds"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	for name, info := range parsed.Models {
		mq := models.ModelQuota{
			Name:             name,
			DisplayName:      info.DisplayName,
			SupportsImages:   info.SupportsImages,
			SupportsThinking: info.SupportsThinking,
			ThinkingBudget:   info.ThinkingBudget,
			Recommended:      info.Recommended,
		}
		if info.QuotaInfo != nil {
			mq.ResetTime = info.QuotaInfo.ResetTime
			if info.QuotaInfo.RemainingFraction != nil {
				mq.Percentage = int(*info.QuotaInfo.RemainingFraction * 100)
			}
		}
		q.Models = append(q.Models, mq)
	}
	for oldID, info := range parsed.Deprecated {
		q.ForwardingRules[oldID] = info.NewModelID
	}
	if groups := c.fetchQuotaSummary(accessToken, projectID); len(groups) > 0 {
		q.QuotaGroups = groups
	}
	return q, nil
}

func (c *Client) fetchQuotaSummary(accessToken, projectID string) []models.QuotaGroup {
	payload := map[string]any{}
	if projectID != "" {
		payload["project"] = projectID
	}
	resp, data, err := c.doJSON(nil, "retrieveUserQuotaSummary", accessToken, payload, "")
	if err != nil || resp.StatusCode >= 400 {
		return nil
	}
	var parsed struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
			Buckets     []struct {
				BucketID          string   `json:"bucketId"`
				Window            string   `json:"window"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
				DisplayName       string   `json:"displayName"`
				Description       string   `json:"description"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		return nil
	}
	out := make([]models.QuotaGroup, 0, len(parsed.Groups))
	for _, g := range parsed.Groups {
		group := models.QuotaGroup{DisplayName: g.DisplayName, Description: g.Description}
		for _, b := range g.Buckets {
			frac := 0.0
			if b.RemainingFraction != nil {
				frac = *b.RemainingFraction
			}
			group.Buckets = append(group.Buckets, models.QuotaBucket{
				BucketID:          b.BucketID,
				Window:            b.Window,
				RemainingFraction: frac,
				ResetTime:         b.ResetTime,
				DisplayName:       b.DisplayName,
				Description:       b.Description,
			})
		}
		out = append(out, group)
	}
	return out
}

func (c *Client) Generate(ctx context.Context, accessToken string, payload any, stream bool) (*http.Response, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := c.Stream(ctx, "streamGenerateContent", accessToken, payload, "alt=sse")
	if err != nil {
		return nil, nil, err
	}
	if stream {
		return resp, nil, nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp, nil, err
	}
	if resp.StatusCode >= 400 {
		return resp, data, nil
	}
	collected, err := convert.CollectGeminiJSON(bytes.NewReader(data))
	if err != nil {
		return resp, data, err
	}
	return resp, collected, nil
}

type noLenReader struct{ r io.Reader }

func (n noLenReader) Read(p []byte) (int, error) { return n.r.Read(p) }

func requestBody(body []byte, chunked bool) io.Reader {
	r := bytes.NewReader(body)
	if chunked {
		return noLenReader{r}
	}
	return r
}

func payloadModel(payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	s, _ := m["model"].(string)
	return strings.TrimSpace(s)
}

func payloadProject(payload any) string {
	var project string
	switch v := payload.(type) {
	case map[string]any:
		project, _ = v["project"].(string)
	default:
		raw, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			return ""
		}
		project, _ = m["project"].(string)
	}
	project = strings.TrimSpace(project)
	if project == "" || project == "test-project" || project == "project-id" {
		return ""
	}
	return project
}

func parseAntigravityVersion(ua string) string {
	const prefix = "Antigravity/"
	i := strings.Index(ua, prefix)
	if i < 0 {
		return "4.6.7"
	}
	rest := ua[i+len(prefix):]
	if j := strings.IndexAny(rest, " \t("); j >= 0 {
		rest = rest[:j]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "4.6.7"
	}
	return rest
}

func loadOrCreateMachineID(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = "./data"
	}
	path := filepath.Join(dir, "machine-id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	id := uuid.NewString()
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(path, []byte(id+"\n"), 0o644)
	}
	return id
}

func isServiceDisabled(body []byte) bool {
	return strings.Contains(strings.ToUpper(string(body)), "SERVICE_DISABLED")
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 800 {
		return s[:800] + "..."
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
