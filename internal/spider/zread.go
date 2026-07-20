// Package spider 提供 zread 周 trending 接入 weekly-api 的实现。
//
// 从 starcat-trending-api/internal/spider/zread.go 复制并改造：
//  1. import 路径改成 weekly-api
//  2. BaseRequest 不复用（weekly-api 没有），改用 stdlib http.Client
//  3. store 写入接口改成 UpsertZreadTrending（决策 ① 独立建表）
//
// 设计文档：19-wiki集成.md §8.2 / §8.3 / §8.4
package spider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

// ZreadSpider 拉取 zread 公开周 trending 端点，写入 zread_trending 表。
//
// 端点契约见 19-wiki集成.md §8.1：无鉴权、固定返回 10 group / 153 repo / 周更。
// 年份推断走 InferYear（见 zread_year_infer.go）。
type ZreadSpider struct {
	client *http.Client
	store  *store.SQLiteStore
	url    string
}

// NewZreadSpider 创建 spider。
func NewZreadSpider(s *store.SQLiteStore) *ZreadSpider {
	return &ZreadSpider{
		client: &http.Client{Timeout: 30 * time.Second},
		store:  s,
		url:    "https://zread.ai/api/v1/public/repo/trending",
	}
}

// RunOnce 拉取一次 zread trending 并写入数据库。
//
// 流程：
//  1. HTTP GET zread 端点
//  2. 解析 JSON，校验 code == 0
//  3. 对每个 group 推断年份（InferYear + 异常告警）
//  4. 对每个 repo 调 store.UpsertZreadTrending 写库（决策 ① 独立表）
//
// 失败行为：返回 error，由调用方（scheduler）决定是否重试。
func (s *ZreadSpider) RunOnce(ctx context.Context) error {
	rows, err := s.FetchRows(ctx)
	if err != nil {
		return err
	}
	if s.store == nil {
		return nil
	}
	for _, row := range rows {
		if err := s.store.UpsertZreadTrending(row); err != nil {
			log.Printf("[zread] upsert %s/%s error: %v", row.Owner, row.Name, err)
		}
	}
	return nil
}

// FetchRows pulls zread and returns normalized rows without writing them.
// R-04 scheduler uses this method so it can EnsureGitHubRepo first and then
// attach zread_events by immutable gh_repo_id.
func (s *ZreadSpider) FetchRows(ctx context.Context) ([]model.ZreadTrending, error) {
	log.Println("[zread] starting fetch trending...")

	result, err := s.fetchAndParse(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	rows := make([]model.ZreadTrending, 0)
	for _, group := range result.Data {
		// 推断年份（异常告警由 InferYear 内部 log.Warnf 完成）
		year, err := InferYear(group.TimeSpan.Start, now)
		if err != nil {
			log.Printf("[zread] failed to infer year for %s: %v", group.TimeSpan.Start, err)
			continue
		}

		weekStart := fmt.Sprintf("%d-%s", year, convertMMDD(group.TimeSpan.Start))
		weekEnd := fmt.Sprintf("%d-%s", year, convertMMDD(group.TimeSpan.End))

		for i, r := range group.Repos {
			topics, _ := json.Marshal(r.Topics)
			topicsStr := string(topics)

			row := model.ZreadTrending{
				WeekLabel:     group.Title,
				WeekStart:     weekStart,
				WeekEnd:       weekEnd,
				RankInWeek:    i + 1,
				RepoID:        r.RepoID,
				Owner:         r.Owner,
				Name:          r.Name,
				HTMLURL:       r.URL,
				Description:   r.Description,
				DescriptionZh: r.DescriptionZh,
				StarCount:     r.StarCount,
				Language:      r.Language,
				Topics:        topicsStr,
				WikiID:        r.WikiID,
				// enricher 14 字段（gh_repo_id / forks / open_issues / watchers / subscribers_count /
				// pushed_at / updated_at / created_at / license_spdx / default_branch / is_archived / is_fork）
				// 由 cron 流程后续 enricher.EnrichBatch 补全，spider 只写 zread 拉取原生字段。
				// v0.4.1 跨年回溯字段
				ZreadWeekStartRaw: group.TimeSpan.Start,
				ZreadWeekEndRaw:   group.TimeSpan.End,
				ZreadYearInferred: year,
				FetchedAt:         now.UTC().Format(time.RFC3339),
			}

			rows = append(rows, row)
		}
	}

	log.Printf("[zread] finished fetch %d groups", len(result.Data))
	return rows, nil
}

// fetchAndParse 拉取并解析 zread JSON 端点。
func (s *ZreadSpider) fetchAndParse(ctx context.Context) (*ZreadFetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.url, nil)
	if err != nil {
		return nil, err
	}

	// 防御 Cloudflare / WAF，模拟真实浏览器请求 JSON
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zread status: %d", resp.StatusCode)
	}

	var result ZreadFetchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("zread api error: %d %s", result.Code, result.Msg)
	}

	return &result, nil
}

// convertMMDD 把 zread 的 DD/MM 格式（如 "08/06" = 6 月 8 日）转换为 "MM-DD" 格式。
// zread API 返回 DD/MM，但数据库存储统一使用 MM-DD 以构造 YYYY-MM-DD。
func convertMMDD(ddmm string) string {
	return fmt.Sprintf("%s-%s", ddmm[3:5], ddmm[0:2])
}
