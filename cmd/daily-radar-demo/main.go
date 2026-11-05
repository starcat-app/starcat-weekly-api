// Command daily-radar-demo 为本机界面验收装配真实日报存储、鉴权与发布队列。
// 仅监听 loopback，独立数据库，不启动周刊采集、cron 或生产环境配置加载。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	kitgithub "github.com/starcat-app/starcat-api-kit/github"
	"github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
	"github.com/starcat-app/starcat-weekly-api/internal/middleware"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

// main 显式读取演示凭据，不加载 .env，避免意外复用生产管理员权限和数据库路径。
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	port := flag.Int("port", 18083, "仅监听 127.0.0.1 的本地端口")
	stateDir := flag.String("state-dir", "bin/daily-radar-demo", "独立演示数据库目录")
	flag.Parse()
	readKey, adminKey := os.Getenv("WEEKLY_DEMO_READ_KEY"), os.Getenv("WEEKLY_DEMO_ADMIN_KEY")
	if readKey == "" || adminKey == "" || readKey == adminKey {
		return fmt.Errorf("独立的 WEEKLY_DEMO_READ_KEY / WEEKLY_DEMO_ADMIN_KEY 必填")
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("无效端口")
	}
	if err := os.MkdirAll(*stateDir, 0700); err != nil {
		return err
	}
	s, err := store.NewSQLiteStore(filepath.Join(*stateDir, "weekly-demo.db"))
	if err != nil {
		return err
	}
	defer s.Close()

	// 相关项目仍通过真实 GitHub API 核验；匿名模式无需读取维护者的 PAT。
	client := kitgithub.NewClient(kitgithub.Options{
		AllowAnonymous: true,
		UserAgent:      "StarcatWeeklyEventsLocalDemo",
		Limiter:        kitgithub.NewRateLimitHandler(time.Second),
	})
	worker := dailyradar.NewWorker(s, client)
	handler := dailyradar.NewHandler(s, worker, nil, []string{"starcat-app/ai-weekly-events"})
	readAuth := middleware.NewBearerAuth([]string{readKey})
	adminAuth := middleware.NewBearerAuth([]string{adminKey})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","mode":"local-demo"}`))
	})
	mux.Handle("GET /api/v1/daily-radar/catalog", readAuth.Wrap(http.HandlerFunc(handler.Catalog)))
	mux.Handle("GET /api/v1/daily-radar/dates", readAuth.Wrap(http.HandlerFunc(handler.Dates)))
	mux.Handle("GET /api/v1/daily-radar/reports", readAuth.Wrap(http.HandlerFunc(handler.Reports)))
	mux.Handle("GET /api/v1/daily-radar/reports/{id}", readAuth.Wrap(http.HandlerFunc(handler.Detail)))
	mux.Handle("POST /internal/daily-radar/imports", adminAuth.Wrap(http.HandlerFunc(handler.Import)))
	mux.Handle("GET /internal/daily-radar/imports/{id}", adminAuth.Wrap(http.HandlerFunc(handler.Batch)))

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	defer func() { stop(); <-done }()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只记路由，不记录鉴权头或 query，便于确认 GUI 客户端确实请求了本地服务
		log.Printf("[demo] %s %s", r.Method, r.URL.Path)
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("本地日报演示: http://%s；独立数据库: %s", listener.Addr(), *stateDir)
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
