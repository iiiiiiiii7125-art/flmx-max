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
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM, env: browserEnv, args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu", "--disable-features=LocalNetworkAccessChecks"] });
const rec = {};
const errs = [];
async function session(role, user, pass, flows) {
  const d = await login(user, pass);
  const ctx = await browser.newContext({ viewport: { width: 1366, height: 1000 }, baseURL: "http://127.0.0.1:3114" });
  // Seed the session via an init script instead of evaluating on the
  // login page: the login page clears the stored session on mount, which
  // races the evaluate and intermittently wipes the token before the walk
  // starts (the first, cold-cache session lost that race every time). An
  // init script re-seeds localStorage at document start on every
  // navigation, so each recorded page boots authenticated. Still only
  // token + name are written （我方会话模型 N-002): the role is parsed
  // from the JWT by the frontend.
  await ctx.addInitScript((s) => {
    try { localStorage.setItem("token", s.token); localStorage.setItem("name", s.name); } catch {}
  }, d);
  const page = await ctx.newPage(); page.setDefaultTimeout(20000);
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
  await page.goto("/").catch(() => {});
  for (const [route, steps] of flows) {
    try {
      // Dev-server page loads are heavy; a transient resource error on one
      // navigation must not lose the rest of the walk.
      await page.goto(route).catch(async () => { await page.waitForTimeout(1500); await page.goto(route); });
      // First-hit page loads are heavy and can stall for seconds;
      // 2.5s proved too tight in practice.
      await page.waitForTimeout(4500);
      for (const step of steps || []) {
        try { await step(page); await page.waitForTimeout(1200); } catch (e) { errs.push(`${role} ${route} step: ${e.message.split("\n")[0]}`); }
        await page.keyboard.press("Escape").catch(() => {}); await page.waitForTimeout(300);
      }
    } catch (e) { errs.push(`${role} ${route}: ${e.message.split("\n")[0]}`); }
  }
  await ctx.close();
}
const click = (text, opts = {}) => async (p) => { await p.getByText(text, opts).first().click(); };
// PageTabs render role="tab" buttons; locate tabs by role so a sidebar
// menu item with the same label （订阅） is never hit instead of the tab.
const tab = (name) => async (p) => { await p.getByRole("tab", { name: new RegExp(`^${name}`) }).first().click(); };
// The inbound page's tab switcher is a row of plain buttons inside
// <main> (no role="tab"); scope the text match to <main> so the sidebar
// menu item with the same label is not clicked instead.
const mainClick = (text, opts = {}) => async (p) => { await p.locator("main").getByText(text, opts).first().click(); };
// The assign view's user picker is a native <select> (the bridge Select
// renders one in single mode): its options are not clickable elements.
const pickUser = (label) => async (p) => { await p.locator("main select").first().selectOption({ label }); };
const chain = (...fns) => async (p) => { for (const f of fns) { await f(p); await p.waitForTimeout(700); } };
await session("admin", "admin@user", "demo-admin-123", [
  ["/dashboard"],
  // UI reorg (H): the kernel tab moved from /inbound to the node hub.
  ["/node", [click("香港 HKT"), chain(tab("内核"))]],
  ["/inbound", [click("香港 Reality"), chain(mainClick("订阅", { exact: true })), chain(mainClick("订阅源"))]],
  ["/relay", [click("上海移动 → 香港")]],
  ["/forward"], ["/tunnel"], ["/limit"], ["/user", [click("xiaoming")]], ["/group"], ["/panel-sharing"], ["/monitor"],
  ["/subscription", [
    click("小明 · 手机"),
    // The assign view's user picker is a native <select>: choosing the
    // user fires user-subscription/list for that user.
    chain(click("订阅分配"), pickUser("xiaoming")),
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
