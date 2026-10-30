# 每日热点生成与发布流水线

本目录是安装到受控 `starcat-app/agents-radar` fork 的发布适配器，不是 Weekly 服务部署入口。上游生成器继续负责采集与写作，Node 适配器只把已提交的当日报告发送到现有日报管理 API，并等待发布完成。无需 checkout 未推送的 Weekly 代码，也不新增 npm / Go 依赖。

实现核对基线为上游 `1682c89c9a9c726fcc1139659c52a12872c9baf4`。使用 Node 22、pnpm 9.15.9、上游内置 DeepSeek provider，工作流固定 `DEEPSEEK_MODEL=deepseek-flash`。保留上游 `assertLlmHealthy`，生成失败会阻断提交和发布；`DIGEST_REPO` 留空，不创建 GitHub Issues，不运行上游通知步骤。

## 安装位置

| 本目录文件 | fork 中的位置 |
|---|---|
| `publish.mjs` | `scripts/starcat/publish.mjs` |
| `publish.test.mjs` | `scripts/starcat/publish.test.mjs` |
| `daily-digest.yml` | `.github/workflows/daily-digest.yml`，替换原同名文件 |
| `README.md` | `scripts/starcat/README.md` |

必须替换上游同名 workflow，不能另留一个仍按 cron 调用 Qwen、创建 Issues 的旧生成流程。保留上游许可证和版权声明，以及原有 CI。工作流提供手动触发和 UTC `37 22 * * *` 定时触发，即上海时间每日 06:37；首次安装先仅保留手动入口，真实生成与服务发布验收通过后再启用定时配置。触发时间不等于完成时间；仅本地模板包含 cron 不代表远端已经启用，须核对默认分支中的已推送配置。

需要的 GitHub Secrets：

- `DEEPSEEK_API_KEY`：Starcat 运营侧的模型凭据。
- `DAILY_RADAR_ADMIN_API_KEY`：生产 Weekly 独立管理凭据，不能用公开读取 Key。
- `PRODUCTHUNT_TOKEN`：可选；该来源是否有内容由上游生成器处理。

`GITHUB_TOKEN` 由 GitHub Actions 自动提供，仅授予 `contents: write`。可选仓库变量 `DAILY_RADAR_API_URL` 默认为 `https://starcat-api.fly.dev`，只能是 HTTPS origin。服务端需允许来源仓库 `starcat-app/agents-radar`：聚合配置 `WEEKLY_DAILY_RADAR_SOURCE_REPOSITORIES`，独立服务对应 `DAILY_RADAR_SOURCE_REPOSITORIES`。凭据不得写入命令参数、JSON 产物或日志。

## 一次运行的行为

1. 获取默认分支的完整 Git 历史，确定上海当天日期。检查该日是否有有效 Starcat 生成记录。
2. 如果尚未生成，准备当天目录、调用 `pnpm start`，成功后写生成记录，执行 `pnpm manifest`。
3. 将当日 `digests/`、上游采集游标 `digests/web-state.json`、`manifest.json`、`feed.xml` 提交并推送到受控 fork；推送成功后才调用管理 API，确保来源链接可访问。
4. 从生成记录所在的固定 40 位提交读取标准中英文稿。摘要优先使用同语言 `highlights.json`，缺失时沿用 Go 历史导入器的标题和首段规则。
5. 每批至多 50 份且实际 JSON ≤8 MiB，单份 Markdown ≤1 MiB。POST 持久入队后轮询批次，只有全部发布成功才结束。

来源使用 `source_kind=starcat` 和真实 `source_repository`，保留 `source_path`。标题、正文、封面备用值及来源字段与现有 Go `Candidate` 一致。适配器不上传项目卡片：真实 Markdown 链接由服务端 AST 解析、GitHub 核验并保存快照，日报不会写周刊来源事件。

无封面 CDN 时上传 `card_url/hero_url=null`，使用客户端分类图；不伪造已生成封面。未知或旧类型文件不会自动映射为新日报，输出跳过文件清单。中文与英文分别按文件名标识，翻译内容质量仍取决于上游生成结果。

## 重跑与来源保护

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

测试使用临时 Git 仓库与模拟 Fetch，覆盖固定提交、中英文、高亮回退、来源拒绝、分片、幂等、网络与限流重试、禁止重定向、终态失败和轮询超时，不会调用生产或付费模型。
