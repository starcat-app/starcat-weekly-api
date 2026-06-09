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

// ToRepoCard converts Project to StarcatRepoCardDTO.
func (p Project) ToRepoCard() StarcatRepoCardDTO {
	card := StarcatRepoCardDTO{
		GhRepoID:    p.GhRepoID,
		FullName:    p.RepoOwner + "/" + p.RepoName,
		Owner:       p.RepoOwner,
		Repo:        p.RepoName,
		OwnerAvatar: &p.OwnerAvatar,
		Description: &p.Description,
		Language:    &p.Language,
		Stars:       p.Stars,
		Forks:       p.Forks,
		Watchers:    p.Watchers,
		Subscribers: p.Subscribers,
		Homepage:    &p.Homepage,
		LicenseSpdx: &p.LicenseSpdx,
		IsArchived:  p.IsArchived,
		IsFork:      p.IsFork,
		IsPrivate:   p.IsPrivate,
		OpenIssues:  p.OpenIssues,
		HtmlURL:     &p.URL,
		Weekly: &WeeklyExtension{
			FirstIssue: p.FirstIssueNumber,
			IssueURL:   p.IssueURL,
		},
	}

	if p.Topics != "" {
		card.Topics = strings.Split(p.Topics, ",")
	}
	if p.DefaultBranch != "" {
		card.DefaultBranch = &p.DefaultBranch
	}
	if p.PushedAt != "" {
		card.PushedAt = &p.PushedAt
	}
	if p.UpdatedAt != "" {
		card.UpdatedAt = &p.UpdatedAt
	}
	if p.CreatedAt != "" {
		card.CreatedAt = &p.CreatedAt
	}

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
