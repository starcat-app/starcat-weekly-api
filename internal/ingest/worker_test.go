package ingest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/github"
	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

func TestWorkerCompletesRepoEventAndBatchAtomically(t *testing.T) {
	repository := newWorkerStore(t)
	wake := NewWakeSignal()
	service := NewService(repository, wake)
	service.newID = func() (string, error) { return "success-batch", nil }
	now := time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if _, err := service.Enqueue(aiImportRequest("success", "acme", "agent")); err != nil {
		t.Fatal(err)
	}
	fetcher := &workerRepoFetcher{response: testRepoResponse("acme", "agent")}
	cache := &countingInvalidator{}
	worker := NewWorker(repository, fetcher, wake, cache)
	worker.now = func() time.Time { return now }

	processed, err := worker.ProcessAvailable(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || fetcher.calls != 1 || cache.count != 1 {
		t.Fatalf("processed=%d calls=%d invalidations=%d", processed, fetcher.calls, cache.count)
	}
	batch, err := repository.GetIngestBatch("success-batch", true)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || batch.Status != model.IngestBatchSuccess || batch.Success != 1 || batch.Items[0].Status != model.IngestItemSuccess {
		t.Fatalf("batch=%#v", batch)
	}
	detail, err := repository.GetRepoDetail(42)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || len(detail.Repo.SourceEntries) != 1 || detail.Repo.SourceEntries[0].SourceCode != model.SourceAIIntelligence {
		t.Fatalf("detail=%#v", detail)
	}
}

func TestWorkerRetriesAtFifteenAndThirtyMinutesThenDiscards(t *testing.T) {
	repository := newWorkerStore(t)
	service := NewService(repository, NewWakeSignal())
	service.newID = func() (string, error) { return "retry-batch", nil }
	base := time.Date(2026, 7, 16, 4, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if _, err := service.Enqueue(aiImportRequest("retry", "acme", "missing")); err != nil {
		t.Fatal(err)
	}
	fetcher := &workerRepoFetcher{err: errors.New("temporary network failure")}
	worker := NewWorker(repository, fetcher, NewWakeSignal(), nil)
	current := base
	worker.now = func() time.Time { return current }

	assertProcessed(t, worker, 1)
	assertRetryState(t, repository, "retry-batch", 1, base.Add(15*time.Minute))
	assertProcessed(t, worker, 0)

	current = base.Add(15 * time.Minute)
	assertProcessed(t, worker, 1)
	assertRetryState(t, repository, "retry-batch", 2, base.Add(45*time.Minute))

	current = base.Add(45 * time.Minute)
	assertProcessed(t, worker, 1)
	batch, err := repository.GetIngestBatch("retry-batch", true)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != model.IngestBatchFailed || batch.Discarded != 1 || batch.Items[0].Attempts != 3 || batch.Items[0].Status != model.IngestItemDiscarded {
		t.Fatalf("batch=%#v", batch)
	}
}

func TestWorkerPermanentNotFoundDiscardsWithoutRetry(t *testing.T) {
	repository := newWorkerStore(t)
	service := NewService(repository, NewWakeSignal())
	service.newID = func() (string, error) { return "not-found-batch", nil }
	if _, err := service.Enqueue(aiImportRequest("not-found", "acme", "missing")); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(repository, &workerRepoFetcher{err: github.ErrRepoNotFound}, NewWakeSignal(), nil)
	assertProcessed(t, worker, 1)
	batch, err := repository.GetIngestBatch("not-found-batch", true)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Items[0].Attempts != 1 || batch.Items[0].Status != model.IngestItemDiscarded || batch.Items[0].LastErrorCode != "github_not_found" {
		t.Fatalf("batch=%#v", batch)
	}
}

func TestWorkerSkipsGitHubForFreshCanonicalRepo(t *testing.T) {
	repository := newWorkerStore(t)
	base := time.Date(2026, 7, 16, 5, 0, 0, 0, time.UTC)
	enrichedAt := base.Add(-time.Hour)
	if err := repository.UpsertGitHubRepo(model.GitHubRepo{
		GhRepoID: 42, Owner: "acme", Name: "agent", FullName: "acme/agent",
		FirstEventAt: base, LatestEventAt: base, EnrichedAt: &enrichedAt, IsAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, NewWakeSignal())
	service.newID = func() (string, error) { return "fresh-batch", nil }
	service.now = func() time.Time { return base }
	if _, err := service.Enqueue(aiImportRequest("fresh", "acme", "agent")); err != nil {
		t.Fatal(err)
	}
	fetcher := &workerRepoFetcher{err: errors.New("must not be called")}
	worker := NewWorker(repository, fetcher, NewWakeSignal(), nil)
	worker.now = func() time.Time { return base }
	assertProcessed(t, worker, 1)
	if fetcher.calls != 0 {
		t.Fatalf("fresh repo caused %d GitHub calls", fetcher.calls)
	}
}

func TestExpiredLeaseCanBeClaimedAgain(t *testing.T) {
	repository := newWorkerStore(t)
	service := NewService(repository, NewWakeSignal())
	service.newID = func() (string, error) { return "lease-batch", nil }
	base := time.Date(2026, 7, 16, 6, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if _, err := service.Enqueue(aiImportRequest("lease", "acme", "agent")); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ClaimIngestItem("worker-a", base, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.Attempts != 1 {
		t.Fatalf("first=%#v", first)
	}
	beforeExpiry, err := repository.ClaimIngestItem("worker-b", base.Add(29*time.Minute), 30*time.Minute)
	if err != nil || beforeExpiry != nil {
		t.Fatalf("beforeExpiry=%#v err=%v", beforeExpiry, err)
	}
	afterExpiry, err := repository.ClaimIngestItem("worker-b", base.Add(31*time.Minute), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if afterExpiry == nil || afterExpiry.ID != first.ID || afterExpiry.Attempts != 2 {
		t.Fatalf("afterExpiry=%#v", afterExpiry)
	}
}

func TestWorkerFallbackScanProcessesLostWake(t *testing.T) {
	repository := newWorkerStore(t)
	wake := NewWakeSignal()
	worker := NewWorker(repository, &workerRepoFetcher{response: testRepoResponse("acme", "agent")}, wake, nil)
	worker.scanInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go worker.Run(ctx)
	// 等启动扫描先完成，再直接写 store，故意绕过 Service.Notify 模拟信号丢失。
	time.Sleep(20 * time.Millisecond)
	request := model.EnqueueBatchRequest{
		ID: "fallback-batch", SourceCode: model.SourceAIIntelligence,
		Kind: model.IngestKindManualImport, IdempotencyKey: "fallback",
		Candidates: []model.IngestCandidate{{Owner: "acme", Repo: "agent", ExternalKey: "fallback:acme/agent", OccurredAt: time.Now().UTC()}},
	}
	if _, err := repository.EnqueueIngestBatch(request); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		batch, err := repository.GetIngestBatch("fallback-batch", false)
		if err != nil {
			t.Fatal(err)
		}
		if batch != nil && batch.Status == model.IngestBatchSuccess {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fallback scan did not process persisted batch")
}

func newWorkerStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	repository, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	return repository
}

func aiImportRequest(key, owner, repo string) model.EnqueueBatchRequest {
	return model.EnqueueBatchRequest{
		SourceCode: model.SourceAIIntelligence, Kind: model.IngestKindManualImport,
		IdempotencyKey: key, Candidates: []model.IngestCandidate{{Owner: owner, Repo: repo}},
	}
}

func testRepoResponse(owner, repo string) *github.RepoResponse {
	description := "AI agent"
	return &github.RepoResponse{
		ID: 42, Owner: owner, Name: repo, FullName: owner + "/" + repo,
		Description: &description, Language: stringPointer("Go"), Stars: 100, DefaultBranch: "main",
	}
}

func stringPointer(value string) *string { return &value }

type workerRepoFetcher struct {
	response *github.RepoResponse
	err      error
	calls    int
}

func (f *workerRepoFetcher) GetRepo(context.Context, string, string) (*github.RepoResponse, error) {
	f.calls++
	return f.response, f.err
}

type countingInvalidator struct{ count int }

func (c *countingInvalidator) Invalidate() { c.count++ }

func assertProcessed(t *testing.T, worker *Worker, want int) {
	t.Helper()
	got, err := worker.ProcessAvailable(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("processed=%d want=%d", got, want)
	}
}

func assertRetryState(t *testing.T, repository *store.SQLiteStore, batchID string, attempts int, next time.Time) {
	t.Helper()
	batch, err := repository.GetIngestBatch(batchID, true)
	if err != nil {
		t.Fatal(err)
	}
	item := batch.Items[0]
	if item.Status != model.IngestItemRetrying || item.Attempts != attempts || item.NextAttemptAt != next.UTC().Format(time.RFC3339) {
		t.Fatalf("item=%#v next=%s", item, next.Format(time.RFC3339))
	}
}
