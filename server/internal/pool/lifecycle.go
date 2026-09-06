package pool

import (
	"context"
	"errors"
	"fmt"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/oauth"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Cancellable per-account single-flight locks, removed when the last waiter leaves.
func (p *Pool) acquireGate(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.gateMu.Lock()
	gate := p.gates[id]
	if gate == nil {
		gate = &accountGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		p.gates[id] = gate
	}
	gate.refs++
	p.gateMu.Unlock()
	drop := func() {
		p.gateMu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(p.gates, id)
		}
		p.gateMu.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-gate.token:
		var once sync.Once
		return func() { once.Do(func() { gate.token <- struct{}{}; drop() }) }, nil
	}
}

func (p *Pool) ensureFreshContext(ctx context.Context, acc *models.Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if acc.AccessToken != "" && !oauth.NeedsRefresh(acc.ExpiryTimestamp) {
		return nil
	}
	unlock, err := p.acquireGate(ctx, acc.ID)
	if err != nil {
		return err
	}
	defer unlock()
	latest, err := p.store.GetAccountContext(ctx, acc.ID)
	if err != nil {
		return err
	}
	*acc = *latest
	if acc.Disabled {
		return fmt.Errorf("account disabled")
	}
	if acc.AccessToken != "" && !oauth.NeedsRefresh(acc.ExpiryTimestamp) {
		return nil
	}
	if p.refresh == nil {
		return fmt.Errorf("OAuth client unavailable")
	}
	p.gateMu.Lock()
	failure, failed := p.refreshFailures[acc.ID]
	p.gateMu.Unlock()
	if failed && failure.until.After(time.Now()) {
		return errors.New(failure.message)
	}
	tok, err := p.refreshToken(ctx, acc.RefreshToken)
	if err != nil {
		if ctx.Err() == nil {
			message := err.Error()
			if len(message) > 2048 {
				message = message[:2048]
			}
			p.gateMu.Lock()
			if len(p.refreshFailures) >= maxSessions {
				for id, f := range p.refreshFailures {
					if !f.until.After(time.Now()) {
						delete(p.refreshFailures, id)
					}
				}
				if len(p.refreshFailures) >= maxSessions {
					clear(p.refreshFailures)
				}
			}
			p.refreshFailures[acc.ID] = refreshFailure{message: message, until: time.Now().Add(5 * time.Second)}
			p.gateMu.Unlock()
		}
		return err
	}
	p.gateMu.Lock()
	delete(p.refreshFailures, acc.ID)
	p.gateMu.Unlock()
	if err := p.store.UpdateTokenContext(ctx, acc.ID, tok.AccessToken, tok.RefreshToken, tok.ExpiresIn, time.Now().Unix()+tok.ExpiresIn); err != nil {
		return err
	}
	latest, err = p.store.GetAccountContext(ctx, acc.ID)
	if err != nil {
		return err
	}
	*acc = *latest
	return nil
}
func (p *Pool) ensureFresh(acc *models.Account) error { return p.ensureFreshContext(p.ctx, acc) }

func (p *Pool) ensureProject(ctx context.Context, acc *models.Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := p.acquireGate(ctx, acc.ID)
	if err != nil {
		return err
	}
	defer unlock()
	latest, err := p.store.GetAccountContext(ctx, acc.ID)
	if err != nil {
		return err
	}
	*acc = *latest
	if acc.Disabled {
		return fmt.Errorf("account disabled")
	}
	if acc.ProjectID != "" {
		return nil
	}
	if p.loadAssist == nil {
		return fmt.Errorf("Cloud Code client unavailable")
	}
	project, tier, err := p.loadCodeAssist(ctx, acc.AccessToken)
	if err != nil {
		return err
	}
	if strings.TrimSpace(project) == "" {
		return fmt.Errorf("loadCodeAssist returned no project")
	}
	if err := p.store.UpdateAccountMetadataContext(ctx, acc.ID, project, tier, nil); err != nil {
		return err
	}
	latest, err = p.store.GetAccountContext(ctx, acc.ID)
	if err != nil {
		return err
	}
	*acc = *latest
	return nil
}
func (p *Pool) InvalidateToken(id string) error { return p.InvalidateTokenContext(p.ctx, id) }
func (p *Pool) InvalidateTokenContext(ctx context.Context, id string) error {
	ctx, done := p.operationContext(ctx)
	defer done()
	unlock, err := p.acquireGate(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	p.gateMu.Lock()
	delete(p.refreshFailures, id)
	p.gateMu.Unlock()
	return p.store.InvalidateTokenContext(ctx, id)
}
func (p *Pool) RefreshAccount(id string) (*models.Account, error) {
	return p.RefreshAccountContext(p.ctx, id)
}
func (p *Pool) RefreshAccountContext(ctx context.Context, id string) (*models.Account, error) {
	ctx, done := p.operationContext(ctx)
	defer done()
	acc, err := p.store.GetAccountContext(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.ensureFreshContext(ctx, acc); err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			_ = p.store.SetDisabledContext(ctx, id, true, "refresh_token 已失效")
		}
		if ctx.Err() == nil {
			_ = p.store.SetAccountErrorContext(ctx, id, err.Error())
		}
		return acc, err
	}
	if p.loadAssist == nil || p.fetchQuota == nil {
		return acc, fmt.Errorf("Cloud Code client unavailable")
	}
	project, tier, loadErr := p.loadCodeAssist(ctx, acc.AccessToken)
	if project == "" {
		project = acc.ProjectID
	}
	quota, quotaErr := p.fetchAccountQuota(ctx, acc.AccessToken, project)
	if ctx.Err() != nil {
		return acc, ctx.Err()
	}
	if quota != nil {
		if len(quota.QuotaGroups) == 0 && acc.Quota != nil && !quota.IsForbidden {
			quota.QuotaGroups = acc.Quota.QuotaGroups
		}
		if quota.SubscriptionTier != "" {
			tier = quota.SubscriptionTier
		}
	}
	if err := p.store.UpdateAccountMetadataContext(ctx, id, project, tier, quota); err != nil {
		return acc, err
	}
	combined := errors.Join(loadErr, quotaErr)
	message := ""
	if combined != nil {
		message = combined.Error()
	}
	_ = p.store.SetAccountErrorContext(ctx, id, message)
	acc, err = p.store.GetAccountContext(ctx, id)
	return acc, errors.Join(combined, err)
}
func (p *Pool) startBackground(run func()) bool {
	p.backgroundMu.Lock()
	defer p.backgroundMu.Unlock()
	if p.closed {
		return false
	}
	p.background.Add(1)
	go func() { defer p.background.Done(); run() }()
	return true
}
func (p *Pool) Close() {
	p.backgroundMu.Lock()
	p.closed = true
	p.cancel()
	p.backgroundMu.Unlock()
	p.background.Wait()
}
func (p *Pool) enqueueRefresh(id string) { p.enqueueRefreshContext(p.ctx, id) }
func (p *Pool) enqueueRefreshContext(ctx context.Context, id string) {
	p.mu.Lock()
	if p.queued[id] {
		p.mu.Unlock()
		return
	}
	p.queued[id] = true
	p.mu.Unlock()
	select {
	case p.refreshQueue <- id:
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.queued, id)
		p.mu.Unlock()
	}
}
func (p *Pool) refreshWorker() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case id := <-p.refreshQueue:
			_, _ = p.RefreshAccountContext(p.ctx, id)
			p.mu.Lock()
			delete(p.queued, id)
			p.mu.Unlock()
		}
	}
}
func (p *Pool) RefreshAll() (int, error) { return p.refreshAccounts(false) }
func (p *Pool) refreshAccounts(healthOnly bool) (int, error) {
	return p.RefreshAllContext(p.ctx, healthOnly)
}

