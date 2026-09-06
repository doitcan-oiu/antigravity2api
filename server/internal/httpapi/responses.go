package httpapi

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wo/antigravity2api/internal/convert"
)

type responseHistoryEntry struct {
	key     string
	input   []byte
	expires time.Time
}
type responseHistory struct {
	mu                        sync.Mutex
	items                     map[string]*list.Element
	lru                       *list.List
	capacity, maxBytes, bytes int
	ttl                       time.Duration
}

func newResponseHistory(capacity, maxBytes int, ttl time.Duration) *responseHistory {
	return &responseHistory{items: make(map[string]*list.Element), lru: list.New(), capacity: capacity, maxBytes: maxBytes, ttl: ttl}
}
func (h *responseHistory) remove(e *list.Element) {
	entry := e.Value.(responseHistoryEntry)
	delete(h.items, entry.key)
	h.bytes -= len(entry.input)
	h.lru.Remove(e)
}
func (h *responseHistory) get(key string) ([]any, bool) {
	h.mu.Lock()
	e, ok := h.items[key]
	if !ok {
		h.mu.Unlock()
		return nil, false
	}
	entry := e.Value.(responseHistoryEntry)
	if time.Now().After(entry.expires) {
		h.remove(e)
		h.mu.Unlock()
		return nil, false
	}
	h.lru.MoveToFront(e)
	h.mu.Unlock()
	var input []any
	if json.Unmarshal(entry.input, &input) != nil {
		return nil, false
	}
	return input, true
}
func (h *responseHistory) put(key string, input []any) {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 2<<20 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.items[key]; e != nil {
		h.remove(e)
	}
	for e := h.lru.Back(); e != nil; {
		previous := e.Prev()
		if time.Now().After(e.Value.(responseHistoryEntry).expires) {
			h.remove(e)
		}
		e = previous
	}
	for len(h.items) >= h.capacity || h.bytes+len(raw) > h.maxBytes {
		e := h.lru.Back()
		if e == nil {
			return
		}
		h.remove(e)
	}
	h.items[key] = h.lru.PushFront(responseHistoryEntry{key: key, input: raw, expires: time.Now().Add(h.ttl)})
	h.bytes += len(raw)
}

func responseOwner(r *http.Request) string {
	key := bearer(r)
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	if key == "" {
		key = r.Header.Get("x-goog-api-key")
	}
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
func responseInput(input any) []any {
	if input == nil {
		return nil
	}
	if list, ok := input.([]any); ok {
		return list
	}
	return []any{map[string]any{"role": "user", "content": input}}
}

func (s *Server) openaiResponses(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		writeRequestError(w, "responses", err)
		return
	}
	var req convert.OpenAIRequest
	body, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(body, &req) != nil {
		writeProxyError(w, "responses", 400, "invalid Responses request")
		return
	}
	var previous string
	if v := raw["previous_response_id"]; len(v) > 0 && string(v) != "null" {
		if json.Unmarshal(v, &previous) != nil {
			writeProxyError(w, "responses", 400, "previous_response_id must be a string")
			return
		}
	}
	var background bool
	_ = json.Unmarshal(raw["background"], &background)
	if background {
		writeProxyError(w, "responses", 400, "background responses are not supported")
		return
	}
	storeResponse := true
	if v := raw["store"]; len(v) > 0 {
		if json.Unmarshal(v, &storeResponse) != nil {
			writeProxyError(w, "responses", 400, "store must be a boolean")
			return
		}
	}
	input := responseInput(req.Input)
	owner := responseOwner(r)
	if previous != "" {
		prior, ok := s.history.get(owner + ":" + previous)
		if !ok {
			writeProxyError(w, "responses", 404, "previous_response_id was not found or has expired; send the complete input history")
			return
		}
		input = append(prior, input...)
	}
	if len(input) == 0 && len(req.Messages) > 0 {
		b, _ := json.Marshal(req.Messages)
		_ = json.Unmarshal(b, &input)
	}
	if len(input) == 0 {
		writeProxyError(w, "responses", 400, "input is required")
		return
	}
	if b, _ := json.Marshal(input); int64(len(b)) > maxRequestBytes {
		writeProxyError(w, "responses", 413, "combined response history exceeds request limit")
		return
	}
	req.Input = input
	req.Messages = nil
	custom := customToolNames(req.Tools, "")
	record := func(response map[string]any) {
		if !storeResponse {
			return
		}
		id, _ := response["id"].(string)
		status, _ := response["status"].(string)
		if id == "" || status != "completed" {
			return
		}
		output, _ := response["output"].([]any)
		history := append(append([]any(nil), input...), output...)
		s.history.put(owner+":"+id, history)
	}
	s.serveOpenAI(w, r, req, "responses", func(model string, raw []byte) ([]byte, error) {
		out, err := convert.GeminiToResponsesWithTools(model, raw, custom)
		if err == nil {
			var response map[string]any
			if json.Unmarshal(out, &response) == nil {
				record(response)
			}
		}
		return out, err
	}, func(dst io.Writer, model string, src io.Reader) (convert.StreamStats, error) {
		return convert.WriteResponsesSSEWithTools(&responseRecorder{dst: dst, record: record}, model, src, custom)
	})
}

