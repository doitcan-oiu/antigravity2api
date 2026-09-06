package convert

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type InnerRequest struct {
	SystemInstruction any            `json:"systemInstruction,omitempty"`
	Tools             any            `json:"tools,omitempty"`
	ToolConfig        any            `json:"toolConfig,omitempty"`
	GenerationConfig  any            `json:"generationConfig,omitempty"`
	SafetySettings    any            `json:"safetySettings,omitempty"`
	SessionID         any            `json:"sessionId,omitempty"`
	Contents          any            `json:"contents"`
	Extra             map[string]any `json:"-"`
}

func (r InnerRequest) MarshalJSON() ([]byte, error) {
	type plain InnerRequest
	if len(r.Extra) == 0 {
		return json.Marshal(plain(r))
	}
	raw, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		if _, known := fields[k]; !known {
			encoded, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			fields[k] = encoded
		}
	}
	return json.Marshal(fields)
}

type OuterRequest struct {
	Project            string `json:"project"`
	Request            any    `json:"request"`
	Model              string `json:"model"`
	UserAgent          string `json:"userAgent"`
	RequestType        string `json:"requestType"`
	RequestID          string `json:"requestId"`
	EnabledCreditTypes any    `json:"enabledCreditTypes,omitempty"`
}

func SafetyOff() []map[string]string {
	cats := []string{
		"HARM_CATEGORY_HARASSMENT",
		"HARM_CATEGORY_HATE_SPEECH",
		"HARM_CATEGORY_SEXUALLY_EXPLICIT",
		"HARM_CATEGORY_DANGEROUS_CONTENT",
	}
	out := make([]map[string]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, map[string]string{"category": c, "threshold": "OFF"})
	}
	return out
}

func Wrap(projectID, model, email, sessionID string, inner InnerRequest, image bool) OuterRequest {
	if inner.SafetySettings == nil {
		inner.SafetySettings = SafetyOff()
	}
	if AsString(inner.SessionID) == "" && sessionID != "" {
		inner.SessionID = sessionID
	}
	normalizeInner(model, &inner, image)
	ua := "antigravity"
	reqType := "agent"
	if image {
		reqType = "image_gen"
	}
	if email != "" && !strings.HasSuffix(email, "@gmail.com") && !strings.HasSuffix(email, "@googlemail.com") {
		ua = "jetski"
	}
	out := OuterRequest{
		Project:     projectID,
		Request:     inner,
		Model:       model,
		UserAgent:   ua,
		RequestType: reqType,
		RequestID:   fmt.Sprintf("agent/%d/%s", time.Now().UnixMilli(), uuid.NewString()[:8]),
	}
	if !image {
		out.EnabledCreditTypes = []string{"GOOGLE_ONE_AI"}
	}
	return out
}

func SessionID(accountID, fallback string) string {
	if fallback != "" {
		return fallback
	}
	sum := sha1.Sum([]byte(accountID))
	return hex.EncodeToString(sum[:])[:16]
}

func normalizeInner(model string, inner *InnerRequest, image bool) {
	if inner == nil {
		return
	}
	inner.GenerationConfig = normalizeGenerationConfig(model, cloneValue(inner.GenerationConfig), image)
	if image {
		inner.Tools = nil
		inner.ToolConfig = nil
		inner.SystemInstruction = nil
	}
	// Claude function-call history requires a real signature; Gemini's sentinel
	// cannot authenticate it. Keep the tool history and disable thinking for this
	// request when the client did not preserve a recoverable signature.
	if strings.Contains(strings.ToLower(model), "claude") && thinkingConfigEnabled(inner.GenerationConfig) {
		missing := false
		for _, v := range AsSlice(inner.Contents) {
			m := AsMap(v)
			for _, v := range AsSlice(m["parts"]) {
				p := AsMap(v)
				if p["functionCall"] != nil {
					sig := thoughtSignature(p)
					if sig == "" || sig == "skip_thought_signature_validator" {
						missing = true
					}
				}
			}
		}
		if missing {
			AsMap(inner.GenerationConfig)["thinkingConfig"] = map[string]any{"includeThoughts": false, "thinkingBudget": 0}
		}
	}
	inner.Contents = sanitizeContents(cloneValue(inner.Contents), model, thinkingConfigEnabled(inner.GenerationConfig) && !image)
}

func normalizeGenerationConfig(model string, gen any, image bool) map[string]any {
	gc := AsMap(gen)
	if gc == nil {
		gc = map[string]any{}
	}
	// Some internal models reject the presence of penalty fields even at zero.
	// Zero is the neutral default, so omit it without changing generation intent.
	for _, field := range []string{"presencePenalty", "frequencyPenalty", "presence_penalty", "frequency_penalty"} {
		if value, ok := gc[field]; ok && isZeroPenalty(value) {
			delete(gc, field)
		}
	}
	if image {
		delete(gc, "responseMimeType")
		delete(gc, "responseModalities")
		return gc
	}
	if gc["topK"] == nil {
		gc["topK"] = 40
	}
	if gc["topP"] == nil {
		gc["topP"] = 1.0
	}
	maxTokens := intVal(gc["maxOutputTokens"])
	modelMax := MaxOutputTokens(model)
	if maxTokens <= 0 || maxTokens > modelMax {
		maxTokens = modelMax
	}
	tc := AsMap(gc["thinkingConfig"])
	if tc != nil {
		if level := AsString(tc["thinkingLevel"]); level != "" && !strings.Contains(strings.ToLower(model), "claude") {
			delete(tc, "thinkingLevel")
			if tc["thinkingBudget"] == nil {
				tc["thinkingBudget"] = thinkingBudgetFromLevel(model, level)
			}
		}
		if _, hasBudget := tc["thinkingBudget"]; hasBudget {
			budget := intVal(tc["thinkingBudget"])
			capBudget := DefaultThinkingBudget(model)
			// A client may choose a larger Sonnet budget than its conservative default.
			if strings.Contains(strings.ToLower(model), "claude-sonnet") {
				capBudget = 32768
			}
			if budget > capBudget && capBudget > 0 {
				budget = capBudget
			}
			if budget >= maxTokens {
				maxTokens = budget + 8192
				if maxTokens > modelMax {
					maxTokens = modelMax
					budget = maxTokens - 8192
				}
			}
			tc["thinkingBudget"] = budget
		}
		gc["thinkingConfig"] = tc
	}
	gc["maxOutputTokens"] = maxTokens
	return gc
}

