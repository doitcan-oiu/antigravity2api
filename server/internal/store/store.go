package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wo/antigravity2api/internal/models"

	_ "modernc.org/sqlite"
)

type Store struct {
	db         *sql.DB
	settingsMu sync.RWMutex
	settings   map[string]string
	logCh      chan models.RequestLog
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dataDir, "antigravity2api.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, settings: map[string]string{}, logCh: make(chan models.RequestLog, 2048)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	s.loadSettings()
	go s.logWorker()
	return s, nil
}

func (s *Store) Close() error {
	close(s.logCh)
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS batches (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  note TEXT,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  batch_id TEXT NOT NULL,
  email TEXT NOT NULL,
  name TEXT,
  refresh_token TEXT NOT NULL UNIQUE,
  access_token TEXT,
  expires_in INTEGER,
  expiry_timestamp INTEGER,
  project_id TEXT,
  subscription_tier TEXT,
  quota_json TEXT,
  disabled INTEGER NOT NULL DEFAULT 0,
  disabled_reason TEXT,
  last_used INTEGER,
  last_error TEXT,
  rate_limited_until INTEGER,
  created_at INTEGER NOT NULL,
  FOREIGN KEY(batch_id) REFERENCES batches(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS request_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at INTEGER NOT NULL,
  protocol TEXT,
  model TEXT,
  mapped_model TEXT,
  account_id TEXT,
  account_email TEXT,
  status INTEGER,
  stream INTEGER,
  latency_ms INTEGER,
  error TEXT,
  mixed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_accounts_batch ON accounts(batch_id);
CREATE INDEX IF NOT EXISTS idx_logs_created ON request_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_accounts_pick ON accounts(disabled, last_used);
`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE batches ADD COLUMN purchased_at INTEGER`)
	_, _ = s.db.Exec(`UPDATE batches SET purchased_at = created_at WHERE purchased_at IS NULL OR purchased_at = 0`)
	_, _ = s.db.Exec(`ALTER TABLE request_logs ADD COLUMN mixed INTEGER NOT NULL DEFAULT 0`)
	return nil
}

func (s *Store) loadSettings() {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return
	}
	defer rows.Close()
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			s.settings[k] = v
		}
	}
}

func (s *Store) GetSetting(key, fallback string) string {
	s.settingsMu.RLock()
	if v, ok := s.settings[key]; ok {
		s.settingsMu.RUnlock()
		if v == "" {
			return fallback
		}
		return v
	}
	s.settingsMu.RUnlock()
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil || v == "" {
		return fallback
	}
	s.settingsMu.Lock()
	s.settings[key] = v
	s.settingsMu.Unlock()
	return v
}

