package convert

import (
	"encoding/json"
	"strings"
)

func NativeGeminiToInternal(body map[string]any, model, projectID, email, accountID string) (OuterRequest, string, bool) {
	mapped := MapModel(model)
	innerMap := body
	if req := AsMap(body["request"]); req != nil {
		innerMap = req
		if m := AsString(body["model"]); m != "" {
			mapped = MapModel(m)
		}
	}
	inner := InnerRequest{
		SystemInstruction: innerMap["systemInstruction"],
		Tools:             innerMap["tools"],
		ToolConfig:        innerMap["toolConfig"],
		GenerationConfig:  innerMap["generationConfig"],
		SafetySettings:    innerMap["safetySettings"],
		SessionID:         firstNonEmpty(AsString(innerMap["sessionId"]), SessionID(accountID, "")),
		Contents:          innerMap["contents"],
	}
	if inner.SafetySettings == nil {
		inner.SafetySettings = SafetyOff()
	}
	if inner.GenerationConfig == nil {
		inner.GenerationConfig = map[string]any{}
	}
	if IsThinkingModel(mapped) && !IsImageModel(mapped) {
		gc := AsMap(inner.GenerationConfig)
		if gc == nil {
			gc = map[string]any{}
		}
		if gc["thinkingConfig"] == nil {
			gc["thinkingConfig"] = map[string]any{
				"includeThoughts": true,
				"thinkingBudget":  DefaultThinkingBudget(mapped),
			}
			inner.GenerationConfig = gc
		}
	}
	stream := false
	if v, ok := body["stream"].(bool); ok {
		stream = v
	}
	return Wrap(projectID, mapped, email, SessionID(accountID, ""), inner, IsImageModel(mapped)), mapped, stream
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

func ParseModelPath(path string) (model string, action string) {
	// /v1beta/models/gemini-2.5-flash:generateContent
	path = strings.TrimPrefix(path, "/v1beta/models/")
	path = strings.TrimPrefix(path, "/v1/models/")
	if i := strings.Index(path, ":"); i >= 0 {
		return path[:i], path[i+1:]
	}
	return path, ""
}
