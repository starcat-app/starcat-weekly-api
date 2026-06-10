// Package store 定义数据存储接口
package store

import "github.com/dong4j/starcat-weekly-api/internal/model"

// Store 数据存储接口，便于 mock 测试和未来切换存储后端
type Store interface {
	// UpsertProject 插入或忽略项目（同一 owner+name 去重）
	UpsertProject(p *model.Project) error

	// GetProjects 分页查询项目列表，返回 items + total count
	GetProjects(params model.QueryParams) ([]model.Project, int, error)

	// UpsertIssue 插入或更新周刊期号
	UpsertIssue(issue *model.WeeklyIssue) error

	// GetIssues 列出所有期号
	GetIssues() ([]model.WeeklyIssue, error)

	// GetIssue 根据期号获取单期详情
	GetIssue(number int) (*model.WeeklyIssue, error)

	// GetLatestIssueNumber 返回最新期号
	GetLatestIssueNumber() (int, error)

	// GetUnenrichedProjects 获取未补全元数据的项目（enriched_at IS NULL）
	GetUnenrichedProjects(limit int) ([]model.Project, error)

	// UpdateProjectMeta 更新项目的 GitHub 元数据（stars, language, description 等）
	UpdateProjectMeta(p *model.Project) error

	// GetProjectByOwnerRepo 获取单个项目
	GetProjectByOwnerRepo(owner, repo string) (*model.Project, error)

	// --- zread 周 trending（v0.5 R-02 新增，决策 ① 独立表）---

	// UpsertZreadTrending upsert 一条 zread trending 记录
	UpsertZreadTrending(z model.ZreadTrending) error

	// QueryZreadTrending 按 week 参数查 zread 周 trending 列表
	QueryZreadTrending(week string, limit int) ([]model.ZreadTrending, error)

	// LookupZreadWikiID 反查 zread wiki_id（wiki-api 复用）
	LookupZreadWikiID(owner, name string) (string, error)

	// Close 关闭数据库连接
	Close() error
}
