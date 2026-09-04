package convert

import (
	"encoding/json"
	"fmt"
	"strings"
)

type OpenAIRequest struct {
	Model            string          `json:"model"`
	Messages         []OpenAIMessage `json:"messages"`
	Input            any             `json:"input"`
	Instructions     string          `json:"instructions"`
	Tools            []any           `json:"tools"`
	Stream           bool            `json:"stream"`
	Temperature      *float64        `json:"temperature"`
	TopP             *float64        `json:"top_p"`
	MaxTokens        *int            `json:"max_tokens"`
	MaxOutputTokens  *int            `json:"max_output_tokens"`
	Thinking         *OpenAIThinking `json:"thinking"`
	Reasoning        *OpenAIThinking `json:"reasoning"`
	ToolChoice       any             `json:"tool_choice"`
}

type OpenAIThinking struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens"`
}

type OpenAIMessage struct {
	Role             string `json:"role"`
	Content          any    `json:"content"`
	Name             string `json:"name"`
	ToolCallID       string `json:"tool_call_id"`
	ToolCalls        []any  `json:"tool_calls"`
	ReasoningContent string `json:"reasoning_content"`
}

func OpenAIToGemini(req OpenAIRequest, projectID, email, accountID string) (OuterRequest, string, bool) {
	mapped := MapModel(req.Model)
	msgs := req.Messages
	if len(msgs) == 0 && req.Input != nil {
		msgs = responsesToMessages(req.Instructions, req.Input)
	}

	var systemParts []string
	if strings.TrimSpace(req.Instructions) != "" {
		systemParts = append(systemParts, req.Instructions)
	}
	contents := make([]map[string]any, 0)
	for _, msg := range msgs {
		role := strings.ToLower(msg.Role)
		if role == "system" || role == "developer" {
			if t := extractText(msg.Content); t != "" {
				systemParts = append(systemParts, t)
			}
			continue
		}
		parts := make([]any, 0)
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
			if msg.ReasoningContent != "" {
				parts = append(parts, map[string]any{"text": msg.ReasoningContent, "thought": true})
			}
		}
		if role == "tool" || role == "function" {
			name := firstNonEmpty(msg.Name, "tool")
			resp := map[string]any{"result": extractText(msg.Content)}
			fr := map[string]any{"name": name, "response": resp}
			if msg.ToolCallID != "" {
				fr["id"] = msg.ToolCallID
			}
			parts = append(parts, map[string]any{"functionResponse": fr})
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
			continue
		}
		parts = append(parts, contentToParts(msg.Content)...)
		for _, tc := range msg.ToolCalls {
			m := AsMap(tc)
			if m == nil {
				continue
			}
			fn := AsMap(m["function"])
			name := AsString(GetPath(m, "function", "name"))
			argsRaw := AsString(GetPath(m, "function", "arguments"))
			var args any = map[string]any{}
			if argsRaw != "" {
				_ = json.Unmarshal([]byte(argsRaw), &args)
			} else if fn != nil && fn["arguments"] != nil && AsString(fn["arguments"]) == "" {
				args = fn["arguments"]
			}
			fc := map[string]any{"name": name, "args": args}
			if id := AsString(m["id"]); id != "" {
				fc["id"] = id
			}
			parts = append(parts, map[string]any{"functionCall": fc})
		}
		if len(parts) == 0 {
			continue
		}
		contents = append(contents, map[string]any{"role": geminiRole, "parts": parts})
	}
	if len(contents) == 0 {
		contents = append(contents, map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": " "}},
		})
	}

	gen := map[string]any{}
	if req.Temperature != nil {
		gen["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		gen["topP"] = *req.TopP
	}
	maxTok := 0
	if req.MaxOutputTokens != nil {
		maxTok = *req.MaxOutputTokens
	} else if req.MaxTokens != nil {
		maxTok = *req.MaxTokens
	}
	if maxTok > 0 {
		gen["maxOutputTokens"] = maxTok
	}

	thinking := req.Thinking
	if thinking == nil {
		thinking = req.Reasoning
	}
	includeThoughts := IsThinkingModel(mapped)
	if thinking != nil && strings.EqualFold(thinking.Type, "enabled") {
		includeThoughts = true
	}
	if thinking != nil && (strings.EqualFold(thinking.Type, "disabled") || strings.EqualFold(thinking.Type, "none")) {
		includeThoughts = false
	}
	if includeThoughts && !IsImageModel(mapped) {
		budget := DefaultThinkingBudget(mapped)
		if thinking != nil && thinking.BudgetTokens != nil {
			budget = *thinking.BudgetTokens
		}
		gen["thinkingConfig"] = map[string]any{
			"includeThoughts": true,
			"thinkingBudget":  budget,
		}
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
	if decls := openaiTools(req.Tools); len(decls) > 0 {
		inner.Tools = []any{map[string]any{"functionDeclarations": decls}}
		inner.ToolConfig = map[string]any{
			"functionCallingConfig": map[string]any{"mode": openaiToolMode(req.ToolChoice)},
		}
	}
	return Wrap(projectID, mapped, email, SessionID(accountID, ""), inner, IsImageModel(mapped)), mapped, req.Stream
}

func openaiToolMode(choice any) string {
	switch t := choice.(type) {
	case string:
		switch strings.ToLower(t) {
		case "none":
			return "NONE"
		case "required":
			return "ANY"
		}
	}
	return "AUTO"
}

func openaiTools(tools []any) []any {
	decls := make([]any, 0)
	var walk func([]any)
	walk = func(list []any) {
		for _, tool := range list {
			m := AsMap(tool)
			if m == nil {
				continue
			}
			typ := AsString(m["type"])
			if typ == "namespace" {
				if sub := AsSlice(m["tools"]); len(sub) > 0 {
					walk(sub)
				}
				continue
			}
			fn := AsMap(m["function"])
			name := AsString(m["name"])
			desc := AsString(m["description"])
			schema := m["parameters"]
			if fn != nil {
				if name == "" {
					name = AsString(fn["name"])
				}
				if desc == "" {
					desc = AsString(fn["description"])
				}
				if schema == nil {
					schema = fn["parameters"]
				}
			}
			if name == "" {
				continue
			}
			decl := map[string]any{"name": name}
			if desc != "" {
				decl["description"] = desc
			}
			if schema != nil {
				decl["parameters"] = schema
			}
			decls = append(decls, decl)
		}
	}
	walk(tools)
	return decls
}

func contentToParts(content any) []any {
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
			typ := AsString(m["type"])
			switch typ {
			case "text", "input_text", "output_text":
				if t := AsString(m["text"]); t != "" {
					parts = append(parts, map[string]any{"text": t})
				}
			case "image_url", "input_image":
				url := AsString(GetPath(m, "image_url", "url"))
				if url == "" {
					url = AsString(m["image_url"])
				}
				if url == "" {
					url = AsString(m["url"])
				}
				if p := imagePart(url); p != nil {
					parts = append(parts, p)
				}
			default:
				if t := AsString(m["text"]); t != "" {
					parts = append(parts, map[string]any{"text": t})
				}
			}
		}
	case map[string]any:
		if t := AsString(c["text"]); t != "" {
			parts = append(parts, map[string]any{"text": t})
		}
	}
	return parts
}

