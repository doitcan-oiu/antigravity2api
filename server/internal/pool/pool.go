package pool

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wo/antigravity2api/internal/cloudcode"
	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/oauth"
	"github.com/wo/antigravity2api/internal/store"
)

type Pool struct {
	cfg      config.Config
	store    *store.Store
	oauth    *oauth.Client
	cc       *cloudcode.Client
	locks    sync.Map
	inflight sync.Map
}

func New(cfg config.Config, st *store.Store, oa *oauth.Client, cc *cloudcode.Client) *Pool {
	return &Pool{cfg: cfg, store: st, oauth: oa, cc: cc}
}

func (p *Pool) accountLock(id string) *sync.Mutex {
	v, _ := p.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (p *Pool) SkipExpired() bool {
	return p.store.BoolSetting("skip_expired_accounts", p.cfg.SkipExpiredDefault)
}

func (p *Pool) Import(name, note, raw string, purchasedAt time.Time) (*models.ImportResult, error) {
	tokens := oauth.ExtractTokens(raw)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("未找到有效 refresh_token（需以 1// 开头）")
	}
	days := p.cfg.BatchValidityDays
	if n := p.store.GetSetting("batch_validity_days", ""); n != "" {
		fmt.Sscanf(n, "%d", &days)
		if days <= 0 {
			days = p.cfg.BatchValidityDays
		}
	}
	batch, err := p.store.CreateBatch(name, note, days, purchasedAt)
	if err != nil {
		return nil, err
	}
	return p.importTokens(batch, tokens)
}

func (p *Pool) ImportInto(batchID, raw string) (*models.ImportResult, error) {
	batch, err := p.store.GetBatch(batchID)
	if err != nil {
		return nil, fmt.Errorf("批次不存在")
	}
	tokens := oauth.ExtractTokens(raw)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("未找到有效 refresh_token（需以 1// 开头）")
	}
	return p.importTokens(batch, tokens)
}

func (p *Pool) importTokens(batch *models.Batch, tokens []string) (*models.ImportResult, error) {
	res := &models.ImportResult{Batch: batch}
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		importedIDs []string
	)
	sem := make(chan struct{}, 4)
	for _, token := range tokens {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			item := models.ImportItem{Token: maskToken(token)}
			if p.store.HasRefreshToken(token) {
				item.Status = "skipped"
				item.Error = "账号已存在"
				mu.Lock()
				res.Skipped++
				res.Items = append(res.Items, item)
				mu.Unlock()
				return
			}
			acc, err := p.hydrateAccount(batch.ID, token)
			if err != nil {
				item.Status = "failed"
				item.Error = err.Error()
				mu.Lock()
				res.Failed++
				res.Items = append(res.Items, item)
				mu.Unlock()
				return
			}
			if err := p.store.InsertAccount(acc); err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					item.Status = "skipped"
					item.Error = "账号已存在"
					mu.Lock()
					res.Skipped++
					res.Items = append(res.Items, item)
					mu.Unlock()
					return
				}
				item.Status = "failed"
				item.Error = err.Error()
				mu.Lock()
				res.Failed++
				res.Items = append(res.Items, item)
				mu.Unlock()
				return
			}
			item.Status = "imported"
			item.Email = acc.Email
			mu.Lock()
			res.Imported++
			res.Items = append(res.Items, item)
			importedIDs = append(importedIDs, acc.ID)
			mu.Unlock()
		}(token)
	}
	wg.Wait()
	if b, err := p.store.GetBatch(batch.ID); err == nil {
		res.Batch = b
	}
	if len(importedIDs) > 0 {
		go p.enrichAccounts(importedIDs)
	}
	return res, nil
}

func (p *Pool) hydrateAccount(batchID, refreshToken string) (*models.Account, error) {
	tok, err := p.oauth.Refresh(refreshToken)
	if err != nil {
		return nil, err
	}
	info, err := p.oauth.UserInfo(tok.AccessToken)
	if err != nil {
		return nil, err
	}
	return &models.Account{
		BatchID:         batchID,
		Email:           info.Email,
		Name:            info.DisplayName(),
		RefreshToken:    refreshToken,
		AccessToken:     tok.AccessToken,
		ExpiresIn:       tok.ExpiresIn,
		ExpiryTimestamp: time.Now().Unix() + tok.ExpiresIn,
		CreatedAt:       time.Now().Unix(),
	}, nil
}

func (p *Pool) enrichAccounts(ids []string) {
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, _ = p.RefreshAccount(id)
		}(id)
	}
	wg.Wait()
}

