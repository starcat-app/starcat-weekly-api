package model

import "time"

const (
	IngestKindCollector    = "collector"
	IngestKindManualImport = "manual_import"
	IngestKindBackfill     = "backfill"

	IngestBatchPending        = "pending"
	IngestBatchProcessing     = "processing"
	IngestBatchSuccess        = "success"
	IngestBatchPartialSuccess = "partial_success"
	IngestBatchFailed         = "failed"

	IngestItemPending    = "pending"
	IngestItemProcessing = "processing"
	IngestItemRetrying   = "retrying"
	IngestItemSuccess    = "success"
	IngestItemDiscarded  = "discarded"
)

// IngestCandidate 是 Collector 或人工导入提交的单个候选仓库。
// Owner/Repo 是尚未经 GitHub API 校正的输入，Worker 成功后以 GitHub 返回身份为准。
type IngestCandidate struct {
	Owner       string
	Repo        string
	ExternalKey string
	OccurredAt  time.Time
	SourceURL   string
	Title       string
	Summary     string
	Rank        *int
	Payload     map[string]any
}

// EnqueueBatchRequest 是持久化入队请求；调用方不得在构造或提交期间执行 GitHub enrich。
type EnqueueBatchRequest struct {
	ID             string
	SourceCode     string
	Kind           string
	IdempotencyKey string
	Cursor         map[string]any
	Candidates     []IngestCandidate
}

type IngestBatch struct {
	ID             string         `json:"batch_id"`
	SourceCode     string         `json:"source_code"`
	Kind           string         `json:"kind"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Status         string         `json:"status"`
	Cursor         map[string]any `json:"cursor,omitempty"`
	Total          int            `json:"total"`
	Success        int            `json:"success"`
	Discarded      int            `json:"discarded"`
	CreatedAt      string         `json:"created_at"`
	StartedAt      string         `json:"started_at,omitempty"`
	FinishedAt     string         `json:"finished_at,omitempty"`
	UpdatedAt      string         `json:"updated_at"`
	Items          []IngestItem   `json:"items,omitempty"`
}

type IngestItem struct {
	ID                 int64  `json:"id"`
	Owner              string `json:"owner"`
	Repo               string `json:"repo"`
	NormalizedFullName string `json:"normalized_full_name"`
	ExternalKey        string `json:"external_key"`
	Status             string `json:"status"`
	Attempts           int    `json:"attempts"`
	NextAttemptAt      string `json:"next_attempt_at,omitempty"`
	GhRepoID           *int64 `json:"gh_repo_id,omitempty"`
	LastErrorCode      string `json:"last_error_code,omitempty"`
	LastErrorMessage   string `json:"last_error_message,omitempty"`
	FinishedAt         string `json:"finished_at,omitempty"`
}

// IngestWorkItem 是 Worker 领取后的完整工作快照。
// 领取事务结束后只使用该值执行网络请求，避免持有 SQLite transaction。
type IngestWorkItem struct {
	ID          int64
	BatchID     string
	SourceCode  string
	Owner       string
	Repo        string
	ExternalKey string
	OccurredAt  time.Time
	SourceURL   string
	Title       string
	Summary     string
	Rank        *int
	Payload     map[string]any
	Attempts    int
}

type IngestFailureResult struct {
	Status        string
	Attempts      int
	NextAttemptAt *time.Time
	BatchTerminal bool
}

type EnqueueBatchResult struct {
	Batch          IngestBatch
	DuplicateCount int
	Created        bool
}

type IngestBatchAcceptance struct {
	BatchID        string `json:"batch_id"`
	SourceCode     string `json:"source_code"`
	Status         string `json:"status"`
	Total          int    `json:"total"`
	DuplicateCount int    `json:"duplicate_count"`
	CreatedAt      string `json:"created_at"`
}
