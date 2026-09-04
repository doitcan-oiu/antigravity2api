package oauth

import "testing"

func TestExtractTokensJSON(t *testing.T) {
	raw := `[{"refresh_token":"1//abc_def-123"},{"refresh_token":"1//abc_def-123"},{"refresh_token":"nope"}]`
	got := ExtractTokens(raw)
	if len(got) != 1 || got[0] != "1//abc_def-123" {
		t.Fatalf("got %#v", got)
	}
}

func TestExtractTokensLines(t *testing.T) {
	raw := "1//aaa\nfoo 1//bbb-ccc\n"
	got := ExtractTokens(raw)
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
}
