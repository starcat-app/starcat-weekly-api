// 超时与取消语义测试使用模拟仓库，避免真实等待两分钟或调用 GitHub。
package dailyradar

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/github"
)

type workerStore struct{ published *Detail }

func (s *workerStore) EnqueueRadar(ImportRequest) (*Batch, error)                { return nil, nil }
func (s *workerStore) RadarBatch(string) (*Batch, error)                         { return nil, nil }
func (s *workerStore) RecoverRadarJobs() error                                   { return nil }
func (s *workerStore) ClaimRadarJob(time.Time) (*Job, error)                     { return nil, nil }
func (s *workerStore) FailRadarJob(int64, string, time.Time, bool) error         { return nil }
func (s *workerStore) PublishRadar(_ Job, d Detail) error                        { s.published = &d; return nil }
func (s *workerStore) RadarSnapshot() (Snapshot, error)                          { return Snapshot{}, nil }
func (s *workerStore) RadarDetail(string, string, string, bool) (*Detail, error) { return nil, nil }

type deadlineClient struct{}

func (deadlineClient) GetRepo(ctx context.Context, _, _ string) (*github.RepoResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFinalDeadlinePublishesPartialButParentCancellationDoesNot(t *testing.T) {
	job := Job{Attempts: 3, Request: ImportRequest{Source: "agents-radar", SourceKind: "upstream", Reports: []Candidate{{
		EditionDate: "2026-10-28", Category: "ai-cli", Language: "en", Title: "AI CLI",
		Markdown: "# Report\n\n[Project](https://github.com/o/r)", SourceCommit: strings.Repeat("a", 40),
		SourcePath: "digests/2026-10-28/ai-cli.md", GeneratedAt: "2026-10-28T01:00:00Z",
		Cover: Cover{Kind: "category", Fallback: "orbit"},
	}}}}
	s := &workerStore{}
	w := NewWorker(s, deadlineClient{})
	w.jobTimeout = time.Millisecond
	if err := w.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if s.published == nil || s.published.RelatedProjectsStatus != "partial" {
		t.Fatal("final timeout lost readable body")
	}
	s.published = nil
	job.Attempts = 1
	if err := w.Process(context.Background(), job); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if s.published != nil {
		t.Fatal("early temporary failure should retry")
	}
	job.Attempts = 3
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Process(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.published != nil {
		t.Fatal("shutdown must not publish partial content")
	}
}
