package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

const maxWeeklyPins = 50

type PinValidationError struct{ Message string }

func (e *PinValidationError) Error() string { return e.Message }

func (s *SQLiteStore) SearchWeeklyRepos(query string, limit int) ([]model.RepoSearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	pattern := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	rows, err := s.db.Query(`
		SELECT gr.gh_repo_id, gr.full_name, gr.owner, gr.name, gr.owner_avatar,
		       gr.description, gr.stars, gr.source_types_json
		FROM github_repos gr
		WHERE gr.is_available=1 AND `+hasAnySourceSQL()+`
		  AND (lower(gr.full_name) LIKE ? OR lower(COALESCE(gr.description, '')) LIKE ?)
		ORDER BY gr.stars DESC, gr.gh_repo_id DESC LIMIT ?`, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.RepoSearchResult
	for rows.Next() {
		var item model.RepoSearchResult
		var avatar, description, sourceTypes sql.NullString
		if err := rows.Scan(&item.GhRepoID, &item.FullName, &item.Owner, &item.Repo,
			&avatar, &description, &item.Stars, &sourceTypes); err != nil {
			return nil, err
		}
		item.OwnerAvatar = avatar.String
		item.Description = description.String
		item.SourceTypes = model.DecodeStringArray(sourceTypes.String)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) GetWeeklyPins() ([]model.PinnedRepo, error) {
	rows, err := s.db.Query(`
		SELECT gr.gh_repo_id, gr.full_name, gr.owner, gr.name, gr.owner_avatar,
		       gr.description, gr.stars, gr.source_types_json, p.position, p.pinned_at
		FROM weekly_pins p JOIN github_repos gr ON gr.gh_repo_id=p.gh_repo_id
		ORDER BY p.position ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.PinnedRepo
	for rows.Next() {
		var item model.PinnedRepo
		var avatar, description, sourceTypes sql.NullString
		if err := rows.Scan(&item.GhRepoID, &item.FullName, &item.Owner, &item.Repo,
			&avatar, &description, &item.Stars, &sourceTypes, &item.Position, &item.PinnedAt); err != nil {
			return nil, err
		}
		item.OwnerAvatar = avatar.String
		item.Description = description.String
		item.SourceTypes = model.DecodeStringArray(sourceTypes.String)
		result = append(result, item)
	}
	return result, rows.Err()
}

// ReplaceWeeklyPins 在一个 transaction 中原子替换完整有序列表。
func (s *SQLiteStore) ReplaceWeeklyPins(repoIDs []int64, now time.Time) ([]model.PinnedRepo, error) {
	if len(repoIDs) > maxWeeklyPins {
		return nil, &PinValidationError{Message: fmt.Sprintf("weekly pins exceeds limit %d", maxWeeklyPins)}
	}
	seen := make(map[int64]struct{}, len(repoIDs))
	for _, repoID := range repoIDs {
		if repoID <= 0 {
			return nil, &PinValidationError{Message: fmt.Sprintf("invalid gh_repo_id %d", repoID)}
		}
		if _, exists := seen[repoID]; exists {
			return nil, &PinValidationError{Message: fmt.Sprintf("duplicate gh_repo_id %d", repoID)}
		}
		seen[repoID] = struct{}{}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	for _, repoID := range repoIDs {
		var valid int
		if err := tx.QueryRow(`
			SELECT COUNT(*) FROM github_repos gr
			WHERE gr.gh_repo_id=? AND gr.is_available=1 AND `+hasAnySourceSQL(), repoID).Scan(&valid); err != nil {
			return nil, err
		}
		if valid != 1 {
			return nil, &PinValidationError{Message: fmt.Sprintf("repo %d is missing, unavailable, or has no enabled source", repoID)}
		}
	}
	if _, err := tx.Exec(`DELETE FROM weekly_pins`); err != nil {
		return nil, err
	}
	nowText := now.UTC().Format(time.RFC3339)
	for index, repoID := range repoIDs {
		if _, err := tx.Exec(`
			INSERT INTO weekly_pins(gh_repo_id, position, pinned_at, updated_at)
			VALUES (?, ?, ?, ?)`, repoID, index+1, nowText, nowText); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetWeeklyPins()
}
