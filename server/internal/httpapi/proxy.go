package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wo/antigravity2api/internal/convert"
	"github.com/wo/antigravity2api/internal/models"
)

func (s *Server) openaiModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"object": "list", "data": s.modelCatalog()})
}

func (s *Server) openaiChat(w http.ResponseWriter, r *http.Request) {
	var req convert.OpenAIRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	original := req.Model
	used, mixed := s.routeModel(original)
	req.Model = used
	s.proxy(w, r, "openai", original, mixed, req.Stream, func(projectID, email, accountID string) (any, string, bool, error) {
		outer, mapped, stream := convert.OpenAIToGemini(req, projectID, email, accountID)
		return outer, mapped, stream, nil
	}, func(report string, raw []byte) ([]byte, error) {
		return convert.GeminiToOpenAI(report, raw, "chatcmpl-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	}, convert.WriteOpenAISSE)
}

func (s *Server) claudeMessages(w http.ResponseWriter, r *http.Request) {
	var req convert.ClaudeRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": err.Error()}})
		return
	}
	original := req.Model
	used, mixed := s.routeModel(original)
	req.Model = used
	s.proxy(w, r, "claude", original, mixed, req.Stream, func(projectID, email, accountID string) (any, string, bool, error) {
		outer, mapped, stream := convert.ClaudeToGemini(req, projectID, email, accountID)
		return outer, mapped, stream, nil
	}, convert.GeminiToClaude, convert.WriteClaudeSSE)
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
		out = append(out, map[string]any{
			"name":                       "models/" + id,
			"version":                    "001",
			"displayName":                display,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		})
	}
	writeJSON(w, 200, map[string]any{"models": out})
}

func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request) {
	model, action := convert.ParseModelPath(r.URL.Path)
	stream := strings.Contains(strings.ToLower(action), "stream")
	var body map[string]any
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{
			"name":        "models/" + model,
			"displayName": model,
		})
		return
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if v, ok := body["stream"].(bool); ok && v {
		stream = true
	}
	original := model
	used, mixed := s.routeModel(original)
	model = used
	s.proxy(w, r, "gemini", original, mixed, stream, func(projectID, email, accountID string) (any, string, bool, error) {
		outer, mapped, st := convert.NativeGeminiToInternal(body, model, projectID, email, accountID)
		return outer, mapped, st || stream, nil
	}, func(report string, raw []byte) ([]byte, error) {
		out, err := convert.UnwrapGemini(raw)
		if err != nil {
			return nil, err
		}
		return convert.StampGeminiModel(out, report), nil
	}, func(dst io.Writer, report string, src io.Reader) error {
		return convert.WriteGeminiSSE(dst, report, src)
	})
}

type buildFn func(projectID, email, accountID string) (payload any, mapped string, stream bool, err error)
type toJSONFn func(mapped string, raw []byte) ([]byte, error)
type toSSEFn func(dst io.Writer, model string, src io.Reader) error

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, protocol, model string, mixed, streamHint bool, build buildFn, toJSON toJSONFn, toSSE toSSEFn) {
	start := time.Now()
	exclude := map[string]struct{}{}
	var lastStatus int
	var lastErr string
	var lastEmail, lastID, mapped string
	stream := streamHint
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	for attempt := 0; attempt < 5; attempt++ {
		if ctx.Err() != nil {
			lastErr = "request canceled"
			lastStatus = 499
			break
		}
		acc, err := s.pool.Next(exclude)
		if err != nil {
			lastErr = err.Error()
			lastStatus = http.StatusServiceUnavailable
			break
		}
		lastID, lastEmail = acc.ID, acc.Email
		payload, mappedModel, streamFlag, err := build(acc.ProjectID, acc.Email, acc.ID)
		if err != nil {
			lastErr = err.Error()
			lastStatus = 400
			break
		}
		mapped = convert.RewriteToAvailable(mappedModel, accountModelNames(acc), accountForwarding(acc))
		if mapped != mappedModel {
			if outer, ok := payload.(convert.OuterRequest); ok {
				outer.Model = mapped
				payload = outer
			}
		}
		stream = streamFlag || streamHint
		resp, data, err := s.cc.Generate(ctx, acc.AccessToken, payload, stream)
		if err != nil {
			lastErr = err.Error()
			lastStatus = 502
			exclude[acc.ID] = struct{}{}
			continue
		}
		readBody := func() []byte {
			if data != nil {
				return data
			}
			if resp.Body != nil {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				return b
			}
			return nil
		}
		if resp.StatusCode == 429 {
			body := readBody()
			lastStatus = 429
			lastErr = clipErr(body)
			exclude[acc.ID] = struct{}{}
			continue
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			body := readBody()
			lastStatus = resp.StatusCode
			lastErr = clipErr(body)
			exclude[acc.ID] = struct{}{}
			if strings.Contains(strings.ToLower(string(body)), "invalid") {
				_ = s.store.SetDisabled(acc.ID, true, "upstream unauthorized")
			}
			continue
		}
		if resp.StatusCode >= 500 {
			body := readBody()
			lastStatus = resp.StatusCode
			lastErr = clipErr(body)
			exclude[acc.ID] = struct{}{}
			continue
		}
		if stream {
			if resp.StatusCode >= 400 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				lastStatus = resp.StatusCode
				lastErr = clipErr(body)
				writeJSON(w, resp.StatusCode, map[string]any{"error": lastErr})
				s.logReq(protocol, model, mapped, mixed, lastID, lastEmail, resp.StatusCode, true, start, lastErr)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(200)
			flusher, _ := w.(http.Flusher)
			pw := &flushWriter{w: w, f: flusher}
			if err := toSSE(pw, model, resp.Body); err != nil {
				lastErr = err.Error()
			}
			resp.Body.Close()
			s.logReq(protocol, model, mapped, mixed, lastID, lastEmail, 200, true, start, lastErr)
			return
		}
		if resp.StatusCode >= 400 {
			lastStatus = resp.StatusCode
			lastErr = clipErr(data)
			writeJSON(w, resp.StatusCode, map[string]any{"error": lastErr})
			s.logReq(protocol, model, mapped, mixed, lastID, lastEmail, resp.StatusCode, false, start, lastErr)
			return
		}
		out, err := toJSON(model, data)
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			s.logReq(protocol, model, mapped, mixed, lastID, lastEmail, 502, false, start, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write(out)
		s.logReq(protocol, model, mapped, mixed, lastID, lastEmail, 200, false, start, "")
		return
	}

	if lastStatus == 0 {
		lastStatus = 503
	}
	if lastErr == "" {
		lastErr = "no available accounts"
	}
	writeJSON(w, lastStatus, map[string]any{"error": lastErr})
	s.logReq(protocol, model, mapped, mixed, lastID, lastEmail, lastStatus, stream, start, lastErr)
}

func (s *Server) loggingEnabled() bool {
	return s.store.BoolSetting("enable_logging", true)
}

func (s *Server) logReq(protocol, model, mapped string, mixed bool, accID, email string, status int, stream bool, start time.Time, errMsg string) {
	if !s.loggingEnabled() {
		return
	}
	_ = s.store.AddLog(models.RequestLog{
		Protocol:     protocol,
		Model:        model,
		MappedModel:  mapped,
		AccountID:    accID,
		AccountEmail: email,
		Status:       status,
		Stream:       stream,
		LatencyMS:    time.Since(start).Milliseconds(),
		Error:        errMsg,
		Mixed:        mixed,
	})
}

type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.f != nil {
		f.f.Flush()
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
