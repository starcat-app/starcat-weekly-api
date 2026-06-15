// Package handler 中的 bulk_cache.go 实现 /api/v1/repos/bulk 响应的内存缓存。
//
// R-06.3（2026-06-15）：把 weekly bulk endpoint 从"每次请求都全表 scan + json.Marshal
// 4000+ 条 repos + languages 聚合"升级为内存预拼 payload（同时预压缩 gzip），配合
// ETag / Last-Modified 让客户端走 304 节省 80%+ 带宽。
//
// 设计要点:
//   - 单 entry: bulk endpoint 不分桶（不像 trending 按 since × lang × limit 分），
//     全量数据只有一份 payload。Get/Set/Invalidate 全部针对这一个 entry。
//   - TTL 60s: 远小于 trending 后端的 1h~24h，原因是 bulk 用于客户端 weekly 整张
//     repo 全量拉取（4000 条），客户端 12h TTL 已经做了大压缩；后端缓存仅用来扛
//     "用户主动刷新"这种短时间高频请求场景（60s 内的并发请求合并到一个 build）。
//     与 scheduler 的 weekly_cron `0 0 * * 1` / zread_cron `0 6 * * *` 完全脱钩——
//     scheduler 跑完会主动 Invalidate 不等 TTL。
//   - 预压缩 gzip: build 时一次性算好 gzip payload，命中 + 客户端带
//     `Accept-Encoding: gzip` 直接 `w.Write(gzipped)`，省去每次响应都重新压缩的
//     CPU 开销（4000 条 ~5MB JSON gzip 后 ~800KB，CPU 节省显著）。同时保留原始
//     uncompressed payload 给不支持 gzip 的客户端兜底。
//   - Weak ETag: `W/"<sha256[:8]>"` 16 hex char（HTTP 7232 §2.1 语义等价 validator）。
//   - 锁粒度: `sync.RWMutex` + 单 `*bulkCacheEntry` 指针；read 走 RLock 拿指针，
//     entry 内部字段写入时已经原子完成（Set 是整 entry 一次性替换 pointer），
//     不需要 entry 内部加锁。
//
// 不做的事（保持简单）:
//   - 不持久化磁盘: 服务重启后冷启动，下一次请求自然回填。
//   - 不做 LRU: 只有一个 entry，没有 eviction 需要。
//   - 不接受 query 参数（page / sort / lang）的分支缓存: bulk endpoint 故意只返回
//     全量未过滤数据，客户端拿到后本地做排序 / 过滤 / 分页。
package handler

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// BulkCache 是 /api/v1/repos/bulk 响应的进程内内存缓存。
//
// 所有方法 goroutine-safe。
type BulkCache struct {
	mu    sync.RWMutex
	entry *bulkCacheEntry
}

// bulkCacheEntry 单条 cache entry。
//
// 不要直接修改字段——entry 是 immutable，Set 时整个替换指针。
type bulkCacheEntry struct {
	payload      []byte    // pre-marshaled envelope JSON（uncompressed）
	gzipPayload  []byte    // pre-compressed gzip payload（命中 + Accept-Encoding: gzip 时直接写）
	etag         string    // weak ETag，形如 `W/"abc123..."`
	lastModified time.Time // = builtAt
	builtAt      time.Time // 用来判 TTL
}

// bulkCacheTTL 是 bulk endpoint cache 的 TTL。
//
// 60 秒选择理由（dong4j R-06 讨论拍板）:
//   - bulk 是 weekly 全量 4000+ 条数据，build 一次 ~50ms（SQLite 查询 + marshal +
//     gzip 都串起来）；客户端 12h TTL 控制日常请求频率，后端 cache 仅用来扛"用户
//     在 60s 内重复点刷新 / 多客户端并发"场景；
//   - scheduler 跑完后立即 Invalidate 不等 TTL（保证数据更新可见）；
//   - TTL 太短（如 10s）会让"用户主动连点 3 次刷新"还是触发 3 次 build；TTL 太长
//     （如 5min）会让 scheduler 之外的 admin/RebuildAggregates 路径需要严格依赖
//     主动 Invalidate 才能新鲜，60s 是合理中间值。
const bulkCacheTTL = 60 * time.Second

