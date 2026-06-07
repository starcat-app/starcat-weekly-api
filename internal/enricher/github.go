// Package enricher 通过 GitHub API 补全项目的 stars、语言、描述等元数据
package enricher

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

// GitHubRepo API 返回结构（仅取所需字段）
type githubRepoResponse struct {
	FullName    string   `json:"full_name"`
	Description *string  `json:"description"`
	Stargazers  int      `json:"stargazers_count"`
	Language    *string  `json:"language"`
	Topics      []string `json:"topics"`
	Message     string   `json:"message"` // 错误信息
}

// Enricher GitHub 元数据补全器
type Enricher struct {
	store   store.Store
	client  *http.Client
	token   string
	limiter *rateLimiter
}

// rateLimiter 简易令牌桶限速器（5000 次/小时）
type rateLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

func newRateLimiter(perHour int) *rateLimiter {
	return &rateLimiter{
		interval: time.Hour / time.Duration(perHour),
	}
}

func (rl *rateLimiter) wait() {
	rl.mu.Lock()
	elapsed := time.Since(rl.last)
	if elapsed < rl.interval {
		time.Sleep(rl.interval - elapsed)
	}
	rl.last = time.Now()
	rl.mu.Unlock()
}

// NewEnricher 创建补全器
func NewEnricher(s store.Store) *Enricher {
	token := os.Getenv("GITHUB_TOKEN")
	return &Enricher{
		store:   s,
		client:  &http.Client{Timeout: 15 * time.Second},
		token:   token,
		limiter: newRateLimiter(5000),
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

// enrichOne 补全单个项目
func (e *Enricher) enrichOne(p *model.Project) {
	e.limiter.wait()

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", p.RepoOwner, p.RepoName)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "starcat-weekly-api")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		log.Printf("[enricher] HTTP %s/%s: %v", p.RepoOwner, p.RepoName, err)
		return
	}
	defer resp.Body.Close()

	// 404 → 标记不可用
	if resp.StatusCode == http.StatusNotFound {
		p.IsAvailable = false
		if err := e.store.UpdateProjectMeta(p); err != nil {
			log.Printf("[enricher] update %s/%s: %v", p.RepoOwner, p.RepoName, err)
		}
		return
	}

	// 速率限制 → 等一等再继续
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		log.Printf("[enricher] 速率限制触发，等待 60s...")
		time.Sleep(60 * time.Second)
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

	// 用 GitHub 上的官方描述覆盖（如果周刊描述较短或为空）
	ghDesc := ""
	if gh.Description != nil {
		ghDesc = strings.TrimSpace(*gh.Description)
	}
	if ghDesc != "" && (len(ghDesc) > len(p.Description) || p.Description == "") {
		p.Description = ghDesc
	}

	p.Stars = gh.Stargazers
	if gh.Language != nil {
		p.Language = *gh.Language
	}
	if len(gh.Topics) > 0 {
		topicsJSON, _ := json.Marshal(gh.Topics)
		p.Topics = string(topicsJSON)
	}
	p.IsAvailable = true

	if err := e.store.UpdateProjectMeta(p); err != nil {
		log.Printf("[enricher] update %s/%s: %v", p.RepoOwner, p.RepoName, err)
	}
}
