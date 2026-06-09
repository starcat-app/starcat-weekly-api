# Changelog

本项目的所有重要变更都会记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Changed (R-01 Transformation)
- **Breaking Change**: 移除旧版 `/api/weekly/*` 接口，全量迁移至 `/api/v1/*`。
- **Breaking Change**: 从 `GITHUB_TOKEN` 迁移至 `GITHUB_TOKENS` (Token Pool)。
- **API 升级**: 所有业务响应现在包入统一的 `{ "schema_version": 1, "data": ... }` envelope。
- **鉴权增强**: 引入 `Bearer Token` 鉴权，需在请求头中携带 `Authorization: Bearer <key>`。
- **字段补齐**: `projects` 表新增 14 个 GitHub 元数据字段（`gh_repo_id`, `forks`, `watchers`, `subscribers`, `owner_avatar`, `homepage`, `license_spdx`, `is_archived`, `is_fork`, `is_private`, `default_branch`, `open_issues`, `pushed_at`, `updated_at`, `created_at`）。
- **配置管理**: 引入 `joho/godotenv` 支持从 `.env` 加载环境变量。
- **存储演进**: 引入 SQLite `migrateV2` (基于 `PRAGMA user_version`) 自动处理表结构升级。
- **限流优化**: 接入 Quota-aware Token Pool 和 `RateLimitHandler` 主动退避。

### Added
- 新增单项目聚合接口 `GET /api/v1/projects/{owner}/{repo}`。
- 新增 `StarcatRepoCardDTO` 统一 DTO 结构。

## [1.0.0] - 2026-06-08

### Added
- 阮一峰周刊（ruanyf/weekly）每小时自动同步（git clone/pull + goldmark 解析）
- 项目列表 API（`GET /api/weekly/projects`，支持分页 / 期号 / 语言 / 排序筛选）
- 期号列表 API（`GET /api/weekly/issues`）
- 期号详情 API（`GET /api/weekly/issues/{id}`）
- 手动同步接口（`POST /internal/sync`）
- 健康检查接口（`GET /healthz`）
- 基于 modernc.org/sqlite 的本地存储（无 CGO，可交叉编译进 scratch 镜像）
- GitHub API 元数据补全（stars / language / description，异步批量）
- robfig/cron/v3 定时调度
- Docker + Fly.io 多阶段部署（256MB 内存）
- GitHub Actions CI 工作流（`go vet` / `gofmt` / 编译 / 单测）
- Dependabot 自动依赖更新
- Issue / PR 模板
- 贡献指南和变更日志
- 内部版本号包 (`internal/version`, 暴露 `version.Version` 常量)

[Unreleased]: https://github.com/dong4j/starcat-weekly-api/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/dong4j/starcat-weekly-api/releases/tag/v1.0.0