// Two workers consume account IDs; goroutine count is independent of pool size.
func (p *Pool) RefreshAllContext(ctx context.Context, healthOnly bool) (int, error) {
	ctx, done := p.operationContext(ctx)
	defer done()
	accounts, err := p.store.ListAccountsContext(ctx, "")
	if err != nil {
		return 0, err
	}
	jobs := make(chan string)
	var workers sync.WaitGroup
	var completed atomic.Int64
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				if _, err := p.RefreshAccountContext(ctx, id); err == nil {
					completed.Add(1)
				}
			}
		}()
	}
feed:
	for _, a := range accounts {
		if healthOnly && (a.Expired || (a.Disabled && strings.Contains(a.DisabledReason, "refresh_token"))) {
			continue
		}
		select {
		case jobs <- a.ID:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	workers.Wait()
	return int(completed.Load()), ctx.Err()
}
func (p *Pool) AccountCheckInterval() time.Duration {
	minutes := p.store.IntSetting("account_check_minutes", 30)
	if minutes <= 0 {
		return 0
	}
	if minutes < 5 {
		minutes = 5
	}
	if minutes > 1440 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}
func (p *Pool) StartHealthLoop() { p.healthOnce.Do(func() { p.startBackground(p.healthLoop) }) }
func (p *Pool) healthLoop() {
	first := true
	for {
		interval := p.AccountCheckInterval()
		wait := interval
		if interval <= 0 {
			first = true
			wait = 15 * time.Second
		} else if first {
			wait = 2 * time.Minute
			if interval < wait {
				wait = interval
			}
			first = false
		}
		timer := time.NewTimer(wait)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if interval <= 0 {
			continue
		}
		n, err := p.refreshAccounts(true)
		if p.ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("account health check failed: %v", err)
		} else {
			log.Printf("account health check refreshed %d accounts", n)
		}
	}
}

// RefreshTokenContext refreshes credentials without fetching project or quota data.
func (p *Pool) RefreshTokenContext(ctx context.Context, id string) (*models.Account, error) {
	ctx, done := p.operationContext(ctx)
	defer done()
	acc, err := p.store.GetAccountContext(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.ensureFreshContext(ctx, acc); err != nil {
		return nil, err
	}
	if acc.Disabled {
		return nil, fmt.Errorf("account disabled")
	}
	return acc, nil
}
func (p *Pool) networkPermit(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case p.networkSlots <- struct{}{}:
		return func() { <-p.networkSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (p *Pool) refreshToken(ctx context.Context, token string) (*oauth.TokenResponse, error) {
	release, err := p.networkPermit(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if p.refresh == nil {
		return nil, fmt.Errorf("OAuth client unavailable")
	}
	return p.refresh(ctx, token)
}
func (p *Pool) loadCodeAssist(ctx context.Context, token string) (string, string, error) {
	release, err := p.networkPermit(ctx)
	if err != nil {
		return "", "", err
	}
	defer release()
	return p.loadAssist(ctx, token)
}
func (p *Pool) fetchAccountQuota(ctx context.Context, token, project string) (*models.QuotaData, error) {
	release, err := p.networkPermit(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return p.fetchQuota(ctx, token, project)
}
