# starcat-weekly-api

<!-- starcat-promo:start -->
<div align="center">
<a href="https://starcat.ink"><img src="https://raw.githubusercontent.com/dong4j/starcat-pro/main/banner.webp" width="100%" alt="Starcat" /></a>

<p><strong>Self-hostable support API for Starcat weekly project feeds and discovery pipeline.</strong></p>
<p>Starcat is a native macOS app that turns GitHub Stars into a searchable, organized and AI-assisted knowledge base. It supports README rendering, tags, private notes, release tracking, repository health signals, AI summaries, semantic search, browser plugin workflows and self-hostable support APIs.</p>

<a href="https://github.com/dong4j/homebrew-starcat"><img src="https://img.shields.io/badge/Install%20with-Homebrew-FBBF24?style=for-the-badge&logo=homebrew&logoColor=white" width="220" alt="Install with Homebrew"/></a>
<br/>
<sub><a href="./README-ZH.md">中文说明</a></sub>
</div>

<div align="center">
<a href="https://starcat.ink"><img src="https://img.shields.io/badge/website-starcat.ink-38BDF8?style=flat&color=blue" alt="website"/></a>
<a href="https://github.com/dong4j/starcat-pro"><img src="https://img.shields.io/badge/support-starcat--pro-lightgrey.svg?style=flat&color=blue" alt="support"/></a>
<a href="https://github.com/dong4j/homebrew-starcat"><img src="https://img.shields.io/badge/install-homebrew-lightgrey.svg?style=flat&color=blue" alt="homebrew"/></a>
<a href="https://github.com/dong4j/starcat-localization"><img src="https://img.shields.io/badge/localization-open-lightgrey.svg?style=flat&color=blue" alt="localization"/></a>
</div>

<div align="center">
<img width="900" src="https://raw.githubusercontent.com/dong4j/starcat-pro/main/main.webp" alt="Starcat main window"/>
</div>

**Preferred install method:**

```bash
brew tap dong4j/starcat
brew trust dong4j/starcat
brew install --cask starcat
```

**Useful links:**

