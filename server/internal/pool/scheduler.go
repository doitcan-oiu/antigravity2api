package pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wo/antigravity2api/internal/cloudcode"
	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/convert"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/oauth"
	"github.com/wo/antigravity2api/internal/store"
)

var ErrNoAccounts = errors.New("no available accounts")

type UnavailableError struct{ RetryAfter time.Duration }

func (e *UnavailableError) Error() string { return ErrNoAccounts.Error() }
func (e *UnavailableError) Unwrap() error { return ErrNoAccounts }

const (
	maxSessions = 4096
	maxLimits   = 8192
	sessionTTL  = 30 * time.Minute
)

type reservation struct {
	active int
	last   uint64
}
type sessionBinding struct {
	accountID string
	expires   time.Time
}
type limitKey struct{ accountID, model string }
type modelCandidate struct {
	mapped     string
	present    bool
	percentage int
	reset      time.Time
}
type limitRecord struct{ until, markedAt time.Time }
type refreshFailure struct {
	message string
	until   time.Time
}
type accountGate struct {
	token chan struct{}
	refs  int
}

type Pool struct {
	cfg             config.Config
	store           *store.Store
	oauth           *oauth.Client
	cc              *cloudcode.Client
	refresh         func(context.Context, string) (*oauth.TokenResponse, error)
	loadAssist      func(context.Context, string) (string, string, error)
	fetchQuota      func(context.Context, string, string) (*models.QuotaData, error)
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	accounts        []models.Account
	accountIndex    map[string]int
	modelViews      map[string][]modelCandidate
	version         uint64
	sequence        uint64
	reservations    map[string]*reservation
	sessions        map[string]sessionBinding
	limits          map[limitKey]limitRecord
	changed         chan struct{}
	gateMu          sync.Mutex
	gates           map[string]*accountGate
	refreshFailures map[string]refreshFailure
	backgroundMu    sync.Mutex
	background      sync.WaitGroup
	closed          bool
	healthOnce      sync.Once
	importSlots     chan struct{}
	networkSlots    chan struct{}
	refreshQueue    chan string
	queued          map[string]bool
}

func New(cfg config.Config, st *store.Store, oa *oauth.Client, cc *cloudcode.Client) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{cfg: cfg, store: st, oauth: oa, cc: cc, ctx: ctx, cancel: cancel, reservations: make(map[string]*reservation), sessions: make(map[string]sessionBinding), limits: make(map[limitKey]limitRecord), changed: make(chan struct{}), gates: make(map[string]*accountGate), refreshFailures: make(map[string]refreshFailure), importSlots: make(chan struct{}, 4), networkSlots: make(chan struct{}, 8), refreshQueue: make(chan string, 128), queued: make(map[string]bool)}
	if oa != nil {
		p.refresh = oa.RefreshContext
	}
	if cc != nil {
		p.loadAssist = cc.LoadCodeAssistContext
		p.fetchQuota = cc.FetchQuotaContext
	}
	for i := 0; i < 2; i++ {
		p.startBackground(p.refreshWorker)
	}
	return p
}

func (p *Pool) operationContext(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	if p.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (p *Pool) notifyLocked() { close(p.changed); p.changed = make(chan struct{}) }

func (p *Pool) loadSnapshot(ctx context.Context) error {
	version := p.store.AccountsVersion()
	p.mu.Lock()
	fresh := p.version == version
	p.mu.Unlock()
	if fresh {
		return nil
	}
	unlock, err := p.acquireGate(ctx, "@scheduler-snapshot")
	if err != nil {
		return err
	}
	defer unlock()
	version = p.store.AccountsVersion()
	p.mu.Lock()
	fresh = p.version == version
	p.mu.Unlock()
	if fresh {
		return nil
	}
	accounts, err := p.store.ListAccountsContext(ctx, "")
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	live := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		live[a.ID] = true
	}
	for id, r := range p.reservations {
		if !live[id] && r.active == 0 {
			delete(p.reservations, id)
		}
	}
	for key := range p.limits {
		if !live[key.accountID] {
			delete(p.limits, key)
		}
	}
	for key, b := range p.sessions {
		if !live[b.accountID] {
			delete(p.sessions, key)
		}
	}
	p.accounts, p.version = accounts, version
	p.accountIndex = make(map[string]int, len(accounts))
	for i := range accounts {
		p.accountIndex[accounts[i].ID] = i
	}
	p.modelViews = make(map[string][]modelCandidate)
	return nil
}

func sessionKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])
}

func (p *Pool) limitedUntilLocked(id, model string) time.Time {
	until := p.limits[limitKey{accountID: id}].until
	if t := p.limits[limitKey{accountID: id, model: model}].until; t.After(until) {
		until = t
	}
	return until
}

