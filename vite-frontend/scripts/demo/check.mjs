// Checks the built online demo the way a visitor sees it: serves the build
// under /flmx-max/demo/, opens every page in headless Chrome (as admin, then as
// the demo user) and fails on page errors, pages that bounce to the login
// screen, and API calls the recording doesn't answer ("[demo] not
// recorded", see README.md). No dependencies: Chrome DevTools Protocol over
// Node's built-in WebSocket.
//
//   node scripts/demo/check.mjs <demo build dir>
//   CHROME=/path/to/chrome to pick the browser.
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";

const dir = path.resolve(process.argv[2] || "dist-demo");
const BASE = "/flmx-max/demo/";
// R5X-008: admins have no standalone monitor page since the DA-1 IA
// merge — /monitor redirects them to /node — so "monitor" is not an
// admin walk page. The user walk covers it instead: the demo user book
// is a monitoring-authorized one (monitor/access allowed:true), which
// is the only role that still renders MonitorOverview at /monitor.
const ADMIN_PAGES = ["dashboard", "node", "inbound", "relay", "forward", "tunnel", "subscription", "limit", "user", "panel-sharing", "config", "profile", "change-password"];
const USER_PAGES = ["dashboard", "my-subscription", "forward", "monitor", "profile"];
// Settle, condition-based (R9 BINFRA; K settle 条件化评估采纳,
// R9-X-verdicts §7 / R9-K-report 观察项, same root as R8-J O-1): a
// fixed 2500ms settle flakes under high host load — the R9本体 run
// failed only "user #/monitor (interaction): control not found"
// while a 10s-settle diagnostic copy of the same dist passed all
// 13 admin + 5 user pages. Page and interaction settles now poll a
// readiness condition until SETTLE_MAX_MS; the historical 2500ms
// remains the minimum settle, so a page is never judged earlier
// than under the old fixed wait. Overrides: DEMO_CHECK_SETTLE_MS
// (minimum settle) and DEMO_CHECK_SETTLE_MAX_MS (poll ceiling).
const SETTLE_MS = Number(process.env.DEMO_CHECK_SETTLE_MS || 2500);
const SETTLE_MAX_MS = Math.max(SETTLE_MS, Number(process.env.DEMO_CHECK_SETTLE_MAX_MS || 10000));
const SETTLE_POLL_MS = 100;

const TYPES = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon", ".json": "application/json", ".webmanifest": "application/manifest+json", ".woff2": "font/woff2" };
const server = http.createServer((req, res) => {
  const url = decodeURIComponent(req.url.split("?")[0]);
  if (!url.startsWith(BASE)) return res.writeHead(404).end();
  let file = path.join(dir, url.slice(BASE.length));
  if (!file.startsWith(dir)) return res.writeHead(403).end();
  if (fs.existsSync(file) && fs.statSync(file).isDirectory()) file = path.join(file, "index.html");
  if (!fs.existsSync(file)) return res.writeHead(404).end();
  res.writeHead(200, { "Content-Type": TYPES[path.extname(file)] || "application/octet-stream" });
  fs.createReadStream(file).pipe(res);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const origin = `http://127.0.0.1:${server.address().port}`;

const onPath = (bin) => (process.env.PATH || "").split(path.delimiter).some((d) => fs.existsSync(path.join(d, bin)));
const chromeBin = process.env.CHROME || ["google-chrome", "chromium", "chromium-browser"].find(onPath) || "google-chrome";
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "demo-check-"));
const browserEnv = Object.fromEntries(Object.entries(process.env).filter(([k]) => !/proxy/i.test(k)));
const chrome = spawn(chromeBin, ["--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
  "--disable-features=LocalNetworkAccessChecks",
  "--remote-debugging-port=0", `--user-data-dir=${profile}`, "--window-size=1366,900", "about:blank"], { env: browserEnv });
const wsURL = await new Promise((resolve, reject) => {
  let err = "";
  chrome.stderr.on("data", (d) => {
    err += d;
    const m = err.match(/DevTools listening on (ws:\/\/\S+)/);
    if (m) resolve(m[1]);
  });
  chrome.on("exit", (code) => reject(new Error(`chrome exited (${code}): ${err}`)));
  setTimeout(() => reject(new Error("chrome did not start")), 30000);
});

