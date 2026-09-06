package pool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/oauth"
)

func (p *Pool) SkipExpired() bool {
	return p.store.BoolSetting("skip_expired_accounts", p.cfg.SkipExpiredDefault)
}

func (p *Pool) Import(name, note, raw string, purchasedAt time.Time) (*models.ImportResult, error) {
	return p.ImportContext(p.ctx, name, note, raw, purchasedAt)
}
func (p *Pool) ImportContext(ctx context.Context, name, note, raw string, purchasedAt time.Time) (*models.ImportResult, error) {
	ctx, done := p.operationContext(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	return p.importTokens(ctx, batch, tokens)
}

func (p *Pool) ImportInto(batchID, raw string) (*models.ImportResult, error) {
	return p.ImportIntoContext(p.ctx, batchID, raw)
}
func (p *Pool) ImportIntoContext(ctx context.Context, batchID, raw string) (*models.ImportResult, error) {
	ctx, done := p.operationContext(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	batch, err := p.store.GetBatch(batchID)
	if err != nil {
		return nil, fmt.Errorf("批次不存在")
	}
	tokens := oauth.ExtractTokens(raw)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("未找到有效 refresh_token（需以 1// 开头）")
	}
	return p.importTokens(ctx, batch, tokens)
}

func (p *Pool) importTokens(ctx context.Context, batch *models.Batch, tokens []string) (*models.ImportResult, error) {
	res := &models.ImportResult{Batch: batch}
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		importedIDs []string
	)
	sem := p.importSlots
importLoop:
	for _, token := range tokens {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break importLoop
		}
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
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
			acc, err := p.hydrateAccount(ctx, batch.ID, token)
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
	for _, id := range importedIDs {
		if ctx.Err() != nil {
			break
		}
		p.enqueueRefreshContext(ctx, id)
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

func (p *Pool) hydrateAccount(ctx context.Context, batchID, refreshToken string) (*models.Account, error) {
	tok, err := p.refreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	info, err := p.oauth.UserInfoContext(ctx, tok.AccessToken)
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken != "" {
		refreshToken = tok.RefreshToken
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

func maskToken(t string) string {
	if len(t) <= 10 {
		return t
	}
	return t[:6] + "…" + t[len(t)-4:]
}
