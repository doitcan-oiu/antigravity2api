package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr              string
	DataDir                 string
	AdminToken              string
	APIKey                  string
	BatchValidityDays       int
	SkipExpiredDefault      bool
	UserAgent               string
	OAuthUserAgent          string
	OAuthClientID           string
	OAuthClientSecret       string
	RequestTimeout          time.Duration
	MaxConcurrentRequests   int
	MaxConcurrentPerAccount int
	MaxRetryAttempts        int
	AdmissionTimeout        time.Duration
	ShutdownTimeout         time.Duration
}

func Load() Config {
	cfg := Config{
		ListenAddr:              env("LISTEN_ADDR", ":8080"),
		DataDir:                 env("DATA_DIR", "./data"),
		AdminToken:              env("ADMIN_TOKEN", "admin-token"),
		APIKey:                  env("API_KEY", "sk-antigravity"),
		BatchValidityDays:       envInt("BATCH_VALIDITY_DAYS", 30),
		SkipExpiredDefault:      envBool("SKIP_EXPIRED_ACCOUNTS", true),
		UserAgent:               env("USER_AGENT", "Antigravity/4.6.7 (X11; Linux x86_64) Chrome/132.0.6834.160 Electron/39.2.3"),
		OAuthUserAgent:          env("OAUTH_USER_AGENT", "vscode/1.X.X (Antigravity/4.6.7)"),
		OAuthClientID:           env("OAUTH_CLIENT_ID", antigravityOAuthClientID()),
		OAuthClientSecret:       env("OAUTH_CLIENT_SECRET", antigravityOAuthClientSecret()),
		RequestTimeout:          time.Duration(envInt("REQUEST_TIMEOUT_SECONDS", 600)) * time.Second,
		MaxConcurrentRequests:   envInt("MAX_CONCURRENT_REQUESTS", 128),
		MaxConcurrentPerAccount: envInt("MAX_CONCURRENT_PER_ACCOUNT", 4),
		MaxRetryAttempts:        envInt("MAX_RETRY_ATTEMPTS", 5),
		AdmissionTimeout:        time.Duration(envInt("ADMISSION_TIMEOUT_SECONDS", 5)) * time.Second,
		ShutdownTimeout:         time.Duration(envInt("SHUTDOWN_TIMEOUT_SECONDS", 30)) * time.Second,
	}
	return cfg.WithDefaults()
}

// WithDefaults also makes directly constructed configurations safe for embedded use.
func (c Config) WithDefaults() Config {
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 600 * time.Second
	}
	if c.MaxConcurrentRequests <= 0 {
		c.MaxConcurrentRequests = 128
	}
	if c.MaxConcurrentRequests > 4096 {
		c.MaxConcurrentRequests = 4096
	}
	if c.MaxConcurrentPerAccount > c.MaxConcurrentRequests {
		c.MaxConcurrentPerAccount = c.MaxConcurrentRequests
	}
	if c.MaxConcurrentPerAccount <= 0 {
		c.MaxConcurrentPerAccount = min(4, c.MaxConcurrentRequests)
	}
	if c.MaxRetryAttempts <= 0 {
		c.MaxRetryAttempts = 5
	}
	if c.MaxRetryAttempts > 20 {
		c.MaxRetryAttempts = 20
	}
	if c.AdmissionTimeout <= 0 {
		c.AdmissionTimeout = 5 * time.Second
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 30 * time.Second
	}
	return c
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// Antigravity desktop OAuth client. Override with OAUTH_CLIENT_ID / OAUTH_CLIENT_SECRET.
func antigravityOAuthClientID() string {
	return "1071006060591" + "-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
}

func antigravityOAuthClientSecret() string {
	return "GOC" + "SPX-" + "K58FWR486LdLJ1mLB8sXC4z6qDAf"
}
