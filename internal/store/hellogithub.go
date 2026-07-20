package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

// CreateHelloGitHubBackfill 在单个事务中创建不含 item 的控制批次。
// POST 只有在该事务 commit 后才会由上层发送内存唤醒信号。
func (s *SQLiteStore) CreateHelloGitHubBackfill(start model.HelloGitHubBackfillStart) (*model.IngestBatch, bool, error) {
	if start.IdempotencyKey != "" {
		existing, err := s.getIngestBatchByIdempotencyKey(start.IdempotencyKey, false)
		if err != nil || existing != nil {
			return existing, false, err
		}
	}
	cursor := model.HelloGitHubBackfillCursor{
		Controller: true, FromVolume: start.FromVolume, ToVolume: start.ToVolume, NextVolume: start.FromVolume,
	}
	cursorJSON, err := json.Marshal(cursor)
	if err != nil {
		return nil, false, err
	}
	total := 0
	if start.ToVolume > 0 {
		total = start.ToVolume - start.FromVolume + 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Exec(`
		INSERT INTO ingest_batches(
			id, source_code, kind, idempotency_key, status, cursor_json,
			total, success, discarded, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
	`, start.ID, model.SourceHelloGitHub, model.IngestKindBackfill, nullString(start.IdempotencyKey),
		model.IngestBatchPending, string(cursorJSON), total, now, now)
	if err != nil {
		// 并发幂等请求由唯一键兜底，返回胜出的控制批次。
		if start.IdempotencyKey != "" {
			existing, lookupErr := s.getIngestBatchByIdempotencyKey(start.IdempotencyKey, false)
			if lookupErr != nil {
				return nil, false, lookupErr
			}
			if existing != nil {
				return existing, false, nil
			}
		}
		return nil, false, err
	}
	batch, err := s.GetIngestBatch(start.ID, false)
	return batch, true, err
}

// NextHelloGitHubBackfill 返回当前到期的最早控制批次，并把 pending 原子切换为 processing。
func (s *SQLiteStore) NextHelloGitHubBackfill(now time.Time) (*model.IngestBatch, model.HelloGitHubBackfillCursor, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, model.HelloGitHubBackfillCursor{}, err
	}
	defer rollback(tx)
	var id string
	err = tx.QueryRow(`
		SELECT id FROM ingest_batches
		WHERE source_code=? AND kind=?
		  AND json_extract(cursor_json, '$.controller')=1
		  AND status IN (?, ?)
		  AND COALESCE(json_extract(cursor_json, '$.next_attempt_at'), '') <= ?
		ORDER BY created_at, id LIMIT 1
	`, model.SourceHelloGitHub, model.IngestKindBackfill, model.IngestBatchPending,
		model.IngestBatchProcessing, now.UTC().Format(time.RFC3339)).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, model.HelloGitHubBackfillCursor{}, nil
	}
	if err != nil {
		return nil, model.HelloGitHubBackfillCursor{}, err
	}
	if _, err := tx.Exec(`
		UPDATE ingest_batches SET status=?, started_at=COALESCE(started_at, ?), updated_at=? WHERE id=?
	`, model.IngestBatchProcessing, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), id); err != nil {
		return nil, model.HelloGitHubBackfillCursor{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, model.HelloGitHubBackfillCursor{}, err
	}
	batch, err := s.GetIngestBatch(id, false)
	if err != nil {
		return nil, model.HelloGitHubBackfillCursor{}, err
	}
	cursor, err := decodeHelloGitHubBackfillCursor(batch)
	return batch, cursor, err
}

// UpdateHelloGitHubBackfill 保存期号 checkpoint 和控制批次状态。
func (s *SQLiteStore) UpdateHelloGitHubBackfill(id string, cursor model.HelloGitHubBackfillCursor, total, success int, status string, now time.Time) error {
	cursorJSON, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	var finished any
	if status == model.IngestBatchSuccess || status == model.IngestBatchFailed {
		finished = now.UTC().Format(time.RFC3339)
	}
	result, err := s.db.Exec(`
		UPDATE ingest_batches SET cursor_json=?, total=?, success=?, status=?,
			finished_at=?, updated_at=? WHERE id=?
	`, string(cursorJSON), total, success, status, finished, now.UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("HelloGitHub backfill batch %s not found", id)
	}
	return nil
}

func decodeHelloGitHubBackfillCursor(batch *model.IngestBatch) (model.HelloGitHubBackfillCursor, error) {
	if batch == nil {
		return model.HelloGitHubBackfillCursor{}, fmt.Errorf("nil HelloGitHub backfill batch")
	}
	raw, err := json.Marshal(batch.Cursor)
	if err != nil {
		return model.HelloGitHubBackfillCursor{}, err
	}
	var cursor model.HelloGitHubBackfillCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return model.HelloGitHubBackfillCursor{}, fmt.Errorf("decode HelloGitHub backfill cursor %s: %w", batch.ID, err)
	}
	return cursor, nil
}
