"use strict";

const byID = (id) => document.getElementById(id);
const records = new Map();
let polling = false;

byID("api-url").value = location.origin;
byID("sample").value = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>双联送货单</title>
<style>
@page { size: A4; margin: 15mm; }
body { font: 13px "PingFang SC", "Noto Sans CJK SC", sans-serif; color: #172f37; }
section { height: 118mm; box-sizing: border-box; padding: 5mm 0; }
section + section { border-top: 1px dashed #666; }
h1 { text-align: center; font-size: 24px; letter-spacing: 5px; }
.meta { display: flex; justify-content: space-between; margin: 8mm 0; }
table { width: 100%; border-collapse: collapse; }
th, td { border: 1px solid #777; padding: 3mm; text-align: left; }
th { background: #eef3ef; } .foot { margin-top: 10mm; }
@media print { section { break-inside: avoid; } }
</style></head><body>
<section><h1>送货单 · 客户联</h1><div class="meta"><span>客户：样例商店</span><span>单号：DEMO-001</span></div>
<table><thead><tr><th>商品</th><th>数量</th><th>单位</th></tr></thead><tbody><tr><td>A4 打印纸</td><td>10</td><td>包</td></tr><tr><td>文件夹</td><td>24</td><td>个</td></tr></tbody></table><p class="foot">收货人：________________　日期：________________</p></section>
<section><h1>送货单 · 存根联</h1><div class="meta"><span>客户：样例商店</span><span>单号：DEMO-001</span></div>
<table><thead><tr><th>商品</th><th>数量</th><th>单位</th></tr></thead><tbody><tr><td>A4 打印纸</td><td>10</td><td>包</td></tr><tr><td>文件夹</td><td>24</td><td>个</td></tr></tbody></table><p class="foot">经办人：________________　日期：________________</p></section>
</body></html>`;

function newKey() {
  byID("request-key").value = crypto.randomUUID();
}
newKey();
byID("new-key").addEventListener("click", newKey);
byID("kind").addEventListener("change", () => {
  const html = byID("kind").value === "html";
  byID("document").value = "";
  byID("document").accept = html ? ".html,.htm,text/html" : ".pdf,application/pdf";
  byID("sample-details").hidden = !html;
  byID("file-hint").textContent = html
    ? "未选文件时使用下方样例。HTML 需包含内联 CSS，并使用 data URL 携带图片和字体。"
    : "请选择一个 PDF 文件；服务会保存原始字节，下载后可核对 SHA-256。";
  newKey();
});

function connection() {
  const url = new URL(byID("api-url").value);
  if (url.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(url.hostname) || url.username || url.password || url.pathname !== "/" || url.search || url.hash) {
    throw new Error("请填写本机 HTTP 地址，例如 http://127.0.0.1:9148");
  }
  const token = byID("token").value.trim();
  if (!token) throw new Error("请先输入调用方令牌");
  return { base: url.origin, token };
}

async function request(config, path, options = {}) {
  let response;
  try {
    response = await fetch(config.base + path, {
      ...options,
      headers: { ...options.headers, Authorization: "Bearer " + config.token },
      cache: "no-store",
      credentials: "omit",
      signal: AbortSignal.timeout(15000),
    });
  } catch (error) {
    throw new Error("无法访问服务，请检查进程、地址、Origin 配置及浏览器本地网络权限。" + (error.name === "TimeoutError" ? "请求已超时。" : ""));
  }
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new Error(body?.error?.message || `请求失败（HTTP ${response.status}）`);
  }
  return response;
}

function feedback(message, failed = false) {
  byID("feedback").textContent = message;
  byID("feedback").classList.toggle("error", failed);
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function renderRecord(record) {
  const { job, node } = record;
  node.replaceChildren();
  const stateText = { queued: "等待执行", rendering: "正在处理", output_ready: job.kind === "html" ? "已生成 PDF" : "已保存 PDF", failed: "处理失败" };
  const head = element("div", "job-head");
  head.append(element("h3", "", job.title || "未命名文档"), element("span", "badge " + job.state, stateText[job.state] || job.state));
  node.append(head, element("p", "job-id", job.id));
  const details = element("dl");
  for (const [label, value] of [["格式", job.kind.toUpperCase()], ["创建时间", new Date(job.created_at).toLocaleString()], ["产物大小", job.output_size ? (job.output_size / 1024).toFixed(1) + " KiB" : "—"]]) {
    details.append(element("dt", "", label), element("dd", "", value));
  }
  node.append(details);
  if (job.attempts?.length) {
    const attempts = element("ol");
    for (const attempt of job.attempts) {
      attempts.append(element("li", attempt.error ? "error" : "", `执行 ${attempt.number} · ${attempt.finished_at ? "已结束" : "进行中"}${attempt.error ? " · " + attempt.error : ""}`));
    }
    node.append(attempts);
  }
  if (job.error) node.append(element("p", "hint error", job.error));
  if (record.queryError) node.append(element("p", "hint error", record.queryError));
  if (job.artifact_sha256) node.append(element("p", "digest", "SHA-256 · " + job.artifact_sha256));
  if (job.state === "output_ready") {
    const download = element("button", "secondary download", "下载 PDF ↓");
    download.type = "button";
    download.addEventListener("click", async () => {
      download.disabled = true;
      try {
        const response = await request(record.connection, `/v1/jobs/${encodeURIComponent(job.id)}/artifact`);
        const bytes = await response.arrayBuffer();
        const digest = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), (byte) => byte.toString(16).padStart(2, "0")).join("");
        if (digest !== job.artifact_sha256 || digest !== response.headers.get("X-Content-SHA256")) throw new Error("下载摘要不一致，请重试并检查服务");
        const href = URL.createObjectURL(new Blob([bytes], { type: "application/pdf" }));
        const anchor = element("a");
        anchor.href = href;
        anchor.download = "document.pdf";
        document.body.append(anchor);
        anchor.click();
        anchor.remove();
        setTimeout(() => URL.revokeObjectURL(href), 10000);
        feedback("PDF 已下载，SHA-256 校验一致。");
      } catch (error) {
        feedback(error.message, true);
      } finally {
        download.disabled = false;
      }
    });
    node.append(download);
  }
}

function track(job, config) {
  byID("empty-state")?.remove();
  const key = config.base + "/" + job.id;
  let record = records.get(key);
  if (!record) {
    record = { node: element("article", "job") };
    records.set(key, record);
    byID("jobs").prepend(record.node);
  }
  Object.assign(record, { job, connection: config, queryError: "", failures: 0 });
  renderRecord(record);
}

byID("submit-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = byID("submit-button");
  button.disabled = true;
  try {
    const config = connection();
    const kind = byID("kind").value;
    let file = byID("document").files[0];
    if (!file && kind === "html") file = new File([byID("sample").value], "sample.html", { type: "text/html" });
    if (!file) throw new Error("请选择 PDF 文件");
    const form = new FormData();
    form.append("metadata", JSON.stringify({ kind, title: byID("title").value }));
    form.append("document", file);
    const response = await request(config, "/v1/jobs", { method: "POST", headers: { "Idempotency-Key": byID("request-key").value }, body: form });
    const job = await response.json();
    track(job, config);
    byID("lookup-id").value = job.id;
    feedback(response.status === 202 ? "任务已受理。可复制任务编号，在重新打开页面后查询。" : "已找到相同请求对应的任务。");
  } catch (error) {
    feedback(error.message, true);
  } finally {
    button.disabled = false;
  }
});

byID("lookup-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const config = connection();
    const response = await request(config, "/v1/jobs/" + encodeURIComponent(byID("lookup-id").value.trim()));
    track(await response.json(), config);
    feedback("已恢复任务查询。");
  } catch (error) {
    feedback(error.message, true);
  }
});

setInterval(async () => {
  if (polling) return;
  polling = true;
  try {
    await Promise.all(Array.from(records.values(), async (record) => {
      if (!["queued", "rendering"].includes(record.job.state) || record.failures >= 3) return;
      try {
        const response = await request(record.connection, `/v1/jobs/${encodeURIComponent(record.job.id)}`);
        record.job = await response.json();
        record.queryError = "";
        record.failures = 0;
      } catch (error) {
        record.failures += 1;
        record.queryError = error.message + (record.failures >= 3 ? " 连续查询失败，请按任务编号重新查询。" : "");
      }
      renderRecord(record);
    }));
  } finally {
    polling = false;
  }
}, 1200);
