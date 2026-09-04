package convert

import "testing"

func TestMatchMixPreview(t *testing.T) {
	if !MatchMixSource("gemini-3.1-pro", "gemini-3.1-pro-preview", "gemini-3.1-pro-preview") {
		t.Fatal("preview should match")
	}
	if MatchMixSource("gemini-3.1-pro", "gemini-3.1-pro-image", "gemini-3.1-pro-image") {
		t.Fatal("image should not match pro")
	}
}

func TestApplyMixHit(t *testing.T) {
	rules := []MixRule{{From: "gemini-3.1-pro", To: "gemini-3.7-flash", Percent: 5, Enabled: true}}
	if got := ApplyMix("gemini-3.1-pro-preview", rules, 0); got != "gemini-3.7-flash" {
		t.Fatalf("got %s", got)
	}
	if got := ApplyMix("gemini-3.1-pro-preview", rules, 5); got != "gemini-3.1-pro-preview" {
		t.Fatalf("got %s", got)
	}
}

func TestApplyMixOpenAIAlias(t *testing.T) {
	rules := []MixRule{{From: "gemini-2.5-flash", To: "gemini-3-flash", Percent: 100, Enabled: true}}
	if got := ApplyMix("gpt-4o", rules, 0); got != "gemini-3-flash" {
		t.Fatalf("got %s", got)
	}
}
