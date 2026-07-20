// Package scheduler 定时同步周刊数据
package scheduler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/starcat-app/starcat-weekly-api/internal/discovery"
	"github.com/starcat-app/starcat-weekly-api/internal/fetcher"
	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/notifier"
	"github.com/starcat-app/starcat-weekly-api/internal/parser"
	weeklysource "github.com/starcat-app/starcat-weekly-api/internal/source"
	"github.com/starcat-app/starcat-weekly-api/internal/spider"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

// Scheduler 同步调度器
type Scheduler struct {
	store              store.Store
	enqueuer           ingestEnqueuer
	wikiNotifier       *notifier.WikiNotifier
	discovery          discoveryRunner
	helloGitHub        helloGitHubRunner
	discoveryCron      string
	helloCron          string
	helloReconcileCron string
	zreadCron          string
	repoDir            string
	cron               *cron.Cron
	funcMu             sync.Mutex
	running            map[string]bool // 防止并发跑同一任务（funcName 锁）
}

type ingestEnqueuer interface {
	Enqueue(model.EnqueueBatchRequest) (model.IngestBatchAcceptance, error)
}

type discoveryRunner interface {
	RunOnce(context.Context) (discovery.RunStats, error)
}

type helloGitHubRunner interface {
	RunFeatured(context.Context) (weeklysource.HelloGitHubRunStats, error)
	ReconcileLatest(context.Context) (weeklysource.HelloGitHubReconcileStats, error)
}

// New 创建调度器。
//
// GitHub enrich 完成后的 bulk cache 失效由统一 Worker 负责；Collector 入队时不能提前失效。
func New(s store.Store, enqueuer ingestEnqueuer, wn *notifier.WikiNotifier, repoDir string, discoveryService discoveryRunner, helloGitHubService helloGitHubRunner, discoveryCron string, helloCron string, helloReconcileCron string, zreadCron string) *Scheduler {
	if discoveryCron == "" {
		discoveryCron = "17 * * * *"
	}
	if zreadCron == "" {
		zreadCron = "0 6 * * *" // 默认每天 06:00 UTC
	}
	if helloCron == "" {
		helloCron = "31 6 * * *"
	}
	if helloReconcileCron == "" {
		helloReconcileCron = "29 7 29 * *"
	}
	return &Scheduler{
		store:              s,
		enqueuer:           enqueuer,
		wikiNotifier:       wn,
		repoDir:            repoDir,
		discovery:          discoveryService,
		helloGitHub:        helloGitHubService,
		discoveryCron:      discoveryCron,
		helloCron:          helloCron,
		helloReconcileCron: helloReconcileCron,
		zreadCron:          zreadCron,
		cron:               cron.New(),
		running:            make(map[string]bool),
	}
}

// Start 注册 cron 并并行触发三源首次同步。
//
// 关键约束：三个 Collector 只能并行解析并持久化候选，不能在 scheduler 内调用 GitHub；
// GitHub 配额由单一 Worker 串行消费，避免某个来源的冷启动阻塞其他来源入队。
func (s *Scheduler) Start() {
	log.Println("[scheduler] 注册 cron 并并行启动首次同步（weekly / zread / discovery / hellogithub）...")

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
	}
	if s.helloGitHub != nil {
		_, err = s.cron.AddFunc(s.helloCron, s.runHelloGitHub)
		if err != nil {
			log.Printf("[scheduler] cron add (hellogithub): %v", err)
		}
		_, err = s.cron.AddFunc(s.helloReconcileCron, s.runHelloGitHubReconcile)
		if err != nil {
			log.Printf("[scheduler] cron add (hellogithub reconcile): %v", err)
		}
	}

	s.cron.Start()
	log.Printf("[scheduler] cron 已启动 (weekly Mon 00:00 UTC + zread %s + discovery %s + hellogithub %s)", s.zreadCron, s.discoveryCron, s.helloCron)

	if !shouldRunInitialCollectors(s.store) {
		log.Println("[scheduler] 检测到已有持久化数据，跳过启动首次同步；定时 cron 与管理员手动同步仍可用")
		return
	}

	go func() {
		log.Println("[scheduler] 首次 weekly 同步...")
		repos := s.sync()
		s.wikiNotifier.NotifyRepos(repos)
	}()
	go s.runZreadFetch()
	if s.discovery != nil {
		go s.runDiscovery()
	}
	if s.helloGitHub != nil {
		go s.runHelloGitHub()
	}
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
	sp := spider.NewZreadSpider(nil)
	rows, err := sp.FetchRows(context.Background())
	if err != nil {
		log.Printf("[scheduler] zread fetch: %v", err)
		return
	}
	candidates := zreadCandidates(rows)
	allRepos := make([]string, 0, len(rows))
	for _, row := range rows {
		allRepos = append(allRepos, row.Owner+"/"+row.Name)
	}
	if len(candidates) > 0 {
		fetchedAt := rows[0].FetchedAt
		if fetchedAt == "" {
			fetchedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		acceptance, err := s.enqueuer.Enqueue(model.EnqueueBatchRequest{
			SourceCode: model.SourceZread, Kind: model.IngestKindCollector,
			IdempotencyKey: "zread:" + fetchedAt, Candidates: candidates,
		})
		if err != nil {
			log.Printf("[scheduler] zread enqueue: %v", err)
			return
		}
		log.Printf("[scheduler] zread fetch 完成: rows=%d batch=%s queued=%d", len(rows), acceptance.BatchID, acceptance.Total)
	}

	// 异步通知 wiki-api 预热本次 zread 拉取的 repo
	if s.wikiNotifier.IsEnabled() {
		s.wikiNotifier.NotifyRepos(allRepos)
	}
}

