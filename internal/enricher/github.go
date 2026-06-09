// Package enricher 通过 GitHub API 补全项目的 stars、语言、描述等元数据。
//
// R-01 v1.2: 接入 Token Pool（多 PAT 冗余 + Quota-aware）+
// RateLimitHandler（主动退避）+ 14+5 字段扩拉。
package enricher

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
	"github.com/dong4j/starcat-weekly-api/internal/tokenpool"
)

// GitHubRepo API 返回结构
type githubRepoResponse struct {
	ID            int64    `json:"id"`
	FullName      string   `json:"full_name"`
	Description   *string  `json:"description"`
	Stargazers    int      `json:"stargazers_count"`
	Forks         int      `json:"forks_count"`
	Watchers      int      `json:"watchers_count"`
	Subscribers   int      `json:"subscribers_count"`
	Language      *string  `json:"language"`
	Topics        []string `json:"topics"`
	Homepage      *string  `json:"homepage"`
	License       *struct {
		SpdxID *string `json:"spdx_id"`
	} `json:"license"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	OpenIssues    int    `json:"open_issues_count"`
	PushedAt      string `json:"pushed_at"`
	UpdatedAt     string `json:"updated_at"`
	CreatedAt     string `json:"created_at"`
	Owner         *struct {
		AvatarURL *string `json:"avatar_url"`
	} `json:"owner"`
	Message string `json:"message"`
}

// Enricher GitHub 元数据补全器。
// 持有 Token Pool（多 PAT 池）+ RateLimitHandler（请求间隔 + 主动暂停）。
type Enricher struct {
	store   store.Store
	client  *http.Client
	pool    *tokenpool.Pool
	limiter *RateLimitHandler
}

// NewEnricher 创建补全器。
func NewEnricher(s store.Store, pool *tokenpool.Pool, rl *RateLimitHandler) *Enricher {
	return &Enricher{
		store:   s,
		client:  &http.Client{Timeout: 15 * time.Second},
		pool:    pool,
		limiter: rl,
	}
}

// EnrichAll 批量补全所有未补全的项目（阻塞，适合启动时调用）
func (e *Enricher) EnrichAll() {
	for {
		projects, err := e.store.GetUnenrichedProjects(50)
		if err != nil {
			log.Printf("[enricher] query unenriched: %v", err)
			return
		}
		if len(projects) == 0 {
			log.Printf("[enricher] 所有项目已补全")
			return
		}

		log.Printf("[enricher] 待补全 %d 个项目...", len(projects))
		for i := range projects {
			e.enrichOne(&projects[i])
		}
	}
}

// EnrichBatch 增量补全（非阻塞，适合 cron 调用）
func (e *Enricher) EnrichBatch() {
	go func() {
		projects, err := e.store.GetUnenrichedProjects(30)
		if err != nil {
			log.Printf("[enricher] query unenriched: %v", err)
			return
		}
		if len(projects) == 0 {
			return
		}
		log.Printf("[enricher] 增量补全 %d 个项目...", len(projects))
		for i := range projects {
			e.enrichOne(&projects[i])
		}
	}()
}

// enrichOne 补全单个项目。
//
// 流程：
//  1. PickBest 拿 Quota-aware token；nil 则 sleep 到池的 EarliestReset。
//  2. RateLimitHandler.Wait() 阻塞到允许发起请求。
//  3. 调 GET /repos/{o}/{r}，UpdateFromResponse 让 pool 感知 quota / dead。
//  4. 按 status 分支：200 写库 / 404 标 unavailable / 429+403 主动 Pause 到 reset 时刻。
//
// 注：本函数不做 retry（与现有调用方 EnrichAll 行为兼容，
// 失败的 project 会保留 enriched_at=NULL，下次 GetUnenrichedProjects 自动重选）。
func (e *Enricher) enrichOne(p *model.Project) {
	token := e.pool.PickBest()
	if token == nil {
		// 所有 token 都耗尽或已 dead，sleep 到最早的 reset 时刻再退出本轮
		resetAt := e.pool.EarliestReset()
		if !resetAt.IsZero() && resetAt.After(time.Now()) {
			d := time.Until(resetAt)
			log.Printf("[enricher] no available tokens, sleeping %v until %s",
				d.Round(time.Second), resetAt.Format(time.RFC3339))
			time.Sleep(d)
		} else {
			// 兜底：池中没有 ResetAt 信息（启动期所有 token remaining=-1 且全 dead），sleep 60s
			log.Printf("[enricher] no available tokens and no reset info, sleeping 60s")
			time.Sleep(60 * time.Second)
		}
		return
	}

	e.limiter.Wait()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", p.RepoOwner, p.RepoName)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "starcat-weekly-api")
	req.Header.Set("Authorization", "Bearer "+token.Value)

	resp, err := e.client.Do(req)
	if err != nil {
		log.Printf("[enricher] HTTP %s/%s: %v", p.RepoOwner, p.RepoName, err)
		return
	}
	defer resp.Body.Close()

	e.pool.UpdateFromResponse(token, resp)

	// 404 → 标记不可用
	if resp.StatusCode == http.StatusNotFound {
		p.IsAvailable = false
		if err := e.store.UpdateProjectMeta(p); err != nil {
			log.Printf("[enricher] update %s/%s: %v", p.RepoOwner, p.RepoName, err)
		}
		return
	}

	// 速率限制 → 主动 Pause 到 reset 时刻（替代固定 sleep 60s）
	// 这样所有并发 worker 都会自动等到 reset 才继续。
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		pauseUntil := token.ResetAt
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
				ra := time.Now().Add(time.Duration(secs) * time.Second)
				if ra.After(pauseUntil) {
					pauseUntil = ra
				}
			}
		}
		// 兜底：reset 信息缺失或已过期，至少暂停 60s
		if pauseUntil.Before(time.Now().Add(60 * time.Second)) {
			pauseUntil = time.Now().Add(60 * time.Second)
		}
		log.Printf("[enricher] 速率限制触发 (%d)，主动暂停到 %s", resp.StatusCode, pauseUntil.Format(time.RFC3339))
		e.limiter.Pause(pauseUntil)
		return
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("[enricher] %s/%s HTTP %d", p.RepoOwner, p.RepoName, resp.StatusCode)
		return
	}

	var gh githubRepoResponse
	if err := json.NewDecoder(resp.Body).Decode(&gh); err != nil {
		log.Printf("[enricher] decode %s/%s: %v", p.RepoOwner, p.RepoName, err)
		return
	}

	// 更新字段
	p.GhRepoID = gh.ID
	if gh.Description != nil {
		p.Description = strings.TrimSpace(*gh.Description)
	}
	p.Stars = gh.Stargazers
	p.Forks = gh.Forks
	p.Watchers = gh.Watchers
	p.Subscribers = gh.Subscribers
	if gh.Language != nil {
		p.Language = *gh.Language
	}
	if len(gh.Topics) > 0 {
		topicsJSON, _ := json.Marshal(gh.Topics)
		p.Topics = string(topicsJSON)
	}
	if gh.Homepage != nil {
		p.Homepage = *gh.Homepage
	}
	if gh.License != nil && gh.License.SpdxID != nil {
		p.LicenseSpdx = *gh.License.SpdxID
	}
	p.IsArchived = gh.Archived
	p.IsFork = gh.Fork
	p.IsPrivate = gh.Private
	p.DefaultBranch = gh.DefaultBranch
	p.OpenIssues = gh.OpenIssues
	p.PushedAt = gh.PushedAt
	p.UpdatedAt = gh.UpdatedAt
	p.CreatedAt = gh.CreatedAt
	if gh.Owner != nil && gh.Owner.AvatarURL != nil {
		p.OwnerAvatar = *gh.Owner.AvatarURL
	}

	p.IsAvailable = true

	if err := e.store.UpdateProjectMeta(p); err != nil {
		log.Printf("[enricher] update %s/%s: %v", p.RepoOwner, p.RepoName, err)
	}
}
