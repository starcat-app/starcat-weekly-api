// Package model 定义 Weekly 服务的数据模型
package model

import "time"

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
}

// ProjectResponse 项目列表 API 响应
type ProjectResponse struct {
	Items    []Project `json:"items"`
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
}

// QueryParams 项目查询参数
type QueryParams struct {
	Page      int
	PageSize  int
	Issue     string // "latest" 或具体期号
	IssueFrom int
	IssueTo   int
	Language  string
	Sort      string
}
