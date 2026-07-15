package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestHelloGitHubBackfillControllerPersistsCursorAndResumes(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "hellogithub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := model.HelloGitHubBackfillStart{ID: "job-1", IdempotencyKey: "history-1", FromVolume: 1, ToVolume: 3}
	batch, created, err := s.CreateHelloGitHubBackfill(start)
	if err != nil || !created || batch.Total != 3 {
		t.Fatalf("batch=%+v created=%t err=%v", batch, created, err)
	}

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	claimed, cursor, err := s.NextHelloGitHubBackfill(now)
	if err != nil || claimed.ID != "job-1" || cursor.NextVolume != 1 || !cursor.Controller {
		t.Fatalf("claimed=%+v cursor=%+v err=%v", claimed, cursor, err)
	}
	cursor.NextVolume = 2
	if err := s.UpdateHelloGitHubBackfill("job-1", cursor, 3, 1, model.IngestBatchProcessing, now); err != nil {
		t.Fatal(err)
	}
	resumed, resumedCursor, err := s.NextHelloGitHubBackfill(now.Add(time.Minute))
	if err != nil || resumed.ID != "job-1" || resumedCursor.NextVolume != 2 {
		t.Fatalf("resumed=%+v cursor=%+v err=%v", resumed, resumedCursor, err)
	}

	replayed, created, err := s.CreateHelloGitHubBackfill(model.HelloGitHubBackfillStart{ID: "other", IdempotencyKey: "history-1", FromVolume: 2, ToVolume: 2})
	if err != nil || created || replayed.ID != "job-1" {
		t.Fatalf("replayed=%+v created=%t err=%v", replayed, created, err)
	}
}

func TestHelloGitHubBackfillRetryWaitsUntilDue(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "hellogithub-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _, err = s.CreateHelloGitHubBackfill(model.HelloGitHubBackfillStart{ID: "job-2", FromVolume: 5, ToVolume: 5})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	_, cursor, err := s.NextHelloGitHubBackfill(now)
	if err != nil {
		t.Fatal(err)
	}
	cursor.Attempts = 1
	cursor.NextAttemptAt = now.Add(15 * time.Minute).Format(time.RFC3339)
	if err := s.UpdateHelloGitHubBackfill("job-2", cursor, 1, 0, model.IngestBatchProcessing, now); err != nil {
		t.Fatal(err)
	}
	batch, _, err := s.NextHelloGitHubBackfill(now.Add(14 * time.Minute))
	if err != nil || batch != nil {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	batch, _, err = s.NextHelloGitHubBackfill(now.Add(15 * time.Minute))
	if err != nil || batch == nil {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
}

func TestSourceStatusKeepsActiveHelloGitHubBackfillVisible(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "hellogithub-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _, err = s.CreateHelloGitHubBackfill(model.HelloGitHubBackfillStart{ID: "controller", FromVolume: 1, ToVolume: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NextHelloGitHubBackfill(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// 子批次后创建，模拟回填已处理一期；来源状态仍应返回总任务 controller。
	if _, err := s.EnqueueIngestBatch(model.EnqueueBatchRequest{
		ID: "volume-1", SourceCode: model.SourceHelloGitHub, Kind: model.IngestKindCollector,
		Cursor: map[string]any{"volume": 1},
	}); err != nil {
		t.Fatal(err)
	}

	statuses, err := s.GetSourceStatuses(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.Code == model.SourceHelloGitHub {
			if status.LatestBatch == nil || status.LatestBatch.ID != "controller" {
				t.Fatalf("latest_batch=%+v", status.LatestBatch)
			}
			return
		}
	}
	t.Fatal("HelloGitHub source status missing")
}
