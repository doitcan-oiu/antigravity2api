package convert

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type InnerRequest struct {
	SystemInstruction any `json:"systemInstruction,omitempty"`
	Tools             any `json:"tools,omitempty"`
	ToolConfig        any `json:"toolConfig,omitempty"`
	GenerationConfig  any `json:"generationConfig,omitempty"`
	SafetySettings    any `json:"safetySettings,omitempty"`
	SessionID         any `json:"sessionId,omitempty"`
	Contents          any `json:"contents"`
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
	if sessionID != "" {
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
	inner.GenerationConfig = normalizeGenerationConfig(model, inner.GenerationConfig, image)
	inner.Contents = sanitizeContents(inner.Contents, model, thinkingConfigEnabled(inner.GenerationConfig) && !image)
}

func normalizeGenerationConfig(model string, gen any, image bool) map[string]any {
	gc := AsMap(gen)
	if gc == nil {
		gc = map[string]any{}
	}
	lower := strings.ToLower(model)
	isGemini := strings.Contains(lower, "gemini")
	if image {
		if tc := AsMap(gc["thinkingConfig"]); tc == nil {
			gc["thinkingConfig"] = map[string]any{"includeThoughts": false}
		}
		delete(gc, "responseMimeType")
		delete(gc, "responseModalities")
		return gc
	}
	if isGemini {
		if gc["topK"] == nil {
			gc["topK"] = 40
		}
		if gc["topP"] == nil {
			gc["topP"] = 1.0
		}
	}
	tc := AsMap(gc["thinkingConfig"])
	if tc == nil {
		return gc
	}
	if level := strings.ToUpper(AsString(tc["thinkingLevel"])); level != "" {
		delete(tc, "thinkingLevel")
		if tc["thinkingBudget"] == nil {
			tc["thinkingBudget"] = thinkingBudgetFromLevel(model, level)
		}
	}
	budget := intVal(tc["thinkingBudget"])
	capBudget := DefaultThinkingBudget(model)
	if capBudget > 0 && budget > capBudget {
		budget = capBudget
		tc["thinkingBudget"] = budget
	}
	gc["thinkingConfig"] = tc
	if budget <= 0 {
		return gc
	}
	maxTok := intVal(gc["maxOutputTokens"])
	modelMax := MaxOutputTokens(model)
	need := budget + 8192
	if need > modelMax {
		need = modelMax
	}
	if maxTok <= 0 || maxTok <= budget {
		maxTok = need
	}
	if maxTok > modelMax {
		maxTok = modelMax
	}
	if maxTok <= budget {
		budget = maxTok - 8192
		if budget < 1024 {
			budget = maxTok / 2
		}
		if budget < 0 {
			budget = 0
		}
		tc["thinkingBudget"] = budget
		gc["thinkingConfig"] = tc
	}
	gc["maxOutputTokens"] = maxTok
	return gc
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
	for i, item := range items {
		msg := AsMap(item)
		if msg == nil {
			continue
		}
		if AsString(msg["role"]) == "" {
			msg["role"] = "user"
		}
		role := strings.ToLower(AsString(msg["role"]))
		parts := AsSlice(msg["parts"])
		if thinking && gemini {
			for _, p := range parts {
				part := AsMap(p)
				if part == nil {
					continue
				}
				if AsMap(part["functionCall"]) != nil || partIsThought(part) {
					ensureThoughtSignature(part)
				}
			}
		}
		if thinking && (role == "model" || role == "assistant") {
			parts = ensureLeadingThought(parts, gemini)
			msg["parts"] = parts
		}
		items[i] = msg
	}
	return items
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
	part["thought_signature"] = sig
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