func (s *Store) BoolSetting(key string, fallback bool) bool {
	def := "false"
	if fallback {
		def = "true"
	}
	v := strings.ToLower(strings.TrimSpace(s.GetSetting(key, def)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err == nil {
		s.settingsMu.Lock()
		s.settings[key] = value
		s.settingsMu.Unlock()
	}
	return err
}

func (s *Store) CreateBatch(name, note string, validityDays int, purchasedAt time.Time) (*models.Batch, error) {
	now := time.Now()
	if validityDays <= 0 {
		validityDays = 30
	}
	if purchasedAt.IsZero() {
		purchasedAt = now
	}
	loc := time.FixedZone("CST", 8*3600)
	purchasedAt = purchasedAt.In(loc)
	purchasedAt = time.Date(purchasedAt.Year(), purchasedAt.Month(), purchasedAt.Day(), 0, 0, 0, 0, loc)
	expiresAt := purchasedAt.AddDate(0, 0, validityDays).Add(24*time.Hour - time.Second)
	b := &models.Batch{
		ID:          uuid.NewString(),
		Name:        strings.TrimSpace(name),
		Note:        strings.TrimSpace(note),
		CreatedAt:   now.Unix(),
		PurchasedAt: purchasedAt.Unix(),
		ExpiresAt:   expiresAt.Unix(),
	}
	if b.Name == "" {
		b.Name = purchasedAt.Format("2006-01-02") + " 批次"
	}
	_, err := s.db.Exec(`INSERT INTO batches(id, name, note, created_at, purchased_at, expires_at) VALUES(?,?,?,?,?,?)`,
		b.ID, b.Name, b.Note, b.CreatedAt, b.PurchasedAt, b.ExpiresAt)
	if err != nil {
		return nil, err
	}
	s.decorateBatch(b, now)
	return b, nil
}

func (s *Store) decorateBatch(b *models.Batch, now time.Time) {
	if b.PurchasedAt <= 0 {
		b.PurchasedAt = b.CreatedAt
	}
	b.RemainingDays = models.RemainingDays(b.ExpiresAt, now)
	b.Expired = now.Unix() >= b.ExpiresAt
	b.Progress = models.BatchProgress(b.PurchasedAt, b.ExpiresAt, now.Unix())
}

func (s *Store) ListBatches() ([]models.Batch, error) {
	rows, err := s.db.Query(`
SELECT b.id, b.name, b.note, b.created_at, COALESCE(b.purchased_at, b.created_at), b.expires_at,
       COUNT(a.id),
       COALESCE(SUM(CASE WHEN a.disabled = 0 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN a.disabled = 1 THEN 1 ELSE 0 END), 0)
FROM batches b
LEFT JOIN accounts a ON a.batch_id = b.id
GROUP BY b.id
ORDER BY b.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := make([]models.Batch, 0)
	for rows.Next() {
		var b models.Batch
		if err := rows.Scan(&b.ID, &b.Name, &b.Note, &b.CreatedAt, &b.PurchasedAt, &b.ExpiresAt, &b.AccountCount, &b.ActiveCount, &b.DisabledCount); err != nil {
			return nil, err
		}
		s.decorateBatch(&b, now)
		if b.Expired {
			b.ExpiredCount = b.AccountCount
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetBatch(id string) (*models.Batch, error) {
	var b models.Batch
	err := s.db.QueryRow(`SELECT id, name, note, created_at, COALESCE(purchased_at, created_at), expires_at FROM batches WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Note, &b.CreatedAt, &b.PurchasedAt, &b.ExpiresAt)
	if err != nil {
		return nil, err
	}
	s.decorateBatch(&b, time.Now())
	_ = s.db.QueryRow(`
SELECT COUNT(*), SUM(CASE WHEN disabled = 0 THEN 1 ELSE 0 END), SUM(CASE WHEN disabled = 1 THEN 1 ELSE 0 END)
FROM accounts WHERE batch_id = ?`, b.ID).Scan(&b.AccountCount, &b.ActiveCount, &b.DisabledCount)
	return &b, nil
}

func (s *Store) UpdateBatch(id, name, note string) error {
	_, err := s.db.Exec(`UPDATE batches SET name = ?, note = ? WHERE id = ?`, name, note, id)
	return err
}

func (s *Store) DeleteBatch(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM accounts WHERE batch_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM batches WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) InsertAccount(a *models.Account) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.CreatedAt == 0 {
		a.CreatedAt = time.Now().Unix()
	}
	quotaJSON, _ := json.Marshal(a.Quota)
	_, err := s.db.Exec(`
INSERT INTO accounts(
  id, batch_id, email, name, refresh_token, access_token, expires_in, expiry_timestamp,
  project_id, subscription_tier, quota_json, disabled, disabled_reason, last_used, last_error, rate_limited_until, created_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.BatchID, a.Email, a.Name, a.RefreshToken, a.AccessToken, a.ExpiresIn, a.ExpiryTimestamp,
		a.ProjectID, a.SubscriptionTier, string(quotaJSON), boolToInt(a.Disabled), a.DisabledReason, a.LastUsed, a.LastError, a.RateLimitedUntil, a.CreatedAt)
	return err
}

func (s *Store) HasRefreshToken(token string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE refresh_token = ?`, token).Scan(&n)
	return n > 0
}

func (s *Store) ListAccounts(batchID string) ([]models.Account, error) {
	q := `
SELECT a.id, a.batch_id, b.name, a.email, a.name, a.refresh_token, a.access_token, a.expires_in, a.expiry_timestamp,
       a.project_id, a.subscription_tier, a.quota_json, a.disabled, a.disabled_reason, a.last_used, a.last_error,
       a.rate_limited_until, a.created_at, b.expires_at
FROM accounts a JOIN batches b ON a.batch_id = b.id`
	args := []any{}
	if batchID != "" {
		q += ` WHERE a.batch_id = ?`
		args = append(args, batchID)
	}
	q += ` ORDER BY a.created_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	out := make([]models.Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		decorateAccount(&a, now)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAccount(id string) (*models.Account, error) {
	row := s.db.QueryRow(`
SELECT a.id, a.batch_id, b.name, a.email, a.name, a.refresh_token, a.access_token, a.expires_in, a.expiry_timestamp,
       a.project_id, a.subscription_tier, a.quota_json, a.disabled, a.disabled_reason, a.last_used, a.last_error,
       a.rate_limited_until, a.created_at, b.expires_at
FROM accounts a JOIN batches b ON a.batch_id = b.id WHERE a.id = ?`, id)
	a, err := scanAccount(row)
	if err != nil {
		return nil, err
	}
	decorateAccount(&a, time.Now())
	return &a, nil
}

func (s *Store) UpdateAccount(a *models.Account) error {
	quotaJSON, _ := json.Marshal(a.Quota)
	_, err := s.db.Exec(`
UPDATE accounts SET
  email=?, name=?, access_token=?, expires_in=?, expiry_timestamp=?, project_id=?, subscription_tier=?,
  quota_json=?, disabled=?, disabled_reason=?, last_used=?, last_error=?, rate_limited_until=?
WHERE id=?`,
		a.Email, a.Name, a.AccessToken, a.ExpiresIn, a.ExpiryTimestamp, a.ProjectID, a.SubscriptionTier,
		string(quotaJSON), boolToInt(a.Disabled), a.DisabledReason, a.LastUsed, a.LastError, a.RateLimitedUntil, a.ID)
	return err
}

func (s *Store) DeleteAccount(id string) error {
	_, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	return err
}

func (s *Store) SetDisabled(id string, disabled bool, reason string) error {
	_, err := s.db.Exec(`UPDATE accounts SET disabled=?, disabled_reason=? WHERE id=?`, boolToInt(disabled), reason, id)
	return err
}

func (s *Store) MarkUsed(id string) error {
	_, err := s.db.Exec(`UPDATE accounts SET last_used=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

func (s *Store) MarkRateLimited(id string, until int64, errMsg string) error {
	_, err := s.db.Exec(`UPDATE accounts SET rate_limited_until=?, last_error=? WHERE id=?`, until, errMsg, id)
	return err
}

func (s *Store) PickAccount(skipExpired bool, exclude map[string]struct{}) (*models.Account, error) {
	now := time.Now().Unix()
	q := `
SELECT a.id, a.batch_id, b.name, a.email, a.name, a.refresh_token, a.access_token, a.expires_in, a.expiry_timestamp,
       a.project_id, a.subscription_tier, a.quota_json, a.disabled, a.disabled_reason, a.last_used, a.last_error,
       a.rate_limited_until, a.created_at, b.expires_at
FROM accounts a JOIN batches b ON a.batch_id = b.id
WHERE a.disabled = 0 AND COALESCE(a.rate_limited_until, 0) <= ?`
	args := []any{now}
	if skipExpired {
		q += ` AND b.expires_at > ?`
		args = append(args, now)
	}
	if len(exclude) > 0 {
		q += ` AND a.id NOT IN (`
		i := 0
		for id := range exclude {
			if i > 0 {
				q += `,`
			}
			q += `?`
			args = append(args, id)
			i++
		}
		q += `)`
	}
	q += ` ORDER BY a.last_used ASC, a.created_at ASC LIMIT 1`
	a, err := scanAccount(s.db.QueryRow(q, args...))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no available accounts")
		}
		return nil, err
	}
	decorateAccount(&a, time.Now())
	_ = s.MarkUsed(a.ID)
	a.LastUsed = now
	return &a, nil
}

