package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/wo/antigravity2api/internal/convert"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/pool"
)

type toJSONFn func(string, []byte) ([]byte, error)
type toSSEFn func(io.Writer, string, io.Reader) (convert.StreamStats, error)
type proxyPlan struct {
	protocol, model, target, session string
	mixed, stream, count, direct     bool
	contentType                      string
	build                            func(*models.Account, string) (any, error)
	toJSON                           toJSONFn
	toSSE                            toSSEFn
}

func (s *Server) openaiModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"object": "list", "data": s.modelCatalog()})
}

func (s *Server) openaiChat(w http.ResponseWriter, r *http.Request) {
	var req convert.OpenAIRequest
	if err := readJSON(r, &req); err != nil {
		writeRequestError(w, "openai", err)
		return
	}
	s.serveOpenAI(w, r, req, "openai", func(model string, raw []byte) ([]byte, error) {
		return convert.GeminiToOpenAI(model, raw, "chatcmpl-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	}, convert.WriteOpenAISSE)
}

func (s *Server) serveOpenAI(w http.ResponseWriter, r *http.Request, req convert.OpenAIRequest, protocol string, toJSON toJSONFn, toSSE toSSEFn) {
	if strings.TrimSpace(req.Model) == "" {
		writeProxyError(w, protocol, 400, "model is required")
		return
	}
	if len(req.Messages) == 0 && req.Input == nil {
		writeProxyError(w, protocol, 400, "messages or input is required")
		return
	}
	if err := validateOpenAIRequest(req); err != nil {
		writeRequestError(w, protocol, err)
		return
	}
	original := req.Model
	used, mixed := s.routeModel(original)
	req.Model = used
	var budget *int
	effort := req.ReasoningEffort
	thinking := req.Thinking
	if thinking == nil {
		thinking = req.Reasoning
	}
	if thinking != nil {
		budget = thinking.BudgetTokens
		if thinking.Effort != "" {
			effort = thinking.Effort
		}
	}
	resolved := convert.ResolveModel(used, budget, effort)
	input := any(req.Messages)
	if req.Input != nil {
		input = req.Input
	}
	session := requestSession(r, original, input)
	if req.SessionID != "" {
		session = conversationKey(r, original, req.SessionID)
	}
	s.proxy(w, r, proxyPlan{protocol: protocol, model: original, target: resolved.Model, session: session, mixed: mixed, stream: req.Stream,
		build: func(a *models.Account, final string) (any, error) {
			outer, _, _ := convert.OpenAIToGeminiWithModel(req, a.ProjectID, a.Email, a.ID, final)
			return setSession(outer, session), nil
		}, toJSON: toJSON, toSSE: toSSE})
}

func (s *Server) claudeMessages(w http.ResponseWriter, r *http.Request) {
	s.serveClaude(w, r, false)
}
func (s *Server) claudeCountTokens(w http.ResponseWriter, r *http.Request) {
	s.serveClaude(w, r, true)
}
func (s *Server) serveClaude(w http.ResponseWriter, r *http.Request, count bool) {
	var req convert.ClaudeRequest
	if err := readJSON(r, &req); err != nil {
		writeRequestError(w, "claude", err)
		return
	}
	if strings.TrimSpace(req.Model) == "" || len(req.Messages) == 0 {
		writeProxyError(w, "claude", 400, "model and messages are required")
		return
	}
	if err := validateClaudeRequest(req); err != nil {
		writeRequestError(w, "claude", err)
		return
	}
	original := req.Model
	used, mixed := s.routeModel(original)
	req.Model = used
	var budget *int
	effort := ""
	if req.Thinking != nil {
		budget = req.Thinking.BudgetTokens
		effort = req.Thinking.Effort
	}
	if value := convert.AsString(convert.GetPath(req.OutputConfig, "effort")); value != "" {
		effort = value
	}
	target := convert.ResolveModel(used, budget, effort).Model
	session := requestSession(r, original, req.Messages)
	if value := convert.AsString(convert.GetPath(req.Metadata, "user_id")); value != "" {
		session = conversationKey(r, original, value)
	}
	toJSON := toJSONFn(convert.GeminiToClaude)
	if count {
		toJSON = func(_ string, raw []byte) ([]byte, error) { return tokenCountJSON(raw, "input_tokens") }
	}
	s.proxy(w, r, proxyPlan{protocol: "claude", model: original, target: target, session: session, mixed: mixed, stream: req.Stream && !count, count: count,
		build: func(a *models.Account, final string) (any, error) {
			outer, _, _ := convert.ClaudeToGeminiWithModel(req, a.ProjectID, a.Email, a.ID, final)
			return setSession(outer, session), nil
		},
		toJSON: toJSON, toSSE: convert.WriteClaudeSSE})
}

func (s *Server) geminiList(w http.ResponseWriter, r *http.Request) {
	catalog := s.modelCatalog()
	out := make([]any, 0, len(catalog))
	for _, m := range catalog {
		id, _ := m["id"].(string)
		display, _ := m["display_name"].(string)
		if display == "" {
			display = id
		}
		out = append(out, map[string]any{"name": "models/" + id, "version": "001", "displayName": display, "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}})
	}
	writeJSON(w, 200, map[string]any{"models": out})
}
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	count := strings.HasSuffix(path, "/countTokens")
	if count {
		path = strings.TrimSuffix(path, "/countTokens")
	}
	model, action := convert.ParseModelPath(path)
	if count {
		action = "countTokens"
	}
	if model == "" {
		writeProxyError(w, "gemini", 400, "model is required")
		return
	}
	if r.Method == http.MethodGet && action == "" {
		writeJSON(w, 200, map[string]any{"name": "models/" + model, "displayName": model})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeProxyError(w, "gemini", 405, "method not allowed")
		return
	}
	if action != "generateContent" && action != "streamGenerateContent" && action != "countTokens" {
		writeProxyError(w, "gemini", 400, "unsupported model action")
		return
	}
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeRequestError(w, "gemini", err)
		return
	}
	if body == nil {
		writeProxyError(w, "gemini", 400, "request must be an object")
		return
	}
	original := model
	used, mixed := s.routeModel(original)
	inner := body
	if wrapped := convert.AsMap(body["request"]); wrapped != nil {
		inner = wrapped
	}
	if len(convert.AsSlice(inner["contents"])) == 0 {
		writeProxyError(w, "gemini", 400, "contents must be a nonempty array")
		return
	}
	if err := validateNativeRequest(inner); err != nil {
		writeRequestError(w, "gemini", err)
		return
	}
	var budget *int
	tc := convert.AsMap(convert.GetPath(inner, "generationConfig", "thinkingConfig"))
	if value, ok := tc["thinkingBudget"]; ok {
		parsed, err := strconv.Atoi(fmt.Sprint(value))
		if err != nil {
			writeProxyError(w, "gemini", 400, "thinkingBudget must be an integer")
			return
		}
		budget = &parsed
	}
	effort := convert.AsString(tc["thinkingLevel"])
	target := convert.ResolveModel(used, budget, effort).Model
	count = action == "countTokens"
	session := requestSession(r, original, inner["contents"])
	if explicit := convert.AsString(inner["sessionId"]); explicit != "" {
		session = conversationKey(r, original, explicit)
	} else if explicit := convert.AsString(inner["session_id"]); explicit != "" {
		session = conversationKey(r, original, explicit)
	}
	toJSON := toJSONFn(func(report string, raw []byte) ([]byte, error) {
		out, err := convert.UnwrapGemini(raw)
		if err != nil {
			return nil, err
		}
		return convert.StampGeminiModel(out, report), nil
	})
	if count {
		toJSON = func(_ string, raw []byte) ([]byte, error) { return tokenCountJSON(raw, "totalTokens") }
	}
	s.proxy(w, r, proxyPlan{protocol: "gemini", model: original, target: target, session: session, mixed: mixed, stream: action == "streamGenerateContent", count: count,
		build: func(a *models.Account, final string) (any, error) {
			outer, _, _ := convert.NativeGeminiToInternalWithModel(body, used, a.ProjectID, a.Email, a.ID, final)
			return setSession(outer, session), nil
		},
		toJSON: toJSON, toSSE: convert.WriteGeminiSSE})
}

func tokenCountJSON(raw []byte, field string) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if response, ok := obj["response"].(map[string]any); ok {
		obj = response
	}
	count, ok := obj["totalTokens"]
	if !ok {
		return nil, fmt.Errorf("upstream countTokens response has no totalTokens")
	}
	return json.Marshal(map[string]any{field: count})
}
func setSession(outer convert.OuterRequest, session string) convert.OuterRequest {
	switch inner := outer.Request.(type) {
	case convert.InnerRequest:
		inner.SessionID = session
		outer.Request = inner
	case map[string]any:
		inner["sessionId"] = session
	}
	return outer
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, plan proxyPlan) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()
	var activeRelease func()
	var activeBody io.Closer
	defer func() {
		if activeRelease != nil {
			activeRelease()
		}
		if activeBody != nil {
			_ = activeBody.Close()
		}
	}()
	exclude := map[string]struct{}{}
	graceUsed := map[string]bool{}
	refreshed := map[string]bool{}
	var reuse *models.Account
	var reuseRelease func()
	var lastStatus int
	var lastErr, lastEmail, lastID, mapped string
	var retryAfter time.Duration
	attempts := 0
	maxAttempts := s.cfg.MaxRetryAttempts
	// A grace retry or forced token refresh may reuse the reserved slot once without consuming rotation budget.
	for attempts < maxAttempts || reuse != nil {
		if err := ctx.Err(); err != nil {
			lastStatus, lastErr = contextFailure(err)
			break
		}
		var acc *models.Account
		var release func()
		var err error
		if reuse != nil {
			acc, release = reuse, reuseRelease
			reuse, reuseRelease = nil, nil
		} else {
			acc, release, err = s.pool.Acquire(ctx, plan.target, plan.session, exclude)
			if err != nil {
				if ctx.Err() != nil {
					lastStatus, lastErr = contextFailure(ctx.Err())
					break
				}
				var unavailable *pool.UnavailableError
				if errors.As(err, &unavailable) && unavailable.RetryAfter > 0 {
					retryAfter = unavailable.RetryAfter
				}
				if lastStatus == 0 {
					lastStatus, lastErr = 503, err.Error()
				}
				break
			}
			attempts++
		}
		activeRelease = release // Idempotent pool release also protects the slot if a converter panics.
		lastID, lastEmail = acc.ID, acc.Email
		mapped = convert.RewriteToAvailable(plan.target, accountModelNames(acc), accountForwarding(acc))
		payload, buildErr := plan.build(acc, mapped)
		if buildErr != nil {
			release()
			lastStatus, lastErr = 400, buildErr.Error()
			break
		}
		var resp *http.Response
		var data []byte
		attemptStartedAt := time.Now()
		s.upstreamAttempts.Add(1)
		if plan.count {
			outer, ok := payload.(convert.OuterRequest)
			if !ok {
				release()
				lastStatus, lastErr = 500, "invalid countTokens request"
				break
			}
			resp, data, err = s.cc.CountTokens(ctx, acc.AccessToken, mapped, outer.Request)
		} else {
			if plan.direct {
				resp, data, err = s.cc.GenerateDirect(ctx, acc.AccessToken, payload)
			} else {
				resp, data, err = s.cc.Generate(ctx, acc.AccessToken, payload, plan.stream)
			}
		}
		if err == nil && resp == nil {
			err = fmt.Errorf("empty upstream response")
		}
		status := 0
		headers := make(http.Header)
		if resp != nil {
			activeBody = resp.Body
			status = resp.StatusCode
			headers = resp.Header
		}
		var source io.Reader
		var output []byte
		if err == nil && status >= 200 && status < 300 && plan.stream {
			source, err = convert.PrepareGeminiStream(resp.Body)
		}
		if err == nil && status >= 200 && status < 300 && !plan.stream {
			output, err = plan.toJSON(plan.model, data)
		}
		if err != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			status = 502
			var upstreamErr *convert.UpstreamError
			if errors.As(err, &upstreamErr) && upstreamErr.Code >= 400 {
				status = upstreamErr.Code
			}
			data, _ = json.Marshal(map[string]any{"error": map[string]any{"message": err.Error()}})
		} else if status >= 300 {
			if data == nil && resp.Body != nil {
				data, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				if err != nil {
					data = []byte(err.Error())
				}
			}
		}
		if status == 429 {
			s.upstream429.Add(1)
		}
		if err == nil && status >= 200 && status < 300 {
			w.Header().Set("X-Mapped-Model", mapped)
			w.Header().Set("X-Request-ID", middleware.GetReqID(ctx))
			if plan.stream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Accel-Buffering", "no")
				w.WriteHeader(200)
				flusher, _ := w.(http.Flusher)
				stats, streamErr := plan.toSSE(&flushWriter{w: w, f: flusher}, plan.model, source)
				resp.Body.Close()
				release()
				logStatus := 200
				message := ""
				if streamErr != nil {
					logStatus, message = 502, streamErr.Error()
					var upstreamErr *convert.UpstreamError
					if errors.As(streamErr, &upstreamErr) && upstreamErr.Code >= 400 {
						logStatus = upstreamErr.Code
						if logStatus == 429 {
							s.upstream429.Add(1)
						}
						decision := retryPolicy(logStatus, make(http.Header), []byte(message), attempts-1)
						scope := ""
						if decision.modelScoped {
							scope = mapped
						}
						if decision.cooldown > 0 {
							s.pool.MarkLimited(acc.ID, scope, time.Now().Add(decision.cooldown))
						}
					}
					if ctx.Err() != nil {
						logStatus, message = contextFailure(ctx.Err())
					}
				} else {
					s.pool.ClearLimitedBefore(acc.ID, "", attemptStartedAt)
					s.pool.ClearLimitedBefore(acc.ID, mapped, attemptStartedAt)
				}
				s.logReq(plan.protocol, plan.model, mapped, plan.mixed, lastID, lastEmail, logStatus, true, start, sanitizeError(message), stats)
				return
			}
			release()
			s.pool.ClearLimitedBefore(acc.ID, "", attemptStartedAt)
			s.pool.ClearLimitedBefore(acc.ID, mapped, attemptStartedAt)
			contentType := plan.contentType
			if contentType == "" {
				contentType = "application/json; charset=utf-8"
			}
			w.Header().Set("Content-Type", contentType)
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
			w.WriteHeader(200)
			_, writeErr := w.Write(output)
			logStatus, message := 200, ""
			if writeErr != nil {
				logStatus, message = 502, sanitizeError(writeErr.Error())
				if ctx.Err() != nil {
					logStatus, message = contextFailure(ctx.Err())
				}
			}
			s.logReq(plan.protocol, plan.model, mapped, plan.mixed, lastID, lastEmail, logStatus, false, start, message, convert.StreamStats{Usage: convert.UsageFromGemini(data)})
			return
		}
		if ctx.Err() != nil {
			release()
			lastStatus, lastErr = contextFailure(ctx.Err())
			break
		}
		lastStatus, lastErr = status, clipErr(data)
		decision := retryPolicy(status, headers, data, attempts-1)
		if status == 403 && (strings.Contains(strings.ToUpper(string(data)), "VALIDATION_REQUIRED") || strings.Contains(strings.ToUpper(string(data)), "PERMISSION_DENIED")) {
			_ = s.store.SetForbidden(acc.ID, sanitizeError(lastErr))
		}
		retryAfter = decision.cooldown
		// Refresh a rejected access token once. A generation 401 is not evidence that the refresh token is revoked.
		if status == 401 && !refreshed[acc.ID] {
			refreshed[acc.ID] = true
			if refreshErr := s.pool.InvalidateTokenContext(ctx, acc.ID); refreshErr == nil {
				if fresh, refreshErr := s.pool.RefreshTokenContext(ctx, acc.ID); refreshErr == nil {
					reuse, reuseRelease = fresh, release
					s.logAttempt(ctx, plan, mapped, acc.ID, attempts, status, "refresh_access_token", lastErr, 0)
					continue
				}
			}
		}
		scope := ""
		if decision.modelScoped {
			scope = mapped
		}
		if decision.cooldown > 0 {
			s.pool.MarkLimited(acc.ID, scope, time.Now().Add(decision.cooldown))
		}
		s.logAttempt(ctx, plan, mapped, acc.ID, attempts, status, decision.category, lastErr, decision.delay)
		if !decision.retry {
			release()
			break
		}
		if decision.grace && !graceUsed[acc.ID] {
			graceUsed[acc.ID] = true
			if err = s.wait(ctx, decision.delay); err != nil {
				release()
				lastStatus, lastErr = contextFailure(err)
				break
			}
			reuse, reuseRelease = acc, release
			continue
		}
		exclude[acc.ID] = struct{}{}
		release()
		if attempts >= maxAttempts {
			break
		}
		if err = s.wait(ctx, decision.delay); err != nil {
			lastStatus, lastErr = contextFailure(err)
			break
		}
	}
	if reuseRelease != nil {
		reuseRelease()
	}
	if lastStatus < 400 {
		lastStatus = 503
	}
	if lastErr == "" {
		lastErr = "no available accounts"
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
	}
	lastErr = sanitizeError(lastErr)
	writeProxyError(w, plan.protocol, lastStatus, lastErr)
	s.logReq(plan.protocol, plan.model, mapped, plan.mixed, lastID, lastEmail, lastStatus, plan.stream, start, lastErr, convert.StreamStats{})
}

