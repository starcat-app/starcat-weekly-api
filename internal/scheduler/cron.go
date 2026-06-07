// Package scheduler 定时同步周刊数据
package scheduler

import (
	"log"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/dong4j/starcat-weekly-api/internal/enricher"
	"github.com/dong4j/starcat-weekly-api/internal/fetcher"
	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/parser"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

// Scheduler 同步调度器
type Scheduler struct {
	store    store.Store
	enricher *enricher.Enricher
	repoDir  string
	cron     *cron.Cron
}

// New 创建调度器
func New(s store.Store, enr *enricher.Enricher, repoDir string) *Scheduler {
	return &Scheduler{
		store:    s,
		enricher: enr,
		repoDir:  repoDir,
		cron:     cron.New(),
	}
}

// Start 启动定时器 + 首次全量同步
func (s *Scheduler) Start() {
	log.Println("[scheduler] 首次全量同步...")
	s.sync()

	log.Println("[scheduler] 启动元数据补全...")
	s.enricher.EnrichAll()

	// 每小时同步一次（取第 7 分钟避免整点拥挤）
	_, err := s.cron.AddFunc("7 * * * *", func() {
		log.Println("[scheduler] 定时同步...")
		s.sync()
		s.enricher.EnrichBatch()
	})
	if err != nil {
		log.Printf("[scheduler] cron add: %v", err)
		return
	}

	s.cron.Start()
	log.Println("[scheduler] cron 已启动 (每小时第 7 分)")
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// Sync 执行一次完整的 fetcher → parser → store 流程（导出供手动触发）
func (s *Scheduler) Sync() {
	s.sync()
}

// sync 内部同步逻辑
func (s *Scheduler) sync() {
	issues, err := fetcher.CloneOrPull(s.repoDir)
	if err != nil {
		log.Printf("[scheduler] fetch: %v", err)
		return
	}
	log.Printf("[scheduler] 获取到 %d 期周刊", len(issues))

	newCount := 0
	for i, issue := range issues {
		existing, _ := s.store.GetIssue(issue.Number)
		if existing != nil && i < len(issues)-10 {
			// 最近 10 期可能更新内容，其余跳过
			continue
		}

		projects, err := parser.ParseFile(issue.Path, issue.Number)
		if err != nil {
			log.Printf("[scheduler] parse issue-%d: %v", issue.Number, err)
			continue
		}

		// 写入期号信息
		srcURL := "https://github.com/ruanyf/weekly/blob/master/docs/issue-" + strconv.Itoa(issue.Number) + ".md"
		s.store.UpsertIssue(&model.WeeklyIssue{
			Number:    issue.Number,
			SourceURL: srcURL,
			ParsedAt:  time.Now().UTC(),
		})

		// 写入项目
		for j := range projects {
			if err := s.store.UpsertProject(&projects[j]); err != nil {
				log.Printf("[scheduler] upsert %s/%s: %v",
					projects[j].RepoOwner, projects[j].RepoName, err)
			}
		}

		if len(projects) > 0 {
			newCount++
		}

		// 每 50 期满拍暂停片刻（避免 git 操作过于密集）
		if i%50 == 49 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	log.Printf("[scheduler] 同步完成: %d 期新/更新", newCount)
}
