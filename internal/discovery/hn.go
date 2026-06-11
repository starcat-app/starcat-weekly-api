// Package discovery 实现 Show HN -> GitHub -> AI 分类的数据流水线。
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

const defaultHNBaseURL = "https://hacker-news.firebaseio.com/v0"

var (
	githubURLPattern  = regexp.MustCompile(`(?i)https?://(?:www\.)?github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`)
	githubPartPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// HNClient 通过 Hacker News 官方 API 拉取 Show HN 投稿。
// 使用官方 JSON API 可以避免依赖 HTML DOM 和额外的 Algolia 请求。
type HNClient struct {
	baseURL   string
	client    *http.Client
	userAgent string
}

// NewHNClient 创建 HN 官方 API 客户端。
func NewHNClient(client *http.Client) *HNClient {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &HNClient{
		baseURL:   defaultHNBaseURL,
		client:    client,
		userAgent: "Starcat-Discovery-Bot/1.0 (+https://github.com/dong4j/starcat)",
	}
}

type hnItem struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Text        string `json:"text"`
	Score       int    `json:"score"`
	Descendants int    `json:"descendants"`
	Time        int64  `json:"time"`
	Deleted     bool   `json:"deleted"`
	Dead        bool   `json:"dead"`
}

// Fetch 拉取最新 Show HN item，并为每个 GitHub 链接生成独立投稿记录。
func (c *HNClient) Fetch(ctx context.Context, limit int, now time.Time) ([]model.DiscoverySubmission, error) {
	if limit < 1 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}

	var ids []int64
	if err := c.getJSON(ctx, c.baseURL+"/showstories.json", &ids); err != nil {
		return nil, fmt.Errorf("fetch showstories: %w", err)
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}

	result := make([]model.DiscoverySubmission, 0, len(ids))
	for _, id := range ids {
		var item hnItem
		if err := c.getJSON(ctx, fmt.Sprintf("%s/item/%d.json", c.baseURL, id), &item); err != nil {
			return nil, fmt.Errorf("fetch HN item %d: %w", id, err)
		}
		if item.Deleted || item.Dead || item.Type != "story" {
			continue
		}
		for _, repo := range extractGitHubRepos(item.URL + "\n" + html.UnescapeString(item.Text)) {
			result = append(result, model.DiscoverySubmission{
				HNID: id, Owner: repo.owner, Repo: repo.repo, Title: item.Title,
				HNURL:     fmt.Sprintf("https://news.ycombinator.com/item?id=%d", id),
				SourceURL: item.URL, Score: item.Score, Comments: item.Descendants,
				PublishedAt: time.Unix(item.Time, 0).UTC(), FirstSeenAt: now.UTC(), LastSeenAt: now.UTC(),
			})
		}
	}
	return result, nil
}

func (c *HNClient) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

type repoRef struct{ owner, repo string }

func extractGitHubRepos(raw string) []repoRef {
	seen := make(map[string]bool)
	result := make([]repoRef, 0)
	for _, match := range githubURLPattern.FindAllString(raw, -1) {
		parsed, err := url.Parse(match)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) < 2 {
			continue
		}
		owner := parts[0]
		repo := strings.TrimRight(parts[1], ".,;:!?)\"]}'")
		repo = strings.TrimSuffix(repo, ".git")
		if !validGitHubRepo(owner, repo) {
			continue
		}
		key := strings.ToLower(owner + "/" + repo)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, repoRef{owner: owner, repo: repo})
	}
	return result
}

func validGitHubRepo(owner, repo string) bool {
	if owner == "" || repo == "" || !githubPartPattern.MatchString(owner) || !githubPartPattern.MatchString(repo) {
		return false
	}
	switch strings.ToLower(owner) {
	case "about", "collections", "events", "features", "login", "marketplace", "new", "orgs", "pricing", "search", "settings", "sponsors", "topics", "trending", "users":
		return false
	default:
		return true
	}
}
