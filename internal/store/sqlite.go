// Package store implements the SQLite persistence layer.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/source"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &SQLiteStore{db: db}
	if err := s.createSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("createSchema: %w", err)
	}
	return s, nil
}

// createSchema 先确保当前线上基线表存在，再追加执行版本 migration。
// 旧表暂时保留一个发布窗口作为回滚证据，但新功能不得再以删库重建为升级手段。
func (s *SQLiteStore) createSchema() error {
	log.Println("[store] createSchema: github_repos + weekly_extras + zread_events + discovery_submissions")
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS github_repos (
			gh_repo_id        INTEGER PRIMARY KEY,
			owner             TEXT NOT NULL,
			name              TEXT NOT NULL,
			full_name         TEXT NOT NULL,
			description       TEXT,
			homepage          TEXT,
			language          TEXT,
			stars             INTEGER NOT NULL DEFAULT 0,
			forks             INTEGER NOT NULL DEFAULT 0,
			watchers          INTEGER NOT NULL DEFAULT 0,
			subscribers       INTEGER NOT NULL DEFAULT 0,
			open_issues       INTEGER NOT NULL DEFAULT 0,
			owner_avatar      TEXT,
			default_branch    TEXT,
			license_spdx      TEXT,
			topics_json       TEXT NOT NULL DEFAULT '[]',
			pushed_at         TEXT,
			updated_at        TEXT,
			created_at        TEXT,
			is_archived       INTEGER NOT NULL DEFAULT 0,
			is_fork           INTEGER NOT NULL DEFAULT 0,
			is_private        INTEGER NOT NULL DEFAULT 0,
			source_types_json TEXT NOT NULL DEFAULT '[]',
			first_event_at    TEXT NOT NULL,
			latest_event_at   TEXT NOT NULL,
			enriched_at       TEXT,
			record_updated_at TEXT NOT NULL,
			is_available      INTEGER NOT NULL DEFAULT 1,
			UNIQUE(owner, name)
		);
		CREATE INDEX IF NOT EXISTS idx_github_repos_language ON github_repos(language);
		CREATE INDEX IF NOT EXISTS idx_github_repos_latest_event ON github_repos(latest_event_at DESC, gh_repo_id DESC);
		CREATE INDEX IF NOT EXISTS idx_github_repos_stars ON github_repos(stars DESC, gh_repo_id DESC);
		CREATE INDEX IF NOT EXISTS idx_github_repos_pushed ON github_repos(pushed_at DESC, gh_repo_id DESC);

		CREATE TABLE IF NOT EXISTS weekly_issues (
			number       INTEGER PRIMARY KEY,
			published_at TEXT NOT NULL,
			source_url   TEXT NOT NULL,
			parsed_at    TEXT NOT NULL,
			content_hash TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS weekly_extras (
			gh_repo_id         INTEGER PRIMARY KEY REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE,
			first_issue_number INTEGER NOT NULL REFERENCES weekly_issues(number),
			issue_url          TEXT NOT NULL,
			recommendation     TEXT,
			parsed_at          TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_weekly_extras_issue ON weekly_extras(first_issue_number DESC);

		CREATE TABLE IF NOT EXISTS zread_events (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			gh_repo_id            INTEGER NOT NULL REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE,
			week_start            TEXT NOT NULL,
			week_end              TEXT,
			week_label            TEXT,
			rank_in_week          INTEGER NOT NULL,
			description_zh        TEXT,
			zread_repo_id         TEXT,
			wiki_id               TEXT,
			zread_week_start_raw  TEXT,
			zread_week_end_raw    TEXT,
			zread_year_inferred   INTEGER,
			fetched_at            TEXT NOT NULL,
			UNIQUE(gh_repo_id, week_start)
		);
		CREATE INDEX IF NOT EXISTS idx_zread_events_repo_time ON zread_events(gh_repo_id, week_start DESC);

		CREATE TABLE IF NOT EXISTS discovery_submissions (
			hn_id         INTEGER PRIMARY KEY,
			gh_repo_id    INTEGER NOT NULL REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE,
			title         TEXT NOT NULL,
			hn_url        TEXT NOT NULL,
			source_url    TEXT,
			score         INTEGER NOT NULL DEFAULT 0,
			comments      INTEGER NOT NULL DEFAULT 0,
			published_at  TEXT NOT NULL,
			first_seen_at TEXT NOT NULL,
			last_seen_at  TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_discovery_submissions_repo_time ON discovery_submissions(gh_repo_id, published_at DESC);
		CREATE INDEX IF NOT EXISTS idx_discovery_submissions_time ON discovery_submissions(published_at DESC, hn_id DESC);
	`); err != nil {
		return err
	}
	return s.runMigrations()
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

// HasStartupData 判断当前库是否已有需要保留的同步状态。
//
// 服务启动时只有全新空库才允许自动启动所有 Collector；生产重启或把生产备份
// 恢复到本地时，重复抓取历史数据会制造大量候选并消耗 GitHub 配额。任一来源
// 已有事件、周刊期号或持久化批次，都说明该库不是可安全冷启动的空库。
func (s *SQLiteStore) HasStartupData() (bool, error) {
	var exists bool
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM weekly_issues
			UNION ALL SELECT 1 FROM repo_source_events
			UNION ALL SELECT 1 FROM ingest_batches
		)
	`).Scan(&exists)
	return exists, err
}

func (s *SQLiteStore) UpsertGitHubRepo(repo model.GitHubRepo) error {
	return upsertGitHubRepo(s.db, repo, time.Now().UTC())
}

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertGitHubRepo(executor sqlExecutor, repo model.GitHubRepo, now time.Time) error {
	if repo.FullName == "" {
		repo.FullName = repo.Owner + "/" + repo.Name
	}
	if repo.FirstEventAt.IsZero() {
		repo.FirstEventAt = now
	}
	if repo.LatestEventAt.IsZero() {
		repo.LatestEventAt = repo.FirstEventAt
	}
	repo.RecordUpdated = now
	topics := model.EncodeStringArray(repo.Topics)
	sourceTypes := model.EncodeStringArray(repo.SourceTypes)
	enrichedAt := nullableTime(repo.EnrichedAt)

	_, err := executor.Exec(`
		INSERT INTO github_repos (
			gh_repo_id, owner, name, full_name, description, homepage, language,
			stars, forks, watchers, subscribers, open_issues, owner_avatar,
			default_branch, license_spdx, topics_json, pushed_at, updated_at,
			created_at, is_archived, is_fork, is_private, source_types_json,
			first_event_at, latest_event_at, enriched_at, record_updated_at, is_available
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gh_repo_id) DO UPDATE SET
			owner=excluded.owner,
			name=excluded.name,
			full_name=excluded.full_name,
			description=excluded.description,
			homepage=excluded.homepage,
			language=excluded.language,
			stars=excluded.stars,
			forks=excluded.forks,
			watchers=excluded.watchers,
			subscribers=excluded.subscribers,
			open_issues=excluded.open_issues,
			owner_avatar=excluded.owner_avatar,
			default_branch=excluded.default_branch,
			license_spdx=excluded.license_spdx,
			topics_json=excluded.topics_json,
			pushed_at=excluded.pushed_at,
			updated_at=excluded.updated_at,
			created_at=excluded.created_at,
			is_archived=excluded.is_archived,
			is_fork=excluded.is_fork,
			is_private=excluded.is_private,
			enriched_at=excluded.enriched_at,
			record_updated_at=excluded.record_updated_at,
			is_available=excluded.is_available
	`, repo.GhRepoID, repo.Owner, repo.Name, repo.FullName, nullString(repo.Description),
		nullString(repo.Homepage), nullString(repo.Language), repo.Stars, repo.Forks, repo.Watchers,
		repo.Subscribers, repo.OpenIssues, nullString(repo.OwnerAvatar), nullString(repo.DefaultBranch),
		nullString(repo.LicenseSpdx), topics, nullString(repo.PushedAt), nullString(repo.UpdatedAt),
		nullString(repo.CreatedAt), boolInt(repo.IsArchived), boolInt(repo.IsFork), boolInt(repo.IsPrivate),
		sourceTypes, repo.FirstEventAt.UTC().Format(time.RFC3339), repo.LatestEventAt.UTC().Format(time.RFC3339),
		enrichedAt, repo.RecordUpdated.UTC().Format(time.RFC3339), boolInt(repo.IsAvailable))
	return err
}

func (s *SQLiteStore) GetGitHubRepoByOwnerName(owner, name string) (*model.GitHubRepo, error) {
	row := s.db.QueryRow(`SELECT `+githubRepoColumns()+` FROM github_repos WHERE lower(owner)=lower(?) AND lower(name)=lower(?) LIMIT 1`, owner, name)
	repo, err := scanGitHubRepo(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return repo, err
}

func (s *SQLiteStore) MarkGitHubRepoUnavailable(owner, name, _ string, now time.Time) error {
	_, err := s.db.Exec(`
		UPDATE github_repos
		SET is_available=0, enriched_at=?, record_updated_at=?
		WHERE lower(owner)=lower(?) AND lower(name)=lower(?)
	`, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), owner, name)
	return err
}

func (s *SQLiteStore) AttachWeeklyEvent(repoID int64, project model.Project, issue model.WeeklyIssue) error {
	if issue.PublishedAt.IsZero() {
		issue.PublishedAt = time.Now().UTC()
	}
	issueNumber := project.FirstIssueNumber
	if issueNumber == 0 {
		issueNumber = issue.Number
	}
	sourceURL := project.IssueURL
	if sourceURL == "" {
		sourceURL = issue.SourceURL
	}
	return s.UpsertSourceEvent(repoID, model.SourceEventInput{
		SourceCode: model.SourceWeekly, ExternalKey: fmt.Sprintf("issue:%d:%d", issueNumber, repoID),
		OccurredAt: issue.PublishedAt, SourceURL: sourceURL, Summary: project.Description,
		Payload: map[string]any{"issue_number": issueNumber},
	})
}

func (s *SQLiteStore) AttachZreadEvent(repoID int64, event model.ZreadTrending) error {
	occurredAt, _ := time.Parse("2006-01-02", event.WeekStart)
	if occurredAt.IsZero() {
		occurredAt = parseTime(event.FetchedAt)
	}
	rank := event.RankInWeek
	return s.UpsertSourceEvent(repoID, model.SourceEventInput{
		SourceCode: model.SourceZread, ExternalKey: fmt.Sprintf("week:%s:%d", event.WeekStart, repoID),
		OccurredAt: occurredAt, Title: event.WeekLabel, Summary: event.DescriptionZh, Rank: &rank,
		Payload: map[string]any{
			"week_start": event.WeekStart, "week_end": event.WeekEnd, "zread_repo_id": event.RepoID,
			"wiki_id": event.WikiID, "zread_year_inferred": event.ZreadYearInferred,
			"zread_week_start_raw": event.ZreadWeekStartRaw, "zread_week_end_raw": event.ZreadWeekEndRaw,
		},
	})
}

func (s *SQLiteStore) AttachDiscoveryEvent(repoID int64, sub model.DiscoverySubmission) error {
	return s.UpsertSourceEvent(repoID, model.SourceEventInput{
		SourceCode: model.SourceDiscovery, ExternalKey: fmt.Sprintf("hn:%d:%s/%s", sub.HNID, strings.ToLower(sub.Owner), strings.ToLower(sub.Repo)),
		OccurredAt: sub.PublishedAt, SourceURL: sub.HNURL, Title: sub.Title,
		Payload: map[string]any{
			"hn_id": sub.HNID, "score": sub.Score, "comments": sub.Comments,
			"github_source_url": sub.SourceURL,
		},
	})
}

// UpsertSourceEvent 是所有来源事实的唯一写入口。
// 写事件与重建 repo 聚合字段处于同一短事务，调用方不得在该事务中执行网络请求。
func (s *SQLiteStore) UpsertSourceEvent(repoID int64, event model.SourceEventInput) error {
	if _, ok := source.Find(event.SourceCode); !ok {
		return fmt.Errorf("unknown source_code %q", event.SourceCode)
	}
	if strings.TrimSpace(event.ExternalKey) == "" {
		return fmt.Errorf("external_key is required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := upsertSourceEventTx(tx, repoID, event, now); err != nil {
		return err
	}
	if err := recomputeAggregateTx(tx, repoID); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertSourceEventTx(tx *sql.Tx, repoID int64, event model.SourceEventInput, now time.Time) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("encode source payload: %w", err)
	}
	_, err = tx.Exec(`
		INSERT INTO repo_source_events(
			source_code, external_key, gh_repo_id, occurred_at, source_url,
			title, summary, rank, payload_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_code, external_key) DO UPDATE SET
			gh_repo_id=excluded.gh_repo_id,
			occurred_at=excluded.occurred_at,
			source_url=excluded.source_url,
			title=excluded.title,
			summary=excluded.summary,
			rank=excluded.rank,
			payload_json=excluded.payload_json,
			updated_at=excluded.updated_at
	`, event.SourceCode, event.ExternalKey, repoID, event.OccurredAt.UTC().Format(time.RFC3339),
		nullString(event.SourceURL), nullString(event.Title), nullString(event.Summary), event.Rank,
		string(payload), now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) QueryRepos(params model.RepoQuery) ([]model.RepoFeedItem, int, error) {
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PageSize < 1 {
		params.PageSize = 30
	}
	if params.PageSize > 50 {
		params.PageSize = 50
	}
	where, args := buildRepoWhere(params, true)
	countQuery := `SELECT COUNT(*) FROM github_repos gr ` + where
	var total int
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderBy := repoOrderBy(params.Sort, params.Order)
	args = append(args, params.PageSize, (params.Page-1)*params.PageSize)
	rows, err := s.db.Query(`SELECT `+githubRepoColumns()+` FROM github_repos gr `+where+` `+orderBy+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	repos := make([]model.GitHubRepo, 0)
	for rows.Next() {
		repo, err := scanGitHubRepo(rows)
		if err != nil {
			return nil, 0, err
		}
		repos = append(repos, *repo)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()

	items := make([]model.RepoFeedItem, 0, len(repos))
	for i := range repos {
		repo := repos[i]
		item, err := s.feedItem(&repo)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, nil
}

// QueryAllRepos 返回当前可用的全部 repos（不分页 / 不过滤 / 默认排序）。
//
// R-06.3：为 /api/v1/repos/bulk 提供"全量一次性出"的查询路径。约束:
//   - 只取 is_available=1 + 至少一个已启用来源事件的 repo
//   - ORDER BY latest_event_at DESC, gh_repo_id DESC（与 QueryRepos 默认一致）
//   - 不接受任何过滤参数（客户端拿到全量后本地做 source/lang/sort 过滤）
//   - feedItem 按 repo 拼通用来源代表事件、兼容快照和置顶状态；bulk cache 会吸收
//     这条全量查询的成本，后续可在性能审查中再合并为单条聚合 SQL
func (s *SQLiteStore) QueryAllRepos() ([]model.RepoFeedItem, error) {
	rows, err := s.db.Query(`SELECT ` + githubRepoColumns() + ` FROM github_repos gr WHERE gr.is_available=1 AND ` + hasAnySourceSQL() + ` ` + repoOrderBy("latest_event_at", "desc"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	repos := make([]model.GitHubRepo, 0)
	for rows.Next() {
		repo, err := scanGitHubRepo(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, *repo)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	items := make([]model.RepoFeedItem, 0, len(repos))
	for i := range repos {
		repo := repos[i]
		item, err := s.feedItem(&repo)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *SQLiteStore) GetRepoDetail(repoID int64) (*model.RepoDetail, error) {
	row := s.db.QueryRow(`SELECT `+githubRepoColumns()+` FROM github_repos gr WHERE gr.gh_repo_id=? AND `+hasAnySourceSQL(), repoID)
	repo, err := scanGitHubRepo(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item, err := s.feedItem(repo)
	if err != nil {
		return nil, err
	}
	events, err := s.sourceEvents(repoID)
	if err != nil {
		return nil, err
	}
	return &model.RepoDetail{Repo: item, Events: events}, nil
}

// GetAggregatedLanguages 聚合全部已启用 Weekly 来源 repo 的语言列表,供客户端 picker / sidebar 使用。
//
// 排序约定（dong4j 2026-06-16 调整 — 与 trending 后端同款）:
//
//  1. **未分类（__uncategorized__）排在第 1 位**;
//  2. 其它语言按 count DESC;
//  3. count 相同时按 key ASC（保证响应稳定）。
//
// 客户端会在前面 prepend `""` 哨兵作为「全部」选项,所以最终 picker 顺序是:
// 全部 → 未分类 → count 最多的语言 → ... → count 最少且 key 字典序最大的语言。
//
// 历史:之前是「未分类排最后」(`ORDER BY key=? ASC` 把 1 放后面),dong4j 反馈这种放法
// 用户找「未分类」要滚到底太吃力,改成放在前列让用户一眼能看到这个特殊选项。
func (s *SQLiteStore) GetAggregatedLanguages() ([]model.LanguageAggregate, error) {
	rows, err := s.db.Query(`
		SELECT
			CASE WHEN language IS NULL OR language='' THEN ? ELSE language END AS key,
			COUNT(*) AS count
		FROM github_repos gr
		WHERE is_available=1 AND `+hasAnySourceSQL()+`
		GROUP BY key
		ORDER BY (key=?) DESC, count DESC, key ASC
	`, model.UncategorizedLanguageKey, model.UncategorizedLanguageKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.LanguageAggregate
	for rows.Next() {
		var item model.LanguageAggregate
		if err := rows.Scan(&item.Key, &item.Count); err != nil {
			return nil, err
		}
		item.Label = item.Key
		if item.Key == model.UncategorizedLanguageKey {
			item.Label = model.UncategorizedLanguageLabel
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetSourceCatalog 返回已启用且至少存在一条公开仓库事件的来源目录。
func (s *SQLiteStore) GetSourceCatalog() ([]model.SourceDescriptor, error) {
	rows, err := s.db.Query(`
		SELECT sc.code, sc.display_name_zh, sc.display_name_en, sc.icon_key,
		       sc.sort_order, COUNT(DISTINCT e.gh_repo_id)
		FROM source_catalog sc
		JOIN repo_source_events e ON e.source_code=sc.code
		JOIN github_repos gr ON gr.gh_repo_id=e.gh_repo_id AND gr.is_available=1
		WHERE sc.enabled=1
		GROUP BY sc.code, sc.display_name_zh, sc.display_name_en, sc.icon_key, sc.sort_order
		HAVING COUNT(DISTINCT e.gh_repo_id) > 0
		ORDER BY sc.sort_order ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.SourceDescriptor
	for rows.Next() {
		var item model.SourceDescriptor
		if err := rows.Scan(&item.Code, &item.DisplayNameZH, &item.DisplayNameEN, &item.IconKey, &item.SortOrder, &item.Count); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) RebuildAggregates() error {
	rows, err := s.db.Query(`SELECT gh_repo_id FROM github_repos`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var repoID int64
		if err := rows.Scan(&repoID); err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if err := recomputeAggregateTx(tx, repoID); err != nil {
			rollback(tx)
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *SQLiteStore) GetAllSourceRepos() []string {
	rows, err := s.db.Query(`SELECT owner, name FROM github_repos WHERE ` + hasAnySourceSQL())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var owner, name string
		if rows.Scan(&owner, &name) == nil {
			result = append(result, owner+"/"+name)
		}
	}
	return result
}

// ---- Compatibility methods retained for old helpers/tests while public old routes are removed. ----

func (s *SQLiteStore) UpsertProject(*model.Project) error { return nil }

func (s *SQLiteStore) GetProjects(params model.QueryParams) ([]model.Project, int, error) {
	items, total, err := s.QueryRepos(model.RepoQuery{Page: params.Page, PageSize: params.PageSize, Language: params.Language, Sort: "latest_event_at", Order: "desc"})
	if err != nil {
		return nil, 0, err
	}
	projects := make([]model.Project, 0, len(items))
	for _, item := range items {
		projects = append(projects, model.Project{
			RepoOwner:     item.Owner,
			RepoName:      item.Repo,
			URL:           stringValue(item.HTMLURL),
			Description:   stringValue(item.Description),
			Stars:         item.Stars,
			Language:      stringValue(item.Language),
			GhRepoID:      item.GhRepoID,
			Forks:         item.Forks,
			Watchers:      item.Watchers,
			Subscribers:   item.Subscribers,
			OwnerAvatar:   stringValue(item.OwnerAvatar),
			Homepage:      stringValue(item.Homepage),
			LicenseSpdx:   stringValue(item.LicenseSpdx),
			IsArchived:    item.IsArchived,
			IsFork:        item.IsFork,
			IsPrivate:     item.IsPrivate,
			DefaultBranch: stringValue(item.DefaultBranch),
			OpenIssues:    item.OpenIssues,
			PushedAt:      stringValue(item.PushedAt),
			UpdatedAt:     stringValue(item.UpdatedAt),
			CreatedAt:     stringValue(item.CreatedAt),
			IsAvailable:   item.IsAvailable,
		})
		if item.Weekly != nil {
			projects[len(projects)-1].FirstIssueNumber = item.Weekly.IssueNumber
			projects[len(projects)-1].IssueURL = item.Weekly.IssueURL
		}
	}
	return projects, total, nil
}

func (s *SQLiteStore) UpsertIssue(issue *model.WeeklyIssue) error {
	if issue.PublishedAt.IsZero() {
		issue.PublishedAt = time.Now().UTC()
	}
	if issue.ParsedAt.IsZero() {
		issue.ParsedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO weekly_issues(number, published_at, source_url, parsed_at, content_hash)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(number) DO UPDATE SET
			published_at=excluded.published_at,
			source_url=excluded.source_url,
			parsed_at=excluded.parsed_at,
			content_hash=excluded.content_hash
	`, issue.Number, issue.PublishedAt.UTC().Format(time.RFC3339), issue.SourceURL, issue.ParsedAt.UTC().Format(time.RFC3339), issue.ContentHash)
	return err
}

func (s *SQLiteStore) GetIssues() ([]model.WeeklyIssue, error) {
	rows, err := s.db.Query(`SELECT number, published_at, source_url, parsed_at, content_hash FROM weekly_issues ORDER BY number DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var issues []model.WeeklyIssue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

func (s *SQLiteStore) GetIssue(number int) (*model.WeeklyIssue, error) {
	row := s.db.QueryRow(`SELECT number, published_at, source_url, parsed_at, content_hash FROM weekly_issues WHERE number=?`, number)
	issue, err := scanIssue(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &issue, err
}

func (s *SQLiteStore) GetLatestIssueNumber() (int, error) {
	var number sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(number) FROM weekly_issues`).Scan(&number)
	if err != nil || !number.Valid {
		return 0, err
	}
	return int(number.Int64), nil
}

func (s *SQLiteStore) GetUnenrichedProjects(int) ([]model.Project, error) { return nil, nil }
func (s *SQLiteStore) UpdateProjectMeta(*model.Project) error             { return nil }

func (s *SQLiteStore) GetProjectByOwnerRepo(owner, repo string) (*model.Project, error) {
	g, err := s.GetGitHubRepoByOwnerName(owner, repo)
	if err != nil || g == nil {
		return nil, err
	}
	p := &model.Project{
		RepoOwner: owner, RepoName: repo, URL: "https://github.com/" + owner + "/" + repo,
		Description: g.Description, Stars: g.Stars, Language: g.Language, GhRepoID: g.GhRepoID,
		Forks: g.Forks, Watchers: g.Watchers, Subscribers: g.Subscribers, OwnerAvatar: g.OwnerAvatar,
		Homepage: g.Homepage, LicenseSpdx: g.LicenseSpdx, IsArchived: g.IsArchived, IsFork: g.IsFork,
		IsPrivate: g.IsPrivate, DefaultBranch: g.DefaultBranch, OpenIssues: g.OpenIssues,
		PushedAt: g.PushedAt, UpdatedAt: g.UpdatedAt, CreatedAt: g.CreatedAt, IsAvailable: g.IsAvailable,
	}
	if weekly, _ := s.weeklySnapshot(g.GhRepoID); weekly != nil {
		p.FirstIssueNumber = weekly.IssueNumber
		p.IssueURL = weekly.IssueURL
	}
	return p, nil
}

func (s *SQLiteStore) UpsertZreadTrending(z model.ZreadTrending) error { return nil }
func (s *SQLiteStore) QueryZreadTrending(week string, limit int) ([]model.ZreadTrending, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(e.title, ''), COALESCE(json_extract(e.payload_json, '$.week_start'), ''),
		       COALESCE(json_extract(e.payload_json, '$.week_end'), ''), COALESCE(e.rank, 0),
		       COALESCE(json_extract(e.payload_json, '$.zread_repo_id'), ''),
		       gr.owner, gr.name, gr.description, COALESCE(e.summary, ''), gr.stars, gr.language,
		       COALESCE(json_extract(e.payload_json, '$.wiki_id'), ''), gr.gh_repo_id,
		       gr.forks, gr.open_issues, gr.watchers, gr.subscribers,
		       gr.pushed_at, gr.updated_at, gr.created_at, gr.license_spdx, gr.default_branch,
		       gr.is_archived, gr.is_fork,
		       COALESCE(json_extract(e.payload_json, '$.zread_week_start_raw'), ''),
		       COALESCE(json_extract(e.payload_json, '$.zread_week_end_raw'), ''),
		       COALESCE(json_extract(e.payload_json, '$.zread_year_inferred'), 0), e.updated_at
		FROM repo_source_events e
		JOIN github_repos gr ON gr.gh_repo_id=e.gh_repo_id
		WHERE e.source_code=?
		ORDER BY e.occurred_at DESC, e.rank ASC LIMIT ?`, model.SourceZread, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []model.ZreadTrending
	for rows.Next() {
		var z model.ZreadTrending
		var desc, lang, wiki, pushed, updated, created, license, branch sql.NullString
		var archived, fork int
		if err := rows.Scan(&z.WeekLabel, &z.WeekStart, &z.WeekEnd, &z.RankInWeek, &z.RepoID,
			&z.Owner, &z.Name, &desc, &z.DescriptionZh, &z.StarCount, &lang, &wiki, &z.GhRepoID,
			&z.Forks, &z.OpenIssues, &z.Watchers, &z.SubscribersCount, &pushed, &updated, &created,
			&license, &branch, &archived, &fork, &z.ZreadWeekStartRaw, &z.ZreadWeekEndRaw,
			&z.ZreadYearInferred, &z.FetchedAt); err != nil {
			return nil, err
		}
		z.Description = desc.String
		z.Language = lang.String
		z.WikiID = wiki.String
		z.PushedAt = pushed.String
		z.UpdatedAt = updated.String
		z.CreatedAt = created.String
		z.LicenseSpdx = license.String
		z.DefaultBranch = branch.String
		z.IsArchived = archived == 1
		z.IsFork = fork == 1
		z.HTMLURL = "https://github.com/" + z.Owner + "/" + z.Name
		items = append(items, z)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) LookupZreadWikiID(owner, name string) (string, error) {
	var wiki sql.NullString
	err := s.db.QueryRow(`
		SELECT json_extract(e.payload_json, '$.wiki_id')
		FROM repo_source_events e JOIN github_repos gr ON gr.gh_repo_id=e.gh_repo_id
		WHERE e.source_code=? AND lower(gr.owner)=lower(?) AND lower(gr.name)=lower(?)
		  AND COALESCE(json_extract(e.payload_json, '$.wiki_id'), '') <> ''
		ORDER BY e.occurred_at DESC, e.id DESC LIMIT 1`, model.SourceZread, owner, name).Scan(&wiki)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return wiki.String, nil
}

func (s *SQLiteStore) GetZreadRepos() []string { return s.GetAllSourceRepos() }
func (s *SQLiteStore) GetUnenrichedZreadRepos(int) ([]model.ZreadTrending, error) {
	return nil, nil
}
func (s *SQLiteStore) UpdateZreadEnriched(string, string, string, *model.ZreadTrending) error {
	return nil
}

// ---- Query helpers ----

type rowScanner interface {
	Scan(dest ...any) error
}

func githubRepoColumns() string {
	return `gh_repo_id, owner, name, full_name, description, homepage, language, stars, forks,
		watchers, subscribers, open_issues, owner_avatar, default_branch, license_spdx,
		topics_json, pushed_at, updated_at, created_at, is_archived, is_fork, is_private,
		source_types_json, first_event_at, latest_event_at, enriched_at, record_updated_at, is_available`
}

func scanGitHubRepo(scanner rowScanner) (*model.GitHubRepo, error) {
	var r model.GitHubRepo
	var desc, homepage, lang, avatar, branch, license, topics, pushed, updated, created, sources, enriched sql.NullString
	var firstEvent, latestEvent, recordUpdated string
	var archived, fork, private, available int
	if err := scanner.Scan(&r.GhRepoID, &r.Owner, &r.Name, &r.FullName, &desc, &homepage, &lang,
		&r.Stars, &r.Forks, &r.Watchers, &r.Subscribers, &r.OpenIssues, &avatar, &branch,
		&license, &topics, &pushed, &updated, &created, &archived, &fork, &private, &sources,
		&firstEvent, &latestEvent, &enriched, &recordUpdated, &available); err != nil {
		return nil, err
	}
	r.Description = desc.String
	r.Homepage = homepage.String
	r.Language = lang.String
	r.OwnerAvatar = avatar.String
	r.DefaultBranch = branch.String
	r.LicenseSpdx = license.String
	r.Topics = model.DecodeStringArray(topics.String)
	r.PushedAt = pushed.String
	r.UpdatedAt = updated.String
	r.CreatedAt = created.String
	r.IsArchived = archived == 1
	r.IsFork = fork == 1
	r.IsPrivate = private == 1
	r.IsAvailable = available == 1
	r.SourceTypes = model.DecodeStringArray(sources.String)
	r.FirstEventAt = parseTime(firstEvent)
	r.LatestEventAt = parseTime(latestEvent)
	if enriched.Valid {
		t := parseTime(enriched.String)
		r.EnrichedAt = &t
	}
	r.RecordUpdated = parseTime(recordUpdated)
	return &r, nil
}

func (s *SQLiteStore) feedItem(repo *model.GitHubRepo) (model.RepoFeedItem, error) {
	entries, err := s.latestSourceEntries(repo.GhRepoID)
	if err != nil {
		return model.RepoFeedItem{}, err
	}
	weekly, err := s.weeklySnapshot(repo.GhRepoID)
	if err != nil {
		return model.RepoFeedItem{}, err
	}
	zread, err := s.zreadSnapshot(repo.GhRepoID)
	if err != nil {
		return model.RepoFeedItem{}, err
	}
	discovery, err := s.discoverySnapshot(repo.GhRepoID)
	if err != nil {
		return model.RepoFeedItem{}, err
	}
	pinPosition, err := s.pinPosition(repo.GhRepoID)
	if err != nil {
		return model.RepoFeedItem{}, err
	}
	return model.NewRepoFeedItem(*repo, weekly, zread, discovery, entries, pinPosition), nil
}

func (s *SQLiteStore) weeklySnapshot(repoID int64) (*model.WeeklySnapshot, error) {
	row := s.db.QueryRow(`
		SELECT CAST(json_extract(payload_json, '$.issue_number') AS INTEGER), source_url, summary
		FROM repo_source_events
		WHERE gh_repo_id=? AND source_code=? AND json_extract(payload_json, '$.issue_number') IS NOT NULL
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, repoID, model.SourceWeekly)
	var item model.WeeklySnapshot
	var rec sql.NullString
	if err := row.Scan(&item.IssueNumber, &item.IssueURL, &rec); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	item.Recommendation = rec.String
	return &item, nil
}

func (s *SQLiteStore) zreadSnapshot(repoID int64) (*model.ZreadSnapshot, error) {
	row := s.db.QueryRow(`
		SELECT json_extract(payload_json, '$.week_start'), json_extract(payload_json, '$.week_end'),
		       title, COALESCE(rank, 0), summary
		FROM repo_source_events
		WHERE gh_repo_id=? AND source_code=? AND json_extract(payload_json, '$.week_start') IS NOT NULL
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, repoID, model.SourceZread)
	var item model.ZreadSnapshot
	var end, label, desc sql.NullString
	if err := row.Scan(&item.WeekStart, &end, &label, &item.RankInWeek, &desc); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	item.WeekEnd = end.String
	item.WeekLabel = label.String
	item.DescriptionZh = desc.String
	return &item, nil
}

func (s *SQLiteStore) discoverySnapshot(repoID int64) (*model.DiscoverySnapshot, error) {
	row := s.db.QueryRow(`
		SELECT CAST(json_extract(payload_json, '$.hn_id') AS INTEGER), title,
		       CAST(json_extract(payload_json, '$.score') AS INTEGER),
		       CAST(json_extract(payload_json, '$.comments') AS INTEGER), occurred_at
		FROM repo_source_events
		WHERE gh_repo_id=? AND source_code=? AND json_extract(payload_json, '$.hn_id') IS NOT NULL
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, repoID, model.SourceDiscovery)
	var item model.DiscoverySnapshot
	if err := row.Scan(&item.HNID, &item.Title, &item.Score, &item.Comments, &item.PublishedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

func (s *SQLiteStore) sourceEvents(repoID int64) ([]model.SourceEvent, error) {
	rows, err := s.db.Query(`
		SELECT id, source_code, occurred_at, source_url, title, summary, rank, payload_json
		FROM repo_source_events WHERE gh_repo_id=?
		ORDER BY occurred_at DESC, id DESC`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]model.SourceEvent, 0)
	for rows.Next() {
		var id int64
		entry, payload, err := scanSourceEntry(rows, &id)
		if err != nil {
			return nil, err
		}
		event := model.SourceEvent{
			ID: fmt.Sprintf("%s:%d", entry.SourceCode, id), Source: entry.SourceCode, SourceCode: entry.SourceCode,
			OccurredAt: entry.OccurredAt, URL: entry.SourceURL, SourceURL: entry.SourceURL,
			Title: entry.Title, Summary: entry.Summary, Rank: entry.Rank, Payload: entry.Payload,
		}
		applyLegacyEventPayload(&event, payload)
		events = append(events, event)
	}
	return events, rows.Err()
}

func buildRepoWhere(params model.RepoQuery, availableOnly bool) (string, []any) {
	clauses := []string{hasAnySourceSQL()}
	args := make([]any, 0)
	if availableOnly {
		clauses = append(clauses, "gr.is_available=1")
	}
	if params.Language != "" {
		if params.Language == model.UncategorizedLanguageKey {
			clauses = append(clauses, "(gr.language IS NULL OR gr.language='')")
		} else {
			clauses = append(clauses, "gr.language=?")
			args = append(args, params.Language)
		}
	}
	for _, sourceCode := range params.Source {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM repo_source_events e JOIN source_catalog sc ON sc.code=e.source_code WHERE e.gh_repo_id=gr.gh_repo_id AND e.source_code=? AND sc.enabled=1)")
		args = append(args, sourceCode)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func hasAnySourceSQL() string {
	return `EXISTS (
		SELECT 1 FROM repo_source_events e
		JOIN source_catalog sc ON sc.code=e.source_code
		WHERE e.gh_repo_id=gr.gh_repo_id AND sc.enabled=1
	)`
}

func repoOrderBy(sortKey, order string) string {
	dir := "DESC"
	if strings.EqualFold(order, "asc") {
		dir = "ASC"
	}
	pinPrefix := `ORDER BY
		CASE WHEN EXISTS (SELECT 1 FROM weekly_pins p WHERE p.gh_repo_id=gr.gh_repo_id) THEN 0 ELSE 1 END ASC,
		COALESCE((SELECT p.position FROM weekly_pins p WHERE p.gh_repo_id=gr.gh_repo_id), 2147483647) ASC, `
	switch sortKey {
	case "stars":
		return pinPrefix + "gr.stars " + dir + ", gr.gh_repo_id DESC"
	case "updated_at":
		return pinPrefix + "gr.updated_at " + dir + ", gr.gh_repo_id DESC"
	case "created_at":
		return pinPrefix + "gr.created_at " + dir + ", gr.gh_repo_id DESC"
	case "name":
		return pinPrefix + "LOWER(gr.full_name) " + dir + ", gr.gh_repo_id DESC"
	default:
		return pinPrefix + "gr.latest_event_at " + dir + ", gr.gh_repo_id DESC"
	}
}

func recomputeAggregateTx(tx *sql.Tx, repoID int64) error {
	rows, err := tx.Query(`
		SELECT DISTINCT e.source_code
		FROM repo_source_events e
		JOIN source_catalog sc ON sc.code=e.source_code
		WHERE e.gh_repo_id=? AND sc.enabled=1
		ORDER BY sc.sort_order`, repoID)
	if err != nil {
		return err
	}
	var sourceTypes []string
	for rows.Next() {
		var sourceCode string
		if err := rows.Scan(&sourceCode); err != nil {
			rows.Close()
			return err
		}
		sourceTypes = append(sourceTypes, sourceCode)
	}
	rows.Close()
	if len(sourceTypes) == 0 {
		return nil
	}
	var firstEvent, latestEvent string
	if err := tx.QueryRow(`SELECT MIN(occurred_at), MAX(occurred_at) FROM repo_source_events WHERE gh_repo_id=?`, repoID).Scan(&firstEvent, &latestEvent); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.Exec(`
		UPDATE github_repos
		SET source_types_json=?, first_event_at=?, latest_event_at=?, record_updated_at=?
		WHERE gh_repo_id=?`,
		model.EncodeStringArray(sourceTypes), firstEvent, latestEvent, now, repoID)
	return err
}

func (s *SQLiteStore) latestSourceEntries(repoID int64) ([]model.SourceEntry, error) {
	rows, err := s.db.Query(`
		SELECT e.source_code, e.occurred_at, e.source_url, e.title, e.summary, e.rank, e.payload_json
		FROM repo_source_events e
		JOIN source_catalog sc ON sc.code=e.source_code AND sc.enabled=1
		WHERE e.gh_repo_id=? AND NOT EXISTS (
			SELECT 1 FROM repo_source_events newer
			WHERE newer.gh_repo_id=e.gh_repo_id AND newer.source_code=e.source_code
			  AND (newer.occurred_at > e.occurred_at OR (newer.occurred_at=e.occurred_at AND newer.id > e.id))
		)
		ORDER BY sc.sort_order`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []model.SourceEntry
	for rows.Next() {
		entry, _, err := scanSourceEntry(rows, nil)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func scanSourceEntry(scanner rowScanner, id *int64) (model.SourceEntry, map[string]any, error) {
	var entry model.SourceEntry
	var sourceURL, title, summary sql.NullString
	var rank sql.NullInt64
	var payloadJSON string
	dest := []any{&entry.SourceCode, &entry.OccurredAt, &sourceURL, &title, &summary, &rank, &payloadJSON}
	if id != nil {
		dest = append([]any{id}, dest...)
	}
	if err := scanner.Scan(dest...); err != nil {
		return entry, nil, err
	}
	entry.SourceURL = sourceURL.String
	entry.Title = title.String
	entry.Summary = summary.String
	if rank.Valid {
		value := int(rank.Int64)
		entry.Rank = &value
	}
	payload := make(map[string]any)
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return entry, nil, fmt.Errorf("decode source payload: %w", err)
	}
	entry.Payload = payload
	return entry, payload, nil
}

func applyLegacyEventPayload(event *model.SourceEvent, payload map[string]any) {
	switch event.SourceCode {
	case model.SourceWeekly:
		event.Weekly = &model.WeeklyEventPayload{IssueNumber: intFromPayload(payload, "issue_number"), Recommendation: event.Summary}
	case model.SourceZread:
		rank := 0
		if event.Rank != nil {
			rank = *event.Rank
		}
		event.Zread = &model.ZreadEventPayload{WeekStart: stringFromPayload(payload, "week_start"), WeekEnd: stringFromPayload(payload, "week_end"), RankInWeek: rank, DescriptionZh: event.Summary}
	case model.SourceDiscovery:
		event.Discovery = &model.DiscoveryEventPayload{HNID: int64(intFromPayload(payload, "hn_id")), Title: event.Title, Score: intFromPayload(payload, "score"), Comments: intFromPayload(payload, "comments")}
	}
}

func intFromPayload(payload map[string]any, key string) int {
	value, _ := payload[key].(float64)
	return int(value)
}

func stringFromPayload(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func (s *SQLiteStore) pinPosition(repoID int64) (*int, error) {
	var position int
	if err := s.db.QueryRow(`SELECT position FROM weekly_pins WHERE gh_repo_id=?`, repoID).Scan(&position); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &position, nil
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func parseTime(raw string) time.Time {
	t, _ := time.Parse(time.RFC3339, raw)
	return t
}

func scanIssue(scanner rowScanner) (model.WeeklyIssue, error) {
	var issue model.WeeklyIssue
	var published, parsed string
	if err := scanner.Scan(&issue.Number, &published, &issue.SourceURL, &parsed, &issue.ContentHash); err != nil {
		return issue, err
	}
	issue.PublishedAt = parseTime(published)
	issue.ParsedAt = parseTime(parsed)
	return issue, nil
}

func prettyJSON(values []string) string {
	data, _ := json.Marshal(values)
	return string(data)
}
