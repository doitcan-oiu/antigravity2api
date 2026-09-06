package outbound

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// This exercises Manager.Do's underlying http.Client path against a local TLS
// peer, so accidentally replacing req's transport with net/http is observable.
func TestManagerDoPreservesChromeHelloAndHTTP2(t *testing.T) {
	hellos := make(chan []uint16, 1)
	requests := make(chan *http.Request, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requests <- r:
		default:
		}
		_, _ = io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		select {
		case hellos <- append([]uint16(nil), hello.CipherSuites...):
		default:
		}
		return nil, nil
	}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	m := New()
	t.Cleanup(m.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	m.client.GetTLSClientConfig().RootCAs = roots
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{"request":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer synthetic-token")
	r.Header.Set("User-Agent", "Antigravity/transport-test")
	resp, err := m.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("negotiated %s, want HTTP/2", resp.Proto)
	}
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_120)
	if err != nil {
		t.Fatal(err)
	}
	withoutGREASE := func(values []uint16) []uint16 {
		var result []uint16
		for _, value := range values {
			if value&0x0f0f != 0x0a0a || value>>8 != value&0xff {
				result = append(result, value)
			}
		}
		return result
	}
	select {
	case hello := <-hellos:
		if !reflect.DeepEqual(withoutGREASE(hello), withoutGREASE(spec.CipherSuites)) {
			t.Fatalf("Chrome ClientHello cipher order changed: %x", hello)
		}
	case <-ctx.Done():
		t.Fatal("local TLS peer did not observe a ClientHello")
	}
	select {
	case got := <-requests:
		if got.Header.Get("Authorization") != "Bearer synthetic-token" || got.Header.Get("User-Agent") != "Antigravity/transport-test" {
			t.Fatalf("explicit identity headers changed: %v", got.Header)
		}
	case <-ctx.Done():
		t.Fatal("local HTTP/2 peer did not receive the request")
	}
}

func TestManagerDoPreservesConfiguredHeaderOrderOnWire(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	observed := make(chan []string, 1)
	serverError := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverError <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		var headers []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				serverError <- err
				return
			}
			if line == "\r\n" {
				break
			}
			if name, _, ok := strings.Cut(line, ":"); ok {
				headers = append(headers, strings.ToLower(name))
			}
		}
		observed <- headers
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		serverError <- err
	}()
	m := New()
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"authorization", "content-type", "user-agent", "x-client-name", "x-client-version", "x-machine-id", "x-vscode-sessionid", "x-goog-user-project", "accept-encoding"}
	for _, name := range want {
		r.Header.Set(name, "synthetic")
	}
	resp, err := m.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	select {
	case got := <-observed:
		var ordered []string
		for _, name := range got {
			if name != "host" && name != "connection" {
				ordered = append(ordered, name)
			}
		}
		if !reflect.DeepEqual(ordered, want) {
			t.Fatalf("wire header order=%v, want=%v", ordered, want)
		}
	case <-ctx.Done():
		t.Fatal("local peer did not observe request headers")
	}
	select {
	case err := <-serverError:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("local peer did not finish its response")
	}
}
