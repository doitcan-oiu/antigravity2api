package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamRetryDelay(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header, body string
		want               time.Duration
		ok                 bool
	}{
		{"seconds", "1.5", "", 1500 * time.Millisecond, true},
		{"date", now.Add(4 * time.Second).Format(http.TimeFormat), "", 4 * time.Second, true},
		{"retry_info", "", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0.7s"}]}}`, 700 * time.Millisecond, true},
		{"quota_reset", "", `{"error":{"details":[{"metadata":{"quotaResetDelay":"2h3m"}}]}}`, 123 * time.Minute, true},
		{"duration_object", "", `{"retryDelay":{"seconds":"1","nanos":500000000}}`, 1500 * time.Millisecond, true},
		{"bounded", "9999999999999999", "", 24 * time.Hour, true},
		{"bounded_date", now.Add(7 * 24 * time.Hour).Format(http.TimeFormat), "", 24 * time.Hour, true},
		{"invalid", "-1", `{"retryDelay":"bad"}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := upstreamRetryDelay(tc.header, []byte(tc.body), now)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %v,%v want %v,%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
func TestRetryPolicyScopesAndBudgets(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		code                 int
		hint, body           string
		retry, scoped, grace bool
	}{
		{"short_rpm", 429, "1", `{"error":{"message":"too many requests"}}`, true, false, true},
		{"quota", 429, "1", `{"error":{"message":"quota exhausted"}}`, true, true, false},
		{"long_rpm", 429, "60", "", true, false, false},
		{"server", 503, "", "", true, false, false},
		{"client", 400, "", "", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := make(http.Header)
			h.Set("Retry-After", tc.hint)
			p := retryPolicy(tc.code, h, []byte(tc.body), 0)
			if p.retry != tc.retry || p.modelScoped != tc.scoped || p.grace != tc.grace {
				t.Fatalf("policy=%+v", p)
			}
			if p.delay > 31*time.Second {
				t.Fatalf("unbounded delay %v", p.delay)
			}
		})
	}
}
func TestErrorSanitization(t *testing.T) {
	for _, input := range []string{`{"refresh_token":"supersecret"}`, `Bearer supersecret`, "https://user:supersecret@proxy.example", `access_token=supersecret`} {
		if strings.Contains(sanitizeError(input), "supersecret") {
			t.Fatalf("credential remained in %q", sanitizeError(input))
		}
	}
}

func TestRetryHintsAndCooldownParity(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		header, body string
		want         time.Duration
	}{
		{"", `{"error":{"message":"Please try again in 1.5 seconds."}}`, 1500 * time.Millisecond},
		{"", `{"error":{"message":"quota will reset after 1 hour 2 minutes 3.5 seconds."}}`, time.Hour + 2*time.Minute + 3500*time.Millisecond},
		{"", `{"error":{"message":"wait 5 seconds","details":[{"metadata":{"retry-after":"0.5s"}}]}}`, 500 * time.Millisecond},
		{"1.2s", "", 1200 * time.Millisecond},
		{"", `{"error":{"details":[{"backoff_limit":{"seconds":"2","nanos":250000000}}]}}`, 2250 * time.Millisecond},
	} {
		got, ok := upstreamRetryDelay(tc.header, []byte(tc.body), now)
		if !ok || got != tc.want {
			t.Errorf("hint=%s %s got=%s,%v want=%s", tc.header, tc.body, got, ok, tc.want)
		}
	}
	for _, body := range []string{`{"retryDelay":{}}`, `{"retryDelay":{"seconds":"invalid"}}`, `{"retryDelay":{"seconds":-1}}`, `{"retryDelay":"NaN"}`, `{"requestId":"1234s"}`} {
		if d, ok := upstreamRetryDelay("", []byte(body), now); ok {
			t.Errorf("invalid hint accepted: %s=%s", body, d)
		}
	}
	p := retryPolicy(429, nil, []byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`), 0)
	if p.cooldown != 30*time.Second || p.modelScoped || p.category != "resource_exhausted" {
		t.Fatalf("generic resource policy=%+v", p)
	}
	p = retryPolicy(429, nil, []byte(`{"error":{"message":"Resource has been exhausted","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`), 0)
	if p.category != "model_capacity_exhausted" || p.modelScoped {
		t.Fatalf("capacity=%+v", p)
	}
	p = retryPolicy(429, nil, []byte(`{"error":{"message":"Resource has been exhausted","details":[{"retryDelay":"3s"}]}}`), 0)
	if !p.grace || p.delay < 3200*time.Millisecond || p.delay > 3400*time.Millisecond {
		t.Fatalf("5s grace window missing: %+v", p)
	}
	p = retryPolicy(429, nil, []byte(`{"error":{"message":"try again in 1.5 seconds"}}`), 0)
	if !p.grace || p.delay < 2500*time.Millisecond {
		t.Fatalf("text hint buffer missing: %+v", p)
	}
}

func TestExplicitReasonOverridesGenericMessage(t *testing.T) {
	p := retryPolicy(429, nil, []byte(`{"error":{"message":"Rate limit exceeded","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"60s"},{"reason":"QUOTA_EXHAUSTED"}]}}`), 0)
	if !p.modelScoped || p.category != "quota_exhausted" || p.cooldown != time.Minute {
		t.Fatalf("structured reason was overridden by message: %+v", p)
	}
}
