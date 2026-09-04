package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/outbound"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.DashboardStats(r.URL.Query().Get("range"))
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	d.CatalogModels = len(s.modelCatalog())
	d.HasAPIKey = strings.TrimSpace(s.store.GetSetting("api_key", s.cfg.APIKey)) != ""
	writeJSON(w, 200, d)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, models.Settings{
		APIKey:              s.store.GetSetting("api_key", s.cfg.APIKey),
		AdminToken:          s.store.GetSetting("admin_token", s.cfg.AdminToken),
		SkipExpiredAccounts: s.pool.SkipExpired(),
		EnableLogging:       s.loggingEnabled(),
		ListenAddr:          s.cfg.ListenAddr,
		BatchValidityDays:   atoi(s.store.GetSetting("batch_validity_days", strconv.Itoa(s.cfg.BatchValidityDays)), s.cfg.BatchValidityDays),
		ProxyEnabled:        s.store.BoolSetting("proxy_enabled", false),
		ProxyURL:            s.store.GetSetting("proxy_url", ""),
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var body models.Settings
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	proxyURL := outbound.Normalize(body.ProxyURL)
	if err := s.out.Apply(body.ProxyEnabled, proxyURL); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if body.APIKey != "" {
		_ = s.store.SetSetting("api_key", body.APIKey)
	}
	if body.AdminToken != "" {
		_ = s.store.SetSetting("admin_token", body.AdminToken)
	}
	skip := "false"
	if body.SkipExpiredAccounts {
		skip = "true"
	}
	_ = s.store.SetSetting("skip_expired_accounts", skip)
	logv := "false"
	if body.EnableLogging {
		logv = "true"
	}
	_ = s.store.SetSetting("enable_logging", logv)
	if body.BatchValidityDays > 0 {
		_ = s.store.SetSetting("batch_validity_days", strconv.Itoa(body.BatchValidityDays))
	}
	proxyOn := "false"
	if body.ProxyEnabled {
		proxyOn = "true"
	}
	_ = s.store.SetSetting("proxy_enabled", proxyOn)
	_ = s.store.SetSetting("proxy_url", proxyURL)
	s.getSettings(w, r)
}

func (s *Server) listBatches(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListBatches()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func parsePurchaseDate(v string) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	v = strings.TrimSpace(v)
	if v == "" {
		return today
	}
	t, err := time.ParseInLocation("2006-01-02", v, loc)
	if err != nil {
		return today
	}
	return t
}

func (s *Server) createBatchImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Note        string `json:"note"`
		Raw         string `json:"raw"`
		PurchasedAt string `json:"purchased_at"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	res, err := s.pool.Import(body.Name, body.Note, body.Raw, parsePurchaseDate(body.PurchasedAt))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) getBatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	b, err := s.store.GetBatch(id)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "batch not found"})
		return
	}
	accs, _ := s.store.ListAccounts(id)
	writeJSON(w, 200, map[string]any{"batch": b, "accounts": accs})
}

func (s *Server) updateBatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if err := s.store.UpdateBatch(id, body.Name, body.Note); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	s.getBatch(w, r)
}

func (s *Server) deleteBatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteBatch(id); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	batchID := r.URL.Query().Get("batch_id")
	list, err := s.store.ListAccounts(batchID)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	for i := range list {
		list[i].RefreshToken = ""
		list[i].AccessToken = ""
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	acc, err := s.store.GetAccount(id)
	if err != nil {
		writeJSON(w, 404, map[string]any{"error": "account not found"})
		return
	}
	acc.RefreshToken = ""
	acc.AccessToken = ""
	writeJSON(w, 200, acc)
}

func (s *Server) refreshAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	acc, err := s.pool.RefreshAccount(id)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "account": acc})
		return
	}
	acc.RefreshToken = ""
	acc.AccessToken = ""
	writeJSON(w, 200, acc)
}

func (s *Server) disableAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Reason string `json:"reason"`
	}
	_ = readJSON(r, &body)
	if body.Reason == "" {
		body.Reason = "手动停用"
	}
	if err := s.store.SetDisabled(id, true, body.Reason); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) enableAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.SetDisabled(id, false, ""); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAccount(id); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) refreshAll(w http.ResponseWriter, r *http.Request) {
	n, err := s.pool.RefreshAll()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"refreshed": n})
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.store.ListLogs(limit)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	total, success, errors, err := s.store.LogStats()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"items":   list,
		"total":   total,
		"success": success,
		"errors":  errors,
	})
}

func (s *Server) clearLogs(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ClearLogs(); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"items": s.modelCatalog()})
}

func (s *Server) listModelRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"items": s.loadMixRules()})
}

func (s *Server) putModelRoutes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []models.MixRule `json:"items"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	out := make([]models.MixRule, 0, len(body.Items))
	for _, item := range body.Items {
		from := strings.TrimSpace(item.From)
		to := strings.TrimSpace(item.To)
		if from == "" || to == "" {
			continue
		}
		if item.Percent < 0 {
			item.Percent = 0
		}
		if item.Percent > 100 {
			item.Percent = 100
		}
		if strings.TrimSpace(item.ID) == "" {
			item.ID = uuid.NewString()
		}
		item.From, item.To = from, to
		out = append(out, item)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	if err := s.store.SetSetting("model_routes", string(raw)); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) loadMixRules() []models.MixRule {
	raw := strings.TrimSpace(s.store.GetSetting("model_routes", "[]"))
	if raw == "" {
		return []models.MixRule{}
	}
	var items []models.MixRule
	if json.Unmarshal([]byte(raw), &items) != nil {
		return []models.MixRule{}
	}
	if items == nil {
		return []models.MixRule{}
	}
	return items
}

func atoi(s string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return n
}
