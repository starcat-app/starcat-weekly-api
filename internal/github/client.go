// Package github 提供统一的 GitHub API 客户端，供 enricher / discovery / zread 共用。
//
// Client 封装了 Token 池选择、速率限制等待、HTTP 调用、响应状态码分支处理，
// 三个消费者共享同一个 Client 实例以统筹 GitHub API 配额。
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/tokenpool"
)

// ErrRepoNotFound GitHub 返回 404 时返回此错误，调用方可用 errors.Is 判断。
var ErrRepoNotFound = errors.New("repo not found (404)")

// ErrRateLimited GitHub 返回 429/403（速率限制）时返回此错误。
// 客户端内部已调用 Pause 让所有后续请求等待，调用方只需跳过本次。
var ErrRateLimited = errors.New("rate limited")

// RepoResponse GET /repos/{owner}/{repo} 的统一返回结构。
//
// 所有指针字段在 API 返回 null 时为 nil，调用方自行映射到各自的 model。
type RepoResponse struct {
	ID            int64
	Description   *string
	Homepage      *string
	Language      *string
	Stars         int
	Forks         int
	Watchers      int
	Subscribers   int
	OpenIssues    int
	Topics        []string
	LicenseSpdx   *string
	OwnerAvatar   *string
	Archived      bool
	Fork          bool
	Private       bool
	DefaultBranch string
	PushedAt      string
	UpdatedAt     string
	CreatedAt     string
}

// Client 统一的 GitHub API 客户端。
//
// enricher / discovery / zread 三个消费者共用同一个 Client 实例，
// 共享 Token Pool 与 RateLimitHandler，确保配额统筹。
type Client struct {
	baseURL string
	http    *http.Client
	pool    *tokenpool.Pool
	limiter *RateLimitHandler
}

// NewClient 创建 GitHub API 客户端。
func NewClient(pool *tokenpool.Pool, limiter *RateLimitHandler) *Client {
	return &Client{
		baseURL: "https://api.github.com",
		http:    &http.Client{Timeout: 30 * time.Second},
		pool:    pool,
		limiter: limiter,
	}
}

// SetBaseURL 覆盖 API 基础 URL（测试用，如 httptest.NewServer 的 URL）。
func (c *Client) SetBaseURL(url string) {
	c.baseURL = url
}

// SetHTTPClient 覆盖 HTTP 客户端（测试用）。
func (c *Client) SetHTTPClient(client *http.Client) {
	c.http = client
}

// GetRepo 调 GET /repos/{owner}/{repo}，返回统一 RepoResponse。
//
// 内部处理：token 选择、速率限制等待、3 次重试（429/401/5xx）、pool 状态更新。
// 调用方只需关心返回的 RepoResponse 或 error。
func (c *Client) GetRepo(ctx context.Context, owner, repo string) (*RepoResponse, error) {
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := c.getRepoOnce(ctx, owner, repo)
		if err == nil {
			return resp, nil
		}

		// 速率限制 → 已内部 Pause，等一轮再重试
		if errors.Is(err, ErrRateLimited) {
			continue
		}

		// 401 可能是 token 临时失效，换 token 重试
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized {
			continue
		}

		// 5xx 重试
		if errors.As(err, &httpErr) && httpErr.StatusCode >= 500 {
			continue
		}

		return nil, err
	}
	return nil, fmt.Errorf("GetRepo %s/%s failed after 3 attempts", owner, repo)
}

func (c *Client) getRepoOnce(ctx context.Context, owner, repo string) (*RepoResponse, error) {
	token := c.pool.PickBest()
	if token == nil {
		resetAt := c.pool.EarliestReset()
		if !resetAt.IsZero() && resetAt.After(time.Now()) {
			d := time.Until(resetAt)
			log.Printf("[github] no tokens, sleeping %v until %s", d.Round(time.Second), resetAt.Format(time.RFC3339))
			time.Sleep(d)
		}
		return nil, fmt.Errorf("no available GitHub token")
	}

	if c.limiter != nil {
		c.limiter.Wait()
	}

	url := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token.Value)
	req.Header.Set("User-Agent", "starcat-weekly-api")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	c.pool.UpdateFromResponse(token, resp)

	switch resp.StatusCode {
	case http.StatusOK:
		var apiResp githubRepoAPIResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			return nil, fmt.Errorf("decode repo response: %w", err)
		}
		return apiResp.toRepoResponse(), nil

	case http.StatusNotFound:
		return nil, ErrRepoNotFound

	case http.StatusForbidden, http.StatusTooManyRequests:
		c.handleRateLimit(resp, token)
		return nil, ErrRateLimited

	case http.StatusUnauthorized:
		return nil, &HTTPError{StatusCode: resp.StatusCode, Message: "unauthorized"}

	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := fmt.Sprintf("GitHub /repos/%s/%s HTTP %d: %s", owner, repo, resp.StatusCode, strings.TrimSpace(string(body)))
		if resp.StatusCode >= 500 {
			return nil, &HTTPError{StatusCode: resp.StatusCode, Message: msg}
		}
		return nil, &HTTPError{StatusCode: resp.StatusCode, Message: msg}
	}
}

