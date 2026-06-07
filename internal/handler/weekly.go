// Package handler 提供 Weekly REST API 的 HTTP 处理函数
package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

// WeeklyHandler Weekly API 处理器
type WeeklyHandler struct {
	store store.Store
	sync  func() // 手动触发同步回调
}

// NewWeeklyHandler 创建处理器
func NewWeeklyHandler(s store.Store, syncFn func()) *WeeklyHandler {
	return &WeeklyHandler{store: s, sync: syncFn}
}

// HandleProjects GET /api/weekly/projects — 项目列表（分页 + 筛选）
func (h *WeeklyHandler) HandleProjects(w http.ResponseWriter, r *http.Request) {
	params := parseQueryParams(r)

	projects, total, err := h.store.GetProjects(params)
	if err != nil {
		log.Printf("[handler] GetProjects: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	if projects == nil {
		projects = []model.Project{}
	}

	writeJSON(w, http.StatusOK, model.ProjectResponse{
		Items:    projects,
		Total:    total,
		Page:     params.Page,
		PageSize: params.PageSize,
	})
}

// HandleIssues GET /api/weekly/issues — 列出所有期号
func (h *WeeklyHandler) HandleIssues(w http.ResponseWriter, r *http.Request) {
	issues, err := h.store.GetIssues()
	if err != nil {
		log.Printf("[handler] GetIssues: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if issues == nil {
		issues = []model.WeeklyIssue{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": issues,
		"total": len(issues),
	})
}

// HandleIssue GET /api/weekly/issues/{number} — 某期详情 + 项目列表
func (h *WeeklyHandler) HandleIssue(w http.ResponseWriter, r *http.Request) {
	numStr := r.PathValue("number")
	num, err := strconv.Atoi(numStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid issue number"})
		return
	}

	issue, err := h.store.GetIssue(num)
	if err != nil {
		log.Printf("[handler] GetIssue: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if issue == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "issue not found"})
		return
	}

	// 查询该期的项目
	projects, _, err := h.store.GetProjects(model.QueryParams{
		Page:     1,
		PageSize: 1000,
		Issue:    numStr,
	})
	if err != nil {
		log.Printf("[handler] GetProjects for issue: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if projects == nil {
		projects = []model.Project{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"issue":    issue,
		"projects": projects,
	})
}

// HandleSync POST /internal/sync — 手动触发同步
func (h *WeeklyHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	go h.sync()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
}

// parseQueryParams 从 URL Query 解析参数
func parseQueryParams(r *http.Request) model.QueryParams {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	issueFrom, _ := strconv.Atoi(q.Get("issue_from"))
	issueTo, _ := strconv.Atoi(q.Get("issue_to"))

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	return model.QueryParams{
		Page:      page,
		PageSize:  pageSize,
		Issue:     q.Get("issue"),
		IssueFrom: issueFrom,
		IssueTo:   issueTo,
		Language:  q.Get("lang"),
		Sort:      q.Get("sort"),
	}
}

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[handler] write json: %v", err)
	}
}
