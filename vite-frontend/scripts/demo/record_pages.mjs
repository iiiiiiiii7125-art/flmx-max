// Visits every page (and the main dialogs) against the seeded backend and
// records each /api/v1 response, keyed by path and request body.
//
// Recording source discipline: B is a constant on purpose — recordings may
// only ever come from the throwaway seeded panel on 127.0.0.1:16370
// (see README.md). Never point this at a real panel: responses are written
// to disk verbatim and later published inside fixtures.json.
import { chromium } from "playwright-core";
import fs from "node:fs";
const B = "http://127.0.0.1:16370/api/v1";
const out = process.argv[2];
const login = (u, p) => fetch(B + "/user/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ username: u, password: p }) }).then(r => r.json()).then(r => r.data);
// A proxied shell environment makes Chrome treat loopback navigation as
// public-to-local and block it (Local Network Access): strip proxy vars
// for the browser process only.
const browserEnv = Object.fromEntries(Object.entries(process.env).filter(([k]) => !/proxy/i.test(k)));
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM, env: browserEnv, args: ["--no-sandbox", "--disable-features=LocalNetworkAccessChecks"] });
const rec = {};
const errs = [];
async function session(role, user, pass, flows) {
  const d = await login(user, pass);
  const ctx = await browser.newContext({ viewport: { width: 1366, height: 1000 }, baseURL: "http://127.0.0.1:3114" });
  const page = await ctx.newPage(); page.setDefaultTimeout(10000);
  page.on("pageerror", (e) => errs.push(role + ": " + e.message));
  rec[role] = {};
  page.on("response", async (res) => {
    const u = new URL(res.url());
    if (!u.pathname.startsWith("/api/v1/") || res.request().method() === "OPTIONS") return;
    let json; try { json = await res.json(); } catch { return; }
    const path = u.pathname.slice("/api/v1/".length) + u.search;
    const body = res.request().postData() || "";
    (rec[role][path] ||= []);
    if (!rec[role][path].some((e) => e.body === body)) rec[role][path].push({ body, res: json });
  });
  await page.goto("/");
  // 我方会话模型（N-002）：只写 token 与 name，角色由前端从 JWT 解析。
  await page.evaluate((s) => { localStorage.setItem("token", s.d.token); localStorage.setItem("name", s.d.name); }, { d });
  for (const [route, steps] of flows) {
    try {
      // Dev-server page loads are heavy; a transient resource error on one
      // navigation must not lose the rest of the walk.
      await page.goto(route).catch(async () => { await page.waitForTimeout(1500); await page.goto(route); });
      await page.waitForTimeout(2500);
      for (const step of steps || []) {
        try { await step(page); await page.waitForTimeout(1200); } catch (e) { errs.push(`${role} ${route} step: ${e.message.split("\n")[0]}`); }
        await page.keyboard.press("Escape").catch(() => {}); await page.waitForTimeout(300);
      }
    } catch (e) { errs.push(`${role} ${route}: ${e.message.split("\n")[0]}`); }
  }
  await ctx.close();
}
const click = (text, opts = {}) => async (p) => { await p.getByText(text, opts).first().click(); };
const chain = (...fns) => async (p) => { for (const f of fns) { await f(p); await p.waitForTimeout(700); } };
await session("admin", "admin@user", "demo-admin-123", [
  ["/dashboard"],
  ["/node", [click("香港 HKT")]],
  ["/inbound", [click("香港 Reality"), chain(click(/^内核/)), chain(click("订阅源"))]],
  ["/relay", [click("上海移动 → 香港")]],
  ["/forward"], ["/tunnel"], ["/limit"], ["/user", [click("xiaoming")]], ["/group"], ["/panel-sharing"], ["/monitor"],
  ["/subscription", [
    click("小明 · 手机"),
    chain(click("订阅分配"), click("xiaoming")),
  ]],
  ["/config"],
  ["/profile"],
]);
await session("user", "xiaoming", "demo123456", [["/dashboard"], ["/my-subscription"], ["/forward"], ["/profile"]]);
fs.writeFileSync(out, JSON.stringify(rec));
const n = (r) => Object.keys(rec[r]).length;
console.log("admin paths", n("admin"), "user paths", n("user"));
console.log(errs.slice(0, 30).join("\n"));
await browser.close();
