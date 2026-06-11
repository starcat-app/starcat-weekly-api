// Package store 提供 AI Discovery 的 SQLite 持久化实现。
//
// 仓库与投稿分表是这里最重要的约束：仓库只 enrich/classify 一次，Show HN 每次投稿
// 独立保留。列表查询再选每个仓库在时间窗口内最新的一次投稿，避免拼错历史数据。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

const discoveryRepoColumns = `
	r.owner, r.repo, r.gh_repo_id, r.description, r.homepage, r.language,
	r.stars, r.forks, r.watchers, r.subscribers, r.open_issues, r.owner_avatar,
	r.default_branch, r.license_spdx, r.topics_json, r.pushed_at, r.updated_at, r.created_at,
	r.is_archived, r.is_fork, r.is_private, r.readme_excerpt,
	r.enrichment_status, r.enrich_attempts, r.enrich_next_retry_at, r.enrich_error, r.enriched_at,
	r.category, r.classify_status, r.classify_confidence, r.classify_reason,
	r.classify_method, r.classify_model, r.classify_attempts,
	r.classify_next_retry_at, r.classify_error, r.classified_at,
	r.first_seen_at, r.last_seen_at, r.record_updated_at`

// UpsertDiscoverySubmission 写入一次 Show HN 投稿，并确保仓库级记录存在。
func (s *SQLiteStore) UpsertDiscoverySubmission(submission model.DiscoverySubmission) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := submission.LastSeenAt.UTC().Format(time.RFC3339)
	firstSeen := submission.FirstSeenAt.UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`
		INSERT INTO discovery_repos (owner, repo, first_seen_at, last_seen_at, record_updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(owner, repo) DO UPDATE SET
			last_seen_at = excluded.last_seen_at,
			record_updated_at = excluded.record_updated_at
	`, submission.Owner, submission.Repo, firstSeen, now, now); err != nil {
		return fmt.Errorf("upsert discovery repo: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO discovery_submissions
			(hn_id, owner, repo, title, hn_url, source_url, score, comments,
			 published_at, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hn_id, owner, repo) DO UPDATE SET
			title = excluded.title,
			source_url = excluded.source_url,
			score = excluded.score,
			comments = excluded.comments,
			last_seen_at = excluded.last_seen_at
	`, submission.HNID, submission.Owner, submission.Repo, submission.Title,
		submission.HNURL, submission.SourceURL, submission.Score, submission.Comments,
		submission.PublishedAt.UTC().Format(time.RFC3339), firstSeen, now); err != nil {
		return fmt.Errorf("upsert discovery submission: %w", err)
	}

	return tx.Commit()
}

// GetDiscoveryEnrichmentCandidates 获取待补全或已到重试时间的仓库。
func (s *SQLiteStore) GetDiscoveryEnrichmentCandidates(limit int, now time.Time) ([]model.DiscoveryRepo, error) {
	limit = clampDiscoveryLimit(limit)
	rows, err := s.db.Query(`SELECT `+discoveryRepoColumns+`
		FROM discovery_repos r
		WHERE r.enrichment_status = 'pending'
		   OR (r.enrichment_status = 'retryable' AND r.enrich_next_retry_at <= ?)
		ORDER BY r.first_seen_at ASC LIMIT ?`, now.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDiscoveryRepos(rows)
}

// UpdateDiscoveryEnriched 原子写入 GitHub metadata + README 摘要，并放行分类队列。
func (s *SQLiteStore) UpdateDiscoveryEnriched(repo model.DiscoveryRepo, now time.Time) error {
	topics, err := json.Marshal(repo.Topics)
	if err != nil {
		return fmt.Errorf("marshal discovery topics: %w", err)
	}
	_, err = s.db.Exec(`
		UPDATE discovery_repos SET
			gh_repo_id = ?, description = ?, homepage = ?, language = ?, stars = ?, forks = ?,
			watchers = ?, subscribers = ?, open_issues = ?, owner_avatar = ?, default_branch = ?,
			license_spdx = ?, topics_json = ?, pushed_at = ?, updated_at = ?, created_at = ?,
			is_archived = ?, is_fork = ?, is_private = ?, readme_excerpt = ?,
			enrichment_status = 'ready', enrich_attempts = 0, enrich_next_retry_at = NULL,
			enrich_error = NULL, enriched_at = ?, record_updated_at = ?
		WHERE owner = ? AND repo = ?
	`, repo.GhRepoID, nullIfEmpty(repo.Description), nullIfEmpty(repo.Homepage), nullIfEmpty(repo.Language),
		repo.Stars, repo.Forks, repo.Watchers, repo.Subscribers, repo.OpenIssues,
		nullIfEmpty(repo.OwnerAvatar), nullIfEmpty(repo.DefaultBranch), nullIfEmpty(repo.LicenseSpdx),
		string(topics), nullIfEmpty(repo.PushedAt), nullIfEmpty(repo.UpdatedAt), nullIfEmpty(repo.CreatedAt),
		boolToInt(repo.IsArchived), boolToInt(repo.IsFork), boolToInt(repo.IsPrivate), repo.READMEExcerpt,
		now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), repo.Owner, repo.Repo)
	return err
}

// UpdateDiscoveryEnrichmentFailure 保留失败原因，并按 nextRetryAt 重新入队。
func (s *SQLiteStore) UpdateDiscoveryEnrichmentFailure(owner, repo, message string, nextRetryAt time.Time) error {
	_, err := s.db.Exec(`UPDATE discovery_repos SET
		enrichment_status = 'retryable', enrich_attempts = enrich_attempts + 1,
		enrich_next_retry_at = ?, enrich_error = ?, record_updated_at = ?
		WHERE owner = ? AND repo = ?`, nextRetryAt.UTC().Format(time.RFC3339), message,
		time.Now().UTC().Format(time.RFC3339), owner, repo)
	return err
}

// MarkDiscoveryUnavailable 把 GitHub 404 仓库移出后续流水线。
func (s *SQLiteStore) MarkDiscoveryUnavailable(owner, repo, message string, now time.Time) error {
	_, err := s.db.Exec(`UPDATE discovery_repos SET
		enrichment_status = 'unavailable', enrich_error = ?, enrich_next_retry_at = NULL,
		record_updated_at = ? WHERE owner = ? AND repo = ?`, message,
		now.UTC().Format(time.RFC3339), owner, repo)
	return err
}

// GetDiscoveryClassificationCandidates 只选择已 enrich 且待分类/到期重试的仓库。
func (s *SQLiteStore) GetDiscoveryClassificationCandidates(limit int, now time.Time) ([]model.DiscoveryRepo, error) {
	limit = clampDiscoveryLimit(limit)
	rows, err := s.db.Query(`SELECT `+discoveryRepoColumns+`
		FROM discovery_repos r
		WHERE r.enrichment_status = 'ready' AND (
			r.classify_status = 'pending' OR
			(r.classify_status = 'retryable' AND r.classify_next_retry_at <= ?)
		)
		ORDER BY r.first_seen_at ASC LIMIT ?`, now.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDiscoveryRepos(rows)
}

// UpdateDiscoveryClassified 写入最终分类；低置信度/非 AI 项目使用 rejected，避免无意义重试。
func (s *SQLiteStore) UpdateDiscoveryClassified(owner, repo, category string, confidence float64, reason, method, classifierModel string, rejected bool, now time.Time) error {
	status := model.DiscoveryStatusClassified
	if rejected {
		status = model.DiscoveryStatusRejected
		category = model.DiscoveryCategoryUnknown
	}
	_, err := s.db.Exec(`UPDATE discovery_repos SET
		category = ?, classify_status = ?, classify_confidence = ?, classify_reason = ?,
		classify_method = ?, classify_model = ?, classify_attempts = 0,
		classify_next_retry_at = NULL, classify_error = NULL, classified_at = ?, record_updated_at = ?
		WHERE owner = ? AND repo = ?`, category, status, confidence, nullIfEmpty(reason), method,
		classifierModel, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), owner, repo)
	return err
}

// UpdateDiscoveryClassificationFailure 记录临时错误。达到阈值时 resetAttempts=true，
// attempts 清零并把 next_retry_at 推到冷却期末，届时能够真正再次被选中。
func (s *SQLiteStore) UpdateDiscoveryClassificationFailure(owner, repo, message string, nextRetryAt time.Time, resetAttempts bool) error {
	attemptExpr := "classify_attempts + 1"
	if resetAttempts {
		attemptExpr = "0"
	}
	query := `UPDATE discovery_repos SET classify_status = 'retryable', classify_attempts = ` + attemptExpr + `,
		classify_next_retry_at = ?, classify_error = ?, record_updated_at = ? WHERE owner = ? AND repo = ?`
	_, err := s.db.Exec(query, nextRetryAt.UTC().Format(time.RFC3339), message,
		time.Now().UTC().Format(time.RFC3339), owner, repo)
	return err
}

// QueryDiscovery 返回时间窗口内每个仓库最新的一次 Show HN 投稿。
func (s *SQLiteStore) QueryDiscovery(params model.DiscoveryQuery) ([]model.DiscoveryItemDTO, int, error) {
	page, pageSize := normalizeDiscoveryPage(params.Page, params.PageSize)
	where := []string{"r.classify_status = 'classified'", "s.published_at >= ?", "s.row_num = 1"}
	args := []any{params.Since.UTC().Format(time.RFC3339)}
	if params.Category != "" && params.Category != "all" {
		where = append(where, "r.category = ?")
		args = append(args, params.Category)
	}
	whereClause := strings.Join(where, " AND ")
	cte := `WITH ranked AS (
		SELECT ds.*, ROW_NUMBER() OVER (
			PARTITION BY ds.owner, ds.repo ORDER BY ds.published_at DESC, ds.hn_id DESC
		) AS row_num FROM discovery_submissions ds
	)`

	var total int
	if err := s.db.QueryRow(cte+` SELECT COUNT(*) FROM ranked s
		JOIN discovery_repos r ON r.owner = s.owner AND r.repo = s.repo
		WHERE `+whereClause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	query := cte + ` SELECT ` + discoveryRepoColumns + `,
		s.hn_id, s.title, s.hn_url, s.source_url, s.score, s.comments, s.published_at
		FROM ranked s JOIN discovery_repos r ON r.owner = s.owner AND r.repo = s.repo
		WHERE ` + whereClause + ` ORDER BY s.score DESC, s.published_at DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(query, append(args, pageSize, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanDiscoveryItems(rows)
	return items, total, err
}

// GetDiscoveryByOwnerRepo 返回仓库最近一次投稿；只暴露已完成分类的数据。
func (s *SQLiteStore) GetDiscoveryByOwnerRepo(owner, repo string) (*model.DiscoveryItemDTO, error) {
	rows, err := s.db.Query(`SELECT `+discoveryRepoColumns+`,
		s.hn_id, s.title, s.hn_url, s.source_url, s.score, s.comments, s.published_at
		FROM discovery_repos r JOIN discovery_submissions s
		ON r.owner = s.owner AND r.repo = s.repo
		WHERE r.owner = ? AND r.repo = ? AND r.classify_status = 'classified'
		ORDER BY s.published_at DESC, s.hn_id DESC LIMIT 1`, owner, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanDiscoveryItems(rows)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

func scanDiscoveryItems(rows *sql.Rows) ([]model.DiscoveryItemDTO, error) {
	items := make([]model.DiscoveryItemDTO, 0)
	for rows.Next() {
		row := newDiscoveryRepoRow()
		values := row.scanTargets()
		var hnID int64
		var title, hnURL, publishedAt string
		var sourceURL sql.NullString
		var score, comments int
		values = append(values, &hnID, &title, &hnURL, &sourceURL, &score, &comments, &publishedAt)
		if err := rows.Scan(values...); err != nil {
			return nil, fmt.Errorf("scan discovery item: %w", err)
		}
		repo := row.model()
		items = append(items, model.DiscoveryItemDTO{
			Repo: repo.ToRepoCard(),
			Discovery: model.DiscoveryExtension{
				HNID: hnID, HNTitle: title, HNURL: hnURL, SourceURL: sourceURL.String,
				HNScore: score, HNComments: comments, HNPublishedAt: publishedAt,
				Category: repo.Category, ClassifyConfidence: repo.ClassifyConfidence,
				ClassifyReason: repo.ClassifyReason,
			},
		})
	}
	return items, rows.Err()
}

func scanDiscoveryRepos(rows *sql.Rows) ([]model.DiscoveryRepo, error) {
	items := make([]model.DiscoveryRepo, 0)
	for rows.Next() {
		row := newDiscoveryRepoRow()
		if err := rows.Scan(row.scanTargets()...); err != nil {
			return nil, fmt.Errorf("scan discovery repo: %w", err)
		}
		items = append(items, row.model())
	}
	return items, rows.Err()
}

// discoveryRepoRow 集中维护 SELECT 列与 Scan 顺序，避免 40 个字段在多处漂移。
// 每次扫描创建独立实例，因此并发查询之间没有共享可变状态。
type discoveryRepoRow struct {
	repo                                                                                     model.DiscoveryRepo
	ghRepoID                                                                                 sql.NullInt64
	description, homepage, language, ownerAvatar, defaultBranch, licenseSpdx                 sql.NullString
	topicsJSON                                                                               string
	pushedAt, updatedAt, createdAt                                                           sql.NullString
	isArchived, isFork, isPrivate                                                            int
	enrichNext, enrichError, enrichedAt                                                      sql.NullString
	confidence                                                                               sql.NullFloat64
	classifyReason, classifyMethod, classifyModel, classifyNext, classifyError, classifiedAt sql.NullString
	firstSeen, lastSeen, recordUpdated                                                       string
}

func newDiscoveryRepoRow() *discoveryRepoRow { return &discoveryRepoRow{} }

func (r *discoveryRepoRow) scanTargets() []any {
	return []any{
		&r.repo.Owner, &r.repo.Repo, &r.ghRepoID, &r.description, &r.homepage, &r.language,
		&r.repo.Stars, &r.repo.Forks, &r.repo.Watchers, &r.repo.Subscribers, &r.repo.OpenIssues, &r.ownerAvatar,
		&r.defaultBranch, &r.licenseSpdx, &r.topicsJSON, &r.pushedAt, &r.updatedAt, &r.createdAt,
		&r.isArchived, &r.isFork, &r.isPrivate, &r.repo.READMEExcerpt,
		&r.repo.EnrichmentStatus, &r.repo.EnrichAttempts, &r.enrichNext, &r.enrichError, &r.enrichedAt,
		&r.repo.Category, &r.repo.ClassifyStatus, &r.confidence, &r.classifyReason,
		&r.classifyMethod, &r.classifyModel, &r.repo.ClassifyAttempts,
		&r.classifyNext, &r.classifyError, &r.classifiedAt, &r.firstSeen, &r.lastSeen, &r.recordUpdated,
	}
}

func (r *discoveryRepoRow) model() model.DiscoveryRepo {
	result := r.repo
	result.GhRepoID = r.ghRepoID.Int64
	result.Description, result.Homepage, result.Language = r.description.String, r.homepage.String, r.language.String
	result.OwnerAvatar, result.DefaultBranch, result.LicenseSpdx = r.ownerAvatar.String, r.defaultBranch.String, r.licenseSpdx.String
	_ = json.Unmarshal([]byte(r.topicsJSON), &result.Topics)
	result.PushedAt, result.UpdatedAt, result.CreatedAt = r.pushedAt.String, r.updatedAt.String, r.createdAt.String
	result.IsArchived, result.IsFork, result.IsPrivate = r.isArchived == 1, r.isFork == 1, r.isPrivate == 1
	result.EnrichError, result.ClassifyReason = r.enrichError.String, r.classifyReason.String
	result.ClassifyMethod, result.ClassifyModel, result.ClassifyError = r.classifyMethod.String, r.classifyModel.String, r.classifyError.String
	if r.confidence.Valid {
		value := r.confidence.Float64
		result.ClassifyConfidence = &value
	}
	result.EnrichNextRetryAt = parseNullableTime(r.enrichNext)
	result.EnrichedAt = parseNullableTime(r.enrichedAt)
	result.ClassifyNextRetryAt = parseNullableTime(r.classifyNext)
	result.ClassifiedAt = parseNullableTime(r.classifiedAt)
	result.FirstSeenAt, _ = time.Parse(time.RFC3339, r.firstSeen)
	result.LastSeenAt, _ = time.Parse(time.RFC3339, r.lastSeen)
	result.UpdatedRecordAt, _ = time.Parse(time.RFC3339, r.recordUpdated)
	return result
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func normalizeDiscoveryPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 30
	}
	if pageSize > 50 {
		pageSize = 50
	}
	return page, pageSize
}

func clampDiscoveryLimit(limit int) int {
	if limit < 1 {
		return 20
	}
	if limit > 50 {
		return 50
	}
	return limit
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
