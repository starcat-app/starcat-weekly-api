package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/source"
)

type schemaMigration struct {
	version int
	name    string
	apply   func(*sql.Tx) error
}

// runMigrations 只追加执行尚未落库的 migration。
//
// weekly-api 已经存在生产 SQLite 数据，不能再依赖“删库重建”。每个 migration
// 与版本记录必须处于同一事务，服务进程在中途退出时要么全部成功，要么下次重试。
func (s *SQLiteStore) runMigrations() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	migrations := []schemaMigration{
		{version: 1, name: "weekly multi-source foundation", apply: migrateMultiSourceFoundation},
	}
	for _, migration := range migrations {
		var applied int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, migration.version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration v%d: %w", migration.version, err)
		}
		if applied > 0 {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration v%d: %w", migration.version, err)
		}
		if err := migration.apply(tx); err != nil {
			rollback(tx)
			return fmt.Errorf("apply migration v%d %s: %w", migration.version, migration.name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			migration.version, migration.name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			rollback(tx)
			return fmt.Errorf("record migration v%d: %w", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration v%d: %w", migration.version, err)
		}
	}
	return nil
}

func migrateMultiSourceFoundation(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE source_catalog (
			code                  TEXT PRIMARY KEY,
			display_name_zh       TEXT NOT NULL,
			display_name_en       TEXT NOT NULL,
			icon_key              TEXT NOT NULL,
			ingest_mode           TEXT NOT NULL CHECK (ingest_mode IN ('crawler', 'manual')),
			sort_order            INTEGER NOT NULL UNIQUE,
			enabled               INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
			manual_import_enabled INTEGER NOT NULL DEFAULT 0 CHECK (manual_import_enabled IN (0, 1)),
			created_at            TEXT NOT NULL,
			updated_at            TEXT NOT NULL
		);

		CREATE TABLE repo_source_events (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			source_code  TEXT NOT NULL REFERENCES source_catalog(code),
			external_key TEXT NOT NULL,
			gh_repo_id   INTEGER NOT NULL REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE,
			occurred_at  TEXT NOT NULL,
			source_url   TEXT,
			title        TEXT,
			summary      TEXT,
			rank         INTEGER,
			payload_json TEXT NOT NULL DEFAULT '{}',
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL,
			UNIQUE(source_code, external_key)
		);
		CREATE INDEX idx_repo_source_events_source_time ON repo_source_events(source_code, occurred_at DESC);
		CREATE INDEX idx_repo_source_events_repo_time ON repo_source_events(gh_repo_id, occurred_at DESC);

		CREATE TABLE ingest_batches (
			id              TEXT PRIMARY KEY,
			source_code     TEXT NOT NULL REFERENCES source_catalog(code),
			kind            TEXT NOT NULL CHECK (kind IN ('collector', 'manual_import', 'backfill')),
			idempotency_key TEXT UNIQUE,
			status          TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'success', 'partial_success', 'failed')),
			cursor_json     TEXT NOT NULL DEFAULT '{}',
			total           INTEGER NOT NULL DEFAULT 0,
			success         INTEGER NOT NULL DEFAULT 0,
			discarded       INTEGER NOT NULL DEFAULT 0,
			created_at      TEXT NOT NULL,
			started_at      TEXT,
			finished_at     TEXT,
			updated_at      TEXT NOT NULL
		);
		CREATE INDEX idx_ingest_batches_source_created ON ingest_batches(source_code, created_at DESC);

		CREATE TABLE ingest_items (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			batch_id             TEXT NOT NULL REFERENCES ingest_batches(id) ON DELETE CASCADE,
			owner                TEXT NOT NULL,
			repo                 TEXT NOT NULL,
			normalized_full_name TEXT NOT NULL,
			external_key         TEXT NOT NULL,
			occurred_at          TEXT NOT NULL,
			source_url           TEXT,
			title                TEXT,
			summary              TEXT,
			rank                 INTEGER,
			payload_json         TEXT NOT NULL DEFAULT '{}',
			status               TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'retrying', 'success', 'discarded')),
			attempts             INTEGER NOT NULL DEFAULT 0,
			next_attempt_at      TEXT,
			lease_owner          TEXT,
			lease_expires_at     TEXT,
			gh_repo_id           INTEGER REFERENCES github_repos(gh_repo_id),
			last_error_code      TEXT,
			last_error_message   TEXT,
			created_at           TEXT NOT NULL,
			updated_at           TEXT NOT NULL,
			finished_at          TEXT,
			UNIQUE(batch_id, normalized_full_name, external_key)
		);
		CREATE INDEX idx_ingest_items_claim ON ingest_items(status, next_attempt_at, lease_expires_at, id);
		CREATE INDEX idx_ingest_items_batch_status ON ingest_items(batch_id, status);

		CREATE TABLE weekly_pins (
			gh_repo_id INTEGER PRIMARY KEY REFERENCES github_repos(gh_repo_id) ON DELETE CASCADE,
			position   INTEGER NOT NULL UNIQUE CHECK (position > 0),
			pinned_at  TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, definition := range source.Definitions {
		if _, err := tx.Exec(`
			INSERT INTO source_catalog(
				code, display_name_zh, display_name_en, icon_key, ingest_mode,
				sort_order, enabled, manual_import_enabled, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, definition.Code, definition.DisplayNameZH, definition.DisplayNameEN, definition.IconKey,
			definition.IngestMode, definition.SortOrder, boolInt(definition.Enabled),
			boolInt(definition.ManualImportEnabled), now, now); err != nil {
			return fmt.Errorf("seed source %s: %w", definition.Code, err)
		}
	}

	// 旧表只作为本次 migration 的回滚证据。新代码随后只读写 repo_source_events，
	// 不双写两套来源事实，避免线上状态分叉。
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO repo_source_events(
			source_code, external_key, gh_repo_id, occurred_at, source_url,
			title, summary, rank, payload_json, created_at, updated_at
		)
		SELECT 'weekly', 'issue:' || w.first_issue_number || ':' || w.gh_repo_id,
			w.gh_repo_id, i.published_at, w.issue_url, NULL, w.recommendation, NULL,
			json_object('issue_number', w.first_issue_number), w.parsed_at, w.parsed_at
		FROM weekly_extras w
		JOIN weekly_issues i ON i.number=w.first_issue_number;

		INSERT OR IGNORE INTO repo_source_events(
			source_code, external_key, gh_repo_id, occurred_at, source_url,
			title, summary, rank, payload_json, created_at, updated_at
		)
		SELECT 'zread', 'week:' || z.week_start || ':' || z.gh_repo_id,
			z.gh_repo_id, z.week_start || 'T00:00:00Z', NULL, z.week_label,
			z.description_zh, z.rank_in_week,
			json_object(
				'week_start', z.week_start,
				'week_end', z.week_end,
				'zread_repo_id', z.zread_repo_id,
				'wiki_id', z.wiki_id,
				'zread_year_inferred', z.zread_year_inferred
			), z.fetched_at, z.fetched_at
		FROM zread_events z;

		INSERT OR IGNORE INTO repo_source_events(
			source_code, external_key, gh_repo_id, occurred_at, source_url,
			title, summary, rank, payload_json, created_at, updated_at
		)
		SELECT 'discovery', 'hn:' || d.hn_id, d.gh_repo_id, d.published_at,
			d.hn_url, d.title, NULL, NULL,
			json_object(
				'hn_id', d.hn_id,
				'score', d.score,
				'comments', d.comments,
				'github_source_url', d.source_url
			), d.first_seen_at, d.last_seen_at
		FROM discovery_submissions d;
	`); err != nil {
		return fmt.Errorf("backfill legacy source events: %w", err)
	}

	if err := validateLegacyBackfill(tx); err != nil {
		return err
	}

	_, err := tx.Exec(`
		UPDATE github_repos AS gr
		SET source_types_json = (
				SELECT json_group_array(source_code)
				FROM (
					SELECT DISTINCT e.source_code
					FROM repo_source_events e
					JOIN source_catalog sc ON sc.code=e.source_code
					WHERE e.gh_repo_id=gr.gh_repo_id
					ORDER BY sc.sort_order
				)
			),
			first_event_at = (SELECT MIN(e.occurred_at) FROM repo_source_events e WHERE e.gh_repo_id=gr.gh_repo_id),
			latest_event_at = (SELECT MAX(e.occurred_at) FROM repo_source_events e WHERE e.gh_repo_id=gr.gh_repo_id),
			record_updated_at = ?
		WHERE EXISTS (SELECT 1 FROM repo_source_events e WHERE e.gh_repo_id=gr.gh_repo_id)
	`, now)
	return err
}

func validateLegacyBackfill(tx *sql.Tx) error {
	checks := []struct {
		legacySQL string
		source    string
	}{
		{legacySQL: `SELECT COUNT(*) FROM weekly_extras`, source: "weekly"},
		{legacySQL: `SELECT COUNT(*) FROM zread_events`, source: "zread"},
		{legacySQL: `SELECT COUNT(*) FROM discovery_submissions`, source: "discovery"},
	}
	for _, check := range checks {
		var legacyCount, eventCount int
		if err := tx.QueryRow(check.legacySQL).Scan(&legacyCount); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM repo_source_events WHERE source_code=?`, check.source).Scan(&eventCount); err != nil {
			return err
		}
		if eventCount < legacyCount {
			return fmt.Errorf("source %s backfill mismatch: legacy=%d events=%d", check.source, legacyCount, eventCount)
		}
	}
	return nil
}
