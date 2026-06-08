# Changelog

本项目的所有重要变更都会记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

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
