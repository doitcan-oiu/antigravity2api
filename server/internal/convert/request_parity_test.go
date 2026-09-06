package convert

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func decodeOpenAI(t *testing.T, s string) OpenAIRequest {
	t.Helper()
	var req OpenAIRequest
	if err := json.Unmarshal([]byte(s), &req); err != nil {
		t.Fatal(err)
	}
	return req
}
func decodeClaude(t *testing.T, s string) ClaudeRequest {
	t.Helper()
	var req ClaudeRequest
	if err := json.Unmarshal([]byte(s), &req); err != nil {
		t.Fatal(err)
	}
	return req
}
func innerOf(t *testing.T, outer OuterRequest) InnerRequest {
	t.Helper()
	r, ok := outer.Request.(InnerRequest)
	if !ok {
		t.Fatalf("request %T", outer.Request)
	}
	return r
}
func allRequestParts(inner InnerRequest) []map[string]any {
	var out []map[string]any
	for _, v := range AsSlice(inner.Contents) {
		for _, p := range AsSlice(AsMap(v)["parts"]) {
			out = append(out, AsMap(p))
		}
	}
	return out
}
func requestPart(inner InnerRequest, key string) map[string]any {
	for _, p := range allRequestParts(inner) {
		if p[key] != nil {
			return p
		}
	}
	return nil
}

