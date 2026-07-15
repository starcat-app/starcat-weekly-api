package ingest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/source"
)

const maxBatchSize = 200

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

type batchRepository interface {
	EnqueueIngestBatch(model.EnqueueBatchRequest) (model.EnqueueBatchResult, error)
}

// Service 负责来源权限、输入规范化、批内去重和 commit 后唤醒。
// 它没有 GitHub client 依赖，从类型边界上保证 POST 不可能同步 enrich。
type Service struct {
	repository batchRepository
	wake       *WakeSignal
	now        func() time.Time
	newID      func() (string, error)
}

// ValidationError 表示调用方可修正的 400 错误；数据库错误仍作为 500 交给 handler。
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func validationErrorf(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

func NewService(repository batchRepository, wake *WakeSignal) *Service {
	return &Service{repository: repository, wake: wake, now: time.Now, newID: newUUID}
}

func (s *Service) Enqueue(request model.EnqueueBatchRequest) (model.IngestBatchAcceptance, error) {
	definition, ok := source.Find(request.SourceCode)
	if !ok || !definition.Enabled {
		return model.IngestBatchAcceptance{}, validationErrorf("source_code %q is not enabled", request.SourceCode)
	}
	if request.Kind == model.IngestKindManualImport && !definition.ManualImportEnabled {
		return model.IngestBatchAcceptance{}, validationErrorf("source_code %q does not allow manual import", request.SourceCode)
	}
	if request.Kind != model.IngestKindCollector && request.Kind != model.IngestKindManualImport && request.Kind != model.IngestKindBackfill {
		return model.IngestBatchAcceptance{}, validationErrorf("invalid ingest kind %q", request.Kind)
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" {
		return model.IngestBatchAcceptance{}, validationErrorf("idempotency_key is required")
	}
	if len(request.Candidates) == 0 {
		return model.IngestBatchAcceptance{}, validationErrorf("repositories must not be empty")
	}
	if len(request.Candidates) > maxBatchSize {
		return model.IngestBatchAcceptance{}, validationErrorf("repositories exceeds limit %d", maxBatchSize)
	}

	id, err := s.newID()
	if err != nil {
		return model.IngestBatchAcceptance{}, err
	}
	request.ID = id
	now := s.now().UTC()
	deduplicated := make([]model.IngestCandidate, 0, len(request.Candidates))
	seen := make(map[string]struct{}, len(request.Candidates))
	for index, candidate := range request.Candidates {
		candidate.Owner = strings.TrimSpace(candidate.Owner)
		candidate.Repo = strings.TrimSpace(candidate.Repo)
		if !ownerPattern.MatchString(candidate.Owner) || !repoPattern.MatchString(candidate.Repo) {
			return model.IngestBatchAcceptance{}, validationErrorf("invalid repository at index %d: %s/%s", index, candidate.Owner, candidate.Repo)
		}
		normalized := strings.ToLower(candidate.Owner + "/" + candidate.Repo)
		if candidate.OccurredAt.IsZero() {
			candidate.OccurredAt = now
		}
		if strings.TrimSpace(candidate.ExternalKey) == "" {
			candidate.ExternalKey = fmt.Sprintf("%s:%s", request.IdempotencyKey, normalized)
		}
		// 人工情报按 repo 去重；Collector/Backfill 允许同一 repo 在同批拥有多个
		// 不同 external event（例如同仓库多次 Show HN 投稿）。
		deduplicationKey := normalized
		if request.Kind != model.IngestKindManualImport {
			deduplicationKey += "\x00" + candidate.ExternalKey
		}
		if _, exists := seen[deduplicationKey]; exists {
			continue
		}
		seen[deduplicationKey] = struct{}{}
		deduplicated = append(deduplicated, candidate)
	}
	duplicateCount := len(request.Candidates) - len(deduplicated)
	request.Candidates = deduplicated
	result, err := s.repository.EnqueueIngestBatch(request)
	if err != nil {
		return model.IngestBatchAcceptance{}, err
	}
	if result.Created {
		s.wake.Notify()
	}
	return model.IngestBatchAcceptance{
		BatchID: result.Batch.ID, SourceCode: result.Batch.SourceCode, Status: result.Batch.Status,
		Total: result.Batch.Total, DuplicateCount: duplicateCount, CreatedAt: result.Batch.CreatedAt,
	}, nil
}

func newUUID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate batch id: %w", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(bytes)
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32], nil
}
