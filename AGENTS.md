# AGENTS.md — starcat-weekly-api

## 项目概述

Go 后端服务，解析阮一峰周刊（ruanyf/weekly）推荐的 GitHub 开源项目，
通过 REST API 提供给 Starcat 前端。

## 技术栈

- Go 1.23.5, 纯标准库 HTTP (net/http)
- goldmark 解析 Markdown AST
- modernc.org/sqlite (纯 Go SQLite, 无 CGO)
- robfig/cron/v3 定时同步
- Docker + Fly.io 部署

## 项目结构

```
cmd/server/main.go          # 入口
internal/
  model/project.go           # 数据模型
  store/store.go             # 存储接口
  store/sqlite.go            # SQLite 实现
  fetcher/git.go             # git clone/pull
  parser/markdown.go         # MD 解析
  enricher/github.go         # GitHub API 补全元数据
  handler/weekly.go          # HTTP handlers
  scheduler/cron.go          # 定时任务
docs/issue-399.md            # 测试样本
```

## 开发

```bash
go mod tidy
go build ./cmd/server/
go test ./internal/parser/
```

## Commit 规范

- `feat:` 新功能
- `fix:` 修复
- `test:` 测试
- `chore:` 构建/配置
- 每个 commit 末尾 `Closes HOM-176`
