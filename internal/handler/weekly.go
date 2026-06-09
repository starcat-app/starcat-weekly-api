// Package handler 提供 Weekly REST API 的 HTTP 处理函数
package handler

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

// issueDetailData 是 /api/v1/issues/{n} 响应 data 的具体类型。
// 抽出来让 writeJSONWithMeta 的类型推断更稳，避免 map[string]any 与 envelope JSON 字段名漂移。
type issueDetailData struct {
	Issue    *model.WeeklyIssue           `json:"issue"`
	Projects []model.StarcatRepoCardDTO   `json:"projects"`
}

// WeeklyHandler Weekly API 处理器
type WeeklyHandler struct {
	store store.Store
	sync  func() // 手动触发同步回调
}

// NewWeeklyHandler 创建处理器
func NewWeeklyHandler(s store.Store, syncFn func()) *WeeklyHandler {
	return &WeeklyHandler{store: s, sync: syncFn}
}

// HandleProjectsV1 GET /api/v1/projects — 项目列表（分页 + 筛选）
func (h *WeeklyHandler) HandleProjectsV1(w http.ResponseWriter, r *http.Request) {
	params := parseQueryParams(r)

	projects, total, err := h.store.GetProjects(params)
	if err != nil {
		log.Printf("[handler] GetProjects: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}

	cards := make([]model.StarcatRepoCardDTO, 0)
	for _, p := range projects {
		cards = append(cards, p.ToRepoCard())
	}

	meta := &model.Meta{
		Page:        params.Page,
		PageSize:    params.PageSize,
		Total:       total,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if params.Page*params.PageSize < total {
		next := params.Page + 1
		meta.NextPage = &next
	}

	writeJSONWithMeta(w, cards, meta)
}

// HandleProjectByOwnerRepoV1 GET /api/v1/projects/{owner}/{repo} — 获取单 repo 聚合
func (h *WeeklyHandler) HandleProjectByOwnerRepoV1(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")

	p, err := h.store.GetProjectByOwnerRepo(owner, repo)
	if err != nil {
		log.Printf("[handler] GetProjectByOwnerRepo: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Repo not found in weekly archive", map[string]any{
			"owner": owner,
			"repo":  repo,
		})
		return
	}

	writeJSON(w, p.ToRepoCard())
}

// HandleIssuesV1 GET /api/v1/issues — 列出所有期号
func (h *WeeklyHandler) HandleIssuesV1(w http.ResponseWriter, r *http.Request) {
	issues, err := h.store.GetIssues()
	if err != nil {
		log.Printf("[handler] GetIssues: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	if issues == nil {
		issues = []model.WeeklyIssue{}
	}

	writeJSONWithMeta(w, issues, &model.Meta{
		Total:       len(issues),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// HandleIssueV1 GET /api/v1/issues/{number} — 某期详情 + 项目列表
func (h *WeeklyHandler) HandleIssueV1(w http.ResponseWriter, r *http.Request) {
	numStr := r.PathValue("number")
	num, err := strconv.Atoi(numStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid issue number",
			map[string]any{"param": "number", "got": numStr})
		return
	}

	issue, err := h.store.GetIssue(num)
	if err != nil {
		log.Printf("[handler] GetIssue: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}
	if issue == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "issue not found",
			map[string]any{"number": num})
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
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}

	cards := make([]model.StarcatRepoCardDTO, 0)
	for _, p := range projects {
		cards = append(cards, p.ToRepoCard())
	}

	writeJSON(w, issueDetailData{
		Issue:    issue,
		Projects: cards,
	})
}

// HandleAdminSync POST /internal/sync — 手动触发同步 (fire-and-forget)
func (h *WeeklyHandler) HandleAdminSync(w http.ResponseWriter, r *http.Request) {
	taskID := "task-" + time.Now().UTC().Format("2006-01-02T15:04:05Z") + "-weekly"
	go h.sync()

	writeJSON(w, map[string]string{
		"task_id":    taskID,
		"started_at": time.Now().UTC().Format(time.RFC3339),
		"status":     "running",
	})
}

// Healthz GET /healthz - 不鉴权的健康检查
func (h *WeeklyHandler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
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

	includeUnenriched := q.Get("include_unenriched") == "true"

	return model.QueryParams{
		Page:              page,
		PageSize:          pageSize,
		Issue:             q.Get("issue"),
		IssueFrom:         issueFrom,
		IssueTo:           issueTo,
		Language:          q.Get("lang"),
		Sort:              q.Get("sort"),
		IncludeUnenriched: includeUnenriched,
	}
}
