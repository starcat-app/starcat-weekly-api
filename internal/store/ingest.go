package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// EnqueueIngestBatch 在一个短事务中写入 batch 和全部 items。
// 返回前 transaction 已经 commit，因此上层收到 Created=true 后才可以发送内存 wake。
func (s *SQLiteStore) EnqueueIngestBatch(request model.EnqueueBatchRequest) (model.EnqueueBatchResult, error) {
	if request.IdempotencyKey != "" {
		existing, err := s.getIngestBatchByIdempotencyKey(request.IdempotencyKey, false)
		if err != nil {
			return model.EnqueueBatchResult{}, err
		}
		if existing != nil {
			return model.EnqueueBatchResult{Batch: *existing, Created: false}, nil
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return model.EnqueueBatchResult{}, err
	}
	defer rollback(tx)
	now := time.Now().UTC().Format(time.RFC3339)
	if request.Cursor == nil {
		request.Cursor = map[string]any{}
	}
	cursorJSON, err := json.Marshal(request.Cursor)
	if err != nil {
		return model.EnqueueBatchResult{}, fmt.Errorf("encode cursor: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO ingest_batches(
			id, source_code, kind, idempotency_key, status, cursor_json,
			total, success, discarded, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
	`, request.ID, request.SourceCode, request.Kind, nullString(request.IdempotencyKey),
		model.IngestBatchPending, string(cursorJSON), len(request.Candidates), now, now); err != nil {
		// 两个并发重放请求可能都在 transaction 前未查到记录；唯一约束胜出后，
		// 回滚当前 transaction，再由外层按幂等键读取已存在批次。
		rollback(tx)
		if request.IdempotencyKey != "" && strings.Contains(strings.ToLower(err.Error()), "unique") {
			existing, lookupErr := s.getIngestBatchByIdempotencyKey(request.IdempotencyKey, false)
			if lookupErr != nil {
				return model.EnqueueBatchResult{}, lookupErr
			}
			if existing != nil {
				return model.EnqueueBatchResult{Batch: *existing, Created: false}, nil
			}
		}
		return model.EnqueueBatchResult{}, err
	}
	for _, candidate := range request.Candidates {
		payloadJSON, err := json.Marshal(candidate.Payload)
		if err != nil {
			return model.EnqueueBatchResult{}, fmt.Errorf("encode payload for %s/%s: %w", candidate.Owner, candidate.Repo, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO ingest_items(
				batch_id, owner, repo, normalized_full_name, external_key,
				occurred_at, source_url, title, summary, rank, payload_json,
				status, attempts, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
		`, request.ID, candidate.Owner, candidate.Repo, strings.ToLower(candidate.Owner+"/"+candidate.Repo),
			candidate.ExternalKey, candidate.OccurredAt.UTC().Format(time.RFC3339), nullString(candidate.SourceURL),
			nullString(candidate.Title), nullString(candidate.Summary), candidate.Rank, string(payloadJSON),
			model.IngestItemPending, now, now); err != nil {
			return model.EnqueueBatchResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.EnqueueBatchResult{}, err
	}
	batch, err := s.GetIngestBatch(request.ID, false)
	if err != nil {
		return model.EnqueueBatchResult{}, err
	}
	if batch == nil {
		return model.EnqueueBatchResult{}, fmt.Errorf("batch %s missing after commit", request.ID)
	}
	return model.EnqueueBatchResult{Batch: *batch, Created: true}, nil
}

// GetIngestBatch 返回批次汇总；includeItems 仅供管理状态页按需展开，避免列表响应膨胀。
func (s *SQLiteStore) GetIngestBatch(id string, includeItems bool) (*model.IngestBatch, error) {
	batch, err := scanIngestBatch(s.db.QueryRow(`
		SELECT id, source_code, kind, idempotency_key, status, cursor_json,
		       total, success, discarded, created_at, started_at, finished_at, updated_at
		FROM ingest_batches WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if includeItems {
		items, err := s.getIngestItems(id)
		if err != nil {
			return nil, err
		}
		batch.Items = items
	}
	return batch, nil
}

func (s *SQLiteStore) getIngestBatchByIdempotencyKey(key string, includeItems bool) (*model.IngestBatch, error) {
	var id string
	if err := s.db.QueryRow(`SELECT id FROM ingest_batches WHERE idempotency_key=?`, key).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return s.GetIngestBatch(id, includeItems)
}

func scanIngestBatch(scanner rowScanner) (*model.IngestBatch, error) {
	var batch model.IngestBatch
	var idempotency, cursorJSON, started, finished sql.NullString
	if err := scanner.Scan(&batch.ID, &batch.SourceCode, &batch.Kind, &idempotency, &batch.Status,
		&cursorJSON, &batch.Total, &batch.Success, &batch.Discarded, &batch.CreatedAt,
		&started, &finished, &batch.UpdatedAt); err != nil {
		return nil, err
	}
	batch.IdempotencyKey = idempotency.String
	batch.StartedAt = started.String
	batch.FinishedAt = finished.String
	batch.Cursor = make(map[string]any)
	if cursorJSON.Valid && cursorJSON.String != "" {
		if err := json.Unmarshal([]byte(cursorJSON.String), &batch.Cursor); err != nil {
			return nil, fmt.Errorf("decode cursor for batch %s: %w", batch.ID, err)
		}
	}
	return &batch, nil
}

func (s *SQLiteStore) getIngestItems(batchID string) ([]model.IngestItem, error) {
	rows, err := s.db.Query(`
		SELECT id, owner, repo, normalized_full_name, external_key, status, attempts,
		       next_attempt_at, gh_repo_id, last_error_code, last_error_message, finished_at
		FROM ingest_items WHERE batch_id=? ORDER BY id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.IngestItem
	for rows.Next() {
		var item model.IngestItem
		var nextAttempt, errorCode, errorMessage, finished sql.NullString
		var repoID sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Owner, &item.Repo, &item.NormalizedFullName,
			&item.ExternalKey, &item.Status, &item.Attempts, &nextAttempt, &repoID,
			&errorCode, &errorMessage, &finished); err != nil {
			return nil, err
		}
		item.NextAttemptAt = nextAttempt.String
		if repoID.Valid {
			value := repoID.Int64
			item.GhRepoID = &value
		}
		item.LastErrorCode = errorCode.String
		item.LastErrorMessage = errorMessage.String
		item.FinishedAt = finished.String
		items = append(items, item)
	}
	return items, rows.Err()
}
