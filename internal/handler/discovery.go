package handler

import (
	"net/http"
	"strconv"
	"time"
)

// DiscoveryHandler keeps the Show HN manual sync endpoint.
//
// R-04 folds Discovery reads into /api/v1/repos and /api/v1/repos/{gh_repo_id}.
type DiscoveryHandler struct {
	sync func()
}

func NewDiscoveryHandler(_ any, syncFn func()) *DiscoveryHandler {
	return &DiscoveryHandler{sync: syncFn}
}

// HandleAdminSync POST /internal/sync/discovery.
func (h *DiscoveryHandler) HandleAdminSync(w http.ResponseWriter, _ *http.Request) {
	taskID := "task-" + time.Now().UTC().Format("2006-01-02T15:04:05Z") + "-discovery"
	go h.sync()
	writeJSON(w, map[string]string{
		"task_id":    taskID,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "running",
	})
}

func positiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
