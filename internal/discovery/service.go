package discovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// Repository 是 Discovery 流水线需要的最小持久化边界。
type Repository interface {
	UpsertDiscoverySubmission(model.DiscoverySubmission) error
	GetDiscoveryEnrichmentCandidates(limit int, now time.Time) ([]model.DiscoveryRepo, error)
	UpdateDiscoveryEnriched(repo model.DiscoveryRepo, now time.Time) error
	UpdateDiscoveryEnrichmentFailure(owner, repo, message string, nextRetryAt time.Time) error
	MarkDiscoveryUnavailable(owner, repo, message string, now time.Time) error
	GetDiscoveryClassificationCandidates(limit int, now time.Time) ([]model.DiscoveryRepo, error)
	UpdateDiscoveryClassified(owner, repo, category string, confidence float64, reason, method, classifierModel string, rejected bool, now time.Time) error
	UpdateDiscoveryClassificationFailure(owner, repo, message string, nextRetryAt time.Time, resetAttempts bool) error
}

type submissionFetcher interface {
	Fetch(ctx context.Context, limit int, now time.Time) ([]model.DiscoverySubmission, error)
}

type repoFetcher interface {
	Fetch(ctx context.Context, owner, repo string) (model.DiscoveryRepo, error)
}

type classifier interface {
	Classify(ctx context.Context, repo model.DiscoveryRepo) (Classification, error)
	Model() string
}

// Config 控制 Discovery 每轮工作量与失败退避。
type Config struct {
	HNLimit             int
	BatchSize           int
	ConfidenceThreshold float64
	MaxClassifyAttempts int
	ClassifyCooldown    time.Duration
	RetryDelay          time.Duration
}

// RunStats 用于日志和 admin sync 响应后的任务排查。
type RunStats struct {
	Submissions int
	Enriched    int
	Classified  int
	Rejected    int
	Failures    int
}

// Service 编排 collect -> enrich -> classify 三段；每条失败只影响自身。
type Service struct {
	repository Repository
	hn         submissionFetcher
	github     repoFetcher
	classifier classifier
	config     Config
	now        func() time.Time
}

// NewService 创建 Discovery 流水线。
func NewService(repository Repository, hn submissionFetcher, github repoFetcher, llm classifier, config Config) *Service {
	if config.HNLimit <= 0 {
		config.HNLimit = 30
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 20
	}
	if config.ConfidenceThreshold <= 0 {
		config.ConfidenceThreshold = 0.6
	}
	if config.MaxClassifyAttempts <= 0 {
		config.MaxClassifyAttempts = 3
	}
	if config.ClassifyCooldown <= 0 {
		config.ClassifyCooldown = 7 * 24 * time.Hour
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = time.Hour
	}
	return &Service{repository: repository, hn: hn, github: github, classifier: llm, config: config, now: time.Now}
}

// RunOnce 执行一轮同步。collect 整体失败会返回 error；单仓库 enrich/classify 失败会记库并继续。
func (s *Service) RunOnce(ctx context.Context) (RunStats, error) {
	now := s.now().UTC()
	stats := RunStats{}
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

	if err := s.enrich(ctx, now, &stats); err != nil {
		return stats, err
	}
	if s.classifier != nil {
		if err := s.classify(ctx, now, &stats); err != nil {
			return stats, err
		}
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
			var httpErr *GitHubHTTPError
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
		if err := s.repository.UpdateDiscoveryEnriched(enriched, now); err != nil {
			return err
		}
		stats.Enriched++
	}
	return nil
}

func (s *Service) classify(ctx context.Context, now time.Time, stats *RunStats) error {
	repos, err := s.repository.GetDiscoveryClassificationCandidates(s.config.BatchSize, now)
	if err != nil {
		return err
	}
	for _, candidate := range repos {
		result, err := s.classifier.Classify(ctx, candidate)
		if err != nil {
			nextAttempt := candidate.ClassifyAttempts + 1
			coolingDown := nextAttempt >= s.config.MaxClassifyAttempts
			nextRetry := now.Add(s.config.RetryDelay)
			if coolingDown {
				nextRetry = now.Add(s.config.ClassifyCooldown)
			}
			if storeErr := s.repository.UpdateDiscoveryClassificationFailure(candidate.Owner, candidate.Repo, err.Error(), nextRetry, coolingDown); storeErr != nil {
				return storeErr
			}
			stats.Failures++
			log.Printf("[discovery] classify %s/%s: %v", candidate.Owner, candidate.Repo, err)
			continue
		}
		rejected := result.Category == model.DiscoveryCategoryUnknown || result.Confidence < s.config.ConfidenceThreshold
		if err := s.repository.UpdateDiscoveryClassified(candidate.Owner, candidate.Repo, result.Category, result.Confidence,
			result.Reason, "llm", s.classifier.Model(), rejected, now); err != nil {
			return err
		}
		if rejected {
			stats.Rejected++
		} else {
			stats.Classified++
		}
	}
	return nil
}
