package outbound

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"
	"github.com/imroc/req/v3/http2"
	utls "github.com/refraction-networking/utls"
)

type Manager struct {
	mu      sync.RWMutex
	client  *req.Client
	enabled bool
	rawURL  string
}

func New() *Manager {
	return &Manager{client: newChromeClient("")}
}

func (m *Manager) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

func (m *Manager) URL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rawURL
}

func (m *Manager) Client(timeout time.Duration) *http.Client {
	m.mu.RLock()
	c := m.client
	m.mu.RUnlock()
	hc := c.GetClient()
	return &http.Client{
		Timeout:   timeout,
		Transport: hc.Transport,
	}
}

func (m *Manager) Apply(enabled bool, raw string) error {
	raw = Normalize(raw)
	if !enabled {
		c := newChromeClient("")
		m.mu.Lock()
		m.client = c
		m.enabled = false
		m.rawURL = raw
		m.mu.Unlock()
		return nil
	}
	if raw == "" {
		return fmt.Errorf("请填写代理地址")
	}
	u, err := parseProxyURL(raw)
	if err != nil {
		return err
	}
	c := newChromeClient(u.String())
	m.mu.Lock()
	m.client = c
	m.enabled = true
	m.rawURL = raw
	m.mu.Unlock()
	return nil
}

func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, `\@`, "@")
	return raw
}

func parseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("代理地址缺少主机")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	case "socks":
		u.Scheme = "socks5"
		return u, nil
	default:
		return nil, fmt.Errorf("不支持的代理协议: %s", u.Scheme)
	}
}

func newChromeClient(proxyRaw string) *req.Client {
	c := req.C().
		SetTimeout(0).
		SetTLSFingerprint(utls.HelloChrome_120).
		SetHTTP2SettingsFrame(
			http2.Setting{ID: http2.SettingHeaderTableSize, Val: 65536},
			http2.Setting{ID: http2.SettingEnablePush, Val: 0},
			http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: 1000},
			http2.Setting{ID: http2.SettingInitialWindowSize, Val: 6291456},
			http2.Setting{ID: http2.SettingMaxHeaderListSize, Val: 262144},
		).
		SetHTTP2ConnectionFlow(15663105).
		SetCommonPseudoHeaderOder(":method", ":authority", ":scheme", ":path").
		SetHTTP2HeaderPriority(http2.PriorityParam{StreamDep: 0, Exclusive: true, Weight: 255}).
		SetCommonHeaderOrder(
			"authorization",
			"content-type",
			"user-agent",
			"x-client-name",
			"x-client-version",
			"x-machine-id",
			"x-vscode-sessionid",
			"x-goog-user-project",
			"accept-encoding",
		)
	tr := c.GetTransport()
	tr.MaxIdleConns = 100
	tr.MaxIdleConnsPerHost = 20
	tr.IdleConnTimeout = 90 * time.Second
	tr.TLSHandshakeTimeout = 20 * time.Second
	tr.ResponseHeaderTimeout = 0
	tr.DisableKeepAlives = false
	if proxyRaw != "" {
		c.SetProxyURL(proxyRaw)
	}
	return c
}
