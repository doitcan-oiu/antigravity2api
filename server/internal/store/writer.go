package store

import (
	"database/sql"
	"time"

	"github.com/wo/antigravity2api/internal/models"
)

const logBatchSize = 128

// MarkUsed coalesces timestamps in memory. It does not perform a SQL write on the
// request path; the writer persists at most one timestamp per account per batch.
func (s *Store) MarkUsed(id string) error {
	s.logMu.RLock()
	defer s.logMu.RUnlock()
	if s.logClosed {
		return ErrClosed
	}
	s.usedMu.Lock()
	if len(s.used) < 16384 || s.used[id] != 0 {
		s.used[id] = time.Now().Unix()
	}
	s.usedMu.Unlock()
	return nil
}

func (s *Store) AddLog(l models.RequestLog) error {
	if l.CreatedAt == 0 {
		l.CreatedAt = time.Now().Unix()
	}
	s.logMu.RLock()
	defer s.logMu.RUnlock()
	if s.logClosed {
		return ErrClosed
	}
	select {
	case s.logCh <- l:
		return nil
	default:
		s.droppedLogs.Add(1)
		return ErrLogQueueFull
	}
}

func (s *Store) DroppedLogs() uint64 { return s.droppedLogs.Load() }

func (s *Store) logWorker() {
	defer close(s.logDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]models.RequestLog, 0, logBatchSize)
	flush := func() {
		s.usedMu.Lock()
		used := s.used
		s.used = make(map[string]int64)
		s.usedMu.Unlock()
		if len(batch) == 0 && len(used) == 0 {
			return
		}
		if err := s.writeBatch(batch, used); err != nil {
			s.logWriteErrors.Add(1)
			s.droppedLogs.Add(uint64(len(batch)))
			s.logErrMu.Lock()
			if s.logErr == nil {
				s.logErr = err
			}
			s.logErrMu.Unlock()
		}
		clear(batch)
		batch = batch[:0]
	}
	for {
		select {
		case l, ok := <-s.logCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, l)
			if len(batch) >= logBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Store) writeBatch(batch []models.RequestLog, used map[string]int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(batch) != 0 {
		stmt, err := tx.Prepare(`INSERT INTO request_logs(created_at, protocol, model, mapped_model, account_id, account_email, status, stream, latency_ms, error, mixed, ttft_ms, input_tokens, output_tokens, cache_tokens, reasoning_tokens, tps) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, l := range batch {
			if _, err := stmt.Exec(l.CreatedAt, l.Protocol, l.Model, l.MappedModel, l.AccountID, l.AccountEmail, l.Status, boolToInt(l.Stream), l.LatencyMS, l.Error, boolToInt(l.Mixed), l.TTFTMS, l.InputTokens, l.OutputTokens, l.CacheTokens, l.ReasoningTokens, l.TPS); err != nil {
				return err
			}
		}
	}
	var stmt *sql.Stmt
	if len(used) != 0 {
		stmt, err = tx.Prepare(`UPDATE accounts SET last_used=MAX(COALESCE(last_used,0),?) WHERE id=?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for id, timestamp := range used {
			if _, err := stmt.Exec(timestamp, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Diagnostics exposes bounded writer pressure without account data or secrets.
type Diagnostics struct {
	LogQueueDepth            int    `json:"log_queue_depth"`
	LogQueueCapacity         int    `json:"log_queue_capacity"`
	DroppedLogs              uint64 `json:"dropped_logs"`
	LogWriteErrors           uint64 `json:"log_write_errors"`
	PendingAccountTimestamps int    `json:"pending_account_timestamps"`
	ReadDBWaitCount          int64  `json:"read_db_wait_count"`
	WriteDBWaitCount         int64  `json:"write_db_wait_count"`
}

func (s *Store) Diagnostics() Diagnostics {
	s.usedMu.Lock()
	pending := len(s.used)
	s.usedMu.Unlock()
	return Diagnostics{LogQueueDepth: len(s.logCh), LogQueueCapacity: cap(s.logCh), DroppedLogs: s.droppedLogs.Load(), LogWriteErrors: s.logWriteErrors.Load(), PendingAccountTimestamps: pending, ReadDBWaitCount: s.readDB.Stats().WaitCount, WriteDBWaitCount: s.db.Stats().WaitCount}
}
