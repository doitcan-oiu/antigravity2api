package convert

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
)

type OpenAIRequest struct {
	Model               string          `json:"model"`
	Messages            []OpenAIMessage `json:"messages"`
	Input               any             `json:"input"`
	Prompt              any             `json:"prompt"`
	Instructions        string          `json:"instructions"`
	PreviousResponseID  string          `json:"previous_response_id"`
	Tools               []any           `json:"tools"`
	Stream              bool            `json:"stream"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	TopK                *int            `json:"top_k"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	MaxOutputTokens     *int            `json:"max_output_tokens"`
	Thinking            *OpenAIThinking `json:"thinking"`
	Reasoning           *OpenAIThinking `json:"reasoning"`
	ReasoningEffort     string          `json:"reasoning_effort"`
	ToolChoice          any             `json:"tool_choice"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls"`
	Stop                any             `json:"stop"`
	N                   *int            `json:"n"`
	Seed                *int64          `json:"seed"`
	PresencePenalty     *float64        `json:"presence_penalty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty"`
	ResponseFormat      any             `json:"response_format"`
	Text                any             `json:"text"`
	Size                string          `json:"size"`
	Quality             string          `json:"quality"`
	ImageSize           string          `json:"imageSize"`
	SessionID           string          `json:"session_id"`
}

type OpenAIThinking struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens"`
	Effort       string `json:"effort"`
}

func (t *OpenAIThinking) UnmarshalJSON(raw []byte) error {
	type plain OpenAIThinking
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.BudgetTokens == nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for _, key := range []string{"budgetTokens", "budget"} {
			if v := fields[key]; len(v) > 0 {
				if err := json.Unmarshal(v, &value.BudgetTokens); err != nil {
					return err
				}
				break
			}
		}
	}
	*t = OpenAIThinking(value)
	return nil
}

type OpenAIMessage struct {
	Role               string `json:"role"`
	Content            any    `json:"content"`
	Name               string `json:"name"`
	ToolCallID         string `json:"tool_call_id"`
	ToolCalls          []any  `json:"tool_calls"`
	ReasoningContent   string `json:"reasoning_content"`
	ReasoningSignature string `json:"reasoning_signature,omitempty"`
	Signature          string `json:"signature,omitempty"`
	ExtraContent       any    `json:"extra_content,omitempty"`
}

func OpenAIToGemini(req OpenAIRequest, projectID, email, accountID string) (OuterRequest, string, bool) {
	return OpenAIToGeminiWithModel(req, projectID, email, accountID, "")
}

