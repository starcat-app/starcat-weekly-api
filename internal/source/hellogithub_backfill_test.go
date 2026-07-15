package source

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

type backfillRepositoryStub struct {
	batch   *model.IngestBatch
	cursor  model.HelloGitHubBackfillCursor
	created bool
}

func (s *backfillRepositoryStub) CreateHelloGitHubBackfill(start model.HelloGitHubBackfillStart) (*model.IngestBatch, bool, error) {
	s.batch = &model.IngestBatch{ID: start.ID, SourceCode: model.SourceHelloGitHub, Status: model.IngestBatchPending}
	s.cursor = model.HelloGitHubBackfillCursor{Controller: true, FromVolume: start.FromVolume, ToVolume: start.ToVolume, NextVolume: start.FromVolume}
	return s.batch, true, nil
}
func (s *backfillRepositoryStub) NextHelloGitHubBackfill(time.Time) (*model.IngestBatch, model.HelloGitHubBackfillCursor, error) {
	if s.batch == nil || s.batch.Status == model.IngestBatchSuccess || s.batch.Status == model.IngestBatchFailed || s.cursor.NextAttemptAt != "" {
		return nil, model.HelloGitHubBackfillCursor{}, nil
	}
	return s.batch, s.cursor, nil
}
func (s *backfillRepositoryStub) UpdateHelloGitHubBackfill(_ string, cursor model.HelloGitHubBackfillCursor, total, success int, status string, _ time.Time) error {
	s.cursor = cursor
	s.batch.Total = total
	s.batch.Success = success
	s.batch.Status = status
	return nil
}

type volumeFetcherStub struct {
	volume HelloGitHubVolume
	err    error
}

func (s *volumeFetcherStub) FetchVolume(context.Context, int) (HelloGitHubVolume, error) {
	return s.volume, s.err
}

func TestHelloGitHubBackfillProcessesVolumesAndCompletes(t *testing.T) {
	repository := &backfillRepositoryStub{
		batch:  &model.IngestBatch{ID: "parent", SourceCode: model.SourceHelloGitHub, Status: model.IngestBatchPending},
		cursor: model.HelloGitHubBackfillCursor{Controller: true, FromVolume: 1, NextVolume: 1},
	}
	fetcher := &volumeFetcherStub{volume: HelloGitHubVolume{
		Number: 1, Latest: 1,
		Candidates: []model.IngestCandidate{{Owner: "owner", Repo: "repo", ExternalKey: "volume:1:owner/repo"}},
	}}
	enqueuer := &helloGitHubEnqueuerStub{}
	manager := NewHelloGitHubBackfillManager(repository, fetcher, enqueuer)
	manager.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }

	processed, err := manager.ProcessNext(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%t err=%v", processed, err)
	}
	if repository.batch.Status != model.IngestBatchSuccess || repository.batch.Success != 1 || repository.cursor.NextVolume != 2 {
		t.Fatalf("batch=%+v cursor=%+v", repository.batch, repository.cursor)
	}
	if len(enqueuer.requests) != 1 || enqueuer.requests[0].Kind != model.IngestKindBackfill {
		t.Fatalf("requests=%+v", enqueuer.requests)
	}
}

func TestHelloGitHubBackfillRetriesThenFails(t *testing.T) {
	repository := &backfillRepositoryStub{
		batch:  &model.IngestBatch{ID: "parent", SourceCode: model.SourceHelloGitHub, Status: model.IngestBatchProcessing, Total: 1},
		cursor: model.HelloGitHubBackfillCursor{Controller: true, FromVolume: 1, ToVolume: 1, NextVolume: 1},
	}
	manager := NewHelloGitHubBackfillManager(repository, &volumeFetcherStub{err: errors.New("upstream changed")}, &helloGitHubEnqueuerStub{})
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	for attempt := 1; attempt <= 3; attempt++ {
		repository.cursor.NextAttemptAt = "" // 模拟时间已到，由 store 在真实运行中负责 due 判断。
		if _, err := manager.ProcessNext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if repository.batch.Status != model.IngestBatchFailed || repository.cursor.Attempts != 3 || repository.cursor.LastError == "" {
		t.Fatalf("batch=%+v cursor=%+v", repository.batch, repository.cursor)
	}
}

func TestHelloGitHubBackfillStartValidatesRangeAndWakesAfterCreate(t *testing.T) {
	repository := &backfillRepositoryStub{}
	manager := NewHelloGitHubBackfillManager(repository, &volumeFetcherStub{}, &helloGitHubEnqueuerStub{})
	manager.newID = func() (string, error) { return "job", nil }
	if _, err := manager.Start(3, 2, "key"); err == nil {
		t.Fatal("expected invalid range")
	}
	acceptance, err := manager.Start(1, 0, "key")
	if err != nil || acceptance.BatchID != "job" {
		t.Fatalf("acceptance=%+v err=%v", acceptance, err)
	}
	select {
	case <-manager.wake:
	default:
		t.Fatal("expected commit-time wake")
	}
}
