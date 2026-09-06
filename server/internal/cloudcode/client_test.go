package cloudcode

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wo/antigravity2api/internal/config"
)

func TestParseAntigravityVersion(t *testing.T) {
	got := parseAntigravityVersion("Antigravity/4.6.7 (X11; Linux x86_64) Chrome/132.0.6834.160 Electron/39.2.3")
	if got != "4.6.7" {
		t.Fatalf("got %s", got)
	}
	if parseAntigravityVersion("curl/8") != "4.6.7" {
		t.Fatal("fallback version")
	}
}

func TestPayloadProject(t *testing.T) {
	if got := payloadProject(map[string]any{"project": "abc"}); got != "abc" {
		t.Fatalf("got %s", got)
	}
	if got := payloadProject(map[string]any{"project": "test-project"}); got != "" {
		t.Fatalf("placeholder should be empty, got %s", got)
	}
	if got := payloadProject(struct {
		Project string `json:"project"`
	}{Project: "xyz"}); got != "xyz" {
		t.Fatalf("struct project got %s", got)
	}
}

func TestApplyHeaders(t *testing.T) {
	c := &Client{
		cfg:       config.Config{UserAgent: "Antigravity/4.6.7 (X11; Linux x86_64) Chrome/132.0.6834.160 Electron/39.2.3"},
		machineID: "machine-1",
		sessionID: "session-1",
		version:   "4.6.7",
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	c.applyHeaders(req, "tok", "loadCodeAssist", map[string]any{"project": "proj-1"})
	if req.Header.Get("x-client-name") != "antigravity" {
		t.Fatal("missing x-client-name")
	}
	if req.Header.Get("x-client-version") != "4.6.7" {
		t.Fatal("missing x-client-version")
	}
	if req.Header.Get("x-machine-id") != "machine-1" {
		t.Fatal("missing x-machine-id")
	}
	if req.Header.Get("x-vscode-sessionid") != "session-1" {
		t.Fatal("missing x-vscode-sessionid")
	}
	if req.Header.Get("x-goog-user-project") != "proj-1" {
		t.Fatal("metadata request should send project header")
	}
	if req.Header.Get("Accept") == "text/event-stream" {
		t.Fatal("should not send SSE accept on official client")
	}

	content, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	c.applyHeaders(content, "tok", "generateContent", map[string]any{"project": "proj-1"})
	if content.Header.Get("x-goog-user-project") != "" {
		t.Fatal("content request must omit x-goog-user-project")
	}

	claude, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	c.applyHeaders(claude, "tok", "streamGenerateContent", map[string]any{"model": "claude-sonnet-4-5"})
	if claude.Header.Get("anthropic-beta") == "" {
		t.Fatal("claude request should send anthropic-beta")
	}
}

func TestChunkedBodyHasNoLen(t *testing.T) {
	r := requestBody([]byte(`{"a":1}`), true)
	if _, ok := r.(interface{ Len() int }); ok {
		t.Fatal("chunked body should not expose Len()")
	}
	b, err := io.ReadAll(r)
	if err != nil || string(b) != `{"a":1}` {
		t.Fatalf("read %s %v", b, err)
	}
	fixed := requestBody([]byte(`{"a":1}`), false)
	if _, ok := fixed.(interface{ Len() int }); !ok {
		t.Fatal("fixed body should expose Len()")
	}
}

func TestMachineIDPersists(t *testing.T) {
	dir := t.TempDir()
	id1 := loadOrCreateMachineID(dir)
	id2 := loadOrCreateMachineID(dir)
	if id1 == "" || id1 != id2 {
		t.Fatalf("machine id not stable: %s %s", id1, id2)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "machine-id"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got == "" {
		t.Fatal("empty machine-id file")
	}
}

func TestServiceDisabled(t *testing.T) {
	if !isServiceDisabled([]byte(`{"error":{"status":"SERVICE_DISABLED"}}`)) {
		t.Fatal("expected match")
	}
	if isServiceDisabled([]byte(`{"error":"nope"}`)) {
		t.Fatal("unexpected match")
	}
}

func TestNoLenReaderType(t *testing.T) {
	var r io.Reader = noLenReader{}
	if reflect.TypeOf(r).Name() == "" {
		t.Fatal("should be named type")
	}
}

func TestParseQuotaSummary(t *testing.T) {
	raw := []byte(`{
	  "groups": [
	    {
	      "displayName": "Gemini Models",
	      "description": "Models within this group: Gemini Flash, Gemini Pro",
	      "buckets": [
	        {
	          "bucketId": "gemini-weekly",
	          "window": "weekly",
	          "remainingFraction": 0.62,
	          "resetTime": "2026-09-10T18:25:10Z",
	          "displayName": "Weekly Limit Remaining",
	          "description": "You have used some of your weekly limit, it will fully refresh in 5 days, 22 hours."
	        },
	        {
	          "bucketId": "gemini-5h",
	          "window": "5h",
	          "remainingFraction": "1",
	          "resetTime": "2026-09-06T18:32:27Z",
	          "displayName": "Five Hour Limit Remaining"
	        }
	      ]
	    },
	    {
	      "displayName": "Claude and GPT models",
	      "buckets": [
	        {
	          "bucketId": "3p-weekly",
	          "window": "weekly",
	          "remainingFraction": 0,
	          "resetTime": "2026-09-08T06:53:00Z",
	          "displayName": "Weekly Limit Remaining"
	        },
	        {
	          "bucketId": "3p-5h",
	          "window": "5h",
	          "remainingFraction": {"value": 1},
	          "resetTime": "2026-09-06T18:32:27Z",
	          "displayName": "Five Hour Limit Remaining"
	        }
	      ]
	    }
	  ]
	}`)
	groups := parseQuotaSummary(raw)
	if len(groups) != 2 {
		t.Fatalf("groups=%d", len(groups))
	}
	if groups[0].Buckets[0].Window != "weekly" || groups[0].Buckets[0].RemainingFraction != 0.62 {
		t.Fatalf("gemini weekly %+v", groups[0].Buckets[0])
	}
	if groups[0].Buckets[1].Window != "5h" || groups[0].Buckets[1].RemainingFraction != 1 {
		t.Fatalf("gemini 5h %+v", groups[0].Buckets[1])
	}
	if groups[1].Buckets[0].RemainingFraction != 0 {
		t.Fatalf("claude weekly %+v", groups[1].Buckets[0])
	}
	if groups[1].Buckets[1].RemainingFraction != 1 {
		t.Fatalf("claude 5h %+v", groups[1].Buckets[1])
	}

	wrapped := []byte(`{"quotaGroups":[{"display_name":"Gemini Models","buckets":[{"bucket_id":"gemini-weekly","window":"weekly","remaining_fraction":0.5}]}]}`)
	got := parseQuotaSummary(wrapped)
	if len(got) != 1 || got[0].Buckets[0].BucketID != "gemini-weekly" || got[0].Buckets[0].RemainingFraction != 0.5 {
		t.Fatalf("snake/camel fallback %+v", got)
	}
}
