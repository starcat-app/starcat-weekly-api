package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/source"
	_ "modernc.org/sqlite"
)

func TestMultiSourceMigrationBackfillsLegacyEventsAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	legacy := &SQLiteStore{db: db}
	if err := legacy.createLegacySchemaForTest(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	if err := legacy.UpsertGitHubRepo(model.GitHubRepo{
		GhRepoID: 42, Owner: "acme", Name: "agent", FullName: "acme/agent",
		FirstEventAt: now, LatestEventAt: now, IsAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := seedLegacySourceRows(legacy.db, now); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assertRowCount(t, store.db, `SELECT COUNT(*) FROM source_catalog`, len(source.Definitions))
	assertRowCount(t, store.db, `SELECT COUNT(*) FROM repo_source_events WHERE gh_repo_id=42`, 3)
	assertRowCount(t, store.db, `SELECT COUNT(*) FROM schema_migrations WHERE version=1`, 1)
	assertRowCount(t, store.db, `SELECT COUNT(*) FROM schema_migrations WHERE version=2`, 1)
	issue, err := store.GetIssue(100)
	if err != nil {
		t.Fatal(err)
	}
	if issue == nil || issue.ContentHash != "" {
		t.Fatalf("legacy issue content hash=%#v want empty baseline", issue)
	}

	var sourceTypes string
	if err := store.db.QueryRow(`SELECT source_types_json FROM github_repos WHERE gh_repo_id=42`).Scan(&sourceTypes); err != nil {
		t.Fatal(err)
	}
	wantSources := `["weekly","zread","discovery"]`
	if sourceTypes != wantSources {
		t.Fatalf("source_types_json=%s want=%s", sourceTypes, wantSources)
	}

	if err := store.runMigrations(); err != nil {
		t.Fatal(err)
	}
	assertRowCount(t, store.db, `SELECT COUNT(*) FROM repo_source_events WHERE gh_repo_id=42`, 3)
}

func TestHasStartupDataTracksPersistedState(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "startup-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hasData, err := store.HasStartupData()
	if err != nil {
		t.Fatal(err)
	}
	if hasData {
		t.Fatal("new database must allow the initial collectors")
	}

	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertIssue(&model.WeeklyIssue{
		Number: 1, PublishedAt: now, SourceURL: "https://weekly.example/1", ParsedAt: now, ContentHash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	hasData, err = store.HasStartupData()
	if err != nil {
		t.Fatal(err)
	}
	if !hasData {
		t.Fatal("existing weekly issue must suppress startup collectors")
	}
}

func seedLegacySourceRows(db *sql.DB, now time.Time) error {
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO weekly_issues(number, published_at, source_url, parsed_at) VALUES (100, ?, 'https://weekly.example/100', ?)`, []any{now.Add(-48 * time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)}},
		{`INSERT INTO weekly_extras(gh_repo_id, first_issue_number, issue_url, recommendation, parsed_at) VALUES (42, 100, 'https://weekly.example/100', 'weekly summary', ?)`, []any{now.Format(time.RFC3339)}},
		{`INSERT INTO zread_events(gh_repo_id, week_start, week_end, rank_in_week, description_zh, fetched_at) VALUES (42, '2026-07-13', '2026-07-19', 3, 'zread summary', ?)`, []any{now.Format(time.RFC3339)}},
		{`INSERT INTO discovery_submissions(hn_id, gh_repo_id, title, hn_url, score, comments, published_at, first_seen_at, last_seen_at) VALUES (123, 42, 'Show HN', 'https://news.ycombinator.com/item?id=123', 10, 2, ?, ?, ?)`, []any{now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339)}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func TestFixedSourceCatalogOnlyAllowsAIIntelligenceManualImport(t *testing.T) {
	for _, definition := range source.Definitions {
		if definition.ManualImportEnabled != (definition.Code == model.SourceAIIntelligence) {
			t.Fatalf("source %s manual_import_enabled=%v", definition.Code, definition.ManualImportEnabled)
		}
	}
	if _, ok := source.Find("arbitrary-category"); ok {
		t.Fatal("unknown source must not be accepted")
	}
}

func (s *SQLiteStore) createLegacySchemaForTest() error {
	// createSchema 会立即执行 migration；升级测试需要先构造真实旧版状态，
	// 因此只在测试中复制调用 migration 之前的基线 DDL。
	_, err := s.db.Exec(`
		CREATE TABLE github_repos (
			gh_repo_id INTEGER PRIMARY KEY, owner TEXT NOT NULL, name TEXT NOT NULL,
			full_name TEXT NOT NULL, description TEXT, homepage TEXT, language TEXT,
			stars INTEGER NOT NULL DEFAULT 0, forks INTEGER NOT NULL DEFAULT 0,
			watchers INTEGER NOT NULL DEFAULT 0, subscribers INTEGER NOT NULL DEFAULT 0,
			open_issues INTEGER NOT NULL DEFAULT 0, owner_avatar TEXT, default_branch TEXT,
			license_spdx TEXT, topics_json TEXT NOT NULL DEFAULT '[]', pushed_at TEXT,
			updated_at TEXT, created_at TEXT, is_archived INTEGER NOT NULL DEFAULT 0,
			is_fork INTEGER NOT NULL DEFAULT 0, is_private INTEGER NOT NULL DEFAULT 0,
			source_types_json TEXT NOT NULL DEFAULT '[]', first_event_at TEXT NOT NULL,
			latest_event_at TEXT NOT NULL, enriched_at TEXT, record_updated_at TEXT NOT NULL,
			is_available INTEGER NOT NULL DEFAULT 1, UNIQUE(owner, name)
		);
		CREATE TABLE weekly_issues (number INTEGER PRIMARY KEY, published_at TEXT NOT NULL, source_url TEXT NOT NULL, parsed_at TEXT NOT NULL);
		CREATE TABLE weekly_extras (gh_repo_id INTEGER PRIMARY KEY REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE, first_issue_number INTEGER NOT NULL REFERENCES weekly_issues(number), issue_url TEXT NOT NULL, recommendation TEXT, parsed_at TEXT NOT NULL);
		CREATE TABLE zread_events (id INTEGER PRIMARY KEY AUTOINCREMENT, gh_repo_id INTEGER NOT NULL REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE, week_start TEXT NOT NULL, week_end TEXT, week_label TEXT, rank_in_week INTEGER NOT NULL, description_zh TEXT, zread_repo_id TEXT, wiki_id TEXT, zread_week_start_raw TEXT, zread_week_end_raw TEXT, zread_year_inferred INTEGER, fetched_at TEXT NOT NULL, UNIQUE(gh_repo_id, week_start));
		CREATE TABLE discovery_submissions (hn_id INTEGER PRIMARY KEY, gh_repo_id INTEGER NOT NULL REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE, title TEXT NOT NULL, hn_url TEXT NOT NULL, source_url TEXT, score INTEGER NOT NULL DEFAULT 0, comments INTEGER NOT NULL DEFAULT 0, published_at TEXT NOT NULL, first_seen_at TEXT NOT NULL, last_seen_at TEXT NOT NULL);
	`)
	return err
}

func assertRowCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query %q count=%d want=%d", query, got, want)
	}
}
