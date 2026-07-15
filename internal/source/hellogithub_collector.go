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

// HelloGitHubReconcileStats 描述最新月刊对账创建的持久化批次。
type HelloGitHubReconcileStats struct {
	Volume  int    `json:"volume"`
	Fetched int    `json:"fetched"`
	Queued  int    `json:"queued"`
	BatchID string `json:"batch_id,omitempty"`
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

// ReconcileLatest 先从任一期结构化响应探测最新期号，再把最新月刊作为普通采集批次入队。
// 月刊 external_key 与历史回填一致，因此同一来源事实最终只保留一条。
func (c *HelloGitHubCollector) ReconcileLatest(ctx context.Context) (HelloGitHubReconcileStats, error) {
	volumeFetcher, ok := c.fetcher.(helloGitHubVolumeFetcher)
	if !ok {
		return HelloGitHubReconcileStats{}, fmt.Errorf("HelloGitHub fetcher does not support periodical volumes")
	}
	probe, err := volumeFetcher.FetchVolume(ctx, 1)
	if err != nil {
		return HelloGitHubReconcileStats{}, err
	}
	latest := probe
	if probe.Latest != probe.Number {
		latest, err = volumeFetcher.FetchVolume(ctx, probe.Latest)
		if err != nil {
			return HelloGitHubReconcileStats{}, err
		}
	}
	acceptance, err := c.enqueuer.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceHelloGitHub,
		Kind:       model.IngestKindCollector,
		IdempotencyKey: fmt.Sprintf("hellogithub:reconcile:%d:%s", latest.Number,
			candidateFingerprint(latest.Candidates)),
		Cursor:     map[string]any{"volume": latest.Number, "reconcile": true},
		Candidates: latest.Candidates,
	})
	if err != nil {
		return HelloGitHubReconcileStats{}, err
	}
	return HelloGitHubReconcileStats{Volume: latest.Number, Fetched: len(latest.Candidates), Queued: acceptance.Total, BatchID: acceptance.BatchID}, nil
}

func candidateFingerprint(candidates []model.IngestCandidate) string {
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, strings.ToLower(candidate.ExternalKey+"\x00"+candidate.Owner+"/"+candidate.Repo))
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:8])
}
