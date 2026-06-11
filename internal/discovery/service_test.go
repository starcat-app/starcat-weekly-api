package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// serviceRepositoryFake 只记录编排层的状态转换；SQL 行为由 store/discovery_test.go 单独覆盖。
type serviceRepositoryFake struct {
	enrichmentCandidates     []model.DiscoveryRepo
	classificationCandidates []model.DiscoveryRepo
	submissions              []model.DiscoverySubmission
	enriched                 []model.DiscoveryRepo
	classified               []classificationWrite
	failure                  classificationFailure
}

type classificationWrite struct {
	owner      string
	repo       string
	category   string
	confidence float64
	rejected   bool
}

type classificationFailure struct {
	nextRetry     time.Time
	resetAttempts bool
}

func (f *serviceRepositoryFake) UpsertDiscoverySubmission(submission model.DiscoverySubmission) error {
	f.submissions = append(f.submissions, submission)
	return nil
}

func (f *serviceRepositoryFake) GetDiscoveryEnrichmentCandidates(int, time.Time) ([]model.DiscoveryRepo, error) {
	return f.enrichmentCandidates, nil
}

func (f *serviceRepositoryFake) UpdateDiscoveryEnriched(repo model.DiscoveryRepo, _ time.Time) error {
	f.enriched = append(f.enriched, repo)
	return nil
}

func (f *serviceRepositoryFake) UpdateDiscoveryEnrichmentFailure(string, string, string, time.Time) error {
	return nil
}

func (f *serviceRepositoryFake) MarkDiscoveryUnavailable(string, string, string, time.Time) error {
	return nil
}

func (f *serviceRepositoryFake) GetDiscoveryClassificationCandidates(int, time.Time) ([]model.DiscoveryRepo, error) {
	return f.classificationCandidates, nil
}

func (f *serviceRepositoryFake) UpdateDiscoveryClassified(owner, repo, category string, confidence float64, _, _, _ string, rejected bool, _ time.Time) error {
	f.classified = append(f.classified, classificationWrite{
		owner: owner, repo: repo, category: category, confidence: confidence, rejected: rejected,
	})
	return nil
}

func (f *serviceRepositoryFake) UpdateDiscoveryClassificationFailure(_, _, _ string, nextRetryAt time.Time, resetAttempts bool) error {
	f.failure = classificationFailure{nextRetry: nextRetryAt, resetAttempts: resetAttempts}
	return nil
}

type submissionFetcherFake struct{ submissions []model.DiscoverySubmission }

func (f submissionFetcherFake) Fetch(context.Context, int, time.Time) ([]model.DiscoverySubmission, error) {
	return f.submissions, nil
}

type repoFetcherFake struct{ repo model.DiscoveryRepo }

func (f repoFetcherFake) Fetch(context.Context, string, string) (model.DiscoveryRepo, error) {
	return f.repo, nil
}

type classifierFake struct {
	result Classification
	err    error
}

func (f classifierFake) Classify(context.Context, model.DiscoveryRepo) (Classification, error) {
	return f.result, f.err
}

func (classifierFake) Model() string { return "test-model" }

func TestServiceRunOnceExecutesCollectEnrichAndClassify(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	repository := &serviceRepositoryFake{
		enrichmentCandidates:     []model.DiscoveryRepo{{Owner: "acme", Repo: "agent"}},
		classificationCandidates: []model.DiscoveryRepo{{Owner: "acme", Repo: "agent"}},
	}
	service := NewService(
		repository,
		submissionFetcherFake{submissions: []model.DiscoverySubmission{{HNID: 1, Owner: "acme", Repo: "agent"}}},
		repoFetcherFake{repo: model.DiscoveryRepo{Owner: "acme", Repo: "agent", GhRepoID: 42}},
		classifierFake{result: Classification{Category: "agent", Confidence: 0.91}},
		Config{},
	)
	service.now = func() time.Time { return now }

	stats, err := service.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Submissions != 1 || stats.Enriched != 1 || stats.Classified != 1 || stats.Failures != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if len(repository.submissions) != 1 || len(repository.enriched) != 1 || len(repository.classified) != 1 {
		t.Fatalf("pipeline writes missing: submissions=%d enriched=%d classified=%d",
			len(repository.submissions), len(repository.enriched), len(repository.classified))
	}
	if repository.classified[0].rejected || repository.classified[0].category != "agent" {
		t.Fatalf("unexpected classification write: %#v", repository.classified[0])
	}
}

func TestServiceClassificationFailureEntersCooldownAtThreshold(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	repository := &serviceRepositoryFake{
		classificationCandidates: []model.DiscoveryRepo{{
			Owner: "acme", Repo: "agent", ClassifyAttempts: 2,
		}},
	}
	service := NewService(
		repository,
		submissionFetcherFake{},
		repoFetcherFake{},
		classifierFake{err: errors.New("temporary LLM error")},
		Config{MaxClassifyAttempts: 3, ClassifyCooldown: 7 * 24 * time.Hour},
	)
	service.now = func() time.Time { return now }

	stats, err := service.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failures != 1 || !repository.failure.resetAttempts {
		t.Fatalf("expected cooldown failure, stats=%#v write=%#v", stats, repository.failure)
	}
	if want := now.Add(7 * 24 * time.Hour); !repository.failure.nextRetry.Equal(want) {
		t.Fatalf("want next retry %s, got %s", want, repository.failure.nextRetry)
	}
}
