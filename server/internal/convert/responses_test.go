package convert

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesStreamingLifecycleAndReplayableOutput(t *testing.T) {
	input := testFrame(`{"candidates":[{"content":{"parts":[{"text":"plan","thought":true,"thoughtSignature":"sig"},{"text":"hello"},{"functionCall":{"name":"read","args":{"file_path":"a.go"}}}]}}]}`) + testFrame(`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3,"cachedContentTokenCount":2}}`)
	var out bytes.Buffer
	if _, err := WriteResponsesSSE(&out, "gemini", strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	events := streamTestEvents(t, out.String())
	if events[0]["type"] != "response.created" || events[1]["type"] != "response.in_progress" || events[len(events)-1]["type"] != "response.completed" {
		t.Fatalf("bad lifecycle: %s", out.String())
	}
	for i, e := range events {
		if intVal(e["sequence_number"]) != i {
			t.Fatal("nonmonotonic sequence")
		}
	}
	response := AsMap(events[len(events)-1]["response"])
	output := AsSlice(response["output"])
	if response["object"] != "response" || !strings.HasPrefix(AsString(response["id"]), "resp_") || len(output) != 3 {
		t.Fatalf("bad response: %#v", response)
	}
	if AsMap(output[0])["type"] != "reasoning" || AsMap(output[0])["encrypted_content"] != "sig" || AsMap(output[1])["type"] != "message" || AsMap(output[2])["type"] != "function_call" || AsString(AsMap(output[2])["call_id"]) == "" {
		t.Fatalf("output not replayable: %#v", output)
	}
	if intVal(AsMap(response["usage"])["input_tokens"]) != 8 || intVal(AsMap(AsMap(response["usage"])["input_tokens_details"])["cached_tokens"]) != 2 {
		t.Fatal("lost usage")
	}
	if !strings.Contains(out.String(), "event: response.function_call_arguments.delta\n") || strings.Contains(out.String(), "[DONE]") {
		t.Fatal("wrong Responses wire format")
	}
}
func TestResponsesNonStreamingAndCustomTool(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"apply_patch","args":{"input":"*** Begin Patch\n*** End Patch"}}}]},"finishReason":"STOP"}]}}`)
	encoded, err := GeminiToResponsesWithTools("gemini", raw, map[string]bool{"apply_patch": true})
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	output := AsSlice(response["output"])
	if len(output) != 1 {
		t.Fatal("missing tool")
	}
	tool := AsMap(output[0])
	if tool["type"] != "custom_tool_call" || tool["input"] != "*** Begin Patch\n*** End Patch" || tool["arguments"] != nil {
		t.Fatalf("custom tool encoded as ordinary JSON tool: %s", encoded)
	}
	var stream bytes.Buffer
	if _, err := WriteResponsesSSEWithTools(&stream, "gemini", strings.NewReader(testFrame(string(raw))), map[string]bool{"apply_patch": true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stream.String(), "response.custom_tool_call_input.delta") {
		t.Fatal("custom delta missing")
	}
}
func TestResponsesInterruptedAndReasoningOnlyAreIncomplete(t *testing.T) {
	for _, input := range []string{testFrame(`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`), testFrame(`{"candidates":[{"content":{"parts":[{"text":"plan","thought":true}]},"finishReason":"STOP"}]}`)} {
		var out bytes.Buffer
		if _, err := WriteResponsesSSE(&out, "gemini", strings.NewReader(input)); err == nil {
			t.Fatal("interrupted or empty response reported success")
		}
		events := streamTestEvents(t, out.String())
		last := events[len(events)-1]
		if last["type"] != "response.incomplete" || AsMap(last["response"])["error"] == nil {
			t.Fatalf("missing explicit interruption: %#v", last)
		}
	}
}
func TestResponsesMaxTokens(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}]}`)
	encoded, err := GeminiToResponses("gemini", raw)
	if err != nil {
		t.Fatal(err)
	}
	response := mustMap(encoded)
	if response["status"] != "incomplete" || AsMap(response["incomplete_details"])["reason"] != "max_output_tokens" {
		t.Fatalf("lost finish reason: %s", encoded)
	}
}
func TestLegacyHasTextCompletionSchema(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`)
	encoded, err := GeminiToLegacy("gemini", raw)
	if err != nil {
		t.Fatal(err)
	}
	response := mustMap(encoded)
	choice := AsMap(AsSlice(response["choices"])[0])
	if response["object"] != "text_completion" || choice["text"] != "answer" || choice["message"] != nil {
		t.Fatalf("wrong legacy JSON: %s", encoded)
	}
	var out bytes.Buffer
	if _, err := WriteLegacySSE(&out, "gemini", strings.NewReader(testFrame(string(raw)))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"text":"answer"`) || strings.Contains(out.String(), `"delta"`) {
		t.Fatalf("wrong legacy SSE: %s", out.String())
	}
}

func TestResponsesBlockedOutputRetainsContentFilterReason(t *testing.T) {
	raw := []byte(`{"candidates":[{"finishReason":"SAFETY"}]}`)
	out, err := GeminiToResponses("gemini", raw)
	if err != nil {
		t.Fatal(err)
	}
	response := mustMap(out)
	if AsMap(response["incomplete_details"])["reason"] != "content_filter" || response["error"] != nil {
		t.Fatalf("content filter became interruption: %s", out)
	}
}
func TestResponsesPartialMessageHasIncompleteStatus(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}]}`)
	out, err := GeminiToResponses("gemini", raw)
	if err != nil {
		t.Fatal(err)
	}
	response := mustMap(out)
	item := AsMap(AsSlice(response["output"])[0])
	if item["status"] != "incomplete" {
		t.Fatalf("partial item claims completion: %s", out)
	}
}
