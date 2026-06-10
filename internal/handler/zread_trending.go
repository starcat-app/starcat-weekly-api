// Package handler 提供 zread 周 trending API 处理器。
//
// v0.5 R-02 新增：独立端点 GET /api/v1/trending/zread（决策 ②），
// 不增加 ?source= 参数、不动现有阮一峰周刊端点。
//
// envelope 走共享件 envelope.go（Source / MergedFromXxx 4 字段 omitempty 不输出）；
// 通过 data 内嵌的 week_label 区分 zread 数据源。
package handler

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

// ZreadTrendingHandler zread 周 trending 处理器。
type ZreadTrendingHandler struct {
	store store.Store
}

// NewZreadTrendingHandler 创建处理器。
func NewZreadTrendingHandler(s store.Store) *ZreadTrendingHandler {
	return &ZreadTrendingHandler{store: s}
}

// HandleZreadTrendingV1 GET /api/v1/trending/zread?week=this|last|YYYY-MM-DD&limit=20
//
// 返回当前（或历史）zread 周 trending 列表。
// 响应 envelope.data 是 ZreadTrendingEnvelope（model 内定义），含 week_label / week_start / items。
func (h *ZreadTrendingHandler) HandleZreadTrendingV1(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	week := strings.TrimSpace(q.Get("week"))
	if week == "" {
		week = "this"
	}

	limit := 20
	if l := q.Get("limit"); l != "" {
		var n int
		if _, err := fmt.Sscanf(l, "%d", &n); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 50 {
		limit = 50
	}

	items, err := h.store.QueryZreadTrending(week, limit)
	if err != nil {
		log.Printf("[handler] QueryZreadTrending(week=%s, limit=%d): %v", week, limit, err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
		return
	}

	// 构造响应 data（与设计文档 §8.4.2 示例对齐）
	envelope := model.ZreadTrendingEnvelope{
		Items: items,
	}
	if len(items) > 0 {
		// 从第一条数据带出 week 元信息（week 是聚合值，items 共享）
		envelope.WeekLabel = items[0].WeekLabel
		envelope.WeekStart = items[0].WeekStart
		envelope.WeekEnd = items[0].WeekEnd
		envelope.FetchedAt = items[0].FetchedAt
	} else {
		envelope.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	}

	meta := &model.Meta{
		Total:       len(items),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		CacheStatus: cacheStatus(items),
	}
	writeJSONWithMeta(w, envelope, meta)
}

// cacheStatus 根据 items 数量判断 fresh / cold。
func cacheStatus(items []model.ZreadTrending) string {
	if len(items) == 0 {
		return "cold"
	}
	return "fresh"
}
