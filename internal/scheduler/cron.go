// Package scheduler 定时同步周刊数据
package scheduler

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/dong4j/starcat-weekly-api/internal/discovery"
	"github.com/dong4j/starcat-weekly-api/internal/enricher"
	"github.com/dong4j/starcat-weekly-api/internal/fetcher"
	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/notifier"
	"github.com/dong4j/starcat-weekly-api/internal/parser"
	"github.com/dong4j/starcat-weekly-api/internal/spider"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

// Scheduler 同步调度器
type Scheduler struct {
	store         store.Store
	enricher      *enricher.Enricher
	wikiNotifier  *notifier.WikiNotifier
	discovery     *discovery.Service
	discoveryCron string
	repoDir       string
	cron          *cron.Cron
	funcMu        sync.Mutex
	running       map[string]bool // 防止并发跑同一任务（funcName 锁）
}

// New 创建调度器
func New(s store.Store, enr *enricher.Enricher, wn *notifier.WikiNotifier, repoDir string, discoveryService *discovery.Service, discoveryCron string) *Scheduler {
	if discoveryCron == "" {
		discoveryCron = "17 * * * *"
	}
	return &Scheduler{
		store:         s,
		enricher:      enr,
		wikiNotifier:  wn,
		repoDir:       repoDir,
		discovery:     discoveryService,
		discoveryCron: discoveryCron,
		cron:          cron.New(),
		running:       make(map[string]bool),
	}
}

// Start 启动定时器 + 首次全量同步
func (s *Scheduler) Start() {
	log.Println("[scheduler] 首次全量同步...")
	repos := s.sync()
	s.wikiNotifier.NotifyRepos(repos)

	log.Println("[scheduler] 启动元数据补全...")
	s.enricher.EnrichAll()

	// 每小时同步一次（取第 7 分钟避免整点拥挤）
	_, err := s.cron.AddFunc("7 * * * *", func() {
		log.Println("[scheduler] 定时同步...")
		repos := s.sync()
		s.wikiNotifier.NotifyRepos(repos)
		s.enricher.EnrichBatch()
	})
	if err != nil {
		log.Printf("[scheduler] cron add (阮一峰): %v", err)
	}

	// v0.5 R-02 新增：周一 06:00 UTC 拉 zread 周 trending
	// 详见 19-wiki集成.md §8.2.3 — zread 周一 00:00 UTC 更新，留 6h buffer
	_, err = s.cron.AddFunc("0 0 6 * * 1", s.runZreadFetch)
	if err != nil {
		log.Printf("[scheduler] cron add (zread): %v", err)
	}

	if s.discovery != nil {
		// 与阮一峰第 7 分错开；任务自身还有 funcName 锁，避免 cron 与 admin 重叠。
		_, err = s.cron.AddFunc(s.discoveryCron, s.runDiscovery)
		if err != nil {
			log.Printf("[scheduler] cron add (discovery): %v", err)
		}
		go s.runDiscovery()
	}

	s.cron.Start()
	log.Printf("[scheduler] cron 已启动 (阮一峰每小时第 7 分 + zread 周一 06:00 + discovery %s)", s.discoveryCron)
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// runZreadFetch 拉取 zread 周 trending 一次。
//
// 由 cron 周期触发，也可被 admin endpoint 手动调用。
// 内部用 tryLock("zread") 防并发跑同一任务（funcName 锁）。
func (s *Scheduler) runZreadFetch() {
	if !s.tryLock("zread") {
		log.Println("[scheduler] zread fetch 已在运行中，跳过本次")
		return
	}
	defer s.unlock("zread")

	log.Println("[scheduler] 拉取 zread 周 trending...")
	sp := spider.NewZreadSpider(s.store.(*store.SQLiteStore))
	if err := sp.RunOnce(context.Background()); err != nil {
		log.Printf("[scheduler] zread fetch: %v", err)
		return
	}
	// 拉取成功后随手 enrich 一遍，让前端下次拿到卡片时已有 14 字段
	s.enricher.EnrichAll()

	// 异步通知 wiki-api 预热本次 zread 拉取的 repo
	if s.wikiNotifier.IsEnabled() {
		repos := s.store.GetZreadRepos() // 从 DB 提取所有 zread repo
		s.wikiNotifier.NotifyRepos(repos)
	}
}

// SyncZread 手动触发 zread 同步（导出供 admin endpoint 复用）。
func (s *Scheduler) SyncZread() {
	go s.runZreadFetch()
}

// runDiscovery 执行 Show HN collect -> GitHub enrich -> LLM classify。
func (s *Scheduler) runDiscovery() {
	if s.discovery == nil {
		return
	}
	if !s.tryLock("discovery") {
		log.Println("[scheduler] discovery sync 已在运行中，跳过本次")
		return
	}
	defer s.unlock("discovery")

	stats, err := s.discovery.RunOnce(context.Background())
	if err != nil {
		log.Printf("[scheduler] discovery sync: %v", err)
		return
	}
	log.Printf("[scheduler] discovery sync 完成: submissions=%d enriched=%d classified=%d rejected=%d failures=%d",
		stats.Submissions, stats.Enriched, stats.Classified, stats.Rejected, stats.Failures)
}

// SyncDiscovery 异步触发 Discovery 同步，供独立 Admin endpoint 复用。
func (s *Scheduler) SyncDiscovery() {
	go s.runDiscovery()
}

// tryLock 检查并标记任务运行中。返回 false 表示已有同名任务在跑。
func (s *Scheduler) tryLock(name string) bool {
	s.funcMu.Lock()
	defer s.funcMu.Unlock()
	if s.running[name] {
		return false
	}
	s.running[name] = true
	return true
}

func (s *Scheduler) unlock(name string) {
	s.funcMu.Lock()
	s.running[name] = false
	s.funcMu.Unlock()
}

// Sync 执行一次完整的 fetcher → parser → store 流程（导出供手动触发）
func (s *Scheduler) Sync() {
	repos := s.sync()
	s.wikiNotifier.NotifyRepos(repos)
}

// sync 内部同步逻辑。
// 返回本次解析出的 owner/repo 列表，用于 wiki 预热。
func (s *Scheduler) sync() []string {
	issues, err := fetcher.CloneOrPull(s.repoDir)
	if err != nil {
		log.Printf("[scheduler] fetch: %v", err)
		return nil
	}
	log.Printf("[scheduler] 获取到 %d 期周刊", len(issues))

	newCount := 0
	var allRepos []string

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
				continue
			}
			allRepos = append(allRepos, projects[j].RepoOwner+"/"+projects[j].RepoName)
		}

		if len(projects) > 0 {
			newCount++
		}

		// 每 50 期满拍暂停片刻（避免 git 操作过于密集）
		if i%50 == 49 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	log.Printf("[scheduler] 同步完成: %d 期新/更新, %d 个 repo", newCount, len(allRepos))
	return allRepos
}
