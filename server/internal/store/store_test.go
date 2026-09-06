package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wo/antigravity2api/internal/models"
)

func testStore(tb testing.TB, n int) (*Store, string, []string) {
	tb.Helper()
	dir := tb.TempDir()
	s, err := Open(dir)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Error(err)
		}
	})
	b, err := s.CreateBatch("synthetic", "", 30, time.Now())
	if err != nil {
		tb.Fatal(err)
	}
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("account-%d", i)
		if err := s.InsertAccount(&models.Account{ID: ids[i], BatchID: b.ID, Email: ids[i] + "@example.invalid", RefreshToken: "synthetic-" + ids[i], AccessToken: "old-access", ProjectID: "project", CreatedAt: time.Now().Unix() - int64(n-i)}); err != nil {
			tb.Fatal(err)
		}
	}
	return s, dir, ids
}

func TestPickAccountFairConcurrentAndHotPathDoesNotUseSQL(t *testing.T) {
	s, _, ids := testStore(t, 10)
	counts := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a, err := s.PickAccount(true, nil)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				counts[a.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if counts[id] != 100 {
			t.Fatalf("unfair selection: %v", counts)
		}
	}
	var readers []*sql.Conn
	for i := 0; i < 4; i++ {
		conn, err := s.readDB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, conn)
	}
	releaseReaders := func() {
		for _, conn := range readers {
			conn.Close()
		}
	}
	done := make(chan error, 1)
	go func() { _, err := s.PickAccount(true, nil); done <- err }()
	select {
	case err := <-done:
		releaseReaders()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		releaseReaders()
		<-done
		t.Fatal("cached picker waited for a SQL read connection")
	}
}

func TestPartialAccountUpdatesPreserveAdministrativeState(t *testing.T) {
	s, _, ids := testStore(t, 1)
	id := ids[0]
	before := s.AccountsVersion()
	if err := s.SetDisabled(id, true, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateToken(id, "new-access", "rotated-refresh", 3600, time.Now().Unix()+3600); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAccountMetadata(id, "new-project", "pro", &models.QuotaData{Models: []models.ModelQuota{{Name: "model-a", Percentage: 100}}}); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Disabled || a.DisabledReason != "admin" || a.AccessToken != "new-access" || a.RefreshToken != "rotated-refresh" || a.ProjectID != "new-project" {
		t.Fatalf("partial updates overwrote unrelated fields: %+v", a)
	}
	if s.AccountsVersion() < before+3 {
		t.Fatal("mutation did not invalidate snapshots")
	}
	if err := s.InvalidateToken(id); err != nil {
		t.Fatal(err)
	}
	a, err = s.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccessToken != "new-access" || a.ExpiryTimestamp != 0 {
		t.Fatal("invalidation discarded token or kept valid expiry")
	}
}

func TestPickAccountSeesForbiddenDisableAndBatchDeletion(t *testing.T) {
	s, _, ids := testStore(t, 1)
	a, err := s.PickAccount(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetForbidden(ids[0], "verify account"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PickAccount(true, nil); err == nil {
		t.Fatal("forbidden account selected")
	}
	if err := s.UpdateAccountMetadata(ids[0], "", "", &models.QuotaData{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PickAccount(true, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBatch(a.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PickAccount(true, nil); err == nil {
		t.Fatal("deleted batch account selected")
	}
}

func TestLogQueueBoundedAndCloseFlushesAcceptedEntries(t *testing.T) {
	s, dir, ids := testStore(t, 1)
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	accepted, dropped := 0, 0
	for i := 0; i < 10000; i++ {
		err := s.AddLog(models.RequestLog{Status: 200, Model: "synthetic"})
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrLogQueueFull) {
			dropped++
		} else {
			t.Fatal(err)
		}
	}
	if accepted > 2048+logBatchSize || dropped == 0 {
		t.Fatalf("queue not bounded: accepted=%d dropped=%d", accepted, dropped)
	}
	if s.DroppedLogs() != uint64(dropped) {
		t.Fatal("missing overflow counter")
	}
	if err := s.MarkUsed(ids[0]); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLog(models.RequestLog{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
	check, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	total, _, _, err := check.LogStats()
	if err != nil {
		t.Fatal(err)
	}
	if total != accepted {
		t.Fatalf("close lost accepted logs: got %d want %d", total, accepted)
	}
	a, err := check.GetAccount(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if a.LastUsed == 0 {
		t.Fatal("close lost coalesced timestamp")
	}
}

func TestConcurrentLoggingAndCloseDoesNotPanicOrLoseAcceptedLogs(t *testing.T) {
	s, dir, _ := testStore(t, 0)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 300; j++ {
				err := s.AddLog(models.RequestLog{Status: 200})
				if err == nil {
					accepted.Add(1)
				} else if !errors.Is(err, ErrLogQueueFull) && !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
			}
		}()
	}
	closers := make(chan error, 2)
	go func() { closers <- s.Close() }()
	go func() { closers <- s.Close() }()
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-closers; err != nil {
			t.Error(err)
		}
	}
	check, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	total, _, _, err := check.LogStats()
	if err != nil {
		t.Fatal(err)
	}
	if int64(total) != accepted.Load() {
		t.Fatalf("close dropped acknowledged logs: total=%d accepted=%d", total, accepted.Load())
	}
}

func BenchmarkCachedPickAccount100(b *testing.B) {
	s, _, _ := testStore(b, 100)
	if _, err := s.PickAccount(true, nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := s.PickAccount(true, nil); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func TestReadPoolDoesNotBlockCredentialWrites(t *testing.T) {
	s, _, ids := testStore(t, 1)
	var readers []*sql.Conn
	for i := 0; i < 4; i++ {
		conn, err := s.readDB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, conn)
	}
	defer func() {
		for _, conn := range readers {
			conn.Close()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- s.UpdateToken(ids[0], "new-access", "", 3600, time.Now().Unix()+3600) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("busy read pool blocked independent write connection")
	}
	if _, err := readers[0].ExecContext(context.Background(), `UPDATE accounts SET disabled=1`); err == nil {
		t.Fatal("read pool allowed writes")
	}
}

func TestContextQueriesAndCredentialUpdatesCancelWhileConnectionsBusy(t *testing.T) {
	s, _, ids := testStore(t, 1)
	var readers []*sql.Conn
	for i := 0; i < 4; i++ {
		conn, err := s.readDB.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, conn)
	}
	defer func() {
		for _, conn := range readers {
			conn.Close()
		}
	}()
	writer, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.ListAccountsContext(ctx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read connection wait did not cancel: %v", err)
	}
	if _, err := s.GetAccountContext(ctx, ids[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("account read did not cancel: %v", err)
	}
	if err := s.UpdateTokenContext(ctx, ids[0], "fresh", "", 3600, time.Now().Unix()+3600); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("credential write did not cancel: %v", err)
	}
}
