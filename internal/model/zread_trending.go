// Package model 定义 weekly-api 的 zread_trending 行模型。
//
// 决策 ①（v0.5 确认）：zread_trending 独立建表（不合并到 projects / weekly_issues），
// 因为 zread 周 trending 与阮一峰周刊的字段语义差异巨大（json 拉取 vs git parse）。
//
// 本文件只定义 Go 行结构与 JSON wire 格式，DB schema 见 store/sqlite.go migrateV3。
//
// 设计文档：19-wiki集成.md §8.3 / §8.4
package model

// ZreadTrending 对应 zread_trending 表一行。
//
// 字段分组（与 19-wiki集成.md §8.3 SQL schema 1:1 对齐）：
//  1. zread 拉取原生字段（11）
//  2. enricher 14 字段（与阮一峰周刊 projects 共用，enricher.EnrichOne 补全）
//  3. v0.4.1 跨年周回溯字段（3）
//  4. 元数据 fetched_at
type ZreadTrending struct {
	// zread 拉取原生（11）
	WeekLabel     string `json:"week_label"`     // "This Week" / "Last Week" / ""（历史周）
	WeekStart     string `json:"week_start"`     // "2026-06-08" ISO 8601 推断后
	WeekEnd       string `json:"week_end"`       // "2026-06-14" ISO 8601 推断后
	RankInWeek    int    `json:"rank"`           // 该 repo 在本组里的序号
	RepoID        string `json:"repo_id"`        // zread UUID
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	Description   string `json:"description,omitempty"`
	DescriptionZh string `json:"description_zh,omitempty"`
	StarCount     int    `json:"star_count"`
	Language      string `json:"language,omitempty"`
	Topics        string `json:"topics,omitempty"` // JSON array 字符串
	WikiID        string `json:"wiki_id,omitempty"`

	// enricher 14 字段（与阮一峰周刊 projects 字段一一对齐，enricher.EnrichOne 补全）
	GhRepoID         int64  `json:"gh_repo_id,omitempty"`
	Forks            int    `json:"forks"`
	OpenIssues       int    `json:"open_issues"`
	Watchers         int    `json:"watchers"`
	SubscribersCount int    `json:"subscribers_count"`
	PushedAt         string `json:"pushed_at,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	LicenseSpdx      string `json:"license_spdx,omitempty"`
	DefaultBranch    string `json:"default_branch,omitempty"`
	IsArchived       bool   `json:"is_archived"`
	IsFork           bool   `json:"is_fork"`

	// v0.4.1 跨年周回溯字段
	ZreadWeekStartRaw string `json:"zread_week_start_raw,omitempty"`
	ZreadWeekEndRaw   string `json:"zread_week_end_raw,omitempty"`
	ZreadYearInferred int    `json:"zread_year_inferred"`

	// 元数据
	FetchedAt string `json:"fetched_at"` // RFC3339
}

// ZreadTrendingEnvelope 是 R-04 之前 zread 独立端点使用过的响应 DTO。
//
// 抽出来让 writeJSONWithMeta 的类型推断更稳，避免 map[string]any 与 envelope JSON 字段名漂移。
// 顶层 envelope 的 schema_version / meta 不变；通过 data 内嵌的 week_label
// 区分 zread 数据源（不污染 envelope.go 共享件）。
type ZreadTrendingEnvelope struct {
	WeekLabel string            `json:"week_label"`
	WeekStart string            `json:"week_start"`
	WeekEnd   string            `json:"week_end"`
	FetchedAt string            `json:"fetched_at"`
	Items     []ZreadTrending   `json:"items"`
}
