package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wo/antigravity2api/internal/models"
)

type runtimeObserverWriter func([]byte) (int, error)

func (f runtimeObserverWriter) Write(p []byte) (int, error) { return f(p) }

func TestResponseRecorderCachesBeforeCompletionDelivery(t *testing.T) {
	frame := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":\"ready\"}]}}\n\n")
	for _, fragmented := range []bool{false, true} {
		name := "whole_event"
		if fragmented {
			name = "fragmented_event"
		}
		t.Run(name, func(t *testing.T) {
			history := newResponseHistory(4, 1<<20, time.Minute)
			var delivered bytes.Buffer
			records := 0
			recorder := &responseRecorder{record: func(response map[string]any) {
				records++
				output, _ := response["output"].([]any)
				history.put("owner:"+response["id"].(string), output)
			}}
			recorder.dst = runtimeObserverWriter(func(p []byte) (int, error) {
				delivered.Write(p)
				if bytes.HasSuffix(delivered.Bytes(), []byte("\n\n")) {
					cached, ok := history.get("owner:resp_test")
					if !ok || len(cached) != 1 {
						t.Fatal("client observed completed event before history was cached")
					}
				}
				return len(p), nil
			})
			chunks := [][]byte{frame}
			if fragmented {
				chunks = [][]byte{frame[:19], frame[19 : len(frame)-1], frame[len(frame)-1:]}
			}
			for _, chunk := range chunks {
				n, err := recorder.Write(chunk)
				if err != nil || n != len(chunk) {
					t.Fatalf("write=%d %v", n, err)
				}
			}
			if records != 1 || !bytes.Equal(delivered.Bytes(), frame) {
				t.Fatalf("records=%d, bytes delivered changed", records)
			}
		})
	}
}
func TestResponseRecorderDoesNotCacheIncompleteOrOversizedEvents(t *testing.T) {
	records := 0
	recorder := &responseRecorder{dst: io.Discard, record: func(map[string]any) { records++ }}
	incomplete := []byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_partial\",\"status\":\"incomplete\"}}\n\n")
	if _, err := recorder.Write(incomplete); err != nil {
		t.Fatal(err)
	}
	oversized := []byte("data: " + strings.Repeat("x", 2<<20) + "\n\n")
	if _, err := recorder.Write(oversized); err != nil {
		t.Fatal(err)
	}
	if records != 0 || !recorder.disabled || len(recorder.pending) != 0 {
		t.Fatal("optional history recorder failed to enforce its bound")
	}
}
func TestRequestSessionTracksUserConversationAndOwner(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer owner-a")
	conversation := func(system, user string) []any {
		return []any{map[string]any{"role": "system", "content": system}, map[string]any{"role": "user", "content": user}}
	}
	original := conversation("system a", "first question")
	base := requestSession(req, "gemini-2.5-flash", original)
	if base != requestSession(req, "gemini-2.5-flash", conversation("different system", "first question")) {
		t.Fatal("system update changed user conversation affinity")
	}
	if base == requestSession(req, "gemini-2.5-flash", conversation("system a", "different question")) {
		t.Fatal("distinct initial user messages shared a session")
	}
	continued := append(append([]any(nil), original...), map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_one", "function": map[string]any{"name": "read", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "call_one", "content": "file"}, map[string]any{"role": "user", "content": "continue"})
	if base != requestSession(req, "gemini-2.5-flash", continued) {
		t.Fatal("appending a tool round changed affinity")
	}
	other := req.Clone(req.Context())
	other.Header.Set("Authorization", "Bearer owner-b")
	if base == requestSession(other, "gemini-2.5-flash", original) {
		t.Fatal("conversation affinity crossed API credential owners")
	}
	req.Header.Set("X-Session-ID", "explicit-session")
	other.Header.Set("X-Session-ID", "explicit-session")
	if requestSession(req, "gemini-2.5-flash", nil) == requestSession(other, "gemini-2.5-flash", nil) {
		t.Fatal("explicit session bypassed owner isolation")
	}
	if requestSession(req, "gemini-2.5-flash", original) != requestSession(req, "gemini-2.5-flash", continued) {
		t.Fatal("explicit session was unstable")
	}
}
func TestSleepContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delay returned %v", err)
	}
	if err := sleepContext(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("zero delay lost cancellation: %v", err)
	}
	if err := sleepContext(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- sleepContext(ctx, time.Hour) }()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("running delay returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop retry delay")
	}
}

type runtimeTrackedBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *runtimeTrackedBody) Close() error { b.closed.Store(true); return nil }
func TestProxyConverterPanicReleasesBodyAndAccountSlot(t *testing.T) {
	body := &runtimeTrackedBody{Reader: strings.NewReader(successfulGemini)}
	up := &fakeUpstream{call: func(context.Context, string, any, bool, bool) (*http.Response, []byte, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, []byte(successfulGemini), nil
	}}
	server, _ := newProxyFixture(t, 1, up)
	plan := proxyPlan{protocol: "openai", model: "gemini-2.5-flash", target: "gemini-2.5-flash", session: "panic-session", build: func(*models.Account, string) (any, error) { return map[string]any{}, nil }, toJSON: func(string, []byte) ([]byte, error) { panic("converter regression probe") }}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		server.proxy(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), plan)
	}()
	if recovered != "converter regression probe" {
		t.Fatalf("probe did not reach converter: %v", recovered)
	}
	if !body.closed.Load() {
		t.Fatal("converter panic leaked the upstream body")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	// Acquire the entire configured capacity, not merely one slot: otherwise a
	// single leaked reservation could be hidden by the remaining free slots.
	releases := []func(){}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < server.cfg.MaxConcurrentPerAccount; i++ {
		account, release, err := server.pool.Acquire(ctx, "gemini-2.5-flash", "next-session", nil)
		if err != nil || account == nil {
			t.Fatalf("slot %d unavailable after converter panic: %v", i, err)
		}
		releases = append(releases, release)
	}
}

func TestResponseRecorderCacheEntryIsReadableJSON(t *testing.T) {
	history := newResponseHistory(2, 1<<20, time.Minute)
	recorder := &responseRecorder{dst: io.Discard, record: func(response map[string]any) {
		output, _ := response["output"].([]any)
		history.put("owner:"+response["id"].(string), output)
	}}
	event := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_tool", "status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": "call_stable", "name": "read", "arguments": "{\"path\":\"a.go\"}"}}}}
	encoded, _ := json.Marshal(event)
	if _, err := recorder.Write(append(append([]byte("data: "), encoded...), []byte("\n\n")...)); err != nil {
		t.Fatal(err)
	}
	cached, ok := history.get("owner:resp_tool")
	if !ok || len(cached) != 1 {
		t.Fatal("missing tool history")
	}
	call := cached[0].(map[string]any)
	if call["call_id"] != "call_stable" || call["arguments"] != "{\"path\":\"a.go\"}" {
		t.Fatalf("cached tool identity/arguments changed: %#v", call)
	}
}
