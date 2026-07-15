package ingest

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

func TestEnqueuePersistsDeduplicatedBatchThenWakes(t *testing.T) {
	repository, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	wake := NewWakeSignal()
	service := NewService(repository, wake)
	service.now = func() time.Time { return time.Date(2026, 7, 16, 2, 0, 0, 0, time.UTC) }
	service.newID = func() (string, error) { return "batch-1", nil }

	acceptance, err := service.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceAIIntelligence, Kind: model.IngestKindManualImport,
		IdempotencyKey: "news-20260716", Candidates: []model.IngestCandidate{
			{Owner: "Acme", Repo: "Agent", Title: "Agent"},
			{Owner: "acme", Repo: "agent", Title: "duplicate"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if acceptance.BatchID != "batch-1" || acceptance.Total != 1 || acceptance.DuplicateCount != 1 {
		t.Fatalf("acceptance=%#v", acceptance)
	}
	select {
	case <-wake.C():
	default:
		t.Fatal("commit 后应发送 wake")
	}
	batch, err := repository.GetIngestBatch("batch-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Items) != 1 || batch.Items[0].NormalizedFullName != "acme/agent" {
		t.Fatalf("batch=%#v", batch)
	}

	service.newID = func() (string, error) { return "batch-2", nil }
	replayed, err := service.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceAIIntelligence, Kind: model.IngestKindManualImport,
		IdempotencyKey: "news-20260716", Candidates: []model.IngestCandidate{{Owner: "Acme", Repo: "Agent"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.BatchID != "batch-1" {
		t.Fatalf("replayed=%#v", replayed)
	}
	select {
	case <-wake.C():
		t.Fatal("幂等重放没有新 commit，不应重复发送 wake")
	default:
	}
}

func TestEnqueueRejectsCrawlerOnlySourceForManualImport(t *testing.T) {
	service := NewService(errorBatchRepository{}, NewWakeSignal())
	_, err := service.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceHelloGitHub, Kind: model.IngestKindManualImport,
		IdempotencyKey: "invalid", Candidates: []model.IngestCandidate{{Owner: "HelloGitHub-Team", Repo: "geese"}},
	})
	if err == nil {
		t.Fatal("HelloGitHub must reject manual import")
	}
}

func TestEnqueueStoreFailureDoesNotWake(t *testing.T) {
	wake := NewWakeSignal()
	service := NewService(errorBatchRepository{err: errors.New("commit failed")}, wake)
	_, err := service.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceAIIntelligence, Kind: model.IngestKindManualImport,
		IdempotencyKey: "failure", Candidates: []model.IngestCandidate{{Owner: "acme", Repo: "agent"}},
	})
	if err == nil {
		t.Fatal("expected store error")
	}
	select {
	case <-wake.C():
		t.Fatal("failed transaction must not wake worker")
	default:
	}
}

func TestCollectorKeepsDistinctEventsForSameRepo(t *testing.T) {
	repository, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "collector-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	service := NewService(repository, NewWakeSignal())
	service.newID = func() (string, error) { return "collector-events", nil }
	acceptance, err := service.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceDiscovery, Kind: model.IngestKindCollector,
		IdempotencyKey: "collector-events", Candidates: []model.IngestCandidate{
			{Owner: "acme", Repo: "agent", ExternalKey: "hn:1:acme/agent"},
			{Owner: "acme", Repo: "agent", ExternalKey: "hn:2:acme/agent"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if acceptance.Total != 2 || acceptance.DuplicateCount != 0 {
		t.Fatalf("acceptance=%#v", acceptance)
	}
}

type errorBatchRepository struct{ err error }

func (r errorBatchRepository) EnqueueIngestBatch(model.EnqueueBatchRequest) (model.EnqueueBatchResult, error) {
	return model.EnqueueBatchResult{}, r.err
}
