package convert

import "testing"

func TestRewriteToAvailableForwarding(t *testing.T) {
	got := RewriteToAvailable("gemini-3-pro-preview", []string{"gemini-3.1-pro-preview"}, map[string]string{
		"gemini-3-pro-preview": "gemini-3.1-pro-preview",
	})
	if got != "gemini-3.1-pro-preview" {
		t.Fatalf("got %s", got)
	}
}

func TestRewriteToAvailableExactOfficial(t *testing.T) {
	got := RewriteToAvailable("gemini-3-flash", []string{"gemini-3-flash-agent", "gemini-3-flash"}, nil)
	if got != "gemini-3-flash" {
		t.Fatalf("got %s", got)
	}
}

func TestRewriteToAvailableProFamily(t *testing.T) {
	got := RewriteToAvailable("gemini-3-pro-preview", []string{"gemini-3.1-pro-preview"}, nil)
	if got != "gemini-3.1-pro-preview" {
		t.Fatalf("got %s", got)
	}
}

func TestBuildCatalogUsesOfficialIDs(t *testing.T) {
	catalog := BuildCatalog([]OfficialModel{
		{ID: "gemini-3-flash-agent", DisplayName: "Gemini 3 Flash"},
		{ID: "claude-sonnet-4-6-thinking", DisplayName: "Claude Sonnet"},
	})
	ids := map[string]bool{}
	for _, item := range catalog {
		id, _ := item["id"].(string)
		ids[id] = true
		if id == "gemini-3-flash-agent" && item["official"] != true {
			t.Fatal("official flag missing")
		}
	}
	if !ids["gemini-3-flash-agent"] {
		t.Fatal("missing official model")
	}
	if !ids["gpt-4o"] {
		t.Fatal("missing alias")
	}
}
