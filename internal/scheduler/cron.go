// Package scheduler 定时同步周刊数据
package scheduler

import (
	"context"
	"log"
	"os"
	"regexp"
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

// BulkCacheInvalidator 是 scheduler 完成 weekly / zread / discovery 同步后需要
// "主动失效 bulk endpoint 内存缓存"的最小接口。
//
// 让 scheduler 包**不直接 import handler 包**：handler/bulk_cache.go 的
// `*BulkCache` 自动满足这个接口；main.go 把实例注入到 scheduler.New(...) 即可。
//
// 这是 R-06.3（2026-06-15）拆出的接口，目的是避免 scheduler ↔ handler 双向
// import 循环。
type BulkCacheInvalidator interface {
	Invalidate()
}

// noopBulkCacheInvalidator 是 nil-safe 占位，仅用于测试 / 调用方暂时不接缓存的场景。
type noopBulkCacheInvalidator struct{}

func (noopBulkCacheInvalidator) Invalidate() {}

// Scheduler 同步调度器
type Scheduler struct {
	store         store.Store
	enricher      *enricher.Enricher
	wikiNotifier  *notifier.WikiNotifier
	bulkCache     BulkCacheInvalidator // R-06.3: 完成同步后主动失效 bulk cache；nil 时用 noop
	discovery     *discovery.Service
	discoveryCron string
	zreadCron     string
	repoDir       string
	cron          *cron.Cron
	funcMu        sync.Mutex
	running       map[string]bool // 防止并发跑同一任务（funcName 锁）
}

// New 创建调度器。
//
// bulkCache 是 R-06.3 加的可选依赖：完成 sync / runZreadFetch / runDiscovery 后
// 主动失效 bulk endpoint 内存缓存，让客户端下次请求拿到聚合更新后的数据。
// 传 nil 时退化到 noop（不报错，仅不做缓存失效），方便测试 / 暂未接缓存的部署场景。
func New(s store.Store, enr *enricher.Enricher, wn *notifier.WikiNotifier, repoDir string, discoveryService *discovery.Service, discoveryCron string, zreadCron string, bulkCache BulkCacheInvalidator) *Scheduler {
	if discoveryCron == "" {
		discoveryCron = "17 * * * *"
	}
	if zreadCron == "" {
		zreadCron = "0 6 * * *" // 默认每天 06:00 UTC
	}
	if bulkCache == nil {
		bulkCache = noopBulkCacheInvalidator{}
	}
	return &Scheduler{
		store:         s,
		enricher:      enr,
		wikiNotifier:  wn,
		bulkCache:     bulkCache,
		repoDir:       repoDir,
		discovery:     discoveryService,
		discoveryCron: discoveryCron,
		zreadCron:     zreadCron,
		cron:          cron.New(),
		running:       make(map[string]bool),
	}
}

// Start 启动定时器 + 首次全量同步
func (s *Scheduler) Start() {
	log.Println("[scheduler] 首次全量同步...")
	repos := s.sync()
	s.wikiNotifier.NotifyRepos(repos)

	// R-04: weekly 历史数据不需要小时级刷新。默认每周一 00:00 UTC 更新一次。
	_, err := s.cron.AddFunc("0 0 * * 1", func() {
		log.Println("[scheduler] 定时同步...")
		repos := s.sync()
		s.wikiNotifier.NotifyRepos(repos)
	})
	if err != nil {
		log.Printf("[scheduler] cron add (阮一峰): %v", err)
	}

	// v0.5 R-02：zread 周 trending 同步，cron 由 ZREAD_TRENDING_CRON 环境变量控制
	_, err = s.cron.AddFunc(s.zreadCron, s.runZreadFetch)
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
	log.Printf("[scheduler] cron 已启动 (weekly Mon 00:00 UTC + zread %s + discovery %s)", s.zreadCron, s.discoveryCron)
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
	rows, err := sp.FetchRows(context.Background())
	if err != nil {
		log.Printf("[scheduler] zread fetch: %v", err)
		return
	}
	ctx := context.Background()
	written := 0
	for _, row := range rows {
		repo, err := s.enricher.EnsureGitHubRepo(ctx, row.Owner, row.Name, false)
		if err != nil {
			log.Printf("[scheduler] zread ensure %s/%s: %v", row.Owner, row.Name, err)
			continue
		}
		if err := s.store.AttachZreadEvent(repo.GhRepoID, row); err != nil {
			log.Printf("[scheduler] zread attach %s/%s: %v", row.Owner, row.Name, err)
			continue
		}
		written++
	}
	log.Printf("[scheduler] zread fetch 完成: rows=%d attached=%d", len(rows), written)

	// 异步通知 wiki-api 预热本次 zread 拉取的 repo
	if s.wikiNotifier.IsEnabled() {
		repos := s.store.GetAllSourceRepos()
		s.wikiNotifier.NotifyRepos(repos)
	}

	// R-06.3: zread_events / github_repos 已更新，bulk cache 失效（同 sync() 注释）
	s.bulkCache.Invalidate()
}

// SyncZread 手动触发 zread 同步（导出供 admin endpoint 复用）。
func (s *Scheduler) SyncZread() {
	go s.runZreadFetch()
}

// runDiscovery 执行 Show HN collect → GitHub enrich 两阶段（v1.2：移除 LLM classify）。
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
	log.Printf("[scheduler] discovery sync 完成: submissions=%d enriched=%d failures=%d",
		stats.Submissions, stats.Enriched, stats.Failures)

	// R-06.3: discovery_submissions / github_repos 已更新，bulk cache 失效（同 sync() 注释）
	s.bulkCache.Invalidate()
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

// Sync 执行一次完整的 fetcher → parser → store 流程（导出供手动触发）。
//
// R-06.3 注：bulkCache.Invalidate 由 s.sync() 末尾统一负责（Start / cron / Sync
// 三条调用链都经过 sync() 不会漏）。
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

		srcURL := "https://github.com/ruanyf/weekly/blob/master/docs/issue-" + strconv.Itoa(issue.Number) + ".md"
		publishedAt := issuePublishedAt(issue.Path)
		weeklyIssue := model.WeeklyIssue{
			Number:      issue.Number,
			PublishedAt: publishedAt,
			SourceURL:   srcURL,
			ParsedAt:    time.Now().UTC(),
		}
		if err := s.store.UpsertIssue(&weeklyIssue); err != nil {
			log.Printf("[scheduler] upsert issue-%d: %v", issue.Number, err)
			continue
		}

		// 写入项目
		for j := range projects {
			repo, err := s.enricher.EnsureGitHubRepo(context.Background(), projects[j].RepoOwner, projects[j].RepoName, false)
			if err != nil {
				log.Printf("[scheduler] ensure %s/%s: %v", projects[j].RepoOwner, projects[j].RepoName, err)
				continue
			}
			if err := s.store.AttachWeeklyEvent(repo.GhRepoID, projects[j], weeklyIssue); err != nil {
				log.Printf("[scheduler] attach weekly %s/%s: %v", projects[j].RepoOwner, projects[j].RepoName, err)
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

	// R-06.3: weekly sync 写入 weekly_extras + github_repos 后，bulk endpoint
	// 内存缓存的数据已过时；不论本次是否实际新增 repos 都失效（让"刷新但无新数据"
	// 仍能拿到 fresh ETag，避免客户端误判数据未变）。
	s.bulkCache.Invalidate()

	return allRepos
}

func issuePublishedAt(path string) time.Time {
	content, err := os.ReadFile(path)
	if err == nil {
		if matches := issueAssetDatePattern.FindSubmatch(content); len(matches) == 2 {
			if parsed, err := time.ParseInLocation("20060102", string(matches[1]), time.UTC); err == nil {
				return parsed.UTC()
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Now().UTC()
	}
	return info.ModTime().UTC()
}

var issueAssetDatePattern = regexp.MustCompile(`bg(\d{8})`)
