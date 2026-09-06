package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wo/antigravity2api/internal/models"
)

// AccountsVersion changes before a successful account mutation returns. Schedulers
// can reuse immutable snapshots until this version changes, without polling SQL.
func (s *Store) AccountsVersion() uint64 { return s.accountVersion.Load() }

// PickAccount is the compatibility round-robin picker. The proxy uses Pool.Acquire
// for model-aware scheduling and concurrent request reservations.
func (s *Store) PickAccount(skipExpired bool, exclude map[string]struct{}) (*models.Account, error) {
	s.pickMu.Lock()
	defer s.pickMu.Unlock()
	version := s.AccountsVersion()
	if s.pickVersion != version {
		accounts, err := s.ListAccounts("")
		if err != nil {
			return nil, err
		}
		s.pickAccounts, s.pickVersion = accounts, version
	}
	now := time.Now()
	for i := 0; i < len(s.pickAccounts); i++ {
		index := int(s.pickCursor % uint64(len(s.pickAccounts)))
		s.pickCursor++
		a := &s.pickAccounts[index]
		if _, excluded := exclude[a.ID]; excluded {
			continue
		}
		if a.Disabled || (skipExpired && a.ExpiresAt <= now.Unix()) || a.RateLimitedUntil > now.Unix() || (a.Quota != nil && a.Quota.IsForbidden) {
			continue
		}
		result := CloneAccount(a)
		result.LastUsed = now.Unix()
		if err := s.MarkUsed(a.ID); err != nil {
			return nil, err
		}
		return result, nil
	}
	return nil, fmt.Errorf("no available accounts")
}

// CloneAccount separates mutable response data from cached scheduling snapshots.
func CloneAccount(a *models.Account) *models.Account {
	if a == nil {
		return nil
	}
	out := *a
	if a.Quota != nil {
		q := *a.Quota
		q.Models = append([]models.ModelQuota(nil), q.Models...)
		q.ForwardingRules = make(map[string]string, len(a.Quota.ForwardingRules))
		for k, v := range a.Quota.ForwardingRules {
			q.ForwardingRules[k] = v
		}
		q.QuotaGroups = append([]models.QuotaGroup(nil), q.QuotaGroups...)
		for i := range q.QuotaGroups {
			q.QuotaGroups[i].Buckets = append([]models.QuotaBucket(nil), q.QuotaGroups[i].Buckets...)
		}
		out.Quota = &q
	}
	return &out
}

func (s *Store) accountUpdate(query string, args ...any) error {
	return s.accountUpdateContext(context.Background(), query, args...)
}
func (s *Store) accountUpdateContext(ctx context.Context, query string, args ...any) error {
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	s.accountVersion.Add(1)
	return nil
}

// UpdateToken changes credential fields only: concurrent admin disable, quota,
// project and last-used updates cannot be overwritten by a stale account copy.
func (s *Store) UpdateToken(id, accessToken, refreshToken string, expiresIn, expiry int64) error {
	return s.UpdateTokenContext(context.Background(), id, accessToken, refreshToken, expiresIn, expiry)
}
func (s *Store) UpdateTokenContext(ctx context.Context, id, accessToken, refreshToken string, expiresIn, expiry int64) error {
	return s.accountUpdateContext(ctx, `UPDATE accounts SET access_token=?, refresh_token=CASE WHEN ?='' THEN refresh_token ELSE ? END, expires_in=?, expiry_timestamp=? WHERE id=?`, accessToken, refreshToken, refreshToken, expiresIn, expiry, id)
}

func (s *Store) InvalidateToken(id string) error {
	return s.InvalidateTokenContext(context.Background(), id)
}
func (s *Store) InvalidateTokenContext(ctx context.Context, id string) error {
	return s.accountUpdateContext(ctx, `UPDATE accounts SET expiry_timestamp=0 WHERE id=?`, id)
}

func (s *Store) UpdateAccountMetadata(id, project, tier string, quota *models.QuotaData) error {
	return s.UpdateAccountMetadataContext(context.Background(), id, project, tier, quota)
}
func (s *Store) UpdateAccountMetadataContext(ctx context.Context, id, project, tier string, quota *models.QuotaData) error {
	var raw any
	if quota != nil {
		encoded, err := json.Marshal(quota)
		if err != nil {
			return err
		}
		raw = string(encoded)
	}
	return s.accountUpdateContext(ctx, `UPDATE accounts SET project_id=CASE WHEN ?='' THEN project_id ELSE ? END, subscription_tier=CASE WHEN ?='' THEN subscription_tier ELSE ? END, quota_json=COALESCE(?,quota_json) WHERE id=?`, project, project, tier, tier, raw, id)
}

func (s *Store) SetAccountError(id, message string) error {
	return s.SetAccountErrorContext(context.Background(), id, message)
}
func (s *Store) SetAccountErrorContext(ctx context.Context, id, message string) error {
	return s.accountUpdateContext(ctx, `UPDATE accounts SET last_error=? WHERE id=?`, message, id)
}

func (s *Store) SetForbidden(id, reason string) error {
	return s.accountUpdate(`UPDATE accounts SET quota_json=json_set(COALESCE(NULLIF(NULLIF(quota_json,''),'null'),'{}'),'$.is_forbidden',json('true'),'$.forbidden_reason',?), last_error=? WHERE id=?`, reason, reason, id)
}

var (
	ErrClosed       = errors.New("store closed")
	ErrLogQueueFull = errors.New("request log queue full")
)

func (s *Store) SetDisabledContext(ctx context.Context, id string, disabled bool, reason string) error {
	return s.accountUpdateContext(ctx, `UPDATE accounts SET disabled=?,disabled_reason=? WHERE id=?`, boolToInt(disabled), reason, id)
}
