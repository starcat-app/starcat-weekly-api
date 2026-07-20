package source

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

const helloGitHubBackfillScanInterval = 15 * time.Minute

type helloGitHubVolumeFetcher interface {
	FetchVolume(context.Context, int) (HelloGitHubVolume, error)
}

type helloGitHubBackfillRepository interface {
	CreateHelloGitHubBackfill(model.HelloGitHubBackfillStart) (*model.IngestBatch, bool, error)
	NextHelloGitHubBackfill(time.Time) (*model.IngestBatch, model.HelloGitHubBackfillCursor, error)
	UpdateHelloGitHubBackfill(string, model.HelloGitHubBackfillCursor, int, int, string, time.Time) error
}

// HelloGitHubBackfillManager 负责历史期号 checkpoint、恢复和页面级重试。
// 月刊解析后仍通过统一 Enqueue 服务交给 GitHub enrich Worker，不直接写仓库主表。
type HelloGitHubBackfillManager struct {
	repository helloGitHubBackfillRepository
	fetcher    helloGitHubVolumeFetcher
	enqueuer   helloGitHubBatchEnqueuer
	wake       chan struct{}
	now        func() time.Time
	newID      func() (string, error)
}

func NewHelloGitHubBackfillManager(repository helloGitHubBackfillRepository, fetcher helloGitHubVolumeFetcher, enqueuer helloGitHubBatchEnqueuer) *HelloGitHubBackfillManager {
	return &HelloGitHubBackfillManager{
		repository: repository, fetcher: fetcher, enqueuer: enqueuer,
		wake: make(chan struct{}, 1), now: time.Now, newID: newHelloGitHubBackfillID,
	}
}

// Start 只创建持久化控制批次；内存信号严格发生在数据库 commit 返回之后。
func (m *HelloGitHubBackfillManager) Start(fromVolume, toVolume int, idempotencyKey string) (model.IngestBatchAcceptance, error) {
	if fromVolume < 1 {
		return model.IngestBatchAcceptance{}, fmt.Errorf("from_volume must be positive")
	}
	if toVolume != 0 && toVolume < fromVolume {
		return model.IngestBatchAcceptance{}, fmt.Errorf("to_volume must be null or greater than or equal to from_volume")
	}
	id, err := m.newID()
	if err != nil {
		return model.IngestBatchAcceptance{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = "hellogithub:backfill:" + id
	}
	batch, created, err := m.repository.CreateHelloGitHubBackfill(model.HelloGitHubBackfillStart{
		ID: id, IdempotencyKey: idempotencyKey, FromVolume: fromVolume, ToVolume: toVolume,
	})
	if err != nil {
		return model.IngestBatchAcceptance{}, err
	}
	if created {
		m.Notify()
	}
	return model.IngestBatchAcceptance{
		BatchID: batch.ID, SourceCode: batch.SourceCode, Status: batch.Status,
		Total: batch.Total, CreatedAt: batch.CreatedAt,
	}, nil
}

// Notify 非阻塞唤醒回填循环；信号合并不会丢任务，因为数据库才是任务真源。
func (m *HelloGitHubBackfillManager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run 在启动时立即扫描，并以 15 分钟兜底扫描恢复丢失信号或服务重启前的任务。
func (m *HelloGitHubBackfillManager) Run(ctx context.Context) {
	ticker := time.NewTicker(helloGitHubBackfillScanInterval)
	defer ticker.Stop()
	m.Notify()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
			m.processAvailable(ctx)
		case <-ticker.C:
			m.processAvailable(ctx)
		}
	}
}

func (m *HelloGitHubBackfillManager) processAvailable(ctx context.Context) {
	for ctx.Err() == nil {
		processed, err := m.ProcessNext(ctx)
		if err != nil {
			log.Printf("[hellogithub-backfill] process: %v", err)
			return
		}
		if !processed {
			return
		}
	}
}

// ProcessNext 处理一个到期 volume，供运行循环和单元测试复用。
func (m *HelloGitHubBackfillManager) ProcessNext(ctx context.Context) (bool, error) {
	now := m.now().UTC()
	batch, cursor, err := m.repository.NextHelloGitHubBackfill(now)
	if err != nil || batch == nil {
		return false, err
	}
	volume, err := m.fetcher.FetchVolume(ctx, cursor.NextVolume)
	if err != nil {
		return true, m.recordFailure(batch, cursor, err, now)
	}
	if cursor.ToVolume == 0 {
		cursor.ToVolume = volume.Latest
	}
	if cursor.ToVolume > volume.Latest {
		return true, m.recordFailure(batch, cursor,
			fmt.Errorf("to_volume %d exceeds latest volume %d", cursor.ToVolume, volume.Latest), now)
	}
	total := cursor.ToVolume - cursor.FromVolume + 1
	acceptance, err := m.enqueuer.Enqueue(model.EnqueueBatchRequest{
		SourceCode: model.SourceHelloGitHub,
		Kind:       model.IngestKindBackfill,
		IdempotencyKey: fmt.Sprintf("hellogithub:volume:%d:%s", volume.Number,
			candidateFingerprint(volume.Candidates)),
		Cursor: map[string]any{
			"parent_batch_id": batch.ID,
			"volume":          volume.Number,
		},
		Candidates: volume.Candidates,
	})
	if err != nil {
		return true, m.recordFailure(batch, cursor, fmt.Errorf("enqueue volume %d: %w", volume.Number, err), now)
	}

	completed := batch.Success + 1
	cursor.NextVolume++
	cursor.Attempts = 0
	cursor.NextAttemptAt = ""
	cursor.LastError = ""
	status := model.IngestBatchProcessing
	if cursor.NextVolume > cursor.ToVolume {
		status = model.IngestBatchSuccess
	}
	if err := m.repository.UpdateHelloGitHubBackfill(batch.ID, cursor, total, completed, status, now); err != nil {
		return true, err
	}
	log.Printf("[hellogithub-backfill] volume=%d queued=%d child_batch=%s progress=%d/%d", volume.Number, acceptance.Total, acceptance.BatchID, completed, total)
	return true, nil
}

func (m *HelloGitHubBackfillManager) recordFailure(batch *model.IngestBatch, cursor model.HelloGitHubBackfillCursor, cause error, now time.Time) error {
	cursor.Attempts++
	cursor.LastError = cause.Error()
	status := model.IngestBatchProcessing
	if cursor.Attempts >= 3 {
		status = model.IngestBatchFailed
		cursor.NextAttemptAt = ""
	} else {
		delay := 15 * time.Minute
		if cursor.Attempts == 2 {
			delay = 30 * time.Minute
		}
		cursor.NextAttemptAt = now.Add(delay).Format(time.RFC3339)
	}
	total := batch.Total
	if cursor.ToVolume > 0 {
		total = cursor.ToVolume - cursor.FromVolume + 1
	}
	if err := m.repository.UpdateHelloGitHubBackfill(batch.ID, cursor, total, batch.Success, status, now); err != nil {
		return err
	}
	log.Printf("[hellogithub-backfill] volume=%d attempt=%d status=%s error=%v", cursor.NextVolume, cursor.Attempts, status, cause)
	return nil
}

func newHelloGitHubBackfillID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(bytes)
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32], nil
}
