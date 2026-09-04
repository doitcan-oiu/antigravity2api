package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/outbound"
)

const (
	tokenURL    = "https://oauth2.googleapis.com/token"
	userInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	refreshSkew = int64(900)
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

type UserInfo struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
	Picture    string `json:"picture"`
}

func (u UserInfo) DisplayName() string {
	if strings.TrimSpace(u.Name) != "" {
		return u.Name
	}
	parts := strings.TrimSpace(strings.Join([]string{u.GivenName, u.FamilyName}, " "))
	return parts
}

type Client struct {
	cfg config.Config
	out *outbound.Manager
}

func New(cfg config.Config, out *outbound.Manager) *Client {
	if out == nil {
		out = outbound.New()
	}
	return &Client{cfg: cfg, out: out}
}

func (c *Client) client() *http.Client {
	return c.out.Client(20 * time.Second)
}

func (c *Client) Refresh(refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", c.cfg.OAuthClientID)
	form.Set("client_secret", c.cfg.OAuthClientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.cfg.OAuthUserAgent)

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out TokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("refresh failed: empty access_token")
	}
	if out.ExpiresIn == 0 {
		out.ExpiresIn = 3600
	}
	return &out, nil
}

func (c *Client) UserInfo(accessToken string) (*UserInfo, error) {
	req, err := http.NewRequest(http.MethodGet, userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", c.cfg.OAuthUserAgent)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("userinfo failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out UserInfo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.Email == "" {
		return nil, fmt.Errorf("userinfo missing email")
	}
	return &out, nil
}

func NeedsRefresh(expiryTimestamp int64) bool {
	return expiryTimestamp <= time.Now().Unix()+refreshSkew
}

func ExtractTokens(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var tokens []string
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || !strings.HasPrefix(t, "1//") {
			return
		}
		if _, ok := seen[t]; ok {
			return
		}
		seen[t] = struct{}{}
		tokens = append(tokens, t)
	}

	if strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "{") {
		var anyJSON any
		if json.Unmarshal([]byte(raw), &anyJSON) == nil {
			walkRefreshTokens(anyJSON, add)
		}
	}
	if len(tokens) == 0 {
		// regex-like scan for 1// tokens
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "1//") {
				// take first whitespace-separated token
				fields := strings.Fields(line)
				if len(fields) > 0 {
					add(sanitizeToken(fields[0]))
				}
				continue
			}
		}
		// also scan whole blob
		start := 0
		for {
			i := strings.Index(raw[start:], "1//")
			if i < 0 {
				break
			}
			i += start
			j := i + 3
			for j < len(raw) {
				c := raw[j]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
					j++
					continue
				}
				break
			}
			add(raw[i:j])
			start = j
		}
	}
	return tokens
}

func sanitizeToken(t string) string {
	t = strings.Trim(t, `"' ,`)
	return t
}

func walkRefreshTokens(v any, add func(string)) {
	switch x := v.(type) {
	case map[string]any:
		if rt, ok := x["refresh_token"].(string); ok {
			add(rt)
		}
		for _, child := range x {
			walkRefreshTokens(child, add)
		}
	case []any:
		for _, child := range x {
			walkRefreshTokens(child, add)
		}
	}
}
