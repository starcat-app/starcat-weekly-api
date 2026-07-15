package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

type helloGitHubPageFetcher interface {
	FetchFeaturedPage(context.Context, int) ([]model.IngestCandidate, error)
}

type helloGitHubBatchEnqueuer interface {
	Enqueue(model.EnqueueBatchRequest) (model.IngestBatchAcceptance, error)
}

// HelloGitHubRunStats 描述一次精选增量采集的抓取和入队结果。
type HelloGitHubRunStats struct {
	Pages     int      `json:"pages"`
	Fetched   int      `json:"fetched"`
	Queued    int      `json:"queued"`
	BatchIDs  []string `json:"batch_ids,omitempty"`
	Exhausted bool     `json:"exhausted"`
}

// HelloGitHubCollector 把精选列表转换成持久化批次。
// 每页单独提交，避免后续页失败时丢掉已经成功抓取的前序页面。
type HelloGitHubCollector struct {
	fetcher  helloGitHubPageFetcher
	enqueuer helloGitHubBatchEnqueuer
	maxPages int
}

func NewHelloGitHubCollector(fetcher helloGitHubPageFetcher, enqueuer helloGitHubBatchEnqueuer, maxPages int) *HelloGitHubCollector {
	if maxPages <= 0 {
		maxPages = 3
	}
	return &HelloGitHubCollector{fetcher: fetcher, enqueuer: enqueuer, maxPages: maxPages}
}

// RunFeatured 抓取配置上限内的精选页面；遇到空页立即停止。
// 幂等键由本页 external keys 计算，内容未变化时不会重复创建批次或唤醒 Worker。
func (c *HelloGitHubCollector) RunFeatured(ctx context.Context) (HelloGitHubRunStats, error) {
	stats := HelloGitHubRunStats{}
	globalRank := 0
	for page := 1; page <= c.maxPages; page++ {
		candidates, err := c.fetcher.FetchFeaturedPage(ctx, page)
		if err != nil {
			return stats, err
		}
		stats.Pages++
		if len(candidates) == 0 {
			stats.Exhausted = true
			break
		}
		for index := range candidates {
			globalRank++
			position := globalRank
			candidates[index].Rank = &position
		}
		stats.Fetched += len(candidates)
		acceptance, err := c.enqueuer.Enqueue(model.EnqueueBatchRequest{
			SourceCode: model.SourceHelloGitHub,
			Kind:       model.IngestKindCollector,
			// 内容指纹既支持相同页面幂等重放，也允许精选内容更新后创建新批次。
			IdempotencyKey: "hellogithub:featured:" + candidateFingerprint(candidates),
			Cursor:         map[string]any{"featured_page": page},
			Candidates:     candidates,
		})
		if err != nil {
			return stats, fmt.Errorf("enqueue HelloGitHub featured page %d: %w", page, err)
		}
		stats.Queued += acceptance.Total
		stats.BatchIDs = append(stats.BatchIDs, acceptance.BatchID)
	}
	return stats, nil
}

func candidateFingerprint(candidates []model.IngestCandidate) string {
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, strings.ToLower(candidate.ExternalKey+"\x00"+candidate.Owner+"/"+candidate.Repo))
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:8])
}