// SyncZread 手动触发 zread 同步（导出供 admin endpoint 复用）。
func (s *Scheduler) SyncZread() {
	go s.runZreadFetch()
}

// runDiscovery 执行 Show HN collect → 持久化入队，GitHub enrich 由统一 Worker 异步完成。
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
	log.Printf("[scheduler] discovery sync 完成: submissions=%d queued=%d batch=%s",
		stats.Submissions, stats.Queued, stats.BatchID)
}

// SyncDiscovery 异步触发 Discovery 同步，供独立 Admin endpoint 复用。
func (s *Scheduler) SyncDiscovery() {
	go s.runDiscovery()
}

// runHelloGitHub 执行精选增量抓取并持久化入队，GitHub enrich 继续由统一 Worker 完成。
func (s *Scheduler) runHelloGitHub() {
	if s.helloGitHub == nil {
		return
	}
	if !s.tryLock("hellogithub") {
		log.Println("[scheduler] hellogithub sync 已在运行中，跳过本次")
		return
	}
	defer s.unlock("hellogithub")
	stats, err := s.helloGitHub.RunFeatured(context.Background())
	if err != nil {
		log.Printf("[scheduler] hellogithub sync: %v", err)
		return
	}
	log.Printf("[scheduler] hellogithub sync 完成: pages=%d fetched=%d queued=%d batches=%d", stats.Pages, stats.Fetched, stats.Queued, len(stats.BatchIDs))
}

// SyncHelloGitHub 异步触发 HelloGitHub 精选增量同步，供 Admin endpoint 复用。
func (s *Scheduler) SyncHelloGitHub() {
	go s.runHelloGitHub()
}

func (s *Scheduler) runHelloGitHubReconcile() {
	if s.helloGitHub == nil {
		return
	}
	if !s.tryLock("hellogithub") {
		log.Println("[scheduler] hellogithub reconcile 已有任务运行，跳过本次")
		return
	}
	defer s.unlock("hellogithub")
	stats, err := s.helloGitHub.ReconcileLatest(context.Background())
	if err != nil {
		log.Printf("[scheduler] hellogithub reconcile: %v", err)
		return
	}
	log.Printf("[scheduler] hellogithub reconcile 完成: volume=%d fetched=%d queued=%d batch=%s", stats.Volume, stats.Fetched, stats.Queued, stats.BatchID)
}

