package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

func TestDiscoveryKeepsSubmissionsSeparateAndQueriesLatest(t *testing.T) {
	s := newDiscoveryTestStore(t)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	upsertDiscoveryTestRepo(t, s, 42, "acme", "agent", now)
	attachDiscoveryTestSubmission(t, s, 42, 1, "acme", "agent", 5, now.Add(-2*time.Hour), now)
	attachDiscoveryTestSubmission(t, s, 42, 2, "acme", "agent", 50, now.Add(-time.Hour), now)

	detail, err := s.GetRepoDetail(42)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || len(detail.Events) != 2 || detail.Repo.Discovery == nil || detail.Repo.Discovery.HNID != 2 {
		t.Fatalf("unexpected discovery detail: %#v", detail)
	}
}

func TestDiscoveryQueryReposIncludesDiscoverySource(t *testing.T) {
	s := newDiscoveryTestStore(t)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	upsertDiscoveryTestRepo(t, s, 42, "acme", "agent", now)
	attachDiscoveryTestSubmission(t, s, 42, 1, "acme", "agent", 5, now, now)

	items, total, err := s.QueryRepos(model.RepoQuery{Page: 1, PageSize: 30, Source: []string{model.SourceDiscovery}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].GhRepoID != 42 {
		t.Fatalf("unexpected discovery query: total=%d items=%#v", total, items)
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

func upsertDiscoveryTestRepo(t *testing.T, s *SQLiteStore, id int64, owner, repo string, now time.Time) {
	t.Helper()
	if err := s.UpsertGitHubRepo(model.GitHubRepo{
		GhRepoID: id, Owner: owner, Name: repo, FullName: owner + "/" + repo,
		Description: "AI agent", Topics: []string{"ai", "agent"}, Stars: 42,
		FirstEventAt: now, LatestEventAt: now, IsAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func attachDiscoveryTestSubmission(t *testing.T, s *SQLiteStore, repoID, id int64, owner, repo string, score int, publishedAt, now time.Time) {
	t.Helper()
	err := s.AttachDiscoveryEvent(repoID, model.DiscoverySubmission{
		HNID: id, Owner: owner, Repo: repo, Title: "Show HN", HNURL: "https://news.ycombinator.com/item?id=1",
		SourceURL: "https://github.com/" + owner + "/" + repo, Score: score, Comments: score,
		PublishedAt: publishedAt,
		FirstSeenAt: now, LastSeenAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
}
