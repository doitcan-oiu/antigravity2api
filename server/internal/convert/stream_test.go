package convert

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}}\n")
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
	if text != "" || thinking != "only thought" {
		t.Fatalf("text %q thinking %q from %s", text, thinking, raw)
	}
}

func TestWriteOpenAISSEPreservesThoughtOnly(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hidden\",\"thought\":true}]},\"finishReason\":\"STOP\"}]}}\n\n")
	var buf bytes.Buffer
	if _, err := WriteOpenAISSE(&buf, "gemini-3.7-flash", src); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"content":"hidden"`) || !strings.Contains(buf.String(), `"reasoning_content":"hidden"`) {
		t.Fatalf("thinking must remain reasoning, not an invented answer: %s", buf.String())
	}
}

func testFrame(payload string) string { return "data: " + payload + "\n\n" }
func streamTestEvents(t *testing.T, output string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			result = append(result, event)
		}
	}
	return result
}
func TestClaudeStreamToolStateAndUsage(t *testing.T) {
	input := testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"foo","args":{"x":1}}}]}}]}`) + testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bar","args":{"y":2}}}]}}]}`) + testFrame(`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}`)
	var out bytes.Buffer
	if _, err := WriteClaudeSSE(&out, "gemini", strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	stops := 0
	ids := map[string]bool{}
	deltas := 0
	for _, e := range streamTestEvents(t, out.String()) {
		switch e["type"] {
		case "content_block_start":
			b := AsMap(e["content_block"])
			if b["type"] == "tool_use" {
				id := AsString(b["id"])
				if id == "" || ids[id] {
					t.Fatalf("duplicate tool id %q", id)
				}
				ids[id] = true
			}
		case "message_delta":
			deltas++
			if AsMap(e["delta"])["stop_reason"] != "tool_use" {
				t.Fatalf("wrong stop: %#v", e)
			}
			if AsMap(e["usage"])["output_tokens"] != float64(3) {
				t.Fatalf("missing top-level usage: %#v", e)
			}
		case "message_stop":
			stops++
		}
	}
	if stops != 1 || deltas != 1 || len(ids) != 2 {
		t.Fatalf("stops=%d deltas=%d ids=%v", stops, deltas, ids)
	}
}
func TestClaudeStreamMaintainsBlockOrderAndSignature(t *testing.T) {
	input := testFrame(`{"candidates":[{"content":{"parts":[{"text":"before"},{"text":"think","thought":true,"thoughtSignature":"real_signature"},{"text":"after"}]},"finishReason":"STOP"}]}`)
	var out bytes.Buffer
	if _, err := WriteClaudeSSE(&out, "gemini", strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	active := -1
	signature := false
	var kinds []string
	for _, e := range streamTestEvents(t, out.String()) {
		switch e["type"] {
		case "content_block_start":
			if active >= 0 {
				t.Fatal("overlapping content blocks")
			}
			active = intVal(e["index"])
			kinds = append(kinds, AsString(AsMap(e["content_block"])["type"]))
		case "content_block_delta":
			if active != intVal(e["index"]) {
				t.Fatal("delta for wrong block")
			}
			if AsMap(e["delta"])["signature"] == "real_signature" {
				signature = true
			}
		case "content_block_stop":
			if active != intVal(e["index"]) {
				t.Fatal("stop for wrong block")
			}
			active = -1
		}
	}
	if strings.Join(kinds, ",") != "text,thinking,text" || !signature || active != -1 {
		t.Fatalf("kinds=%v signature=%v active=%d", kinds, signature, active)
	}
}
func TestOpenAIStreamUniqueToolsDedupAndLateUsage(t *testing.T) {
	tool := testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"upstream_foo","name":"foo","args":{}}}]}}]}`)
	input := tool + tool + testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bar","args":{}}}]},"finishReason":"STOP"}]}`) + testFrame(`{"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":6}}`)
	var out bytes.Buffer
	if _, err := WriteOpenAISSE(&out, "gemini", strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	usage := false
	finish := ""
	for _, e := range streamTestEvents(t, out.String()) {
		if AsMap(e["usage"])["completion_tokens"] == float64(6) {
			usage = true
		}
		for _, v := range AsSlice(e["choices"]) {
			c := AsMap(v)
			if f := AsString(c["finish_reason"]); f != "" {
				finish = f
			}
			for _, v := range AsSlice(AsMap(c["delta"])["tool_calls"]) {
				id := AsString(AsMap(v)["id"])
				if ids[id] {
					t.Fatal("duplicate id")
				}
				ids[id] = true
			}
		}
	}
	if len(ids) != 2 || !usage || finish != "tool_calls" {
		t.Fatalf("ids=%v usage=%v finish=%q", ids, usage, finish)
	}
}
func TestSSEMultilineAndCRLF(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		input := strings.Join([]string{": ping", "event: message", "data: {\"response\":", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"你好\"}]},\"finishReason\":\"STOP\"}]}}", "", ""}, newline)
		var out bytes.Buffer
		if _, err := WriteOpenAISSE(&out, "gemini", strings.NewReader(input)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "你好") {
			t.Fatal("multiline output lost")
		}
	}
}
func TestStreamsRejectErrorsEmptyAndTruncation(t *testing.T) {
	cases := map[string]string{"empty": "", "error": testFrame(`{"error":{"code":429,"message":"rate limited"}}`), "malformed": "data: {broken}\n\n", "interrupted": testFrame(`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`), "no_output": testFrame(`{"candidates":[{"finishReason":"STOP"}]}`)}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := CollectGeminiJSON(strings.NewReader(input)); err == nil {
				t.Fatal("collector accepted failure")
			}
			for _, write := range []func(io.Writer, string, io.Reader) (StreamStats, error){WriteOpenAISSE, WriteClaudeSSE, WriteGeminiSSE} {
				var out bytes.Buffer
				if _, err := write(&out, "gemini", strings.NewReader(input)); err == nil {
					t.Fatal("writer accepted failure")
				}
				if !strings.Contains(out.String(), `"error"`) {
					t.Fatalf("missing explicit error: %s", out.String())
				}
			}
		})
	}
	var upstream *UpstreamError
	_, err := PrepareGeminiStream(strings.NewReader(cases["error"]))
	if !errors.As(err, &upstream) || upstream.Code != 429 {
		t.Fatalf("lost upstream status: %v", err)
	}
}
func TestPrepareGeminiStreamReplaysBufferedBytes(t *testing.T) {
	input := testFrame(`{"usageMetadata":{"promptTokenCount":8}}`) + testFrame(`{"candidates":[{"content":{"parts":[{"text":"first"}]}}]}`) + testFrame(`{"candidates":[{"content":{"parts":[{"text":"second"}]},"finishReason":"STOP"}]}`)
	reader, err := PrepareGeminiStream(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := CollectGeminiJSON(reader)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, _, usage := collectParts(mustMap(raw))
	if text != "firstsecond" || TokenUsageFromOpenAI(usage).Input != 8 {
		t.Fatalf("replay lost data: %s", raw)
	}
}
func TestCollectorPreservesCandidatesMediaMetadataAndSignatures(t *testing.T) {
	input := testFrame(`{"candidates":[{"index":0,"content":{"parts":[{"text":"a","thought":true}]}},{"index":1,"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"finishReason":"STOP"}]}`) + testFrame(`{"candidates":[{"index":0,"content":{"parts":[{"text":"b","thought":true,"thoughtSignature":"signed"}]},"groundingMetadata":{"webSearchQueries":["query"]},"finishReason":"STOP"}],"modelVersion":"test"}`)
	raw, err := CollectGeminiJSON(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	payload := mustMap(raw)
	candidates := AsSlice(payload["candidates"])
	if len(candidates) != 2 || !strings.Contains(string(raw), `"thoughtSignature":"signed"`) || !strings.Contains(string(raw), `"inlineData"`) || !strings.Contains(string(raw), `"groundingMetadata"`) || payload["modelVersion"] != "test" {
		t.Fatalf("lost metadata: %s", raw)
	}
}

type rejectingWriter struct{}

func (rejectingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestStreamWriteErrorsPropagate(t *testing.T) {
	input := testFrame(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	for _, write := range []func(io.Writer, string, io.Reader) (StreamStats, error){WriteOpenAISSE, WriteClaudeSSE, WriteGeminiSSE, WriteResponsesSSE, WriteLegacySSE} {
		_, err := write(rejectingWriter{}, "gemini", strings.NewReader(input))
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("lost write error: %v", err)
		}
	}
}
func TestSSEEventBound(t *testing.T) {
	reader := io.MultiReader(strings.NewReader("data: "), io.LimitReader(zeroByteReader{}, MaxSSEEventBytes+1))
	_, err := PrepareGeminiStream(reader)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Type != "event_too_large" {
		t.Fatalf("unbounded event: %v", err)
	}
}

type zeroByteReader struct{}

func (zeroByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestIdenticalToolsWithoutIDsAreDistinct(t *testing.T) {
	frame := testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"append","args":{"text":"x"}}},{"functionCall":{"name":"append","args":{"text":"x"}}}]}}]}`)
	input := frame + testFrame(`{"candidates":[{"finishReason":"STOP"}]}`)
	var out bytes.Buffer
	if _, err := WriteOpenAISSE(&out, "gemini", strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, event := range streamTestEvents(t, out.String()) {
		for _, v := range AsSlice(event["choices"]) {
			for _, v := range AsSlice(AsMap(AsMap(v)["delta"])["tool_calls"]) {
				id := AsString(AsMap(v)["id"])
				if ids[id] {
					t.Fatal("reused generated tool ID")
				}
				ids[id] = true
			}
		}
	}
	if len(ids) != 2 {
		t.Fatalf("identical intentional calls collapsed: %s", out.String())
	}
	collected, err := CollectGeminiJSON(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	_, _, calls, _, _ := collectParts(mustMap(collected))
	if len(calls) != 2 {
		t.Fatalf("collector collapsed distinct calls: %s", collected)
	}
}
func TestToolIDConflictsFailAndLateSignaturesSurvive(t *testing.T) {
	first := testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"call_id":"stable_id","name":"read","args":{"path":"a"}}}]}}]}`)
	signature := testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"call_id":"stable_id","name":"read","args":{"path":"a"}},"thoughtSignature":"late_sig"}]},"finishReason":"STOP"}]}`)
	var out bytes.Buffer
	if _, err := WriteOpenAISSE(&out, "gemini", strings.NewReader(first+signature)); err != nil {
		t.Fatal(err)
	}
	if RecallToolSignature("gemini", "stable_id") != "late_sig" {
		t.Fatal("late tool signature lost")
	}
	collected, err := CollectGeminiJSON(strings.NewReader(first + signature))
	if err != nil || !strings.Contains(string(collected), `"thoughtSignature":"late_sig"`) {
		t.Fatalf("collector lost signature: %s %v", collected, err)
	}
	conflict := testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"call_id":"stable_id","name":"read","args":{"path":"b"}}}]},"finishReason":"STOP"}]}`)
	if _, err := CollectGeminiJSON(strings.NewReader(first + conflict)); err == nil {
		t.Fatal("same tool ID accepted inconsistent arguments")
	}
}
func TestMalformedCandidateAndEmptyMediaFail(t *testing.T) {
	for _, input := range []string{testFrame(`{"candidates":[null]}`), testFrame(`{"candidates":[{"index":-1,"finishReason":"STOP"}]}`), testFrame(`{"candidates":[{"content":{"parts":[{"inlineData":{"data":""}}]},"finishReason":"STOP"}]}`), testFrame(`{"candidates":[{"content":{"parts":[{"functionCall":{"args":{}}}]},"finishReason":"STOP"}]}`)} {
		var out bytes.Buffer
		if _, err := WriteOpenAISSE(&out, "gemini", strings.NewReader(input)); err == nil {
			t.Fatalf("malformed candidate accepted: %s", input)
		}
	}
}
func TestFileDataOutputSurvivesValidation(t *testing.T) {
	input := testFrame(`{"candidates":[{"content":{"parts":[{"fileData":{"fileUri":"https://example.com/image.png","mimeType":"image/png"}}]},"finishReason":"STOP"}]}`)
	var out bytes.Buffer
	if _, err := WriteOpenAISSE(&out, "gemini", strings.NewReader(input)); err != nil || !strings.Contains(out.String(), "https://example.com/image.png") {
		t.Fatalf("fileData output lost: %s %v", out.String(), err)
	}
}
