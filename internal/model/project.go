// Package model 定义 Weekly 服务的数据模型
package model

import (
	"strings"
	"time"
)

// WeeklyIssue 代表一期周刊
type WeeklyIssue struct {
	Number      int       `json:"number"`
	PublishedAt time.Time `json:"published_at"`
	SourceURL   string    `json:"source_url"`
	ParsedAt    time.Time `json:"parsed_at"`

	// ContentHash 是周刊 Markdown 原文的稳定版本标识，仅供服务内部判断是否需要重入队。
	// 不能使用本地文件 mtime：git clone、恢复备份或重新挂载卷都会改变 mtime，
	// 但并不代表上游周刊内容发生了变化。
	ContentHash string `json:"-"`
}

// Project 代表从周刊中提取的 GitHub 项目
type Project struct {
	ID               int64      `json:"id"`
	RepoOwner        string     `json:"owner"`
	RepoName         string     `json:"repo"`
	URL              string     `json:"url"`
	Description      string     `json:"description"`
	Stars            int        `json:"stars"`
	Language         string     `json:"language"`
	Topics           string     `json:"topics,omitempty"`
	FirstIssueNumber int        `json:"first_issue"`
	IssueURL         string     `json:"issue_url"`
	EnrichedAt       *time.Time `json:"enriched_at,omitempty"`
	IsAvailable      bool       `json:"is_available"`

	// R-01 补全字段
	GhRepoID      int64  `json:"gh_repo_id"`
	Forks         int    `json:"forks"`
	Watchers      int    `json:"watchers"`
	Subscribers   int    `json:"subscribers"`
	OwnerAvatar   string `json:"owner_avatar"`
	Homepage      string `json:"homepage"`
	LicenseSpdx   string `json:"license_spdx"`
	IsArchived    bool   `json:"is_archived"`
	IsFork        bool   `json:"is_fork"`
	IsPrivate     bool   `json:"is_private"`
	DefaultBranch string `json:"default_branch"`
	OpenIssues    int    `json:"open_issues"`
	PushedAt      string `json:"pushed_at"`
	UpdatedAt     string `json:"updated_at"`
	CreatedAt     string `json:"created_at"`
}

// strPtrOrNil 把 Go 空字符串转成 nil *string。
//
// 设计契约（详细设计 §6.1）：StarcatRepoCardDTO 中所有 *string 字段语义是
// 「缺失即 null」。如果 enricher 还没补全，应该输出 JSON null 而非 ""，否则
// 前端 Swift `String?` 解码会拿到 Optional("") 触发空 URL 请求 / 空白 UI。
//
// 直接 `&p.X` 即便 p.X == "" 也会拿到指向空串的指针，序列化输出为 ""。
// 此 helper 统一兜底，避免每个字段重复 if-else。
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ToRepoCard converts Project to StarcatRepoCardDTO.
//
// 注意：所有 *string 字段都用 strPtrOrNil 包装（详细设计 §6.1）——空串语义等价 null。
func (p Project) ToRepoCard() StarcatRepoCardDTO {
	card := StarcatRepoCardDTO{
		GhRepoID:    p.GhRepoID,
		FullName:    p.RepoOwner + "/" + p.RepoName,
		Owner:       p.RepoOwner,
		Repo:        p.RepoName,
		OwnerAvatar: strPtrOrNil(p.OwnerAvatar),
		Description: strPtrOrNil(p.Description),
		Language:    strPtrOrNil(p.Language),
		Stars:       p.Stars,
		Forks:       p.Forks,
		Watchers:    p.Watchers,
		Subscribers: p.Subscribers,
		Homepage:    strPtrOrNil(p.Homepage),
		LicenseSpdx: strPtrOrNil(p.LicenseSpdx),
		IsArchived:  p.IsArchived,
		IsFork:      p.IsFork,
		IsPrivate:   p.IsPrivate,
		OpenIssues:  p.OpenIssues,
		HTMLURL:     strPtrOrNil(p.URL),
		Weekly: &WeeklyExtension{
			FirstIssue: p.FirstIssueNumber,
			IssueURL:   p.IssueURL,
		},
	}

	if p.Topics != "" {
		card.Topics = strings.Split(p.Topics, ",")
	}
	card.DefaultBranch = strPtrOrNil(p.DefaultBranch)
	card.PushedAt = strPtrOrNil(p.PushedAt)
	card.UpdatedAt = strPtrOrNil(p.UpdatedAt)
	card.CreatedAt = strPtrOrNil(p.CreatedAt)

	return card
}

// QueryParams 项目查询参数
type QueryParams struct {
	Page              int
	PageSize          int
	Issue             string // "latest" 或具体期号
	IssueFrom         int
	IssueTo           int
	Language          string
	Sort              string
	IncludeUnenriched bool // 默认 false，仅返回已 enrich 的项目
}
