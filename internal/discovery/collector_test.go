package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestCollectorEnqueuesShowHNBatchWithoutGitHubEnrich(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	enqueuer := &collectorEnqueuerFake{}
	collector := NewCollector(submissionFetcherFake{submissions: []model.DiscoverySubmission{
		{HNID: 101, Owner: "Acme", Repo: "Agent", Title: "Show HN: Agent", HNURL: "https://news.ycombinator.com/item?id=101", SourceURL: "https://github.com/Acme/Agent", Score: 20, Comments: 3, PublishedAt: now.Add(-time.Hour)},
		{HNID: 101, Owner: "Acme", Repo: "Tools", Title: "Show HN: Agent", HNURL: "https://news.ycombinator.com/item?id=101", SourceURL: "https://github.com/Acme/Tools", Score: 20, Comments: 3, PublishedAt: now.Add(-time.Hour)},
	}}, enqueuer, 30)
	collector.now = func() time.Time { return now }

	stats, err := collector.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Submissions != 2 || stats.Queued != 2 || stats.BatchID != "discovery-batch" {
		t.Fatalf("stats=%#v", stats)
	}
	request := enqueuer.request
	if request.SourceCode != model.SourceDiscovery || request.Kind != model.IngestKindCollector || len(request.Candidates) != 2 {
		t.Fatalf("request=%#v", request)
	}
	if request.Candidates[0].ExternalKey == request.Candidates[1].ExternalKey {
		t.Fatalf("同一 HN 投稿内多个仓库必须拥有独立事件 key: %#v", request.Candidates)
	}
}

type collectorEnqueuerFake struct{ request model.EnqueueBatchRequest }

func (f *collectorEnqueuerFake) Enqueue(request model.EnqueueBatchRequest) (model.IngestBatchAcceptance, error) {
	f.request = request
	return model.IngestBatchAcceptance{BatchID: "discovery-batch", Total: len(request.Candidates)}, nil
}

type submissionFetcherFake struct{ submissions []model.DiscoverySubmission }

func (f submissionFetcherFake) Fetch(context.Context, int, time.Time) ([]model.DiscoverySubmission, error) {
	return f.submissions, nil
}

var _ submissionFetcher = submissionFetcherFake{}
