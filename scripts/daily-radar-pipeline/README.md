# 每日热点双来源生成与发布流水线

本目录是安装到受控 `starcat-app/agents-radar` fork 的发布适配器，不是 Weekly 服务部署入口。流水线有两条互斥来源路径：

- **`upstream`（默认）**：读取 `duanyytop/agents-radar` 固定 Git commit 中已经生成好的中英文 Markdown，不运行上游代码、不调用模型，只通过现有日报管理 API 入库。
- **`starcat`（接管）**：保留现有 DeepSeek 自生成路径。仅在上游失败、结构或语言质量不满足 Starcat 需求、或产品需要接管内容时手动/策略切换。

两条路径最终都发送到同一个 `/internal/daily-radar/imports` 管理 API，继续复用持久队列、幂等批次、GitHub 项目核验、不可变修订和目录缓存；不因为是上游还是自生成而另建发布通道。

实现核对基线为上游 `1682c89c9a9c726fcc1139659c52a12872c9baf4`。使用 Node 22、pnpm 9.15.9、上游内置 DeepSeek provider，工作流固定 `DEEPSEEK_MODEL=deepseek-flash`。保留上游 `assertLlmHealthy`，生成失败会阻断提交和发布；`DIGEST_REPO` 留空，不创建 GitHub Issues，不运行上游通知步骤。

## 安装位置

| 本目录文件 | fork 中的位置 |
|---|---|
| `publish.mjs` | `scripts/starcat/publish.mjs` |
| `publish.test.mjs` | `scripts/starcat/publish.test.mjs` |
| `daily-digest.yml` | `.github/workflows/daily-digest.yml`，替换原同名文件 |
| `README.md` | `scripts/starcat/README.md` |

必须替换上游同名 workflow，不能另留一个仍按 cron 调用 Qwen、创建 Issues 的旧生成流程。保留上游许可证和版权声明，以及原有 CI。

来源模式由仓库变量 `DAILY_RADAR_SOURCE_MODE` 控制，取值只能是 `upstream` 或 `starcat`；缺省为 `upstream`。手动 dispatch 可用 `source_mode` 覆盖仓库变量，便于临时接管或回放。定时触发使用 UTC `30 23 * * *`（上海时间每日 07:30），晚于上游 06:37 的生成任务。触发时间不等于完成时间；仅本地模板包含 cron 不代表远端已经启用，须核对默认分支中的已推送配置。

需要的 GitHub Secrets：

- `DEEPSEEK_API_KEY`：Starcat 运营侧的模型凭据。
- `DAILY_RADAR_ADMIN_API_KEY`：生产 Weekly 独立管理凭据，不能用公开读取 Key。
- `PRODUCTHUNT_TOKEN`：可选；该来源是否有内容由上游生成器处理。

`GITHUB_TOKEN` 由 GitHub Actions 自动提供，仅授予 `contents: write`。可选仓库变量 `DAILY_RADAR_API_URL` 默认为 `https://starcat-api.fly.dev`，只能是 HTTPS origin。服务端需允许来源仓库 `starcat-app/agents-radar`：聚合配置 `WEEKLY_DAILY_RADAR_SOURCE_REPOSITORIES`，独立服务对应 `DAILY_RADAR_SOURCE_REPOSITORIES`。凭据不得写入命令参数、JSON 产物或日志。

## 一次运行的行为

### upstream 路径

1. 按调度模式路由到 `upstream`，在受控 fork 中读取发布适配器。
2. `git fetch --depth=1 https://github.com/duanyytop/agents-radar.git master`，固定 `FETCH_HEAD` 的 40 位 SHA。
3. 只用 `git ls-tree` / `git show` 读取该 commit 下目标日期的标准 Markdown 与 `highlights.json`；不 checkout 上游工作区，不安装上游依赖，不执行上游代码。
4. 每个出现的主题必须同时存在 `zh-Hans` 与 `en` 标准稿，否则该次同步失败。某主题当天整体缺席可以接受。
5. `source_kind=upstream`、`source_repository=duanyytop/agents-radar`，`source_sequence` 使用上游 commit 的 Unix 秒级提交时间；后续上游修订同主题时用更高序列发布新的不可变修订。
6. 每批至多 50 份且实际 JSON ≤8 MiB，单份 Markdown ≤1 MiB。POST 持久入队后轮询批次，只有全部发布成功才结束。

### starcat 路径