// NewBulkCache 创建一个空缓存。
func NewBulkCache() *BulkCache {
	return &BulkCache{}
}

// Get 返回 (entry, true) 表示命中且未过期；否则 (nil, false)。
//
// 调用方拿到 entry 后**不能修改**——payload / gzipPayload 是共享 byte slice。
func (c *BulkCache) Get() (*bulkCacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.entry == nil {
		return nil, false
	}
	if time.Since(c.entry.builtAt) > bulkCacheTTL {
		return nil, false
	}
	return c.entry, true
}

// Set 写入 entry。重复调用直接覆盖（典型场景: cache miss / TTL 过期后填）。
//
// payload 是 pre-marshaled envelope JSON；内部会算出 gzip 和 ETag。
//
// 若 gzip 压缩失败（极小概率，gzip.Writer 内部错误），entry.gzipPayload 留 nil，
// handler 端会自动 fallback 到 uncompressed payload。
func (c *BulkCache) Set(payload []byte) *bulkCacheEntry {
	now := time.Now()
	entry := &bulkCacheEntry{
		payload:      payload,
		gzipPayload:  gzipEncode(payload),
		etag:         computeBulkWeakETag(payload),
		lastModified: now,
		builtAt:      now,
	}
	c.mu.Lock()
	c.entry = entry
	c.mu.Unlock()
	return entry
}

// Invalidate 清空 entry，让下次请求强制重建。
//
// 用途: scheduler 完成 sync()/runZreadFetch()/runDiscovery() / handler 执行
// RebuildAggregates 后调 cache.Invalidate()，保证客户端下次请求 100% 拿到新数据
// （不等 60s TTL 自然过期）。
//
// 不抛错——cache miss 也是合法状态。
func (c *BulkCache) Invalidate() {
	c.mu.Lock()
	c.entry = nil
	c.mu.Unlock()
}

// HasEntry 仅供测试 / 监控：当前是否有非空 entry（不判 TTL）。
func (c *BulkCache) HasEntry() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.entry != nil
}

// computeBulkWeakETag 用 SHA256 前 16 字符生成 weak ETag。
//
// W/ 前缀表"语义等价"——客户端不必要求 byte-by-byte 一致（HTTP 7232 §2.1）。
// 16 字符（64 bit）冲突概率足够低，比完整 64 字符短得多。
func computeBulkWeakETag(payload []byte) string {
	sum := sha256.Sum256(payload)
	return `W/"` + hex.EncodeToString(sum[:8]) + `"`
}

// gzipEncode 把 payload 压成 gzip 字节流。
//
// 用 gzip.BestSpeed（level 1）而不是 DefaultCompression（level 6）—— bulk payload
// 是 JSON，level 1 已经能压到 ~16% 原大小（4MB → 650KB），level 6 大约再压 5%
// 但 CPU 开销翻 3 倍；这里是 build 期间一次性付出，不在 hot path，但 build 也是
// 用户请求触发的，level 1 + 16% 大小已经足够省带宽。
//
// 失败概率极低（gzip.Writer 内部 panic 才会失败），返回 nil 让 handler 兜底
// fallback uncompressed。
func gzipEncode(payload []byte) []byte {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil
	}
	if _, err := gw.Write(payload); err != nil {
		_ = gw.Close()
		return nil
	}
	if err := gw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// 关于 scheduler / handler 解耦的注:
//
// scheduler 包不直接 import handler 包。把 `*BulkCache` 作为
// `BulkCacheInvalidator` 接口（在 scheduler 包内定义）注入到 scheduler.New(...)
// 即可：`*BulkCache` 自动满足该接口。两个包仍只依赖 store / model。详见
// scheduler/cron.go 的 BulkCacheInvalidator 接口定义。
