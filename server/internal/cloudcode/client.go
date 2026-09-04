package cloudcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/models"
)

var bases = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal",
	"https://daily-cloudcode-pa.googleapis.com/v1internal",
	"https://cloudcode-pa.googleapis.com/v1internal",
}

type Client struct {
	cfg  config.Config
	http *http.Client
}

func New(cfg config.Config) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   8 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   8 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
}

func (c *Client) doJSON(method, accessToken string, payload any, query string) (*http.Response, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	var lastErr error
	for i, base := range bases {
		url := base + ":" + method
		if query != "" {
			url += "?" + query
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", c.cfg.UserAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		if shouldFallback(resp.StatusCode) && i < len(bases)-1 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			cancel()
			lastErr = fmt.Errorf("%s returned %d", url, resp.StatusCode)
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		cancel()
		return resp, data, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all cloudcode endpoints failed")
	}
	return nil, nil, lastErr
}

func (c *Client) Stream(ctx context.Context, method, accessToken string, payload any, query string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for i, base := range bases {
		url := base + ":" + method
		if query != "" {
			url += "?" + query
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", c.cfg.UserAgent)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := c.http.Do(req)
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
		return resp, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all cloudcode endpoints failed")
	}
	return nil, lastErr
}

func shouldFallback(status int) bool {
	return status == 404 || status == 408 || status >= 500
}

func (c *Client) LoadCodeAssist(accessToken string) (projectID, tier string, err error) {
	payload := map[string]any{
		"metadata": map[string]any{"ideType": "ANTIGRAVITY"},
	}
	resp, data, err := c.doJSON("loadCodeAssist", accessToken, payload, "")
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("loadCodeAssist failed (%d): %s", resp.StatusCode, clip(data))
	}
	var parsed struct {
		ProjectID   string `json:"cloudaicompanionProject"`
		CurrentTier *tierInfo  `json:"currentTier"`
		PaidTier    *tierInfo  `json:"paidTier"`
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
	resp, data, err := c.doJSON("fetchAvailableModels", accessToken, payload, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 403 && projectID != "" {
		resp, data, err = c.doJSON("fetchAvailableModels", accessToken, map[string]any{}, "")
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
	resp, data, err := c.doJSON("retrieveUserQuotaSummary", accessToken, payload, "")
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
	if stream {
		resp, err := c.Stream(ctx, "streamGenerateContent", accessToken, payload, "alt=sse")
		return resp, nil, err
	}
	resp, data, err := c.doJSON("generateContent", accessToken, payload, "")
	return resp, data, err
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
