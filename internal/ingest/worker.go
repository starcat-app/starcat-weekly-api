package ingest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/github"
	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

const (
	defaultScanInterval  = 15 * time.Minute
	defaultLeaseDuration = 30 * time.Minute
)

type workerRepository interface {
	ClaimIngestItem(workerID string, now time.Time, leaseDuration time.Duration) (*model.IngestWorkItem, error)
	GetGitHubRepoByOwnerName(owner, name string) (*model.GitHubRepo, error)
	CompleteIngestItem(work model.IngestWorkItem, repo model.GitHubRepo, now time.Time) (bool, error)
	FailIngestItem(work model.IngestWorkItem, code, message string, permanent bool, now time.Time) (model.IngestFailureResult, error)
}

type repoFetcher interface {
	GetRepo(ctx context.Context, owner, repo string) (*github.RepoResponse, error)
}

type cacheInvalidator interface {
	Invalidate()
}

type noopInvalidator struct{}

func (noopInvalidator) Invalidate() {}

// Worker 串行消费持久化候选队列。
//
// SQLite claim 与完成各自使用短事务，GitHub 请求严格位于事务外。服务重启时
// 启动扫描会恢复任务；运行中优先响应 wake，15 分钟 ticker 只负责信号丢失兜底。
type Worker struct {
	repository    workerRepository
	github        repoFetcher
	wake          *WakeSignal
	cache         cacheInvalidator
	workerID      string
	scanInterval  time.Duration
	leaseDuration time.Duration
	now           func() time.Time
}

func NewWorker(repository workerRepository, githubClient repoFetcher, wake *WakeSignal, cache cacheInvalidator) *Worker {
	if cache == nil {
		cache = noopInvalidator{}
	}
	return &Worker{
		repository: repository, github: githubClient, wake: wake, cache: cache,
		workerID: "weekly-enrich-worker", scanInterval: defaultScanInterval,
		leaseDuration: defaultLeaseDuration, now: time.Now,
	}
}

// Run 在调用 goroutine 内持续工作，ctx 取消后退出。
func (w *Worker) Run(ctx context.Context) {
	if _, err := w.ProcessAvailable(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[ingest-worker] startup scan: %v", err)
	}
	ticker := time.NewTicker(w.scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake.C():
			if _, err := w.ProcessAvailable(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[ingest-worker] wake scan: %v", err)
			}
		case <-ticker.C:
			if _, err := w.ProcessAvailable(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[ingest-worker] fallback scan: %v", err)
			}
		}
	}
}

// ProcessAvailable 持续 drain 当前可领取任务；retrying 的未来任务留给下次 wake/ticker。
func (w *Worker) ProcessAvailable(ctx context.Context) (int, error) {
	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		now := w.now().UTC()
		work, err := w.repository.ClaimIngestItem(w.workerID, now, w.leaseDuration)
		if err != nil {
			return processed, err
		}
		if work == nil {
			return processed, nil
		}
		processed++
		repo, err := w.resolveRepo(ctx, *work, now)
		if err != nil {
			code, permanent := classifyGitHubError(err)
			failure, storeErr := w.repository.FailIngestItem(*work, code, err.Error(), permanent, now)
			if storeErr != nil {
				return processed, storeErr
			}
			if failure.BatchTerminal {
				w.cache.Invalidate()
			}
			continue
		}
		terminal, err := w.repository.CompleteIngestItem(*work, repo, now)
		if err != nil {
			// 数据库完成事务失败时保留 processing 租约；不能把未提交任务误标成功。
			// 服务重启或 30 分钟租约到期后会自动恢复领取。
			return processed, err
		}
		if terminal {
			w.cache.Invalidate()
		}
	}
}

func (w *Worker) resolveRepo(ctx context.Context, work model.IngestWorkItem, now time.Time) (model.GitHubRepo, error) {
	existing, err := w.repository.GetGitHubRepoByOwnerName(work.Owner, work.Repo)
	if err != nil {
		return model.GitHubRepo{}, err
	}
	if existing != nil && existing.IsAvailable && existing.EnrichedAt != nil && now.Sub(existing.EnrichedAt.UTC()) < 24*time.Hour {
		return *existing, nil
	}
	response, err := w.github.GetRepo(ctx, work.Owner, work.Repo)
	if err != nil {
		return model.GitHubRepo{}, err
	}
	owner := response.Owner
	if owner == "" {
		owner = work.Owner
	}
	name := response.Name
	if name == "" {
		name = work.Repo
	}
	fullName := response.FullName
	if fullName == "" {
		fullName = owner + "/" + name
	}
	enrichedAt := now
	return model.GitHubRepo{
		GhRepoID: response.ID, Owner: owner, Name: name, FullName: fullName,
		Description: pointerValue(response.Description), Homepage: pointerValue(response.Homepage),
		Language: pointerValue(response.Language), Stars: response.Stars, Forks: response.Forks,
		Watchers: response.Watchers, Subscribers: response.Subscribers, OpenIssues: response.OpenIssues,
		OwnerAvatar: pointerValue(response.OwnerAvatar), DefaultBranch: response.DefaultBranch,
		LicenseSpdx: pointerValue(response.LicenseSpdx), Topics: response.Topics,
		PushedAt: response.PushedAt, UpdatedAt: response.UpdatedAt, CreatedAt: response.CreatedAt,
		IsArchived: response.Archived, IsFork: response.Fork, IsPrivate: response.Private,
		FirstEventAt: work.OccurredAt, LatestEventAt: work.OccurredAt,
		EnrichedAt: &enrichedAt, IsAvailable: true,
	}, nil
}

func classifyGitHubError(err error) (string, bool) {
	if errors.Is(err, github.ErrRepoNotFound) {
		return "github_not_found", true
	}
	if errors.Is(err, github.ErrRateLimited) {
		return "github_rate_limited", false
	}
	var httpError *github.HTTPError
	if errors.As(err, &httpError) {
		if httpError.StatusCode == http.StatusUnprocessableEntity {
			return "github_unprocessable", true
		}
		return fmt.Sprintf("github_http_%d", httpError.StatusCode), false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "github_timeout", false
	}
	return "github_error", false
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
