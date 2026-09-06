package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wo/antigravity2api/internal/convert"
)

func TestSSEErrorDetailsDriveGraceRetry(t *testing.T) {
	var tokens []string
	var waits []time.Duration
	up := &fakeUpstream{call: func(_ context.Context, token string, _ any, stream, count bool) (*http.Response, []byte, error) {
		tokens = append(tokens, token)
		if len(tokens) == 1 {
			return upstreamResponse(200, "data: "+`{"response":{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3s"}]}}}`+"\n\n", true)
		}
		return upstreamResponse(200, "data: "+successfulGemini+"\n\n", true)
	}}
	s, handler := newProxyFixture(t, 2, up)
	s.wait = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-3.7-flash-high","stream":true,"messages":[{"role":"user","content":"hello"}]}`, "Authorization", "Bearer test-api")
	if w.Code != 200 || len(tokens) != 2 || tokens[0] != tokens[1] || len(waits) != 1 || waits[0] < 3200*time.Millisecond || waits[0] > 3400*time.Millisecond {
		t.Fatalf("status=%d tokens=%v waits=%v body=%s", w.Code, tokens, waits, w.Body.String())
	}
}

func TestPenaltyRejectionRetriesOnceWithoutChangingAccount(t *testing.T) {
	for _, rejectAgain := range []bool{false, true} {
		t.Run(map[bool]string{true: "persistent", false: "recovered"}[rejectAgain], func(t *testing.T) {
			var tokens []string
			up := &fakeUpstream{call: func(_ context.Context, token string, payload any, stream, count bool) (*http.Response, []byte, error) {
				tokens = append(tokens, token)
				inner := payload.(convert.OuterRequest).Request.(convert.InnerRequest)
				config := convert.AsMap(inner.GenerationConfig)
				if len(tokens) == 1 {
					if config["presencePenalty"] != 0.2 || config["frequencyPenalty"] != 0.3 {
						t.Fatalf("original nonzero penalties lost: %v", config)
					}
				} else {
					if _, exists := config["presencePenalty"]; exists {
						t.Fatalf("penalty remained: %v", config)
					}
					if _, exists := config["frequencyPenalty"]; exists {
						t.Fatalf("penalty remained: %v", config)
					}
					if config["temperature"] != 0.7 {
						t.Fatalf("unrelated parameter changed: %v", config)
					}
				}
				if len(tokens) == 1 || rejectAgain {
					return upstreamResponse(400, `{"error":{"code":400,"message":"Penalty is not enabled for this model.","status":"INVALID_ARGUMENT"}}`, false)
				}
				return upstreamResponse(200, successfulGemini, false)
			}}
			_, handler := newProxyFixture(t, 2, up)
			w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-3.6-flash-medium","presence_penalty":0.2,"frequency_penalty":0.3,"temperature":0.7,"messages":[{"role":"user","content":"hello"}]}`, "Authorization", "Bearer test-api")
			want := 200
			if rejectAgain {
				want = 400
			}
			if w.Code != want || len(tokens) != 2 || tokens[0] != tokens[1] {
				t.Fatalf("status=%d tokens=%v body=%s", w.Code, tokens, w.Body.String())
			}
			if !rejectAgain && w.Header().Get("X-Proxy-Parameter-Adjustments") == "" {
				t.Fatal("missing parameter adjustment header")
			}
		})
	}
}

func TestUnrelated400NeverRemovesPenaltiesOrRetries(t *testing.T) {
	calls := 0
	up := &fakeUpstream{call: func(_ context.Context, _ string, _ any, stream, count bool) (*http.Response, []byte, error) {
		calls++
		return upstreamResponse(400, `{"error":{"message":"Invalid temperature"}}`, false)
	}}
	_, handler := newProxyFixture(t, 2, up)
	w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-3.7-flash-high","presence_penalty":0.2,"messages":[{"role":"user","content":"hello"}]}`, "Authorization", "Bearer test-api")
	if calls != 1 || w.Code != 400 {
		t.Fatal(calls, w.Code, w.Body.String())
	}
}

func TestFinal429LogRetainsStructuredDetails(t *testing.T) {
	raw := `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"2h","api_key":"supersecret"}}]}}`
	up := &fakeUpstream{call: func(_ context.Context, _ string, _ any, stream, count bool) (*http.Response, []byte, error) {
		return upstreamResponse(200, "data: "+raw+"\n\n", true)
	}}
	s, handler := newProxyFixture(t, 1, up)
	if err := s.store.SetSetting("enable_logging", "true"); err != nil {
		t.Fatal(err)
	}
	w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-3.7-flash-high","stream":true,"messages":[{"role":"user","content":"hello"}]}`, "Authorization", "Bearer test-api")
	if w.Code != 429 || w.Header().Get("Retry-After") != "7200" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		logs, err := s.store.ListLogs(20, 0, "", "", false)
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) > 0 {
			detail := logs[0].Error
			if !strings.Contains(detail, "quotaResetDelay") || !strings.Contains(detail, "QUOTA_EXHAUSTED") || !strings.Contains(detail, "attempts=1") || strings.Contains(detail, "supersecret") {
				t.Fatalf("log detail=%s", detail)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("log was not flushed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	states := s.pool.RuntimeStates()
	for _, state := range states {
		if state.CooldownUntil != 0 || state.ModelCooldowns["gemini-3.7-flash-high"] <= time.Now().Unix()+7100 {
			b, _ := json.Marshal(state)
			t.Fatalf("model quota scope lost: %s", b)
		}
	}
}
