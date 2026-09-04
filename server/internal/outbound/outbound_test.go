package outbound

import "testing"

func TestNormalizeProxyURL(t *testing.T) {
	got := Normalize(`socks5://user:pass\@p.webshare.io:80`)
	if got != "socks5://user:pass@p.webshare.io:80" {
		t.Fatalf("got %s", got)
	}
}

func TestParseProxyURL(t *testing.T) {
	u, err := parseProxyURL("socks5://user:pass@p.webshare.io:80")
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "socks5" || u.Host != "p.webshare.io:80" {
		t.Fatalf("got %s", u.String())
	}
	u, err = parseProxyURL("socks://127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "socks5" {
		t.Fatalf("socks should normalize to socks5, got %s", u.Scheme)
	}
	if _, err := parseProxyURL("ftp://x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestApplySOCKS(t *testing.T) {
	m := New()
	if err := m.Apply(true, "socks5://user:pass@p.webshare.io:80"); err != nil {
		t.Fatal(err)
	}
	if !m.Enabled() {
		t.Fatal("expected enabled")
	}
	if m.Client(0) == nil || m.Client(0).Transport == nil {
		t.Fatal("missing chrome transport")
	}
}

func TestApplyHTTP(t *testing.T) {
	m := New()
	if err := m.Apply(true, "http://user:pass@proxy.example:8080"); err != nil {
		t.Fatal(err)
	}
	if !m.Enabled() {
		t.Fatal("expected enabled")
	}
}

func TestApplyDisabled(t *testing.T) {
	m := New()
	if err := m.Apply(true, "socks5://u:p@127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if !m.Enabled() {
		t.Fatal("expected enabled")
	}
	if err := m.Apply(false, "socks5://u:p@127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if m.Enabled() {
		t.Fatal("expected disabled")
	}
}

func TestApplyEmptyEnabled(t *testing.T) {
	m := New()
	if err := m.Apply(true, "  "); err == nil {
		t.Fatal("expected error")
	}
}