- Home: https://starcat.ink
- Download: https://starcat.ink/downloads/Starcat-1.1.0-arm64.dmg
- Public support and release notes: https://github.com/dong4j/starcat-pro
- Homebrew tap: https://github.com/dong4j/homebrew-starcat
- Browser plugins: [Chrome](https://github.com/dong4j/starcat-chrome-plugin) / [Safari](https://github.com/dong4j/starcat-safari-plugin)
- Localization: https://github.com/dong4j/starcat-localization

**Starcat ecosystem:**

- [starcat-sharing-api](https://github.com/dong4j/starcat-sharing-api)
- [starcat-trending-api](https://github.com/dong4j/starcat-trending-api)
- [starcat-weekly-api](https://github.com/dong4j/starcat-weekly-api)
- [starcat-wiki-api](https://github.com/dong4j/starcat-wiki-api)
- [starcat-recommend-api](https://github.com/dong4j/starcat-recommend-api)
- [starcat-discovery-api](https://github.com/dong4j/starcat-discovery-api)
- [starcat-license-api](https://github.com/dong4j/starcat-license-api)

> Starcat provides hosted defaults for normal users. This API is open source so advanced users can inspect it, run it locally, or deploy their own instance.
<!-- starcat-promo:end -->

Starcat Weekly 后端服务 —— 聚合[阮一峰周刊](https://github.com/ruanyf/weekly)、
[zread.ai](https://zread.ai)、Show HN、HelloGitHub 与受控人工情报来源中的 GitHub 项目，
通过统一 REST API 提供给 [Starcat](https://starcat.ink) 前端。

## R-01 改造说明

本项目已完成 R-01 契约升级：
- **字段补齐**：`projects` 表扩充至 14+5 个 GitHub 元数据字段。
- **接口版本化**：所有业务接口迁移至 `/api/v1/*`。
- **响应标准化**：所有响应包入 `{ "schema_version": 1, "data": ... }` envelope。
- **鉴权机制**：引入 `Bearer Token` 鉴权（需 `Authorization` 头）。
- **Token 池**：支持 `GITHUB_TOKENS` 多 token 轮换。

## v0.5 R-02（ZRead 来源）

- **统一列表** `GET /api/v1/repos?source=zread`，不再保留独立公开列表端点
- **新表** `zread_trending`（0.5.0 新建,决策 ① 独立建表不合并 projects）
- **新 cron 任务** 周一 06:00 UTC 拉 zread 公开 JSON 端点并写入数据库

## v0.6 AI Discovery（Show HN）

- Show HN 作为固定 `discovery` 来源进入 `GET /api/v1/repos` 与 bulk，不再保留独立公开端点。
- `POST /internal/sync/discovery`：管理员手动触发同步，使用独立 `ADMIN_API_KEYS`。
- 数据源使用 Hacker News 官方 Firebase API，不解析 HTML、不依赖 Algolia。
- Collector 把同一仓库的多次 Show HN 投稿作为通用来源事件入队，GitHub enrich 由统一 Worker 异步处理。
- 当前流水线不调用 LLM；旧 Discovery 专属表只作为 migration 回滚证据，不再双写。

## Weekly 多来源采集

- `weekly / zread / discovery / hellogithub / ai_intelligence` 统一写入通用来源事件，`GET /api/v1/repos/bulk` 使用 schema v2 返回动态来源目录、通用来源条目和置顶顺序。
- Collector 与人工录入只在 SQLite transaction 中写入 batch/items；commit 后唤醒后台 Worker，由 Worker 在事务外调用 GitHub API。
- Worker 启动时扫描一次，收到内存信号立即处理，并每 15 分钟兜底扫描；瞬时失败按 15/30 分钟退避，最多尝试 3 次后剔除。
- HelloGitHub 支持 featured 增量、月刊对账与可恢复的历史 volume 回填；回填 checkpoint 保存在数据库中。
- `POST /internal/imports` 只允许 `manual_import_enabled=true` 的固定来源，首期仅允许 `ai_intelligence`。
- Weekly 支持多个全局置顶项目，管理端以完整 `gh_repo_ids` 列表原子替换顺序。

## 快速开始

### 本地开发

```bash
# 1. 准备配置文件
cp .env.example .env
# 编辑 .env 填充 API_KEYS 和 GITHUB_TOKENS

# 2. 安装依赖
go mod tidy

# 3. 运行
go run ./cmd/server/

# 4. 测试 API（需带 API Key）
API_KEY="your-key-from-env"
curl -H "Authorization: Bearer $API_KEY" http://localhost:5003/api/v1/weekly?page=1&page_size=5
```

### Docker

```bash
docker build -t starcat-weekly-api .
docker run -p 5003:5003 \
  --env-file .env \
  -v $(pwd)/data:/data \
  starcat-weekly-api
```

### 部署到 Fly.io

```bash
# 设置生产环境 Secrets
fly secrets set \
  API_KEYS="sk-starcat-prodKey1,..." \
  ADMIN_API_KEYS="sk-starcat-adminKey1,..." \
  GITHUB_TOKENS="ghp_token1,ghp_token2" \
  LLM_API_KEY="sk-..." \
  STORE_FILE="/data/weekly.db" \
  REPO_DIR="/data/weekly-repo"

fly deploy
```

## 配置 (.env)

| 变量 | 说明 |
|------|------|
| `PORT` | 服务端口（默认 5003） |
| `STORE_FILE` | SQLite 数据库路径 |
| `REPO_DIR` | 周刊 git clone 存放路径 |
| `API_KEYS` | 逗号分隔的 API Key 白名单（用于 Bearer 鉴权） |
| `ADMIN_API_KEYS` | 来源同步、批量录入和置顶管理专用管理员 Key；不得随客户端分发 |
| `GITHUB_TOKENS` | 逗号分隔的 GitHub PAT 池 |
| `DISCOVERY_CRON` | Discovery cron，默认每小时第 17 分 |
| `HELLOGITHUB_CRON` | HelloGitHub featured 增量 cron，默认每天 06:31 UTC |
| `HELLOGITHUB_RECONCILE_CRON` | HelloGitHub 月刊对账 cron，默认每月 29 日 07:29 UTC |
| `HELLOGITHUB_FEATURED_MAX_PAGES` | featured 增量最大分页数，默认 3 |

## API (v1)

所有业务接口均需携带 `Authorization: Bearer <API_KEY>` 请求头。

### 项目列表

```
GET /api/v1/weekly?page=1&page_size=20&issue=latest&lang=Go&sort=stars_desc
```

### 单个项目详情

```
GET /api/v1/weekly/{owner}/{repo}
```

### 期号列表

```
GET /api/v1/issues
```

### 某期详情

```
GET /api/v1/issues/{number}
```

### 手动同步 (Admin)

```
POST /internal/sync/weekly
```

### 多来源管理接口 (Admin)

所有接口使用 `Authorization: Bearer <ADMIN_API_KEY>`：

```text
GET  /internal/sources?manual_import=true
POST /internal/sources/hellogithub/sync
GET  /internal/ingest-batches/{batch_id}
POST /internal/imports
GET  /internal/imports/{batch_id}
GET  /internal/repos/search?q=owner/repo&limit=20
GET  /internal/pins
POST /internal/pins
```

AI 情报批量录入示例；接口先持久化并返回 `202 Accepted`，GitHub enrich 异步执行：

```json
{
  "source_code": "ai_intelligence",
  "idempotency_key": "news-20260716-001",
  "repositories": [
    {
      "owner": "acme",
      "repo": "agent",
      "title": "AI Agent",
      "source_url": "https://example.com/news"
    }
  ]
}
```

HelloGitHub 历史回填请求：

```json
{
  "mode": "backfill",
  "from_volume": 1,
  "to_volume": null,
  "idempotency_key": "hellogithub-history-v1"
}
```

置顶接口接收完整有序列表，空数组表示清空：

```json
{ "gh_repo_ids": [123, 456, 789] }
```

### ZRead 来源

ZRead 已并入统一 Weekly feed，不再提供独立公开列表端点：

```http
GET /api/v1/repos?source=zread&page=1&page_size=30
Authorization: Bearer <API_KEY>
```

需要立即重新抓取 ZRead 时使用管理端同步接口：

```http
POST /internal/sync/zread
Authorization: Bearer <ADMIN_API_KEY>
```

### AI Discovery 列表（v0.6 新增）

```http
GET /api/v1/discovery?category=all&page=1&page_size=30
Authorization: Bearer <API_KEY>
```

`category` 支持 `all / agent / coding / mcp / rag / infra / model / skill`，默认 `all`；
只返回最近 24 小时且分类状态为 `classified` 的仓库。响应 `data` 每项使用 endpoint 专用结构：

```json
{
  "schema_version": 1,
  "data": [
    {
      "repo": {
        "gh_repo_id": 123,
        "full_name": "owner/repo",
        "owner": "owner",
        "repo": "repo",
        "stars": 42,
        "forks": 3,
        "watchers": 42,
        "subscribers": 2,
        "topics": ["ai", "agent"],
        "is_archived": false,
        "is_fork": false,
        "is_private": false,
        "open_issues": 1
      },
      "discovery": {
        "hn_id": 123456,
        "hn_title": "Show HN: ...",
        "hn_url": "https://news.ycombinator.com/item?id=123456",
        "hn_score": 18,
        "hn_comments": 4,
        "hn_published_at": "2026-06-11T08:30:00Z",
        "category": "agent",
        "classify_confidence": 0.91
      }
    }
  ],
  "meta": { "page": 1, "page_size": 30, "total": 1 }
}
```

### AI Discovery 单仓库

```http
GET /api/v1/discovery/{owner}/{repo}
Authorization: Bearer <API_KEY>
```

### AI Discovery 手动同步（Admin）

```http
POST /internal/sync/discovery
Authorization: Bearer <ADMIN_API_KEY>
```

此端点会消耗 GitHub/LLM 配额，因此不接受普通 `API_KEYS`。

### 健康检查 (不鉴权)

```
GET /healthz
```

## 技术栈

- **Go 1.23** — `net/http` 标准库
- **goldmark** — Markdown AST 解析
- **modernc.org/sqlite** — 纯 Go SQLite（无 CGO，可交叉编译进 Docker scratch）
- **robfig/cron/v3** — 定时同步
- **Docker + Fly.io** — 多阶段构建，256MB 内存

## 测试

```bash
# 全部
go test ./...

# 只跑 parser / spider 单测
go test ./internal/parser/ -v
go test ./internal/spider/ -v
```
