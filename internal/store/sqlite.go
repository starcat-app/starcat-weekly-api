// Package store implements the SQLite persistence layer.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dong4j/starcat-weekly-api/internal/model"
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

// createSchema creates the R-04 aggregate schema from scratch.
//
// Starcat has not shipped yet, so the service intentionally does not keep
// migrations or old-table compatibility. Existing local *.db files should be
// deleted before running this version.
func (s *SQLiteStore) createSchema() error {
	log.Println("[store] createSchema: github_repos + weekly_extras + zread_events + discovery_submissions")
	_, err := s.db.Exec(`
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
			parsed_at    TEXT NOT NULL
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
	`)
	return err
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) UpsertGitHubRepo(repo model.GitHubRepo) error {
	now := time.Now().UTC()
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

	_, err := s.db.Exec(`
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
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)

	if issue.PublishedAt.IsZero() {
		issue.PublishedAt = time.Now().UTC()
	}
	if issue.ParsedAt.IsZero() {
		issue.ParsedAt = time.Now().UTC()
	}
	if _, err := tx.Exec(`
		INSERT INTO weekly_issues(number, published_at, source_url, parsed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(number) DO UPDATE SET
			published_at=excluded.published_at,
			source_url=excluded.source_url,
			parsed_at=excluded.parsed_at
	`, issue.Number, issue.PublishedAt.UTC().Format(time.RFC3339), issue.SourceURL, issue.ParsedAt.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO weekly_extras(gh_repo_id, first_issue_number, issue_url, recommendation, parsed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(gh_repo_id) DO UPDATE SET
			first_issue_number = CASE
				WHEN excluded.first_issue_number < weekly_extras.first_issue_number THEN excluded.first_issue_number
				ELSE weekly_extras.first_issue_number
			END,
			issue_url = CASE
				WHEN excluded.first_issue_number < weekly_extras.first_issue_number THEN excluded.issue_url
				ELSE weekly_extras.issue_url
			END,
			recommendation = COALESCE(NULLIF(weekly_extras.recommendation, ''), excluded.recommendation),
			parsed_at=excluded.parsed_at
	`, repoID, project.FirstIssueNumber, project.IssueURL, project.Description, issue.ParsedAt.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err := recomputeAggregateTx(tx, repoID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) AttachZreadEvent(repoID int64, event model.ZreadTrending) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(`
		INSERT INTO zread_events (
			gh_repo_id, week_start, week_end, week_label, rank_in_week, description_zh,
			zread_repo_id, wiki_id, zread_week_start_raw, zread_week_end_raw,
			zread_year_inferred, fetched_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gh_repo_id, week_start) DO UPDATE SET
			week_end=excluded.week_end,
			week_label=excluded.week_label,
			rank_in_week=excluded.rank_in_week,
			description_zh=excluded.description_zh,
			zread_repo_id=excluded.zread_repo_id,
			wiki_id=excluded.wiki_id,
			zread_week_start_raw=excluded.zread_week_start_raw,
			zread_week_end_raw=excluded.zread_week_end_raw,
			zread_year_inferred=excluded.zread_year_inferred,
			fetched_at=excluded.fetched_at
	`, repoID, event.WeekStart, nullString(event.WeekEnd), nullString(event.WeekLabel), event.RankInWeek,
		nullString(event.DescriptionZh), nullString(event.RepoID), nullString(event.WikiID),
		nullString(event.ZreadWeekStartRaw), nullString(event.ZreadWeekEndRaw), event.ZreadYearInferred, event.FetchedAt); err != nil {
		return err
	}
	if err := recomputeAggregateTx(tx, repoID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) AttachDiscoveryEvent(repoID int64, sub model.DiscoverySubmission) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.Exec(`
		INSERT INTO discovery_submissions(
			hn_id, gh_repo_id, title, hn_url, source_url, score, comments,
			published_at, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hn_id) DO UPDATE SET
			gh_repo_id=excluded.gh_repo_id,
			title=excluded.title,
			hn_url=excluded.hn_url,
			source_url=excluded.source_url,
			score=excluded.score,
			comments=excluded.comments,
			published_at=excluded.published_at,
			last_seen_at=excluded.last_seen_at
	`, sub.HNID, repoID, sub.Title, sub.HNURL, nullString(sub.SourceURL), sub.Score, sub.Comments,
		sub.PublishedAt.UTC().Format(time.RFC3339), sub.FirstSeenAt.UTC().Format(time.RFC3339), sub.LastSeenAt.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err := recomputeAggregateTx(tx, repoID); err != nil {
		return err
	}
	return tx.Commit()
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
//   - 只取 is_available=1 + 至少一个源（weekly / zread / discovery）的 repo
//   - ORDER BY latest_event_at DESC, gh_repo_id DESC（与 QueryRepos 默认一致）
//   - 不接受任何过滤参数（客户端拿到全量后本地做 source/lang/sort 过滤）
//   - feedItem 仍按 repo 一条条拼（每条 repo 内含 weekly/zread/discovery 三快照 N+1
//     查询），4000 条 repos × 3 表查询 ≈ 12000 次 SQLite 调用；现网测试 ~50ms 量级
//     可接受（bulk endpoint 60s 缓存兜底，并发并不会让查询打爆）
func (s *SQLiteStore) QueryAllRepos() ([]model.RepoFeedItem, error) {
	rows, err := s.db.Query(`SELECT ` + githubRepoColumns() + ` FROM github_repos gr WHERE gr.is_available=1 AND ` + hasAnySourceSQL() + ` ORDER BY gr.latest_event_at DESC, gr.gh_repo_id DESC`)
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

func (s *SQLiteStore) GetAggregatedLanguages() ([]model.LanguageAggregate, error) {
	rows, err := s.db.Query(`
		SELECT
			CASE WHEN language IS NULL OR language='' THEN ? ELSE language END AS key,
			COUNT(*) AS count
		FROM github_repos gr
		WHERE is_available=1 AND `+hasAnySourceSQL()+`
		GROUP BY key
		ORDER BY key=? ASC, count DESC, key ASC
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
		INSERT INTO weekly_issues(number, published_at, source_url, parsed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(number) DO UPDATE SET
			published_at=excluded.published_at,
			source_url=excluded.source_url,
			parsed_at=excluded.parsed_at
	`, issue.Number, issue.PublishedAt.UTC().Format(time.RFC3339), issue.SourceURL, issue.ParsedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) GetIssues() ([]model.WeeklyIssue, error) {
	rows, err := s.db.Query(`SELECT number, published_at, source_url, parsed_at FROM weekly_issues ORDER BY number DESC`)
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
	row := s.db.QueryRow(`SELECT number, published_at, source_url, parsed_at FROM weekly_issues WHERE number=?`, number)
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
		SELECT z.week_label, z.week_start, z.week_end, z.rank_in_week, z.zread_repo_id,
		       gr.owner, gr.name, gr.description, z.description_zh, gr.stars, gr.language,
		       z.wiki_id, gr.gh_repo_id, gr.forks, gr.open_issues, gr.watchers, gr.subscribers,
		       gr.pushed_at, gr.updated_at, gr.created_at, gr.license_spdx, gr.default_branch,
		       gr.is_archived, gr.is_fork, z.zread_week_start_raw, z.zread_week_end_raw,
		       z.zread_year_inferred, z.fetched_at
		FROM zread_events z JOIN github_repos gr ON gr.gh_repo_id=z.gh_repo_id
		ORDER BY z.week_start DESC, z.rank_in_week ASC LIMIT ?`, limit)
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
		SELECT z.wiki_id
		FROM zread_events z JOIN github_repos gr ON gr.gh_repo_id=z.gh_repo_id
		WHERE lower(gr.owner)=lower(?) AND lower(gr.name)=lower(?) AND z.wiki_id IS NOT NULL AND z.wiki_id <> ''
		ORDER BY z.week_start DESC LIMIT 1`, owner, name).Scan(&wiki)
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

// Deprecated discovery compatibility methods.
func (s *SQLiteStore) UpsertDiscoverySubmission(sub model.DiscoverySubmission) error { return nil }
func (s *SQLiteStore) GetDiscoveryEnrichmentCandidates(int, time.Time) ([]model.DiscoveryRepo, error) {
	return nil, nil
}
func (s *SQLiteStore) UpdateDiscoveryEnriched(model.DiscoveryRepo, time.Time) error { return nil }
func (s *SQLiteStore) UpdateDiscoveryEnrichmentFailure(string, string, string, time.Time) error {
	return nil
}
func (s *SQLiteStore) MarkDiscoveryUnavailable(string, string, string, time.Time) error { return nil }
func (s *SQLiteStore) QueryDiscovery(model.DiscoveryQuery) ([]model.DiscoveryItemDTO, int, error) {
	return nil, 0, nil
}
func (s *SQLiteStore) GetDiscoveryByOwnerRepo(string, string) (*model.DiscoveryItemDTO, error) {
	return nil, nil
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
	return model.NewRepoFeedItem(*repo, weekly, zread, discovery), nil
}

func (s *SQLiteStore) weeklySnapshot(repoID int64) (*model.WeeklySnapshot, error) {
	row := s.db.QueryRow(`SELECT first_issue_number, issue_url, recommendation FROM weekly_extras WHERE gh_repo_id=?`, repoID)
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
	row := s.db.QueryRow(`SELECT week_start, week_end, week_label, rank_in_week, description_zh FROM zread_events WHERE gh_repo_id=? ORDER BY week_start DESC, id DESC LIMIT 1`, repoID)
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
	row := s.db.QueryRow(`SELECT hn_id, title, score, comments, published_at FROM discovery_submissions WHERE gh_repo_id=? ORDER BY published_at DESC, hn_id DESC LIMIT 1`, repoID)
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
	events := make([]model.SourceEvent, 0)

	rows, err := s.db.Query(`
		SELECT w.first_issue_number, w.issue_url, w.recommendation, i.published_at
		FROM weekly_extras w JOIN weekly_issues i ON i.number=w.first_issue_number
		WHERE w.gh_repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var issue int
		var url, occurred string
		var rec sql.NullString
		if err := rows.Scan(&issue, &url, &rec, &occurred); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, model.SourceEvent{
			ID: "weekly:" + fmt.Sprint(issue), Source: model.SourceWeekly, OccurredAt: occurred, URL: url,
			Weekly: &model.WeeklyEventPayload{IssueNumber: issue, Recommendation: rec.String},
		})
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT id, week_start, week_end, rank_in_week, description_zh FROM zread_events WHERE gh_repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var weekStart string
		var weekEnd, desc sql.NullString
		var rank int
		if err := rows.Scan(&id, &weekStart, &weekEnd, &rank, &desc); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, model.SourceEvent{
			ID: "zread:" + weekStart, Source: model.SourceZread, OccurredAt: weekStart + "T00:00:00Z",
			Zread: &model.ZreadEventPayload{WeekStart: weekStart, WeekEnd: weekEnd.String, RankInWeek: rank, DescriptionZh: desc.String},
		})
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT hn_id, title, hn_url, score, comments, published_at FROM discovery_submissions WHERE gh_repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hnID int64
		var title, url, occurred string
		var score, comments int
		if err := rows.Scan(&hnID, &title, &url, &score, &comments, &occurred); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, model.SourceEvent{
			ID: "discovery:" + fmt.Sprint(hnID), Source: model.SourceDiscovery, OccurredAt: occurred, URL: url,
			Discovery: &model.DiscoveryEventPayload{HNID: hnID, Title: title, Score: score, Comments: comments},
		})
	}
	rows.Close()

	sort.SliceStable(events, func(i, j int) bool {
		if events[i].OccurredAt == events[j].OccurredAt {
			return events[i].ID > events[j].ID
		}
		return events[i].OccurredAt > events[j].OccurredAt
	})
	return events, nil
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
	for _, source := range params.Source {
		switch source {
		case model.SourceWeekly:
			clauses = append(clauses, "EXISTS (SELECT 1 FROM weekly_extras w WHERE w.gh_repo_id=gr.gh_repo_id)")
		case model.SourceZread:
			clauses = append(clauses, "EXISTS (SELECT 1 FROM zread_events z WHERE z.gh_repo_id=gr.gh_repo_id)")
		case model.SourceDiscovery:
			clauses = append(clauses, "EXISTS (SELECT 1 FROM discovery_submissions d WHERE d.gh_repo_id=gr.gh_repo_id)")
		}
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func hasAnySourceSQL() string {
	return `(EXISTS (SELECT 1 FROM weekly_extras w WHERE w.gh_repo_id=gr.gh_repo_id)
		OR EXISTS (SELECT 1 FROM zread_events z WHERE z.gh_repo_id=gr.gh_repo_id)
		OR EXISTS (SELECT 1 FROM discovery_submissions d WHERE d.gh_repo_id=gr.gh_repo_id))`
}

func repoOrderBy(sortKey, order string) string {
	dir := "DESC"
	if strings.EqualFold(order, "asc") {
		dir = "ASC"
	}
	switch sortKey {
	case "stars":
		return "ORDER BY gr.stars " + dir + ", gr.gh_repo_id DESC"
	case "pushed_at":
		return "ORDER BY gr.pushed_at " + dir + ", gr.gh_repo_id DESC"
	default:
		return "ORDER BY gr.latest_event_at " + dir + ", gr.gh_repo_id DESC"
	}
}

func recomputeAggregateTx(tx *sql.Tx, repoID int64) error {
	sourceTypes := make([]string, 0, 3)
	eventTimes := make([]string, 0)
	var t string
	if err := tx.QueryRow(`
		SELECT i.published_at
		FROM weekly_extras w JOIN weekly_issues i ON i.number=w.first_issue_number
		WHERE w.gh_repo_id=?`, repoID).Scan(&t); err == nil {
		sourceTypes = append(sourceTypes, model.SourceWeekly)
		eventTimes = append(eventTimes, t)
	}
	rows, err := tx.Query(`SELECT week_start || 'T00:00:00Z' FROM zread_events WHERE gh_repo_id=?`, repoID)
	if err != nil {
		return err
	}
	zreadSeen := false
	for rows.Next() {
		zreadSeen = true
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		eventTimes = append(eventTimes, t)
	}
	rows.Close()
	if zreadSeen {
		sourceTypes = append(sourceTypes, model.SourceZread)
	}
	rows, err = tx.Query(`SELECT published_at FROM discovery_submissions WHERE gh_repo_id=?`, repoID)
	if err != nil {
		return err
	}
	discoverySeen := false
	for rows.Next() {
		discoverySeen = true
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		eventTimes = append(eventTimes, t)
	}
	rows.Close()
	if discoverySeen {
		sourceTypes = append(sourceTypes, model.SourceDiscovery)
	}
	if len(eventTimes) == 0 {
		return nil
	}
	sort.Strings(eventTimes)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.Exec(`
		UPDATE github_repos
		SET source_types_json=?, first_event_at=?, latest_event_at=?, record_updated_at=?
		WHERE gh_repo_id=?`,
		model.EncodeStringArray(sourceTypes), eventTimes[0], eventTimes[len(eventTimes)-1], now, repoID)
	return err
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
	if err := scanner.Scan(&issue.Number, &published, &issue.SourceURL, &parsed); err != nil {
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
