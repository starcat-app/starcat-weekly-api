package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// ClaimIngestItem 领取一个当前可执行候选并写入租约。
// transaction 只覆盖恢复过期租约、选择和状态更新，返回后不得继续持有数据库资源。
func (s *SQLiteStore) ClaimIngestItem(workerID string, now time.Time, leaseDuration time.Duration) (*model.IngestWorkItem, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	nowText := now.UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`
		UPDATE ingest_items
		SET status=?, lease_owner=NULL, lease_expires_at=NULL, next_attempt_at=?, updated_at=?
		WHERE status=? AND lease_expires_at IS NOT NULL AND lease_expires_at<=?
	`, model.IngestItemRetrying, nowText, nowText, model.IngestItemProcessing, nowText); err != nil {
		return nil, err
	}

	row := tx.QueryRow(`
		SELECT i.id, i.batch_id, b.source_code, i.owner, i.repo, i.external_key,
		       i.occurred_at, i.source_url, i.title, i.summary, i.rank, i.payload_json, i.attempts
		FROM ingest_items i
		JOIN ingest_batches b ON b.id=i.batch_id
		WHERE i.status=? OR (i.status=? AND (i.next_attempt_at IS NULL OR i.next_attempt_at<=?))
		ORDER BY i.id ASC LIMIT 1
	`, model.IngestItemPending, model.IngestItemRetrying, nowText)
	work, err := scanIngestWorkItem(row)
	if err == sql.ErrNoRows {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	leaseExpiresAt := now.Add(leaseDuration).UTC().Format(time.RFC3339)
	result, err := tx.Exec(`
		UPDATE ingest_items
		SET status=?, attempts=attempts+1, lease_owner=?, lease_expires_at=?,
		    next_attempt_at=NULL, updated_at=?
		WHERE id=? AND status IN (?, ?)
	`, model.IngestItemProcessing, workerID, leaseExpiresAt, nowText, work.ID,
		model.IngestItemPending, model.IngestItemRetrying)
	if err != nil {
		return nil, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if updated != 1 {
		return nil, fmt.Errorf("claim ingest item %d lost race", work.ID)
	}
	if _, err := tx.Exec(`
		UPDATE ingest_batches
		SET status=?, started_at=COALESCE(started_at, ?), updated_at=?
		WHERE id=? AND status IN (?, ?)
	`, model.IngestBatchProcessing, nowText, nowText, work.BatchID,
		model.IngestBatchPending, model.IngestBatchProcessing); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	work.Attempts++
	return work, nil
}

// CompleteIngestItem 原子完成 repo upsert、来源事件写入和 item/batch 状态更新。
func (s *SQLiteStore) CompleteIngestItem(work model.IngestWorkItem, repo model.GitHubRepo, now time.Time) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	if err := upsertGitHubRepo(tx, repo, now); err != nil {
		return false, err
	}
	event := model.SourceEventInput{
		SourceCode: work.SourceCode, ExternalKey: work.ExternalKey, OccurredAt: work.OccurredAt,
		SourceURL: work.SourceURL, Title: work.Title, Summary: work.Summary, Rank: work.Rank, Payload: work.Payload,
	}
	if err := upsertSourceEventTx(tx, repo.GhRepoID, event, now); err != nil {
		return false, err
	}
	if err := recomputeAggregateTx(tx, repo.GhRepoID); err != nil {
		return false, err
	}
	nowText := now.UTC().Format(time.RFC3339)
	result, err := tx.Exec(`
		UPDATE ingest_items
		SET status=?, gh_repo_id=?, lease_owner=NULL, lease_expires_at=NULL,
		    last_error_code=NULL, last_error_message=NULL, updated_at=?, finished_at=?
		WHERE id=? AND status=?
	`, model.IngestItemSuccess, repo.GhRepoID, nowText, nowText, work.ID, model.IngestItemProcessing)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if updated != 1 {
		return false, fmt.Errorf("complete ingest item %d without active lease", work.ID)
	}
	terminal, err := finalizeIngestBatchTx(tx, work.BatchID, now)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return terminal, nil
}

