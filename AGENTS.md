# AGENTS.md — starcat-weekly-api

## 项目概述

Go 后端服务，解析阮一峰周刊（ruanyf/weekly）推荐的 GitHub 开源项目，
通过 REST API 提供给 Starcat 前端。

## 技术栈

- Go 1.25.0, 纯标准库 HTTP (net/http)
- goldmark 解析 Markdown AST
- modernc.org/sqlite (纯 Go SQLite, 无 CGO)
- robfig/cron/v3 定时同步
- joho/godotenv 加载 .env
- Docker + Fly.io 部署

## R-01 架构约束

- **统一契约**: 响应必须包入 `internal/model/envelope.go` 定义的结构。
- **鉴权**: 所有 `/api/v1/*` 接口必须带 `Authorization: Bearer <key>`。
- **Token 池**: 调用 GitHub API 必须经由 `internal/tokenpool/tokenpool.go`。
- **硬边界**: 扩充字段必须区分核心 `StarcatRepoCardDTO` 与 `weekly` 扩展段。

## 项目结构

```
cmd/server/main.go          # 入口，装配中间件与 TokenPool
internal/
  model/
    project.go               # DB 模型
    repo_card.go             # 统一 DTO (StarcatRepoCardDTO)
    envelope.go              # 顶层响应信封
  middleware/
    auth.go                  # Bearer 鉴权中间件
  tokenpool/
    tokenpool.go             # GitHub PAT 池
  store/
    sqlite.go                # 含 migrateV2 逻辑
  enricher/
    github.go                # 扩充至 14+5 字段补全
    ratelimit.go             # 主动退避限流
  handler/
    weekly.go                # v1 版 REST Handler
    handler.go               # JSON/Error helper
  ...
```

## 开发

```bash
# 1. 准备 .env
cp .env.example .env

# 2. 安装依赖并运行
go mod tidy
go run ./cmd/server/
```

## Commit 规范

- `feat:` 新功能
- `fix:` 修复
- `test:` 测试
- `chore:` 构建/配置
- 每个 commit 末尾 `Closes HOM-176`
