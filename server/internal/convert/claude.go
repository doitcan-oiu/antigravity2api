package convert

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ClaudeRequest struct {
	Model       string  `json:"model"`
	Messages    []any   `json:"messages"`
	System      any     `json:"system"`
	Tools       []any   `json:"tools"`
	Stream      bool    `json:"stream"`
	MaxTokens   *int    `json:"max_tokens"`
	Temperature *float64 `json:"temperature"`
	TopP        *float64 `json:"top_p"`
	Thinking    *struct {
		Type         string `json:"type"`
		BudgetTokens *int   `json:"budget_tokens"`
	} `json:"thinking"`
	ToolChoice any `json:"tool_choice"`
}

func ClaudeToGemini(req ClaudeRequest, projectID, email, accountID string) (OuterRequest, string, bool) {
	mapped := MapModel(req.Model)
	var systemParts []string
	switch s := req.System.(type) {
	case string:
		if strings.TrimSpace(s) != "" {
			systemParts = append(systemParts, s)
		}
	case []any:
		for _, item := range s {
			m := AsMap(item)
			if m != nil {
				if t := AsString(m["text"]); t != "" {
					systemParts = append(systemParts, t)
				}
			}
		}
	}

	contents := make([]map[string]any, 0)
	for _, msg := range req.Messages {
		m := AsMap(msg)
		if m == nil {
			continue
		}
		role := strings.ToLower(AsString(m["role"]))
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		parts := claudeContentToParts(m["content"], geminiRole == "model")
		if len(parts) == 0 {
			continue
		}
		contents = append(contents, map[string]any{"role": geminiRole, "parts": parts})
	}
	if len(contents) == 0 {
		contents = append(contents, map[string]any{"role": "user", "parts": []any{map[string]any{"text": " "}}})
	}

	gen := map[string]any{}
	if req.Temperature != nil {
		gen["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		gen["topP"] = *req.TopP
	}
	maxTok := 0
	if req.MaxTokens != nil {
		maxTok = *req.MaxTokens
		gen["maxOutputTokens"] = maxTok
	}
	includeThoughts := IsThinkingModel(mapped)
	if req.Thinking != nil {
		if strings.EqualFold(req.Thinking.Type, "enabled") {
			includeThoughts = true
		}
		if strings.EqualFold(req.Thinking.Type, "disabled") {
			includeThoughts = false
		}
	}
	if includeThoughts && !IsImageModel(mapped) {
		budget := DefaultThinkingBudget(mapped)
		if req.Thinking != nil && req.Thinking.BudgetTokens != nil {
			budget = *req.Thinking.BudgetTokens
		}
		gen["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingBudget": budget}
		if maxTok > 0 && maxTok <= budget {
			gen["maxOutputTokens"] = budget + 1024
		}
	}

	inner := InnerRequest{
		Contents:         contents,
		GenerationConfig: gen,
		SafetySettings:   SafetyOff(),
		SessionID:        SessionID(accountID, ""),
	}
	if len(systemParts) > 0 {
		inner.SystemInstruction = map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": strings.Join(systemParts, "\n\n")}},
		}
	}
	if decls := claudeTools(req.Tools); len(decls) > 0 {
		inner.Tools = []any{map[string]any{"functionDeclarations": decls}}
		mode := "AUTO"
		if tc := AsMap(req.ToolChoice); tc != nil {
			switch AsString(tc["type"]) {
			case "any":
				mode = "ANY"
			case "none":
				mode = "NONE"
			}
		}
		inner.ToolConfig = map[string]any{"functionCallingConfig": map[string]any{"mode": mode}}
	}
	return Wrap(projectID, mapped, email, SessionID(accountID, ""), inner, IsImageModel(mapped)), mapped, req.Stream
}

func claudeContentToParts(content any, isModel bool) []any {
	parts := make([]any, 0)
	switch c := content.(type) {
	case string:
		if strings.TrimSpace(c) != "" {
			parts = append(parts, map[string]any{"text": c})
		}
	case []any:
		for _, item := range c {
			m := AsMap(item)
			if m == nil {
				continue
			}
			switch AsString(m["type"]) {
			case "text":
				if t := AsString(m["text"]); t != "" {
					parts = append(parts, map[string]any{"text": t})
				}
			case "thinking":
				if t := AsString(m["thinking"]); t != "" {
					parts = append(parts, map[string]any{"text": t, "thought": true})
				}
			case "image":
				src := AsMap(m["source"])
				if src != nil && AsString(src["type"]) == "base64" {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{
							"mimeType": firstNonEmpty(AsString(src["media_type"]), "image/jpeg"),
							"data":     AsString(src["data"]),
						},
					})
				}
			case "tool_use":
				if isModel {
					fc := map[string]any{"name": AsString(m["name"]), "args": m["input"]}
					if id := AsString(m["id"]); id != "" {
						fc["id"] = id
					}
					parts = append(parts, map[string]any{"functionCall": fc})
				}
			case "tool_result":
				name := firstNonEmpty(AsString(m["name"]), "tool")
				fr := map[string]any{
					"name":     name,
					"response": map[string]any{"result": extractText(m["content"])},
				}
				if id := AsString(m["tool_use_id"]); id != "" {
					fr["id"] = id
				}
				parts = append(parts, map[string]any{"functionResponse": fr})
			}
		}
	}
	return parts
}

func claudeTools(tools []any) []any {
	decls := make([]any, 0)
	for _, tool := range tools {
		m := AsMap(tool)
		if m == nil {
			continue
		}
		name := AsString(m["name"])
		if name == "" {
			continue
		}
		decl := map[string]any{"name": name}
		if d := AsString(m["description"]); d != "" {
			decl["description"] = d
		}
		if schema := m["input_schema"]; schema != nil {
			decl["parameters"] = schema
		}
		decls = append(decls, decl)
	}
	return decls
}

func GeminiToClaude(model string, raw []byte) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	data := envelope
	if r := AsMap(envelope["response"]); r != nil {
		data = r
	}
	text, thinking, toolCalls, finish, usage := collectParts(data)
	content := make([]any, 0)
	if thinking != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": thinking})
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, tc := range toolCalls {
		m := AsMap(tc)
		fn := AsMap(m["function"])
		var input any = map[string]any{}
		if fn != nil {
			_ = json.Unmarshal([]byte(AsString(fn["arguments"])), &input)
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    AsString(m["id"]),
			"name":  AsString(fn["name"]),
			"input": input,
		})
	}
	stop := "end_turn"
	if len(toolCalls) > 0 {
		stop = "tool_use"
	}
	if strings.ToUpper(finish) == "MAX_TOKENS" {
		stop = "max_tokens"
	}
	out := map[string]any{
		"id":            "msg_" + uuid.NewString(),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stop,
		"stop_sequence": nil,
		"usage":         claudeUsage(usage),
	}
	return json.Marshal(out)
}

func claudeUsage(u any) map[string]any {
	m := AsMap(u)
	inTok, outTok := 0, 0
	if m != nil {
		if v, ok := m["prompt_tokens"].(int); ok {
			inTok = v
		}
		if v, ok := m["completion_tokens"].(int); ok {
			outTok = v
		}
	}
	return map[string]any{"input_tokens": inTok, "output_tokens": outTok}
}

func nowUnix() int64 { return time.Now().Unix() }
