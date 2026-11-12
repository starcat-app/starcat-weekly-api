package store

import (
	"encoding/json"
	"testing"
	"time"

	radar "github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

// 修复生成器误收必须发布新修订，同时保留固定修订和来源序列证据。
func TestRepairRadarSourceRepositories(t *testing.T) {
	s, err := NewSQLiteStore(t.TempDir() + "/weekly.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	project := func(fullName string) radar.RelatedProject {
		return radar.RelatedProject{
			Repo:        model.StarcatRepoCardDTO{GhRepoID: 1, FullName: fullName},
			EvidenceURL: "https://github.com/" + fullName,
		}
	}
	old := radar.Detail{
		Summary: radar.Summary{
			ID: "agents-radar:2026-10-28:ai-cli", Revision: "old-revision", EditionDate: "2026-10-28",
			Category: "ai-cli", Title: "AI CLI", RelatedProjectCount: 3, ContentLanguage: "en",
		},
		Markdown: "# AI CLI",
		// RadarDetail 的严格语言校验读取 detail_json，这里补齐 content_language
		// 比依赖 summary_json 更接近真实发布数据。
		RelatedProjects: []radar.RelatedProject{project("owner/repo"), project("duanyytop/agents-radar"), project("starcat-app/agents-radar")},
	}
	now := time.Now().UTC().Format(time.RFC3339)
	old.UpdatedAt = now
	summary, err := json.Marshal(old.Summary)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO daily_radar_revisions VALUES(?,?,?,?,?)`,
		old.Revision, old.ID, old.ContentLanguage, string(summary), string(detail)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO daily_radar_publications VALUES(?,?,?)`, old.ID, old.ContentLanguage, old.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO daily_radar_sources VALUES(?,?,?,?,?,?)`,
		old.ID, old.ContentLanguage, "upstream", 100, "candidate-hash", old.Revision); err != nil {
		t.Fatal(err)
	}

	changed, err := s.RepairRadarSourceRepositories()
	if err != nil || changed != 1 {
		t.Fatalf("repair changed=%d err=%v", changed, err)
	}
	current, err := s.RadarDetail(old.ID, "", old.ContentLanguage, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.RelatedProjects) != 1 || current.RelatedProjects[0].Repo.FullName != "owner/repo" || current.RelatedProjectCount != 1 {
		t.Fatalf("current detail not repaired: %+v", current)
	}
	content := *current
	content.Revision = ""
	content.Summary.Revision = ""
	content.UpdatedAt = ""
	// 修复只重算当前 detail；旧手工测试数据里为空的元数据在序列化后保持空。
	expected := radar.HashJSON(content)
	if current.Revision != expected || current.Summary.Revision != expected {
		raw, _ := json.Marshal(content)
		t.Fatalf("revision not content-derived: current=%s summary=%s expected=%s content=%s", current.Revision, current.Summary.Revision, expected, raw)
	}
	pinned, err := s.RadarDetail(old.ID, old.Revision, old.ContentLanguage, false)
	if err != nil || len(pinned.RelatedProjects) != 3 {
		t.Fatalf("pinned revision changed: err=%v detail=%+v", err, pinned)
	}
	var sourceRevision string
	if err := s.db.QueryRow(`SELECT revision FROM daily_radar_sources WHERE report_id=? AND language=?`, old.ID, old.ContentLanguage).Scan(&sourceRevision); err != nil || sourceRevision != old.Revision {
		t.Fatalf("source revision changed: %s err=%v", sourceRevision, err)
	}
	snapshot, err := s.RadarSnapshot()
	if err != nil || snapshot.Revision != "1" || len(snapshot.Items) != 1 || snapshot.Items[0].Revision != current.Revision {
		t.Fatalf("catalog not advanced: err=%v snapshot=%+v", err, snapshot)
	}
	if changed, err = s.RepairRadarSourceRepositories(); err != nil || changed != 0 {
		t.Fatalf("repair not idempotent: changed=%d err=%v", changed, err)
	}
}

func mustRepoCard(t *testing.T) radar.RelatedProject {
	t.Helper()
	avatar := "https://avatars.example/owner"
	project := radar.RelatedProject{}
	project.Repo.GhRepoID = 123
	project.Repo.FullName = "owner/repo"
	project.Repo.OwnerAvatar = &avatar
	return project
}
