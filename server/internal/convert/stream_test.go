package convert

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteOpenAISSEStats(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}}\n\ndata: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":10}}}\n\n")
	var buf bytes.Buffer
	stats, err := WriteOpenAISSE(&buf, "gemini-3.1-pro", src)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Usage.Output != 10 {
		t.Fatalf("tokens %#v", stats.Usage)
	}
	if stats.Usage.Input != 2 {
		t.Fatalf("input %#v", stats.Usage)
	}
	if stats.FirstTokenAt.IsZero() {
		t.Fatal("missing first token")
	}
	if !strings.Contains(buf.String(), `"content":"hi"`) {
		t.Fatalf("missing content chunk: %s", buf.String())
	}
}

func TestCompletionTokensFromGemini(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":8}}}`)
	u := UsageFromGemini(raw)
	if u.Output != 8 || u.Input != 3 {
		t.Fatalf("got %#v", u)
	}
}

func TestCollectGeminiJSON(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hel\"}]}}]}}\n\ndata: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":2}}}\n\n")
	raw, err := CollectGeminiJSON(src)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, finish, usage := collectParts(mustMap(raw))
	if text != "hello" {
		t.Fatalf("text %q", text)
	}
	if finish != "STOP" {
		t.Fatalf("finish %s", finish)
	}
	if usage == nil {
		t.Fatal("missing usage")
	}
}

func mustMap(raw []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	return m
}

func TestCollectGeminiJSONSingleNewline(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hel\"}]}}]}}\ndata: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}]}}\n")
	raw, err := CollectGeminiJSON(src)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, _, _ := collectParts(mustMap(raw))
	if text != "hello" {
		t.Fatalf("text %q from %s", text, raw)
	}
}

func TestWriteOpenAISSESingleNewline(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}}\n")
	var buf bytes.Buffer
	if _, err := WriteOpenAISSE(&buf, "gemini-3.7-flash", src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"content":"hi"`) {
		t.Fatalf("missing content: %s", buf.String())
	}
}

func TestCollectGeminiJSONThoughtOnly(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"only thought\",\"thought\":true}]},\"finishReason\":\"STOP\"}]}}\n\n")
	raw, err := CollectGeminiJSON(src)
	if err != nil {
		t.Fatal(err)
	}
	text, thinking, _, _, _ := collectParts(mustMap(raw))
	if text != "only thought" {
		t.Fatalf("text %q thinking %q from %s", text, thinking, raw)
	}
}

func TestWriteOpenAISSEPromotesThoughtOnly(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hidden\",\"thought\":true}]},\"finishReason\":\"STOP\"}]}}\n\n")
	var buf bytes.Buffer
	if _, err := WriteOpenAISSE(&buf, "gemini-3.7-flash", src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"content":"hidden"`) {
		t.Fatalf("missing promoted content: %s", buf.String())
	}
}
