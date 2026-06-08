// Package main is the entry point for starcat-weekly-api.
// It parses ruanyf/weekly recommended GitHub repos and exposes a REST API.
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

	// Initialize store
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer s.Close()

	// Initialize enricher
	enr := enricher.NewEnricher(s)

	// Initialize scheduler
	sch := scheduler.New(s, enr, repoDir)

	// Initialize HTTP handler
	wh := handler.NewWeeklyHandler(s, sch.Sync)

	// Register routes (Go 1.22+ style: custom mux + method-aware paths)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler)
	mux.HandleFunc("GET /api/weekly/projects", wh.HandleProjects)
	mux.HandleFunc("GET /api/weekly/issues", wh.HandleIssues)
	mux.HandleFunc("GET /api/weekly/issues/{number}", wh.HandleIssue)
	mux.HandleFunc("POST /internal/sync", wh.HandleSync)

	// Start scheduler (initial sync + cron)
	go sch.Start()

	// Graceful shutdown on SIGINT / SIGTERM
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Received shutdown signal, closing service...")
		sch.Stop()
		s.Close()
		os.Exit(0)
	}()

	// Start HTTP server
	log.Printf("starcat-weekly-api starting on port %s", port)
	log.Printf("Endpoints:")
	log.Printf("  GET  /healthz                  - Health check")
	log.Printf("  GET  /api/weekly/projects      - List projects (params: page, page_size, issue, lang, sort)")
	log.Printf("  GET  /api/weekly/issues        - List issues")
	log.Printf("  GET  /api/weekly/issues/{n}    - Get issue detail")
	log.Printf("  POST /internal/sync            - Trigger manual sync")
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// healthzHandler health check (used by Fly.io http_service.checks)
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
