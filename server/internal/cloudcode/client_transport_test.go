package cloudcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/convert"
	"github.com/wo/antigravity2api/internal/outbound"
)

const transportSSEFirst = "data: {\"response\":{\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hello \"}]}}]}}\n\n"
const transportSSELast = "data: {\"response\":{\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"world\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2,\"totalTokenCount\":5}}}\n\n"

type transportRequest struct {
	method   string
	path     string
	query    string
	length   int64
	encoding []string
	headers  http.Header
	body     []byte
	err      error
}

func captureTransportRequest(r *http.Request) transportRequest {
	body, err := io.ReadAll(r.Body)
	return transportRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.ContentLength, append([]string(nil), r.TransferEncoding...), r.Header.Clone(), body, err}
}

func newTransportTestClient(t *testing.T, endpoints ...string) *Client {
	t.Helper()
	m := outbound.New()
	t.Cleanup(m.Close)
	return &Client{cfg: config.Config{UserAgent: "Antigravity/1.0 transport-test"}, out: m, endpoints: endpoints}
}

func transportTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func receiveTransportRequest(t *testing.T, requests <-chan transportRequest) transportRequest {
	t.Helper()
	select {
	case got := <-requests:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("local upstream did not receive a request")
		return transportRequest{}
	}
}

func TestCountTokensUsesDedicatedActionAndSanitizedBody(t *testing.T) {
	requests := make(chan transportRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureTransportRequest(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"totalTokens":7}`)
	}))
	t.Cleanup(srv.Close)
	c := newTransportTestClient(t, srv.URL+"/v1internal")
	contents := []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello"}}}}
	system := map[string]any{"parts": []any{map[string]any{"text": "system"}}}
	tools := []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "lookup"}}}}
	input := map[string]any{
		"contents": contents, "systemInstruction": system, "tools": tools,
		"sessionId": "session", "safetySettings": []any{},
		"generationConfig": map[string]any{"maxOutputTokens": 1024}, "toolConfig": map[string]any{},
	}
	resp, data, err := c.CountTokens(transportTestContext(t), "synthetic-token", "claude-sonnet-test", input)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(data) != `{"totalTokens":7}` {
		t.Fatalf("status=%d, body=%s", resp.StatusCode, data)
	}
	got := receiveTransportRequest(t, requests)
	if got.method != http.MethodPost || got.path != "/v1internal:countTokens" || got.query != "" {
		t.Fatalf("count request used incorrect action: %+v", got)
	}
	want, _ := json.Marshal(map[string]any{"request": map[string]any{"contents": contents, "systemInstruction": system, "tools": tools}})
	if string(got.body) != string(want) {
		t.Errorf("count payload=%s, want=%s", got.body, want)
	}
	if got.length != int64(len(want)) || len(got.encoding) != 0 {
		t.Errorf("count framing: length=%d, encoding=%v", got.length, got.encoding)
	}
	if got.headers.Get("X-Goog-User-Project") != "" || got.headers.Get("Authorization") != "Bearer synthetic-token" {
		t.Errorf("unexpected count headers: %v", got.headers)
	}
	if len(requests) != 0 {
		t.Fatal("counting made additional upstream requests")
	}
	if _, ok := input["generationConfig"]; !ok {
		t.Fatal("counting modified the caller's request")
	}
}

func TestGenerateDirectSendsFixedLengthJSON(t *testing.T) {
	requests := make(chan transportRequest, 1)
	const response = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureTransportRequest(r)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	c := newTransportTestClient(t, srv.URL+"/v1internal")
	payload := map[string]any{"project": "actual-project", "model": "gemini-test", "request": map[string]any{"contents": []any{}}}
	resp, data, err := c.GenerateDirect(transportTestContext(t), "synthetic-token", payload)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(data) != response {
		t.Fatalf("response status=%d, body=%s", resp.StatusCode, data)
	}
	got := receiveTransportRequest(t, requests)
	want, _ := json.Marshal(payload)
	if got.method != http.MethodPost || got.path != "/v1internal:generateContent" || got.query != "" || string(got.body) != string(want) {
		t.Errorf("direct generation request changed: %+v", got)
	}
	if got.length != int64(len(want)) || len(got.encoding) != 0 {
		t.Errorf("direct generation must have a fixed length: length=%d, encoding=%v", got.length, got.encoding)
	}
	if got.headers.Get("X-Goog-User-Project") != "" {
		t.Error("generation should not attach billing project header")
	}
}

func TestGenerateAggregatesUpstreamSSEAndPreservesUsage(t *testing.T) {
	requests := make(chan transportRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureTransportRequest(r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, transportSSEFirst)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, transportSSELast)
	}))
	t.Cleanup(srv.Close)
	c := newTransportTestClient(t, srv.URL+"/v1internal")
	resp, data, err := c.Generate(transportTestContext(t), "synthetic-token", map[string]any{"model": "gemini-test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.StatusCode)
	}
	got := receiveTransportRequest(t, requests)
	if got.path != "/v1internal:streamGenerateContent" || got.query != "alt=sse" || got.length != -1 || !reflect.DeepEqual(got.encoding, []string{"chunked"}) {
		t.Errorf("stream generation framing/action changed: %+v", got)
	}
	var aggregated struct {
		Candidates []struct {
			Content struct {
				Parts []struct{ Text string }
			}
			FinishReason string
		}
		UsageMetadata struct{ PromptTokenCount, CandidatesTokenCount, TotalTokenCount int }
	}
	if err := json.Unmarshal(data, &aggregated); err != nil {
		t.Fatal(err)
	}
	if len(aggregated.Candidates) != 1 {
		t.Fatalf("aggregated candidates=%s", data)
	}
	var output strings.Builder
	for _, part := range aggregated.Candidates[0].Content.Parts {
		output.WriteString(part.Text)
	}
	if output.String() != "Hello world" || aggregated.Candidates[0].FinishReason != "STOP" || aggregated.UsageMetadata.PromptTokenCount != 3 || aggregated.UsageMetadata.CandidatesTokenCount != 2 || aggregated.UsageMetadata.TotalTokenCount != 5 {
		t.Errorf("aggregation lost content or usage: %s", data)
	}
}

func TestGenerateStreamingReturnsBeforeUpstreamCompletes(t *testing.T) {
	finish := make(chan struct{})
	var finishOnce sync.Once
	finishUpstream := func() { finishOnce.Do(func() { close(finish) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, transportSSEFirst)
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			_, _ = io.WriteString(w, transportSSELast)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(finishUpstream)
	c := newTransportTestClient(t, srv.URL+"/v1internal")
	type result struct {
		resp *http.Response
		data []byte
		err  error
	}
	done := make(chan result, 1)
	ctx := transportTestContext(t)
	go func() {
		resp, data, err := c.Generate(ctx, "synthetic-token", map[string]any{"model": "gemini-test"}, true)
		done <- result{resp, data, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		defer got.resp.Body.Close()
		if got.data != nil {
			t.Errorf("streaming response was buffered: %s", got.data)
		}
		finishUpstream()
		data, err := io.ReadAll(got.resp.Body)
		if err != nil || string(data) != transportSSEFirst+transportSSELast {
			t.Errorf("caller could not consume stream: body=%s, error=%v", data, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streaming call waited for the complete upstream body")
	}
}

func TestGenerateCancellationStopsIOWithoutEndpointFallback(t *testing.T) {
	for _, flushHeaders := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_headers_%t", flushHeaders), func(t *testing.T) {
			started := make(chan struct{})
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path != "/first:streamGenerateContent" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if flushHeaders {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, transportSSEFirst)
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
			}))
			t.Cleanup(srv.Close)
			c := newTransportTestClient(t, srv.URL+"/first", srv.URL+"/fallback")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() {
				_, _, err := c.Generate(ctx, "synthetic-token", map[string]any{"model": "gemini-test"}, false)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream was not reached")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancellation error=%v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("generation did not stop after cancellation")
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("canceled generation called %d endpoints", got)
			}
		})
	}
}

func TestGeneratePropagatesErrorInsideSuccessfulSSE(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"error\":{\"code\":429,\"message\":\"rate limited\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n")
	}))
	t.Cleanup(srv.Close)
	c := newTransportTestClient(t, srv.URL+"/first", srv.URL+"/fallback")
	resp, data, err := c.Generate(transportTestContext(t), "synthetic-token", map[string]any{"model": "gemini-test"}, false)
	var upstream *convert.UpstreamError
	if !errors.As(err, &upstream) || upstream.Code != 429 {
		t.Fatalf("SSE rate limit was hidden: response=%v, body=%s, error=%v", resp, data, err)
	}
	if resp == nil || resp.StatusCode != 200 || len(data) != 0 || calls.Load() != 1 {
		t.Fatalf("SSE error changed response or retried: response=%v, body=%s, calls=%d", resp, data, calls.Load())
	}
}

func TestCloudCodeEndpointFallbackBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		statuses []int
		wantHits int
		wantCode int
	}{
		{"not_found", []int{404, 200, 200}, 2, 200},
		{"timeout", []int{408, 200, 200}, 2, 200},
		{"server_error", []int{500, 200, 200}, 2, 200},
		{"unavailable", []int{503, 200, 200}, 2, 200},
		{"unauthorized", []int{401, 200, 200}, 1, 401},
		{"forbidden", []int{403, 200, 200}, 1, 403},
		{"rate_limited", []int{429, 200, 200}, 1, 429},
		{"bad_request", []int{400, 200, 200}, 1, 400},
		{"rate_limited_after_fallback", []int{503, 429, 200}, 2, 429},
		{"all_unavailable", []int{500, 503, 502}, 3, 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan transportRequest, 8)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- captureTransportRequest(r)
				index := -1
				for i, prefix := range []string{"/first", "/second", "/third"} {
					if r.URL.Path == prefix+":generateContent" {
						index = i
					}
				}
				if index < 0 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.WriteHeader(tc.statuses[index])
				_, _ = fmt.Fprintf(w, `{"endpoint":%d}`, index)
			}))
			t.Cleanup(srv.Close)
			c := newTransportTestClient(t, srv.URL+"/first", srv.URL+"/second", srv.URL+"/third")
			payload := map[string]any{"project": "actual-project", "model": "gemini-test", "request": map[string]any{"contents": []any{}}}
			wantBody, _ := json.Marshal(payload)
			resp, data, err := c.GenerateDirect(transportTestContext(t), "synthetic-token", payload)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.wantCode || string(data) != fmt.Sprintf(`{"endpoint":%d}`, tc.wantHits-1) {
				t.Errorf("final status=%d body=%s", resp.StatusCode, data)
			}
			if len(requests) != tc.wantHits {
				t.Fatalf("called %d endpoints, want %d", len(requests), tc.wantHits)
			}
			for i := 0; i < tc.wantHits; i++ {
				got := receiveTransportRequest(t, requests)
				wantPath := []string{"/first:generateContent", "/second:generateContent", "/third:generateContent"}[i]
				if got.path != wantPath || got.method != http.MethodPost || string(got.body) != string(wantBody) || got.length != int64(len(wantBody)) {
					t.Errorf("fallback lost request body or changed endpoint order: %+v", got)
				}
			}
		})
	}
}

func TestCloudCodeServiceDisabledRetriesOnceWithoutProjectHeader(t *testing.T) {
	requests := make(chan transportRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- captureTransportRequest(r)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"SERVICE_DISABLED","details":[{"reason":"SERVICE_DISABLED"}]}}`)
	}))
	t.Cleanup(srv.Close)
	c := newTransportTestClient(t, srv.URL+"/first", srv.URL+"/second")
	resp, _, err := c.doJSON(transportTestContext(t), "fetchAvailableModels", "synthetic-token", map[string]any{"project": "actual-project"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || len(requests) != 2 {
		t.Fatalf("SERVICE_DISABLED retry exceeded its boundary: status=%d calls=%d", resp.StatusCode, len(requests))
	}
	for i, project := range []string{"actual-project", ""} {
		got := receiveTransportRequest(t, requests)
		if got.path != "/first:fetchAvailableModels" || got.headers.Get("X-Goog-User-Project") != project {
			t.Errorf("attempt %d header/endpoint=%+v", i+1, got)
		}
	}
}
