package convert

import (
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/url"
	"path"
	"strings"
)

// cloneValue prevents normalization and retries from mutating the caller's history.
func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = cloneValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = cloneValue(v)
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = cloneValue(v)
		}
		return out
	default:
		return v
	}
}

func thoughtSignature(m map[string]any) string {
	return firstNonEmpty(AsString(m["thoughtSignature"]), AsString(m["thought_signature"]), AsString(m["signature"]), AsString(GetPath(m, "extra_content", "google", "thought_signature")))
}
func withSignature(part map[string]any, sig string) map[string]any {
	if sig != "" {
		part["thoughtSignature"] = sig
	}
	return part
}
func toolName(m map[string]any) string {
	return firstNonEmpty(AsString(GetPath(m, "function", "name")), AsString(m["name"]))
}
func toolID(m map[string]any) string { return firstNonEmpty(AsString(m["call_id"]), AsString(m["id"])) }
func normalizeToolName(name string) string {
	if name == "local_shell_call" {
		return "shell"
	}
	return name
}
func qualifyTool(namespace, name string) string {
	if name == "" {
		return ""
	}
	if namespace == "" || strings.HasPrefix(name, "mcp__") || strings.HasPrefix(name, namespace+"__") {
		return normalizeToolName(name)
	}
	return strings.TrimSuffix(namespace, "__") + "__" + normalizeToolName(name)
}
func flattenTools(tools []any, namespace string) []any {
	var out []any
	for _, v := range tools {
		m := AsMap(cloneValue(v))
		if m == nil {
			continue
		}
		if AsString(m["type"]) == "namespace" {
			out = append(out, flattenTools(AsSlice(m["tools"]), qualifyTool(namespace, AsString(m["name"])))...)
			continue
		}
		name := qualifyTool(namespace, toolName(m))
		if name != "" {
			m["name"] = name
			if f := AsMap(m["function"]); f != nil {
				f["name"] = name
			}
		}
		out = append(out, m)
	}
	return out
}
func isSearchTool(m map[string]any) bool {
	typ := AsString(m["type"])
	if strings.HasPrefix(typ, "web_search") || typ == "google_search" || typ == "builtin_web_search" {
		return true
	}
	// A custom function named web_search remains a client tool; only built-in tools are promoted.
	return m["function"] == nil && m["parameters"] == nil && m["input_schema"] == nil && (AsString(m["name"]) == "web_search" || AsString(m["name"]) == "google_search")
}
func hasSearchTools(tools []any) bool {
	for _, v := range flattenTools(tools, "") {
		if isSearchTool(AsMap(v)) {
			return true
		}
	}
	return false
}
func functionCallingConfig(choice any) map[string]any {
	mode := "AUTO"
	name := ""
	if s, ok := choice.(string); ok {
		switch strings.ToLower(s) {
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		}
	}
	if m := AsMap(choice); m != nil {
		switch AsString(m["type"]) {
		case "none":
			mode = "NONE"
		case "any", "required", "tool", "function":
			mode = "ANY"
		}
		name = firstNonEmpty(AsString(m["name"]), AsString(GetPath(m, "function", "name")))
		if name != "" {
			mode = "ANY"
		}
	}
	out := map[string]any{"mode": mode}
	if name != "" {
		out["allowedFunctionNames"] = []string{normalizeToolName(name)}
	}
	return out
}
func attachTools(inner *InnerRequest, declarations []any, raw []any, choice any, originalModel string) {
	var ts []any
	if len(declarations) > 0 {
		ts = append(ts, map[string]any{"functionDeclarations": declarations})
		cfg := functionCallingConfig(choice)
		if names, ok := cfg["allowedFunctionNames"].([]string); ok {
			for i, name := range names {
				names[i] = resolveDeclaredToolName(name, flattenTools(raw, ""))
			}
		}
		inner.ToolConfig = map[string]any{"functionCallingConfig": cfg}
	}
	if len(declarations) == 0 && (strings.HasSuffix(strings.ToLower(originalModel), "-online") || hasSearchTools(raw)) {
		ts = append(ts, map[string]any{"googleSearch": map[string]any{}})
	}
	if len(ts) > 0 {
		inner.Tools = ts
	}
}
func partsTextAndMedia(content any) (any, []any) {
	parts := contentToParts(content)
	var texts []string
	var media []any
	for _, v := range parts {
		m := AsMap(v)
		if s, ok := m["text"].(string); ok {
			texts = append(texts, s)
		} else {
			media = append(media, v)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n"), media
	}
	if s, ok := content.(string); ok {
		return s, media
	}
	if len(media) > 0 {
		return "", media
	}
	if content == nil {
		return "", nil
	}
	return cloneValue(content), nil
}
func mediaPart(source, fallbackMime string) map[string]any {
	if strings.HasPrefix(source, "data:") {
		i := strings.IndexByte(source, ',')
		if i < 0 {
			return nil
		}
		head := source[5:i]
		mt := strings.Split(head, ";")[0]
		if mt == "" {
			mt = fallbackMime
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mt, "data": source[i+1:]}}
	}
	u, err := url.Parse(source)
	if err == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "gs") {
		mt := mime.TypeByExtension(path.Ext(u.Path))
		if mt == "" {
			mt = fallbackMime
		}
		if i := strings.IndexByte(mt, ';'); i >= 0 {
			mt = mt[:i]
		}
		return map[string]any{"fileData": map[string]any{"fileUri": source, "mimeType": mt}}
	}
	return nil
}
func audioMIME(format string) string {
	if strings.Contains(format, "/") {
		return format
	}
	switch strings.ToLower(format) {
	case "wav":
		return "audio/wav"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "webm":
		return "audio/webm"
	case "m4a", "mp4":
		return "audio/mp4"
	case "pcm16", "pcm":
		return "audio/L16"
	default:
		return "audio/mpeg"
	}
}
func mergeContents(contents []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(contents))
	for _, m := range contents {
		if len(AsSlice(m["parts"])) == 0 {
			continue
		}
		if len(out) > 0 && out[len(out)-1]["role"] == m["role"] {
			last := out[len(out)-1]
			last["parts"] = append(AsSlice(last["parts"]), AsSlice(m["parts"])...)
		} else {
			out = append(out, m)
		}
	}
	return out
}
func setStop(gen map[string]any, stop any) {
	switch s := stop.(type) {
	case string:
		if s != "" {
			gen["stopSequences"] = []string{s}
		}
	case []any:
		gen["stopSequences"] = cloneValue(s)
	case []string:
		gen["stopSequences"] = append([]string(nil), s...)
	}
}
func applyThinking(gen map[string]any, model string, thinking *OpenAIThinking, effort string) {
	enabled := IsThinkingModel(model)
	budget := DefaultThinkingBudget(model)
	if thinking != nil {
		if effort == "" && thinking.Effort != "" {
			effort = thinking.Effort
		}
		switch strings.ToLower(thinking.Type) {
		case "enabled", "adaptive":
			enabled = true
		case "disabled", "none":
			enabled = false
		}
		if thinking.BudgetTokens != nil {
			budget = *thinking.BudgetTokens
		}
	}
	if thinking != nil && (strings.EqualFold(thinking.Type, "disabled") || strings.EqualFold(thinking.Type, "none")) {
		gen["thinkingConfig"] = map[string]any{"includeThoughts": false, "thinkingBudget": 0}
		return
	}
	if effort != "" {
		if strings.EqualFold(effort, "none") {
			enabled = false
		} else {
			enabled = true
			budget = thinkingBudgetFromLevel(model, effort)
		}
	}
	if IsImageModel(model) {
		return
	}
	if !enabled {
		if thinking != nil || effort != "" {
			gen["thinkingConfig"] = map[string]any{"includeThoughts": false, "thinkingBudget": 0}
		}
		return
	}
	gen["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingBudget": budget}
	if thinking != nil && strings.EqualFold(thinking.Type, "adaptive") && strings.Contains(model, "claude") {
		level := strings.ToLower(effort)
		if level == "" || level == "max" {
			level = "high"
		}
		gen["thinkingConfig"] = map[string]any{"includeThoughts": true, "thinkingLevel": level}
	}
}
func imageGenerationConfig(gen map[string]any, original, size, quality, imageSize string) {
	cfg := AsMap(gen["imageConfig"])
	if cfg == nil {
		cfg = map[string]any{}
	}
	if imageSize == "" {
		switch strings.ToLower(quality) {
		case "hd", "high":
			imageSize = "4K"
		case "medium":
			imageSize = "2K"
		case "standard", "low":
			imageSize = "1K"
		}
	}
	if imageSize == "" {
		for _, s := range []string{"4k", "2k", "1k"} {
			if strings.Contains(strings.ToLower(original), "-"+s) {
				imageSize = strings.ToUpper(s)
				break
			}
		}
	}
	if imageSize != "" {
		cfg["imageSize"] = strings.ToUpper(imageSize)
	}
	ratio := ""
	for _, r := range []string{"21:9", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "5:4", "4:5", "1:1"} {
		if strings.Contains(original, "-"+strings.ReplaceAll(r, ":", "x")) {
			ratio = r
		}
	}
	var w, h int
	if _, err := fmt.Sscanf(size, "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
		target := float64(w) / float64(h)
		best := math.MaxFloat64
		for _, candidate := range []struct {
			ratio string
			value float64
		}{{"1:1", 1}, {"21:9", 21.0 / 9}, {"16:9", 16.0 / 9}, {"9:16", 9.0 / 16}, {"4:3", 4.0 / 3}, {"3:4", 3.0 / 4}, {"3:2", 1.5}, {"2:3", 2.0 / 3}, {"5:4", 1.25}, {"4:5", .8}} {
			if distance := math.Abs(target - candidate.value); distance < best {
				best = distance
				ratio = candidate.ratio
			}
		}
	}

	if ratio != "" {
		cfg["aspectRatio"] = ratio
	}
	if len(cfg) > 0 {
		gen["imageConfig"] = cfg
	}
}
func jsonArguments(v any) any {
	if s, ok := v.(string); ok {
		var a any
		if json.Unmarshal([]byte(s), &a) == nil && a != nil {
			return a
		}
		return map[string]any{"input": s}
	}
	if v == nil {
		return map[string]any{}
	}
	return cloneValue(v)
}

func resolveDeclaredToolName(name string, tools []any) string {
	name = normalizeToolName(name)
	if name == "" {
		return ""
	}
	found := ""
	for _, v := range tools {
		n := normalizeToolName(toolName(AsMap(v)))
		if n == name {
			return n
		}
		if strings.HasSuffix(n, "__"+name) {
			if found != "" {
				return name
			}
			found = n
		}
	}
	return firstNonEmpty(found, name)
}

// ValidateToolCombination reports an upstream limitation before sending a request.
func ValidateToolCombination(tools []any, model string) error {
	if (hasSearchTools(tools) || strings.HasSuffix(strings.ToLower(model), "-online")) && len(openaiTools(tools)) > 0 {
		return fmt.Errorf("this upstream cannot combine built-in web search with function tools; send them in separate requests")
	}
	return nil
}