const ws = new WebSocket(wsURL);
await new Promise((r) => ws.addEventListener("open", r, { once: true }));
let nextId = 0;
const pending = new Map();
const listeners = [];
ws.addEventListener("message", (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id);
    pending.delete(msg.id);
    msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result);
  } else if (msg.method) {
    for (const l of listeners) l(msg);
  }
});
const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
  const id = ++nextId;
  pending.set(id, { resolve, reject });
  ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const { targetId } = await send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
const page = (method, params) => send(method, params, sessionId);
await page("Page.enable");
await page("Runtime.enable");

let current = "";
const problems = [];
const notes = [];
listeners.push((msg) => {
  if (msg.sessionId !== sessionId) return;
  if (msg.method === "Runtime.exceptionThrown") {
    const d = msg.params.exceptionDetails;
    problems.push(`${current}: page error: ${d.exception?.description?.split("\n")[0] || d.text}`);
  }
  if (msg.method === "Runtime.consoleAPICalled") {
    const text = msg.params.args.map((a) => a.value ?? a.description ?? "").join(" ");
    if (text.startsWith("[demo] not recorded")) problems.push(`${current}: ${text}`);
    else if (msg.params.type === "error") notes.push(`${current}: console.error: ${text.slice(0, 200)}`);
  }
});
const evaluate = async (expression) => (await page("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true })).result.value;

// Poll `ready` (an evaluate() thunk) after the minimum settle,
// until it holds or the SETTLE_MAX_MS ceiling is reached. A thunk
// that throws counts as not-ready.
async function settleUntil(ready) {
  await sleep(SETTLE_MS);
  const deadline = Date.now() + (SETTLE_MAX_MS - SETTLE_MS);
  while (Date.now() <= deadline) {
    let ok = false;
    try { ok = await ready(); } catch { ok = false; }
    if (ok) return;
    await sleep(SETTLE_POLL_MS);
  }
}

async function visit(role, route) {
  current = `${role} #/${route}`;
  await page("Page.navigate", { url: `${origin}${BASE}#/${route}` });
  await settleUntil(() => evaluate(`location.hash.startsWith("#/${route}") && !!document.body && document.body.innerText.length >= 40`));
  const state = await evaluate(`({ hash: location.hash, text: document.body.innerText.length })`);
  if (!state.hash.startsWith(`#/${route}`)) problems.push(`${current}: ended up at ${state.hash} (signed out?)`);
  else if (state.text < 40) problems.push(`${current}: blank page`);
}

// Interaction steps for the walk's structural blind spots (R3X-032):
// pages only fetch these endpoints after a click, so a plain page visit
// never proves their recordings exist. A step that cannot find its
// control is itself a failure — the blind spot stays in CI's view.
const INTERACTIONS = {
  // The tunnel monitor pulls per-tunnel quality history (and metrics
  // on detail open). Since the DA-1 IA merge this lives on the user
  // walk (R5X-008): admins are redirected away from /monitor, the
  // authorized demo user is not. It used to mount only after a click
  // on the "隧道" tab; since the 1.7.11 monitor scoping the
  // authorized user's /monitor renders the tunnel monitor directly
  // (UserTunnelMonitorOverview), so there is no tab left to click —
  // the step instead asserts the view is presented, by its "监控面板"
  // heading. The mount itself (and with it the endpoint calls this
  // step exists to keep in CI's view) happens either way.
  "user monitor": `(() => { const h = [...(document.querySelector("main") ?? document).querySelectorAll("h2")].find((x) => x.textContent.trim() === "监控面板"); return !!h; })()`,
  // Expanding a subscription's traffic section mounts SubTrafficStats,
  // which pulls user/subscription/traffic.
  "user my-subscription": `(() => { const b = [...(document.querySelector("main") ?? document).querySelectorAll("button")].find((x) => x.textContent.includes("该订阅流量统计")); if (!b) return false; b.click(); return true; })()`,
  // R3-C5-02: the Gist upload button only calls backup/gist/upload
  // after a click, and the endpoint is a recorded fixture (not a
  // WRITE_WORDS success) because the page consumes the response's
  // {result, status} shape — the not-recorded empty array crashed
  // the section (setStatus(undefined) + TypeError) while no walk
  // ever clicked it. Fill token + password (the button stays
  // disabled until both are present), then click 立即上传. Filling
  // is idempotent, so the poll may re-run this step until the
  // button enables; the click itself happens exactly once (the
  // step returns true only on the evaluating pass that clicks).
  // R4-C5-01/02/03: the same step continues through the config
  // page's other data-carrying actions, in window-flagged phases
  // (each click exactly once; later passes only observe):
  // phase 2 exports a backup (选择并导出 → 全选 → 确认导出; the
  // selector modal closing is the success marker — a failed export
  // keeps it open behind an error toast, and before the adapter
  // synthesized an attachment response the click threw a TypeError
  // page error). 全选 and the confirm click are separate passes:
  // the confirm handler reads the selection from React state, so
  // clicking both in one pass confirms the still-empty selection);
  // phase 3+4 imports one (选择并导入 → 全选 →
  // 下一步选择文件, then the CDP hook in interact() sets the
  // patrol backup file on the hidden input, then 确认导入; the
  // settle marker is the receipt panel's 共导入 line — the patrol
  // file carries a legacy tunnelGroups row, so the panel must also
  // name it as ignored); phase 5 restores from Gist (刷新版本列表
  // → 恢复此版本 → password → 恢复; success reloads the page and
  // the receipt survives via sessionStorage, so the final marker
  // is checked before phase 1 — a reloaded step sees it at once).
  "admin config": `(() => { const w = window; const scope = document.querySelector("main") ?? document; const setVal = (el, v) => { const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set; setter.call(el, v); el.dispatchEvent(new Event("input", { bubbles: true })); }; const btn = (root, t) => [...root.querySelectorAll("button")].find((x) => x.textContent.trim() === t); const dlgByTitle = (t) => [...document.querySelectorAll('[role="dialog"]')].find((d) => { const h = d.querySelector('[data-slot="modal-header"]') ?? d.querySelector("h2"); return h && h.textContent.trim() === t; }) ?? null; if (document.body.textContent.includes("最近一次 Gist 恢复结果")) return true; if (!w.__cg1) { const token = scope.querySelector('input[aria-label="GitHub 令牌"]'); const password = scope.querySelector('input[aria-label="加密密码"]'); if (!token || !password) return false; if (!token.value) setVal(token, "ghp_demo_patrol_token"); if (!password.value) setVal(password, "demo-patrol-password"); const b = btn(scope, "立即上传"); if (!b || b.disabled) return false; w.__cg1 = true; b.click(); return false; } if (!w.__cg2) { const b = btn(scope, "选择并导出"); if (!b) return false; w.__cg2 = true; b.click(); return false; } if (!w.__cg3) { const d = dlgByTitle("选择导出内容"); if (!d) return false; const all = btn(d, "全选"); if (!all) return false; w.__cg3 = true; all.click(); return false; } if (!w.__cg3b) { const d = dlgByTitle("选择导出内容"); if (!d) return false; const b = btn(d, "确认导出"); if (!b || b.disabled) return false; w.__cg3b = true; b.click(); return false; } if (dlgByTitle("选择导出内容")) return false; if (!w.__cg4) { const b = btn(scope, "选择并导入"); if (!b) return false; w.__cg4 = true; b.click(); return false; } if (!w.__cg5) { const d = dlgByTitle("选择导入内容"); if (!d) return false; const all = btn(d, "全选"); if (!all) return false; w.__cg5 = true; all.click(); return false; } if (!w.__cg5b) { const d = dlgByTitle("选择导入内容"); if (!d) return false; const b = btn(d, "下一步选择文件"); if (!b || b.disabled) return false; w.__cg5b = true; w.__impPick = true; b.click(); return false; } if (!w.__impDone) return false; if (!w.__cg6) { const d = dlgByTitle("确认导入备份"); if (!d) return false; const b = btn(d, "确认导入"); if (!b || b.disabled) return false; w.__cg6 = true; b.click(); return false; } if (!document.body.textContent.includes("共导入")) return false; if (!w.__cg7) { const b = btn(scope, "刷新版本列表"); if (!b) return false; w.__cg7 = true; b.click(); return false; } if (!w.__cg8) { const b = btn(scope, "恢复此版本"); if (!b) return false; w.__cg8 = true; b.click(); return false; } if (!w.__cg9) { const d = dlgByTitle("从 Gist 恢复"); if (!d) return false; const pw = d.querySelector('input[aria-label="加密密码"]'); if (!pw) return false; if (!pw.value) setVal(pw, "demo-patrol-password"); const b = btn(d, "恢复"); if (!b || b.disabled) return false; w.__cg9 = true; b.click(); return false; } return false; })()`,
  // R4-C5-01: the node row's action sheet is the only path to
  // node/install; its answer used to be data:null and the modal
  // error-toasted the write answer's own msg ("操作成功"). Open
  // the sheet from the first node row, click 安装命令, and settle
  // on the synthesized command text itself — only a data-carrying
  // answer renders it. (The same sheet's 监控 entry and the
  // service-monitor 立即检查 chain behind it stay registered
  // blind spots, see README: the run answer is covered by the
  // adapter's shape, the chain is a multi-level menu the walk
  // does not drive.)
  "admin node": `(() => { const w = window; const scope = document.querySelector("main") ?? document; if (!w.__ni1) { const row = scope.querySelector('[role="button"].glass-card'); if (!row) return false; w.__ni1 = true; row.click(); return false; } if (!w.__ni2) { const sheet = [...document.querySelectorAll('[role="dialog"]')].find((d) => d.textContent.includes("安装命令")); if (!sheet) return false; const b = [...sheet.querySelectorAll("button")].find((x) => x.textContent.trim() === "安装命令"); if (!b) return false; w.__ni2 = true; b.click(); return false; } const dlg = [...document.querySelectorAll('[role="dialog"]')].find((d) => d.textContent.includes("演示站合成的示例命令")); if (!dlg) return false; const c = [...dlg.querySelectorAll("button")].find((x) => ["关闭", "取消", "完成"].includes(x.textContent.trim())); if (c) c.click(); return true; })()`,
  // R4-C5-I01: importing a remote node used to drop the receipt's
  // panelVersion/versionWarning disclosure (data:null, generic
  // success). Fill the dialog, import, and settle on the dialog
  // closing — it only closes on a code-0 answer.
  "admin panel-sharing": `(() => { const w = window; const scope = document.querySelector("main") ?? document; const setVal = (el, v) => { const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set; setter.call(el, v); el.dispatchEvent(new Event("input", { bubbles: true })); }; const dlg = () => [...document.querySelectorAll('[role="dialog"]')].find((d) => { const h = d.querySelector('[data-slot="modal-header"]') ?? d.querySelector("h2"); return h && h.textContent.trim() === "导入远程节点"; }) ?? null; if (!w.__pf0) { const t = [...scope.querySelectorAll('[role="tab"]')].find((x) => x.textContent.includes("远程节点")); if (!t) return false; w.__pf0 = true; t.focus(); for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) t.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, view: window })); return false; } if (!w.__pf1) { const b = [...scope.querySelectorAll("button")].find((x) => x.textContent.trim() === "导入远程节点"); if (!b) return false; w.__pf1 = true; b.click(); return false; } if (!w.__pf2) { const d = dlg(); if (!d) return false; const url = d.querySelector('input[placeholder^="http://panel"]'); const tok = d.querySelector('input[placeholder^="Bearer"]'); if (!url || !tok) return false; if (!url.value) setVal(url, "http://panel.example.com:8088"); if (!tok.value) setVal(tok, "demo-token"); const b = [...d.querySelectorAll("button")].find((x) => x.textContent.trim() === "导入"); if (!b || b.disabled) return false; w.__pf2 = true; b.click(); return false; } return !dlg(); })()`,
  // R4-C5-02: the external-node import dialog's receipt (created
  // ids + per-line errors) used to be unreachable (data:null →
  // "已导入 0 个节点"). Paste one share link, submit, and settle
  // on the dialog closing — success with no errors closes it, a
  // null receipt kept it open or reported zero.
  "admin inbound": `(() => { const w = window; const scope = document.querySelector("main") ?? document; const setVal = (el, v) => { const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value").set; setter.call(el, v); el.dispatchEvent(new Event("input", { bubbles: true })); }; const dlg = () => { const h = [...document.querySelectorAll("h2")].find((x) => x.textContent.trim() === "导入节点"); return h?.closest('[role="dialog"]') ?? null; }; if (!w.__ib1) { const b = [...scope.querySelectorAll("button")].find((x) => x.textContent.trim() === "导入"); if (!b) return false; w.__ib1 = true; b.click(); return false; } if (!w.__ib2) { const d = dlg(); if (!d) return false; const ta = d.querySelector("textarea"); if (!ta) return false; if (!ta.value) setVal(ta, "ss://YWVzLTI1Ni1nY206ZGVtbw==@203.0.113.11:8388#patrol-demo"); const b = [...d.querySelectorAll("button")].find((x) => x.textContent.trim() === "导入" && !x.disabled); if (!b) return false; w.__ib2 = true; b.click(); return false; } return !dlg(); })()`,
  // R4-C5-01 (partial): opening the template manager proves the
  // recorded list renders; the per-template 刷新 button exists
  // only on URL-sourced templates and the recording's one
  // template is source-type, so the refresh click itself stays a
  // registered blind spot (README) — its synthesized answer is
  // covered by the adapter's shape (refresh returns the recorded
  // row with the fetch error cleared).
  "admin subscription": `(() => { const w = window; const scope = document.querySelector("main") ?? document; if (!w.__st1) { const b = [...scope.querySelectorAll("button")].find((x) => x.textContent.trim() === "模板"); if (!b) return false; w.__st1 = true; b.click(); return false; } const dlg = [...document.querySelectorAll('[role="dialog"]')].find((d) => d.textContent.includes("Mihomo 基础模板")); if (!dlg) return false; const c = [...dlg.querySelectorAll("button")].find((x) => ["关闭", "取消", "完成"].includes(x.textContent.trim())); if (c) c.click(); return true; })()`,
  // RR-C5-05: opening a tunnel's delete dialog fires tunnel/delete-
  // preview (and 批量删除 fires tunnel/batch-delete-preview) —
  // data-carrying POSTs the demo wordlist used to judge writes
  // (data:null). The pages fail closed on a null preview and
  // error-toast the write answer's own msg ("操作成功"), so tunnel
  // deletion was unreachable in the demo. The adapter now synthesizes
  // both previews from the recorded lists. Phase 1: click the first
  // enabled 删除 (the batch button carries the same label but stays
  // disabled with nothing selected) and wait for the dialog to show
  // the dependency branch. Only that branch proves preview data
  // arrived: the no-dependency text ("可以直接删除") is also the
  // modal's transient default while a null preview resolves, so it
  // must NOT be accepted as settled. Every recorded tunnel has
  // forwards, so the dependency branch is the only correct settle.
  // Phase 2: enter 批量 select mode, tick one checkbox, open the
  // batch dialog from the toolbar (first enabled 删除 in DOM order
  // is the toolbar's once select mode is on) and wait for its
  // data-only summary ("条仍被规则使用"). If a preview regresses to
  // null the page closes the dialog itself, the step never settles,
  // and the walk fails. Clicks happen exactly once (window flags);
  // later passes only observe.
  "admin tunnel": `(() => { const w = window; const scope = document.querySelector("main") ?? document; const dlgByTitle = (title) => { const h = [...document.querySelectorAll("h2")].find((x) => x.textContent.trim() === title); return h?.closest('[role="dialog"]') ?? h?.closest("section") ?? null; }; const cancelOf = (dlg) => [...dlg.querySelectorAll("button")].find((x) => x.textContent.trim() === "取消"); if (!w.__dt1) { const b = [...scope.querySelectorAll("button")].find((x) => x.textContent.trim() === "删除" && !x.disabled); if (!b) return false; w.__dt1 = true; b.click(); return false; } if (!w.__dt1Done) { const dlg = dlgByTitle("删除隧道"); if (!dlg) return false; const t = dlg.textContent ?? ""; if (t.includes("正在检查")) return false; if (!t.includes("发现关联规则") || !t.includes("关联规则预览")) return false; const c = cancelOf(dlg); if (c) c.click(); w.__dt1Done = true; return false; } if (!w.__dt2) { const b = [...scope.querySelectorAll("button")].find((x) => x.textContent.trim() === "批量" && !x.disabled); if (!b) return false; w.__dt2 = true; b.click(); return false; } if (!w.__dt2Picked) { const box = scope.querySelector('[role="checkbox"]'); if (!box) return false; box.click(); w.__dt2Picked = true; return false; } if (!w.__dt2Clicked) { const b = [...scope.querySelectorAll("button")].find((x) => x.textContent.trim() === "删除" && !x.disabled); if (!b) return false; w.__dt2Clicked = true; b.click(); return false; } const dlg = dlgByTitle("批量删除隧道"); if (!dlg) return false; const t = dlg.textContent ?? ""; if (t.includes("正在检查")) return false; if (!t.includes("条仍被规则使用")) return false; const c = cancelOf(dlg); if (c) c.click(); return true; })()`,
};

// Multi-phase steps (R4-C5: the config step chains gist upload,
// backup export, backup import and gist restore, the last of which
// reloads the page) get a longer poll ceiling than the page settle;
// the minimum settle is unchanged, so no step is judged earlier
// than under the old fixed wait.
const STEP_BUDGET_MS = {
  "admin config": 120000,
  "admin node": 45000,
  "admin inbound": 45000,
  "admin panel-sharing": 45000,
  "admin subscription": 45000,
};

// The patrol backup file the config step imports: two users, one
// node, and one legacy tunnelGroups row — the import receipt must
// count the three real rows and name the removed segment as
// ignored (R4-C5-02). Written fresh each run; never a real backup.
const patrolBackupPath = path.join(os.tmpdir(), "demo-patrol-backup.json");
fs.writeFileSync(
  patrolBackupPath,
  JSON.stringify({
    version: "1.0",
    exportedAt: Date.now(),
    users: [
      { id: 1, username: "patrol-a" },
      { id: 2, username: "patrol-b" },
    ],
    nodes: [{ id: 1, name: "patrol-node" }],
    tunnelGroups: [{ id: 1, name: "patrol-legacy-group" }],
  }),
);

// CDP hook for the config step's import phase: the step clicks
// 下一步选择文件 (which opens the hidden file input) and flags
// __impPick; file choosers are native UI the evaluate-only steps
// cannot drive, so set the patrol file on the input here. Setting
// files fires the input's change event, which opens the page's
// 确认导入备份 dialog for the step's next phase.
async function maybeSetImportFile(key) {
  if (key !== "admin config") return;
  const want = await evaluate(
    `window.__impPick === true && !window.__impDone`,
  );

  if (!want) return;
  await page("DOM.enable");
  const { root } = await page("DOM.getDocument", { depth: 1 });
  const { nodeId } = await page("DOM.querySelector", {
    nodeId: root.nodeId,
    selector: 'input[type="file"][accept=".json"]',
  });

  if (!nodeId) return;
  await page("DOM.setFileInputFiles", {
    nodeId,
    files: [patrolBackupPath],
  });
  await evaluate(`window.__impDone = true; true`);
}

async function interact(role, route) {
  const key = `${role} ${route}`;
  const step = INTERACTIONS[key];

  if (!step) return;
  current = `${role} #/${route} (interaction)`;
  // The step returns false (and clicks nothing) while its control is
  // not mounted yet, so re-evaluating it until it clicks is safe:
  // poll it under the same ceiling as the page settle instead of
  // judging "control not found" after one fixed wait.
  const budget = STEP_BUDGET_MS[key] ?? SETTLE_MAX_MS;
  let clicked = await evaluate(step);
  const deadline = Date.now() + (budget - SETTLE_MS);
  while (!clicked && Date.now() <= deadline) {
    await sleep(SETTLE_POLL_MS);
    await maybeSetImportFile(key);
    clicked = await evaluate(step);
  }

  if (!clicked) problems.push(`${current}: interaction control not found`);
  await sleep(SETTLE_MS);
}

current = "admin (first load)";
await page("Page.navigate", { url: `${origin}${BASE}` });
await sleep(SETTLE_MS);
for (const r of ADMIN_PAGES) {
  await visit("admin", r);
  await interact("admin", r);
}
// The demo note's switch signs in as the demo user and reloads.
await evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent.includes("普通用户视角"))?.click()`);
await sleep(SETTLE_MS);
for (const r of USER_PAGES) {
  await visit("user", r);
  await interact("user", r);
}

for (const n of notes) console.log(`note  ${n}`);
for (const p of problems) console.log(`FAIL  ${p}`);
console.log(problems.length
  ? `\n${problems.length} problem(s). Recording missing for new pages? See scripts/demo/README.md.`
  : `demo ok: ${ADMIN_PAGES.length} admin + ${USER_PAGES.length} user pages`);

// Chrome keeps writing its profile until it has exited; clean up after
// that, and never let cleanup decide the result.
ws.close();
server.close();
await new Promise((r) => {
  chrome.once("exit", r);
  chrome.kill();
  setTimeout(r, 5000);
});
try {
  fs.rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
} catch {
  // a leftover temp directory is harmless
}
process.exit(problems.length ? 1 : 0);