// ReconcileHelloGitHub 异步触发最新月刊对账，供管理接口复用。
func (s *Scheduler) ReconcileHelloGitHub() {
	go s.runHelloGitHubReconcile()
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
	skipped := 0
	var allRepos []string

	for i, issue := range issues {
		contentHash, err := weeklyIssueContentHash(issue.Path)
		if err != nil {
			log.Printf("[scheduler] hash issue-%d: %v", issue.Number, err)
			continue
		}
		existing, err := s.store.GetIssue(issue.Number)
		if err != nil {
			log.Printf("[scheduler] load issue-%d: %v", issue.Number, err)
			continue
		}

		switch weeklyIssueSyncAction(existing, contentHash) {
		case weeklyIssueSkip:
			skipped++
			continue
		case weeklyIssueBaseline:
			// 已发布数据库没有哈希时只记录当前内容为基线，不解析或入队。
			// 这样升级或恢复备份不会将全部历史周刊再次送进 GitHub Worker。
			existing.ContentHash = contentHash
			if err := s.store.UpsertIssue(existing); err != nil {
				log.Printf("[scheduler] baseline issue-%d: %v", issue.Number, err)
				continue
			}
			skipped++
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
			ContentHash: contentHash,
		}
		candidates := weeklyCandidates(projects, issue.Number, publishedAt, srcURL)
		for j := range projects {
			project := projects[j]
			allRepos = append(allRepos, project.RepoOwner+"/"+project.RepoName)
		}
		if len(candidates) > 0 {
			acceptance, err := s.enqueuer.Enqueue(model.EnqueueBatchRequest{
				SourceCode: model.SourceWeekly, Kind: model.IngestKindCollector,
				IdempotencyKey: weeklyBatchIdempotencyKey(issue.Number, contentHash), Candidates: candidates,
			})
			if err != nil {
				log.Printf("[scheduler] enqueue issue-%d: %v", issue.Number, err)
				continue
			}
			log.Printf("[scheduler] issue-%d queued: batch=%s repos=%d", issue.Number, acceptance.BatchID, acceptance.Total)
		}
		if err := s.store.UpsertIssue(&weeklyIssue); err != nil {
			log.Printf("[scheduler] mark issue-%d parsed: %v", issue.Number, err)
			continue
		}

		if len(projects) > 0 {
			newCount++
		}

		// 每 50 期满拍暂停片刻（避免 git 操作过于密集）
		if i%50 == 49 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	log.Printf("[scheduler] 同步完成: %d 期新/更新, %d 个 repo, %d 期跳过(无变更)", newCount, len(allRepos), skipped)

	return allRepos
}

func zreadCandidates(rows []model.ZreadTrending) []model.IngestCandidate {
	candidates := make([]model.IngestCandidate, 0, len(rows))
	for _, row := range rows {
		rank := row.RankInWeek
		occurredAt, _ := time.Parse("2006-01-02", row.WeekStart)
		candidates = append(candidates, model.IngestCandidate{
			Owner: row.Owner, Repo: row.Name,
			ExternalKey: fmt.Sprintf("week:%s:%s/%s", row.WeekStart, strings.ToLower(row.Owner), strings.ToLower(row.Name)),
			OccurredAt:  occurredAt, Title: row.WeekLabel, Summary: row.DescriptionZh, Rank: &rank,
			Payload: map[string]any{
				"week_start": row.WeekStart, "week_end": row.WeekEnd, "zread_repo_id": row.RepoID,
				"wiki_id": row.WikiID, "zread_year_inferred": row.ZreadYearInferred,
				"zread_week_start_raw": row.ZreadWeekStartRaw, "zread_week_end_raw": row.ZreadWeekEndRaw,
			},
		})
	}
	return candidates
}

func weeklyCandidates(projects []model.Project, issueNumber int, publishedAt time.Time, sourceURL string) []model.IngestCandidate {
	candidates := make([]model.IngestCandidate, 0, len(projects))
	for _, project := range projects {
		candidates = append(candidates, model.IngestCandidate{
			Owner: project.RepoOwner, Repo: project.RepoName,
			ExternalKey: fmt.Sprintf("issue:%d:%s/%s", issueNumber, strings.ToLower(project.RepoOwner), strings.ToLower(project.RepoName)),
			OccurredAt:  publishedAt, SourceURL: sourceURL, Summary: project.Description,
			Payload: map[string]any{"issue_number": issueNumber},
		})
	}
	return candidates
}

type weeklyIssueAction uint8

const (
	weeklyIssueSkip weeklyIssueAction = iota
	weeklyIssueEnqueue
	weeklyIssueBaseline
)

// weeklyIssueSyncAction 只依赖 Markdown 内容，而不依赖 git 文件的本地 mtime。
//
// 历史库的 content_hash 为空时采用一次性静默基线：保留历史数据、写入当前哈希，
// 但不重放 parse + GitHub enrich。之后只有上游内容真实变化才重新入队。
func weeklyIssueSyncAction(existing *model.WeeklyIssue, contentHash string) weeklyIssueAction {
	if existing == nil {
		return weeklyIssueEnqueue
	}
	if existing.ContentHash == "" {
		return weeklyIssueBaseline
	}
	if existing.ContentHash == contentHash {
		return weeklyIssueSkip
	}
	return weeklyIssueEnqueue
}

// weeklyIssueContentHash 计算周刊源文件的稳定内容版本。
func weeklyIssueContentHash(issuePath string) (string, error) {
	content, err := os.ReadFile(issuePath)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(content)), nil
}

func weeklyBatchIdempotencyKey(issueNumber int, contentHash string) string {
	return fmt.Sprintf("weekly:%d:%s", issueNumber, contentHash)
}

type startupDataStore interface {
	HasStartupData() (bool, error)
}

// shouldRunInitialCollectors 仅让从未写入过业务状态的空库执行启动抓取。
// 实际 SQLite 实现查询持久化状态；没有实现该可选能力的测试/替换存储维持旧行为。
func shouldRunInitialCollectors(s any) bool {
	startupStore, ok := s.(startupDataStore)
	if !ok {
		return true
	}
	hasData, err := startupStore.HasStartupData()
	if err != nil {
		// 无法确认库是否为空时宁可等待 cron 或管理员显式触发，避免意外耗尽配额。
		log.Printf("[scheduler] 检查启动数据状态失败，跳过首次同步: %v", err)
		return false
	}
	return !hasData
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
