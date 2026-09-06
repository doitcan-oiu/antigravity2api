package convert

import "testing"

func TestMapModel(t *testing.T) {
	if got := MapModel("gpt-4o"); got != "gemini-2.5-flash" {
		t.Fatalf("got %s", got)
	}
	if got := MapModel("claude-sonnet-4-5"); got != "claude-sonnet-4-6" {
		t.Fatalf("got %s", got)
	}
}

func TestOpenAIToGeminiBasic(t *testing.T) {
	req := OpenAIRequest{
		Model: "gpt-4o",
		Messages: []OpenAIMessage{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "hello"},
		},
	}
	outer, mapped, _ := OpenAIToGemini(req, "proj-1", "a@gmail.com", "acc-1")
	if mapped != "gemini-2.5-flash" {
		t.Fatalf("mapped %s", mapped)
	}
	if outer.Project != "proj-1" || outer.UserAgent != "antigravity" || outer.RequestType != "agent" {
		t.Fatalf("outer %#v", outer)
	}
	inner, ok := outer.Request.(InnerRequest)
	if !ok {
		t.Fatalf("inner type %T", outer.Request)
	}
	if inner.SystemInstruction == nil {
		t.Fatal("missing systemInstruction")
	}
}

func TestWrapThinkingLeavesRoomForAnswer(t *testing.T) {
	req := OpenAIRequest{
		Model: "gemini-3.7-flash",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "hello"},
		},
	}
	outer, _, _ := OpenAIToGemini(req, "proj-1", "a@gmail.com", "acc-1")
	inner, _ := outer.Request.(InnerRequest)
	gc := AsMap(inner.GenerationConfig)
	if gc == nil {
		t.Fatal("missing generationConfig")
	}
	tc := AsMap(gc["thinkingConfig"])
	if tc == nil {
		t.Fatal("missing thinkingConfig")
	}
	budget := intVal(tc["thinkingBudget"])
	maxTok := intVal(gc["maxOutputTokens"])
	if budget <= 0 {
		t.Fatalf("budget %d", budget)
	}
	if maxTok <= budget {
		t.Fatalf("maxOutputTokens %d must be > thinkingBudget %d", maxTok, budget)
	}
}

func TestFlashFunctionCallGetsThoughtSignature(t *testing.T) {
	req := OpenAIRequest{
		Model: "gemini-3.7-flash",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "search"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []any{map[string]any{
					"id":   "call_1",
					"type": "function",
					"function": map[string]any{
						"name":      "web_search",
						"arguments": `{"q":"hi"}`,
					},
				}},
			},
			{Role: "tool", Name: "web_search", ToolCallID: "call_1", Content: "ok"},
		},
	}
	outer, _, _ := OpenAIToGemini(req, "proj-1", "a@gmail.com", "acc-1")
	inner, _ := outer.Request.(InnerRequest)
	found := false
	for _, item := range AsSlice(inner.Contents) {
		msg := AsMap(item)
		for _, p := range AsSlice(msg["parts"]) {
			part := AsMap(p)
			if AsMap(part["functionCall"]) == nil {
				continue
			}
			if AsString(part["thoughtSignature"]) != "skip_thought_signature_validator" {
				t.Fatalf("signature %#v", part["thoughtSignature"])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing functionCall signature")
	}
}

func TestNativeFlashDoesNotAutoInjectThinking(t *testing.T) {
	outer, mapped, _ := NativeGeminiToInternal(map[string]any{
		"contents": []any{map[string]any{"parts": []any{map[string]any{"text": "hi"}}}},
	}, "gemini-3.7-flash", "proj", "a@gmail.com", "acc")
	if mapped != "gemini-3.7-flash" {
		t.Fatalf("mapped %s", mapped)
	}
	inner, _ := outer.Request.(InnerRequest)
	gc := AsMap(inner.GenerationConfig)
	if AsMap(gc["thinkingConfig"]) != nil {
		t.Fatalf("flash native should not auto-inject thinkingConfig: %#v", gc)
	}
	contents := AsSlice(inner.Contents)
	if AsString(AsMap(contents[0])["role"]) != "user" {
		t.Fatalf("missing role %#v", contents[0])
	}
}

func TestAssistantHistoryDoesNotInventThoughtBlock(t *testing.T) {
	req := OpenAIRequest{
		Model: "gemini-3.7-flash",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: "again"},
		},
	}
	outer, _, _ := OpenAIToGemini(req, "proj", "a@gmail.com", "acc")
	inner, _ := outer.Request.(InnerRequest)
	found := false
	for _, item := range AsSlice(inner.Contents) {
		msg := AsMap(item)
		if AsString(msg["role"]) != "model" {
			continue
		}
		parts := AsSlice(msg["parts"])
		if len(parts) == 0 {
			t.Fatal("empty model parts")
		}
		if partIsThought(AsMap(parts[0])) {
			t.Fatalf("must not invent thought history %#v", parts[0])
		}
		found = true
	}
	if !found {
		t.Fatal("missing model message")
	}
}

func TestGeminiToOpenAIKeepsThoughtSeparate(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"secret answer","thought":true}]},"finishReason":"STOP"}]}`)
	out, err := GeminiToOpenAI("gemini-3.7-flash", raw, "chatcmpl-1")
	if err != nil {
		t.Fatal(err)
	}
	payload := mustMap(out)
	choices := AsSlice(payload["choices"])
	msg := AsMap(AsMap(choices[0])["message"])
	if AsString(msg["content"]) != "" || AsString(msg["reasoning_content"]) != "secret answer" {
		t.Fatalf("content %#v", msg["content"])
	}
}
