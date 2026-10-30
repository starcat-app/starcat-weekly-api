#!/usr/bin/env node
/**
 * 将受控 agents-radar 仓库当天已提交的文稿发布到 Starcat 管理 API。
 * 只使用 Node 22 标准库；生成凭据不进入文稿，管理凭据不进入日志。
 * 生成记录固定文件摘要、来源提交和来源序列，让重跑只重试发布而不重复生成。
 */
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, unlinkSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";

const CATEGORIES = new Set([
  "ai-cli", "ai-agents", "ai-infra", "ai-web", "ai-trending", "ai-hn",
  "ai-ph", "ai-arxiv", "ai-hf", "ai-community", "ai-weekly", "ai-monthly",
]);
const MARKER = ".starcat-generation.json";
const START = ".starcat-generation-start.json";
const SHA = /^[a-f0-9]{40}$/;
const REPOSITORY = /^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/;
const MAX_MARKDOWN_BYTES = 1 << 20;
const MAX_REQUEST_BYTES = 8 << 20;
const hash = (value) => createHash("sha256").update(value).digest("hex");
const bytes = (value) => Buffer.byteLength(typeof value === "string" ? value : JSON.stringify(value));
const sleep = (ms) => new Promise((resolveSleep) => setTimeout(resolveSleep, ms));

/** 上海业务日期与上游生成器保持一致，不依赖 runner 的 UTC 默认时区。 */
export function shanghaiDate(now = new Date()) {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit",
  }).format(now);
}

function validateDate(date) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date ?? "") ||
      !Number.isFinite(Date.parse(date)) || new Date(date).toISOString().slice(0, 10) !== date) {
    throw new Error("edition date must be YYYY-MM-DD");
  }
}

function validateRepository(repository) {
  if (!REPOSITORY.test(repository ?? "") || [".", ".."].includes(repository.split("/")[1])) {
    throw new Error("source repository must be owner/repo");
  }
}

/** 不通过 shell 拼接 Git 参数，错误也不转发可能含敏感配置的 stderr。 */
function git(checkout, ...args) {
  try {
    return execFileSync("git", ["-C", checkout, ...args], {
      encoding: "utf8", maxBuffer: 16 << 20, stdio: ["ignore", "pipe", "pipe"],
    });
  } catch {
    throw new Error("cannot read the requested fixed Git object");
  }
}

function fixedCommit(checkout, commit) {
  if (!SHA.test(commit ?? "") ||
      git(checkout, "rev-parse", "--verify", `${commit}^{commit}`).trim() !== commit) {
    throw new Error("a full 40-character commit SHA is required");
  }
  return commit;
}

function reportType(filename) {
  if (!filename.endsWith(".md")) return null;
  let category = filename.slice(0, -3);
  const english = category.endsWith("-en");
  if (english) category = category.slice(0, -3);
  return CATEGORIES.has(category) ? { category, language: english ? "en" : "zh-Hans" } : null;
}

function json(text, message) {
  try { return JSON.parse(text); } catch { throw new Error(message); }
}

