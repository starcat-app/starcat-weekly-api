package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

type submissionFetcher interface {
	Fetch(ctx context.Context, limit int, now time.Time) ([]model.DiscoverySubmission, error)
}

type RunStats struct {
	Submissions int
	Queued      int
	BatchID     string
}

type batchEnqueuer interface {
	Enqueue(model.EnqueueBatchRequest) (model.IngestBatchAcceptance, error)
}

// Collector 只负责把 Show HN 投稿转换成持久化候选，不再同步请求 GitHub。
type Collector struct {
	hn       submissionFetcher
	enqueuer batchEnqueuer
	limit    int
	now      func() time.Time
}

func NewCollector(hn submissionFetcher, enqueuer batchEnqueuer, limit int) *Collector {
	if limit <= 0 {
		limit = 30
	}
	return &Collector{hn: hn, enqueuer: enqueuer, limit: limit, now: time.Now}
}

func (c *Collector) RunOnce(ctx context.Context) (RunStats, error) {
	now := c.now().UTC()
	submissions, err := c.hn.Fetch(ctx, c.limit, now)
	if err != nil {
		return RunStats{}, err
	}
	stats := RunStats{Submissions: len(submissions)}
	if len(submissions) == 0 {
		return stats, nil
	}
	candidates := make([]model.IngestCandidate, 0, len(submissions))
	for _, submission := range submissions {
		candidates = append(candidates, model.IngestCandidate{
			Owner: submission.Owner, Repo: submission.Repo,
			ExternalKey: fmt.Sprintf("hn:%d:%s/%s", submission.HNID, strings.ToLower(submission.Owner), strings.ToLower(submission.Repo)), OccurredAt: submission.PublishedAt,
			SourceURL: submission.HNURL, Title: submission.Title,
			Payload: map[string]any{
				"hn_id": submission.HNID, "score": submission.Score, "comments": submission.Comments,
				"github_source_url": submission.SourceURL,
			},
		})
	}
	acceptance, err := c.enqueuer.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceDiscovery, Kind: model.IngestKindCollector,
		IdempotencyKey: "discovery:" + now.Format(time.RFC3339Nano), Candidates: candidates,
	})
	if err != nil {
		return stats, err
	}
	stats.Queued = acceptance.Total
	stats.BatchID = acceptance.BatchID
	return stats, nil
}
