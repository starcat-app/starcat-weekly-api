// Package store 实现基于 SQLite 的数据存储
package store

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// SQLiteStore SQLite 存储实现
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore 创建并初始化 SQLite 存储
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// 连接池配置：SQLite 单写者，连接数收敛为 1
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &SQLiteStore{db: db}
	if err := s.createSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("createSchema: %w", err)
	}
	return s, nil
}

// createSchema 初始化 weekly-api 全部数据表与索引。
//
// 全新服务,无版本迁移:任何现存 *.db 直接 rm 即可。首启动调一次
// CREATE TABLE IF NOT EXISTS 即可,不做 destructive migration。
//
// 五张表:
//   - weekly_issues:阮一峰周刊 issue 元数据(number / published_at / source_url / parsed_at)
//   - projects:周刊内出现的项目(repo_owner + repo_name 唯一),含 enrich 后的 18 个 GitHub 字段
//   - zread_trending:zread 周 trending 独立表(week_start + owner + name 唯一),与 projects 解耦
//   - discovery_repos:Show HN 发现的 GitHub 仓库元数据 + AI 分类状态
//   - discovery_submissions:每次 Show HN 投稿事实；同一 repo 可保留多次投稿
func (s *SQLiteStore) createSchema() error {
	log.Println("[store] createSchema: weekly_issues + projects + zread_trending + discovery")
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS weekly_issues (
			number       INTEGER PRIMARY KEY,
			published_at TEXT,
			source_url   TEXT,
			parsed_at    TEXT
		);

		CREATE TABLE IF NOT EXISTS projects (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_owner          TEXT NOT NULL,
			repo_name           TEXT NOT NULL,
			description         TEXT DEFAULT '',
			stars               INTEGER DEFAULT 0,
			language            TEXT DEFAULT '',
			topics              TEXT DEFAULT '',
			first_issue_number  INTEGER REFERENCES weekly_issues(number),
			enriched_at         TEXT,
			is_available        INTEGER DEFAULT 1,
			gh_repo_id          INTEGER,
			forks               INTEGER DEFAULT 0,
			watchers            INTEGER DEFAULT 0,
			subscribers         INTEGER DEFAULT 0,
			owner_avatar        TEXT,
			homepage            TEXT,
			license_spdx        TEXT,
			is_archived         INTEGER NOT NULL DEFAULT 0,
			is_fork             INTEGER NOT NULL DEFAULT 0,
			is_private          INTEGER NOT NULL DEFAULT 0,
			default_branch      TEXT,
			open_issues         INTEGER DEFAULT 0,
			pushed_at           TEXT,
			updated_at          TEXT,
			created_at          TEXT,
			UNIQUE(repo_owner, repo_name)
		);

		CREATE INDEX IF NOT EXISTS idx_projects_issue      ON projects(first_issue_number DESC);
		CREATE INDEX IF NOT EXISTS idx_projects_lang       ON projects(language);
		CREATE INDEX IF NOT EXISTS idx_projects_gh_repo_id ON projects(gh_repo_id) WHERE gh_repo_id IS NOT NULL;

		CREATE TABLE IF NOT EXISTS zread_trending (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			week_label          TEXT NOT NULL,
			week_start          TEXT NOT NULL,
			week_end            TEXT NOT NULL,
			rank_in_week        INTEGER NOT NULL,
			repo_id             TEXT NOT NULL,
			owner               TEXT NOT NULL,
			name                TEXT NOT NULL,
			html_url            TEXT NOT NULL,
			description         TEXT,
			description_zh      TEXT,
			star_count          INTEGER,
			language            TEXT,
			topics              TEXT,
			wiki_id             TEXT,
			gh_repo_id          INTEGER,
			forks               INTEGER DEFAULT 0,
			open_issues         INTEGER DEFAULT 0,
			watchers            INTEGER DEFAULT 0,
			subscribers_count   INTEGER DEFAULT 0,
			pushed_at           TEXT,
			updated_at          TEXT,
			created_at          TEXT,
			license_spdx        TEXT,
			default_branch      TEXT,
			is_archived         INTEGER DEFAULT 0,
			is_fork             INTEGER DEFAULT 0,
			zread_week_start_raw TEXT,
			zread_week_end_raw   TEXT,
			zread_year_inferred  INTEGER,
			fetched_at          TEXT NOT NULL,
			UNIQUE(week_start, owner, name)
		);

			CREATE INDEX IF NOT EXISTS idx_zread_trending_owner_repo  ON zread_trending(owner, name);
			CREATE INDEX IF NOT EXISTS idx_zread_trending_week       ON zread_trending(week_start DESC);
			CREATE INDEX IF NOT EXISTS idx_zread_trending_wiki       ON zread_trending(wiki_id);
			CREATE INDEX IF NOT EXISTS idx_zread_trending_gh_repo_id ON zread_trending(gh_repo_id);

			CREATE TABLE IF NOT EXISTS discovery_repos (
				owner                       TEXT COLLATE NOCASE NOT NULL,
				repo                        TEXT COLLATE NOCASE NOT NULL,
				gh_repo_id                  INTEGER,
				description                 TEXT,
				homepage                    TEXT,
				language                    TEXT,
				stars                       INTEGER NOT NULL DEFAULT 0,
				forks                       INTEGER NOT NULL DEFAULT 0,
				watchers                    INTEGER NOT NULL DEFAULT 0,
				subscribers                 INTEGER NOT NULL DEFAULT 0,
				open_issues                 INTEGER NOT NULL DEFAULT 0,
				owner_avatar                TEXT,
				default_branch              TEXT,
				license_spdx                TEXT,
				topics_json                 TEXT NOT NULL DEFAULT '[]',
				pushed_at                   TEXT,
				updated_at                  TEXT,
				created_at                  TEXT,
				is_archived                 INTEGER NOT NULL DEFAULT 0,
				is_fork                     INTEGER NOT NULL DEFAULT 0,
				is_private                  INTEGER NOT NULL DEFAULT 0,
				readme_excerpt              TEXT NOT NULL DEFAULT '',
				enrichment_status           TEXT NOT NULL DEFAULT 'pending',
				enrich_attempts             INTEGER NOT NULL DEFAULT 0,
				enrich_next_retry_at        TEXT,
				enrich_error                TEXT,
				enriched_at                 TEXT,
				category                    TEXT NOT NULL DEFAULT 'unknown',
				classify_status             TEXT NOT NULL DEFAULT 'pending',
				classify_confidence         REAL,
				classify_reason             TEXT,
				classify_method             TEXT,
				classify_model              TEXT,
				classify_attempts           INTEGER NOT NULL DEFAULT 0,
				classify_next_retry_at      TEXT,
				classify_error              TEXT,
				classified_at               TEXT,
				first_seen_at               TEXT NOT NULL,
				last_seen_at                TEXT NOT NULL,
				record_updated_at           TEXT NOT NULL,
				PRIMARY KEY (owner, repo)
			);

			CREATE TABLE IF NOT EXISTS discovery_submissions (
				hn_id          INTEGER NOT NULL,
				owner          TEXT COLLATE NOCASE NOT NULL,
				repo           TEXT COLLATE NOCASE NOT NULL,
				title          TEXT NOT NULL,
				hn_url         TEXT NOT NULL,
				source_url     TEXT,
				score          INTEGER NOT NULL DEFAULT 0,
				comments       INTEGER NOT NULL DEFAULT 0,
				published_at   TEXT NOT NULL,
				first_seen_at  TEXT NOT NULL,
				last_seen_at   TEXT NOT NULL,
				PRIMARY KEY (hn_id, owner, repo),
				FOREIGN KEY (owner, repo) REFERENCES discovery_repos(owner, repo)
			);

			CREATE INDEX IF NOT EXISTS idx_discovery_repos_enrichment
				ON discovery_repos(enrichment_status, enrich_next_retry_at);
			CREATE INDEX IF NOT EXISTS idx_discovery_repos_classification
				ON discovery_repos(classify_status, classify_next_retry_at, category);
			CREATE INDEX IF NOT EXISTS idx_discovery_submissions_published
				ON discovery_submissions(published_at DESC, score DESC);
			CREATE INDEX IF NOT EXISTS idx_discovery_submissions_repo
				ON discovery_submissions(owner, repo, published_at DESC);
		`)
	return err
}

// UpsertZreadTrending upsert 一条 zread trending 记录。
//
// 唯一键 (week_start, owner, name) — 同一周同一 repo 多次抓取会更新
// star_count / fetched_at / 推断字段，不会重复插入。
func (s *SQLiteStore) UpsertZreadTrending(z model.ZreadTrending) error {
	_, err := s.db.Exec(`
		INSERT INTO zread_trending
			(week_label, week_start, week_end, rank_in_week, repo_id, owner, name, html_url,
			 description, description_zh, star_count, language, topics, wiki_id,
			 zread_week_start_raw, zread_week_end_raw, zread_year_inferred, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(week_start, owner, name) DO UPDATE SET
			week_label          = excluded.week_label,
			week_end            = excluded.week_end,
			rank_in_week        = excluded.rank_in_week,
			repo_id             = excluded.repo_id,
			html_url            = excluded.html_url,
			description         = excluded.description,
			description_zh      = excluded.description_zh,
			star_count          = excluded.star_count,
			language            = excluded.language,
			topics              = excluded.topics,
			wiki_id             = excluded.wiki_id,
			zread_week_start_raw = excluded.zread_week_start_raw,
			zread_week_end_raw   = excluded.zread_week_end_raw,
			zread_year_inferred  = excluded.zread_year_inferred,
			fetched_at           = excluded.fetched_at
	`,
		z.WeekLabel, z.WeekStart, z.WeekEnd, z.RankInWeek, z.RepoID, z.Owner, z.Name, z.HTMLURL,
		z.Description, z.DescriptionZh, z.StarCount, z.Language, z.Topics, z.WikiID,
		z.ZreadWeekStartRaw, z.ZreadWeekEndRaw, z.ZreadYearInferred, z.FetchedAt,
	)
	return err
}

// QueryZreadTrending 按 week 参数查 zread 周 trending 列表。
//
// week 取值：
//   - "this" / 空：取数据库里 week_start 最大的那一周
//   - "last"：取第二大的 week_start
//   - ISO 8601 日期（"2026-06-08"）：精确匹配 week_start
//
// limit 上限 50。
func (s *SQLiteStore) QueryZreadTrending(week string, limit int) ([]model.ZreadTrending, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	var query string
	var args []any

	switch week {
	case "", "this":
		query = `SELECT week_label, week_start, week_end, rank_in_week, repo_id, owner, name, html_url,
			description, description_zh, star_count, language, topics, wiki_id,
			gh_repo_id, forks, open_issues, watchers, subscribers_count,
			pushed_at, updated_at, created_at, license_spdx, default_branch, is_archived, is_fork,
			zread_week_start_raw, zread_week_end_raw, zread_year_inferred, fetched_at
			FROM zread_trending
			WHERE week_start = (SELECT MAX(week_start) FROM zread_trending)
			ORDER BY rank_in_week ASC LIMIT ?`
		args = []any{limit}
	case "last":
		query = `SELECT week_label, week_start, week_end, rank_in_week, repo_id, owner, name, html_url,
			description, description_zh, star_count, language, topics, wiki_id,
			gh_repo_id, forks, open_issues, watchers, subscribers_count,
			pushed_at, updated_at, created_at, license_spdx, default_branch, is_archived, is_fork,
			zread_week_start_raw, zread_week_end_raw, zread_year_inferred, fetched_at
			FROM zread_trending
			WHERE week_start = (
				SELECT MAX(week_start) FROM zread_trending
				WHERE week_start < (SELECT MAX(week_start) FROM zread_trending)
			)
			ORDER BY rank_in_week ASC LIMIT ?`
		args = []any{limit}
	default:
		// 精确 ISO 8601 日期匹配
		query = `SELECT week_label, week_start, week_end, rank_in_week, repo_id, owner, name, html_url,
			description, description_zh, star_count, language, topics, wiki_id,
			gh_repo_id, forks, open_issues, watchers, subscribers_count,
			pushed_at, updated_at, created_at, license_spdx, default_branch, is_archived, is_fork,
			zread_week_start_raw, zread_week_end_raw, zread_year_inferred, fetched_at
			FROM zread_trending
			WHERE week_start = ?
			ORDER BY rank_in_week ASC LIMIT ?`
		args = []any{week, limit}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanZreadTrending(rows)
}

// LookupZreadWikiID 给定 owner/repo 反查 zread wiki_id。
//
// 用于 wiki-api 在探测到 zread 未索引时，反向 weekly-api 校验"zread 是否曾收录过这个 repo"。
// 返回 "" 表示未收录（不返回 error，调用方用空串判断）。
func (s *SQLiteStore) LookupZreadWikiID(owner, name string) (string, error) {
	var wikiID sql.NullString
	err := s.db.QueryRow(`
		SELECT wiki_id FROM zread_trending
		WHERE owner = ? AND name = ?
		ORDER BY week_start DESC LIMIT 1
	`, owner, name).Scan(&wikiID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return wikiID.String, nil
}

// scanZreadTrending 把 rows 扫描成 []ZreadTrending。
func (s *SQLiteStore) scanZreadTrending(rows *sql.Rows) ([]model.ZreadTrending, error) {
	items := make([]model.ZreadTrending, 0)
	for rows.Next() {
		var z model.ZreadTrending
		var wikiID, lang, desc, descZh, topics, license, branch sql.NullString
		var pushedAt, updatedAt, createdAt sql.NullString
		var ghRepoID sql.NullInt64
		var isArchived, isFork int

		if err := rows.Scan(
			&z.WeekLabel, &z.WeekStart, &z.WeekEnd, &z.RankInWeek, &z.RepoID, &z.Owner, &z.Name, &z.HTMLURL,
			&desc, &descZh, &z.StarCount, &lang, &topics, &wikiID,
			&ghRepoID, &z.Forks, &z.OpenIssues, &z.Watchers, &z.SubscribersCount,
			&pushedAt, &updatedAt, &createdAt, &license, &branch, &isArchived, &isFork,
			&z.ZreadWeekStartRaw, &z.ZreadWeekEndRaw, &z.ZreadYearInferred, &z.FetchedAt,
		); err != nil {
			return nil, fmt.Errorf("scan zread_trending row: %w", err)
		}

		z.Description = desc.String
		z.DescriptionZh = descZh.String
		z.Language = lang.String
		z.Topics = topics.String
		z.WikiID = wikiID.String
		z.LicenseSpdx = license.String
		z.DefaultBranch = branch.String
		z.PushedAt = pushedAt.String
		z.UpdatedAt = updatedAt.String
		z.CreatedAt = createdAt.String
		if ghRepoID.Valid {
			z.GhRepoID = ghRepoID.Int64
		}
		z.IsArchived = isArchived == 1
		z.IsFork = isFork == 1

		items = append(items, z)
	}
	return items, rows.Err()
}

// UpsertProject 插入项目，已存在则忽略（保留最早出现的期号）
func (s *SQLiteStore) UpsertProject(p *model.Project) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO projects
			(repo_owner, repo_name, description, first_issue_number, is_available)
		VALUES (?, ?, ?, ?, ?)
	`, p.RepoOwner, p.RepoName, p.Description, p.FirstIssueNumber, boolToInt(p.IsAvailable))
	return err
}

