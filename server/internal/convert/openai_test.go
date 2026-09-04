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
