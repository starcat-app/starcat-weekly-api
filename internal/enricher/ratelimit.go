// Package enricher 提供 GitHub API 字段补全。
//
// RateLimitHandler 已迁移到 internal/github 包，本文件保留 re-export
// 以兼容现有引用（main.go / discovery / 测试等）。
//
// ⚠️ 新代码应直接 import "github.com/dong4j/starcat-weekly-api/internal/github"
// 并使用 github.RateLimitHandler。
package enricher

import "github.com/dong4j/starcat-weekly-api/internal/github"

// RateLimitHandler re-export（兼容旧引用）。
type RateLimitHandler = github.RateLimitHandler

// NewRateLimitHandler re-export。
var NewRateLimitHandler = github.NewRateLimitHandler
