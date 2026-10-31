# 每日热点后端

日报复用 Weekly 进程与 SQLite，使用追加 migration v3 的 `daily_radar_*` 表，和仓库来源事件完全隔离。浏览 / 导入日报不会把相关仓库加入周刊。客户端契约基线位于主仓 `docs/3-设计/详细设计/每日热点-API契约.md`，JSON 样本在本仓 `testdata/daily_radar/`。

读取接口为 `/api/v1/daily-radar/catalog`、`dates`、`reports`、`reports/{id}`，沿用 `API_KEYS` Bearer 鉴权；聚合入口携带 `X-SC-Svc: weekly`。管理接口 `POST /internal/daily-radar/imports`、`GET /internal/daily-radar/imports/{id}` 只接受独立 `ADMIN_API_KEYS`。

目录按语言、周期、主题、日期过滤后去重计数；游标绑定完整筛选、limit 与目录版本。目录变化返回 409 `CATALOG_CHANGED`，校验发生在 ETag / 304 之前。目录摘要按发布版本在内存复用，发布后自然失效；正文按修订按需读取，不把全文历史加载进目录缓存。读取返回私有缓存头，所有写入 / 错误响应 `no-store`。

导入先持久化入队再返回 202。单消费者启动恢复上次 processing，30 秒兜底扫描；每项最多三次尝试，暂时失败按 1 / 2 分钟退避，单项核验上限 2 分钟。最后一次核验失败可发布正文及已核验项目并标记 `partial`。明确 404 / 私有项目跳过，不发给客户端；同文稿最多核验 100 个明确仓库，其余标记 partial。正文链接从 Markdown AST 提取，忽略图片、徽章、代码及非仓库导航；Issue / PR / Release 保留原始证据。

同幂等键同请求复用批次，不同请求冲突。同来源同序列不同内容失败；旧序列不覆盖新版本，自生成版本优先于上游历史。文稿、封面、关联卡片和来源组成 SHA-256 修订，短事务原子更新修订、指针及目录版本，网络不占写事务。已发布修订不覆盖或删除，上游清单缺席不触发删除。

`DAILY_RADAR_COVER_HOSTS` 为精确 HTTPS 域名白名单；空值时仅允许 null 图片 URL，客户端使用已内嵌分类图。服务不抓取管理请求提供的图片地址。`DAILY_RADAR_SOURCE_REPOSITORIES` 为自生成文稿的 GitHub `owner/repo` 白名单；上游固定 `duanyytop/agents-radar`，自生成必须提供 `source_repository`。聚合部署使用对应 `WEEKLY_` 前缀，装配时复制到 Options，运行时不依赖裸环境。

## 固定 Git 历史导入

```bash
# 默认只盘点，不访问服务、不执行上游代码或读取未提交文件。
go run ./cmd/daily-radar-import -checkout /path/to/agents-radar -commit FIXED_SHA

# 明确执行时才提交，管理员密钥通过环境变量提供。
go run ./cmd/daily-radar-import -checkout /path/to/agents-radar -commit FIXED_SHA \
  -apply -endpoint https://YOUR_AGGREGATE_HOST -source-sequence 1
```

CLI 用 `git rev-parse` 固定 commit，`git ls-tree` 与 `git show` 读取对象。历史 `generated_at` 取该文件在固定 ref 历史中的最后提交时间，是来源时间的可追溯近似值，不表示已获知精确 AI 生成完成时间；业务日期始终来自目录。标准类型导入，早期 OpenClaw 在同语言无 ai-agents 聚合稿时映射，其他未知 / 子报告记录跳过原因；不以 manifest 数量冒充全历史覆盖。每批最多50份，同时按序列化大小保持请求小于8MiB；同快照、来源、序列及分片产生稳定幂等键。提交超时后可重跑；输出 202 批次 ID，用管理查询观察后续结果。`source_sequence` 必须由生产流水线单调推进，重跑相同内容保持不变；不能把 Git SHA 字符串排序当成版本序列。

日常发布支持两条互斥来源路径，最终都提交同一管理接口：

- `upstream`：CI 固定读取 `duanyytop/agents-radar` 的目标日期 Markdown，不执行上游代码；同主题必须中英文成对，`source_sequence` 使用上游 commit 的 Unix 秒。
- `starcat`：受控 fork 调用 DeepSeek 生成，带 `.starcat-generation.json` 来源记录与独立序列，仅作为接管路径。

上游删除文件、删除仓库或重写历史只影响后续同步，不会删除已入库的不可变修订。服务端展示优先级仍为 `starcat > upstream`，表示“接管”而非最后写入优先。在线请求不运行模型或图像生成。
