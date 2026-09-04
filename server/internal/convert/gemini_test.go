package convert

import (
	"encoding/json"
	"testing"
)

func TestStampGeminiModel(t *testing.T) {
	raw := []byte(`{"modelVersion":"gemini-3.7-flash","candidates":[]}`)
	out := StampGeminiModel(raw, "gemini-3.1-pro-preview")
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "gemini-3.1-pro-preview" || payload["modelVersion"] != "gemini-3.1-pro-preview" {
		t.Fatalf("got %#v", payload)
	}
}
