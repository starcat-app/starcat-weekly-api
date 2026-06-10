# Changelog

本项目的所有重要变更都会记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.5.1] - 2026-06-10

### Changed
- **全新服务语态（dong4j 拍板）**：删除 `migrateV1` / `migrateV2` / `migrateV3` / `PRAGMA user_version` 机制。`store/sqlite.go` 改为单文件 `createSchema(s)` 函数，三张表（`weekly_issues` / `projects` / `zread_trending`）一次性建好。任何现存 `weekly.db` 直接 `rm` 即可，不做 destructive migration。`projects` 表原本 V2 加的 14 个 GitHub 字段直接在 createSchema 里建好（无 ALTER 路径）。

## [0.5.0] - 2026-06-10 (R-02 zread 接入 weekly-api)

### Added
- **新端点** `GET /api/v1/trending/zread?week=this|last|YYYY-MM-DD&limit=20`：
  返回 zread 公开周 trending 列表（无鉴权公开 JSON 端点，决策 ② 独立端点不动现有）
- **新表** `zread_trending`（原 V3 migration,0.5.1 改用 createSchema 一次性建好）：11 zread 字段 + 14 enricher 字段 + 3 v0.4.1
  跨年回溯字段 + 4 索引（决策 ① 独立建表不合并 projects）
- **新 store 方法** `UpsertZreadTrending` / `QueryZreadTrending` / `LookupZreadWikiID`
- **新 cron 任务** 周一 06:00 UTC 拉 zread 周 trending，写库后调 enricher.EnrichAll 补 14 字段
- **新 spider** `internal/spider/zread.go` + `zread_types.go` + `zread_year_infer.go`
  （从 starcat-trending-api 迁入，import 路径改造）
- **年份推断异常告警** `InferYear()` 阈值 `>1`（v0.4.1 dong4j 反馈），
  `log.Printf` 告警含原始 MM/DD，2027 年元旦前后可观察
- **funcName 锁** 防止并发跑同一 cron 任务
- **响应 envelope** `data` 字段新增 `ZreadTrendingEnvelope { week_label, week_start, week_end,
  fetched_at, items }`；envelope `Meta` 通过 data 内嵌的 `week_label` 区分 zread 数据源
  （**不污染** envelope.go 4 份共享件）

### Changed
- **scheduler.go** 新增 `runZreadFetch()` + `SyncZread()` 导出方法
- **.env.example** 新增 `ZREAD_TRENDING_CRON`（注释保留，默认值在 cron.go 硬编码）

### Notes
- **不影响** 现有 `/api/v1/projects` / `/api/v1/issues` / `/api/v1/issues/{number}` 端点
  （决策 ② 严格遵守 — 行为 100% 兼容）
- 客户端 WeeklyView source picker 与 merged 视图将在**独立 PR** 处理（参考
  `docs/详细设计/19-wiki集成.md` §8.5 与 §8.8.3 #40-50）

## [1.0.0] - 2026-06-08 (R-01 v1.2 完成)

### Changed (R-01 Transformation)
- **Breaking Change**: 移除旧版 `/api/weekly/*` 接口，全量迁移至 `/api/v1/*`。
- **Breaking Change**: 从 `GITHUB_TOKEN` 迁移至 `GITHUB_TOKENS` (Token Pool)。
- **API 升级**: 所有业务响应现在包入统一的 `{ "schema_version": 1, "data": ... }` envelope。
- **鉴权增强**: 引入 `Bearer Token` 鉴权，需在请求头中携带 `Authorization: Bearer <key>`。
- **字段补齐**: `projects` 表新增 14 个 GitHub 元数据字段（`gh_repo_id`, `forks`, `watchers`, `subscribers`, `owner_avatar`, `homepage`, `license_spdx`, `is_archived`, `is_fork`, `is_private`, `default_branch`, `open_issues`, `pushed_at`, `updated_at`, `created_at`）。
- **配置管理**: 引入 `joho/godotenv` 支持从 `.env` 加载环境变量。
- **存储演进**: 引入 SQLite `migrateV2` (基于 `PRAGMA user_version`) 自动处理表结构升级（0.5.1 拆除,改为 createSchema 一次性建好）。
- **限流优化**: 接入 Quota-aware Token Pool 和 `RateLimitHandler` 主动退避。

### Added
- 新增单项目聚合接口 `GET /api/v1/projects/{owner}/{repo}`。
- 新增 `StarcatRepoCardDTO` 统一 DTO 结构。

## [1.0.0] - 2026-06-08 (阮一峰周刊首发)

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

[Unreleased]: https://github.com/dong4j/starcat-weekly-api/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/dong4j/starcat-weekly-api/compare/v1.0.0...v0.5.0
[1.0.0]: https://github.com/dong4j/starcat-weekly-api/releases/tag/v1.0.0
