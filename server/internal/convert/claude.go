package convert

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ClaudeRequest struct {
	Model         string          `json:"model"`
	Messages      []any           `json:"messages"`
	System        any             `json:"system"`
	Tools         []any           `json:"tools"`
	Stream        bool            `json:"stream"`
	MaxTokens     *int            `json:"max_tokens"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	TopK          *int            `json:"top_k"`
	Thinking      *OpenAIThinking `json:"thinking"`
	ToolChoice    any             `json:"tool_choice"`
	StopSequences any             `json:"stop_sequences"`
	OutputConfig  any             `json:"output_config"`
	Metadata      any             `json:"metadata"`
	Size          string          `json:"size"`
	Quality       string          `json:"quality"`
	ImageSize     string          `json:"imageSize"`
}

func ClaudeToGemini(req ClaudeRequest, projectID, email, accountID string) (OuterRequest, string, bool) {
	return ClaudeToGeminiWithModel(req, projectID, email, accountID, "")
}
func ClaudeToGeminiWithModel(req ClaudeRequest, projectID, email, accountID, finalModel string) (OuterRequest, string, bool) {
	effort := AsString(GetPath(req.OutputConfig, "effort"))
	mapped := requestModel(req.Model, finalModel, req.Thinking, effort)
	tools := flattenTools(req.Tools, "")
	names := map[string]string{}
	for _, v := range req.Messages {
		m := AsMap(v)
		for _, v := range AsSlice(m["content"]) {
			b := AsMap(v)
			if AsString(b["type"]) == "tool_use" {
				names[AsString(b["id"])] = resolveDeclaredToolName(AsString(b["name"]), tools)
			}
		}
	}
	var contents []map[string]any
	for _, v := range req.Messages {
		m := AsMap(v)
		if m == nil {
			continue
		}
		role := "user"
		if AsString(m["role"]) == "assistant" {
			role = "model"
		}
		parts := claudeParts(m["content"], role == "model", names, mapped)
		if len(parts) > 0 {
			contents = append(contents, map[string]any{"role": role, "parts": parts})
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
	if req.MaxTokens != nil {
		gen["maxOutputTokens"] = *req.MaxTokens
	}
	applyThinking(gen, mapped, req.Thinking, effort)
	applyVariantGeneration(gen, req.Model, mapped)
	setStop(gen, req.StopSequences)
	if format := AsMap(GetPath(req.OutputConfig, "format")); format != nil && AsString(format["type"]) == "json_schema" {
		gen["responseMimeType"] = "application/json"
		gen["responseSchema"] = CleanSchema(format["schema"])
	}
	session := SessionID(accountID, AsString(GetPath(req.Metadata, "user_id")))
	inner := InnerRequest{Contents: mergeContents(contents), GenerationConfig: gen, SafetySettings: SafetyOff(), SessionID: session}
	if parts := claudeSystemParts(req.System); len(parts) > 0 {
		inner.SystemInstruction = map[string]any{"role": "system", "parts": parts}
	}
	attachTools(&inner, claudeTools(tools), tools, req.ToolChoice, req.Model)
	if IsImageModel(mapped) {
		imageGenerationConfig(gen, req.Model, req.Size, req.Quality, req.ImageSize)
	}
	return Wrap(projectID, mapped, email, session, inner, IsImageModel(mapped)), mapped, req.Stream
}

const claudeAgentSDKIdentity = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
const claudeCodeCLIIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

func claudeSystemParts(system any) []any {
	var texts []string
	switch value := system.(type) {
	case string:
		texts = append(texts, value)
	case map[string]any:
		texts = append(texts, AsString(value["text"]))
	default:
		for _, block := range AsSlice(system) {
			texts = append(texts, AsString(GetPath(block, "text")))
		}
	}
	var parts []any
	for _, text := range texts {
		if text == "" {
			continue
		}
		// Match only the standalone SDK identity, before joining system blocks.
		// The demo documents RESOURCE_EXHAUSTED for this exact client identity.
		if text == claudeAgentSDKIdentity {
			text = claudeCodeCLIIdentity
		}
		parts = append(parts, map[string]any{"text": text})
	}
	return parts
}

func claudeContentToParts(content any, isModel bool) []any {
	return claudeParts(content, isModel, nil, "")
}
func claudeParts(content any, isModel bool, names map[string]string, model string) []any {
	if _, ok := content.(string); ok {
		return contentToParts(content)
	}
	var parts []any
	lastSignature := ""
	for _, v := range AsSlice(content) {
		m := AsMap(v)
		if m == nil {
			continue
		}
		switch AsString(m["type"]) {
		case "thinking":
			lastSignature = thoughtSignature(m)
			if t := AsString(m["thinking"]); t != "" {
				parts = append(parts, withSignature(map[string]any{"text": t, "thought": true}, lastSignature))
			}
		case "redacted_thinking":
			if data := AsString(m["data"]); data != "" {
				parts = append(parts, map[string]any{"text": "[Redacted thinking: " + data + "]"})
			}
		case "tool_use":
			if !isModel {
				continue
			}
			id := AsString(m["id"])
			name := firstNonEmpty(names[id], normalizeToolName(AsString(m["name"])))
			fc := map[string]any{"name": name, "args": jsonArguments(m["input"])}
			if id != "" {
				fc["id"] = id
			}
			sig := firstNonEmpty(thoughtSignature(m), lastSignature, RecallToolSignature(model, id))
			parts = append(parts, withSignature(map[string]any{"functionCall": fc}, sig))
		case "tool_result":
			id := AsString(m["tool_use_id"])
			name := firstNonEmpty(names[id], normalizeToolName(AsString(m["name"])), id, "tool")
			result, media := partsTextAndMedia(m["content"])
			response := map[string]any{"result": result}
			if isError, _ := m["is_error"].(bool); isError {
				response["error"] = result
			}
			fr := map[string]any{"name": name, "response": response}
			if id != "" {
				fr["id"] = id
			}
			parts = append(parts, map[string]any{"functionResponse": fr})
			parts = append(parts, media...)
		default:
			parts = append(parts, contentToParts([]any{m})...)
		}
	}
	return parts
}
func claudeTools(tools []any) []any { return openaiTools(tools) }

func GeminiToClaude(model string, raw []byte) ([]byte, error) {
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
	content := make([]any, 0)
	finish := ""
	tools := false
	lastThinking := -1
	cands := AsSlice(data["candidates"])
	if len(cands) == 0 {
		return nil, streamFailure("empty_response", "upstream returned no candidates")
	}
	if len(cands) > 0 {
		cand := AsMap(cands[0])
		finish = AsString(cand["finishReason"])
		for _, v := range AsSlice(GetPath(cand, "content", "parts")) {
			p := AsMap(v)
			if p == nil {
				continue
			}
			sig := thoughtSignature(p)
			if tc := partToToolCall(p); tc != nil {
				fn := AsMap(tc["function"])
				block := map[string]any{"type": "tool_use", "id": tc["id"], "name": fn["name"], "input": jsonArguments(fn["arguments"])}
				if sig != "" {
					block["signature"] = sig
					RememberToolSignature(model, AsString(tc["id"]), sig)
				}
				content = append(content, block)
				tools = true
				lastThinking = -1
				continue
			}
			if text := AsString(p["text"]); text != "" {
				if partIsThought(p) {
					block := map[string]any{"type": "thinking", "thinking": text}
					if sig != "" {
						block["signature"] = sig
					}
					content = append(content, block)
					lastThinking = len(content) - 1
				} else {
					content = append(content, map[string]any{"type": "text", "text": text})
					lastThinking = -1
				}
			}
			if sig != "" && lastThinking >= 0 {
				AsMap(content[lastThinking])["signature"] = sig
			}
			inline := AsMap(p["inlineData"])
			if inline == nil {
				inline = AsMap(p["inline_data"])
			}
			if inline != nil {
				mt := firstNonEmpty(AsString(inline["mimeType"]), AsString(inline["mime_type"]), "image/png")
				if strings.HasPrefix(mt, "image/") {
					content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": inline["data"]}})
				} else if t := imagePartText(p); t != "" {
					content = append(content, map[string]any{"type": "text", "text": t})
				}
			}
		}
	}
	stop := "end_turn"
	if tools {
		stop = "tool_use"
	}
	switch strings.ToUpper(finish) {
	case "MAX_TOKENS":
		stop = "max_tokens"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT":
		stop = "refusal"
	}
	usage := data["usageMetadata"]
	if usage == nil {
		usage = envelope["usageMetadata"]
	}
	return json.Marshal(map[string]any{"id": "msg_" + uuid.NewString(), "type": "message", "role": "assistant", "model": model, "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": claudeUsage(geminiUsageToOpenAI(usage))})
}
func claudeUsage(u any) map[string]any {
	usage := TokenUsageFromOpenAI(u)
	input := usage.Input - usage.Cache
	if input < 0 {
		input = 0
	}
	out := map[string]any{"input_tokens": input, "output_tokens": usage.Output}
	if usage.Cache > 0 {
		out["cache_read_input_tokens"] = usage.Cache
	}
	return out
}
func nowUnix() int64 { return time.Now().Unix() }