/** 沿用 Go 历史导入器的标题/首段规则；highlights 只提供摘要，不制造项目关联。 */
export function describeReport(markdown, highlights, category, language) {
  if (!markdown.trim() || bytes(markdown) > MAX_MARKDOWN_BYTES) {
    throw new Error("markdown must contain 1 byte to 1 MiB");
  }
  let title = "";
  let summary = "";
  for (const raw of markdown.split("\n")) {
    const line = raw.trim();
    if (!title && line.startsWith("# ")) {
      title = line.slice(2).trim();
      continue;
    }
    if (!summary && Array.from(line).length > 15 && !/^[#|<]/.test(line)) {
      summary = Array.from(line).slice(0, 240).join("");
    }
  }
  if (!title || bytes(title) > 1000) throw new Error("report title is missing or too long");
  const body = markdown.replace(/^# .*$/m, "").trim();
  if (!body || /^(?:⚠️\s*)?(?:摘要生成失败|Summary generation failed|趋势报告生成失败|Trending report generation failed)[。.!]?$/.test(body)) {
    throw new Error("report has no publishable body");
  }
  const selected = highlights?.[language === "en" ? "en" : "zh"]?.[category];
  if (Array.isArray(selected)) {
    const text = selected.filter((item) => typeof item === "string").map((item) => item.trim()).filter(Boolean).join(" · ");
    if (text) summary = Array.from(text).slice(0, 240).join("");
  }
  return { title, summary };
}

/** 准备明确的当天目录，移除继承的标准稿；历史和未知文件不碰，旧内容仍在 Git 中。 */
export function prepareGeneration({ checkout, date, repository, sequence, now = new Date() }) {
  validateDate(date);
  validateRepository(repository);
  if (date !== shanghaiDate(now)) throw new Error("generation is limited to the current Shanghai date");
  if (!Number.isSafeInteger(sequence) || sequence < 1) throw new Error("source sequence must be a positive safe integer");
  const directory = join(resolve(checkout), "digests", date);
  if (existsSync(join(directory, MARKER))) throw new Error("generation already recorded; retry publication instead");
  mkdirSync(directory, { recursive: true });
  for (const filename of readdirSync(directory)) {
    if (reportType(filename) || filename === "highlights.json") unlinkSync(join(directory, filename));
  }
  const record = {
    version: 1, source_repository: repository, edition_date: date, source_sequence: sequence,
    baseline_commit: git(checkout, "rev-parse", "HEAD").trim(), started_at: now.toISOString(),
  };
  writeFileSync(join(directory, START), `${JSON.stringify(record, null, 2)}\n`);
}

/** 仅在 pnpm start 成功后执行；摘要绑定这次确实生成的文件，不能给历史补来源标记。 */
export function sealGeneration({ checkout, date, repository, now = new Date() }) {
  validateDate(date);
  validateRepository(repository);
  const directory = join(resolve(checkout), "digests", date);
  const start = json(readFileSync(join(directory, START), "utf8"), "invalid generation start record");
  if (start.source_repository !== repository || start.edition_date !== date ||
      start.baseline_commit !== git(checkout, "rev-parse", "HEAD").trim() ||
      date !== shanghaiDate(now)) throw new Error("generation identity or date changed");
  const highlightsPath = join(directory, "highlights.json");
  const highlightsText = existsSync(highlightsPath) ? readFileSync(highlightsPath, "utf8") : null;
  const highlights = highlightsText ? json(highlightsText, "invalid highlights JSON") : {};
  const files = {};
  for (const filename of readdirSync(directory).sort()) {
    const type = reportType(filename);
    if (!type) continue;
    const markdown = readFileSync(join(directory, filename), "utf8");
    describeReport(markdown, highlights, type.category, type.language);
    files[filename] = hash(markdown);
  }
  if (Object.keys(files).length === 0) throw new Error("no generated reports to publish");
  const marker = {
    version: 1, source_repository: repository, edition_date: date,
    source_sequence: start.source_sequence, generated_at: now.toISOString(),
    files, highlights_sha256: highlightsText === null ? null : hash(highlightsText),
  };
  writeFileSync(join(directory, MARKER), `${JSON.stringify(marker, null, 2)}\n`);
  unlinkSync(join(directory, START));
  return marker;
}

/**
 * 从固定 Git 树读取生成记录和文稿，工作区修改完全不影响发布。
 * 后续无关提交不会改变来源 commit/sequence，发布重跑仍得到同一幂等请求。
 */
export function loadEdition({ checkout, commit, date, repository }) {
  validateDate(date);
  validateRepository(repository);
  fixedCommit(checkout, commit);
  const prefix = `digests/${date}/`;
  const markerPath = `${prefix}${MARKER}`;
  const tree = git(checkout, "ls-tree", "-r", "--name-only", commit, "--", prefix).trim().split("\n");
  if (!tree.includes(markerPath)) return null;
  const publicationCommit = git(checkout, "log", "-1", "--format=%H", commit, "--", markerPath).trim();
  fixedCommit(checkout, publicationCommit);
  const marker = json(git(checkout, "show", `${publicationCommit}:${markerPath}`), "invalid generation record");
  if (marker.version !== 1 || marker.source_repository !== repository || marker.edition_date !== date ||
      !Number.isSafeInteger(marker.source_sequence) || marker.source_sequence < 1 ||
      !Number.isFinite(Date.parse(marker.generated_at)) || !marker.files ||
      typeof marker.files !== "object" || Array.isArray(marker.files)) {
    throw new Error("generation record does not match the requested identity");
  }
  let highlights = {};
  if (marker.highlights_sha256 !== null) {
    const text = git(checkout, "show", `${publicationCommit}:${prefix}highlights.json`);
    if (hash(text) !== marker.highlights_sha256) throw new Error("highlights differ from the generation record");
    highlights = json(text, "invalid highlights JSON");
  }
  const reports = [];
  for (const filename of Object.keys(marker.files).sort()) {
    const type = reportType(filename);
    if (!type || filename.includes("/")) throw new Error("generation record contains an unsupported report");
    const sourcePath = `${prefix}${filename}`;
    const markdown = git(checkout, "show", `${publicationCommit}:${sourcePath}`);
    if (hash(markdown) !== marker.files[filename]) throw new Error("report differs from the generation record");
    const { title, summary } = describeReport(markdown, highlights, type.category, type.language);
    reports.push({
      edition_date: date, category: type.category, language: type.language, title, summary, markdown,
      source_commit: publicationCommit, source_path: sourcePath,
      generated_at: new Date(marker.generated_at).toISOString().replace(".000Z", "Z"),
      cover: {
        kind: "category", card_url: null, hero_url: null,
        fallback: ["ai-trending", "ai-community"].includes(type.category) ? "studio" : "orbit", alt: title,
      },
    });
  }
  if (!reports.length) throw new Error("generation record contains no reports");
  return {
    repository, commit: publicationCommit, date, sequence: marker.source_sequence, reports,
    skipped: tree.filter((file) => file.endsWith(".md") && !reports.some((report) => report.source_path === file)),
  };
}

/** 按序列化后的实际 UTF-8 大小切批，JSON 转义开销同样计入服务的 8 MiB 上限。 */
export function makeBatches(edition, { maxBytes = MAX_REQUEST_BYTES, maxItems = 50 } = {}) {
  if (maxBytes < 1 || maxBytes > MAX_REQUEST_BYTES || maxItems < 1 || maxItems > 50) {
    throw new Error("invalid batch limits");
  }
  const request = (reports) => {
    const core = {
      source: "agents-radar", source_kind: "starcat", source_repository: edition.repository,
      source_sequence: edition.sequence, reports,
    };
    return { idempotency_key: `daily-${edition.commit}-${hash(JSON.stringify(core))}`, ...core };
  };
  const batches = [];
  let pending = [];
  for (const report of edition.reports) {
    if (bytes(report.markdown) > MAX_MARKDOWN_BYTES) throw new Error("markdown exceeds 1 MiB");
    const next = [...pending, report];
    if (next.length > maxItems || bytes(request(next)) > maxBytes) {
      if (!pending.length) throw new Error("single report exceeds request limit");
      batches.push(request(pending));
      pending = [];
    }
    pending.push(report);
    if (bytes(request(pending)) > maxBytes) throw new Error("single report exceeds request limit");
  }
  if (pending.length) batches.push(request(pending));
  if (!batches.length) throw new Error("no reports to publish");
  return batches;
}

function apiRoot(raw, allowLocalHTTP) {
  let url;
  try { url = new URL(raw); } catch { throw new Error("invalid API base URL"); }
  const local = allowLocalHTTP && url.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname);
  if ((url.protocol !== "https:" && !local) || url.username || url.password || url.search || url.hash || url.pathname !== "/") {
    throw new Error("API base URL must be an HTTPS origin without credentials");
  }
  return url.origin;
}

/** 只在网络、429、5xx 有限重试；不记录原始错误响应，不跟随任何携带 Admin Key 的重定向。 */
export async function requestJSON(url, { token, method = "GET", body, fetchImpl = fetch, sleepImpl = sleep, maxAttempts = 5, timeoutMs = 30_000 }) {
  const serialized = body === undefined ? undefined : JSON.stringify(body);
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    let response;
    try {
      response = await fetchImpl(url, {
        method, body: serialized, redirect: "manual", signal: AbortSignal.timeout(timeoutMs),
        headers: {
          Authorization: `Bearer ${token}`, "X-SC-Svc": "weekly", Accept: "application/json",
          ...(serialized === undefined ? {} : { "Content-Type": "application/json" }),
        },
      });
    } catch {
      if (attempt === maxAttempts) throw new Error("API request failed after bounded retries");
      await sleepImpl(Math.min(30_000, 1000 * 2 ** (attempt - 1)));
      continue;
    }
    if (response.status === 429 || response.status >= 500) {
      const retryAfter = response.headers.get("retry-after");
      await response.body?.cancel();
      if (attempt === maxAttempts) throw new Error(`API HTTP ${response.status} after bounded retries`);
      const seconds = Number(retryAfter);
      const retryDelay = retryAfter === null ? NaN : (Number.isFinite(seconds) ? seconds * 1000 : Date.parse(retryAfter) - Date.now());
      await sleepImpl(Math.max(0, Math.min(30_000, Number.isFinite(retryDelay) ? retryDelay : 1000 * 2 ** (attempt - 1))));
      continue;
    }
    if ((method === "POST" && response.status !== 202) || (method === "GET" && response.status !== 200)) {
      await response.body?.cancel();
      throw new Error(`API HTTP ${response.status}; request was not accepted`);
    }
    let envelope;
    try { envelope = await response.json(); } catch { throw new Error("API response is not JSON"); }
    if (envelope?.schema_version !== 1 || !envelope.data) throw new Error("unexpected API envelope");
    return envelope.data;
  }
  throw new Error("invalid retry count");
}