func imagePart(url string) map[string]any {
	if strings.HasPrefix(url, "data:") {
		mime := "image/jpeg"
		data := url
		if i := strings.Index(url, ","); i >= 0 {
			head := url[5:i]
			data = url[i+1:]
			if j := strings.Index(head, ";"); j >= 0 {
				mime = head[:j]
			} else {
				mime = head
			}
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}}
	}
	if strings.HasPrefix(url, "http") {
		return map[string]any{"fileData": map[string]any{"fileUri": url, "mimeType": "image/jpeg"}}
	}
	return nil
}

func extractText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, item := range c {
			m := AsMap(item)
			if m == nil {
				continue
			}
			if t := AsString(m["text"]); t != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(t)
			}
		}
		return b.String()
	case map[string]any:
		return AsString(c["text"])
	default:
		return ""
	}
}

func responsesToMessages(instructions string, input any) []OpenAIMessage {
	msgs := make([]OpenAIMessage, 0)
	if instructions != "" {
		msgs = append(msgs, OpenAIMessage{Role: "system", Content: instructions})
	}
	switch in := input.(type) {
	case string:
		msgs = append(msgs, OpenAIMessage{Role: "user", Content: in})
	case []any:
		for _, item := range in {
			m := AsMap(item)
			if m == nil {
				if s, ok := item.(string); ok {
					msgs = append(msgs, OpenAIMessage{Role: "user", Content: s})
				}
				continue
			}
			typ := AsString(m["type"])
			role := AsString(m["role"])
			switch typ {
			case "message", "":
				if role == "" {
					role = "user"
				}
				msgs = append(msgs, OpenAIMessage{Role: role, Content: m["content"]})
			case "function_call":
				args := m["arguments"]
				argStr := ""
				switch a := args.(type) {
				case string:
					argStr = a
				default:
					b, _ := json.Marshal(a)
					argStr = string(b)
				}
				msgs = append(msgs, OpenAIMessage{
					Role: "assistant",
					ToolCalls: []any{map[string]any{
						"id":   AsString(m["id"]),
						"type": "function",
						"function": map[string]any{
							"name":      AsString(m["name"]),
							"arguments": argStr,
						},
					}},
				})
			case "function_call_output":
				msgs = append(msgs, OpenAIMessage{
					Role:       "tool",
					ToolCallID: firstNonEmpty(AsString(m["call_id"]), AsString(m["id"])),
					Name:       AsString(m["name"]),
					Content:    AsString(m["output"]),
				})
			}
		}
	}
	return msgs
}

