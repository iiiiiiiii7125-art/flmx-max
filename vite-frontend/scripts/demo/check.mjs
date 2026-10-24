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
const ADMIN_PAGES = ["dashboard", "node", "inbound", "relay", "forward", "tunnel", "subscription", "limit", "user", "group", "panel-sharing", "config", "profile", "change-password"];
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
  // Switching to the tunnel tab mounts the tunnel monitor, which pulls
  // per-tunnel quality history (and metrics on detail open). Since the
  // DA-1 IA merge this lives on the user walk (R5X-008): admins are
  // redirected away from /monitor, the authorized demo user is not.
  "user monitor": `(() => { const b = [...(document.querySelector("main") ?? document).querySelectorAll("button")].find((x) => x.textContent.trim() === "隧道"); if (!b) return false; b.click(); return true; })()`,
  // Expanding a subscription's traffic section mounts SubTrafficStats,
  // which pulls user/subscription/traffic.
  "user my-subscription": `(() => { const b = [...(document.querySelector("main") ?? document).querySelectorAll("button")].find((x) => x.textContent.includes("该订阅流量统计")); if (!b) return false; b.click(); return true; })()`,
};

async function interact(role, route) {
  const step = INTERACTIONS[`${role} ${route}`];

  if (!step) return;
  current = `${role} #/${route} (interaction)`;
  // The step returns false (and clicks nothing) while its control is
  // not mounted yet, so re-evaluating it until it clicks is safe:
  // poll it under the same ceiling as the page settle instead of
  // judging "control not found" after one fixed wait.
  let clicked = await evaluate(step);
  const deadline = Date.now() + (SETTLE_MAX_MS - SETTLE_MS);
  while (!clicked && Date.now() <= deadline) {
    await sleep(SETTLE_POLL_MS);
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