func TestToolCallAndResponseNamesMatchWithoutRepeatedName(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-flash","messages":[{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]},{"role":"tool","tool_call_id":"call_a","content":"ok"}]}`)
	outer, _, _ := OpenAIToGemini(req, "p", "a@gmail.com", "a")
	inner := innerOf(t, outer)
	call := AsMap(requestPart(inner, "functionCall")["functionCall"])
	response := AsMap(requestPart(inner, "functionResponse")["functionResponse"])
	if call["id"] != response["id"] || call["name"] != response["name"] {
		t.Fatalf("mismatched call/result: %#v %#v", call, response)
	}
	claude := decodeClaude(t, `{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_a","name":"read_file","input":{"path":"a"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_a","content":"oops","is_error":true}]}]}`)
	outer, _, _ = ClaudeToGemini(claude, "p", "a@gmail.com", "a")
	inner = innerOf(t, outer)
	response = AsMap(requestPart(inner, "functionResponse")["functionResponse"])
	if response["name"] != "read_file" || GetPath(response, "response", "error") != "oops" {
		t.Fatalf("tool error/result lost: %#v", response)
	}
}
func TestResponsesCallsUseCallIDAndPreserveCustomOutput(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-flash","instructions":"once","input":[{"type":"function_call","id":"fc_item","call_id":"call_a","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":[{"type":"text","text":"ok"},{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}}]},{"type":"custom_tool_call","call_id":"call_b","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},{"type":"custom_tool_call_output","call_id":"call_b","output":"done"}]}`)
	outer, _, _ := OpenAIToGemini(req, "p", "e", "a")
	inner := innerOf(t, outer)
	if strings.Count(extractText(GetPath(inner.SystemInstruction, "parts")), "once") != 1 {
		t.Fatal("duplicated instructions")
	}
	var calls, responses []map[string]any
	for _, p := range allRequestParts(inner) {
		if m := AsMap(p["functionCall"]); m != nil {
			calls = append(calls, m)
		}
		if m := AsMap(p["functionResponse"]); m != nil {
			responses = append(responses, m)
		}
	}
	if len(calls) != 2 || len(responses) != 2 || calls[0]["id"] != "call_a" || responses[0]["name"] != "read" || GetPath(calls[1], "args", "input") != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("history lost: %#v %#v", calls, responses)
	}
	if requestPart(inner, "inlineData") == nil {
		t.Fatal("tool screenshot lost")
	}
}
func TestNamespaceSchemaAndForcedToolAreConsistent(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-flash","tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","parameters":{"type":"object","$defs":{"Arg":{"type":"string"}},"properties":{"path":{"anyOf":[{"$ref":"#/$defs/Arg"},{"type":"null"}]},"additionalProperties":true},"additionalProperties":false}}]}],"tool_choice":{"type":"function","function":{"name":"fs__read"}},"messages":[{"role":"user","content":"read"}]}`)
	before, _ := json.Marshal(req)
	outer, _, _ := OpenAIToGemini(req, "p", "e", "a")
	inner := innerOf(t, outer)
	tool := AsMap(AsSlice(inner.Tools)[0])
	decl := AsMap(AsSlice(tool["functionDeclarations"])[0])
	schema := AsMap(decl["parameters"])
	if decl["name"] != "fs__read" || GetPath(schema, "properties", "path", "type") != "STRING" || GetPath(schema, "properties", "path", "nullable") != true {
		t.Fatalf("bad schema %#v", decl)
	}
	if _, ok := schema["$defs"]; ok {
		t.Fatal("defs not flattened")
	}
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatal("unsupported schema field")
	}
	allowed := GetPath(inner.ToolConfig, "functionCallingConfig", "allowedFunctionNames")
	if !reflect.DeepEqual(allowed, []string{"fs__read"}) {
		t.Fatalf("choice %#v", allowed)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("request mutated")
	}
}
func TestSchemaRecursiveRefTerminatesAndKeepsPropertyNames(t *testing.T) {
	var schema any
	_ = json.Unmarshal([]byte(`{"type":"object","$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}},"properties":{"value":{"$ref":"#/$defs/Node"},"default":{"type":"string"}}}`), &schema)
	got := CleanSchema(schema)
	if GetPath(got, "properties", "default", "type") != "STRING" {
		t.Fatal("user property deleted")
	}
	if raw, err := json.Marshal(got); err != nil || strings.Contains(string(raw), "$ref") {
		t.Fatalf("recursive ref leaked %s %v", raw, err)
	}
}
func TestGenerationOptionsAndFinalModelAppliedBeforeBudget(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3.1-pro","messages":[{"role":"user","content":"hi"}],"stop":["END"],"seed":42,"n":2,"presence_penalty":0.2,"frequency_penalty":0.3,"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}}`)
	outer, mapped, _ := OpenAIToGeminiWithModel(req, "p", "e", "a", "gemini-pro-agent")
	gen := AsMap(innerOf(t, outer).GenerationConfig)
	if mapped != "gemini-pro-agent" || intVal(GetPath(gen, "thinkingConfig", "thinkingBudget")) != 10001 {
		t.Fatalf("wrong final budget %#v", gen)
	}
	if gen["responseMimeType"] != "application/json" || GetPath(gen, "responseSchema", "properties", "ok", "type") != "BOOLEAN" || gen["candidateCount"] != 2 || gen["seed"] != int64(42) || gen["presencePenalty"] != 0.2 {
		t.Fatalf("options lost: %#v", gen)
	}
	if _, ok := gen["stopSequences"]; !ok {
		t.Fatal("stop lost")
	}
}
func TestThinkingSignaturePreservedAndUnsignedClaudeHistoryDowngraded(t *testing.T) {
	req := decodeClaude(t, `{"model":"claude-opus-4-6","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"signed","signature":"actual-signature"},{"type":"tool_use","id":"id_signed","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"id_signed","content":"ok"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"unsigned"}]}]}`)
	outer, _, _ := ClaudeToGemini(req, "p", "e", "a")
	inner := innerOf(t, outer)
	if thoughtSignature(requestPart(inner, "functionCall")) != "actual-signature" {
		t.Fatal("tool signature not inherited")
	}
	for _, p := range allRequestParts(inner) {
		if p["text"] == "unsigned" && partIsThought(p) {
			t.Fatal("unsigned Claude thinking sent")
		}
		if p["text"] == "Thinking..." {
			t.Fatal("fabricated thought")
		}
	}
}
func TestNativeRequestPreservesExtraAndSessionWithoutMutation(t *testing.T) {
	var body map[string]any
	_ = json.Unmarshal([]byte(`{"contents":[{"parts":[{"text":"x"}]}],"sessionId":"client-session","cachedContent":"cached/one","labels":{"a":"b"},"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"low"}}}`), &body)
	before, _ := json.Marshal(body)
	outer, _, _ := NativeGeminiToInternalWithModel(body, "gemini-3.1-pro", "p", "e", "a", "claude-sonnet-4-6")
	inner := innerOf(t, outer)
	raw, _ := json.Marshal(inner)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["sessionId"] != "client-session" || out["cachedContent"] != "cached/one" || GetPath(out, "labels", "a") != "b" || GetPath(out, "generationConfig", "thinkingConfig", "thinkingLevel") != "low" {
		t.Fatalf("fields lost %s", raw)
	}
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Fatal("native caller body changed")
	}
}
func TestMultimodalAndImageConfig(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-pro-image","size":"1280x720","quality":"hd","tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}],"messages":[{"role":"system","content":"sys"},{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"YQ==","format":"wav"}},{"type":"video_url","video_url":{"url":"https://example.test/v.mp4"}},{"type":"image_url","image_url":{"url":"https://example.test/i.png"}}]}]}`)
	outer, _, _ := OpenAIToGemini(req, "p", "e", "a")
	inner := innerOf(t, outer)
	gen := AsMap(inner.GenerationConfig)
	if GetPath(gen, "imageConfig", "imageSize") != "4K" || GetPath(gen, "imageConfig", "aspectRatio") != "16:9" {
		t.Fatalf("image config %#v", gen)
	}
	if inner.Tools != nil || inner.SystemInstruction != nil {
		t.Fatal("unsupported image tool/system sent")
	}
	parts := allRequestParts(inner)
	if len(parts) != 3 || GetPath(parts[0], "inlineData", "mimeType") != "audio/wav" || GetPath(parts[2], "fileData", "mimeType") != "image/png" {
		t.Fatalf("media lost %#v", parts)
	}
}
func TestSearchBuiltinAndCustomFunctionRemainDistinct(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-flash-online","tools":[{"type":"web_search"}],"messages":[{"role":"user","content":"news"}]}`)
	outer, mapped, _ := OpenAIToGemini(req, "p", "e", "a")
	inner := innerOf(t, outer)
	if strings.HasSuffix(mapped, "-online") || AsMap(AsSlice(inner.Tools)[0])["googleSearch"] == nil {
		t.Fatal("search not translated")
	}
	custom := []any{map[string]any{"type": "function", "function": map[string]any{"name": "web_search", "parameters": map[string]any{"type": "object"}}}}
	if hasSearchTools(custom) || len(openaiTools(custom)) != 1 {
		t.Fatal("client search function was hijacked")
	}
	if ValidateToolCombination(append(custom, map[string]any{"type": "web_search"}), "gemini-3-flash") == nil {
		t.Fatal("illegal upstream mixing accepted")
	}
}
func TestVariantSelectionAndExplicitPhysicalModels(t *testing.T) {
	low := 1000
	cases := []struct {
		model  string
		budget *int
		effort string
		want   string
	}{{"gemini-3-flash", nil, "", "gemini-3-flash-agent"}, {"gemini-3.1-pro", &low, "", "gemini-3.1-pro-low"}, {"gemini-3.7-flash", nil, "medium", "gemini-3.7-flash-medium"}, {"gemini-3.1-pro-preview", nil, "", "gemini-3.1-pro-preview"}}
	for _, c := range cases {
		if got := ResolveModel(c.model, c.budget, c.effort).Model; got != c.want {
			t.Fatalf("%s -> %s want %s", c.model, got, c.want)
		}
	}
}
func TestConcurrentConversionDoesNotMutateSharedRequest(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}}]}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"x":{"type":["string","null"]}}}}}]}`)
	before, _ := json.Marshal(req)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			OpenAIToGemini(req, "p", "e", "a")
			RememberToolSignature("gemini-3-flash", "call_concurrent", "sig")
			_ = RecallToolSignature("gemini-3-flash", "call_concurrent")
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("concurrent conversion mutated input")
	}
}

func TestSchemaRestrictionsBecomeHintsAndArraysHaveItems(t *testing.T) {
	var raw any
	_ = json.Unmarshal([]byte(`{"type":"object","required":["bad","ok"],"properties":{"bad":false,"ok":{"type":"array"},"limit":{"type":"integer","minimum":1},"either":{"anyOf":[{"type":"string"},{"type":"object","properties":{"x":{"type":"string"}}}]}}}`), &raw)
	got := CleanSchema(raw)
	if GetPath(got, "properties", "ok", "items", "type") != "STRING" {
		t.Fatal("itemless array not normalized")
	}
	if GetPath(got, "properties", "limit", "minimum") != nil || !strings.Contains(AsString(GetPath(got, "properties", "limit", "description")), "minimum=1") {
		t.Fatal("constraint not preserved as hint")
	}
	if GetPath(got, "properties", "either", "anyOf") != nil || GetPath(got, "properties", "either", "properties", "x", "type") != "STRING" {
		t.Fatal("union not flattened")
	}
	if !reflect.DeepEqual(got["required"], []any{"ok"}) {
		t.Fatalf("dangling required %#v", got["required"])
	}
}

func TestExplicitDisabledThinkingWinsOverEffort(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-flash","thinking":{"type":"disabled"},"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	outer, _, _ := OpenAIToGemini(req, "p", "e", "a")
	gen := AsMap(innerOf(t, outer).GenerationConfig)
	if GetPath(gen, "thinkingConfig", "includeThoughts") != false || intVal(GetPath(gen, "thinkingConfig", "thinkingBudget")) != 0 {
		t.Fatalf("explicit disabled lost %#v", gen)
	}
}
func TestClaudeOutputEffortHasPrecedenceOverThinkingEffort(t *testing.T) {
	req := decodeClaude(t, `{"model":"gemini-3.7-flash","thinking":{"type":"enabled","effort":"high"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`)
	_, mapped, _ := ClaudeToGemini(req, "p", "e", "a")
	if mapped != "gemini-3.7-flash-low" {
		t.Fatalf("effort selected %s", mapped)
	}
}
func TestImageSizeNormalizesToSupportedAspectRatio(t *testing.T) {
	gen := map[string]any{}
	imageGenerationConfig(gen, "gemini-3-pro-image", "1000x700", "", "")
	if GetPath(gen, "imageConfig", "aspectRatio") != "3:2" {
		t.Fatalf("unsupported aspect ratio %#v", gen)
	}
}

func TestUnsignedClaudeToolHistoryDisablesThinking(t *testing.T) {
	req := decodeClaude(t, `{"model":"claude-opus-4-6","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"uncached-tool","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"uncached-tool","content":"ok"}]}]}`)
	outer, _, _ := ClaudeToGemini(req, "p", "e", "a")
	inner := innerOf(t, outer)
	if thinkingConfigEnabled(inner.GenerationConfig) {
		t.Fatal("unsigned tool history kept strict Claude thinking enabled")
	}
	if requestPart(inner, "functionCall") == nil || requestPart(inner, "functionResponse") == nil {
		t.Fatal("tool history lost during recovery")
	}
}
func TestThinkingBudgetAliasesAreDecoded(t *testing.T) {
	req := decodeOpenAI(t, `{"model":"gemini-3-flash","thinking":{"type":"enabled","budgetTokens":1000}}`)
	if req.Thinking == nil || req.Thinking.BudgetTokens == nil || *req.Thinking.BudgetTokens != 1000 {
		t.Fatal("camelCase budget discarded")
	}
}
