package convert

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNonstreamMultipleCandidatesImagesSignaturesAndUniqueIDs(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"index":0,"content":{"parts":[{"text":"think","thought":true,"thoughtSignature":"sig-text"},{"functionCall":{"name":"read","args":{"path":"a"}},"thoughtSignature":"sig-tool"},{"inlineData":{"mimeType":"image/png","data":"YQ=="}}]},"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"alternative"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":5,"cachedContentTokenCount":40}}}`)
	out, err := GeminiToOpenAI("gemini-3-flash", raw, "response_a")
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	choices := AsSlice(obj["choices"])
	if len(choices) != 2 {
		t.Fatalf("candidates lost: %s", out)
	}
	msg := AsMap(AsMap(choices[0])["message"])
	if !strings.Contains(AsString(msg["content"]), "data:image/png;base64,YQ==") || msg["reasoning_signature"] != "sig-text" || msg["reasoning_content"] != "think" {
		t.Fatalf("image/signature lost: %s", out)
	}
	tc := AsMap(AsSlice(msg["tool_calls"])[0])
	id := AsString(tc["id"])
	if id == "" || RecallToolSignature("gemini-3-flash", id) != "sig-tool" {
		t.Fatal("tool signature cache not populated")
	}
	second, _ := GeminiToOpenAI("gemini-3-flash", raw, "response_b")
	var obj2 map[string]any
	_ = json.Unmarshal(second, &obj2)
	msg2 := AsMap(AsMap(AsSlice(obj2["choices"])[0])["message"])
	id2 := AsString(AsMap(AsSlice(msg2["tool_calls"])[0])["id"])
	if id == id2 {
		t.Fatal("missing upstream IDs must be unique across responses")
	}
	if AsMap(choices[1])["finish_reason"] != "length" {
		t.Fatal("candidate finish reason lost")
	}
	u := TokenUsageFromOpenAI(obj["usage"])
	if u.Input != 100 || u.Output != 20 || u.Cache != 40 || u.Reasoning != 5 {
		t.Fatalf("usage %#v", u)
	}
}
func TestClaudeResponsePreservesBlockOrderAndCacheUsage(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"think","thought":true},{"thoughtSignature":"late-sig"},{"text":"before"},{"functionCall":{"id":"call_order","name":"read","args":{}},"thoughtSignature":"tool-sig"},{"text":"after"},{"inlineData":{"mimeType":"image/png","data":"YQ=="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"cachedContentTokenCount":30}}`)
	out, err := GeminiToClaude("claude-sonnet-4-6", raw)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	content := AsSlice(obj["content"])
	if len(content) != 5 || AsMap(content[0])["signature"] != "late-sig" || AsMap(content[1])["text"] != "before" || AsMap(content[2])["type"] != "tool_use" || AsMap(content[3])["text"] != "after" || AsMap(content[4])["type"] != "image" {
		t.Fatalf("block order/data changed: %s", out)
	}
	if GetPath(obj, "usage", "input_tokens") != float64(70) || GetPath(obj, "usage", "cache_read_input_tokens") != float64(30) || obj["stop_reason"] != "tool_use" {
		t.Fatalf("usage/stop lost: %s", out)
	}
}
func TestNewUsageFormatAddsSeparateThoughtsOnce(t *testing.T) {
	u := TokenUsageFromOpenAI(geminiUsageToOpenAI(map[string]any{"total_input_tokens": 10, "total_output_tokens": 3, "total_thought_tokens": 5, "total_tool_use_tokens": 2, "candidatesTokenCount": 99}))
	if u.Input != 10 || u.Output != 10 || u.Reasoning != 5 {
		t.Fatalf("usage format precedence %#v", u)
	}
}
func TestToolSignatureRoundTripWithoutClientSignatureExtension(t *testing.T) {
	RememberToolSignature("claude-sonnet-4-6", "call_saved", "real-saved-signature")
	req := decodeOpenAI(t, `{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","tool_calls":[{"id":"call_saved","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_saved","content":"ok"}]}`)
	outer, _, _ := OpenAIToGemini(req, "p", "e", "a")
	if thoughtSignature(requestPart(innerOf(t, outer), "functionCall")) != "real-saved-signature" {
		t.Fatal("tool signature recovery failed")
	}
	if RecallToolSignature("gemini-3-flash", "call_saved") != "" {
		t.Fatal("signature crossed incompatible model families")
	}
}

func TestNonstreamErrorEnvelopesAreErrorsNotSuccessfulBodies(t *testing.T) {
	for _, raw := range []string{`{"error":{"code":429,"message":"quota"}}`, `{"response":{"error":{"code":429,"message":"quota"}}}`, `{"response":{"candidates":[]}}`} {
		if out, err := GeminiToOpenAI("m", []byte(raw), "id"); err == nil {
			t.Fatalf("OpenAI returned success %s", out)
		}
		if out, err := GeminiToClaude("m", []byte(raw)); err == nil {
			t.Fatalf("Claude returned success %s", out)
		}
	}
}

func TestSignatureCacheHasByteBoundsWithoutTruncatingSignatures(t *testing.T) {
	signature := strings.Repeat("x", maxCachedSignatureBytes+1)
	RememberToolSignature("claude-sonnet-4-6", "oversized-signature", signature)
	if RecallToolSignature("claude-sonnet-4-6", "oversized-signature") != "" {
		t.Fatal("oversized signature retained")
	}
	signature = strings.Repeat("s", maxCachedSignatureBytes)
	for i := 0; i < 70; i++ {
		RememberToolSignature("claude-sonnet-4-6", fmt.Sprintf("bounded-signature-%d", i), signature)
	}
	toolSignatures.Lock()
	size, count := toolSignatures.bytes, len(toolSignatures.entries)
	toolSignatures.Unlock()
	if size > maxSignatureCacheBytes || count > maxSignatureCacheEntries {
		t.Fatalf("cache unbounded %d bytes %d entries", size, count)
	}
	if got := RecallToolSignature("claude-sonnet-4-6", "bounded-signature-69"); got != signature {
		t.Fatal("signature truncated")
	}
}
