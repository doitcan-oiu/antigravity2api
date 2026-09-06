package convert

import (
	"encoding/json"
	"strings"
)

func NativeGeminiToInternal(body map[string]any, model, projectID, email, accountID string) (OuterRequest, string, bool) {
	return NativeGeminiToInternalWithModel(body, model, projectID, email, accountID, "")
}
func NativeGeminiToInternalWithModel(body map[string]any, model, projectID, email, accountID, finalModel string) (OuterRequest, string, bool) {
	body = AsMap(cloneValue(body))
	innerMap := body
	if req := AsMap(body["request"]); req != nil {
		innerMap = req
		if m := AsString(body["model"]); m != "" {
			model = m
		}
	}
	mapped := finalModel
	if mapped == "" {
		mapped = MapModel(strings.TrimSuffix(model, "-online"))
	}
	aliases := map[string]string{"system_instruction": "systemInstruction", "generation_config": "generationConfig", "tool_config": "toolConfig", "safety_settings": "safetySettings", "session_id": "sessionId"}
	for from, to := range aliases {
		if innerMap[to] == nil {
			innerMap[to] = innerMap[from]
		}
		delete(innerMap, from)
	}
	extra := AsMap(cloneValue(innerMap))
	for _, k := range []string{"systemInstruction", "tools", "toolConfig", "generationConfig", "safetySettings", "sessionId", "contents", "stream", "model"} {
		delete(extra, k)
	}
	inner := InnerRequest{SystemInstruction: innerMap["systemInstruction"], Tools: innerMap["tools"], ToolConfig: innerMap["toolConfig"], GenerationConfig: innerMap["generationConfig"], SafetySettings: innerMap["safetySettings"], SessionID: firstNonEmpty(AsString(innerMap["sessionId"]), SessionID(accountID, "")), Contents: innerMap["contents"], Extra: extra}
	if inner.SafetySettings == nil {
		inner.SafetySettings = SafetyOff()
	}
	gc := AsMap(inner.GenerationConfig)
	if gc == nil {
		gc = map[string]any{}
	}
	if shouldAutoInjectThinking(mapped) && gc["thinkingConfig"] == nil {
		gc["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingBudget": DefaultThinkingBudget(mapped)}
	}
	if IsImageModel(mapped) {
		imageGenerationConfig(gc, model, "", "", "")
	}
	inner.GenerationConfig = gc
	for _, v := range AsSlice(inner.Tools) {
		tool := AsMap(v)
		for _, d := range AsSlice(tool["functionDeclarations"]) {
			decl := AsMap(d)
			if schema := decl["parameters"]; schema != nil {
				decl["parameters"] = CleanSchema(schema)
			}
		}
	}
	if strings.HasSuffix(strings.ToLower(model), "-online") {
		ts := AsSlice(inner.Tools)
		ts = append(ts, map[string]any{"googleSearch": map[string]any{}})
		inner.Tools = ts
	}
	stream, _ := body["stream"].(bool)
	return Wrap(projectID, mapped, email, AsString(inner.SessionID), inner, IsImageModel(mapped)), mapped, stream
}

func UnwrapGemini(raw []byte) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw, nil
	}
	if r := envelope["response"]; r != nil {
		return json.Marshal(r)
	}
	return raw, nil
}

func StampGeminiModel(raw []byte, model string) []byte {
	if strings.TrimSpace(model) == "" || len(raw) == 0 {
		return raw
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return raw
	}
	stampGeminiModel(payload, model)
	out, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return out
}

func stampGeminiModel(payload map[string]any, model string) {
	if payload == nil || strings.TrimSpace(model) == "" {
		return
	}
	payload["model"] = model
	payload["modelVersion"] = model
	if name := AsString(payload["name"]); strings.Contains(name, "models/") {
		payload["name"] = "models/" + model
	}
}

func ParseModelPath(path string) (model string, action string) {
	// /v1beta/models/gemini-2.5-flash:generateContent
	path = strings.TrimPrefix(path, "/v1beta/models/")
	path = strings.TrimPrefix(path, "/v1/models/")
	if i := strings.Index(path, ":"); i >= 0 {
		return path[:i], path[i+1:]
	}
	return path, ""
}
