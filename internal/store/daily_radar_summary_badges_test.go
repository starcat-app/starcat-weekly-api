package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	radar "github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
)

// TestMigrateDailyRadarSummaryBadges 用旧格式 summary_json 验证 v4 只回填徽标，
// 不改写修订身份并推进目录版本。
func TestMigrateDailyRadarSummaryBadges(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "weekly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	detail := radar.Detail{
		Summary: radar.Summary{ID: "agents-radar:2026-10-28:ai-cli", Revision: "old", EditionDate: "2026-10-28",
			Category: "ai-cli", Period: "daily", Title: "AI CLI", Summary: "summary", ReadingMinutes: 1,
			Cover: radar.Cover{Kind: "category"}, RelatedProjectCount: 1, ContentLanguage: "en"},
		RelatedProjects: []radar.RelatedProject{mustRepoCard(t)},
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	// 旧摘要按迁移前契约不含 related_project_badges。
	legacySummary := `{"id":"agents-radar:2026-10-28:ai-cli","revision":"old","edition_date":"2026-10-28","category":"ai-cli","period":"daily","title":"AI CLI","summary":"summary","reading_minutes":1,"cover":{"kind":"category","fallback":"","alt":""},"related_project_count":1,"content_language":"en","available_languages":["en"]}`
	if _, err := s.db.Exec(`INSERT INTO daily_radar_revisions VALUES(?,?,?,?,?)`, "old", detail.ID, "en", legacySummary, string(detailJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO daily_radar_publications VALUES(?,?,?)`, detail.ID, "en", "old"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateDailyRadarSummaryBadges(tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.RadarSnapshot()
	if err != nil || len(snapshot.Items) != 1 {
		t.Fatalf("snapshot %d %v", len(snapshot.Items), err)
	}
	badges := snapshot.Items[0].RelatedProjectBadges
	if len(badges) != 1 || badges[0].GhRepoID != 123 || badges[0].FullName != "owner/repo" || badges[0].OwnerAvatar == nil || *badges[0].OwnerAvatar != "https://avatars.example/owner" {
		t.Fatalf("badges %+v", badges)
	}
	if snapshot.Revision != "1" {
		t.Fatalf("catalog revision not bumped: %s", snapshot.Revision)
	}
}
