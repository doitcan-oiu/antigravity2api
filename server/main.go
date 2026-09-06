package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/httpapi"
	"github.com/wo/antigravity2api/internal/store"
)

//go:embed all:web
var webEmbed embed.FS

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg := config.Load()
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	if os.Getenv("API_KEY") != "" || st.GetSetting("api_key", "") == "" {
		if err := st.SetSetting("api_key", cfg.APIKey); err != nil {
			return err
		}
	}
	if os.Getenv("ADMIN_TOKEN") != "" || st.GetSetting("admin_token", "") == "" {
		if err := st.SetSetting("admin_token", cfg.AdminToken); err != nil {
			return err
		}
	}

	webFS, err := fs.Sub(webEmbed, "web")
	if err != nil {
		return err
	}

	api := httpapi.New(cfg, st, webFS)
	defer api.Close()
	maintenance, stopMaintenance := context.WithCancel(context.Background())
	var maintenanceWG sync.WaitGroup
	maintenanceWG.Add(1)
	go func() {
		defer maintenanceWG.Done()
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-maintenance.Done():
				return
			case <-t.C:
				st.TrimLogs(5000)
			}
		}
	}()
	defer func() { stopMaintenance(); maintenanceWG.Wait() }()
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("antigravity2api listening on %s (data=%s)", cfg.ListenAddr, abs(cfg.DataDir))
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			api.Close()
			_ = server.Close()
			return err
		}
		return nil
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
