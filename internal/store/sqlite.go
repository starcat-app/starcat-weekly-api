// Package store 实现基于 SQLite 的数据存储
package store

import (
	"database/sql"
	"fmt"
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
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// migrate 建表及迁移
func (s *SQLiteStore) migrate() error {
	// v1 schema
	schemaV1 := `
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
		UNIQUE(repo_owner, repo_name)
	);

	CREATE INDEX IF NOT EXISTS idx_projects_issue ON projects(first_issue_number DESC);
	CREATE INDEX IF NOT EXISTS idx_projects_lang  ON projects(language);
	`
	if _, err := s.db.Exec(schemaV1); err != nil {
		return err
	}

	return s.migrateV2()
}

func (s *SQLiteStore) migrateV2() error {
	var userVersion int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		return err
	}

	if userVersion < 2 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		alterations := []string{
			"ALTER TABLE projects ADD COLUMN gh_repo_id        INTEGER",
			"ALTER TABLE projects ADD COLUMN forks             INTEGER DEFAULT 0",
			"ALTER TABLE projects ADD COLUMN watchers          INTEGER DEFAULT 0",
			"ALTER TABLE projects ADD COLUMN subscribers       INTEGER DEFAULT 0",
			"ALTER TABLE projects ADD COLUMN owner_avatar      TEXT",
			"ALTER TABLE projects ADD COLUMN homepage          TEXT",
			"ALTER TABLE projects ADD COLUMN license_spdx      TEXT",
			"ALTER TABLE projects ADD COLUMN is_archived       INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE projects ADD COLUMN is_fork           INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE projects ADD COLUMN is_private        INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE projects ADD COLUMN default_branch    TEXT",
			"ALTER TABLE projects ADD COLUMN open_issues       INTEGER DEFAULT 0",
			"ALTER TABLE projects ADD COLUMN pushed_at         TEXT",
			"ALTER TABLE projects ADD COLUMN updated_at         TEXT",
			"ALTER TABLE projects ADD COLUMN created_at         TEXT",
			"CREATE INDEX IF NOT EXISTS idx_projects_gh_repo_id ON projects(gh_repo_id) WHERE gh_repo_id IS NOT NULL",
			"PRAGMA user_version = 2",
		}

		for _, sql := range alterations {
			if _, err := tx.Exec(sql); err != nil {
				return fmt.Errorf("exec %s: %w", sql, err)
			}
		}

		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
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
