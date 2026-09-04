package models

import "time"

type Batch struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Note          string  `json:"note,omitempty"`
	CreatedAt     int64   `json:"created_at"`
	PurchasedAt   int64   `json:"purchased_at"`
	ExpiresAt     int64   `json:"expires_at"`
	AccountCount  int     `json:"account_count"`
	ActiveCount   int     `json:"active_count"`
	ExpiredCount  int     `json:"expired_count"`
	DisabledCount int     `json:"disabled_count"`
	RemainingDays int     `json:"remaining_days"`
	Expired       bool    `json:"expired"`
	Progress      float64 `json:"progress"`
}

type Account struct {
	ID               string     `json:"id"`
	BatchID          string     `json:"batch_id"`
	BatchName        string     `json:"batch_name,omitempty"`
	Email            string     `json:"email"`
	Name             string     `json:"name,omitempty"`
	RefreshToken     string     `json:"refresh_token,omitempty"`
	AccessToken      string     `json:"-"`
	ExpiresIn        int64      `json:"expires_in"`
	ExpiryTimestamp  int64      `json:"expiry_timestamp"`
	ProjectID        string     `json:"project_id,omitempty"`
	SubscriptionTier string     `json:"subscription_tier,omitempty"`
	Quota            *QuotaData `json:"quota,omitempty"`
	Disabled         bool       `json:"disabled"`
	DisabledReason   string     `json:"disabled_reason,omitempty"`
	LastUsed         int64      `json:"last_used"`
	LastError        string     `json:"last_error,omitempty"`
	RateLimitedUntil int64      `json:"rate_limited_until,omitempty"`
	CreatedAt        int64      `json:"created_at"`
	ExpiresAt        int64      `json:"expires_at"`
	RemainingDays    int        `json:"remaining_days"`
	Expired          bool       `json:"expired"`
	Status           string     `json:"status"`
}

type QuotaData struct {
	Models           []ModelQuota      `json:"models"`
	LastUpdated      int64             `json:"last_updated"`
	IsForbidden      bool              `json:"is_forbidden"`
	ForbiddenReason  string            `json:"forbidden_reason,omitempty"`
	SubscriptionTier string            `json:"subscription_tier,omitempty"`
	QuotaGroups      []QuotaGroup      `json:"quota_groups,omitempty"`
	ForwardingRules  map[string]string `json:"model_forwarding_rules,omitempty"`
}

type ModelQuota struct {
	Name             string `json:"name"`
	Percentage       int    `json:"percentage"`
	ResetTime        string `json:"reset_time,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
	SupportsImages   *bool  `json:"supports_images,omitempty"`
	SupportsThinking *bool  `json:"supports_thinking,omitempty"`
	ThinkingBudget   *int   `json:"thinking_budget,omitempty"`
	Recommended      *bool  `json:"recommended,omitempty"`
}

type QuotaGroup struct {
	DisplayName string        `json:"display_name"`
	Description string        `json:"description,omitempty"`
	Buckets     []QuotaBucket `json:"buckets"`
}

type QuotaBucket struct {
	BucketID          string  `json:"bucket_id"`
	Window            string  `json:"window"`
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time,omitempty"`
	DisplayName       string  `json:"display_name,omitempty"`
	Description       string  `json:"description,omitempty"`
}

type RequestLog struct {
	ID           int64  `json:"id"`
	CreatedAt    int64  `json:"created_at"`
	Protocol     string `json:"protocol"`
	Model        string `json:"model"`
	MappedModel  string `json:"mapped_model"`
	AccountID    string `json:"account_id"`
	AccountEmail string `json:"account_email"`
	Status       int    `json:"status"`
	Stream       bool   `json:"stream"`
	LatencyMS    int64  `json:"latency_ms"`
	Error        string `json:"error,omitempty"`
	Mixed        bool   `json:"mixed"`
}

type Settings struct {
	APIKey              string `json:"api_key"`
	AdminToken          string `json:"admin_token"`
	SkipExpiredAccounts bool   `json:"skip_expired_accounts"`
	EnableLogging       bool   `json:"enable_logging"`
	ListenAddr          string `json:"listen_addr"`
	BatchValidityDays   int    `json:"batch_validity_days"`
	ProxyEnabled        bool   `json:"proxy_enabled"`
	ProxyURL            string `json:"proxy_url"`
}

type MixRule struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Percent int    `json:"percent"`
	Enabled bool   `json:"enabled"`
}

type Dashboard struct {
	Range            string         `json:"range"`
	TotalBatches     int            `json:"total_batches"`
	TotalAccounts    int            `json:"total_accounts"`
	ActiveAccounts   int            `json:"active_accounts"`
	ExpiredAccounts  int            `json:"expired_accounts"`
	DisabledAccounts int            `json:"disabled_accounts"`
	RateLimited      int            `json:"rate_limited"`
	ExpiringSoon     int            `json:"expiring_soon"`
	Requests24h      int            `json:"requests_24h"`
	Errors24h        int            `json:"errors_24h"`
	Requests         int            `json:"requests"`
	Errors           int            `json:"errors"`
	SuccessRate      float64        `json:"success_rate"`
	AvgLatencyMS     int64          `json:"avg_latency_ms"`
	StreamRequests   int            `json:"stream_requests"`
	CatalogModels    int            `json:"catalog_models"`
	HasAPIKey        bool           `json:"has_api_key"`
	Trend            []TrendPoint   `json:"trend"`
	Protocols        []ProtocolStat `json:"protocols"`
	Models           []ModelStat    `json:"models"`
	Heatmap          Heatmap        `json:"heatmap"`
	UpdatedAt        int64          `json:"updated_at"`
}

type TrendPoint struct {
	Bucket       string `json:"bucket"`
	Label        string `json:"label"`
	Requests     int    `json:"requests"`
	Errors       int    `json:"errors"`
	AvgLatencyMS int64  `json:"avg_latency_ms"`
}

type ProtocolStat struct {
	Name        string  `json:"name"`
	Label       string  `json:"label"`
	Requests    int     `json:"requests"`
	Success     int     `json:"success"`
	Errors      int     `json:"errors"`
	SuccessRate float64 `json:"success_rate"`
	Share       float64 `json:"share"`
}

type ModelStat struct {
	Name         string  `json:"name"`
	Requests     int     `json:"requests"`
	Success      int     `json:"success"`
	Errors       int     `json:"errors"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
	SuccessRate  float64 `json:"success_rate"`
}

type Heatmap struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Days  []int  `json:"days"`
}

type ImportResult struct {
	Batch    *Batch       `json:"batch"`
	Imported int          `json:"imported"`
	Skipped  int          `json:"skipped"`
	Failed   int          `json:"failed"`
	Items    []ImportItem `json:"items"`
}

type ImportItem struct {
	Email  string `json:"email,omitempty"`
	Token  string `json:"token"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func RemainingDays(expiresAt int64, now time.Time) int {
	if expiresAt <= 0 {
		return 0
	}
	diff := time.Unix(expiresAt, 0).Sub(now)
	days := int(diff.Hours() / 24)
	if diff > 0 && days == 0 {
		return 1
	}
	if days < 0 {
		return 0
	}
	return days
}

func BatchProgress(createdAt, expiresAt, now int64) float64 {
	total := float64(expiresAt - createdAt)
	if total <= 0 {
		return 1
	}
	used := float64(now - createdAt)
	if used < 0 {
		used = 0
	}
	p := used / total
	if p > 1 {
		return 1
	}
	return p
}
