package httpapi

import (
	"encoding/json"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"sort"
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
	delay, hinted, fromText := upstreamRetryHint(headers.Get("Retry-After"), body, time.Now())
	switch {
	case status == 429:
		p.retry = true
		p.category = rateLimitCategory(body)
		p.modelScoped = p.category == "quota_exhausted"
		if !hinted {
			delay = time.Duration(min(attempt+1, 6)) * 5 * time.Second
		}
		p.grace = hinted && delay <= 5*time.Second && !p.modelScoped
		p.cooldown = max(delay, time.Second)
		if !hinted {
			switch p.category {
			case "quota_exhausted":
				p.cooldown = time.Minute
			case "resource_exhausted":
				p.cooldown = 30 * time.Second
			case "unknown_rate_limit":
				p.cooldown = time.Minute
			}
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
	// Hints need a margin: retrying at the exact reset boundary can be too early.
	margin := time.Duration(rand.Intn(100)+50) * time.Millisecond
	if hinted {
		if fromText {
			margin += time.Second
		} else {
			margin += 200 * time.Millisecond
		}
	}
	p.delay = min(delay, 30*time.Second) + margin
	return p
}

func rateLimitCategory(body []byte) string {
	var object map[string]any
	if json.Unmarshal(body, &object) == nil {
		if response, ok := object["response"].(map[string]any); ok {
			object = response
		}
		if upstream, ok := object["error"].(map[string]any); ok {
			details, _ := upstream["details"].([]any)
			for _, value := range details {
				detail, _ := value.(map[string]any)
				reason, _ := detail["reason"].(string)
				switch strings.ToUpper(reason) {
				case "QUOTA_EXHAUSTED":
					return "quota_exhausted"
				case "RATE_LIMIT_EXCEEDED":
					return "rate_limit"
				case "MODEL_CAPACITY_EXHAUSTED":
					return "model_capacity_exhausted"
				}
			}
		}
	}
	text := strings.ToLower(string(body))
	// Minute limits are not daily quota exhaustion, even if their text says quota.
	switch {
	case strings.Contains(text, "model_capacity_exhausted") || strings.Contains(text, "no capacity available") || strings.Contains(text, "model capacity"):
		return "model_capacity_exhausted"
	case strings.Contains(text, "rate_limit_exceeded") || strings.Contains(text, "per minute") || strings.Contains(text, "rate limit") || strings.Contains(text, "too many requests"):
		return "rate_limit"
	case strings.Contains(text, "quota_exhausted") || strings.Contains(text, "quota exhausted") || strings.Contains(text, "quota has been exhausted") || strings.Contains(text, "quotareset") || strings.Contains(text, "quota reset") || strings.Contains(text, "quota will reset") || strings.Contains(text, "daily limit") || strings.Contains(text, "weekly limit") || strings.Contains(text, "daily quota") || strings.Contains(text, "per day"):
		return "quota_exhausted"
	case strings.Contains(text, "resource_exhausted") || strings.Contains(text, "resource has been exhausted"):
		return "resource_exhausted"
	default:
		return "unknown_rate_limit"
	}
}

func upstreamRetryDelay(header string, body []byte, now time.Time) (time.Duration, bool) {
	delay, ok, _ := upstreamRetryHint(header, body, now)
	return delay, ok
}

func upstreamRetryHint(header string, body []byte, now time.Time) (time.Duration, bool, bool) {
	if d, ok := parseRetryValue(strings.TrimSpace(header)); ok {
		return d, true, false
	}
	if when, err := http.ParseTime(header); err == nil {
		return min(max(when.Sub(now), 0), 24*time.Hour), true, false
	}
	var value any
	if json.Unmarshal(body, &value) == nil {
		visited := 0
		var visit func(any, int) (time.Duration, bool)
		visit = func(v any, depth int) (time.Duration, bool) {
			visited++
			if depth > 8 || visited > 2048 {
				return 0, false
			}
			switch x := v.(type) {
			case map[string]any:
				keys := make([]string, 0, len(x))
				for k := range x {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, key := range keys {
					norm := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(key))
					switch norm {
					case "retrydelay", "quotaresetdelay", "retryafter", "backofflimit":
						if d, ok := parseRetryValue(x[key]); ok {
							return d, true
						}
					}
				}
				for _, key := range keys {
					if d, ok := visit(x[key], depth+1); ok {
						return d, true
					}
				}
			case []any:
				for _, child := range x {
					if d, ok := visit(child, depth+1); ok {
						return d, true
					}
				}
			}
			return 0, false
		}
		if d, ok := visit(value, 0); ok {
			return d, true, false
		}
	}
	if match := retryTextPattern.FindSubmatch(body); len(match) > 1 {
		if d, ok := parseRetryValue(string(match[1])); ok {
			return d, true, true
		}
	}
	return 0, false, false
}

var durationParts = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(milliseconds?|ms|seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)`)
var retryTextPattern = regexp.MustCompile(`(?i)(?:quota will reset (?:after|in)|retry after|reset after|try again in|backoff for|\bwait)\s+((?:[0-9]+(?:\.[0-9]+)?\s*(?:milliseconds?|ms|seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)\s*)+)`)

func parseRetryValue(raw any) (time.Duration, bool) {
	switch v := raw.(type) {
	case string:
		if seconds, err := strconv.ParseFloat(v, 64); err == nil && finiteNonnegative(seconds) {
			return clampRetryDuration(seconds), true
		}
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return min(d, 24*time.Hour), true
		}
		matches := durationParts.FindAllStringSubmatch(v, -1)
		if len(matches) == 0 || strings.TrimSpace(durationParts.ReplaceAllString(v, "")) != "" {
			return 0, false
		}
		seconds := 0.0
		for _, match := range matches {
			n, err := strconv.ParseFloat(match[1], 64)
			if err != nil || !finiteNonnegative(n) {
				return 0, false
			}
			unit := strings.ToLower(match[2])
			switch {
			case unit == "ms" || strings.HasPrefix(unit, "millisecond"):
				n /= 1000
			case unit == "m" || strings.HasPrefix(unit, "min"):
				n *= 60
			case unit == "h" || strings.HasPrefix(unit, "hr") || strings.HasPrefix(unit, "hour"):
				n *= 3600
			}
			seconds += n
		}
		return clampRetryDuration(seconds), true
	case float64:
		if finiteNonnegative(v) {
			return clampRetryDuration(v), true
		}
	case map[string]any:
		seconds, hasSeconds := v["seconds"]
		nanos, hasNanos := v["nanos"]
		if !hasSeconds && !hasNanos {
			return 0, false
		}
		parse := func(value any) (float64, bool) {
			if value == nil {
				return 0, true
			}
			switch n := value.(type) {
			case float64:
				return n, finiteNonnegative(n)
			case string:
				f, err := strconv.ParseFloat(n, 64)
				return f, err == nil && finiteNonnegative(f)
			}
			return 0, false
		}
		sec, ok := parse(seconds)
		if !ok {
			return 0, false
		}
		nano, ok := parse(nanos)
		if !ok || nano >= 1e9 {
			return 0, false
		}
		return clampRetryDuration(sec + nano/1e9), true
	}
	return 0, false
}
func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
func clampRetryDuration(seconds float64) time.Duration {
	if seconds > 86400 {
		seconds = 86400
	}
	if seconds < 0 || math.IsNaN(seconds) {
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
