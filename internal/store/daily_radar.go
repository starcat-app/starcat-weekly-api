// 日报存储只追加独立表；不创建来源事件，不改变周刊 / AI 情报仓库数量。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	radar "github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
)

// migrateDailyRadar 对已发布库追加迁移，原子保留导入队列、不可变修订及语言指针。
func migrateDailyRadar(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE daily_radar_state(id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL);
		INSERT INTO daily_radar_state VALUES(1,0);
		CREATE TABLE daily_radar_batches(
			id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE, request_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE daily_radar_jobs(
			id INTEGER PRIMARY KEY AUTOINCREMENT, batch_id TEXT NOT NULL REFERENCES daily_radar_batches(id),
			report_id TEXT NOT NULL, payload TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued',
			attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT NOT NULL DEFAULT '',
			revision TEXT, error TEXT
		);
		CREATE INDEX daily_radar_jobs_claim ON daily_radar_jobs(status,next_attempt_at,id);
		CREATE TABLE daily_radar_revisions(
			revision TEXT PRIMARY KEY, report_id TEXT NOT NULL, language TEXT NOT NULL,
			summary_json TEXT NOT NULL, detail_json TEXT NOT NULL
		);
		CREATE INDEX daily_radar_revisions_report ON daily_radar_revisions(report_id,language);
		CREATE TABLE daily_radar_sources(
			report_id TEXT NOT NULL, language TEXT NOT NULL, source_kind TEXT NOT NULL,
			sequence INTEGER NOT NULL, candidate_hash TEXT NOT NULL, revision TEXT NOT NULL REFERENCES daily_radar_revisions(revision),
			PRIMARY KEY(report_id,language,source_kind)
		);
		CREATE TABLE daily_radar_publications(
			report_id TEXT NOT NULL, language TEXT NOT NULL, revision TEXT NOT NULL REFERENCES daily_radar_revisions(revision),
			PRIMARY KEY(report_id,language)
		);
	`)
	return err
}

// EnqueueRadar 仅持久化原始候选；GitHub 网络校验留给事务外 worker。
func (s *SQLiteStore) EnqueueRadar(request radar.ImportRequest) (*radar.Batch, error) {
	hash := radar.HashJSON(request)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var existing, previousHash string
	err = tx.QueryRow(`SELECT id,request_hash FROM daily_radar_batches WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&existing, &previousHash)
	if err == nil {
		if hash != previousHash {
			return nil, radar.ErrIdempotency
		}
		rollback(tx)
		return s.RadarBatch(existing)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	id := uuid.NewString()
	if _, err = tx.Exec(`INSERT INTO daily_radar_batches VALUES(?,?,?,?)`, id, request.IdempotencyKey, hash, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	for _, c := range request.Reports {
		item := request
		item.Reports = []radar.Candidate{c}
		b, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO daily_radar_jobs(batch_id,report_id,payload) VALUES(?,?,?)`, id, radar.ReportID(c), string(b)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.RadarBatch(id)
}

// RadarBatch 从项目状态推导批次，重启不依赖内存计数。
func (s *SQLiteStore) RadarBatch(id string) (*radar.Batch, error) {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM daily_radar_batches WHERE id=?`, id).Scan(&exists); err != nil || exists == 0 {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,status,report_id,revision,error FROM daily_radar_jobs WHERE batch_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batch := &radar.Batch{ID: id, Status: "processing", Items: []radar.BatchItem{}}
	for rows.Next() {
		var item radar.BatchItem
		if err := rows.Scan(&item.ID, &item.Status, &item.ReportID, &item.Revision, &item.Error); err != nil {
			return nil, err
		}
		batch.Total++
		if item.Status == "published" {
			batch.Published++
		} else if item.Status == "failed" {
			batch.Failed++
		}
		batch.Items = append(batch.Items, item)
	}
	if batch.Total == batch.Published+batch.Failed {
		batch.Status = "complete"
		if batch.Failed == batch.Total {
			batch.Status = "failed"
		} else if batch.Failed > 0 {
			batch.Status = "partial"
		}
	}
	return batch, rows.Err()
}

// RecoverRadarJobs 仅在单进程 worker 启动前调用，归还上次停机遗留的领取项。
func (s *SQLiteStore) RecoverRadarJobs() error {
	_, err := s.db.Exec(`UPDATE daily_radar_jobs SET status='queued' WHERE status='processing'`)
	return err
}

// ClaimRadarJob 在短事务内领取一项，事务结束后才允许外部网络请求。
func (s *SQLiteStore) ClaimRadarJob(now time.Time) (*radar.Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var job radar.Job
	var payload string
	err = tx.QueryRow(`SELECT id,attempts,payload FROM daily_radar_jobs WHERE status IN ('queued','retrying') AND next_attempt_at<=? ORDER BY id LIMIT 1`,
		now.UTC().Format(time.RFC3339)).Scan(&job.ID, &job.Attempts, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(payload), &job.Request); err != nil {
		return nil, err
	}
	job.Attempts++
	if _, err := tx.Exec(`UPDATE daily_radar_jobs SET status='processing',attempts=? WHERE id=?`, job.Attempts, job.ID); err != nil {
		return nil, err
	}
	return &job, tx.Commit()
}

// FailRadarJob 保留原有公开版本，最多三次重试由 worker 控制。
func (s *SQLiteStore) FailRadarJob(id int64, code string, next time.Time, retry bool) error {
	status := "failed"
	if retry {
		status = "retrying"
	}
	_, err := s.db.Exec(`UPDATE daily_radar_jobs SET status=?,error=?,next_attempt_at=? WHERE id=?`, status, code, next.UTC().Format(time.RFC3339), id)
	return err
}

// PublishRadar 原子发布不可变修订与语言指针；同序不同候选失败，迟到旧序列不覆盖。
func (s *SQLiteStore) PublishRadar(job radar.Job, detail radar.Detail) error {
	c := job.Request.Reports[0]
	hash := radar.HashJSON(struct {
		Candidate  radar.Candidate
		Repository string
	}{c, job.Request.SourceRepository})
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer rollback(tx)
	var previousSequence int64
	var previousHash, previousRevision string
	err = tx.QueryRow(`SELECT sequence,candidate_hash,revision FROM daily_radar_sources WHERE report_id=? AND language=? AND source_kind=?`,
		detail.ID, c.Language, job.Request.SourceKind).Scan(&previousSequence, &previousHash, &previousRevision)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if job.Request.SourceSequence == previousSequence && hash != previousHash {
			return radar.ErrSequence
		}
		if job.Request.SourceSequence <= previousSequence {
			_, err := tx.Exec(`UPDATE daily_radar_jobs SET status='published',revision=?,error=NULL WHERE id=?`, previousRevision, job.ID)
			if err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	summary, err := json.Marshal(detail.Summary)
	if err != nil {
		return err
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO daily_radar_revisions VALUES(?,?,?,?,?)`,
		detail.Revision, detail.ID, c.Language, string(summary), string(body)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO daily_radar_sources VALUES(?,?,?,?,?,?) ON CONFLICT(report_id,language,source_kind) DO UPDATE SET sequence=excluded.sequence,candidate_hash=excluded.candidate_hash,revision=excluded.revision`,
		detail.ID, c.Language, job.Request.SourceKind, job.Request.SourceSequence, hash, detail.Revision); err != nil {
		return err
	}
	var canonical string
	if err := tx.QueryRow(`SELECT revision FROM daily_radar_sources WHERE report_id=? AND language=? ORDER BY CASE source_kind WHEN 'starcat' THEN 0 ELSE 1 END LIMIT 1`,
		detail.ID, c.Language).Scan(&canonical); err != nil {
		return err
	}
	var old string
	err = tx.QueryRow(`SELECT revision FROM daily_radar_publications WHERE report_id=? AND language=?`, detail.ID, c.Language).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if old != canonical {
		if _, err := tx.Exec(`INSERT INTO daily_radar_publications VALUES(?,?,?) ON CONFLICT(report_id,language) DO UPDATE SET revision=excluded.revision`,
			detail.ID, c.Language, canonical); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE daily_radar_state SET revision=revision+1 WHERE id=1`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE daily_radar_jobs SET status='published',revision=?,error=NULL WHERE id=?`, detail.Revision, job.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// RadarSnapshot 版本与摘要同事务读取，不会拼接发布前计数与发布后内容。
func (s *SQLiteStore) RadarSnapshot() (radar.Snapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return radar.Snapshot{}, err
	}
	defer rollback(tx)
	snapshot := radar.Snapshot{Items: []radar.Summary{}}
	var revision int64
	if err := tx.QueryRow(`SELECT revision FROM daily_radar_state WHERE id=1`).Scan(&revision); err != nil {
		return snapshot, err
	}
	snapshot.Revision = strconv.FormatInt(revision, 10)
	s.radarMu.Lock()
	cached := s.radarSnapshot
	s.radarMu.Unlock()
	if cached != nil && cached.Revision == snapshot.Revision {
		if err := tx.Commit(); err != nil {
			return snapshot, err
		}
		return *cached, nil
	}
	rows, err := tx.Query(`SELECT r.summary_json FROM daily_radar_publications p JOIN daily_radar_revisions r ON r.revision=p.revision`)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var raw string
		var item radar.Summary
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return snapshot, err
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			rows.Close()
			return snapshot, err
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snapshot, err
	}
	if err := tx.Commit(); err != nil {
		return snapshot, err
	}
	s.radarMu.Lock()
	s.radarSnapshot = &snapshot
	s.radarMu.Unlock()
	return snapshot, nil
}

// RadarDetail 指定 revision 时仍校验报告和语言身份，不能跨报告读取内容。
func (s *SQLiteStore) RadarDetail(id, revision, language string, fallback bool) (*radar.Detail, error) {
	var raw string
	var exists bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM daily_radar_publications WHERE report_id=?)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, radar.ErrReport
	}
	if revision != "" {
		if err := s.db.QueryRow(`SELECT detail_json FROM daily_radar_revisions WHERE report_id=? AND revision=?`, id, revision).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, radar.ErrRevision
			}
			return nil, err
		}
	} else {
		rows, err := s.db.Query(`SELECT p.language,r.detail_json FROM daily_radar_publications p JOIN daily_radar_revisions r ON r.revision=p.revision WHERE p.report_id=? ORDER BY CASE p.language WHEN ? THEN 0 WHEN 'en' THEN 1 ELSE 2 END`, id, language)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var lang, body string
			if err := rows.Scan(&lang, &body); err != nil {
				rows.Close()
				return nil, err
			}
			if raw == "" && (fallback || lang == language) {
				raw = body
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if raw == "" {
		return nil, radar.ErrLanguage
	}
	var detail radar.Detail
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return nil, fmt.Errorf("decode radar detail: %w", err)
	}
	if !fallback && detail.ContentLanguage != language {
		return nil, radar.ErrLanguage
	}
	return &detail, nil
}

// RepairRadarSourceRepositories publishes corrected immutable revisions for
// already published articles. Original revisions and source records remain
// intact, so pinned readers and future higher-sequence imports keep working.
func (s *SQLiteStore) RepairRadarSourceRepositories() (int, error) {
	excluded := map[string]bool{
		"duanyytop/agents-radar":   true,
		"starcat-app/agents-radar": true,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	rows, err := tx.Query(`SELECT p.report_id,p.language,r.detail_json
		FROM daily_radar_publications p JOIN daily_radar_revisions r ON r.revision=p.revision`)
	if err != nil {
		return 0, err
	}
	type repair struct{ id, language, revision, summary, detail string }
	var repairs []repair
	for rows.Next() {
		var id, language, raw string
		if err := rows.Scan(&id, &language, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		var value radar.Detail
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			rows.Close()
			return 0, err
		}
		projects := make([]radar.RelatedProject, 0, len(value.RelatedProjects))
		for _, project := range value.RelatedProjects {
			if !excluded[strings.ToLower(project.Repo.FullName)] {
				projects = append(projects, project)
			}
		}
		if len(projects) == len(value.RelatedProjects) {
			continue
		}
		value.RelatedProjects = projects
		value.RelatedProjectCount = len(projects)
		// 与首次发布一致：updated_at 不参与内容 hash；计算完成后再回填时间。
		value.Summary.Revision = ""
		value.UpdatedAt = ""
		value.Revision = radar.HashJSON(value)
		value.Summary.Revision = value.Revision
		value.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		summary, err := json.Marshal(value.Summary)
		if err != nil {
			rows.Close()
			return 0, err
		}
		body, err := json.Marshal(value)
		if err != nil {
			rows.Close()
			return 0, err
		}
		repairs = append(repairs, repair{id: id, language: language, revision: value.Revision, summary: string(summary), detail: string(body)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, item := range repairs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO daily_radar_revisions VALUES(?,?,?,?,?)`,
			item.revision, item.id, item.language, item.summary, item.detail); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`UPDATE daily_radar_publications SET revision=? WHERE report_id=? AND language=?`,
			item.revision, item.id, item.language); err != nil {
			return 0, err
		}
	}
	if len(repairs) > 0 {
		if _, err := tx.Exec(`UPDATE daily_radar_state SET revision=revision+1 WHERE id=1`); err != nil {
			return 0, err
		}
	}
	return len(repairs), tx.Commit()
}
