package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestShouldSyncWeeklyIssueNewIssueAlwaysRuns(t *testing.T) {
	if got := weeklyIssueSyncAction(nil, "hash"); got != weeklyIssueEnqueue {
		t.Fatalf("action=%v want enqueue", got)
	}
}

func TestWeeklyIssueSyncActionSkipsUnchangedContentRegardlessOfMtime(t *testing.T) {
	path := writeTempIssueFile(t, "unchanged")
	firstHash, err := weeklyIssueContentHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	secondHash, err := weeklyIssueContentHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("content hash changed after mtime-only update: %q != %q", firstHash, secondHash)
	}

	existing := &model.WeeklyIssue{Number: 1, ContentHash: firstHash}
	if got := weeklyIssueSyncAction(existing, secondHash); got != weeklyIssueSkip {
		t.Fatalf("action=%v want skip", got)
	}
}

func TestWeeklyIssueSyncActionEnqueuesOnlyWhenContentChanges(t *testing.T) {
	existing := &model.WeeklyIssue{Number: 2, ContentHash: "old-hash"}
	if got := weeklyIssueSyncAction(existing, "new-hash"); got != weeklyIssueEnqueue {
		t.Fatalf("action=%v want enqueue", got)
	}
}

func TestWeeklyIssueSyncActionBaselinesLegacyIssueWithoutReenqueue(t *testing.T) {
	existing := &model.WeeklyIssue{Number: 3}
	if got := weeklyIssueSyncAction(existing, "current-hash"); got != weeklyIssueBaseline {
		t.Fatalf("action=%v want baseline", got)
	}
}

func TestWeeklyBatchIdempotencyKeyUsesContentHash(t *testing.T) {
	const contentHash = "a59b1f"
	if got, want := weeklyBatchIdempotencyKey(123, contentHash), "weekly:123:"+contentHash; got != want {
		t.Fatalf("key=%q want=%q", got, want)
	}
}

func TestShouldRunInitialCollectors(t *testing.T) {
	cases := []struct {
		name  string
		store any
		want  bool
	}{
		{name: "empty sqlite state", store: startupDataStoreStub{}, want: true},
		{name: "existing data", store: startupDataStoreStub{hasData: true}, want: false},
		{name: "state query failure", store: startupDataStoreStub{err: os.ErrPermission}, want: false},
		{name: "store without optional state", store: struct{}{}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRunInitialCollectors(tc.store); got != tc.want {
				t.Fatalf("shouldRunInitialCollectors()=%v want=%v", got, tc.want)
			}
		})
	}
}

type startupDataStoreStub struct {
	hasData bool
	err     error
}

func (s startupDataStoreStub) HasStartupData() (bool, error) { return s.hasData, s.err }

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
