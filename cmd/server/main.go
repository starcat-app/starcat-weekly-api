// Package main 是 starcat-weekly-api 的服务入口
// 解析阮一峰周刊 (ruanyf/weekly) 推荐的开源项目，对外提供 REST API
package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dong4j/starcat-weekly-api/internal/enricher"
	"github.com/dong4j/starcat-weekly-api/internal/handler"
	"github.com/dong4j/starcat-weekly-api/internal/scheduler"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

func main() {
	// ── 配置 ──────────────────────────────────────────────
	port := os.Getenv("PORT")
	if port == "" {
		port = "5003"
	}

	// STORE_FILE 指向 SQLite 数据库路径，缺省使用当前目录
	dbPath := os.Getenv("STORE_FILE")
	if dbPath == "" {
		dbPath = "weekly.db"
	}

	// 周刊仓库缓存路径
	repoDir := os.Getenv("REPO_DIR")
	if repoDir == "" {
		repoDir = ".weekly-repo"
	}

	// ── 初始化存储 ────────────────────────────────────────
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer s.Close()

	// ── 初始化 enricher ───────────────────────────────────
	enr := enricher.NewEnricher(s)

	// ── 初始化调度器 ──────────────────────────────────────
	sch := scheduler.New(s, enr, repoDir)

	// ── 初始化 HTTP handler ───────────────────────────────
	wh := handler.NewWeeklyHandler(s, sch.Sync)

	// ── 注册路由 ──────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler)
	mux.HandleFunc("GET /api/weekly/projects", wh.HandleProjects)
	mux.HandleFunc("GET /api/weekly/issues", wh.HandleIssues)
	mux.HandleFunc("GET /api/weekly/issues/{number}", wh.HandleIssue)
	mux.HandleFunc("POST /internal/sync", wh.HandleSync)

	// ── 启动调度器（首次同步 + cron）──────────────────────
	go sch.Start()

	// ── 优雅退出 ──────────────────────────────────────────
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("收到退出信号，关闭服务...")
		sch.Stop()
		s.Close()
		os.Exit(0)
	}()

	// ── 启动 HTTP 服务 ────────────────────────────────────
	log.Printf("starcat-weekly-api 启动于端口 %s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// healthzHandler 健康检查（Fly.io http_service.checks 使用）
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

