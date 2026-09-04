package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/httpapi"
	"github.com/wo/antigravity2api/internal/store"
)

//go:embed all:web
var webEmbed embed.FS

func main() {
	cfg := config.Load()
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if os.Getenv("API_KEY") != "" || st.GetSetting("api_key", "") == "" {
		_ = st.SetSetting("api_key", cfg.APIKey)
	}
	if os.Getenv("ADMIN_TOKEN") != "" || st.GetSetting("admin_token", "") == "" {
		_ = st.SetSetting("admin_token", cfg.AdminToken)
	}

	webFS, err := fs.Sub(webEmbed, "web")
	if err != nil {
		log.Fatalf("web embed: %v", err)
	}

	api := httpapi.New(cfg, st, webFS)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		st.TrimLogs(5000)
		for range t.C {
			st.TrimLogs(5000)
		}
	}()
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("antigravity2api listening on %s (data=%s)", cfg.ListenAddr, abs(cfg.DataDir))
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func init() {
	if os.Getenv("TZ") == "" {
		os.Setenv("TZ", "Asia/Shanghai")
	}
}
