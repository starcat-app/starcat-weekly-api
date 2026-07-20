// Package handler 中的 bulk.go 实现 GET /api/v1/repos/bulk endpoint。
//
// R-06.3（2026-06-15）：weekly 客户端整张视图（~4000 条聚合 repo + languages 聚合）
// 一次性出，避免老路径的"分页拉 80+ 次 /api/v1/repos"网络浪费。
//
// 响应形式（schema_version=2）:
//
//	{
//	  "schema_version": 2,
//	  "data": {
//	    "sources":   [<SourceDescriptor>...],  // 动态来源目录 + count
//	    "repos":     [<RepoFeedItem>...],     // ~4000 条全量
//	    "languages": [<LanguageAggregate>...] // 聚合后语言列表
//	  },
//	  "meta": {
//	    "total":        4123,                  // = len(repos)
//	    "generated_at": "2026-06-15T13:00:00Z" // payload 构建时间（不是请求时刻）
//	  }
//	}
//
// 性能策略:
//   - 一次性 build payload + 预压缩 gzip，缓存 6h（详见 bulk_cache.go）
//   - cache hit + Accept-Encoding: gzip → 直接写 pre-compressed gzip（~16% 原大小）
//   - cache hit + 客户端带 If-None-Match 等同 ETag → 304 + 无 body（最省带宽）
//   - cache miss / 过期 → 查 store（QueryAllRepos + GetAggregatedLanguages）+
//     marshal + gzip + cache.Set + 写响应
//
// 注: 不接受任何 query 参数。客户端拿到全量后本地做 source/lang/sort/page 过滤。
package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

// bulkData 是 /api/v1/repos/bulk envelope 的 data 字段类型。
//
// 字段顺序与 JSON 顺序一致，方便客户端 streaming decoder 早早拿到 repos 主体
// 后再读 languages 副表（虽然 Go encoding/json 不保证 stream parse，但稳定字段
// 顺序对 LLM/CLI 调试也更友好）。
type bulkData struct {
	Sources   []model.SourceDescriptor  `json:"sources"`
	Repos     []model.RepoFeedItem      `json:"repos"`
	Languages []model.LanguageAggregate `json:"languages"`
}

// HandleBulkV1 GET /api/v1/repos/bulk — 一次性返回 repos + languages 全量打包。
//
// cache 是必传参数（不接 nil）：测试也用 `NewBulkCache()` 显式构造，避免运行时
// cache 被悄悄关掉的风险。
func HandleBulkV1(s store.Store, cache *BulkCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if entry, ok := cache.Get(); ok {
			writeBulkResponse(w, r, entry)
			return
		}

		repos, err := s.QueryAllRepos()
		if err != nil {
			log.Printf("[handler] QueryAllRepos: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query repos", nil)
			return
		}
		langs, err := s.GetAggregatedLanguages()
		if err != nil {
			log.Printf("[handler] GetAggregatedLanguages: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query languages", nil)
			return
		}
		sources, err := s.GetSourceCatalog()
		if err != nil {
			log.Printf("[handler] GetSourceCatalog: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query sources", nil)
			return
		}

		env := model.Envelope[bulkData]{
			SchemaVersion: 2,
			Data: bulkData{
				Sources:   sources,
				Repos:     repos,
				Languages: langs,
			},
			Meta: &model.Meta{
				Total:       len(repos),
				GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			},
		}
		payload, err := json.Marshal(env)
		if err != nil {
			log.Printf("[handler] marshal bulk envelope: %v", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to encode response", nil)
			return
		}

		entry := cache.Set(payload)
		writeBulkResponse(w, r, entry)
	}
}

// writeBulkResponse 把 entry 写到响应。
//
// 流程:
//  1. 设 ETag / Last-Modified / Vary: Accept-Encoding header
//  2. 若 If-None-Match 匹配 → 304（无 body）
//  3. 否则按 Accept-Encoding: gzip 决定写压缩 / 未压缩
//
// `Vary: Accept-Encoding` 必须设，否则中间层缓存（CDN / 反向代理）会用压缩响应
// 给不支持 gzip 的客户端，反之亦然。
func writeBulkResponse(w http.ResponseWriter, r *http.Request, entry *bulkCacheEntry) {
	w.Header().Set("ETag", entry.etag)
	w.Header().Set("Last-Modified", entry.lastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")

	if match := r.Header.Get("If-None-Match"); match != "" && match == entry.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	acceptsGzip := strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
	if acceptsGzip && len(entry.gzipPayload) > 0 {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(entry.gzipPayload); err != nil {
			log.Printf("[handler] write gzipped bulk payload: %v", err)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(entry.payload); err != nil {
		log.Printf("[handler] write bulk payload: %v", err)
	}
}
