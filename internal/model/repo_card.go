// Package model 定义 Weekly 服务的数据模型
//
// R-01 v1.2: StarcatRepoCardDTO 必须与 trending-api / sharing-api 三仓
// **字节级一致**（包括字段顺序 / json tag / Go 字段名）。三仓 DTO 是同一份契约
// 的多处实现，前端 Swift 用单一 Codable 同时解码三个 endpoint 返回值。
//
// 字段严格分两层（详见 supports/docs/R-01-总体设计.md §3.9）：
//   核心字段（顶层）: GitHub /repos/{o}/{r} 原生语义
//   扩展段（嵌套子对象）: trending / weekly / sharing 场景发现型语义
//
// 红线：
//   1. 非 Repo metadata 字段不能放顶层
//   2. 不能把扩展段字段提升到顶层
//   3. 不能在扩展段塞非本场景语义的字段
//
// 本仓与 trending-api 的差异（P1-3a 修订前）：
//   - 字段名 `HtmlURL` → `HTMLURL`（Go 命名规范，对齐 trending-api）
//   - 缺 `Trending *TrendingExtension` 字段（即便 weekly 永远填 nil
//     也必须保留 struct 定义，否则三仓 DTO 不一致）
package model

// StarcatRepoCardDTO 统一卡片数据，所有 /api/v1/repos /api/v1/projects 共用。
type StarcatRepoCardDTO struct {
	GhRepoID      int64              `json:"gh_repo_id"`
	FullName      string             `json:"full_name"`
	Owner         string             `json:"owner"`
	Repo          string             `json:"repo"`
	OwnerAvatar   *string            `json:"owner_avatar"`
	Description   *string            `json:"description"`
	Language      *string            `json:"language"`
	Stars         int                `json:"stars"`
	Forks         int                `json:"forks"`
	Watchers      int                `json:"watchers"`
	Subscribers   int                `json:"subscribers"`
	Topics        []string           `json:"topics"`
	Homepage      *string            `json:"homepage"`
	LicenseSpdx   *string            `json:"license_spdx"`
	IsArchived    bool               `json:"is_archived"`
	IsFork        bool               `json:"is_fork"`
	IsPrivate     bool               `json:"is_private"`
	DefaultBranch *string            `json:"default_branch"`
	OpenIssues    int                `json:"open_issues"`
	PushedAt      *string            `json:"pushed_at"`
	UpdatedAt     *string            `json:"updated_at"`
	CreatedAt     *string            `json:"created_at"`
	HTMLURL       *string            `json:"html_url"`
	Trending      *TrendingExtension `json:"trending,omitempty"`
	Weekly        *WeeklyExtension   `json:"weekly,omitempty"`
}

// TrendingExtension trending 场景发现型字段。
//
// 本仓 (weekly-api) 不会填充此字段（始终为 nil + omitempty 不输出 JSON），
// 但 struct 定义必须与 trending-api 一致，保证三仓 DTO 字节级一致。
type TrendingExtension struct {
	Change       int                   `json:"change"`
	Contributors []TrendingContributor `json:"contributors"`
}

// TrendingContributor 贡献者简要信息。
type TrendingContributor struct {
	Avatar string `json:"avatar"`
	Login  string `json:"login"`
}

// WeeklyExtension weekly 场景发现型字段。
type WeeklyExtension struct {
	FirstIssue int    `json:"first_issue"`
	IssueURL   string `json:"issue_url"`
}