// GetProjects 分页查询项目
func (s *SQLiteStore) GetProjects(params model.QueryParams) ([]model.Project, int, error) {
	page := params.Page
	pageSize := params.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	where := []string{"is_available = 1"}
	args := []any{}

	// 默认只返回已 enrich 的项目，include_unenriched=true 时不过滤
	if !params.IncludeUnenriched {
		where = append(where, "enriched_at IS NOT NULL")
	}

	if params.Issue == "latest" {
		// 最新一期
		where = append(where, "first_issue_number = (SELECT MAX(number) FROM weekly_issues)")
	} else if params.Issue != "" {
		// 指定期号（尝试解析为数字）
		var num int
		if _, err := fmt.Sscanf(params.Issue, "%d", &num); err == nil {
			where = append(where, "first_issue_number = ?")
			args = append(args, num)
		}
	}

	if params.IssueFrom > 0 {
		where = append(where, "first_issue_number >= ?")
		args = append(args, params.IssueFrom)
	}
	if params.IssueTo > 0 {
		where = append(where, "first_issue_number <= ?")
		args = append(args, params.IssueTo)
	}
	if params.Language != "" {
		where = append(where, "language = ?")
		args = append(args, params.Language)
	}

	whereClause := strings.Join(where, " AND ")

	// 先查总数
	var total int
	countQuery := "SELECT COUNT(*) FROM projects WHERE " + whereClause
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 排序
	order := "first_issue_number DESC"
	switch params.Sort {
	case "stars_desc":
		order = "stars DESC"
	case "first_issue_desc":
		order = "first_issue_number DESC"
	}

	// 分页查询
	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT id, repo_owner, repo_name, description, stars, language, topics,
		       first_issue_number, enriched_at, is_available,
		       gh_repo_id, forks, watchers, subscribers, owner_avatar,
		       homepage, license_spdx, is_archived, is_fork, is_private,
		       default_branch, open_issues, pushed_at, updated_at, created_at
		FROM projects
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, whereClause, order)

	rows, err := s.db.Query(query, append(args, pageSize, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	return s.scanProjects(rows, total)
}

func (s *SQLiteStore) scanProjects(rows *sql.Rows, total int) ([]model.Project, int, error) {
	items := make([]model.Project, 0)
	for rows.Next() {
		var p model.Project
		var enrichedAt sql.NullString
		var ghRepoID sql.NullInt64
		var ownerAvatar, homepage, licenseSpdx, defaultBranch, pushedAt, updatedAt, createdAt sql.NullString
		var isArchived, isFork, isPrivate int

		if err := rows.Scan(&p.ID, &p.RepoOwner, &p.RepoName, &p.Description,
			&p.Stars, &p.Language, &p.Topics, &p.FirstIssueNumber,
			&enrichedAt, &p.IsAvailable,
			&ghRepoID, &p.Forks, &p.Watchers, &p.Subscribers, &ownerAvatar,
			&homepage, &licenseSpdx, &isArchived, &isFork, &isPrivate,
			&defaultBranch, &p.OpenIssues, &pushedAt, &updatedAt, &createdAt); err != nil {
			return nil, 0, err
		}
		p.URL = fmt.Sprintf("https://github.com/%s/%s", p.RepoOwner, p.RepoName)
		p.IssueURL = fmt.Sprintf("https://github.com/ruanyf/weekly/blob/master/docs/issue-%d.md", p.FirstIssueNumber)

		if enrichedAt.Valid {
			t, _ := time.Parse(time.RFC3339, enrichedAt.String)
			p.EnrichedAt = &t
		}
		if ghRepoID.Valid {
			p.GhRepoID = ghRepoID.Int64
		}
		p.OwnerAvatar = ownerAvatar.String
		p.Homepage = homepage.String
		p.LicenseSpdx = licenseSpdx.String
		p.IsArchived = isArchived == 1
		p.IsFork = isFork == 1
		p.IsPrivate = isPrivate == 1
		p.DefaultBranch = defaultBranch.String
		p.PushedAt = pushedAt.String
		p.UpdatedAt = updatedAt.String
		p.CreatedAt = createdAt.String

		items = append(items, p)
	}

	return items, total, nil
}

// GetProjectByOwnerRepo 获取单个项目
func (s *SQLiteStore) GetProjectByOwnerRepo(owner, repo string) (*model.Project, error) {
	query := `
		SELECT id, repo_owner, repo_name, description, stars, language, topics,
		       first_issue_number, enriched_at, is_available,
		       gh_repo_id, forks, watchers, subscribers, owner_avatar,
		       homepage, license_spdx, is_archived, is_fork, is_private,
		       default_branch, open_issues, pushed_at, updated_at, created_at
		FROM projects
		WHERE repo_owner = ? AND repo_name = ?
	`
	rows, err := s.db.Query(query, owner, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items, _, err := s.scanProjects(rows, 0)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

// UpsertIssue 插入或更新周刊
func (s *SQLiteStore) UpsertIssue(issue *model.WeeklyIssue) error {
	_, err := s.db.Exec(`
		INSERT INTO weekly_issues (number, published_at, source_url, parsed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(number) DO UPDATE SET
			published_at = excluded.published_at,
			source_url   = excluded.source_url,
			parsed_at    = excluded.parsed_at
	`, issue.Number, issue.PublishedAt.Format(time.RFC3339), issue.SourceURL, issue.ParsedAt.Format(time.RFC3339))
	return err
}

// GetIssues 列出所有期号
func (s *SQLiteStore) GetIssues() ([]model.WeeklyIssue, error) {
	rows, err := s.db.Query(`SELECT number, published_at, source_url, parsed_at FROM weekly_issues ORDER BY number DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []model.WeeklyIssue
	for rows.Next() {
		var issue model.WeeklyIssue
		var pubStr, srcURL, parsedStr string
		if err := rows.Scan(&issue.Number, &pubStr, &srcURL, &parsedStr); err != nil {
			return nil, err
		}
		issue.PublishedAt, _ = time.Parse(time.RFC3339, pubStr)
		issue.SourceURL = srcURL
		issue.ParsedAt, _ = time.Parse(time.RFC3339, parsedStr)
		issues = append(issues, issue)
	}
	return issues, nil
}

// GetIssue 获取单期
func (s *SQLiteStore) GetIssue(number int) (*model.WeeklyIssue, error) {
	var issue model.WeeklyIssue
	var pubStr, srcURL, parsedStr string
	err := s.db.QueryRow(`SELECT number, published_at, source_url, parsed_at FROM weekly_issues WHERE number = ?`, number).
		Scan(&issue.Number, &pubStr, &srcURL, &parsedStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	issue.PublishedAt, _ = time.Parse(time.RFC3339, pubStr)
	issue.SourceURL = srcURL
	issue.ParsedAt, _ = time.Parse(time.RFC3339, parsedStr)
	return &issue, nil
}

// GetLatestIssueNumber 最新期号
func (s *SQLiteStore) GetLatestIssueNumber() (int, error) {
	var num sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(number) FROM weekly_issues`).Scan(&num)
	if err != nil {
		return 0, err
	}
	if !num.Valid {
		return 0, nil
	}
	return int(num.Int64), nil
}

// GetUnenrichedProjects 获取未补全的项目
func (s *SQLiteStore) GetUnenrichedProjects(limit int) ([]model.Project, error) {
	query := `
		SELECT id, repo_owner, repo_name, description, stars, language, topics,
		       first_issue_number, enriched_at, is_available,
		       gh_repo_id, forks, watchers, subscribers, owner_avatar,
		       homepage, license_spdx, is_archived, is_fork, is_private,
		       default_branch, open_issues, pushed_at, updated_at, created_at
		FROM projects
		WHERE enriched_at IS NULL OR gh_repo_id IS NULL
		ORDER BY id ASC
		LIMIT ?
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items, _, err := s.scanProjects(rows, 0)
	return items, err
}

// UpdateProjectMeta 更新项目 GitHub 元数据
func (s *SQLiteStore) UpdateProjectMeta(p *model.Project) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		UPDATE projects
		SET description = ?, stars = ?, language = ?, topics = ?,
		    enriched_at = ?, is_available = ?,
		    gh_repo_id = ?, forks = ?, watchers = ?, subscribers = ?, owner_avatar = ?,
		    homepage = ?, license_spdx = ?, is_archived = ?, is_fork = ?, is_private = ?,
		    default_branch = ?, open_issues = ?, pushed_at = ?, updated_at = ?, created_at = ?
		WHERE id = ?
	`, p.Description, p.Stars, p.Language, p.Topics, now, boolToInt(p.IsAvailable),
		p.GhRepoID, p.Forks, p.Watchers, p.Subscribers, p.OwnerAvatar,
		p.Homepage, p.LicenseSpdx, boolToInt(p.IsArchived), boolToInt(p.IsFork), boolToInt(p.IsPrivate),
		p.DefaultBranch, p.OpenIssues, p.PushedAt, p.UpdatedAt, p.CreatedAt,
		p.ID)
	return err
}

// GetZreadRepos 获取所有 zread trending 的 owner/repo 列表（用于 wiki 预热）。
func (s *SQLiteStore) GetZreadRepos() []string {
	rows, err := s.db.Query(`SELECT DISTINCT owner, name FROM zread_trending`)
	if err != nil {
		log.Printf("[store] GetZreadRepos: %v", err)
		return nil
	}
	defer rows.Close()

	var repos []string
	for rows.Next() {
		var owner, name string
		if err := rows.Scan(&owner, &name); err != nil {
			continue
		}
		repos = append(repos, owner+"/"+name)
	}
	return repos
}

// GetUnenrichedZreadRepos 获取未补全 GitHub 元数据的 zread repos。
// 条件：gh_repo_id IS NULL（zread spider 写入时不带 GitHub 数据）。
func (s *SQLiteStore) GetUnenrichedZreadRepos(limit int) ([]model.ZreadTrending, error) {
	query := `
		SELECT week_label, week_start, week_end, rank_in_week, repo_id, owner, name, html_url,
			description, description_zh, star_count, language, topics, wiki_id,
			gh_repo_id, forks, open_issues, watchers, subscribers_count,
			pushed_at, updated_at, created_at, license_spdx, default_branch, is_archived, is_fork,
			zread_week_start_raw, zread_week_end_raw, zread_year_inferred, fetched_at
		FROM zread_trending
		WHERE gh_repo_id IS NULL
		ORDER BY week_start DESC
		LIMIT ?
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanZreadTrending(rows)
}

// UpdateZreadEnriched 更新单条 zread 记录的 GitHub 元数据字段。
func (s *SQLiteStore) UpdateZreadEnriched(owner, name, weekStart string, z *model.ZreadTrending) error {
	_, err := s.db.Exec(`
		UPDATE zread_trending SET
			gh_repo_id        = ?,
			forks             = ?,
			open_issues       = ?,
			watchers          = ?,
			subscribers_count = ?,
			pushed_at         = ?,
			updated_at        = ?,
			created_at        = ?,
			license_spdx      = ?,
			default_branch    = ?,
			is_archived       = ?,
			is_fork           = ?
		WHERE owner = ? AND name = ? AND week_start = ?
	`,
		z.GhRepoID, z.Forks, z.OpenIssues, z.Watchers, z.SubscribersCount,
		z.PushedAt, z.UpdatedAt, z.CreatedAt, z.LicenseSpdx, z.DefaultBranch,
		boolToInt(z.IsArchived), boolToInt(z.IsFork),
		owner, name, weekStart,
	)
	return err
}

// Close 关闭数据库
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
