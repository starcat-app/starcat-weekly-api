// Package main is the entry point for starcat-weekly-api.
// It parses ruanyf/weekly recommended GitHub repos and exposes a REST API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/dong4j/starcat-weekly-api/internal/discovery"
	"github.com/dong4j/starcat-weekly-api/internal/enricher"
	"github.com/dong4j/starcat-weekly-api/internal/github"
	"github.com/dong4j/starcat-weekly-api/internal/handler"
	"github.com/dong4j/starcat-weekly-api/internal/ingest"
	"github.com/dong4j/starcat-weekly-api/internal/middleware"
	"github.com/dong4j/starcat-weekly-api/internal/notifier"
	"github.com/dong4j/starcat-weekly-api/internal/scheduler"
	"github.com/dong4j/starcat-weekly-api/internal/store"
	"github.com/dong4j/starcat-weekly-api/internal/tokenpool"
)

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Printf("[env] no .env file found, using OS environment only")
	} else {
		log.Printf("[env] .env loaded")
	}

	// Configuration
	port := os.Getenv("PORT")
	if port == "" {
		port = "5003"
	}

	// STORE_FILE points to the SQLite database file
	dbPath := os.Getenv("STORE_FILE")
	if dbPath == "" {
		dbPath = "weekly.db"
	}

	// Weekly repo clone/cache directory
	repoDir := os.Getenv("REPO_DIR")
	if repoDir == "" {
		repoDir = ".weekly-repo"
	}

	// API Keys for authentication
	// 注意：NewBearerAuth 内部自动打 [auth] N keys loaded 启动日志（含日志脱敏），无需 main 重复打印。
	apiKeysStr := os.Getenv("API_KEYS")
	if apiKeysStr == "" {
		log.Fatal("API_KEYS env is required (comma-separated list of valid API keys)")
	}
	apiKeys := strings.Split(apiKeysStr, ",")
	authMW := middleware.NewBearerAuth(apiKeys)

	// Discovery 的手动同步会消耗 GitHub 配额，不能复用会被客户端携带的 API_KEYS。
	// 未配置时中间件白名单为空，路由保持 401，不阻断普通查询与 cron。
	adminKeys := splitNonEmpty(os.Getenv("ADMIN_API_KEYS"))
	adminAuthMW := middleware.NewBearerAuth(adminKeys)
	if len(adminKeys) == 0 {
		log.Println("[auth] ADMIN_API_KEYS not configured; admin discovery sync is disabled")
	}

	// GitHub Token Pool（兼容旧 GITHUB_TOKEN 单值环境变量）
	// 注意：tokenpool.New 内部自动打 [token-pool] loaded N tokens 启动日志，无需 main 重复打印。
	tokensStr := os.Getenv("GITHUB_TOKENS")
	var tokens []string
	if tokensStr != "" {
		tokens = strings.Split(tokensStr, ",")
	} else if old := os.Getenv("GITHUB_TOKEN"); old != "" {
		tokens = []string{old}
		log.Println("[token-pool] migrating legacy GITHUB_TOKEN to GITHUB_TOKENS (single token)")
	} else {
		log.Fatal("GITHUB_TOKENS or GITHUB_TOKEN env required (at least 1 GitHub PAT)")
	}
	pool := tokenpool.New(tokens)

	// Initialize store
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer s.Close()

	// Initialize GitHub client（统一 Token 池 + 速率限制，enricher / discovery / zread 共享）
	ghClient := github.NewClient(pool, github.NewRateLimitHandler(720*time.Millisecond)) // 5000/h ≈ 720ms
	enr := enricher.NewEnricher(s, ghClient)

	// AI Discovery 复用同一个 GitHub Client（v1.2：移除 LLM 分类，仅 collect → enrich 两阶段）。
	hnClient := discovery.NewHNClient(nil)
	discoveryGitHub := discovery.NewGitHubClient(ghClient)
	discoveryService := discovery.NewService(s, hnClient, discoveryGitHub, discovery.Config{
		HNLimit:    envInt("DISCOVERY_HN_LIMIT", 30),
		BatchSize:  envInt("DISCOVERY_BATCH_SIZE", 20),
		RetryDelay: time.Duration(envInt("DISCOVERY_RETRY_DELAY_MINUTES", 60)) * time.Minute,
	})

	// Wiki Notifier（增量预热 wiki-api 缓存，通过 WIKI_API_KEY 控制开关）
	wikiNotifier := notifier.NewWikiNotifier()

	// R-06.3: bulk endpoint 内存缓存（6h TTL + pre-marshaled + pre-gzipped + ETag 304）
	// 单例，由 handler.HandleBulkV1 读 + scheduler / RebuildAggregates 写（Invalidate）。
	bulkCache := handler.NewBulkCache()
	wakeSignal := ingest.NewWakeSignal()
	ingestWorker := ingest.NewWorker(s, ghClient, wakeSignal, bulkCache)
	workerContext, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go ingestWorker.Run(workerContext)

	// Initialize scheduler — bulkCache 注入作为 BulkCacheInvalidator，scheduler 跑完
	// weekly / zread / discovery 同步后主动失效 bulk cache。
	sch := scheduler.New(s, enr, wikiNotifier, repoDir, discoveryService,
		envOrDefault("DISCOVERY_CRON", "17 * * * *"),
		envOrDefault("ZREAD_TRENDING_CRON", "0 6 * * *"),
		bulkCache)

	// Initialize HTTP handler
	wh := handler.NewWeeklyHandler(s, sch.Sync, sch.SyncZread)
	rh := handler.NewReposHandlerWithBulkCache(s, bulkCache)
	dh := handler.NewDiscoveryHandler(s, sch.SyncDiscovery)

	// Register routes (Go 1.22+ style)
	// 注意：authMW.Wrap 接受 http.Handler。把 method value (func(w,r)) 显式包装为
	// http.HandlerFunc 让它满足 http.Handler 接口（Go 不支持隐式转换）。
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", wh.Healthz) // Health check (unauthenticated)

	// R-03 (2026-06-11): /api/v1/ping 专门给 Starcat 客户端「测试连接」按钮用，
	// 在 middleware 后面挂——同时验证服务可达 + Bearer Key 正确。详见 handler/ping.go。
	mux.Handle("GET /api/v1/ping", authMW.Wrap(handler.HandlePingV1("weekly")))

	// API V1 Endpoints (authenticated). R-04 removes old weekly/zread/discovery public routes.
	mux.Handle("GET /api/v1/repos", authMW.Wrap(http.HandlerFunc(rh.HandleListV1)))
	// R-06.3 (2026-06-15): bulk endpoint 让客户端一次性拉全量 ~4000 条 repos +
	// languages 聚合到本地做 sort/filter/page，避免分页 80+ 次往返。详见 handler/bulk.go。
	// 必须挂在 /api/v1/repos/{gh_repo_id} 之前否则被通配吃掉。
	mux.Handle("GET /api/v1/repos/bulk", authMW.Wrap(handler.HandleBulkV1(s, bulkCache)))
	mux.Handle("GET /api/v1/repos/languages", authMW.Wrap(http.HandlerFunc(rh.HandleLanguagesV1)))
	mux.Handle("GET /api/v1/repos/{gh_repo_id}", authMW.Wrap(http.HandlerFunc(rh.HandleDetailV1)))

	// Admin Endpoints (authenticated)
	mux.Handle("POST /internal/sync/weekly", authMW.Wrap(http.HandlerFunc(wh.HandleAdminSync)))
	// v0.5 R-02 新增：zread 同步 admin 端点（与阮一峰周刊同步解耦）
	mux.Handle("POST /internal/sync/zread", authMW.Wrap(http.HandlerFunc(wh.HandleZreadSync)))
	mux.Handle("POST /internal/sync/discovery", adminAuthMW.Wrap(http.HandlerFunc(dh.HandleAdminSync)))
	mux.Handle("POST /internal/rebuild-aggregates", adminAuthMW.Wrap(http.HandlerFunc(rh.HandleRebuildAggregates)))

	// Start scheduler (initial sync + cron)
	go sch.Start()

	// Graceful shutdown on SIGINT / SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received shutdown signal, closing service...")
		stopWorker()
		sch.Stop()
		s.Close()
		os.Exit(0)
	}()

	// Start HTTP server
	log.Printf("starcat-weekly-api starting on port %s", port)
	log.Printf("V1 Endpoints (authenticated):")
	log.Printf("  GET  /api/v1/ping               - Connectivity probe for Starcat client")
	log.Printf("  GET  /api/v1/repos              - List aggregated repos (paginated)")
	log.Printf("  GET  /api/v1/repos/bulk         - One-shot full payload (repos + languages, gzip + ETag 304)")
	log.Printf("  GET  /api/v1/repos/{id}         - Get aggregated repo detail")
	log.Printf("  GET  /api/v1/repos/languages    - List aggregated languages")
	log.Printf("  POST /internal/sync/weekly      - Trigger manual sync (阮一峰周刊)")
	log.Printf("  POST /internal/sync/zread       - Trigger manual sync (zread 周 trending)")
	log.Printf("  POST /internal/sync/discovery   - Trigger manual sync (ADMIN_API_KEYS)")
	log.Printf("  POST /internal/rebuild-aggregates - Recompute source aggregates")
	handler := middleware.CORS(mux)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func splitNonEmpty(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
