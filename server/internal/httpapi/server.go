package httpapi

import (
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/wo/antigravity2api/internal/cloudcode"
	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/oauth"
	"github.com/wo/antigravity2api/internal/outbound"
	"github.com/wo/antigravity2api/internal/pool"
	"github.com/wo/antigravity2api/internal/store"
)

type Server struct {
	cfg   config.Config
	store *store.Store
	oauth *oauth.Client
	cc    *cloudcode.Client
	pool  *pool.Pool
	out   *outbound.Manager
	web   fs.FS
}

func New(cfg config.Config, st *store.Store, webFS fs.FS) *Server {
	out := outbound.New()
	if err := out.Apply(st.BoolSetting("proxy_enabled", false), st.GetSetting("proxy_url", "")); err != nil {
		log.Printf("ignore stored proxy: %v", err)
		_ = out.Apply(false, st.GetSetting("proxy_url", ""))
	}
	oa := oauth.New(cfg, out)
	cc := cloudcode.New(cfg, out)
	return &Server{
		cfg:   cfg,
		store: st,
		oauth: oa,
		cc:    cc,
		pool:  pool.New(cfg, st, oa, cc),
		out:   out,
		web:   webFS,
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"*"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/health", s.health)
	r.Get("/healthz", s.health)

	r.Route("/api", func(r chi.Router) {
		r.Use(s.adminAuth)
		r.Get("/dashboard", s.dashboard)
		r.Get("/settings", s.getSettings)
		r.Put("/settings", s.putSettings)
		r.Get("/batches", s.listBatches)
		r.Post("/batches", s.createBatchImport)
		r.Get("/batches/{id}", s.getBatch)
		r.Patch("/batches/{id}", s.updateBatch)
		r.Delete("/batches/{id}", s.deleteBatch)
		r.Get("/accounts", s.listAccounts)
		r.Get("/accounts/{id}", s.getAccount)
		r.Post("/accounts/{id}/refresh", s.refreshAccount)
		r.Post("/accounts/{id}/disable", s.disableAccount)
		r.Post("/accounts/{id}/enable", s.enableAccount)
		r.Delete("/accounts/{id}", s.deleteAccount)
		r.Post("/accounts/refresh-all", s.refreshAll)
		r.Get("/logs", s.listLogs)
		r.Delete("/logs", s.clearLogs)
		r.Get("/models", s.listModels)
		r.Get("/model-routes", s.listModelRoutes)
		r.Put("/model-routes", s.putModelRoutes)
	})

	r.Group(func(r chi.Router) {
		r.Use(s.apiAuth)
		r.Get("/v1/models", s.openaiModels)
		r.Post("/v1/chat/completions", s.openaiChat)
		r.Post("/v1/completions", s.openaiChat)
		r.Post("/v1/responses", s.openaiChat)
		r.Post("/responses", s.openaiChat)
		r.Post("/v1/messages", s.claudeMessages)
		r.Get("/v1/models/claude", s.openaiModels)
		r.Get("/v1beta/models", s.geminiList)
		r.HandleFunc("/v1beta/models/*", s.geminiGenerate)
	})

	if s.web != nil {
		fileServer(r, s.web)
	}
	return r
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "antigravity2api"})
}

func (s *Server) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			token = r.Header.Get("X-Admin-Token")
		}
		if !s.validSecret(token) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) apiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearer(r)
		if got == "" {
			got = r.Header.Get("x-api-key")
		}
		if !s.validSecret(got) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "invalid api key", "type": "unauthorized"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validSecret(got string) bool {
	got = strings.TrimSpace(strings.TrimPrefix(got, "Bearer "))
	admin := strings.TrimSpace(s.store.GetSetting("admin_token", s.cfg.AdminToken))
	apiKey := strings.TrimSpace(s.store.GetSetting("api_key", s.cfg.APIKey))
	if admin == "" && apiKey == "" {
		return true
	}
	return got != "" && (got == admin || got == apiKey)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(h)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 32<<20))
	dec.UseNumber()
	return dec.Decode(v)
}

func fileServer(r chi.Router, web fs.FS) {
	r.Get("/", serveIndex(web))
	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/")
		if path == "" {
			serveIndex(web)(w, req)
			return
		}
		f, err := web.Open(path)
		if err != nil {
			serveIndex(web)(w, req)
			return
		}
		stat, err := f.Stat()
		f.Close()
		if err != nil || stat.IsDir() {
			serveIndex(web)(w, req)
			return
		}
		http.FileServer(http.FS(web)).ServeHTTP(w, req)
	})
}

func serveIndex(web fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := web.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, f)
	}
}

func logErr(msg string, err error) {
	if err != nil {
		log.Printf("%s: %v", msg, err)
	}
}
