# starcat-weekly-api

<!-- starcat-promo:start -->
<div align="center">
<a href="https://starcat.ink"><img src="https://raw.githubusercontent.com/dong4j/starcat-pro/main/banner.webp" width="100%" alt="Starcat" /></a>

<p><strong>这是 Starcat 周刊项目源与发现流水线的可自部署支撑服务。</strong></p>
<p>Starcat 是一款原生 macOS 应用，可以把 GitHub Stars 变成可搜索、可整理、可用 AI 理解的知识库。它支持 README 渲染、标签与私有笔记、Release 追踪、仓库健康度、AI 摘要、语义搜索、浏览器插件工作流，并提供多个可自部署 API。</p>

<a href="https://github.com/dong4j/homebrew-starcat"><img src="https://img.shields.io/badge/Install%20with-Homebrew-FBBF24?style=for-the-badge&logo=homebrew&logoColor=white" width="220" alt="Install with Homebrew"/></a>
<br/>
<sub><a href="./README.md">English</a></sub>
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

**首选 Homebrew 安装：**

```bash
brew tap dong4j/starcat
brew trust dong4j/starcat
brew install --cask starcat
```

**相关链接：**

- 官网: https://starcat.ink
- 下载: https://starcat.ink/downloads/Starcat-1.1.0-arm64.dmg
- 公开支持与发布说明: https://github.com/dong4j/starcat-pro
- Homebrew tap: https://github.com/dong4j/homebrew-starcat
- 浏览器插件: [Chrome](https://github.com/dong4j/starcat-chrome-plugin) / [Safari](https://github.com/dong4j/starcat-safari-plugin)
- 本地化: https://github.com/dong4j/starcat-localization

**Starcat 生态项目：**

- [starcat-sharing-api](https://github.com/dong4j/starcat-sharing-api)
- [starcat-trending-api](https://github.com/dong4j/starcat-trending-api)
- [starcat-weekly-api](https://github.com/dong4j/starcat-weekly-api)
- [starcat-wiki-api](https://github.com/dong4j/starcat-wiki-api)
- [starcat-recommend-api](https://github.com/dong4j/starcat-recommend-api)
- [starcat-discovery-api](https://github.com/dong4j/starcat-discovery-api)
- [starcat-license-api](https://github.com/dong4j/starcat-license-api)

> Starcat 为普通用户提供默认托管服务。这个 API 开源出来，是为了让进阶用户可以审查实现、本地运行，或部署自己的实例。
<!-- starcat-promo:end -->

Starcat Weekly 后端服务 —— 解析[阮一峰周刊](https://github.com/ruanyf/weekly)推荐的 GitHub 开源项目，
并接入 [zread.ai](https://zread.ai) 公开周 trending 列表（v0.5 R-02 翻转加入），
以及 Hacker News 官方 API 的 Show HN AI 项目发现流水线（v0.6），
通过 REST API 提供给 [Starcat](https://starcat.ink) 前端。

## R-01 改造说明

本项目已完成 R-01 契约升级：
- **字段补齐**：`projects` 表扩充至 14+5 个 GitHub 元数据字段。
- **接口版本化**：所有业务接口迁移至 `/api/v1/*`。
- **响应标准化**：所有响应包入 `{ "schema_version": 1, "data": ... }` envelope。
- **鉴权机制**：引入 `Bearer Token` 鉴权（需 `Authorization` 头）。
- **Token 池**：支持 `GITHUB_TOKENS` 多 token 轮换。

## v0.5 R-02 新增（zread 周 trending 接入）

- **新端点** `GET /api/v1/zread?week=this|last|YYYY-MM-DD&limit=20`
  详见 [API 文档](#zread-周-trending-v05-新增) 章节
- **新表** `zread_trending`（0.5.0 新建,决策 ① 独立建表不合并 projects）
- **新 cron 任务** 周一 06:00 UTC 拉 zread 公开 JSON 端点并写入数据库

## v0.6 AI Discovery（Show HN）

- `GET /api/v1/discovery`：24 小时内已完成 AI 分类的仓库列表。
- `GET /api/v1/discovery/{owner}/{repo}`：单仓库 Discovery 详情。
- `POST /internal/sync/discovery`：管理员手动触发同步，使用独立 `ADMIN_API_KEYS`。
- 数据源使用 Hacker News 官方 Firebase API，不解析 HTML、不依赖 Algolia。
- `discovery_repos` 与 `discovery_submissions` 分表，保留同一仓库的多次 Show HN 投稿。
- LLM 未配置时 collect/enrich 继续工作，分类队列保持 pending，配置后自动消费。

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

### zread 周 trending（v0.5 新增）

```
GET /api/v1/zread?week=this|last|YYYY-MM-DD&limit=20
```

参数：
- `week`：可选，默认 `this`；取值 `this` / `last` / 任意历史周开始日期（ISO 8601 格式如 `2026-05-25`）
- `limit`：可选，默认 20，上限 50

响应示例（envelope 共享，`data.week_label` 标识 zread 数据源）：

```json
{
  "schema_version": 1,
  "data": {
    "week_label": "This Week",
    "week_start": "2026-06-08",
    "week_end": "2026-06-14",
    "fetched_at": "2026-06-10T07:00:00Z",
    "items": [
      {
        "rank": 1,
        "owner": "Panniantong",
        "name": "Agent-Reach",
        "html_url": "https://github.com/Panniantong/Agent-Reach",
        "description": "Give your AI agent eyes to see the entire internet.",
        "description_zh": "赋予 AI 代理互联网视野，零 API 费用。",
        "star_count": 17708,
        "language": "python",
        "wiki_id": "a2570fde-...",
        "gh_repo_id": 871234567
      }
    ]
  },
  "meta": { "total": 19, "generated_at": "2026-06-10T07:00:00Z", "cache_status": "fresh" }
}
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
