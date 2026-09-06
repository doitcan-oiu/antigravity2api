package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const maxRequestBytes int64 = 32 << 20
const proxyRevision = "2026-09-07.2"

func (s *Server) initRuntime() {
	s.runtimeOnce.Do(func() {
		s.cfg = s.cfg.WithDefaults()
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.admission = make(chan struct{}, s.cfg.MaxConcurrentRequests)
		s.pending = make(chan struct{}, s.cfg.MaxConcurrentRequests*2)
		s.history = newResponseHistory(256, 32<<20, 30*time.Minute)
		if s.wait == nil {
			s.wait = sleepContext
		}
	})
}

func (s *Server) Close() {
	s.initRuntime()
	s.cancel()
	if s.pool != nil {
		s.pool.Close()
	}
	if s.out != nil {
		s.out.Close()
	}
}

func (s *Server) limitProxy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol := "openai"
		if strings.HasPrefix(r.URL.Path, "/v1beta/") {
			protocol = "gemini"
		} else if strings.HasPrefix(r.URL.Path, "/v1/messages") {
			protocol = "claude"
		}
		select {
		case s.pending <- struct{}{}:
			defer func() { <-s.pending }()
		default:
			w.Header().Set("Retry-After", "1")
			s.rejectedRequests.Add(1)
			writeProxyError(w, protocol, 503, "server request queue is full")
			return
		}
		// Bound queue residence as well as active requests; every wait is cancelable.
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AdmissionTimeout)
		defer cancel()
		select {
		case s.admission <- struct{}{}:
			defer func() { <-s.admission }()
		case <-ctx.Done():
			w.Header().Set("Retry-After", "1")
			s.rejectedRequests.Add(1)
			writeProxyError(w, protocol, 503, "server is busy; retry later")
			return
		case <-s.ctx.Done():
			s.rejectedRequests.Add(1)
			writeProxyError(w, protocol, 503, "server is shutting down")
			return
		}
		ctx, done := context.WithCancel(r.Context())
		stop := context.AfterFunc(s.ctx, done)
		defer func() { stop(); done() }()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeRequestError(w http.ResponseWriter, protocol string, err error) {
	status := http.StatusBadRequest
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		status = http.StatusRequestEntityTooLarge
	}
	writeProxyError(w, protocol, status, err.Error())
}

func writeProxyError(w http.ResponseWriter, protocol string, status int, message string) {
	kind := "api_error"
	switch status {
	case 400, 413, 422:
		kind = "invalid_request_error"
	case 401:
		kind = "authentication_error"
	case 403:
		kind = "permission_error"
	case 404:
		kind = "not_found_error"
	case 429:
		kind = "rate_limit_error"
	case 503, 529:
		kind = "overloaded_error"
	}
	err := map[string]any{"type": kind, "message": message}
	if protocol == "gemini" {
		err["code"] = status
		err["status"] = map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND", 429: "RESOURCE_EXHAUSTED", 503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED"}[status]
	}
	response := map[string]any{"error": err}
	if protocol == "claude" {
		response["type"] = "error"
	}
	writeJSON(w, status, response)
}

// Session identity belongs to the conversation, never to an upstream account.
func requestSession(r *http.Request, model string, input any) string {
	for _, name := range []string{"X-Session-ID", "Session-ID", "X-Conversation-ID"} {
		if value := r.Header.Get(name); value != "" {
			return conversationKey(r, model, value)
		}
	}
	// Only hash the stable initial message, so appending tool turns preserves affinity.
	raw, _ := json.Marshal(input)
	var messages []json.RawMessage
	if json.Unmarshal(raw, &messages) == nil && len(messages) > 0 {
		for _, message := range messages {
			var item struct {
				Role string `json:"role"`
			}
			if json.Unmarshal(message, &item) == nil && item.Role == "user" {
				raw = message
				break
			}
		}
	}
	return conversationKey(r, model, string(raw))
}

func conversationKey(r *http.Request, model, value string) string {
	return hashSession(responseOwner(r)+":"+model, value)
}

func hashSession(model, value string) string {
	h := sha256.Sum256([]byte(model + "\x00" + value))
	return hex.EncodeToString(h[:16])
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Counters are per process. Queued requests include only those waiting for admission.
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	active, pending := len(s.admission), len(s.pending)
	writeJSON(w, http.StatusOK, map[string]any{
		"proxy_revision":  proxyRevision,
		"active_requests": active, "queued_requests": max(0, pending-active),
		"max_concurrent_requests": cap(s.admission), "max_concurrent_per_account": s.cfg.MaxConcurrentPerAccount,
		"upstream_attempts": s.upstreamAttempts.Load(), "upstream_429": s.upstream429.Load(),
		"rejected_requests": s.rejectedRequests.Load(), "store": s.store.Diagnostics(),
		"accounts": s.pool.RuntimeStates(),
	})
}