// WithModel uses the account-resolved model as-is, so budgets and signatures are
// normalized for the model that will actually receive the request.
func OpenAIToGeminiWithModel(req OpenAIRequest, projectID, email, accountID, finalModel string) (OuterRequest, string, bool) {
	thinking := req.Thinking
	if thinking == nil {
		thinking = req.Reasoning
	}
	effort := req.ReasoningEffort
	if thinking != nil && thinking.Effort != "" {
		effort = thinking.Effort
	}
	mapped := requestModel(req.Model, finalModel, thinking, effort)
	msgs := req.Messages
	if len(msgs) == 0 && req.Input != nil {
		msgs = responsesToMessages("", req.Input)
	}
	if len(msgs) == 0 && req.Prompt != nil {
		msgs = responsesToMessages("", req.Prompt)
	}
	tools := flattenTools(req.Tools, "")
	names := map[string]string{}
	for _, msg := range msgs {
		for _, v := range msg.ToolCalls {
			m := AsMap(v)
			names[toolID(m)] = resolveDeclaredToolName(toolName(m), tools)
		}
	}
	var systems []string
	if req.Instructions != "" {
		systems = append(systems, req.Instructions)
	}
	var contents []map[string]any
	for _, msg := range msgs {
		role := strings.ToLower(msg.Role)
		if role == "system" || role == "developer" {
			if t := extractText(msg.Content); t != "" {
				systems = append(systems, t)
			}
			continue
		}
		var parts []any
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		if role == "tool" || role == "function" {
			name := firstNonEmpty(names[msg.ToolCallID], resolveDeclaredToolName(msg.Name, tools), msg.ToolCallID, "tool")
			result, media := partsTextAndMedia(msg.Content)
			fr := map[string]any{"name": name, "response": map[string]any{"result": result}}
			if msg.ToolCallID != "" {
				fr["id"] = msg.ToolCallID
			}
			parts = append(parts, map[string]any{"functionResponse": fr})
			parts = append(parts, media...)
		} else {
			sig := firstNonEmpty(msg.ReasoningSignature, msg.Signature, AsString(GetPath(msg.ExtraContent, "google", "thought_signature")))
			if msg.ReasoningContent != "" {
				parts = append(parts, withSignature(map[string]any{"text": msg.ReasoningContent, "thought": true}, sig))
			}
			parts = append(parts, contentToParts(msg.Content)...)
			for _, v := range msg.ToolCalls {
				m := AsMap(v)
				if m == nil {
					continue
				}
				name := resolveDeclaredToolName(toolName(m), tools)
				args := GetPath(m, "function", "arguments")
				if args == nil {
					args = m["arguments"]
				}
				if AsString(m["type"]) == "custom" || AsString(m["type"]) == "custom_tool_call" {
					args = map[string]any{"input": m["input"]}
				}
				if AsString(m["type"]) == "local_shell_call" {
					name = "shell"
					args = GetPath(m, "action", "exec")
					if args == nil {
						args = m["action"]
					}
				}
				if op := m["operation"]; op != nil {
					name = "apply_patch"
					args = op
				}
				if name == "" {
					continue
				}
				fc := map[string]any{"name": name, "args": jsonArguments(args)}
				if id := toolID(m); id != "" {
					fc["id"] = id
				}
				parts = append(parts, withSignature(map[string]any{"functionCall": fc}, firstNonEmpty(thoughtSignature(m), sig, RecallToolSignature(mapped, toolID(m)))))
			}
		}
		if len(parts) > 0 {
			contents = append(contents, map[string]any{"role": geminiRole, "parts": parts})
		}
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
	if req.TopK != nil {
		gen["topK"] = *req.TopK
	}
	maxTokens := req.MaxOutputTokens
	if maxTokens == nil {
		maxTokens = req.MaxCompletionTokens
	}
	if maxTokens == nil {
		maxTokens = req.MaxTokens
	}
	if maxTokens != nil {
		gen["maxOutputTokens"] = *maxTokens
	}
	if req.N != nil {
		gen["candidateCount"] = *req.N
	}
	if req.Seed != nil {
		gen["seed"] = *req.Seed
	}
	if req.PresencePenalty != nil {
		gen["presencePenalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		gen["frequencyPenalty"] = *req.FrequencyPenalty
	}
	setStop(gen, req.Stop)
	applyThinking(gen, mapped, thinking, effort)
	format := AsMap(req.ResponseFormat)
	if format == nil {
		format = AsMap(GetPath(req.Text, "format"))
	}
	if format != nil {
		switch AsString(format["type"]) {
		case "json_object":
			gen["responseMimeType"] = "application/json"
		case "json_schema":
			gen["responseMimeType"] = "application/json"
			schema := GetPath(format, "json_schema", "schema")
			if schema == nil {
				schema = format["schema"]
			}
			if schema != nil {
				gen["responseSchema"] = CleanSchema(schema)
			}
		}
	}
	inner := InnerRequest{Contents: mergeContents(contents), GenerationConfig: gen, SafetySettings: SafetyOff(), SessionID: SessionID(accountID, req.SessionID)}
	if len(systems) > 0 {
		inner.SystemInstruction = map[string]any{"role": "system", "parts": []any{map[string]any{"text": strings.Join(systems, "\n\n")}}}
	}
	attachTools(&inner, openaiTools(tools), tools, req.ToolChoice, req.Model)
	if IsImageModel(mapped) {
		imageGenerationConfig(gen, req.Model, req.Size, req.Quality, req.ImageSize)
	}
	return Wrap(projectID, mapped, email, SessionID(accountID, req.SessionID), inner, IsImageModel(mapped)), mapped, req.Stream
}

func openaiToolMode(choice any) string { return AsString(functionCallingConfig(choice)["mode"]) }

func openaiTools(tools []any) []any {
	var decls []any
	seen := map[string]bool{}
	for _, v := range flattenTools(tools, "") {
		m := AsMap(v)
		if m == nil || isSearchTool(m) {
			continue
		}
		fn := AsMap(m["function"])
		if fn == nil {
			fn = m
		}
		name := normalizeToolName(toolName(m))
		if name == "" {
			switch AsString(m["type"]) {
			case "local_shell", "local_shell_call":
				name = "shell"
			case "apply_patch":
				name = "apply_patch"
			}
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		decl := map[string]any{"name": name}
		if d := firstNonEmpty(AsString(fn["description"]), AsString(m["description"])); d != "" {
			decl["description"] = d
		}
		schema := fn["parameters"]
		if schema == nil {
			schema = m["input_schema"]
		}
		if name == "shell" && schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "workdir": map[string]any{"type": "string"}}, "required": []any{"command"}}
		}
		if AsString(m["type"]) == "custom" || (name == "apply_patch" && schema == nil) {
			schema = map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string", "description": "Exact freeform tool input"}}, "required": []any{"input"}}
		}
		decl["parameters"] = CleanSchema(schema)
		decls = append(decls, decl)
	}
	return decls
}

