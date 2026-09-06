package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wo/antigravity2api/internal/config"
	"github.com/wo/antigravity2api/internal/models"
	"github.com/wo/antigravity2api/internal/oauth"
	"github.com/wo/antigravity2api/internal/store"
)

func fixture(tb testing.TB, count, perAccount int) (*Pool, *store.Store, []string) {
	tb.Helper()
	st, err := store.Open(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	b, err := st.CreateBatch("synthetic", "", 30, time.Now())
	if err != nil {
		tb.Fatal(err)
	}
	ids := make([]string, count)
	for i := range ids {
		id := fmt.Sprintf("account-%04d", i)
		ids[i] = id
		err = st.InsertAccount(&models.Account{ID: id, BatchID: b.ID, Email: id + "@example.invalid", RefreshToken: "synthetic-" + id, AccessToken: "synthetic-access", ExpiryTimestamp: time.Now().Unix() + 3600, ProjectID: "synthetic-project", CreatedAt: time.Now().Unix() - int64(count-i), Quota: &models.QuotaData{Models: []models.ModelQuota{{Name: "model-a", Percentage: 100}, {Name: "model-b", Percentage: 100}}}})
		if err != nil {
			tb.Fatal(err)
		}
	}
	p := New(config.Config{MaxConcurrentPerAccount: perAccount, SkipExpiredDefault: true}, st, nil, nil)
	tb.Cleanup(func() {
		p.Close()
		if err := st.Close(); err != nil {
			tb.Error(err)
		}
	})
	return p, st, ids
}

func TestAcquireFairRoundRobinInSameSecond(t *testing.T) {
	p, _, ids := fixture(t, 10, 4)
	counts := map[string]int{}
	for i := 0; i < 500; i++ {
		a, release, err := p.Acquire(context.Background(), "model-a", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		counts[a.ID]++
		release()
		release()
	}
	for _, id := range ids {
		if counts[id] != 50 {
			t.Fatalf("unfair selection: %v", counts)
		}
	}
}

func TestLimitedOldestAccountsCannotStarveHealthyAccounts(t *testing.T) {
	p, _, ids := fixture(t, 10, 4)
	for _, id := range ids[:5] {
		p.MarkLimited(id, "model-a", time.Now().Add(time.Minute))
	}
	for i := 0; i < 100; i++ {
		a, release, err := p.Acquire(context.Background(), "model-a", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		release()
		for _, id := range ids[:5] {
			if a.ID == id {
				t.Fatalf("selected limited account %s", id)
			}
		}
	}
}

func TestConcurrentAcquireRespectsInflightCap(t *testing.T) {
	p, _, _ := fixture(t, 2, 2)
	var mu sync.Mutex
	active := map[string]int{}
	start := make(chan struct{})
	errCh := make(chan error, 40)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			a, release, err := p.Acquire(ctx, "model-a", "", nil)
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			active[a.ID]++
			if active[a.ID] > 2 {
				errCh <- fmt.Errorf("%s exceeded cap: %d", a.ID, active[a.ID])
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			active[a.ID]--
			mu.Unlock()
			release()
			release()
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, r := range p.reservations {
		if r.active != 0 {
			t.Errorf("leaked %s: %d", id, r.active)
		}
	}
}

func TestBusyAcquireCancellationAndClose(t *testing.T) {
	p, _, _ := fixture(t, 1, 1)
	_, release, err := p.Acquire(context.Background(), "model-a", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := p.Acquire(ctx, "model-a", "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	waiting := make(chan error, 1)
	go func() { _, _, err := p.Acquire(context.Background(), "model-a", "", nil); waiting <- err }()
	p.StartHealthLoop()
	p.StartHealthLoop()
	p.Close()
	p.Close()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not wake waiting acquire")
	}
}

func TestModelLimitsForwardingAndAccountIsolation(t *testing.T) {
	p, st, ids := fixture(t, 1, 2)
	a, err := st.GetAccount(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	a.Quota.ForwardingRules = map[string]string{"old-model": "model-a"}
	if err := st.UpdateAccountMetadata(a.ID, "", "", a.Quota); err != nil {
		t.Fatal(err)
	}
	p.MarkLimited(a.ID, "model-a", time.Now().Add(time.Minute))
	_, _, err = p.Acquire(context.Background(), "old-model", "", nil)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.RetryAfter <= 0 {
		t.Fatalf("missing model limit: %v", err)
	}
	_, release, err := p.Acquire(context.Background(), "model-b", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	p.MarkLimited(a.ID, "", time.Now().Add(time.Minute))
	if _, _, err := p.Acquire(context.Background(), "model-b", "", nil); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("account limit ignored: %v", err)
	}
	p.MarkLimited(a.ID, "", time.Time{})
	p.MarkLimited(a.ID, "model-a", time.Time{})
	_, release, err = p.Acquire(context.Background(), "old-model", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestStoreMutationsImmediatelyInvalidateScheduler(t *testing.T) {
	p, st, ids := fixture(t, 1, 2)
	acquire := func(want bool) {
		t.Helper()
		_, release, err := p.Acquire(context.Background(), "model-a", "", nil)
		if release != nil {
			release()
		}
		if (err == nil) != want {
			t.Fatalf("want available %v, err=%v", want, err)
		}
	}
	acquire(true)
	if err := st.SetDisabled(ids[0], true, "admin"); err != nil {
		t.Fatal(err)
	}
	acquire(false)
	if err := st.SetDisabled(ids[0], false, ""); err != nil {
		t.Fatal(err)
	}
	acquire(true)
	if err := st.SetForbidden(ids[0], "permission"); err != nil {
		t.Fatal(err)
	}
	acquire(false)
	q := &models.QuotaData{Models: []models.ModelQuota{{Name: "model-a", Percentage: 0, ResetTime: time.Now().Add(time.Hour).Format(time.RFC3339)}}}
	if err := st.UpdateAccountMetadata(ids[0], "", "", q); err != nil {
		t.Fatal(err)
	}
	acquire(false)
	q.Models[0].ResetTime = time.Now().Add(-time.Second).Format(time.RFC3339)
	if err := st.UpdateAccountMetadata(ids[0], "", "", q); err != nil {
		t.Fatal(err)
	}
	acquire(true)
	q.Models[0] = models.ModelQuota{Name: "model-b", Percentage: 100}
	if err := st.UpdateAccountMetadata(ids[0], "", "", q); err != nil {
		t.Fatal(err)
	}
	acquire(false)
	if err := st.DeleteAccount(ids[0]); err != nil {
		t.Fatal(err)
	}
	acquire(false)
}

func TestStickySessionsAreStableBoundedAndRespectDisable(t *testing.T) {
	p, st, _ := fixture(t, 2, 2)
	a, release, err := p.Acquire(context.Background(), "model-a", "session-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for i := 0; i < 10; i++ {
		b, release, err := p.Acquire(context.Background(), "model-a", "session-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		release()
		if b.ID != a.ID {
			t.Fatal("sticky account changed")
		}
	}
	if err := st.SetDisabled(a.ID, true, "admin"); err != nil {
		t.Fatal(err)
	}
	b, release, err := p.Acquire(context.Background(), "model-a", "session-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if b.ID == a.ID {
		t.Fatal("sticky account ignored disable")
	}
	for i := 0; i < maxSessions+10; i++ {
		_, release, err := p.Acquire(context.Background(), "model-a", fmt.Sprintf("session-%d", i), nil)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if len(p.sessions) > maxSessions {
		t.Fatalf("sessions unbounded: %d", len(p.sessions))
	}
	for i := 0; i < maxLimits+10; i++ {
		p.MarkLimited(fmt.Sprint(i), "model-a", time.Now().Add(time.Minute))
	}
	if len(p.limits) > maxLimits {
		t.Fatalf("limits unbounded: %d", len(p.limits))
	}
}

func TestTokenRefreshSingleFlightPersistsRotation(t *testing.T) {
	p, st, ids := fixture(t, 1, 64)
	if err := st.InvalidateToken(ids[0]); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	p.refresh = func(ctx context.Context, token string) (*oauth.TokenResponse, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return &oauth.TokenResponse{AccessToken: "fresh-access", RefreshToken: "rotated-refresh", ExpiresIn: 3600}, nil
	}
	start := make(chan struct{})
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a, release, err := p.Acquire(context.Background(), "model-a", "", nil)
			if err != nil {
				errs <- err
				return
			}
			defer release()
			if a.AccessToken != "fresh-access" {
				errs <- fmt.Errorf("stale token %q", a.AccessToken)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d", calls.Load())
	}
	a, err := st.GetAccount(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if a.RefreshToken != "rotated-refresh" {
		t.Fatal("rotated refresh token lost")
	}
	if len(p.gates) != 0 {
		t.Fatalf("single-flight locks leaked: %d", len(p.gates))
	}
}

func TestAdminDisableDuringRefreshIsNotOverwritten(t *testing.T) {
	p, st, ids := fixture(t, 1, 2)
	if err := st.InvalidateToken(ids[0]); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	resume := make(chan struct{})
	p.refresh = func(context.Context, string) (*oauth.TokenResponse, error) {
		close(entered)
		<-resume
		return &oauth.TokenResponse{AccessToken: "fresh", ExpiresIn: 3600}, nil
	}
	done := make(chan error, 1)
	go func() { _, _, err := p.Acquire(context.Background(), "model-a", "", nil); done <- err }()
	<-entered
	if err := st.SetDisabled(ids[0], true, "admin disabled"); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; err == nil {
		t.Fatal("disabled account returned")
	}
	a, err := st.GetAccount(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if !a.Disabled || a.DisabledReason != "admin disabled" {
		t.Fatalf("disable overwritten: %+v", a)
	}
}

func TestRefreshGateCancellationAndHealthClose(t *testing.T) {
	p, st, ids := fixture(t, 1, 4)
	if err := st.InvalidateToken(ids[0]); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	p.refresh = func(ctx context.Context, _ string) (*oauth.TokenResponse, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p.enqueueRefresh(ids[0])
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := p.RefreshTokenContext(ctx, ids[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter not canceled: %v", err)
	}
	done := make(chan struct{})
	go func() { p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background refresh ignored close")
	}
	if len(p.gates) != 0 {
		t.Fatal("canceled gates leaked")
	}
}

func BenchmarkAcquire100Accounts(b *testing.B) {
	p, _, _ := fixture(b, 100, 4)
	a, release, err := p.Acquire(context.Background(), "model-a", "", nil)
	_ = a
	if err != nil {
		b.Fatal(err)
	}
	release()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, release, err := p.Acquire(context.Background(), "model-a", "", nil)
			if err != nil {
				b.Error(err)
				return
			}
			release()
		}
	})
}

func TestFailedRefreshIsSharedByConcurrentWaiters(t *testing.T) {
	p, st, ids := fixture(t, 1, 32)
	if err := st.InvalidateToken(ids[0]); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	p.refresh = func(context.Context, string) (*oauth.TokenResponse, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return nil, errors.New("synthetic OAuth unavailable")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.RefreshTokenContext(context.Background(), ids[0]); err == nil {
				t.Error("expected refresh failure")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh failure stampede: %d calls", calls.Load())
	}
}

func TestClearLimitedPreservesOtherModelQuotas(t *testing.T) {
	p, _, ids := fixture(t, 1, 2)
	for _, model := range []string{"", "model-a", "model-b"} {
		p.MarkLimited(ids[0], model, time.Now().Add(time.Minute))
	}
	p.ClearLimited(ids[0], "")
	p.ClearLimited(ids[0], "model-a")
	_, release, err := p.Acquire(context.Background(), "model-a", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, _, err := p.Acquire(context.Background(), "model-b", "", nil); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("cleared unrelated model limit: %v", err)
	}
}

func TestCanceledImportDoesNotCreateBatchOrCallOAuth(t *testing.T) {
	p, st, _ := fixture(t, 0, 2)
	var calls atomic.Int32
	p.refresh = func(context.Context, string) (*oauth.TokenResponse, error) {
		calls.Add(1)
		return nil, errors.New("unexpected OAuth call")
	}
	before, err := st.ListBatches()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.ImportContext(ctx, "canceled", "", "1//synthetic", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("import ignored cancellation: %v", err)
	}
	after, err := st.ListBatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || calls.Load() != 0 {
		t.Fatal("canceled import performed work")
	}
}

func TestOldSuccessCannotClearNewConcurrentRateLimit(t *testing.T) {
	p, _, ids := fixture(t, 1, 2)
	olderAttempt := time.Now().Add(-time.Second)
	p.MarkLimited(ids[0], "model-a", time.Now().Add(time.Minute))
	p.ClearLimitedBefore(ids[0], "model-a", olderAttempt)
	if _, _, err := p.Acquire(context.Background(), "model-a", "", nil); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("old response erased newer limit: %v", err)
	}
	p.ClearLimitedBefore(ids[0], "model-a", time.Now())
	_, release, err := p.Acquire(context.Background(), "model-a", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestCanceledAcquireDoesNotWaitForAnotherSnapshotLoad(t *testing.T) {
	p, _, _ := fixture(t, 1, 2)
	unlock, err := p.acquireGate(context.Background(), "@scheduler-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := p.Acquire(ctx, "model-a", "", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("snapshot wait ignored cancellation: %v", err)
	}
}