1. 获取默认分支的完整 Git 历史，确定目标上海日期。检查该日是否有有效 Starcat 生成记录。
2. 如果尚未生成，准备当天目录、调用 `pnpm start`，成功后写生成记录，执行 `pnpm manifest`。
3. 将当日 `digests/`、上游采集游标 `digests/web-state.json`、`manifest.json`、`feed.xml` 提交并推送到受控 fork；推送成功后才调用管理 API，确保来源链接可访问。
4. 从生成记录所在的固定 40 位提交读取标准中英文稿。摘要优先使用同语言 `highlights.json`，缺失时沿用 Go 历史导入器的标题和首段规则。
5. 每批至多 50 份且实际 JSON ≤8 MiB，单份 Markdown ≤1 MiB。POST 持久入队后轮询批次，只有全部发布成功才结束。

自生成使用 `source_kind=starcat` 和真实 `source_repository`；上游同步使用 `source_kind=upstream` 与固定 `duanyytop/agents-radar`。两条路径均保留 `source_commit/source_path`。适配器不上传项目卡片：真实 Markdown 链接由服务端 AST 解析、GitHub 核验并保存快照，日报不会写周刊来源事件。

无封面 CDN 时上传 `card_url/hero_url=null`，使用客户端分类图；不伪造已生成封面。未知或旧类型文件不会自动映射为新日报，输出跳过文件清单。中文与英文分别按文件名标识，翻译内容质量仍取决于上游生成结果。

## 重跑与来源保护

上游同步不修改 fork 的默认分支，也不 merge 上游代码；重复运行同一上游 commit 时，序列和完整报告摘要生成同一幂等身份。上游删除文件、删除仓库或重写历史只影响后续同步，不会删除 Starcat 已入库的不可变修订。

服务端现有展示优先级是 `starcat` 高于 `upstream`。因此同一天先用自生成接管后，再同步上游不会把公开指针切回上游；如果当天已接管，应从次日起切回 `upstream`。反之，当天上游已发布后切到 `starcat`，则自生成版本会按接管语义成为该日 canonical 版本。

`prepare` 只处理当前上海日期，清除该日期目录中白名单标准稿与 `highlights.json`，随后记录本次生成起点；其它日期、未知文件及 Git 历史不受影响。这可以避免 fork 继承的当日上游稿混入新产物。`seal` 只能在生成起点与当前身份一致时写入 `.starcat-generation.json`，记录本次实际生成文件的 SHA-256、生成时间、来源仓库和序列。

序列为 `GITHUB_RUN_NUMBER * 1000 + GITHUB_RUN_ATTEMPT`，适用于本固定 workflow 的串行生成；不要更名并重置该 workflow 的运行编号。已生成的日期重跑会复用原序列与生成提交，不重新调用模型。幂等键取来源提交和完整批次摘要，因此 POST 超时、网络重试或后续无关 Git 提交都不会产生新的导入身份。

请求不跟随重定向。网络故障、429 和 5xx 最多尝试五次，`Retry-After` 单次最多等待 30 秒；401/403/409 等立即失败。单批最多等待 60 分钟，workflow 总上限 120 分钟。后台队列仍在处理而超时，可以再次运行同一日任务续查；如果日期已经变化，使用下面的指定日期发布命令重试旧日，不重新生成。`partial` 或 `failed` 批次始终使工作流失败，已有成功内容仍保留。

服务端已经判定终态失败的批次不会因重复幂等请求自动重新处理。遇到此类结果先检查错误原因，不要盲目改序列反复入队；需要修正文稿时，应建立新的受控生成记录和来源序列后发布。

## 本地校验与手动续查

在 fork 中运行离线测试：

```bash
node --check scripts/starcat/publish.mjs
node --test scripts/starcat/publish.test.mjs
```

默认命令只盘点，不发网络请求；`--commit` 必须是明确的 40 位 SHA，文稿只从 Git 对象读取，未提交文件不会被发布：

```bash
node scripts/starcat/publish.mjs publish \
  --checkout . \
  --commit <40-character-commit-sha> \
  --date 2026-10-30 \
  --repository starcat-app/agents-radar
```

确认需要发布时，在环境中安全注入 `DAILY_RADAR_ADMIN_API_KEY`，在同一命令追加 `--apply`。该命令可以续查已经成功生成并提交的旧日稿；`prepare/seal` 仅允许当前上海日期，不能给上游历史稿补一个自生成身份。

上游同步的本地校验与发布：

```bash
node scripts/starcat/publish.mjs upstream \
  --checkout /path/to/agents-radar \
  --commit <40-character-upstream-commit-sha> \
  --date 2026-11-01 \
  --sequence <upstream-commit-unix-seconds>
```

追加 `--apply` 才会调用管理 API。`--sequence` 必须来自上游 commit 时间且不可手工改小，否则同来源修订排序会被破坏。

测试使用临时 Git 仓库与模拟 Fetch，覆盖固定提交、中英文、高亮回退、来源拒绝、分片、幂等、网络与限流重试、禁止重定向、终态失败和轮询超时，不会调用生产或付费模型。
