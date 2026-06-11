// Package model 定义 AI Discovery 的持久化模型与 API DTO。
//
// Discovery 刻意把 GitHub 仓库和 Show HN 投稿拆开：同一仓库可能多次投稿，
// 仓库元数据/AI 分类应复用，而每次投稿的标题、分数、评论和发布时间必须独立保留。
package model

import "time"

const (
	DiscoveryCategoryUnknown = "unknown"

	DiscoveryStatusPending     = "pending"
	DiscoveryStatusReady       = "ready"
	DiscoveryStatusRetryable   = "retryable"
	DiscoveryStatusClassified  = "classified"
	DiscoveryStatusRejected    = "rejected"
	DiscoveryStatusUnavailable = "unavailable"
)

// ValidDiscoveryCategory 判断客户端可筛选的 AI 主分类是否合法。
func ValidDiscoveryCategory(category string) bool {
	switch category {
	case "agent", "coding", "mcp", "rag", "infra", "model", "skill":
		return true
	default:
		return false
	}
}

// DiscoveryRepo 保存仓库级 GitHub 元数据与 AI 分类状态。
//
// READMEExcerpt 只保留分类所需的前段文本，不把完整 README 复制进 weekly.db。
// NextRetryAt 是显式重试时钟，避免用 attempts 同时表达“失败次数”和“是否可重试”。
type DiscoveryRepo struct {
	Owner               string
	Repo                string
	GhRepoID            int64
	Description         string
	Homepage            string
	Language            string
	Stars               int
	Forks               int
	Watchers            int
	Subscribers         int
	OpenIssues          int
	OwnerAvatar         string
	DefaultBranch       string
	LicenseSpdx         string
	Topics              []string
	PushedAt            string
	UpdatedAt           string
	CreatedAt           string
	IsArchived          bool
	IsFork              bool
	IsPrivate           bool
	READMEExcerpt       string
	EnrichmentStatus    string
	EnrichAttempts      int
	EnrichNextRetryAt   *time.Time
	EnrichError         string
	EnrichedAt          *time.Time
	Category            string
	ClassifyStatus      string
	ClassifyConfidence  *float64
	ClassifyReason      string
	ClassifyMethod      string
	ClassifyModel       string
	ClassifyAttempts    int
	ClassifyNextRetryAt *time.Time
	ClassifyError       string
	ClassifiedAt        *time.Time
	FirstSeenAt         time.Time
	LastSeenAt          time.Time
	UpdatedRecordAt     time.Time
}

// ToRepoCard 把 Discovery 仓库转换为共享 Repo metadata DTO。
// Show HN 字段不塞进共享 DTO，避免破坏 trending/weekly 的跨服务契约。
func (r DiscoveryRepo) ToRepoCard() StarcatRepoCardDTO {
	card := StarcatRepoCardDTO{
		GhRepoID:      r.GhRepoID,
		FullName:      r.Owner + "/" + r.Repo,
		Owner:         r.Owner,
		Repo:          r.Repo,
		OwnerAvatar:   strPtrOrNil(r.OwnerAvatar),
		Description:   strPtrOrNil(r.Description),
		Language:      strPtrOrNil(r.Language),
		Stars:         r.Stars,
		Forks:         r.Forks,
		Watchers:      r.Watchers,
		Subscribers:   r.Subscribers,
		Topics:        r.Topics,
		Homepage:      strPtrOrNil(r.Homepage),
		LicenseSpdx:   strPtrOrNil(r.LicenseSpdx),
		IsArchived:    r.IsArchived,
		IsFork:        r.IsFork,
		IsPrivate:     r.IsPrivate,
		DefaultBranch: strPtrOrNil(r.DefaultBranch),
		OpenIssues:    r.OpenIssues,
		PushedAt:      strPtrOrNil(r.PushedAt),
		UpdatedAt:     strPtrOrNil(r.UpdatedAt),
		CreatedAt:     strPtrOrNil(r.CreatedAt),
	}
	htmlURL := "https://github.com/" + r.Owner + "/" + r.Repo
	card.HTMLURL = &htmlURL
	return card
}

// DiscoverySubmission 保存一次 Show HN 投稿事实。
type DiscoverySubmission struct {
	HNID        int64
	Owner       string
	Repo        string
	Title       string
	HNURL       string
	SourceURL   string
	Score       int
	Comments    int
	PublishedAt time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// DiscoveryExtension 是 endpoint 专用的 Show HN 语义段。
type DiscoveryExtension struct {
	HNID               int64    `json:"hn_id"`
	HNTitle            string   `json:"hn_title"`
	HNURL              string   `json:"hn_url"`
	SourceURL          string   `json:"source_url,omitempty"`
	HNScore            int      `json:"hn_score"`
	HNComments         int      `json:"hn_comments"`
	HNPublishedAt      string   `json:"hn_published_at"`
	Category           string   `json:"category"`
	ClassifyConfidence *float64 `json:"classify_confidence,omitempty"`
	ClassifyReason     string   `json:"classify_reason,omitempty"`
}

// DiscoveryItemDTO 把共享 Repo DTO 与 Discovery 场景字段组合起来。
// 这样新增场景不会要求 trending-api 同步增加一个永远为 nil 的字段。
type DiscoveryItemDTO struct {
	Repo      StarcatRepoCardDTO `json:"repo"`
	Discovery DiscoveryExtension `json:"discovery"`
}

// DiscoveryQuery 是列表接口的分页与分类参数。
type DiscoveryQuery struct {
	Category string
	Page     int
	PageSize int
	Since    time.Time
}
