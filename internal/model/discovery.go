// Package model 定义 AI Discovery 的持久化模型与 API DTO。
//
// Discovery 刻意把 GitHub 仓库和 Show HN 投稿拆开：同一仓库可能多次投稿，
// 仓库元数据应复用，而每次投稿的标题、分数、评论和发布时间必须独立保留。
//
// v1.2：移除 LLM 分类体系，enrichment 完成即进入 API 可查询状态。
package model

import "time"

const (
	DiscoveryStatusPending     = "pending"
	DiscoveryStatusReady       = "ready"
	DiscoveryStatusRetryable   = "retryable"
	DiscoveryStatusUnavailable = "unavailable"
)

// DiscoveryRepo 保存仓库级 GitHub 元数据与 enrichment 状态。
//
// v1.2 移除：Category / ClassifyStatus / ClassifyConfidence / ClassifyReason /
// ClassifyMethod / ClassifyModel / ClassifyAttempts / ClassifyNextRetryAt /
// ClassifyError / ClassifiedAt。enrichment 完成后 enrichment_status='ready' 即可查询。
type DiscoveryRepo struct {
	Owner             string
	Repo              string
	GhRepoID          int64
	Description       string
	Homepage          string
	Language          string
	Stars             int
	Forks             int
	Watchers          int
	Subscribers       int
	OpenIssues        int
	OwnerAvatar       string
	DefaultBranch     string
	LicenseSpdx       string
	Topics            []string
	PushedAt          string
	UpdatedAt         string
	CreatedAt         string
	IsArchived        bool
	IsFork            bool
	IsPrivate         bool
	READMEExcerpt     string
	EnrichmentStatus  string
	EnrichAttempts    int
	EnrichNextRetryAt *time.Time
	EnrichError       string
	EnrichedAt        *time.Time
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
	UpdatedRecordAt   time.Time
}

// ToRepoCard 把 Discovery 仓库转换为共享 Repo metadata DTO。
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

// DiscoveryExtension 是 endpoint 专用的 Show HN 语义段（v1.2：移除 Category / ClassifyConfidence / ClassifyReason）。
type DiscoveryExtension struct {
	HNID          int64  `json:"hn_id"`
	HNTitle       string `json:"hn_title"`
	HNURL         string `json:"hn_url"`
	SourceURL     string `json:"source_url,omitempty"`
	HNScore       int    `json:"hn_score"`
	HNComments    int    `json:"hn_comments"`
	HNPublishedAt string `json:"hn_published_at"`
}

// DiscoveryItemDTO 把共享 Repo DTO 与 Discovery 场景字段组合。
type DiscoveryItemDTO struct {
	Repo      StarcatRepoCardDTO `json:"repo"`
	Discovery DiscoveryExtension `json:"discovery"`
}

// DiscoveryQuery 是列表接口的分页参数（v1.2：移除 Category 字段）。
type DiscoveryQuery struct {
	Page     int
	PageSize int
	Since    time.Time
}