function validateBatch(batch, total, expectedID) {
  if (!/^[A-Za-z0-9_-]{1,100}$/.test(batch?.id ?? "") ||
      (expectedID && batch.id !== expectedID) || batch.total !== total ||
      !["queued", "processing", "complete", "partial", "failed"].includes(batch.status) ||
      !Number.isInteger(batch.published) || !Number.isInteger(batch.failed) ||
      batch.published < 0 || batch.failed < 0 || batch.published + batch.failed > total) {
    throw new Error("invalid import batch response");
  }
}

/** 202 只表示持久入队；必须等待每批 complete 且数量吻合才算发布成功。 */
export async function publishBatches(batches, {
  baseURL, token, allowLocalHTTP = false, fetchImpl = fetch, sleepImpl = sleep,
  now = Date.now, pollIntervalMs = 5_000, pollTimeoutMs = 60 * 60_000,
  log = (line) => console.log(line),
}) {
  const base = apiRoot(baseURL, allowLocalHTTP);
  if (!token || /[\r\n]/.test(token)) throw new Error("DAILY_RADAR_ADMIN_API_KEY is required");
  const outcomes = [];
  for (const request of batches) {
    let batch = await requestJSON(`${base}/internal/daily-radar/imports`, {
      token, method: "POST", body: request, fetchImpl, sleepImpl,
    });
    validateBatch(batch, request.reports.length);
    const id = batch.id;
    const deadline = now() + pollTimeoutMs;
    let previous = "";
    while (true) {
      validateBatch(batch, request.reports.length, id);
      const state = `${batch.status}:${batch.published}:${batch.failed}`;
      if (state !== previous) log(`batch ${id}: ${batch.status} (${batch.published}/${batch.total} published, ${batch.failed} failed)`);
      previous = state;
      if (batch.status === "complete" && batch.published === batch.total && batch.failed === 0) break;
      if (["complete", "partial", "failed"].includes(batch.status)) throw new Error(`batch ${id} did not publish all reports`);
      if (now() >= deadline) throw new Error(`batch ${id} still processing; rerun the same committed edition to resume`);
      await sleepImpl(Math.min(pollIntervalMs, deadline - now()));
      batch = await requestJSON(`${base}/internal/daily-radar/imports/${id}`, { token, fetchImpl, sleepImpl });
    }
    outcomes.push(batch);
  }
  return outcomes;
}

