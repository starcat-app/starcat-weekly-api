package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// serviceRepositoryFake 只记录编排层的状态转换；SQL 行为由 store/discovery_test.go 单独覆盖。
type serviceRepositoryFake struct {
	repos       []model.GitHubRepo
	submissions []model.DiscoverySubmission
}

func (f *serviceRepositoryFake) UpsertGitHubRepo(repo model.GitHubRepo) error {
	f.repos = append(f.repos, repo)
	return nil
}

func (f *serviceRepositoryFake) AttachDiscoveryEvent(_ int64, submission model.DiscoverySubmission) error {
	f.submissions = append(f.submissions, submission)
	return nil
}

type submissionFetcherFake struct{ submissions []model.DiscoverySubmission }

func (f submissionFetcherFake) Fetch(context.Context, int, time.Time) ([]model.DiscoverySubmission, error) {
	return f.submissions, nil
}

type repoFetcherFake struct{ repo model.GitHubRepo }

func (f repoFetcherFake) Fetch(context.Context, string, string) (model.GitHubRepo, error) {
	return f.repo, nil
}

func TestServiceRunOnceExecutesCollectAndEnrich(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	repository := &serviceRepositoryFake{}
	service := NewService(
		repository,
		submissionFetcherFake{submissions: []model.DiscoverySubmission{{HNID: 1, Owner: "acme", Repo: "agent"}}},
		repoFetcherFake{repo: model.GitHubRepo{Owner: "acme", Name: "agent", GhRepoID: 42, IsAvailable: true}},
		Config{},
	)
	service.now = func() time.Time { return now }

	stats, err := service.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Submissions != 1 || stats.Enriched != 1 || stats.Failures != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if len(repository.submissions) != 1 || len(repository.repos) != 1 {
		t.Fatalf("pipeline writes missing: submissions=%d repos=%d",
			len(repository.submissions), len(repository.repos))
	}
}