// Model matching is cached once per store version and requested model, instead
// of allocating names and resolving forwarding rules for every account/request.
func (p *Pool) modelViewLocked(requested string) []modelCandidate {
	requested = strings.TrimPrefix(strings.TrimSpace(requested), "models/")
	if view, ok := p.modelViews[requested]; ok {
		return view
	}
	view := make([]modelCandidate, len(p.accounts))
	for i := range p.accounts {
		a := &p.accounts[i]
		c := modelCandidate{mapped: requested, present: true, percentage: -1}
		if requested != "" && a.Quota != nil && len(a.Quota.Models) > 0 {
			names := make([]string, 0, len(a.Quota.Models))
			for _, m := range a.Quota.Models {
				names = append(names, m.Name)
			}
			c.mapped = convert.RewriteToAvailable(requested, names, a.Quota.ForwardingRules)
			c.present = false
			for _, m := range a.Quota.Models {
				if m.Name == c.mapped {
					c.present = true
					c.percentage = m.Percentage
					c.reset, _ = time.Parse(time.RFC3339, m.ResetTime)
					break
				}
			}
		}
		view[i] = c
	}
	if len(p.modelViews) >= 128 {
		clear(p.modelViews)
	}
	p.modelViews[requested] = view
	return view
}
func (c modelCandidate) available(now time.Time) bool {
	return c.present && (c.percentage != 0 || (!c.reset.IsZero() && !c.reset.After(now)))
}

func (p *Pool) selectAccount(ctx context.Context, model, sid string, excluded map[string]struct{}) (*models.Account, func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := p.ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := p.loadSnapshot(ctx); err != nil {
			return nil, nil, err
		}
		p.mu.Lock()
		now := time.Now()
		skipExpired := p.SkipExpired()
		capPerAccount := p.cfg.MaxConcurrentPerAccount
		if capPerAccount <= 0 {
			capPerAccount = 4
		}
		var picked *models.Account
		var chosen *reservation
		busy := false
		var earliest time.Time
		view := p.modelViewLocked(model)
		bound, hasBinding := p.sessions[sid]
		if hasBinding && !bound.expires.After(now) {
			delete(p.sessions, sid)
			hasBinding = false
		}
		for i := range p.accounts {
			a := &p.accounts[i]
			if _, ok := excluded[a.ID]; ok {
				continue
			}
			if a.Disabled || (skipExpired && a.ExpiresAt <= now.Unix()) || (a.Quota != nil && a.Quota.IsForbidden) {
				continue
			}
			candidate := view[i]
			if !candidate.available(now) {
				reset := candidate.reset
				if reset.After(now) && (earliest.IsZero() || reset.Before(earliest)) {
					earliest = reset
				}
				continue
			}
			until := p.limitedUntilLocked(a.ID, candidate.mapped)
			if t := time.Unix(a.RateLimitedUntil, 0); t.After(until) {
				until = t
			}
			if until.After(now) {
				if earliest.IsZero() || until.Before(earliest) {
					earliest = until
				}
				continue
			}
			r := p.reservations[a.ID]
			if r == nil {
				r = &reservation{}
				p.reservations[a.ID] = r
			}
			if r.active >= capPerAccount {
				busy = true
				continue
			}
			if hasBinding && a.ID == bound.accountID {
				picked = a
				chosen = r
				break
			}
			if chosen == nil || r.active < chosen.active || (r.active == chosen.active && r.last < chosen.last) {
				picked = a
				chosen = r
			}
		}
		if picked == nil {
			changed := p.changed
			p.mu.Unlock()
			if busy {
				// Release wakes waiters immediately. The bounded timer also observes admin
				// mutations and externally recorded rate-limit expiry without SQL polling.
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, nil, ctx.Err()
				case <-changed:
					timer.Stop()
				case <-timer.C:
				}
				continue
			}
			delay := time.Duration(0)
			if !earliest.IsZero() {
				delay = time.Until(earliest)
			}
			return nil, nil, &UnavailableError{RetryAfter: delay}
		}
		p.sequence++
		chosen.last = p.sequence
		chosen.active++
		account := store.CloneAccount(picked)
		if sid != "" {
			if len(p.sessions) >= maxSessions {
				var oldestKey string
				var oldest time.Time
				for key, b := range p.sessions {
					if !b.expires.After(now) {
						delete(p.sessions, key)
					} else if oldest.IsZero() || b.expires.Before(oldest) {
						oldestKey, oldest = key, b.expires
					}
				}
				if len(p.sessions) >= maxSessions {
					delete(p.sessions, oldestKey)
				}
			}
			p.sessions[sid] = sessionBinding{account.ID, now.Add(sessionTTL)}
		}
		p.mu.Unlock()
		var once sync.Once
		release := func() { once.Do(func() { p.mu.Lock(); chosen.active--; p.notifyLocked(); p.mu.Unlock() }) }
		return account, release, nil
	}
}