/** CLI 默认仅验证固定提交并盘点；只有 publish --apply 才会调用管理 API。 */
export async function main(argv = process.argv.slice(2), env = process.env) {
  const { values, positionals } = parseArgs({
    args: argv, allowPositionals: true,
    options: {
      checkout: { type: "string", default: "." }, commit: { type: "string" },
      date: { type: "string", default: shanghaiDate() }, repository: { type: "string", default: env.GITHUB_REPOSITORY },
      sequence: { type: "string" }, apply: { type: "boolean", default: false },
    },
  });
  const options = { checkout: values.checkout, commit: values.commit, date: values.date, repository: values.repository };
  const command = positionals[0] ?? "publish";
  if (positionals.length > 1) throw new Error("unexpected positional arguments");
  if (command === "prepare") {
    prepareGeneration({ ...options, sequence: Number(values.sequence) });
    return;
  }
  if (command === "seal") {
    sealGeneration(options);
    return;
  }
  if (!["publish", "status"].includes(command)) throw new Error("expected prepare, seal, status or publish");
  const edition = loadEdition(options);
  if (command === "status") {
    console.log(`ready=${edition !== null}`);
    return;
  }
  if (!edition) throw new Error("no Starcat generation record; do not publish inherited upstream files as self-generated");
  const batches = makeBatches(edition);
  console.log(`${edition.date}: ${edition.reports.length} reports, ${batches.length} batches, source ${edition.commit}`);
  for (const skipped of edition.skipped) console.log(`skip ${skipped}: not included in this generation`);
  if (!values.apply) {
    console.log("dry-run complete; no API requests sent");
    return;
  }
  await publishBatches(batches, {
    baseURL: env.DAILY_RADAR_API_URL || "https://starcat-api.fly.dev",
    token: env.DAILY_RADAR_ADMIN_API_KEY,
  });
  console.log("all reports published");
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch((error) => {
    // 底层异常不包含请求头或响应正文；不要在这里打印对象/堆栈扩大凭据暴露面。
    console.error(`daily-radar: ${error.message}`);
    process.exitCode = 1;
  });
}
