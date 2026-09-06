package httpapi

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type retryDecision struct {
	retry       bool
	delay       time.Duration
	cooldown    time.Duration
	modelScoped bool
	grace       bool
	category    string
}

func retryPolicy(status int, headers http.Header, body []byte, attempt int) retryDecision {
	p := retryDecision{category: "upstream_error"}
	text := strings.ToLower(string(body))
	delay, hinted := upstreamRetryDelay(headers.Get("Retry-After"), body, time.Now())
	switch {
	case status == 429:
		p.retry, p.category = true, "rate_limit"
		p.modelScoped = strings.Contains(text, "quota_exhausted") || strings.Contains(text, "quota exhausted") || strings.Contains(text, "quota has been exhausted") || strings.Contains(text, "quotareset") || strings.Contains(text, "daily limit") || strings.Contains(text, "weekly limit")
		if p.modelScoped {
			p.category = "quota_exhausted"
		}
		if !hinted {
			delay = time.Duration(min(attempt+1, 6)) * 5 * time.Second
		}
		p.grace = hinted && delay <= 2*time.Second && !p.modelScoped
		p.cooldown = max(delay, time.Second)
		if p.modelScoped && !hinted {
			p.cooldown = time.Minute
		}
	case status == 401:
		p.retry, p.category, p.delay = true, "unauthorized", 200*time.Millisecond
		return p
	case status == 403:
		p.retry, p.category, p.cooldown = true, "forbidden", 10*time.Minute
		return p
	case status == 404 || status == 408:
		p.retry, p.category, delay = true, "endpoint_unavailable", 300*time.Millisecond
	case status >= 500:
		p.retry, p.category = true, "upstream_unavailable"
		if !hinted {
			delay = time.Duration(1<<min(attempt, 4)) * time.Second
		}
		p.cooldown = min(delay, 30*time.Second)
	default:
		return p
	}
	// Add a small jitter so parallel callers do not all retry on the same boundary.
	p.delay = min(delay, 30*time.Second) + time.Duration(rand.Intn(100)+50)*time.Millisecond
	return p
}

func upstreamRetryDelay(header string, body []byte, now time.Time) (time.Duration, bool) {
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && seconds >= 0 {
		return clampRetryDuration(seconds), true
	}
	if when, err := http.ParseTime(header); err == nil {
		return min(max(when.Sub(now), 0), 24*time.Hour), true
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return 0, false
	}
	var visit func(any) (time.Duration, bool)
	visit = func(v any) (time.Duration, bool) {
		switch x := v.(type) {
		case map[string]any:
			for _, key := range []string{"retryDelay", "retry_delay", "quotaResetDelay", "quota_reset_delay", "retryAfter", "retry_after"} {
				if raw, ok := x[key]; ok {
					switch d := raw.(type) {
					case string:
						if parsed, err := time.ParseDuration(d); err == nil && parsed >= 0 {
							return min(parsed, 24*time.Hour), true
						}
						if seconds, err := strconv.ParseFloat(d, 64); err == nil && seconds >= 0 {
							return clampRetryDuration(seconds), true
						}
					case float64:
						if d >= 0 {
							return clampRetryDuration(d), true
						}
					case map[string]any:
						seconds, _ := d["seconds"].(float64)
						if s, ok := d["seconds"].(string); ok {
							seconds, _ = strconv.ParseFloat(s, 64)
						}
						nanos, _ := d["nanos"].(float64)
						return clampRetryDuration(seconds + nanos/1e9), true
					}
				}
			}
			for _, child := range x {
				if d, ok := visit(child); ok {
					return d, true
				}
			}
		case []any:
			for _, child := range x {
				if d, ok := visit(child); ok {
					return d, true
				}
			}
		}
		return 0, false
	}
	return visit(value)
}

func clampRetryDuration(seconds float64) time.Duration {
	if seconds > 86400 {
		seconds = 86400
	}
	if seconds < 0 {
		seconds = 0
	}
	return time.Duration(seconds * float64(time.Second))
}

var sensitiveValues = regexp.MustCompile(`(?i)(Bearer\s+)[^\s",}]+|((?:access_token|refresh_token|api_key|client_secret)["\s:=]+)[^\s",}]+|1//[A-Za-z0-9_-]+`)
var proxyCredentials = regexp.MustCompile(`(https?|socks5h?)://[^\s/@]+:[^\s/@]+@`)

func sanitizeError(value string) string {
	value = sensitiveValues.ReplaceAllString(value, "${1}${2}[redacted]")
	return proxyCredentials.ReplaceAllString(value, "${1}://[redacted]@")
}
