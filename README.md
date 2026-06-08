# starcat-weekly-api

Starcat Weekly 后端服务 —— 解析[阮一峰周刊](https://github.com/ruanyf/weekly)推荐的 GitHub 开源项目，通过 REST API 提供给 [Starcat](https://starcat.ink) 前端。

## 架构

```
ruanyf/weekly (GitHub)
    │  git clone/pull (每小时 cron)
    ▼
[fetcher] ──► [parser: goldmark] ──► [store: SQLite]
                                       ▲
                                       │ 异步补全 stars/lang/desc
                                [enricher: GitHub API]
                                       │
                                       ▼
                               [HTTP API] ──► Starcat 前端
```

## 快速开始

### 本地开发

```bash
# 设置 GitHub Token（可选，用于 API 补全元数据）
export GITHUB_TOKEN=ghp_xxx

# 安装依赖
go mod tidy

# 运行（自动 clone ruanyf/weekly 并启动服务）
go run ./cmd/server/

# 测试 API
curl http://localhost:5003/api/weekly/projects?page=1&page_size=5
```

### Docker

```bash
docker build -t starcat-weekly-api .
docker run -p 5003:5003 \
  -e GITHUB_TOKEN=ghp_xxx \
  -v $(pwd)/data:/data \
  -e STORE_FILE=/data/weekly.db \
  starcat-weekly-api
```

### 部署到 Fly.io

```bash
fly launch                    # 首次创建应用
fly secrets set GITHUB_TOKEN=ghp_xxx
fly volumes create starcat_weekly_data --size 1
fly deploy
```

### 发版流程 (推荐: scripts/deploy.sh)

日常发版**不要**直接 `fly deploy` 或 push 到 main。统一走 `scripts/deploy.sh` 脚本:

```bash
# 在 dev 分支上, 确认所有改动已 commit + push
./scripts/deploy.sh v1.1.0
```

脚本会自动完成:
1. 校验 (semver 格式 / 分支 / 工作区 / tag 唯一性 / gh 认证)
2. 推送当前 dev 分支到 origin
3. 创建 PR `dev → main` (按 PULL_REQUEST_TEMPLATE)
4. 合并 PR (`--merge`, 保留 dev 历史, **不删 dev 分支**)
5. 切 main, pull
6. 打 annotated tag `v1.1.0` (指向 merge commit)
7. 推送 tag → 自动触发 `.github/workflows/fly-deploy.yml` 完成 Fly.io 部署

**关键约束**:
- tag 必须在 PR merge 之后打, 这样 tag 指向 main 的 merge commit
- `fly-deploy.yml` 已改为 tag-only 触发 (`tags: ['v*']`), 避免普通 main push 误部署
- `go.yml` 也会在 tag 推送后跑一次 CI, 验证 release 包可编译
- 脚本不可在 `main` / `master` 分支运行

首次部署 / 应急:仍可用 `fly deploy` 手动部署, 不影响 tag 触发的正式流程。

## API

所有响应均为 JSON 格式，无额外包装层。

### 项目列表

```
GET /api/weekly/projects?page=1&page_size=20
```

参数：

| 参数 | 说明 |
|------|------|
| `page` | 页码（默认 1） |
| `page_size` | 每页数量（默认 20，最大 100） |
| `issue` | 筛选期号：`latest` 或具体数字如 `399` |
| `issue_from` | 起始期号 |
| `issue_to` | 截止期号 |
| `lang` | 筛选编程语言 |
| `sort` | 排序：`stars_desc` / `first_issue_desc`（默认） |

响应示例：

```json
{
  "items": [
    {
      "id": 1,
      "owner": "sky22333",
      "repo": "skyadb",
      "url": "https://github.com/sky22333/skyadb",
      "description": "运行在安卓手机上的 ADB 管理工具",
      "stars": 123,
      "language": "Java",
      "first_issue": 399,
      "issue_url": "https://github.com/ruanyf/weekly/blob/master/docs/issue-399.md"
    }
  ],
  "total": 100,
  "page": 1,
  "page_size": 20
}
```

### 期号列表

```
GET /api/weekly/issues
```

### 某期详情

```
GET /api/weekly/issues/399
```

### 手动同步

```
POST /internal/sync
```

### 健康检查

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
go test ./internal/parser/ -v
```
