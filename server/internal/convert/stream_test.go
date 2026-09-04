package convert

import (
	"bytes"
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
