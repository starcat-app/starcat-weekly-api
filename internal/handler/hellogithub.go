package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// HelloGitHubHandler 暴露精选增量同步入口；具体抓取在 scheduler goroutine 中执行。
type HelloGitHubHandler struct {
	sync      func()
	reconcile func()
	backfill  helloGitHubBackfillStarter
}

type helloGitHubBackfillStarter interface {
	Start(fromVolume, toVolume int, idempotencyKey string) (model.IngestBatchAcceptance, error)
}

type helloGitHubSyncRequest struct {
	Mode           string `json:"mode"`
	FromVolume     *int   `json:"from_volume,omitempty"`
	ToVolume       *int   `json:"to_volume,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func NewHelloGitHubHandler(syncFn func(), reconcileFn func(), backfill helloGitHubBackfillStarter) *HelloGitHubHandler {
	return &HelloGitHubHandler{sync: syncFn, reconcile: reconcileFn, backfill: backfill}
}

// HandleSourceSync POST /internal/sources/{source_code}/sync。
// 当前只有 HelloGitHub 需要带 mode 的同步控制，未知固定来源必须显式拒绝。
func (h *HelloGitHubHandler) HandleSourceSync(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("source_code") != model.SourceHelloGitHub {
		writeError(w, http.StatusNotFound, "SOURCE_NOT_FOUND", "source sync endpoint not found", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	request := helloGitHubSyncRequest{Mode: "incremental"}
	if r.ContentLength != 0 {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body", map[string]string{"error": err.Error()})
			return
		}
	}
	switch strings.ToLower(strings.TrimSpace(request.Mode)) {
	case "", "incremental", "featured":
		h.sync()
		writeJSONStatus(w, http.StatusAccepted, map[string]string{
			"task_id":    "task-" + time.Now().UTC().Format("2006-01-02T15:04:05Z") + "-hellogithub",
			"started_at": time.Now().UTC().Format(time.RFC3339),
			"status":     "running",
		})
	case "backfill":
		if h.backfill == nil {
			writeError(w, http.StatusServiceUnavailable, "BACKFILL_UNAVAILABLE", "HelloGitHub backfill is unavailable", nil)
			return
		}
		from := 1
		if request.FromVolume != nil {
			from = *request.FromVolume
		}
		to := 0
		if request.ToVolume != nil {
			to = *request.ToVolume
		}
		acceptance, err := h.backfill.Start(from, to, request.IdempotencyKey)
		if err != nil {
			if strings.Contains(err.Error(), "volume") {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error(), nil)
				return
			}
			log.Printf("[handler] start HelloGitHub backfill: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to start HelloGitHub backfill", nil)
			return
		}
		writeJSONStatus(w, http.StatusAccepted, acceptance)
	case "reconcile":
		if h.reconcile == nil {
			writeError(w, http.StatusServiceUnavailable, "RECONCILE_UNAVAILABLE", "HelloGitHub reconcile is unavailable", nil)
			return
		}
		h.reconcile()
		writeJSONStatus(w, http.StatusAccepted, map[string]string{"status": "running"})
	default:
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "mode must be incremental, featured, reconcile, or backfill", nil)
	}
}
