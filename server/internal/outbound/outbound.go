package outbound

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

type Manager struct {
	mu        sync.RWMutex
	transport *http.Transport
	enabled   bool
	rawURL    string
}

func New() *Manager {
	m := &Manager{}
	m.transport = defaultTransport()
	return m
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
	tr := m.transport
	m.mu.RUnlock()
	return &http.Client{Timeout: timeout, Transport: tr}
}

func (m *Manager) Apply(enabled bool, raw string) error {
	raw = Normalize(raw)
	if !enabled {
		m.mu.Lock()
		m.transport = defaultTransport()
		m.enabled = false
		m.rawURL = raw
		m.mu.Unlock()
		return nil
	}
	if raw == "" {
		return fmt.Errorf("请填写代理地址")
	}
	tr, err := buildTransport(raw)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.transport = tr
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

func defaultTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

func buildTransport(raw string) (*http.Transport, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("代理地址缺少主机")
	}
	tr := defaultTransport()
	tr.DisableKeepAlives = true
	tr.MaxIdleConns = 0
	tr.MaxIdleConnsPerHost = 0
	tr.IdleConnTimeout = 0
	tr.ForceAttemptHTTP2 = false
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		tr.Proxy = http.ProxyURL(u)
		return tr, nil
	case "socks5", "socks5h", "socks":
		var auth *proxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pass}
		}
		dialer, err := proxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 8 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 代理无效: %w", err)
		}
		tr.Proxy = nil
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			tr.DialContext = cd.DialContext
		} else {
			tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return dialer.Dial(network, address)
			}
		}
		return tr, nil
	default:
		return nil, fmt.Errorf("不支持的代理协议: %s", u.Scheme)
	}
}