func contentToParts(content any) []any {
	var parts []any
	switch c := content.(type) {
	case string:
		if c != "" {
			parts = append(parts, map[string]any{"text": c})
		}
	case map[string]any:
		return contentToParts([]any{c})
	case []any:
		for _, v := range c {
			m := AsMap(v)
			if m == nil {
				continue
			}
			var part map[string]any
			switch AsString(m["type"]) {
			case "text", "input_text", "output_text":
				if t := AsString(m["text"]); t != "" {
					part = map[string]any{"text": t}
				}
			case "thinking", "reasoning":
				if t := firstNonEmpty(AsString(m["thinking"]), AsString(m["text"])); t != "" {
					part = withSignature(map[string]any{"text": t, "thought": true}, thoughtSignature(m))
				}
			case "image_url", "input_image":
				part = imagePart(firstNonEmpty(AsString(GetPath(m, "image_url", "url")), AsString(m["image_url"]), AsString(m["url"])))
			case "image", "document":
				src := AsMap(m["source"])
				mt := AsString(src["media_type"])
				if mt == "" {
					if AsString(m["type"]) == "document" {
						mt = "application/pdf"
					} else {
						mt = "image/jpeg"
					}
				}
				if AsString(src["type"]) == "base64" {
					part = map[string]any{"inlineData": map[string]any{"mimeType": mt, "data": src["data"]}}
				} else if AsString(src["type"]) == "text" {
					part = map[string]any{"text": src["data"]}
				} else {
					part = mediaPart(AsString(src["url"]), mt)
				}
			case "input_audio", "audio":
				a := AsMap(m["input_audio"])
				if a == nil {
					a = AsMap(m["audio"])
				}
				data := AsString(a["data"])
				mt := audioMIME(AsString(a["format"]))
				if strings.HasPrefix(data, "data:") {
					part = mediaPart(data, mt)
				} else if data != "" {
					part = map[string]any{"inlineData": map[string]any{"mimeType": mt, "data": data}}
				}
			case "audio_url", "video_url":
				key := AsString(m["type"])
				a := AsMap(m[key])
				src := firstNonEmpty(AsString(a["url"]), AsString(m[key]))
				mt := firstNonEmpty(AsString(a["mime_type"]), AsString(a["mimeType"]))
				if mt == "" {
					if key == "audio_url" {
						mt = "audio/mpeg"
					} else {
						mt = "video/mp4"
					}
				}
				part = mediaPart(src, mt)
			case "input_file", "file":
				f := AsMap(m["file"])
				if f == nil {
					f = m
				}
				data := AsString(f["file_data"])
				if data != "" {
					part = mediaPart(data, "application/pdf")
				} else {
					part = mediaPart(AsString(f["file_url"]), "application/pdf")
				}
			default:
				if t := AsString(m["text"]); t != "" {
					part = map[string]any{"text": t}
				}
			}
			if part != nil {
				parts = append(parts, part)
			}
		}
	}
	return parts
}
func imagePart(source string) map[string]any { return mediaPart(source, "image/jpeg") }

func extractText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, v := range c {
			if m := AsMap(v); m != nil {
				if s := AsString(m["text"]); s != "" {
					texts = append(texts, s)
				}
			}
		}
		return strings.Join(texts, "\n")
	case map[string]any:
		return AsString(c["text"])
	}
	return ""
}