func (s *Store) AddLog(l models.RequestLog) error {
	if l.CreatedAt == 0 {
		l.CreatedAt = time.Now().Unix()
	}
	select {
	case s.logCh <- l:
	default:
	}
	return nil
}

func (s *Store) logWorker() {
	for l := range s.logCh {
		_, _ = s.db.Exec(`
INSERT INTO request_logs(created_at, protocol, model, mapped_model, account_id, account_email, status, stream, latency_ms, error, mixed)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			l.CreatedAt, l.Protocol, l.Model, l.MappedModel, l.AccountID, l.AccountEmail, l.Status, boolToInt(l.Stream), l.LatencyMS, l.Error, boolToInt(l.Mixed))
	}
}

func (s *Store) TrimLogs(keep int) {
	if keep <= 0 {
		keep = 5000
	}
	_, _ = s.db.Exec(`DELETE FROM request_logs WHERE id < COALESCE((SELECT MAX(id) FROM request_logs), 0) - ?`, keep)
}

func (s *Store) ClearLogs() error {
	_, err := s.db.Exec(`DELETE FROM request_logs`)
	return err
}

func (s *Store) LogStats() (total, success, errors int, err error) {
	var ok, bad sql.NullInt64
	err = s.db.QueryRow(`
SELECT COUNT(*),
       SUM(CASE WHEN status < 400 THEN 1 ELSE 0 END),
       SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END)
FROM request_logs`).Scan(&total, &ok, &bad)
	if err != nil {
		return 0, 0, 0, err
	}
	if ok.Valid {
		success = int(ok.Int64)
	}
	if bad.Valid {
		errors = int(bad.Int64)
	}
	return total, success, errors, nil
}

func (s *Store) ListLogs(limit int) ([]models.RequestLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
SELECT id, created_at, protocol, model, mapped_model, account_id, account_email, status, stream, latency_ms, error, COALESCE(mixed, 0)
FROM request_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.RequestLog, 0)
	for rows.Next() {
		var l models.RequestLog
		var stream, mixed int
		if err := rows.Scan(&l.ID, &l.CreatedAt, &l.Protocol, &l.Model, &l.MappedModel, &l.AccountID, &l.AccountEmail, &l.Status, &stream, &l.LatencyMS, &l.Error, &mixed); err != nil {
			return nil, err
		}
		l.Stream = stream == 1
		l.Mixed = mixed == 1
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Dashboard() (*models.Dashboard, error) {
	return s.DashboardStats("30d")
}

func normalizeRange(v string) string {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "24h", "7d", "90d":
		return strings.ToLower(v)
	default:
		return "30d"
	}
}

func rangeStart(key string, now time.Time) (from int64, hourly bool, buckets int) {
	loc := time.FixedZone("CST", 8*3600)
	now = now.In(loc)
	switch key {
	case "24h":
		start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, loc).Add(-23 * time.Hour)
		return start.Unix(), true, 24
	case "7d":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -6)
		return start.Unix(), false, 7
	case "90d":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -89)
		return start.Unix(), false, 90
	default:
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -29)
		return start.Unix(), false, 30
	}
}

func cstDate(ts int64) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	t := time.Unix(ts, 0).In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func rate(success, total int) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(success)*1000/float64(total)) / 10
}

func familyLabel(name string) string {
	switch name {
	case "gemini-pro":
		return "Gemini Pro"
	case "gemini-flash":
		return "Gemini Flash"
	case "claude":
		return "Claude"
	default:
		return name
	}
}

func modelFamily(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(n, "claude") || strings.Contains(n, "opus") || strings.Contains(n, "sonnet") || strings.Contains(n, "haiku") {
		return "claude"
	}
	if strings.Contains(n, "image") {
		return ""
	}
	if strings.Contains(n, "flash") {
		return "gemini-flash"
	}
	if strings.Contains(n, "gemini") && strings.Contains(n, "pro") {
		return "gemini-pro"
	}
	return ""
}

func (s *Store) DashboardStats(rangeKey string) (*models.Dashboard, error) {
	now := time.Now()
	d := &models.Dashboard{Range: normalizeRange(rangeKey), UpdatedAt: now.Unix()}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM batches`).Scan(&d.TotalBatches)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&d.TotalAccounts)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE disabled = 1`).Scan(&d.DisabledAccounts)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE rate_limited_until > ?`, now.Unix()).Scan(&d.RateLimited)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE created_at >= ?`, now.Add(-24*time.Hour).Unix()).Scan(&d.Requests24h)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE created_at >= ? AND status >= 400`, now.Add(-24*time.Hour).Unix()).Scan(&d.Errors24h)

	rows, err := s.db.Query(`
SELECT a.disabled, b.expires_at FROM accounts a JOIN batches b ON a.batch_id = b.id`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var disabled int
		var expiresAt int64
		if err := rows.Scan(&disabled, &expiresAt); err != nil {
			rows.Close()
			return d, err
		}
		expired := now.Unix() >= expiresAt
		if expired {
			d.ExpiredAccounts++
		} else if disabled == 0 {
			d.ActiveAccounts++
			if models.RemainingDays(expiresAt, now) <= 5 {
				d.ExpiringSoon++
			}
		}
	}
	rows.Close()

	from, hourly, nBuckets := rangeStart(d.Range, now)
	var avg sql.NullFloat64
	var stream, errs sql.NullInt64
	_ = s.db.QueryRow(`
SELECT COUNT(*), SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), AVG(latency_ms), SUM(stream)
FROM request_logs WHERE created_at >= ?`, from).Scan(&d.Requests, &errs, &avg, &stream)
	if errs.Valid {
		d.Errors = int(errs.Int64)
	}
	if avg.Valid {
		d.AvgLatencyMS = int64(avg.Float64 + 0.5)
	}
	if stream.Valid {
		d.StreamRequests = int(stream.Int64)
	}
	d.SuccessRate = rate(d.Requests-d.Errors, d.Requests)

	bucketSQL := `strftime('%Y-%m-%d', created_at, 'unixepoch', '+8 hours')`
	if hourly {
		bucketSQL = `strftime('%Y-%m-%d %H:00', created_at, 'unixepoch', '+8 hours')`
	}
	trendRows, err := s.db.Query(`
SELECT `+bucketSQL+` AS bucket, COUNT(*), SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), AVG(latency_ms)
FROM request_logs WHERE created_at >= ?
GROUP BY bucket ORDER BY bucket`, from)
	if err != nil {
		return d, err
	}
	found := map[string]models.TrendPoint{}
	for trendRows.Next() {
		var p models.TrendPoint
		var lat sql.NullFloat64
		if err := trendRows.Scan(&p.Bucket, &p.Requests, &p.Errors, &lat); err != nil {
			trendRows.Close()
			return d, err
		}
		if lat.Valid {
			p.AvgLatencyMS = int64(lat.Float64 + 0.5)
		}
		found[p.Bucket] = p
	}
	trendRows.Close()

	d.Trend = make([]models.TrendPoint, 0, nBuckets)
	loc := time.FixedZone("CST", 8*3600)
	cursor := time.Unix(from, 0).In(loc)
	if !hourly {
		cursor = cstDate(from)
	} else {
		cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), cursor.Hour(), 0, 0, 0, loc)
	}
	for i := 0; i < nBuckets; i++ {
		var key, label string
		if hourly {
			key = cursor.Format("2006-01-02 15:00")
			label = cursor.Format("15:00")
			cursor = cursor.Add(time.Hour)
		} else {
			key = cursor.Format("2006-01-02")
			if nBuckets > 40 {
				label = cursor.Format("1/2")
			} else {
				label = cursor.Format("1/2")
			}
			cursor = cursor.Add(24 * time.Hour)
		}
		p := found[key]
		p.Bucket = key
		p.Label = label
		d.Trend = append(d.Trend, p)
	}

	kindRows, err := s.db.Query(`
SELECT COALESCE(NULLIF(mapped_model, ''), NULLIF(model, ''), ''), COUNT(*), SUM(CASE WHEN status < 400 THEN 1 ELSE 0 END)
FROM request_logs WHERE created_at >= ?
GROUP BY 1`, from)
	if err != nil {
		return d, err
	}
	kindMap := map[string]models.ProtocolStat{}
	for kindRows.Next() {
		var name string
		var req int
		var ok sql.NullInt64
		if err := kindRows.Scan(&name, &req, &ok); err != nil {
			kindRows.Close()
			return d, err
		}
		fam := modelFamily(name)
		if fam == "" {
			continue
		}
		st := kindMap[fam]
		st.Name = fam
		st.Requests += req
		if ok.Valid {
			st.Success += int(ok.Int64)
		}
		kindMap[fam] = st
	}
	kindRows.Close()
	for _, name := range []string{"gemini-pro", "gemini-flash", "claude"} {
		st := kindMap[name]
		st.Name = name
		st.Label = familyLabel(name)
		st.Errors = st.Requests - st.Success
		st.SuccessRate = rate(st.Success, st.Requests)
		if d.Requests > 0 {
			st.Share = rate(st.Requests, d.Requests)
		}
		d.Protocols = append(d.Protocols, st)
	}

	modelRows, err := s.db.Query(`
SELECT COALESCE(NULLIF(mapped_model, ''), NULLIF(model, ''), 'unknown') AS name,
       COUNT(*), SUM(CASE WHEN status < 400 THEN 1 ELSE 0 END), AVG(latency_ms)
FROM request_logs WHERE created_at >= ?
GROUP BY name ORDER BY COUNT(*) DESC LIMIT 10`, from)
	if err != nil {
		return d, err
	}
	for modelRows.Next() {
		var m models.ModelStat
		var lat sql.NullFloat64
		if err := modelRows.Scan(&m.Name, &m.Requests, &m.Success, &lat); err != nil {
			modelRows.Close()
			return d, err
		}
		m.Errors = m.Requests - m.Success
		m.SuccessRate = rate(m.Success, m.Requests)
		if lat.Valid {
			m.AvgLatencyMS = int64(lat.Float64 + 0.5)
		}
		d.Models = append(d.Models, m)
	}
	modelRows.Close()
	if d.Models == nil {
		d.Models = []models.ModelStat{}
	}

	heatDays := 140
	heatFrom := cstDate(now.Unix()).AddDate(0, 0, -(heatDays - 1))
	heatRows, err := s.db.Query(`
SELECT strftime('%Y-%m-%d', created_at, 'unixepoch', '+8 hours') AS d, COUNT(*)
FROM request_logs WHERE created_at >= ?
GROUP BY d`, heatFrom.Unix())
	if err != nil {
		return d, err
	}
	heatMap := map[string]int{}
	for heatRows.Next() {
		var day string
		var count int
		if err := heatRows.Scan(&day, &count); err != nil {
			heatRows.Close()
			return d, err
		}
		heatMap[day] = count
	}
	heatRows.Close()
	days := make([]int, 0, heatDays)
	for i := 0; i < heatDays; i++ {
		day := heatFrom.AddDate(0, 0, i)
		days = append(days, heatMap[day.Format("2006-01-02")])
	}
	d.Heatmap = models.Heatmap{
		Start: heatFrom.Format("2006-01-02"),
		End:   heatFrom.AddDate(0, 0, heatDays-1).Format("2006-01-02"),
		Days:  days,
	}
	if d.Trend == nil {
		d.Trend = []models.TrendPoint{}
	}
	if d.Protocols == nil {
		d.Protocols = []models.ProtocolStat{}
	}
	return d, nil
}

