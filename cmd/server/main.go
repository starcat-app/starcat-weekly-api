// Package main is the entry point for starcat-weekly-api.
// It parses ruanyf/weekly recommended GitHub repos and exposes a REST API.
package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/dong4j/starcat-weekly-api/internal/enricher"
	"github.com/dong4j/starcat-weekly-api/internal/handler"
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

	// Initialize enricher with TokenPool and RateLimitHandler
	rl := enricher.NewRateLimitHandler(720 * time.Millisecond) // 5000/h ≈ 720ms
	enr := enricher.NewEnricher(s, pool, rl)

	// Wiki Notifier（增量预热 wiki-api 缓存，通过 WIKI_API_KEY 控制开关）
	wikiNotifier := notifier.NewWikiNotifier()

	// Initialize scheduler
	sch := scheduler.New(s, enr, wikiNotifier, repoDir)

	// Initialize HTTP handler
	wh := handler.NewWeeklyHandler(s, sch.Sync, sch.SyncZread)
	zh := handler.NewZreadTrendingHandler(s)

	// Register routes (Go 1.22+ style)
	// 注意：authMW.Wrap 接受 http.Handler。把 method value (func(w,r)) 显式包装为
	// http.HandlerFunc 让它满足 http.Handler 接口（Go 不支持隐式转换）。
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", wh.Healthz) // Health check (unauthenticated)

	// API V1 Endpoints (authenticated)
	mux.Handle("GET /api/v1/weekly", authMW.Wrap(http.HandlerFunc(wh.HandleProjectsV1)))
	mux.Handle("GET /api/v1/weekly/{owner}/{repo}", authMW.Wrap(http.HandlerFunc(wh.HandleProjectByOwnerRepoV1)))
	mux.Handle("GET /api/v1/issues", authMW.Wrap(http.HandlerFunc(wh.HandleIssuesV1)))
	mux.Handle("GET /api/v1/issues/{number}", authMW.Wrap(http.HandlerFunc(wh.HandleIssueV1)))
	// v0.5 R-02 新增：zread 周 trending 端点（决策 ② 独立端点，不污染阮一峰现有）
	mux.Handle("GET /api/v1/zread", authMW.Wrap(http.HandlerFunc(zh.HandleZreadTrendingV1)))

	// Admin Endpoints (authenticated)
	mux.Handle("POST /internal/sync/weekly", authMW.Wrap(http.HandlerFunc(wh.HandleAdminSync)))
	// v0.5 R-02 新增：zread 同步 admin 端点（与阮一峰周刊同步解耦）
	mux.Handle("POST /internal/sync/zread", authMW.Wrap(http.HandlerFunc(wh.HandleZreadSync)))

	// Start scheduler (initial sync + cron)
	go sch.Start()

	// Graceful shutdown on SIGINT / SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received shutdown signal, closing service...")
		sch.Stop()
		s.Close()
		os.Exit(0)
	}()

	// Start HTTP server
	log.Printf("starcat-weekly-api starting on port %s", port)
	log.Printf("V1 Endpoints (authenticated):")
	log.Printf("  GET  /api/v1/weekly             - List projects")
	log.Printf("  GET  /api/v1/weekly/{o}/{r}     - Get single project")
	log.Printf("  GET  /api/v1/issues             - List issues")
	log.Printf("  GET  /api/v1/issues/{n}         - Get issue detail")
	log.Printf("  GET  /api/v1/zread               - List zread weekly trending (v0.5)")
	log.Printf("  POST /internal/sync/weekly      - Trigger manual sync (阮一峰周刊)")
	log.Printf("  POST /internal/sync/zread       - Trigger manual sync (zread 周 trending)")
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
