package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dong4j/starcat-weekly-api/internal/enricher"
	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/tokenpool"
)

const defaultGitHubBaseURL = "https://api.github.com"

// GitHubClient 拉取 Discovery 分类所需的 repo metadata 与 README 摘要。
type GitHubClient struct {
	baseURL string
	client  *http.Client
	pool    *tokenpool.Pool
	limiter *enricher.RateLimitHandler
}

// NewGitHubClient 创建共享 Token Pool 的 Discovery GitHub 客户端。
func NewGitHubClient(client *http.Client, pool *tokenpool.Pool, limiter *enricher.RateLimitHandler) *GitHubClient {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &GitHubClient{baseURL: defaultGitHubBaseURL, client: client, pool: pool, limiter: limiter}
}

type githubRepo struct {
	ID            int64    `json:"id"`
	Description   *string  `json:"description"`
	Homepage      *string  `json:"homepage"`
	Language      *string  `json:"language"`
	Stars         int      `json:"stargazers_count"`
	Forks         int      `json:"forks_count"`
	Watchers      int      `json:"watchers_count"`
	Subscribers   int      `json:"subscribers_count"`
	OpenIssues    int      `json:"open_issues_count"`
	Topics        []string `json:"topics"`
	Archived      bool     `json:"archived"`
	Fork          bool     `json:"fork"`
	Private       bool     `json:"private"`
	DefaultBranch string   `json:"default_branch"`
	PushedAt      string   `json:"pushed_at"`
	UpdatedAt     string   `json:"updated_at"`
	CreatedAt     string   `json:"created_at"`
	License       *struct {
		SPDXID *string `json:"spdx_id"`
	} `json:"license"`
	Owner *struct {
		AvatarURL *string `json:"avatar_url"`
	} `json:"owner"`
}

type githubReadme struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// GitHubHTTPError 让编排层区分永久 404 与可重试错误。
type GitHubHTTPError struct {
	StatusCode int
	Message    string
}

func (e *GitHubHTTPError) Error() string { return e.Message }

// Fetch 返回完整 metadata；README 404 视为“无 README”而不是仓库不可用。
func (c *GitHubClient) Fetch(ctx context.Context, owner, repo string) (model.DiscoveryRepo, error) {
	var metadata githubRepo
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo), &metadata); err != nil {
		return model.DiscoveryRepo{}, err
	}

	readme := ""
	var readmeResponse githubReadme
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/readme", owner, repo), &readmeResponse); err != nil {
		if httpErr, ok := err.(*GitHubHTTPError); !ok || httpErr.StatusCode != http.StatusNotFound {
			return model.DiscoveryRepo{}, err
		}
	} else if readmeResponse.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(readmeResponse.Content, "\n", ""))
		if err != nil {
			return model.DiscoveryRepo{}, fmt.Errorf("decode README base64: %w", err)
		}
		readme = sanitizeREADME(string(decoded), 2000)
	}

	result := model.DiscoveryRepo{
		Owner: owner, Repo: repo, GhRepoID: metadata.ID,
		Description: stringValue(metadata.Description), Homepage: stringValue(metadata.Homepage),
		Language: stringValue(metadata.Language), Stars: metadata.Stars, Forks: metadata.Forks,
		Watchers: metadata.Watchers, Subscribers: metadata.Subscribers, OpenIssues: metadata.OpenIssues,
		DefaultBranch: metadata.DefaultBranch, Topics: metadata.Topics, PushedAt: metadata.PushedAt,
		UpdatedAt: metadata.UpdatedAt, CreatedAt: metadata.CreatedAt, IsArchived: metadata.Archived,
		IsFork: metadata.Fork, IsPrivate: metadata.Private, READMEExcerpt: readme,
	}
	if metadata.Owner != nil {
		result.OwnerAvatar = stringValue(metadata.Owner.AvatarURL)
	}
	if metadata.License != nil {
		result.LicenseSpdx = stringValue(metadata.License.SPDXID)
	}
	return result, nil
}

func (c *GitHubClient) get(ctx context.Context, path string, target any) error {
	token := c.pool.PickBest()
	if token == nil {
		return fmt.Errorf("no available GitHub token; earliest reset %s", c.pool.EarliestReset().Format(time.RFC3339))
	}
	if c.limiter != nil {
		c.limiter.Wait()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token.Value)
	req.Header.Set("User-Agent", "starcat-weekly-api")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.pool.UpdateFromResponse(token, resp)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		message := fmt.Sprintf("GitHub %s HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			pauseUntil := token.ResetAt
			if retryAfter, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && retryAfter > 0 {
				candidate := time.Now().Add(time.Duration(retryAfter) * time.Second)
				if candidate.After(pauseUntil) {
					pauseUntil = candidate
				}
			}
			if c.limiter != nil && pauseUntil.After(time.Now()) {
				c.limiter.Pause(pauseUntil)
			}
		}
		return &GitHubHTTPError{StatusCode: resp.StatusCode, Message: message}
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

var (
	markdownImagePattern = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	htmlTagPattern       = regexp.MustCompile(`<[^>]+>`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
)

func sanitizeREADME(raw string, maxRunes int) string {
	cleaned := markdownImagePattern.ReplaceAllString(raw, "")
	cleaned = markdownLinkPattern.ReplaceAllString(cleaned, "$1")
	cleaned = htmlTagPattern.ReplaceAllString(cleaned, " ")
	lines := strings.Split(cleaned, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if trimmed == "" || strings.Contains(lower, "shields.io") || lower == "table of contents" || lower == "toc" {
			continue
		}
		kept = append(kept, trimmed)
	}
	cleaned = strings.Join(kept, "\n")
	if utf8.RuneCountInString(cleaned) <= maxRunes {
		return cleaned
	}
	runes := []rune(cleaned)
	return string(runes[:maxRunes])
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
