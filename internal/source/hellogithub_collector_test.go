package source

import (
	"context"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

type helloGitHubFetcherStub struct {
	pages map[int][]model.IngestCandidate
	calls []int
}

func (s *helloGitHubFetcherStub) FetchFeaturedPage(_ context.Context, page int) ([]model.IngestCandidate, error) {
	s.calls = append(s.calls, page)
	return s.pages[page], nil
}

type helloGitHubEnqueuerStub struct {
	requests []model.EnqueueBatchRequest
}

func (s *helloGitHubEnqueuerStub) Enqueue(request model.EnqueueBatchRequest) (model.IngestBatchAcceptance, error) {
	s.requests = append(s.requests, request)
	return model.IngestBatchAcceptance{BatchID: request.IdempotencyKey, Total: len(request.Candidates)}, nil
}

func TestHelloGitHubCollectorStopsAtEmptyPageAndUsesGlobalRank(t *testing.T) {
	fetcher := &helloGitHubFetcherStub{pages: map[int][]model.IngestCandidate{
		1: {{Owner: "one", Repo: "first", ExternalKey: "featured:1", OccurredAt: time.Now()}},
		2: {{Owner: "two", Repo: "second", ExternalKey: "featured:2", OccurredAt: time.Now()}},
		3: {},
	}}
	enqueuer := &helloGitHubEnqueuerStub{}
	collector := NewHelloGitHubCollector(fetcher, enqueuer, 10)

	stats, err := collector.RunFeatured(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pages != 3 || stats.Fetched != 2 || stats.Queued != 2 || !stats.Exhausted {
		t.Fatalf("stats=%+v", stats)
	}
	if len(enqueuer.requests) != 2 || *enqueuer.requests[0].Candidates[0].Rank != 1 || *enqueuer.requests[1].Candidates[0].Rank != 2 {
		t.Fatalf("requests=%+v", enqueuer.requests)
	}
	if enqueuer.requests[0].SourceCode != model.SourceHelloGitHub || enqueuer.requests[0].Kind != model.IngestKindCollector {
		t.Fatalf("request=%+v", enqueuer.requests[0])
	}
}

func TestHelloGitHubCollectorFingerprintIsStable(t *testing.T) {
	candidates := []model.IngestCandidate{{Owner: "Owner", Repo: "Repo", ExternalKey: "featured:1"}}
	first := candidateFingerprint(candidates)
	second := candidateFingerprint(candidates)
	if first != second || len(first) != 16 {
		t.Fatalf("fingerprints=%q/%q", first, second)
	}
}
