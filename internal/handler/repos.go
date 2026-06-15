package handler

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

type ReposHandler struct {
	store     store.Store
	bulkCache *BulkCache // R-06.3: RebuildAggregates 跑完后主动失效；可为 nil（旧测试路径）
}

func NewReposHandler(s store.Store) *ReposHandler {
	return &ReposHandler{store: s}
}

// NewReposHandlerWithBulkCache 注入 bulk cache 用于 RebuildAggregates 后主动失效。
//
// R-06.3 后所有生产 callsite 都应该走这个构造函数；保留 NewReposHandler 仅供
// 既有 handler test 复用（cache 为 nil 时 Invalidate 调用走 nil-safe 短路）。
func NewReposHandlerWithBulkCache(s store.Store, bulkCache *BulkCache) *ReposHandler {
	return &ReposHandler{store: s, bulkCache: bulkCache}
}

func (h *ReposHandler) HandleListV1(w http.ResponseWriter, r *http.Request) {
	params, ok := parseRepoQuery(w, r)
	if !ok {
		return
	}
	items, total, err := h.store.QueryRepos(params)
	if err != nil {
		log.Printf("[handler] QueryRepos: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	meta := &model.Meta{Page: params.Page, PageSize: params.PageSize, Total: total, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	if params.Page*params.PageSize < total {
		next := params.Page + 1
		meta.NextPage = &next
	}
	writeJSONWithMeta(w, items, meta)
}

func (h *ReposHandler) HandleDetailV1(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("gh_repo_id")
	repoID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || repoID <= 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid gh_repo_id", map[string]any{"param": "gh_repo_id", "got": raw})
		return
	}
	detail, err := h.store.GetRepoDetail(repoID)
	if err != nil {
		log.Printf("[handler] GetRepoDetail(%d): %v", repoID, err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repo not found", map[string]any{"gh_repo_id": repoID})
		return
	}
	writeJSON(w, detail)
}

func (h *ReposHandler) HandleLanguagesV1(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("source") != "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "source filter is not supported by /api/v1/repos/languages", nil)
		return
	}
	items, err := h.store.GetAggregatedLanguages()
	if err != nil {
		log.Printf("[handler] GetAggregatedLanguages: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	writeJSONWithMeta(w, items, &model.Meta{Total: len(items), GeneratedAt: time.Now().UTC().Format(time.RFC3339)})
}

func (h *ReposHandler) HandleRebuildAggregates(w http.ResponseWriter, _ *http.Request) {
	if err := h.store.RebuildAggregates(); err != nil {
		log.Printf("[handler] RebuildAggregates: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	// R-06.3: 聚合表已重算，bulk endpoint 6h 缓存的数据已经过时，立即失效让下次
	// 请求强制重建。bulkCache 为 nil 时（旧测试路径）短路，不影响正常调用。
	if h.bulkCache != nil {
		h.bulkCache.Invalidate()
	}
	writeJSON(w, map[string]string{
		"status":       "ok",
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func parseRepoQuery(w http.ResponseWriter, r *http.Request) (model.RepoQuery, bool) {
	q := r.URL.Query()
	page := positiveInt(q.Get("page"), 1)
	pageSize := positiveInt(q.Get("page_size"), 30)
	if pageSize > 50 {
		pageSize = 50
	}

	sortKey := q.Get("sort")
	if sortKey == "" {
		sortKey = "latest_event_at"
	}
	switch sortKey {
	case "latest_event_at", "stars", "pushed_at":
	default:
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid sort", map[string]string{"sort": sortKey})
		return model.RepoQuery{}, false
	}

	order := q.Get("order")
	if order == "" {
		order = "desc"
	}
	switch order {
	case "asc", "desc":
	default:
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid order", map[string]string{"order": order})
		return model.RepoQuery{}, false
	}

	sources := splitSources(q.Get("source"))
	for _, source := range sources {
		switch source {
		case model.SourceWeekly, model.SourceZread, model.SourceDiscovery:
		default:
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid source", map[string]string{"source": source})
			return model.RepoQuery{}, false
		}
	}

	return model.RepoQuery{
		Page: page, PageSize: pageSize, Source: sources, Language: q.Get("lang"), Sort: sortKey, Order: order,
	}, true
}

func splitSources(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