func customToolNames(tools []any, prefix string) map[string]bool {
	names := map[string]bool{}
	for _, tool := range tools {
		m := convert.AsMap(tool)
		name := convert.AsString(m["name"])
		if convert.AsString(m["type"]) == "namespace" {
			for n := range customToolNames(convert.AsSlice(m["tools"]), prefix+name+"__") {
				names[n] = true
			}
		}
		if convert.AsString(m["type"]) == "custom" && name != "" {
			names[prefix+name] = true
		}
	}
	return names
}

// Record completion before delivering it: a client can immediately resume using the response ID.
type responseRecorder struct {
	dst      io.Writer
	pending  []byte
	record   func(map[string]any)
	disabled bool
}

func (w *responseRecorder) Write(p []byte) (int, error) {
	if w.disabled || len(p) == 0 {
		return w.dst.Write(p)
	}
	if len(w.pending)+len(p) > 2<<20 {
		w.pending = nil
		w.disabled = true
		return w.dst.Write(p)
	}
	w.pending = append(w.pending, p...)
	for {
		end := bytes.Index(w.pending, []byte("\n\n"))
		if end < 0 {
			break
		}
		event := w.pending[:end]
		var data []byte
		for _, line := range bytes.Split(event, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				data = append(data, bytes.TrimSpace(line[5:])...)
			}
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) == nil && payload["type"] == "response.completed" {
			if response, ok := payload["response"].(map[string]any); ok {
				w.record(response)
			}
		}
		w.pending = w.pending[end+2:]
	}
	return w.dst.Write(p)
}

func (s *Server) openaiLegacy(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		writeRequestError(w, "openai", err)
		return
	}
	var req convert.OpenAIRequest
	body, _ := json.Marshal(raw)
	if err := json.Unmarshal(body, &req); err != nil {
		writeRequestError(w, "openai", err)
		return
	}
	var prompt string
	if err := json.Unmarshal(raw["prompt"], &prompt); err != nil {
		writeProxyError(w, "openai", 400, "prompt must be a string; batched or tokenized prompts are not supported")
		return
	}
	for _, field := range []string{"echo", "suffix", "logprobs", "best_of"} {
		v := strings.TrimSpace(string(raw[field]))
		allowed := v == "" || v == "null" || (field == "echo" && v == "false") || (field == "suffix" && v == `""`) || (field == "best_of" && v == "1")
		if !allowed {
			writeProxyError(w, "openai", 400, fmt.Sprintf("%s is not supported", field))
			return
		}
	}
	req.Messages = []convert.OpenAIMessage{{Role: "user", Content: prompt}}
	if strings.TrimSpace(req.Model) == "" {
		writeProxyError(w, "openai", 400, "model is required")
		return
	}
	s.serveOpenAI(w, r, req, "openai", convert.GeminiToLegacy, convert.WriteLegacySSE)
}
