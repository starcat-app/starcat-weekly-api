// Package enricher 通过 github.Client 补全项目的 GitHub 元数据。
//
// 此前 enricher 自己封装 HTTP 调用、Token 池、速率限制。
// 统一到 internal/github 后，enricher 只负责：
//  1. 从 store 取未补全的 project → 调 github.Client.GetRepo()
//  2. 将 RepoResponse 映射到 model.Project → 调 store.UpdateProjectMeta()
//
// RateLimitHandler 已迁移到 internal/github 包，本文件保留 re-export 以兼容旧引用。
package enricher

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/github"
	"github.com/dong4j/starcat-weekly-api/internal/model"
	"github.com/dong4j/starcat-weekly-api/internal/store"
)

// Enricher GitHub 元数据补全器。
type Enricher struct {
	store  store.Store
	client *github.Client
}

// NewEnricher 创建补全器。
func NewEnricher(s store.Store, client *github.Client) *Enricher {
	return &Enricher{store: s, client: client}
}

// EnrichAll 批量补全所有未补全的项目（阻塞，适合启动时 / zread 同步后调用）。
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
			e.enrichProject(&projects[i])
		}
	}
}

// EnrichBatch 增量补全（非阻塞，适合 cron 调用）。
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
			e.enrichProject(&projects[i])
		}
	}()
}

// enrichProject 补全单个 project（阮一峰周刊 projects 表）。
func (e *Enricher) enrichProject(p *model.Project) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := e.client.GetRepo(ctx, p.RepoOwner, p.RepoName)
	if err != nil {
		if errors.Is(err, github.ErrRepoNotFound) {
			p.IsAvailable = false
			if err := e.store.UpdateProjectMeta(p); err != nil {
				log.Printf("[enricher] mark unavailable %s/%s: %v", p.RepoOwner, p.RepoName, err)
			}
			return
		}
		// 速率限制或网络错误，本轮跳过，下次 cron 重试
		log.Printf("[enricher] %s/%s: %v", p.RepoOwner, p.RepoName, err)
		return
	}

	// 映射 github.RepoResponse → model.Project
	p.GhRepoID = resp.ID
	if resp.Description != nil {
		p.Description = *resp.Description
	}
	p.Stars = resp.Stars
	p.Forks = resp.Forks
	p.Watchers = resp.Watchers
	p.Subscribers = resp.Subscribers
	if resp.Language != nil {
		p.Language = *resp.Language
	}
	if resp.Homepage != nil && *resp.Homepage != "" {
		p.Homepage = *resp.Homepage
	}
	if resp.LicenseSpdx != nil {
		p.LicenseSpdx = *resp.LicenseSpdx
	}
	if resp.OwnerAvatar != nil {
		p.OwnerAvatar = *resp.OwnerAvatar
	}
	p.IsArchived = resp.Archived
	p.IsFork = resp.Fork
	p.IsPrivate = resp.Private
	p.DefaultBranch = resp.DefaultBranch
	p.OpenIssues = resp.OpenIssues
	p.PushedAt = resp.PushedAt
	p.UpdatedAt = resp.UpdatedAt
	p.CreatedAt = resp.CreatedAt
	p.IsAvailable = true

	if err := e.store.UpdateProjectMeta(p); err != nil {
		log.Printf("[enricher] update %s/%s: %v", p.RepoOwner, p.RepoName, err)
	}
}

// EnrichAllZread 批量补全所有未补全的 zread trending repos。
func (e *Enricher) EnrichAllZread() {
	for {
		zreads, err := e.store.GetUnenrichedZreadRepos(50)
		if err != nil {
			log.Printf("[enricher] query unenriched zread: %v", err)
			return
		}
		if len(zreads) == 0 {
			log.Printf("[enricher] 所有 zread repos 已补全")
			return
		}

		log.Printf("[enricher] 待补全 zread %d 个...", len(zreads))
		for i := range zreads {
			e.enrichZread(&zreads[i])
		}
	}
}

// enrichZread 补全单条 zread trending 记录的 GitHub 元数据。
func (e *Enricher) enrichZread(z *model.ZreadTrending) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := e.client.GetRepo(ctx, z.Owner, z.Name)
	if err != nil {
		if errors.Is(err, github.ErrRepoNotFound) {
			// zread 仓库存续期短，404 只打日志不标 unavailable
			log.Printf("[enricher] zread %s/%s: repo gone (404), skip", z.Owner, z.Name)
			return
		}
		log.Printf("[enricher] zread %s/%s: %v", z.Owner, z.Name, err)
		return
	}

	// 映射 github.RepoResponse → model.ZreadTrending
	z.GhRepoID = resp.ID
	z.Forks = resp.Forks
	z.OpenIssues = resp.OpenIssues
	z.Watchers = resp.Watchers
	z.SubscribersCount = resp.Subscribers
	if resp.PushedAt != "" {
		z.PushedAt = resp.PushedAt
	}
	if resp.UpdatedAt != "" {
		z.UpdatedAt = resp.UpdatedAt
	}
	if resp.CreatedAt != "" {
		z.CreatedAt = resp.CreatedAt
	}
	if resp.LicenseSpdx != nil {
		z.LicenseSpdx = *resp.LicenseSpdx
	}
	if resp.DefaultBranch != "" {
		z.DefaultBranch = resp.DefaultBranch
	}
	z.IsArchived = resp.Archived
	z.IsFork = resp.Fork

	if err := e.store.UpdateZreadEnriched(z.Owner, z.Name, z.WeekStart, z); err != nil {
		log.Printf("[enricher] update zread %s/%s: %v", z.Owner, z.Name, err)
	}
}