func responsesToMessages(instructions string, input any) []OpenAIMessage {
	var msgs []OpenAIMessage
	if instructions != "" {
		msgs = append(msgs, OpenAIMessage{Role: "system", Content: instructions})
	}
	if s, ok := input.(string); ok {
		return append(msgs, OpenAIMessage{Role: "user", Content: s})
	}
	for _, v := range AsSlice(input) {
		m := AsMap(v)
		if m == nil {
			continue
		}
		typ := AsString(m["type"])
		switch typ {
		case "message", "":
			role := firstNonEmpty(AsString(m["role"]), "user")
			msgs = append(msgs, OpenAIMessage{Role: role, Content: cloneValue(m["content"])})
		case "reasoning":
			var thoughts []string
			for _, v := range AsSlice(m["summary"]) {
				if s := AsString(AsMap(v)["text"]); s != "" {
					thoughts = append(thoughts, s)
				}
			}
			if len(thoughts) > 0 {
				msgs = append(msgs, OpenAIMessage{Role: "assistant", ReasoningContent: strings.Join(thoughts, "\n"), Signature: thoughtSignature(m)})
			}
		case "function_call", "custom_tool_call", "local_shell_call", "web_search_call":
			name := AsString(m["name"])
			args := m["arguments"]
			if typ == "custom_tool_call" {
				args = map[string]any{"input": m["input"]}
			}
			if typ == "local_shell_call" {
				name = "shell"
				args = GetPath(m, "action", "exec")
				if args == nil {
					args = m["action"]
				}
			}
			if typ == "web_search_call" {
				name = "google_search"
				args = m["action"]
			}
			var argString string
			if s, ok := args.(string); ok {
				argString = s
			} else {
				raw, _ := json.Marshal(args)
				argString = string(raw)
			}
			tc := map[string]any{"id": toolID(m), "type": "function", "function": map[string]any{"name": normalizeToolName(name), "arguments": argString}}
			if sig := thoughtSignature(m); sig != "" {
				tc["signature"] = sig
			}
			msgs = append(msgs, OpenAIMessage{Role: "assistant", ToolCalls: []any{tc}})
		case "function_call_output", "custom_tool_call_output":
			msgs = append(msgs, OpenAIMessage{Role: "tool", ToolCallID: toolID(m), Name: AsString(m["name"]), Content: cloneValue(m["output"])})
		}
	}
	return msgs
}

func GeminiToOpenAI(model string, raw []byte, streamID string) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if err := payloadError(envelope); err != nil {
		return nil, err
	}
	data := envelope
	if r := AsMap(envelope["response"]); r != nil {
		data = r
	}
	if err := payloadError(data); err != nil {
		return nil, err
	}
	var choices []any
	for index, v := range AsSlice(data["candidates"]) {
		cand := AsMap(v)
		if cand == nil {
			continue
		}
		text, thinking, calls, finish, _ := collectParts(map[string]any{"candidates": []any{cand}})
		msg := map[string]any{"role": "assistant", "content": text}
		if thinking != "" {
			msg["reasoning_content"] = thinking
		}
		for _, v := range AsSlice(GetPath(cand, "content", "parts")) {
			p := AsMap(v)
			if sig := thoughtSignature(p); sig != "" && p["functionCall"] == nil {
				msg["reasoning_signature"] = sig
			}
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
			if text == "" {
				msg["content"] = nil
			}
			for _, v := range calls {
				tc := AsMap(v)
				RememberToolSignature(model, AsString(tc["id"]), thoughtSignature(tc))
			}
		}
		i := index
		if cand["index"] != nil {
			i = intVal(cand["index"])
		}
		choice := map[string]any{"index": i, "message": msg, "finish_reason": openaiFinish(finish, len(calls) > 0)}
		if g := cand["groundingMetadata"]; g != nil {
			choice["grounding_metadata"] = g
		}
		choices = append(choices, choice)
	}
	if len(choices) == 0 {
		return nil, streamFailure("empty_response", "upstream returned no candidates")
	}
	out := map[string]any{"id": streamID, "object": "chat.completion", "created": nowUnix(), "model": model, "choices": choices}
	if u := data["usageMetadata"]; u != nil {
		out["usage"] = geminiUsageToOpenAI(u)
	} else if u := envelope["usageMetadata"]; u != nil {
		out["usage"] = geminiUsageToOpenAI(u)
	}
	if pf := data["promptFeedback"]; pf != nil {
		out["prompt_feedback"] = pf
	}
	return json.Marshal(out)
}