func (p *Pool) RefreshAccount(id string) (*models.Account, error) {
	acc, err := p.store.GetAccount(id)
	if err != nil {
		return nil, err
	}
	if err := p.ensureFresh(acc); err != nil {
		acc.LastError = err.Error()
		if strings.Contains(err.Error(), "invalid_grant") {
			acc.Disabled = true
			acc.DisabledReason = "refresh_token 已失效"
		}
		_ = p.store.UpdateAccount(acc)
		return acc, err
	}
	projectID, tier, _ := p.cc.LoadCodeAssist(acc.AccessToken)
	if projectID != "" {
		acc.ProjectID = projectID
	}
	if tier != "" {
		acc.SubscriptionTier = tier
	}
	var prevGroups []models.QuotaGroup
	if acc.Quota != nil {
		prevGroups = acc.Quota.QuotaGroups
	}
	if q, err := p.cc.FetchQuota(acc.AccessToken, acc.ProjectID); err == nil {
		if len(q.QuotaGroups) == 0 && len(prevGroups) > 0 && !q.IsForbidden {
			q.QuotaGroups = prevGroups
		}
		acc.Quota = q
		if q.SubscriptionTier != "" {
			acc.SubscriptionTier = q.SubscriptionTier
		}
	}
	acc.LastError = ""
	_ = p.store.UpdateAccount(acc)
	return acc, nil
}

func (p *Pool) RefreshAll() (int, error) {
	return p.refreshAccounts(false)
}

func (p *Pool) AccountCheckInterval() time.Duration {
	mins := p.store.IntSetting("account_check_minutes", 30)
	if mins <= 0 {
		return 0
	}
	if mins < 5 {
		mins = 5
	}
	if mins > 1440 {
		mins = 1440
	}
	return time.Duration(mins) * time.Minute
}

func (p *Pool) StartHealthLoop() {
	go p.healthLoop()
}

func (p *Pool) healthLoop() {
	var busy sync.Mutex
	first := true
	for {
		d := p.AccountCheckInterval()
		if d <= 0 {
			first = true
			time.Sleep(15 * time.Second)
			continue
		}
		wait := d
		if first {
			wait = 2 * time.Minute
			if wait > d {
				wait = d
			}
			first = false
		}
		time.Sleep(wait)
		if !busy.TryLock() {
			continue
		}
		go func() {
			defer busy.Unlock()
			n, err := p.refreshAccounts(true)
			if err != nil {
				log.Printf("account health check failed: %v", err)
				return
			}
			log.Printf("account health check refreshed %d accounts", n)
		}()
	}
}

func (p *Pool) refreshAccounts(healthOnly bool) (int, error) {
	accs, err := p.store.ListAccounts("")
	if err != nil {
		return 0, err
	}
	n := 0
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range accs {
		acc := accs[i]
		if healthOnly {
			if acc.Expired {
				continue
			}
			if acc.Disabled && strings.Contains(acc.DisabledReason, "refresh_token") {
				continue
			}
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, err := p.RefreshAccount(id); err == nil {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}(acc.ID)
	}
	wg.Wait()
	return n, nil
}

func (p *Pool) Next(exclude map[string]struct{}) (*models.Account, error) {
	if exclude == nil {
		exclude = map[string]struct{}{}
	}
	skip := p.SkipExpired()
	for i := 0; i < 32; i++ {
		acc, err := p.store.PickAccount(skip, exclude)
		if err != nil {
			return nil, err
		}
		if err := p.ensureFresh(acc); err != nil {
			acc.LastError = err.Error()
			if strings.Contains(err.Error(), "invalid_grant") {
				acc.Disabled = true
				acc.DisabledReason = "refresh_token 已失效"
			}
			_ = p.store.UpdateAccount(acc)
			exclude[acc.ID] = struct{}{}
			continue
		}
		if acc.ProjectID == "" {
			go p.fillProject(acc.ID)
		}
		return acc, nil
	}
	return nil, fmt.Errorf("no available accounts")
}

func (p *Pool) fillProject(id string) {
	if _, loaded := p.inflight.LoadOrStore(id, struct{}{}); loaded {
		return
	}
	defer p.inflight.Delete(id)
	acc, err := p.store.GetAccount(id)
	if err != nil || acc.ProjectID != "" {
		return
	}
	if err := p.ensureFresh(acc); err != nil {
		return
	}
	pid, tier, err := p.cc.LoadCodeAssist(acc.AccessToken)
	if err != nil {
		return
	}
	if pid != "" {
		acc.ProjectID = pid
	}
	if tier != "" {
		acc.SubscriptionTier = tier
	}
	_ = p.store.UpdateAccount(acc)
}

func (p *Pool) ensureFresh(acc *models.Account) error {
	mu := p.accountLock(acc.ID)
	mu.Lock()
	defer mu.Unlock()
	if latest, err := p.store.GetAccount(acc.ID); err == nil {
		*acc = *latest
	}
	if acc.AccessToken != "" && !oauth.NeedsRefresh(acc.ExpiryTimestamp) {
		return nil
	}
	tok, err := p.oauth.Refresh(acc.RefreshToken)
	if err != nil {
		return err
	}
	acc.AccessToken = tok.AccessToken
	acc.ExpiresIn = tok.ExpiresIn
	acc.ExpiryTimestamp = time.Now().Unix() + tok.ExpiresIn
	return p.store.UpdateAccount(acc)
}

func maskToken(t string) string {
	if len(t) <= 10 {
		return t
	}
	return t[:6] + "…" + t[len(t)-4:]
}
