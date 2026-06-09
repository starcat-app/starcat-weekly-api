# starcat-weekly-api

Starcat Weekly 后端服务 —— 解析[阮一峰周刊](https://github.com/ruanyf/weekly)推荐的 GitHub 开源项目，通过 REST API 提供给 [Starcat](https://starcat.ink) 前端。

## R-01 改造说明

本项目已完成 R-01 契约升级：
- **字段补齐**：`projects` 表扩充至 14+5 个 GitHub 元数据字段。
- **接口版本化**：所有业务接口迁移至 `/api/v1/*`。
- **响应标准化**：所有响应包入 `{ "schema_version": 1, "data": ... }` envelope。
- **鉴权机制**：引入 `Bearer Token` 鉴权（需 `Authorization` 头）。
- **Token 池**：支持 `GITHUB_TOKENS` 多 token 轮换。

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
go test ./internal/parser/ -v
```
