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
await call(tok, "POST", "group/user/list");
await call(tok, "POST", "group/tunnel/list");
await call(tok, "POST", "group/permission/list");
const nodes = (await call(tok, "POST", "node/list")).data;
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
await ucall(utok, "POST", "config/get", { name: "forward_group_order_map" });
await ucall(utok, "POST", "config/get", { name: "forward_group_collapsed_map" });
await ucall(utok, "POST", "federation/share/list", {});
await ucall(utok, "POST", "federation/share/remote-usage/list", {});
rec.recordedAt = now;
fs.writeFileSync(file, JSON.stringify(rec));
console.log(Object.keys(rec.admin).length, "admin paths,", Object.keys(rec.user).length, "user paths");