func GeminiToOpenAI(model string, raw []byte, streamID string) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	data := envelope
	if r := AsMap(envelope["response"]); r != nil {
		data = r
	}
	text, thinking, toolCalls, finish, usage := collectParts(data)
	msg := map[string]any{"role": "assistant", "content": text}
	if thinking != "" {
		msg["reasoning_content"] = thinking
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		if text == "" {
			msg["content"] = nil
		}
	}
	out := map[string]any{
		"id":      streamID,
		"object":  "chat.completion",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": openaiFinish(finish, len(toolCalls) > 0),
		}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

func collectParts(data map[string]any) (text, thinking string, toolCalls []any, finish string, usage any) {
	cands := AsSlice(data["candidates"])
	if len(cands) == 0 {
		return
	}
	cand := AsMap(cands[0])
	if cand == nil {
		return
	}
	finish = AsString(cand["finishReason"])
	content := AsMap(cand["content"])
	if content != nil {
		for _, p := range AsSlice(content["parts"]) {
			part := AsMap(p)
			if part == nil {
				continue
			}
			if fc := AsMap(part["functionCall"]); fc != nil {
				args, _ := json.Marshal(fc["args"])
				id := AsString(fc["id"])
				if id == "" {
					id = fmt.Sprintf("call_%d", len(toolCalls)+1)
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      AsString(fc["name"]),
						"arguments": string(args),
					},
				})
				continue
			}
			t := AsString(part["text"])
			if t == "" {
				continue
			}
			if thought, _ := part["thought"].(bool); thought {
				thinking += t
			} else {
				text += t
			}
		}
	}
	if u := data["usageMetadata"]; u != nil {
		usage = geminiUsageToOpenAI(u)
	}
	return
}

func geminiUsageToOpenAI(u any) map[string]any {
	m := AsMap(u)
	if m == nil {
		return nil
	}
	num := func(keys ...string) int {
		for _, k := range keys {
			switch v := m[k].(type) {
			case float64:
				return int(v)
			case int:
				return v
			case json.Number:
				n, _ := v.Int64()
				return int(n)
			}
		}
		return 0
	}
	prompt := num("total_input_tokens", "promptTokenCount")
	outTok := num("total_output_tokens", "candidatesTokenCount")
	thought := num("total_thought_tokens", "thoughtsTokenCount")
	tool := num("total_tool_use_tokens")
	if m["total_output_tokens"] != nil {
		outTok = outTok + thought + tool
	}
	total := num("total_tokens", "totalTokenCount")
	if total == 0 {
		total = prompt + outTok
	}
	usage := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": outTok,
		"total_tokens":      total,
	}
	if thought > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": thought}
	}
	return usage
}

func openaiFinish(reason string, hasTools bool) string {
	switch strings.ToUpper(reason) {
	case "STOP":
		if hasTools {
			return "tool_calls"
		}
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		if hasTools {
			return "tool_calls"
		}
		if reason == "" {
			return "stop"
		}
		return strings.ToLower(reason)
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