func isZeroPenalty(value any) bool {
	switch n := value.(type) {
	case float64:
		return n == 0
	case float32:
		return n == 0
	case int:
		return n == 0
	case int64:
		return n == 0
	case json.Number:
		f, err := n.Float64()
		return err == nil && f == 0
	default:
		return false
	}
}

func thinkingBudgetFromLevel(model, level string) int {
	capBudget := DefaultThinkingBudget(model)
	switch strings.ToUpper(level) {
	case "NONE":
		return 0
	case "LOW":
		if capBudget/4 > 4096 {
			return capBudget / 4
		}
		return 4096
	case "HIGH":
		return capBudget
	default:
		if capBudget/2 > 8192 {
			return capBudget / 2
		}
		return 8192
	}
}

func thinkingConfigEnabled(gen any) bool {
	gc := AsMap(gen)
	if gc == nil {
		return false
	}
	tc := AsMap(gc["thinkingConfig"])
	if tc == nil {
		return false
	}
	if v, ok := tc["includeThoughts"].(bool); ok && v {
		return true
	}
	budget := intVal(tc["thinkingBudget"])
	return budget > 0 || budget == -1
}

func sanitizeContents(contents any, model string, thinking bool) any {
	items := AsSlice(contents)
	if len(items) == 0 {
		return contents
	}
	gemini := strings.Contains(strings.ToLower(model), "gemini")
	var out []map[string]any
	for _, v := range items {
		msg := AsMap(v)
		if msg == nil {
			continue
		}
		role := strings.ToLower(AsString(msg["role"]))
		if role == "" {
			role = "user"
		}
		if role == "assistant" {
			role = "model"
		}
		msg["role"] = role
		var parts []any
		for _, v := range AsSlice(msg["parts"]) {
			p := AsMap(v)
			if p == nil {
				continue
			}
			sig := thoughtSignature(p)
			if partIsThought(p) {
				if !thinking || (!gemini && (sig == "" || sig == "skip_thought_signature_validator")) {
					delete(p, "thought")
					delete(p, "thoughtSignature")
					delete(p, "thought_signature")
				} else if gemini {
					ensureThoughtSignature(p)
				}
			}
			if AsMap(p["functionCall"]) != nil {
				if gemini {
					ensureThoughtSignature(p)
				} else if sig == "skip_thought_signature_validator" {
					delete(p, "thoughtSignature")
					delete(p, "thought_signature")
				}
			}
			parts = append(parts, p)
		}
		msg["parts"] = parts
		out = append(out, msg)
	}
	return mergeContents(out)
}

func ensureThoughtSignature(part map[string]any) {
	if part == nil {
		return
	}
	sig := firstNonEmpty(AsString(part["thoughtSignature"]), AsString(part["thought_signature"]))
	if sig == "" {
		sig = "skip_thought_signature_validator"
	}
	part["thoughtSignature"] = sig
	delete(part, "thought_signature")
}

func ensureLeadingThought(parts []any, gemini bool) []any {
	if leadingThoughtOK(parts) {
		if gemini && len(parts) > 0 {
			if part := AsMap(parts[0]); part != nil {
				if !partIsThought(part) {
					part["thought"] = true
				}
				ensureThoughtSignature(part)
			}
		}
		return parts
	}
	dummy := map[string]any{"text": "Thinking...", "thought": true}
	if gemini {
		ensureThoughtSignature(dummy)
	}
	return append([]any{dummy}, parts...)
}

func leadingThoughtOK(parts []any) bool {
	if len(parts) == 0 {
		return false
	}
	part := AsMap(parts[0])
	if part == nil {
		return false
	}
	if AsString(part["text"]) == "" {
		return false
	}
	return partIsThought(part) || AsString(part["thoughtSignature"]) != "" || AsString(part["thought_signature"]) != ""
}

func shouldAutoInjectThinking(model string) bool {
	m := strings.ToLower(model)
	if IsImageModel(m) || strings.Contains(m, "claude") || strings.Contains(m, "preview") {
		return false
	}
	return strings.Contains(m, "thinking") ||
		strings.Contains(m, "gemini-2.0-pro") ||
		strings.Contains(m, "gemini-3-pro") ||
		strings.Contains(m, "gemini-3.1-pro")
}

func partIsThought(part map[string]any) bool {
	if part == nil {
		return false
	}
	switch v := part["thought"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func AsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func AsMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func AsSlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []map[string]any:
		out := make([]any, len(s))
		for i, item := range s {
			out[i] = item
		}
		return out
	default:
		return nil
	}
}

func GetPath(v any, keys ...string) any {
	cur := v
	for _, k := range keys {
		m := AsMap(cur)
		if m == nil {
			return nil
		}
		cur = m[k]
	}
	return cur
}
