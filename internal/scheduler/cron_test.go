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

func writeTempIssueFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".md")
	if err := os.WriteFile(path, []byte("# weekly\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
