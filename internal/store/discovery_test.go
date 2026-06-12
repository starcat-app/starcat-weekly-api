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

	// v1.2：enrichment_status='ready' 即进入 API 可查询状态
	items, total, err := s.QueryDiscovery(model.DiscoveryQuery{Page: 1, PageSize: 30, Since: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Discovery.HNID != 2 || items[0].Discovery.HNScore != 50 {
		t.Fatalf("unexpected latest submission result: total=%d items=%#v", total, items)
	}
}

func TestDiscoveryEnrichmentFailureRetries(t *testing.T) {
	s := newDiscoveryTestStore(t)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	upsertDiscoveryTestSubmission(t, s, 1, "acme", "agent", 5, now, now)

	retryAt := now.Add(time.Hour)
	if err := s.UpdateDiscoveryEnrichmentFailure("acme", "agent", "timeout", retryAt); err != nil {
		t.Fatal(err)
	}
	// 未到重试时间，不应出现在候选列表
	before, err := s.GetDiscoveryEnrichmentCandidates(20, retryAt.Add(-time.Second))
	if err != nil || len(before) != 0 {
		t.Fatalf("should still wait: %d, %v", len(before), err)
	}
	// 已到重试时间，应重新入队
	after, err := s.GetDiscoveryEnrichmentCandidates(20, retryAt.Add(time.Second))
	if err != nil || len(after) != 1 {
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
