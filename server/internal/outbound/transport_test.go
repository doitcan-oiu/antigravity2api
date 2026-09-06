package outbound

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type transportTestRoundTripper func(*http.Request) (*http.Response, error)

func (f transportTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestManagerDoPreservesRequestTransportContract(t *testing.T) {
	m := New()
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const body = `{"request":{"contents":[]}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unused.invalid/action", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Multiple", "first")
	req.Header.Add("X-Multiple", "second")
	m.client.GetClient().Transport = transportTestRoundTripper(func(got *http.Request) (*http.Response, error) {
		if got.Context() != ctx {
			t.Error("request context was replaced")
		}
		if got.ContentLength != int64(len(body)) || len(got.TransferEncoding) != 0 {
			t.Errorf("request framing changed: length=%d, encoding=%v", got.ContentLength, got.TransferEncoding)
		}
		if values := got.Header.Values("X-Multiple"); !reflect.DeepEqual(values, []string{"first", "second"}) {
			t.Errorf("multiple header values changed: %v", values)
		}
		if got.GetBody == nil {
			t.Error("request lost its replay function")
		} else {
			replay, replayErr := got.GetBody()
			if replayErr != nil {
				t.Error(replayErr)
			} else {
				replayed, readErr := io.ReadAll(replay)
				replay.Close()
				if readErr != nil || string(replayed) != body {
					t.Errorf("replayed body=%q, error=%v", replayed, readErr)
				}
			}
		}
		data, readErr := io.ReadAll(got.Body)
		got.Body.Close()
		if readErr != nil || string(data) != body {
			t.Errorf("body=%q, error=%v", data, readErr)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: got}, nil
	})
	resp, err := m.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := m.Do(nil); err == nil {
		t.Fatal("nil request should fail")
	}
}

func TestManagerDoReplaysFixedLengthPOSTOnTemporaryRedirect(t *testing.T) {
	const body = `{"hello":"world"}`
	type received struct {
		method string
		path   string
		length int64
		values []string
		body   string
		err    error
	}
	requests := make(chan received, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		requests <- received{r.Method, r.URL.Path, r.ContentLength, r.Header.Values("X-Multiple"), string(data), err}
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "/destination")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	m := New()
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/start", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Multiple", "first")
	req.Header.Add("X-Multiple", "second")
	resp, err := m.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("redirect was not followed: %d", resp.StatusCode)
	}
	for _, path := range []string{"/start", "/destination"} {
		select {
		case got := <-requests:
			if got.err != nil || got.path != path || got.method != http.MethodPost || got.length != int64(len(body)) || got.body != body {
				t.Errorf("redirect request changed: %+v", got)
			}
			if !reflect.DeepEqual(got.values, []string{"first", "second"}) {
				t.Errorf("redirect header values changed: %v", got.values)
			}
		case <-ctx.Done():
			t.Fatal("missing redirected request")
		}
	}
}

func TestManagerDoHonorsCancellation(t *testing.T) {
	m := New()
	t.Cleanup(m.Close)
	started := make(chan struct{})
	m.client.GetClient().Transport = transportTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unused.invalid/action", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := m.Do(req)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("transport was not called")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request outlived its canceled context")
	}
}
