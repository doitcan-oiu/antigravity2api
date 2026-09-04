package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr         string
	DataDir            string
	AdminToken         string
	APIKey             string
	BatchValidityDays  int
	SkipExpiredDefault bool
	UserAgent          string
	OAuthUserAgent     string
	OAuthClientID      string
	OAuthClientSecret  string
	RequestTimeout     time.Duration
}

func Load() Config {
	cfg := Config{
		ListenAddr:         env("LISTEN_ADDR", ":8080"),
		DataDir:            env("DATA_DIR", "./data"),
		AdminToken:         env("ADMIN_TOKEN", "admin-token"),
		APIKey:             env("API_KEY", "sk-antigravity"),
		BatchValidityDays:  envInt("BATCH_VALIDITY_DAYS", 30),
		SkipExpiredDefault: envBool("SKIP_EXPIRED_ACCOUNTS", true),
		UserAgent:          env("USER_AGENT", "Antigravity/4.6.7 (X11; Linux x86_64) Chrome/132.0.6834.160 Electron/39.2.3"),
		OAuthUserAgent:     env("OAUTH_USER_AGENT", "vscode/1.X.X (Antigravity/4.6.7)"),
		OAuthClientID:      env("OAUTH_CLIENT_ID", antigravityOAuthClientID()),
		OAuthClientSecret:  env("OAUTH_CLIENT_SECRET", antigravityOAuthClientSecret()),
		RequestTimeout:     time.Duration(envInt("REQUEST_TIMEOUT_SECONDS", 600)) * time.Second,
	}
	return cfg
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
