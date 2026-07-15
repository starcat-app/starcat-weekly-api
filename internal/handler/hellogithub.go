package handler

import (
	"net/http"
	"time"
)

// HelloGitHubHandler 暴露精选增量同步入口；具体抓取在 scheduler goroutine 中执行。
type HelloGitHubHandler struct {
	sync func()
}

func NewHelloGitHubHandler(syncFn func()) *HelloGitHubHandler {
	return &HelloGitHubHandler{sync: syncFn}
}

// HandleAdminSync POST /internal/sync/hellogithub。
func (h *HelloGitHubHandler) HandleAdminSync(w http.ResponseWriter, _ *http.Request) {
	h.sync()
	writeJSONStatus(w, http.StatusAccepted, map[string]string{
		"task_id":    "task-" + time.Now().UTC().Format("2006-01-02T15:04:05Z") + "-hellogithub",
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "running",
	})
}
