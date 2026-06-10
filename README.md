# starcat-weekly-api

Starcat Weekly 后端服务 —— 解析[阮一峰周刊](https://github.com/ruanyf/weekly)推荐的 GitHub 开源项目，
并接入 [zread.ai](https://zread.ai) 公开周 trending 列表（v0.5 R-02 翻转加入），
通过 REST API 提供给 [Starcat](https://starcat.ink) 前端。

## R-01 改造说明

本项目已完成 R-01 契约升级：
- **字段补齐**：`projects` 表扩充至 14+5 个 GitHub 元数据字段。
- **接口版本化**：所有业务接口迁移至 `/api/v1/*`。
- **响应标准化**：所有响应包入 `{ "schema_version": 1, "data": ... }` envelope。
- **鉴权机制**：引入 `Bearer Token` 鉴权（需 `Authorization` 头）。
- **Token 池**：支持 `GITHUB_TOKENS` 多 token 轮换。

## v0.5 R-02 新增（zread 周 trending 接入）

- **新端点** `GET /api/v1/trending/zread?week=this|last|YYYY-MM-DD&limit=20`
  详见 [API 文档](#zread-周-trending-v05-新增) 章节
- **新表** `zread_trending`（0.5.0 新建,决策 ① 独立建表不合并 projects）
- **新 cron 任务** 周一 06:00 UTC 拉 zread 公开 JSON 端点并写入数据库

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
curl -H "Authorization: Bearer $API_KEY" http://localhost:5003/api/v1/projects?page=1&page_size=5
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
  GITHUB_TOKENS="ghp_token1,ghp_token2" \
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
| `GITHUB_TOKENS` | 逗号分隔的 GitHub PAT 池 |

## API (v1)

所有业务接口均需携带 `Authorization: Bearer <API_KEY>` 请求头。

### 项目列表

```
GET /api/v1/projects?page=1&page_size=20&issue=latest&lang=Go&sort=stars_desc
```

### 单个项目详情

```
GET /api/v1/projects/{owner}/{repo}
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
POST /internal/sync
```

### zread 周 trending（v0.5 新增）

```
GET /api/v1/trending/zread?week=this|last|YYYY-MM-DD&limit=20
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

完整字段定义见 `docs/详细设计/19-wiki集成.md` §8.3 / §8.4。

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