// GetReadme 调 GET /repos/{owner}/{repo}/readme，返回 base64 解码后的内容。
//
// README 404 返回空字符串 + ErrRepoNotFound（调用方可自行决定是否视为"无 README"）。
// 429/403 内部已调用 Pause 并返回 ErrRateLimited。
func (c *Client) GetReadme(ctx context.Context, owner, repo string) (string, error) {
	token := c.pool.PickBest()
	if token == nil {
		return "", fmt.Errorf("no available GitHub token")
	}

	if c.limiter != nil {
		c.limiter.Wait()
	}

	url := fmt.Sprintf("%s/repos/%s/%s/readme", c.baseURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token.Value)
	req.Header.Set("User-Agent", "starcat-weekly-api")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	c.pool.UpdateFromResponse(token, resp)

	if resp.StatusCode == http.StatusNotFound {
		return "", ErrRepoNotFound
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		c.handleRateLimit(resp, token)
		return "", ErrRateLimited
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", &HTTPError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("GitHub %s HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))}
	}

	var readmeResp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&readmeResp); err != nil {
		return "", fmt.Errorf("decode readme: %w", err)
	}

	if readmeResp.Encoding != "base64" {
		return "", fmt.Errorf("unsupported readme encoding: %s", readmeResp.Encoding)
	}

	// GitHub README 内容中包含换行符，先清理再解码
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' {
			return -1
		}
		return r
	}, readmeResp.Content)

	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", fmt.Errorf("decode README base64: %w", err)
	}
	return string(decoded), nil
}

// handleRateLimit 从响应头提取 Retry-After + token reset，调 Pause 挂起后续请求。
func (c *Client) handleRateLimit(resp *http.Response, token *tokenpool.TokenState) {
	pauseUntil := token.ResetAt
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
			ra := time.Now().Add(time.Duration(secs) * time.Second)
			if ra.After(pauseUntil) {
				pauseUntil = ra
			}
		}
	}
	if pauseUntil.Before(time.Now().Add(60 * time.Second)) {
		pauseUntil = time.Now().Add(60 * time.Second)
	}
	log.Printf("[github] rate limited (%d), pausing until %s", resp.StatusCode, pauseUntil.Format(time.RFC3339))
	if c.limiter != nil {
		c.limiter.Pause(pauseUntil)
	}
}

// HTTPError 非 200/404/429 的 GitHub HTTP 错误，保留状态码供调用方分支判断。
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string { return e.Message }

// --- 内部 GitHub API 响应结构（仅用于 JSON 解码） ---

type githubRepoAPIResponse struct {
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
		SpdxID *string `json:"spdx_id"`
	} `json:"license"`
	Owner *struct {
		AvatarURL *string `json:"avatar_url"`
	} `json:"owner"`
}

func (r *githubRepoAPIResponse) toRepoResponse() *RepoResponse {
	out := &RepoResponse{
		ID:            r.ID,
		Description:   r.Description,
		Homepage:      r.Homepage,
		Language:      r.Language,
		Stars:         r.Stars,
		Forks:         r.Forks,
		Watchers:      r.Watchers,
		Subscribers:   r.Subscribers,
		OpenIssues:    r.OpenIssues,
		Topics:        r.Topics,
		Archived:      r.Archived,
		Fork:          r.Fork,
		Private:       r.Private,
		DefaultBranch: r.DefaultBranch,
		PushedAt:      r.PushedAt,
		UpdatedAt:     r.UpdatedAt,
		CreatedAt:     r.CreatedAt,
	}
	if r.License != nil {
		out.LicenseSpdx = r.License.SpdxID
	}
	if r.Owner != nil {
		out.OwnerAvatar = r.Owner.AvatarURL
	}
	return out
}