// Acquire reserves one model-capable account. Release is idempotent and must run
// after the upstream response body is consumed, including streaming responses.
// When all eligible accounts are busy, Acquire waits until release or ctx cancel.
func (p *Pool) Acquire(ctx context.Context, model, sessionID string, exclude map[string]struct{}) (*models.Account, func(), error) {
	ctx, done := p.operationContext(ctx)
	defer done()
	excluded := make(map[string]struct{}, len(exclude))
	for id := range exclude {
		excluded[id] = struct{}{}
	}
	sid := sessionKey(sessionID)
	var lastErr error
	for {
		acc, release, err := p.selectAccount(ctx, model, sid, excluded)
		if err != nil {
			if lastErr != nil && errors.Is(err, ErrNoAccounts) {
				return nil, nil, errors.Join(err, lastErr)
			}
			return nil, nil, err
		}
		if err = p.ensureFreshContext(ctx, acc); err == nil && acc.ProjectID == "" {
			err = p.ensureProject(ctx, acc)
		}
		if err == nil && acc.Disabled {
			err = fmt.Errorf("account disabled")
		}
		if err != nil {
			release()
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			lastErr = err
			excluded[acc.ID] = struct{}{}
			if strings.Contains(err.Error(), "invalid_grant") {
				_ = p.store.SetDisabledContext(ctx, acc.ID, true, "refresh_token 已失效")
			} else {
				p.MarkLimited(acc.ID, "", time.Now().Add(5*time.Second))
			}
			_ = p.store.SetAccountErrorContext(ctx, acc.ID, err.Error())
			continue
		}
		// Refresh/project lookup can take time: revalidate any admin change before
		// returning credentials, without a DB read when the version stayed unchanged.
		err = p.loadSnapshot(ctx)
		p.mu.Lock()
		valid := false
		cooling := false
		if err == nil {
			if i, ok := p.accountIndex[acc.ID]; ok {
				a := &p.accounts[i]
				now := time.Now()
				candidate := p.modelViewLocked(model)[i]
				valid = !a.Disabled && (a.Quota == nil || !a.Quota.IsForbidden) && (!p.SkipExpired() || a.ExpiresAt > now.Unix()) && candidate.available(now)
				// Another request may have observed a 429 while credentials were
				// refreshing. Recheck both limit scopes before returning this slot.
				cooling = p.limitedUntilLocked(a.ID, candidate.mapped).After(now) || a.RateLimitedUntil > now.Unix()
			}
		}
		p.mu.Unlock()
		if !valid || cooling {
			release()
			if err != nil {
				return nil, nil, err
			}
			if !valid {
				excluded[acc.ID] = struct{}{}
			}
			// Keep a cooling account visible to selection so an exhausted pool
			// reports its RetryAfter instead of treating it as permanently absent.
			continue
		}
		if err := ctx.Err(); err != nil {
			release()
			return nil, nil, err
		}
		acc.LastUsed = time.Now().Unix()
		if err := p.store.MarkUsed(acc.ID); err != nil {
			release()
			return nil, nil, err
		}
		return acc, release, nil
	}
}

// Next preserves legacy callers without leaving a reservation behind.
func (p *Pool) Next(exclude map[string]struct{}) (*models.Account, error) {
	a, release, err := p.Acquire(context.Background(), "", "", exclude)
	if release != nil {
		release()
	}
	return a, err
}

// An empty model limits an entire account; a model limits only that mapped model.
// State is process-local and bounded. It never permanently disables an account.
func (p *Pool) MarkLimited(accountID, model string, until time.Time) {
	model = strings.TrimPrefix(strings.TrimSpace(model), "models/")
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	key := limitKey{accountID, model}
	if !until.After(now) {
		delete(p.limits, key)
		p.notifyLocked()
		return
	}
	if previous := p.limits[key]; previous.until.After(until) {
		until = previous.until
	}
	if len(p.limits) >= maxLimits {
		var oldestKey limitKey
		var oldest time.Time
		for k, record := range p.limits {
			t := record.until
			if !t.After(now) {
				delete(p.limits, k)
			} else if oldest.IsZero() || t.Before(oldest) {
				oldestKey, oldest = k, t
			}
		}
		if len(p.limits) >= maxLimits {
			delete(p.limits, oldestKey)
		}
	}
	p.limits[key] = limitRecord{until: until, markedAt: now}
	// A limited sticky account should not keep capturing subsequent sessions.
	for sid, b := range p.sessions {
		if b.accountID == accountID {
			delete(p.sessions, sid)
		}
	}
	p.notifyLocked()
}

// ClearLimited removes exactly the recovered scope, preserving other models.
func (p *Pool) ClearLimited(accountID, model string) {
	model = strings.TrimPrefix(strings.TrimSpace(model), "models/")
	p.mu.Lock()
	delete(p.limits, limitKey{accountID, model})
	p.notifyLocked()
	p.mu.Unlock()
}

// ClearLimitedBefore prevents an older in-flight success from erasing a limit
// observed by a newer concurrent attempt. Pass the successful attempt's start.
func (p *Pool) ClearLimitedBefore(accountID, model string, attemptStarted time.Time) {
	model = strings.TrimPrefix(strings.TrimSpace(model), "models/")
	p.mu.Lock()
	key := limitKey{accountID, model}
	if limit, ok := p.limits[key]; ok && !limit.markedAt.After(attemptStarted) {
		delete(p.limits, key)
		p.notifyLocked()
	}
	p.mu.Unlock()
}