func (s *Store) OfficialModels() ([]models.ModelQuota, map[string]string, error) {
	rows, err := s.db.Query(`SELECT quota_json FROM accounts WHERE quota_json IS NOT NULL AND quota_json != '' AND quota_json != 'null'`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	seen := map[string]models.ModelQuota{}
	forwarding := map[string]string{}
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		if !raw.Valid || strings.TrimSpace(raw.String) == "" {
			continue
		}
		var q models.QuotaData
		if json.Unmarshal([]byte(raw.String), &q) != nil {
			continue
		}
		for _, m := range q.Models {
			name := strings.TrimSpace(m.Name)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			prev, ok := seen[key]
			if !ok {
				seen[key] = m
				continue
			}
			if strings.TrimSpace(prev.DisplayName) == "" && strings.TrimSpace(m.DisplayName) != "" {
				seen[key] = m
			}
		}
		for oldID, newID := range q.ForwardingRules {
			oldID, newID = strings.TrimSpace(oldID), strings.TrimSpace(newID)
			if oldID == "" || newID == "" {
				continue
			}
			if _, exists := forwarding[oldID]; !exists {
				forwarding[oldID] = newID
			}
		}
	}
	out := make([]models.ModelQuota, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	return out, forwarding, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (models.Account, error) {
	var a models.Account
	var quotaJSON sql.NullString
	var name, access, project, tier, reason, lastErr sql.NullString
	var expiresIn, expiry, lastUsed, rateUntil sql.NullInt64
	var disabled int
	err := row.Scan(
		&a.ID, &a.BatchID, &a.BatchName, &a.Email, &name, &a.RefreshToken, &access, &expiresIn, &expiry,
		&project, &tier, &quotaJSON, &disabled, &reason, &lastUsed, &lastErr, &rateUntil, &a.CreatedAt, &a.ExpiresAt,
	)
	if err != nil {
		return a, err
	}
	a.Name = name.String
	a.AccessToken = access.String
	a.ExpiresIn = expiresIn.Int64
	a.ExpiryTimestamp = expiry.Int64
	a.ProjectID = project.String
	a.SubscriptionTier = tier.String
	a.Disabled = disabled == 1
	a.DisabledReason = reason.String
	a.LastUsed = lastUsed.Int64
	a.LastError = lastErr.String
	a.RateLimitedUntil = rateUntil.Int64
	if quotaJSON.Valid && quotaJSON.String != "" && quotaJSON.String != "null" {
		var q models.QuotaData
		if json.Unmarshal([]byte(quotaJSON.String), &q) == nil {
			a.Quota = &q
		}
	}
	return a, nil
}

func decorateAccount(a *models.Account, now time.Time) {
	a.RemainingDays = models.RemainingDays(a.ExpiresAt, now)
	a.Expired = now.Unix() >= a.ExpiresAt
	switch {
	case a.Disabled:
		a.Status = "disabled"
	case a.Expired:
		a.Status = "expired"
	case a.RateLimitedUntil > now.Unix():
		a.Status = "rate_limited"
	default:
		a.Status = "active"
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
