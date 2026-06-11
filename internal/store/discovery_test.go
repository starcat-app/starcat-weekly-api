package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestDiscoveryKeepsSubmissionsSeparateAndQueriesLatest(t *testing.T) {
	s := newDiscoveryTestStore(t)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	upsertDiscoveryTestSubmission(t, s, 1, "acme", "agent", 5, now.Add(-2*time.Hour), now)
	upsertDiscoveryTestSubmission(t, s, 2, "acme", "agent", 50, now.Add(-time.Hour), now)

	candidates, err := s.GetDiscoveryEnrichmentCandidates(20, now)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("enrichment candidates: %d, %v", len(candidates), err)
	}
	repo := candidates[0]
	repo.GhRepoID = 42
	repo.Description = "AI agent"
	repo.Topics = []string{"ai", "agent"}
	if err := s.UpdateDiscoveryEnriched(repo, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDiscoveryClassified("acme", "agent", "agent", 0.9, "agent framework", "llm", "test", false, now); err != nil {
		t.Fatal(err)
	}

	items, total, err := s.QueryDiscovery(model.DiscoveryQuery{Category: "all", Page: 1, PageSize: 30, Since: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Discovery.HNID != 2 || items[0].Discovery.HNScore != 50 {
		t.Fatalf("unexpected latest submission result: total=%d items=%#v", total, items)
	}
}

func TestDiscoveryClassificationCooldownCanReenterQueue(t *testing.T) {
	s := newDiscoveryTestStore(t)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	upsertDiscoveryTestSubmission(t, s, 1, "acme", "agent", 5, now, now)
	repo := model.DiscoveryRepo{Owner: "acme", Repo: "agent", GhRepoID: 42, Topics: []string{"ai"}}
	if err := s.UpdateDiscoveryEnriched(repo, now); err != nil {
		t.Fatal(err)
	}
	cooldownEnd := now.Add(7 * 24 * time.Hour)
	if err := s.UpdateDiscoveryClassificationFailure("acme", "agent", "timeout", cooldownEnd, true); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetDiscoveryClassificationCandidates(20, cooldownEnd.Add(-time.Second))
	if err != nil || len(before) != 0 {
		t.Fatalf("should still cool down: %d, %v", len(before), err)
	}
	after, err := s.GetDiscoveryClassificationCandidates(20, cooldownEnd.Add(time.Second))
	if err != nil || len(after) != 1 || after[0].ClassifyAttempts != 0 {
		t.Fatalf("should reenter queue: %#v, %v", after, err)
	}
}

func newDiscoveryTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func upsertDiscoveryTestSubmission(t *testing.T, s *SQLiteStore, id int64, owner, repo string, score int, publishedAt, now time.Time) {
	t.Helper()
	err := s.UpsertDiscoverySubmission(model.DiscoverySubmission{
		HNID: id, Owner: owner, Repo: repo, Title: "Show HN", HNURL: "https://news.ycombinator.com/item?id=1",
		SourceURL: "https://github.com/" + owner + "/" + repo, Score: score, PublishedAt: publishedAt,
		FirstSeenAt: now, LastSeenAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
}
