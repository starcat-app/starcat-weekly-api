package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestShouldSyncWeeklyIssueNewIssueAlwaysRuns(t *testing.T) {
	if shouldSyncWeeklyIssue(nil, "/tmp/issue-1.md") {
		return
	}
	t.Fatal("new issue should sync")
}

func TestShouldSyncWeeklyIssueSkipsWhenFileUnchanged(t *testing.T) {
	path := writeTempIssueFile(t, "unchanged")
	parsedAt := time.Now().UTC().Add(time.Hour)
	existing := &model.WeeklyIssue{Number: 1, ParsedAt: parsedAt}

	if shouldSyncWeeklyIssue(existing, path) {
		t.Fatal("unchanged issue should be skipped")
	}
}

func TestShouldSyncWeeklyIssueRunsWhenFileModifiedAfterParse(t *testing.T) {
	path := writeTempIssueFile(t, "modified")
	parsedAt := time.Now().UTC().Add(-time.Hour)
	existing := &model.WeeklyIssue{Number: 2, ParsedAt: parsedAt}

	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !shouldSyncWeeklyIssue(existing, path) {
		t.Fatal("modified issue should sync")
	}
}

func TestShouldSyncWeeklyIssueRunsWhenStatFails(t *testing.T) {
	existing := &model.WeeklyIssue{Number: 3, ParsedAt: time.Now().UTC()}
	if !shouldSyncWeeklyIssue(existing, filepath.Join(t.TempDir(), "missing.md")) {
		t.Fatal("missing file should trigger conservative resync")
	}
}

func TestWeeklyAndZreadCollectorsBuildPersistentCandidates(t *testing.T) {
	publishedAt := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	weekly := weeklyCandidates([]model.Project{{RepoOwner: "Acme", RepoName: "Agent", Description: "summary"}}, 123, publishedAt, "https://weekly.example/123")
	if len(weekly) != 1 || weekly[0].ExternalKey != "issue:123:acme/agent" || weekly[0].Payload["issue_number"] != 123 {
		t.Fatalf("weekly=%#v", weekly)
	}
	zread := zreadCandidates([]model.ZreadTrending{{
		Owner: "Acme", Name: "Agent", WeekStart: "2026-07-13", WeekEnd: "2026-07-19",
		RankInWeek: 2, DescriptionZh: "摘要", WikiID: "wiki-1",
	}})
	if len(zread) != 1 || zread[0].ExternalKey != "week:2026-07-13:acme/agent" || zread[0].Rank == nil || *zread[0].Rank != 2 || zread[0].Payload["wiki_id"] != "wiki-1" {
		t.Fatalf("zread=%#v", zread)
	}
}

func writeTempIssueFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".md")
	if err := os.WriteFile(path, []byte("# weekly\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