func partToToolCall(part map[string]any) map[string]any {
	fc := AsMap(part["functionCall"])
	if fc == nil {
		return nil
	}
	id := firstNonEmpty(AsString(fc["call_id"]), AsString(fc["id"]))
	if id == "" {
		id = "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	args := fc["args"]
	if args == nil {
		args = map[string]any{}
	}
	raw, _ := json.Marshal(args)
	tc := map[string]any{"id": id, "type": "function", "function": map[string]any{"name": AsString(fc["name"]), "arguments": string(raw)}}
	if sig := thoughtSignature(part); sig != "" {
		tc["signature"] = sig
		tc["extra_content"] = map[string]any{"google": map[string]any{"thought_signature": sig}}
	}
	return tc
}
func imagePartText(part map[string]any) string {
	inline := AsMap(part["inlineData"])
	if inline == nil {
		inline = AsMap(part["inline_data"])
	}
	if inline != nil {
		mt := firstNonEmpty(AsString(inline["mimeType"]), AsString(inline["mime_type"]), "image/png")
		data := AsString(inline["data"])
		if data != "" {
			return "\n![generated media](data:" + mt + ";base64," + data + ")\n"
		}
	}
	if f := AsMap(part["fileData"]); f != nil {
		if uri := firstNonEmpty(AsString(f["fileUri"]), AsString(f["file_uri"])); uri != "" {
			return "\n![generated media](" + uri + ")\n"
		}
	}
	return ""
}
func collectParts(data map[string]any) (text, thinking string, toolCalls []any, finish string, usage any) {
	if u := data["usageMetadata"]; u != nil {
		usage = geminiUsageToOpenAI(u)
	}
	cands := AsSlice(data["candidates"])
	if len(cands) == 0 {
		return
	}
	cand := AsMap(cands[0])
	if cand == nil {
		return
	}
	finish = AsString(cand["finishReason"])
	for _, v := range AsSlice(GetPath(cand, "content", "parts")) {
		p := AsMap(v)
		if p == nil {
			continue
		}
		if tc := partToToolCall(p); tc != nil {
			toolCalls = append(toolCalls, tc)
			continue
		}
		if t := AsString(p["text"]); t != "" {
			if partIsThought(p) {
				thinking += t
			} else {
				text += t
			}
		}
		text += imagePartText(p)
	}
	return
}

type TokenUsage struct {
	Input     int
	Output    int
	Cache     int
	Reasoning int
}

func (u TokenUsage) Empty() bool {
	return u.Input == 0 && u.Output == 0 && u.Cache == 0 && u.Reasoning == 0
}

func intVal(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func TokenUsageFromOpenAI(usage any) TokenUsage {
	m := AsMap(usage)
	if m == nil {
		return TokenUsage{}
	}
	u := TokenUsage{
		Input:  intVal(m["prompt_tokens"]),
		Output: intVal(m["completion_tokens"]),
	}
	if d := AsMap(m["prompt_tokens_details"]); d != nil {
		u.Cache = intVal(d["cached_tokens"])
	}
	if u.Cache == 0 {
		u.Cache = intVal(m["cached_tokens"])
	}
	if d := AsMap(m["completion_tokens_details"]); d != nil {
		u.Reasoning = intVal(d["reasoning_tokens"])
	}
	if u.Reasoning == 0 {
		u.Reasoning = intVal(m["reasoning_tokens"])
	}
	return u
}

func completionTokens(usage any) int {
	return TokenUsageFromOpenAI(usage).Output
}

func UsageFromGemini(raw []byte) TokenUsage {
	if len(raw) == 0 {
		return TokenUsage{}
	}
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) != nil {
		return TokenUsage{}
	}
	if u := usageFromPayload(envelope); !u.Empty() {
		return u
	}
	if r := AsMap(envelope["response"]); r != nil {
		return usageFromPayload(r)
	}
	return TokenUsage{}
}

func CompletionTokensFromGemini(raw []byte) int {
	return UsageFromGemini(raw).Output
}

func usageFromPayload(data map[string]any) TokenUsage {
	_, _, _, _, usage := collectParts(data)
	return TokenUsageFromOpenAI(usage)
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
	candidates := num("candidatesTokenCount")
	outTok := candidates
	if outTok == 0 {
		outTok = num("total_output_tokens")
	}
	thought := num("total_thought_tokens", "thoughtsTokenCount")
	tool := num("total_tool_use_tokens")
	if m["total_output_tokens"] != nil {
		outTok = num("total_output_tokens") + thought + tool
	}
	total := num("total_tokens", "totalTokenCount")
	if total == 0 {
		total = prompt + outTok
	}
	cache := num("cachedContentTokenCount", "cachedTokens", "cached_content_token_count", "cache_read_input_tokens", "cached_input_tokens")
	if cache == 0 {
		if d := AsMap(m["promptTokensDetails"]); d != nil {
			cache = intVal(d["cachedContentTokenCount"])
			if cache == 0 {
				cache = intVal(d["cachedTokens"])
			}
		}
	}
	usage := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": outTok,
		"total_tokens":      total,
	}
	if thought > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": thought}
	}
	if cache > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": cache}
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
