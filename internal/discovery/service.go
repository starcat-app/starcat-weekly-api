package discovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/github"
	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// Repository 是 Discovery 流水线需要的最小持久化边界（v1.2：仅 enrichment 阶段）。
type Repository interface {
	UpsertDiscoverySubmission(model.DiscoverySubmission) error
	GetDiscoveryEnrichmentCandidates(limit int, now time.Time) ([]model.DiscoveryRepo, error)
	UpdateDiscoveryEnriched(repo model.DiscoveryRepo, now time.Time) error
	UpdateDiscoveryEnrichmentFailure(owner, repo, message string, nextRetryAt time.Time) error
	MarkDiscoveryUnavailable(owner, repo, message string, now time.Time) error
}

type submissionFetcher interface {
	Fetch(ctx context.Context, limit int, now time.Time) ([]model.DiscoverySubmission, error)
}

type repoFetcher interface {
	Fetch(ctx context.Context, owner, repo string) (model.DiscoveryRepo, error)
}

// Config 控制 Discovery 每轮工作量与失败退避（v1.2：移除 LLM 分类相关配置）。
type Config struct {
	HNLimit    int
	BatchSize  int
	RetryDelay time.Duration
}

// RunStats 用于日志和 admin sync 响应后的任务排查（v1.2：移除 Classified / Rejected）。
type RunStats struct {
	Submissions int
	Enriched    int
	Failures    int
}

// Service 编排 collect → enrich 两阶段（v1.2：移除 classify 阶段）。
type Service struct {
	repository Repository
	hn         submissionFetcher
	github     repoFetcher
	config     Config
	now        func() time.Time
}

// NewService 创建 Discovery 流水线（v1.2：不再需要 llm 参数）。
func NewService(repository Repository, hn submissionFetcher, github repoFetcher, config Config) *Service {
	if config.HNLimit <= 0 {
		config.HNLimit = 30
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 20
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = time.Hour
	}
	return &Service{repository: repository, hn: hn, github: github, config: config, now: time.Now}
}

// RunOnce 执行一轮同步。collect 整体失败会返回 error；单仓库 enrich 失败会记库并继续。
func (s *Service) RunOnce(ctx context.Context) (RunStats, error) {
	now := s.now().UTC()
	stats := RunStats{}

	// Phase 1: 从 HN 采集新投稿
	submissions, err := s.hn.Fetch(ctx, s.config.HNLimit, now)
	if err != nil {
		return stats, err
	}
	for _, submission := range submissions {
		if err := s.repository.UpsertDiscoverySubmission(submission); err != nil {
			return stats, fmt.Errorf("store discovery submission: %w", err)
		}
		stats.Submissions++
	}

	// Phase 2: 补全 GitHub 元数据
	if err := s.enrich(ctx, now, &stats); err != nil {
		return stats, err
	}

	return stats, nil
}

func (s *Service) enrich(ctx context.Context, now time.Time, stats *RunStats) error {
	repos, err := s.repository.GetDiscoveryEnrichmentCandidates(s.config.BatchSize, now)
	if err != nil {
		return err
	}
	for _, candidate := range repos {
		enriched, err := s.github.Fetch(ctx, candidate.Owner, candidate.Repo)
		if err != nil {
			var httpErr *github.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				if storeErr := s.repository.MarkDiscoveryUnavailable(candidate.Owner, candidate.Repo, err.Error(), now); storeErr != nil {
					return storeErr
				}
			} else if storeErr := s.repository.UpdateDiscoveryEnrichmentFailure(candidate.Owner, candidate.Repo, err.Error(), now.Add(s.config.RetryDelay)); storeErr != nil {
				return storeErr
			}
			stats.Failures++
			log.Printf("[discovery] enrich %s/%s: %v", candidate.Owner, candidate.Repo, err)
			continue
		}
		// enrichment 完成即进入 API 可查询状态（v1.2：不再需要分类阶段）
		if err := s.repository.UpdateDiscoveryEnriched(enriched, now); err != nil {
			return err
		}
		stats.Enriched++
	}
	return nil
}
