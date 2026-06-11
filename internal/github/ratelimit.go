// Package github 提供统一的 GitHub API 客户端，供 enricher / discovery / zread 共用。
//
// 包含 RateLimitHandler（请求间隔 + 主动退避）和 Client（HTTP 调用封装）。
// 此前 RateLimitHandler 在 enricher 包内，discovery 跨包引用 enricher.RateLimitHandler，
// 现统一到 github 包，消除跨模块隐式依赖。
package github

import (
	"log"
	"sync"
	"time"
)

// RateLimitHandler 请求间隔约束 + 主动退避。
//
// 两条规则：
//  1. 任意两次请求间至少间隔 minInterval（5000/h 配额下推荐 720ms）
//  2. 主动 Pause(until) 后，所有调用 Wait() 都会 sleep 到 until 时刻
//
// 并发安全：多个 worker 共用一份 RateLimitHandler 也能正常排队。
type RateLimitHandler struct {
	mu          sync.Mutex
	minInterval time.Duration // 兜底间隔，如 720ms（5000/h）
	lastReq     time.Time
	pausedUntil time.Time // 当 remaining 过低时主动暂停到此时刻
}

// NewRateLimitHandler 创建速率限制处理器。
func NewRateLimitHandler(minInterval time.Duration) *RateLimitHandler {
	return &RateLimitHandler{
		minInterval: minInterval,
	}
}

// Wait 在发起请求前调用，必要时 sleep。
// 并发安全：内部串行化排队（lock 期间 sleep 也持锁，等价于全局漏桶）。
func (rl *RateLimitHandler) Wait() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// 主动暂停期
	if time.Now().Before(rl.pausedUntil) {
		d := time.Until(rl.pausedUntil)
		log.Printf("[ratelimit] paused for %v", d.Round(time.Second))
		time.Sleep(d)
	}

	// 最小间隔
	if elapsed := time.Since(rl.lastReq); elapsed < rl.minInterval {
		time.Sleep(rl.minInterval - elapsed)
	}
	rl.lastReq = time.Now()
}

// Pause 主动暂停所有请求（remaining 过低且 reset 还远时调用）。
// until 是 token reset 时刻。后续所有 Wait() 都会 sleep 到该时间。
func (rl *RateLimitHandler) Pause(until time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.pausedUntil = until
	log.Printf("[ratelimit] pausing until %s", until.Format(time.RFC3339))
}

// Reset 清除暂停状态（手动 admin 重置 / 测试用）。
func (rl *RateLimitHandler) Reset() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.pausedUntil = time.Time{}
}
