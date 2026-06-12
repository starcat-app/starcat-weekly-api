// Package store 定义数据存储接口
package store

import (
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// Store 数据存储接口，便于 mock 测试和未来切换存储后端
type Store interface {
	// --- R-04 聚合主表 ---

	UpsertGitHubRepo(repo model.GitHubRepo) error
	GetGitHubRepoByOwnerName(owner, name string) (*model.GitHubRepo, error)
	MarkGitHubRepoUnavailable(owner, name, message string, now time.Time) error
	AttachWeeklyEvent(repoID int64, project model.Project, issue model.WeeklyIssue) error
	AttachZreadEvent(repoID int64, event model.ZreadTrending) error
	AttachDiscoveryEvent(repoID int64, submission model.DiscoverySubmission) error
	QueryRepos(params model.RepoQuery) ([]model.RepoFeedItem, int, error)
	GetRepoDetail(repoID int64) (*model.RepoDetail, error)
	GetAggregatedLanguages() ([]model.LanguageAggregate, error)
	RebuildAggregates() error
	GetAllSourceRepos() []string

	// --- 旧内部适配方法：公开旧接口会删除，保留是为了降低测试与辅助代码迁移成本 ---

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

	// GetZreadRepos 获取所有 zread trending 的 owner/repo 列表（用于 wiki 预热）
	GetZreadRepos() []string

	// GetUnenrichedZreadRepos 获取未补全 GitHub 元数据的 zread repos
	GetUnenrichedZreadRepos(limit int) ([]model.ZreadTrending, error)

	// UpdateZreadEnriched 更新单条 zread 记录的 GitHub 元数据
	UpdateZreadEnriched(owner, name, weekStart string, z *model.ZreadTrending) error

	// --- AI Discovery（Show HN）v1.2：仅 enrichment 阶段 ---
	UpsertDiscoverySubmission(submission model.DiscoverySubmission) error
	GetDiscoveryEnrichmentCandidates(limit int, now time.Time) ([]model.DiscoveryRepo, error)
	UpdateDiscoveryEnriched(repo model.DiscoveryRepo, now time.Time) error
	UpdateDiscoveryEnrichmentFailure(owner, repo, message string, nextRetryAt time.Time) error
	MarkDiscoveryUnavailable(owner, repo, message string, now time.Time) error
	QueryDiscovery(params model.DiscoveryQuery) ([]model.DiscoveryItemDTO, int, error)
	GetDiscoveryByOwnerRepo(owner, repo string) (*model.DiscoveryItemDTO, error)

	// Close 关闭数据库连接
	Close() error
}
