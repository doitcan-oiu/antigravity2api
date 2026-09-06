package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wo/antigravity2api/internal/cloudcode"
	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/oauth"
	"github.com/wo/antigravity2api/internal/outbound"
	"github.com/wo/antigravity2api/internal/pool"
	"github.com/wo/antigravity2api/internal/store"
)

const successfulGemini = `{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}}`

type fakeUpstream struct {
	call func(context.Context, string, any, bool, bool) (*http.Response, []byte, error)
}

func (f *fakeUpstream) Generate(ctx context.Context, token string, payload any, stream bool) (*http.Response, []byte, error) {
	return f.call(ctx, token, payload, stream, false)
}
func (f *fakeUpstream) GenerateDirect(ctx context.Context, token string, payload any) (*http.Response, []byte, error) {
	return f.call(ctx, token, payload, false, false)
}
func (f *fakeUpstream) CountTokens(ctx context.Context, token, model string, payload any) (*http.Response, []byte, error) {
	return f.call(ctx, token, payload, false, true)
}

func upstreamResponse(status int, body string, stream bool) (*http.Response, []byte, error) {
	resp := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	if stream {
		return resp, nil, nil
	}
	return resp, []byte(body), nil
}
func newProxyFixture(t *testing.T, accounts int, up *fakeUpstream) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: dir, AdminToken: "test-admin", APIKey: "test-api", RequestTimeout: 2 * time.Second}.WithDefaults()
	_ = st.SetSetting("enable_logging", "false")
	batch, err := st.CreateBatch("test", "", 30, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < accounts; i++ {
		a := &models.Account{BatchID: batch.ID, Email: "test@example.com", RefreshToken: string(rune('a' + i)), AccessToken: string(rune('a' + i)), ProjectID: "project", ExpiryTimestamp: time.Now().Add(time.Hour).Unix(), CreatedAt: int64(i + 1)}
		if err := st.InsertAccount(a); err != nil {
			t.Fatal(err)
		}
	}
	out := outbound.New()
	oa := oauth.New(cfg, out)
	cc := cloudcode.New(cfg, out)
	s := &Server{cfg: cfg, store: st, oauth: oa, cc: up, out: out, pool: pool.New(cfg, st, oa, cc), wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}
	t.Cleanup(func() {
		s.Close()
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, s.Router()
}
func successUpstream() *fakeUpstream {
	return &fakeUpstream{call: func(_ context.Context, _ string, _ any, stream, count bool) (*http.Response, []byte, error) {
		if count {
			return upstreamResponse(200, `{"response":{"totalTokens":7}}`, false)
		}
		if stream {
			return upstreamResponse(200, "data: "+successfulGemini+"\n\n", true)
		}
		return upstreamResponse(200, successfulGemini, false)
	}}
}
func request(handler http.Handler, method, path, body, keyHeader, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(keyHeader, key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestCredentialsAreSeparated(t *testing.T) {
	_, handler := newProxyFixture(t, 1, successUpstream())
	for _, tc := range []struct {
		path, header, key string
		want              int
	}{
		{"/api/accounts", "Authorization", "Bearer test-api", 401},
		{"/api/accounts", "X-Admin-Token", "test-admin", 200},
		{"/v1/models", "Authorization", "Bearer test-admin", 401},
		{"/v1/models", "Authorization", "Bearer test-api", 200},
		{"/v1beta/models", "x-goog-api-key", "test-api", 200},
		{"/v1/models", "x-api-key", "", 401},
	} {
		t.Run(tc.path+tc.header+tc.key, func(t *testing.T) {
			w := request(handler, "GET", tc.path, "", tc.header, tc.key)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestCountTokensNeverGenerates(t *testing.T) {
	for _, tc := range []struct{ path, body, field string }{
		{"/v1beta/models/gemini-2.5-flash:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, "totalTokens"},
		{"/v1beta/models/gemini-2.5-flash/countTokens", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, "totalTokens"},
		{"/v1/messages/count_tokens", `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`, "input_tokens"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			calls := 0
			up := successUpstream()
			base := up.call
			up.call = func(ctx context.Context, token string, payload any, stream, count bool) (*http.Response, []byte, error) {
				calls++
				if !count || stream {
					t.Error("countTokens called generation")
				}
				return base(ctx, token, payload, stream, count)
			}
			_, handler := newProxyFixture(t, 1, up)
			w := request(handler, "POST", tc.path, tc.body, "Authorization", "Bearer test-api")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"`+tc.field+`":7`) || calls != 1 {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body, calls)
			}
		})
	}
}

func TestResponsesProtocolAndHistory(t *testing.T) {
	var payloads []string
	up := successUpstream()
	base := up.call
	up.call = func(ctx context.Context, token string, payload any, stream, count bool) (*http.Response, []byte, error) {
		b, _ := json.Marshal(payload)
		payloads = append(payloads, string(b))
		return base(ctx, token, payload, stream, count)
	}
	_, handler := newProxyFixture(t, 1, up)
	w := request(handler, "POST", "/v1/responses", `{"model":"gemini-2.5-flash","input":"first question"}`, "Authorization", "Bearer test-api")
	var response map[string]any
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response["object"] != "response" || response["status"] != "completed" {
		t.Fatalf("bad response: %d %s", w.Code, w.Body)
	}
	input, _ := json.Marshal(map[string]any{"model": "gemini-2.5-flash", "input": "second question", "previous_response_id": response["id"]})
	w = request(handler, "POST", "/v1/responses", string(input), "Authorization", "Bearer test-api")
	if w.Code != 200 || len(payloads) != 2 || !strings.Contains(payloads[1], "first question") || !strings.Contains(payloads[1], "hello") || !strings.Contains(payloads[1], "second question") {
		t.Fatalf("history not replayed: %d %s %v", w.Code, w.Body, payloads)
	}
	w = request(handler, "POST", "/v1/responses", `{"model":"gemini-2.5-flash","input":"hi","stream":true}`, "Authorization", "Bearer test-api")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "response.completed") || strings.Contains(w.Body.String(), "chat.completion.chunk") {
		t.Fatalf("bad stream: %d %s", w.Code, w.Body)
	}
}

func TestFinal429SurvivesAccountExhaustion(t *testing.T) {
	calls := 0
	up := &fakeUpstream{call: func(_ context.Context, _ string, _ any, stream, count bool) (*http.Response, []byte, error) {
		calls++
		return upstreamResponse(429, `{"error":{"message":"quota exhausted marker"}}`, stream)
	}}
	_, handler := newProxyFixture(t, 2, up)
	w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`, "Authorization", "Bearer test-api")
	if w.Code != 429 || !strings.Contains(w.Body.String(), "quota exhausted marker") || calls != 2 {
		t.Fatalf("%d %s calls=%d", w.Code, w.Body, calls)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry header")
	}
}

func TestShort429RetriesSameAccountOnce(t *testing.T) {
	var tokens []string
	up := &fakeUpstream{call: func(_ context.Context, token string, _ any, stream, count bool) (*http.Response, []byte, error) {
		tokens = append(tokens, token)
		if len(tokens) == 1 {
			resp, body, err := upstreamResponse(429, `{"error":{"message":"temporarily limited"}}`, stream)
			resp.Header.Set("Retry-After", "0")
			return resp, body, err
		}
		return upstreamResponse(200, successfulGemini, false)
	}}
	_, handler := newProxyFixture(t, 2, up)
	w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`, "Authorization", "Bearer test-api")
	if w.Code != 200 || len(tokens) != 2 || tokens[0] != tokens[1] {
		t.Fatalf("%d %s tokens=%v", w.Code, w.Body, tokens)
	}
}

func TestErrorFirstFrameDoesNotCommitSuccess(t *testing.T) {
	up := &fakeUpstream{call: func(_ context.Context, _ string, _ any, stream, count bool) (*http.Response, []byte, error) {
		return upstreamResponse(200, "data: {\"error\":{\"code\":429,\"message\":\"quota exhausted marker\"}}\n\n", true)
	}}
	_, handler := newProxyFixture(t, 1, up)
	w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`, "Authorization", "Bearer test-api")
	if w.Code != 429 || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestInvalidJSONAndActionsDoNotCallUpstream(t *testing.T) {
	up := &fakeUpstream{call: func(context.Context, string, any, bool, bool) (*http.Response, []byte, error) {
		t.Fatal("unexpected upstream request")
		return nil, nil, nil
	}}
	_, handler := newProxyFixture(t, 1, up)
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gemini-2.5-flash","messages":[]} {}`},
		{"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1beta/models/gemini-2.5-flash:unknown", `{"contents":[]}`},
		{"/v1/responses", `{"model":"gemini-2.5-flash","input":"hi","previous_response_id":"missing"}`},
	} {
		w := request(handler, "POST", tc.path, tc.body, "Authorization", "Bearer test-api")
		if w.Code < 400 {
			t.Fatalf("accepted %s", tc.body)
		}
	}
}

func TestAdmissionBoundsActiveAndQueuedRequests(t *testing.T) {
	s := &Server{cfg: config.Config{MaxConcurrentRequests: 2, AdmissionTimeout: time.Second}}
	s.initRuntime()
	defer s.Close()
	var active, maxActive atomic.Int32
	entered := make(chan struct{}, 4)
	finish := make(chan struct{})
	h := s.limitProxy(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
		}
		entered <- struct{}{}
		<-finish
		active.Add(-1)
	}))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil)) }()
	}
	<-entered
	<-entered
	deadline := time.Now().Add(time.Second)
	for len(s.pending) < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 503 {
		t.Errorf("unbounded queue: %d", w.Code)
	}
	close(finish)
	wg.Wait()
	if maxActive.Load() != 2 {
		t.Fatalf("active peak=%d", maxActive.Load())
	}
}