func contextFailure(err error) (int, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return 504, "request deadline exceeded"
	}
	return 499, "request canceled"
}
func (s *Server) logAttempt(ctx context.Context, plan proxyPlan, mapped, account string, attempt, status int, category, message string, delay time.Duration) {
	if !s.loggingEnabled() {
		return
	}
	slog.WarnContext(ctx, "upstream attempt failed", "request_id", middleware.GetReqID(ctx), "protocol", plan.protocol, "model", plan.model, "mapped_model", mapped, "account_id", account, "attempt", attempt, "status", status, "category", category, "retry_delay_ms", delay.Milliseconds(), "error", sanitizeError(message))
}

func (s *Server) loggingEnabled() bool {
	return s.store.BoolSetting("enable_logging", true)
}

func (s *Server) logReq(protocol, model, mapped string, mixed bool, accID, email string, status int, stream bool, start time.Time, errMsg string, stats convert.StreamStats) {
	if !s.loggingEnabled() {
		return
	}
	latency := time.Since(start).Milliseconds()
	var ttft int64
	if !stats.FirstTokenAt.IsZero() {
		ttft = stats.FirstTokenAt.Sub(start).Milliseconds()
		if ttft < 1 {
			ttft = 1
		}
		if ttft > latency {
			ttft = latency
		}
	} else if !stream && status < 400 && latency > 0 {
		ttft = latency
	}
	outTok := stats.Usage.Output
	var tps float64
	if outTok > 0 {
		gen := latency
		if stream && ttft > 0 && latency > ttft {
			gen = latency - ttft
		}
		if gen > 0 {
			tps = math.Round((float64(outTok)/(float64(gen)/1000.0))*100) / 100
		}
	}
	_ = s.store.AddLog(models.RequestLog{
		Protocol:        protocol,
		Model:           model,
		MappedModel:     mapped,
		AccountID:       accID,
		AccountEmail:    email,
		Status:          status,
		Stream:          stream,
		LatencyMS:       latency,
		Error:           errMsg,
		Mixed:           mixed,
		TTFTMS:          ttft,
		InputTokens:     stats.Usage.Input,
		OutputTokens:    outTok,
		CacheTokens:     stats.Usage.Cache,
		ReasoningTokens: stats.Usage.Reasoning,
		TPS:             tps,
	})
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	if rw, ok := f.w.(http.ResponseWriter); ok {
		_ = http.NewResponseController(rw).SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
	n, err := f.w.Write(p)
	if err == nil {
		if rw, ok := f.w.(http.ResponseWriter); ok {
			flushErr := http.NewResponseController(rw).Flush()
			if !errors.Is(flushErr, http.ErrNotSupported) {
				err = flushErr
			}
		} else if f.f != nil {
			f.f.Flush()
		}
	}
	return n, err
}

func clipErr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "upstream error"
	}
	if json.Valid(b) {
		var v map[string]any
		if json.Unmarshal(b, &v) == nil {
			if e := v["error"]; e != nil {
				switch t := e.(type) {
				case string:
					s = t
				case map[string]any:
					if m, ok := t["message"].(string); ok {
						s = m
					}
				}
			}
		}
	}
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

