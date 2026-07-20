// Package handler provides admin sync endpoints for the weekly service.
package handler

import (
	"net/http"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

// WeeklyHandler keeps the two manual sync entrypoints.
//
// R-04 removes the old public weekly list/detail endpoints. Public reads now
// go through ReposHandler and the aggregated /api/v1/repos contract.
type WeeklyHandler struct {
	store     store.Store
	sync      func()
	syncZread func()
}

func NewWeeklyHandler(s store.Store, syncFn func(), syncZreadFn func()) *WeeklyHandler {
	return &WeeklyHandler{store: s, sync: syncFn, syncZread: syncZreadFn}
}

// HandleAdminSync POST /internal/sync/weekly — trigger weekly sync.
func (h *WeeklyHandler) HandleAdminSync(w http.ResponseWriter, _ *http.Request) {
	taskID := "task-" + time.Now().UTC().Format("2006-01-02T15:04:05Z") + "-weekly"
	go h.sync()
	writeJSON(w, map[string]string{
		"task_id":    taskID,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "running",
	})
}

// HandleZreadSync POST /internal/sync/zread — trigger zread sync.
func (h *WeeklyHandler) HandleZreadSync(w http.ResponseWriter, _ *http.Request) {
	taskID := "task-" + time.Now().UTC().Format("2006-01-02T15:04:05Z") + "-zread"
	go h.syncZread()
	writeJSON(w, map[string]string{
		"task_id":    taskID,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "running",
	})
}

// Healthz GET /healthz - unauthenticated health check.
func (h *WeeklyHandler) Healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
