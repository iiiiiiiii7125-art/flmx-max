// Item-level endpoints the page walk doesn't reach, called directly and
// merged into the raw recording (see README.md). Same recording-source
// discipline as record_pages.mjs: seeded throwaway panel only.
import fs from "node:fs";
const B = "http://127.0.0.1:16370/api/v1";
const file = process.argv[2];
const rec = JSON.parse(fs.readFileSync(file, "utf8"));
const login = (u, p) => fetch(B + "/user/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ username: u, password: p }) }).then(r => r.json()).then(r => r.data.token);
const tok = await login("admin@user", "demo-admin-123");
const callAs = (book) => async (token, method, path, body) => {
  const res = await fetch(B + "/" + path, { method, headers: { Authorization: token, "Content-Type": "application/json" }, body: method === "POST" ? JSON.stringify(body ?? {}) : undefined }).then(r => r.json());
  const bodyText = method === "POST" ? JSON.stringify(body ?? {}) : "";
  (book[path] ||= []);
  if (!book[path].some((e) => e.body === bodyText)) book[path].push({ body: bodyText, res });
  return res;
};
const call = callAs(rec.admin);
await call(tok, "POST", "kernel/capabilities");
// R15-X-02: the config page mounts <GistBackup/> unconditionally for
// admins and calls backup/gist/status on mount; without a direct
// recording the demo adapter warns "[demo] not recorded" and the
// config walk fails. Record the seed panel's honest answer (no token
// configured) like every other item-level endpoint here.
await call(tok, "POST", "backup/gist/status", {});
await call(tok, "POST", "group/user/list");
await call(tok, "POST", "group/tunnel/list");
await call(tok, "POST", "group/permission/list");
const nodes = (await call(tok, "POST", "node/list")).data;
// R5X-007: the node page calls this unconditionally on mount; without a
// recording the demo adapter warns "[demo] not recorded" and CI fails.
await call(tok, "POST", "node/traffic/month", { tzOffset: -480 });
const inbounds = (await call(tok, "POST", "inbound/list")).data;
const kernels = (await call(tok, "POST", "kernel/list")).data;
const users = (await call(tok, "POST", "user/list", {})).data;
const now = Date.now();
for (const n of nodes) {
  for (const hours of [24, 72, 168]) await call(tok, "POST", "node/latency/history", { id: n.id, hours });
  await call(tok, "POST", "node/route", { id: n.id });
  await call(tok, "GET", `monitor/nodes/${n.id}/metrics?start=${now - 86400000}&end=${now}`);
}
for (const i of inbounds) { await call(tok, "POST", "inbound/share", { id: i.id }); await call(tok, "POST", "inbound/relay/list", { inboundId: i.id }); }
for (const k of (Array.isArray(kernels) ? kernels : kernels.items ?? [])) await call(tok, "POST", "kernel/logs", { id: k.id });
for (const u of users) {
  await call(tok, "POST", "user/groups", { id: u.id });
  await call(tok, "POST", "tunnel/user/list", { userId: u.id });
  await call(tok, "POST", "user-subscription/list", { userId: u.id });
}
await call(tok, "GET", "monitor/tunnels"); await call(tok, "GET", "monitor/tunnels/quality");
await call(tok, "GET", "monitor/permission/list");
await call(tok, "POST", "inbound/sub/info", { reset: false });
// R3X-009 (D-1): monitor/access for the admin book. The monitor page now
// asks for it under every role; the value must be the panel's real admin
// answer ({allowed:true, admin:true}) — never a copy of the user book's
// need_admin_grant entry.
await call(tok, "GET", "monitor/access");
// R3X-032 latent interactive reads (a–k): endpoints the page walk only
// reaches after a click/tab switch, recorded here by direct call.
// (a) tunnel detail metrics + (b) tunnel quality history, per tunnel.
const demoTunnels = (await call(tok, "GET", "monitor/tunnels")).data ?? [];
for (const t of demoTunnels) {
  await call(tok, "GET", `monitor/tunnels/${t.id}/metrics?start=${now - 86400000}&end=${now}`);
  await call(tok, "GET", `monitor/tunnels/${t.id}/quality?start=${now - 3600000}&end=${now}`);
}
// (c) per-monitor result history.
const demoServices = (await call(tok, "GET", "monitor/services")).data ?? [];
for (const s of demoServices) await call(tok, "GET", `monitor/services/${s.id}/results?limit=100`);
// R6X-022: MonitorView's remaining reads (node list, latest results,
// limits) previously reached the admin book only when the page walk's
// timing happened to open a node's monitor detail — an intermittent
// recording source. Record them directly like every other item-level
// endpoint here; the user book gets the same set in its section below.
await call(tok, "GET", "monitor/nodes");
await call(tok, "GET", "monitor/services/latest-results");
await call(tok, "GET", "monitor/services/limits");
// (d) kernel version lists, one per kernel type the panel implements.
const caps = (await call(tok, "POST", "kernel/capabilities")).data;
for (const k of caps?.kernels ?? []) await call(tok, "POST", "kernel/versions", { kernelType: k.type });
// (e) node release lists for both channels the upgrade dialog offers.
await call(tok, "POST", "node/releases", { channel: "stable" });
await call(tok, "POST", "node/releases", { channel: "dev" });
// (f) latency-targets save: the adapter classifies the bare segment
// "targets" as a read, so the endpoint needs a recorded answer; the body
// mirrors the dialog's "follow defaults" save for the first node.
if (nodes?.length) await call(tok, "POST", "node/latency/targets", { ids: [nodes[0].id], custom: false, points: [], pinned: [] });
// (g)/(h) forward/tunnel diagnose are NOT recorded on purpose: a seeded
// panel has no agents, so a recorded POST would be a failure payload.
// The demo adapter synthesizes diagnosis results instead (see
// src/demo/index.ts, DIAGNOSES).
// (i) subscription preview, using the first recorded subscription as the
// draft (same field set the editor's toPayload sends).
const subList = (await call(tok, "POST", "subscription/list")).data;
const sub0 = subList?.items?.[0];
if (sub0) {
  const { id, name, kind, sourceAll, inboundIds, sourceIds, options, expireTime, enabled, remark, trafficLimit, resetDay } = sub0;
  await call(tok, "POST", "subscription/preview", { id, name, kind, sourceAll, inboundIds, sourceIds, options, expireTime, enabled, remark, trafficLimit, resetDay });
  // (j, admin half) subscription traffic for the traffic-bearing demo
  // subscription, in all three ranges the stats view can switch to.
  const trafficSub = subList.items.find((s) => s.uploadBytes > 0) ?? sub0;
  for (const range of ["hour", "day", "month"])
    await call(tok, "POST", "subscription/traffic", { id: trafficSub.id, range, tzOffset: -480, tz: "Asia/Shanghai" });
}
// (k) passkey list: PasskeyManager only calls it after passkey/status
// reports enabled (recorded disabled in the demo, so the demo UI never
// fires it — conditional coverage), but the endpoint itself does not
// gate on enabled and answers a real empty list; record it for both
// books so the key exists if a future re-record enables passkeys.
await call(tok, "POST", "user/passkey/list");
// The user book: 我的订阅 is built from these three calls (our model).
const utok = await login("xiaoming", "demo123456");
const ucall = callAs(rec.user);
await ucall(utok, "POST", "user-subscription/mine", {});
await ucall(utok, "POST", "user-inbound/mine", {});
await ucall(utok, "POST", "inbound/sub/info", { reset: false });
// Shared reads the user pages make (forward/profile): they must exist in
// the user book too — the adapter never falls back to the admin book.
await ucall(utok, "POST", "forward/list", {});
await ucall(utok, "POST", "tunnel/list", {});
await ucall(utok, "POST", "node/list", {});
await ucall(utok, "POST", "speed-limit/list", {});
await ucall(utok, "POST", "tunnel/user/tunnel");
await ucall(utok, "POST", "user/passkey/status");
// R3X-031 (D-2): re-record monitor/access for the user book so the entry
// carries the current response shape instead of the pre-R2X-038
// {allowed, reason} shape that only rendered correctly by default.
// R6X-022 updates the expected shape: the seed now grants the demo user
// monitoring permission (seed_2), so this records the real authorized
// answer ({allowed:true, admin:false}) — the value R5X-008 could only
// hand-edit into fixtures.
await ucall(utok, "GET", "monitor/access");
// R6X-022: the user book's full monitor set, mirroring the admin book's
// direct calls above. R5X-008's user monitor coverage existed only as
// hand-copied fixtures entries; without these calls a full re-record
// dropped every key below and check.mjs's user monitor walk went red.
// Same windows as the admin calls (now-based) so each path carries the
// canonical 24h / 1h entry the demo adapter falls back to.
await ucall(utok, "GET", "monitor/nodes");
for (const n of nodes) {
  await ucall(utok, "GET", `monitor/nodes/${n.id}/metrics?start=${now - 86400000}&end=${now}`);
}
const userTunnels = (await ucall(utok, "GET", "monitor/tunnels")).data ?? [];
await ucall(utok, "GET", "monitor/tunnels/quality");
for (const t of userTunnels) {
  await ucall(utok, "GET", `monitor/tunnels/${t.id}/metrics?start=${now - 86400000}&end=${now}`);
  await ucall(utok, "GET", `monitor/tunnels/${t.id}/quality?start=${now - 3600000}&end=${now}`);
}
const userServices = (await ucall(utok, "GET", "monitor/services")).data ?? [];
await ucall(utok, "GET", "monitor/services/latest-results");
await ucall(utok, "GET", "monitor/services/limits");
for (const s of userServices) await ucall(utok, "GET", `monitor/services/${s.id}/results?limit=100`);
// R3X-032 (j, user half): my-subscription traffic, for the subscriptions
// assigned to the demo user, in all three ranges.
const mySubs = (await ucall(utok, "POST", "user-subscription/mine", {})).data?.subscriptions ?? [];
for (const s of mySubs)
  for (const range of ["hour", "day", "month"])
    await ucall(utok, "POST", "user/subscription/traffic", { id: s.id, range, tzOffset: -480, tz: "Asia/Shanghai" });
// R3X-032 (k, user half): passkey list — see the admin-book note above.
await ucall(utok, "POST", "user/passkey/list");
await ucall(utok, "POST", "config/get", { name: "forward_group_order_map" });
await ucall(utok, "POST", "config/get", { name: "forward_group_collapsed_map" });
await ucall(utok, "POST", "federation/share/list", {});
await ucall(utok, "POST", "federation/share/remote-usage/list", {});
rec.recordedAt = now;
fs.writeFileSync(file, JSON.stringify(rec));
console.log(Object.keys(rec.admin).length, "admin paths,", Object.keys(rec.user).length, "user paths");
