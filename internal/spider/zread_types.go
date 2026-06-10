// Package spider 提供 weekly-api 的外部数据源拉取器。
//
// 本包从 starcat-trending-api/internal/spider/ 复制并改造,v0.5
// 把 zread 周 trending 从 trending-api 迁出到 weekly-api。
//
// 字段语义与 zread 公开 JSON 端点 1:1 对齐；解析时只取必要字段，
// 未知字段容错忽略（zread 后续增字段不会破坏本解析）。
package spider

// ZreadFetchResult zread JSON 端点顶层响应。
//
// 端点：https://zread.ai/api/v1/public/repo/trending（无鉴权，忽略所有 query 参数）
// 固定返回 10 group / 153 repo / 周更（每周一 00:00 UTC 更新）。
type ZreadFetchResult struct {
	Code int          `json:"code"` // 0 = 成功
	Msg  string       `json:"msg"`
	Data []ZreadGroup `json:"data"`
}

// ZreadGroup 一组 trending（一个时间窗口，如 "This Week" / "Last Week"）。
type ZreadGroup struct {
	Title    string      `json:"title"`     // "This Week" / "Last Week" / ""（历史周）
	TimeSpan ZreadTime   `json:"time_span"` // 起止日期（MM/DD 格式，无年份）
	Repos    []ZreadRepo `json:"repos"`
}

// ZreadTime zread 端点时间窗口的起止，MM/DD 格式。
// 年份需要由调用方根据"now" 推断（见 zread_year_infer.go）。
type ZreadTime struct {
	Start string `json:"start"` // "08/06" MM/DD
	End   string `json:"end"`   // "14/06" MM/DD
}

// ZreadRepo zread 单个 repo 原始数据。
type ZreadRepo struct {
	RepoID        string   `json:"repo_id"`
	Owner         string   `json:"owner"`
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Description   string   `json:"description"`
	DescriptionZh string   `json:"description_zh"`
	StarCount     int      `json:"star_count"`
	Language      string   `json:"language"`
	Topics        []string `json:"topics"`
	WikiID        string   `json:"wiki_id"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}
