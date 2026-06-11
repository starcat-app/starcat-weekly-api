// Package handler 提供 AI Discovery 查询与同步端点。
package handler

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// DiscoveryStore 是 handler 所需的只读存储边界。
type DiscoveryStore interface {
	QueryDiscovery(params model.DiscoveryQuery) ([]model.DiscoveryItemDTO, int, error)
	GetDiscoveryByOwnerRepo(owner, repo string) (*model.DiscoveryItemDTO, error)
}

// DiscoveryHandler 处理列表、详情与管理员同步触发。
type DiscoveryHandler struct {
	store DiscoveryStore
	sync  func()
	now   func() time.Time
}

// NewDiscoveryHandler 创建 Discovery handler。
func NewDiscoveryHandler(store DiscoveryStore, syncFn func()) *DiscoveryHandler {
	return &DiscoveryHandler{store: store, sync: syncFn, now: time.Now}
}

// HandleListV1 GET /api/v1/discovery?category=all&page=1&page_size=30。
func (h *DiscoveryHandler) HandleListV1(w http.ResponseWriter, r *http.Request) {
	category := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category")))
	if category == "" {
		category = "all"
	}
	if category != "all" && !model.ValidDiscoveryCategory(category) {
		writeError(w, http.StatusBadRequest, "INVALID_CATEGORY", "invalid discovery category", map[string]any{
			"category": category,
			"allowed":  []string{"all", "agent", "coding", "mcp", "rag", "infra", "model", "skill"},
		})
		return
	}
	page := positiveInt(r.URL.Query().Get("page"), 1)
	pageSize := positiveInt(r.URL.Query().Get("page_size"), 30)
	if pageSize > 50 {
		pageSize = 50
	}

	items, total, err := h.store.QueryDiscovery(model.DiscoveryQuery{
		Category: category, Page: page, PageSize: pageSize,
		Since: h.now().UTC().Add(-24 * time.Hour),
	})
	if err != nil {
		log.Printf("[handler] QueryDiscovery: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	meta := &model.Meta{Page: page, PageSize: pageSize, Total: total, GeneratedAt: h.now().UTC().Format(time.RFC3339)}
	if page*pageSize < total {
		next := page + 1
		meta.NextPage = &next
	}
	writeJSONWithMeta(w, items, meta)
}

// HandleDetailV1 GET /api/v1/discovery/{owner}/{repo}。
func (h *DiscoveryHandler) HandleDetailV1(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.PathValue("owner"))
	repo := strings.TrimSpace(r.PathValue("repo"))
	if owner == "" || repo == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "owner and repo are required", nil)
		return
	}
	item, err := h.store.GetDiscoveryByOwnerRepo(owner, repo)
	if err != nil {
		log.Printf("[handler] GetDiscoveryByOwnerRepo: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Repo not found in AI Discovery", map[string]string{
			"owner": owner, "repo": repo,
		})
		return
	}
	writeJSON(w, item)
}

// HandleAdminSync POST /internal/sync/discovery，实际鉴权由独立 ADMIN_API_KEYS middleware 负责。
func (h *DiscoveryHandler) HandleAdminSync(w http.ResponseWriter, _ *http.Request) {
	taskID := "task-" + time.Now().UTC().Format("2006-01-02T15:04:05Z") + "-discovery"
	go h.sync()
	writeJSON(w, map[string]string{
		"task_id": taskID, "started_at": time.Now().UTC().Format(time.RFC3339), "status": "running",
	})
}

func positiveInt(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
