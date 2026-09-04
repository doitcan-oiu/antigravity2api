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