func accountModelNames(acc *models.Account) []string {
	if acc == nil || acc.Quota == nil {
		return nil
	}
	out := make([]string, 0, len(acc.Quota.Models))
	for _, m := range acc.Quota.Models {
		if n := strings.TrimSpace(m.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func accountForwarding(acc *models.Account) map[string]string {
	if acc == nil || acc.Quota == nil {
		return nil
	}
	return acc.Quota.ForwardingRules
}

func (s *Server) modelCatalog() []map[string]any {
	official, err := s.officialModels()
	if err != nil {
		return convert.Catalog()
	}
	return convert.BuildCatalog(official)
}

func (s *Server) officialModels() ([]convert.OfficialModel, error) {
	rows, _, err := s.store.OfficialModels()
	if err != nil {
		return nil, err
	}
	out := make([]convert.OfficialModel, 0, len(rows))
	for _, m := range rows {
		out = append(out, convert.OfficialModel{ID: m.Name, DisplayName: m.DisplayName})
	}
	return out, nil
}

func (s *Server) routeModel(requested string) (string, bool) {
	rules := s.loadMixRules()
	converted := make([]convert.MixRule, 0, len(rules))
	for _, rule := range rules {
		converted = append(converted, convert.MixRule{
			From:    rule.From,
			To:      rule.To,
			Percent: rule.Percent,
			Enabled: rule.Enabled,
		})
	}
	used := convert.ApplyMix(requested, converted, rand.Intn(100))
	return used, used != requested
}
