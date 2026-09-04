package outbound

import "testing"

func TestNormalizeProxyURL(t *testing.T) {
	got := Normalize(`socks5://user:pass\@p.webshare.io:80`)
	if got != "socks5://user:pass@p.webshare.io:80" {
		t.Fatalf("got %s", got)
	}
}

func TestBuildTransportSOCKS(t *testing.T) {
	tr, err := buildTransport("socks5://user:pass@p.webshare.io:80")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Fatal("socks should not use HTTP proxy")
	}
	if !tr.DisableKeepAlives {
		t.Fatal("rotating proxy should disable keep-alives")
	}
}

func TestBuildTransportHTTP(t *testing.T) {
	tr, err := buildTransport("http://user:pass@proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy == nil {
		t.Fatal("http proxy missing")
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