// FailIngestItem 保存可诊断错误并按 attempts 决定退避或剔除。
func (s *SQLiteStore) FailIngestItem(work model.IngestWorkItem, code, message string, permanent bool, now time.Time) (model.IngestFailureResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.IngestFailureResult{}, err
	}
	defer rollback(tx)
	status := model.IngestItemRetrying
	var nextAttempt *time.Time
	if permanent || work.Attempts >= 3 {
		status = model.IngestItemDiscarded
	} else {
		delay := 15 * time.Minute
		if work.Attempts >= 2 {
			delay = 30 * time.Minute
		}
		value := now.Add(delay).UTC()
		nextAttempt = &value
	}
	nowText := now.UTC().Format(time.RFC3339)
	var nextAttemptValue, finishedValue any
	if nextAttempt != nil {
		nextAttemptValue = nextAttempt.Format(time.RFC3339)
	} else {
		finishedValue = nowText
	}
	result, err := tx.Exec(`
		UPDATE ingest_items
		SET status=?, next_attempt_at=?, lease_owner=NULL, lease_expires_at=NULL,
		    last_error_code=?, last_error_message=?, updated_at=?, finished_at=?
		WHERE id=? AND status=?
	`, status, nextAttemptValue, code, message, nowText, finishedValue, work.ID, model.IngestItemProcessing)
	if err != nil {
		return model.IngestFailureResult{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return model.IngestFailureResult{}, err
	}
	if updated != 1 {
		return model.IngestFailureResult{}, fmt.Errorf("fail ingest item %d without active lease", work.ID)
	}
	terminal, err := finalizeIngestBatchTx(tx, work.BatchID, now)
	if err != nil {
		return model.IngestFailureResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.IngestFailureResult{}, err
	}
	return model.IngestFailureResult{Status: status, Attempts: work.Attempts, NextAttemptAt: nextAttempt, BatchTerminal: terminal}, nil
}

func scanIngestWorkItem(scanner rowScanner) (*model.IngestWorkItem, error) {
	var work model.IngestWorkItem
	var occurredAt string
	var sourceURL, title, summary sql.NullString
	var rank sql.NullInt64
	var payloadJSON string
	if err := scanner.Scan(&work.ID, &work.BatchID, &work.SourceCode, &work.Owner, &work.Repo,
		&work.ExternalKey, &occurredAt, &sourceURL, &title, &summary, &rank, &payloadJSON,
		&work.Attempts); err != nil {
		return nil, err
	}
	work.OccurredAt = parseTime(occurredAt)
	work.SourceURL = sourceURL.String
	work.Title = title.String
	work.Summary = summary.String
	if rank.Valid {
		value := int(rank.Int64)
		work.Rank = &value
	}
	work.Payload = make(map[string]any)
	if err := json.Unmarshal([]byte(payloadJSON), &work.Payload); err != nil {
		return nil, fmt.Errorf("decode ingest item %d payload: %w", work.ID, err)
	}
	return &work, nil
}

func finalizeIngestBatchTx(tx *sql.Tx, batchID string, now time.Time) (bool, error) {
	var total, success, discarded, active int
	if err := tx.QueryRow(`
		SELECT COUNT(*),
		       SUM(CASE WHEN status=? THEN 1 ELSE 0 END),
		       SUM(CASE WHEN status=? THEN 1 ELSE 0 END),
		       SUM(CASE WHEN status IN (?, ?, ?) THEN 1 ELSE 0 END)
		FROM ingest_items WHERE batch_id=?
	`, model.IngestItemSuccess, model.IngestItemDiscarded,
		model.IngestItemPending, model.IngestItemProcessing, model.IngestItemRetrying, batchID).
		Scan(&total, &success, &discarded, &active); err != nil {
		return false, err
	}
	status := model.IngestBatchProcessing
	var finishedAt any
	terminal := active == 0
	if terminal {
		finishedAt = now.UTC().Format(time.RFC3339)
		switch {
		case success == total:
			status = model.IngestBatchSuccess
		case success > 0:
			status = model.IngestBatchPartialSuccess
		default:
			status = model.IngestBatchFailed
		}
	}
	_, err := tx.Exec(`
		UPDATE ingest_batches
		SET status=?, total=?, success=?, discarded=?, finished_at=?, updated_at=?
		WHERE id=?
	`, status, total, success, discarded, finishedAt, now.UTC().Format(time.RFC3339), batchID)
	return terminal, err
}
