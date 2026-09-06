package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestConcurrentProxyReservations(t *testing.T) {
	var mu sync.Mutex
	active, peak, calls := map[string]int{}, map[string]int{}, map[string]int{}
	upstream := &fakeUpstream{call: func(ctx context.Context, token string, payload any, stream, count bool) (*http.Response, []byte, error) {
		mu.Lock()
		active[token]++
		calls[token]++
		peak[token] = max(peak[token], active[token])
		mu.Unlock()
		defer func() { mu.Lock(); active[token]--; mu.Unlock() }()
		if err := sleepContext(ctx, 5*time.Millisecond); err != nil {
			return nil, nil, err
		}
		return upstreamResponse(200, successfulGemini, false)
	}}
	_, handler := newProxyFixture(t, 10, upstream)
	var wg sync.WaitGroup
	failures := make(chan string, 120)
	for i := 0; i < 120; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"conversation %d"}]}`, i)
			w := request(handler, "POST", "/v1/chat/completions", body, "Authorization", "Bearer test-api")
			if w.Code != 200 {
				failures <- fmt.Sprintf("%d: %s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 10 {
		t.Fatalf("used %d of 10 accounts: %v", len(calls), calls)
	}
	for token, n := range peak {
		if n > 4 {
			t.Errorf("account %s exceeded cap: %d", token, n)
		}
		if active[token] != 0 {
			t.Errorf("leaked active slot %s: %d", token, active[token])
		}
	}
	t.Logf("120 requests across 10 accounts: calls=%v peak=%v", calls, peak)
}

func TestDiagnosticsCountsRecovered429(t *testing.T) {
	calls := 0
	up := &fakeUpstream{call: func(_ context.Context, _ string, _ any, stream, count bool) (*http.Response, []byte, error) {
		calls++
		if calls == 1 {
			resp, data, err := upstreamResponse(429, `{"error":{"message":"temporary rate limit"}}`, false)
			resp.Header.Set("Retry-After", "0.01")
			return resp, data, err
		}
		return upstreamResponse(200, successfulGemini, false)
	}}
	s, handler := newProxyFixture(t, 1, up)
	w := request(handler, "POST", "/v1/chat/completions", `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`, "Authorization", "Bearer test-api")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.upstream429.Load() != 1 || s.upstreamAttempts.Load() != 2 {
		t.Fatalf("429=%d attempts=%d", s.upstream429.Load(), s.upstreamAttempts.Load())
	}
	if w := request(handler, "GET", "/api/diagnostics", "", "Authorization", "Bearer test-api"); w.Code != 401 {
		t.Fatal("diagnostics exposed to API credential")
	}
	if w := request(handler, "GET", "/api/diagnostics", "", "Authorization", "Bearer test-admin"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAdminShowsAccountAndModelCooldowns(t *testing.T) {
	s, handler := newProxyFixture(t, 1, successUpstream())
	accounts, err := s.store.ListAccounts("")
	if err != nil {
		t.Fatal(err)
	}
	id := accounts[0].ID
	s.pool.MarkLimited(id, "gemini-2.5-flash", time.Now().Add(time.Minute))
	s.pool.MarkLimited(id, "", time.Now().Add(30*time.Second))
	w := request(handler, "GET", "/api/accounts", "", "Authorization", "Bearer test-admin")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			ID, Status     string
			ModelCooldowns map[string]int64 `json:"model_cooldowns"`
			RefreshToken   string           `json:"refresh_token"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Status != "rate_limited" || body.Items[0].ModelCooldowns["gemini-2.5-flash"] <= time.Now().Unix() || body.Items[0].RefreshToken != "" {
		t.Fatalf("body=%s", w.Body.String())
	}
	s.pool.ClearLimited(id, "")
	w = request(handler, "GET", "/api/accounts/"+id, "", "Authorization", "Bearer test-admin")
	var account map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	if account["status"] != "active" || account["model_cooldowns"] == nil {
		t.Fatalf("model cooldown must not disable entire account: %s", w.Body.String())
	}
	w = request(handler, "GET", "/api/dashboard", "", "Authorization", "Bearer test-admin")
	var dashboard map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || dashboard["rate_limited"] != float64(1) {
		t.Fatalf("dashboard=%s", w.Body.String())
	}
}
