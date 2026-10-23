// Content-level check for the demo fixtures (R7X-006).
//
// Why: the fixtures reconciliation used to stop at key-set existence —
// every recorded key present, done. R6's full re-record passed that
// check while washing node/traffic/month back to an empty map: the
// seed never wrote node_traffic rows, so the seeded panel's honest
// answer was {}, and "key present" said nothing about it. The demo
// node page's 本月流量（网卡）slot silently went blank for every node.
//
// This check upgrades the reconciliation rule to "key set + key
// content non-empty" for the keys whose payload is a runtime map or
// series (declared-shape checks cannot see inside Record<string, …>
// payloads — that blind spot is exactly what check-shape-drift.mjs
// leaves to this script, see its header). The same assertions run at
// recording time inside build_fixtures.py, which refuses to write a
// fixtures file that fails them; this script is the anytime verifier
// for the committed fixtures (release gate: fixtures 保真).
//
// Key list (minimum set, R7-X-verdicts §4):
//   admin  node/traffic/month        every recording's data is a
//                                    non-empty node map (≥1 node id)
//   admin  subscription/traffic      every recording's data carries a
//   user   user/subscription/traffic non-empty points series
//
// R9 BINFRA (J O-1 通则, R9-X-verdicts §4) adds a universal recorded-
// code gate below: EVERY recorded response in EVERY book must carry
// res.code === 0, except the per-cell whitelist (13 cells, sourced
// from the R9-J §2.1 full-set scan). Adding or removing a cell
// requires a per-cell reason recorded next to the entry in
// build_fixtures.py's CODE_WHITELIST — whole-class exemptions (by
// path, by code, or by msg alone) are forbidden. Keep this list
// cell-for-cell equal to build_fixtures.py's.
//
// R8X-006 adds:
//   admin  kernel/versions           the xray / singbox recordings must
//                                    be a successful version list
//                                    (res.code === 0, data a non-empty
//                                    array of {version, …} entries) —
//                                    never a recorded failure response.
//                                    The 1.4.4 re-record froze a
//                                    transient GitHub 403 into both
//                                    cells (code -1, data null) and no
//                                    gate covered the key, so the demo
//                                    kernel dialog showed the error
//                                    verbatim. snell / realm recordings
//                                    under the same key are static lists
//                                    and stay out of this check's scope.
//
// Usage:
//   node scripts/demo/check-fixtures-content.mjs [frontendRoot]
//   frontendRoot defaults to the vite-frontend directory containing
//   this script. Exit code: 0 = all key content non-empty, 1 = fail.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = path.resolve(
  process.argv[2] || path.join(path.dirname(fileURLToPath(import.meta.url)), "..", ".."),
);
const fixturesPath = path.join(frontendRoot, "src/demo/fixtures.json");
if (!fs.existsSync(fixturesPath)) {
  console.log(`FAIL  input missing: ${fixturesPath}`);
  process.exit(1);
}
const fixtures = JSON.parse(fs.readFileSync(fixturesPath, "utf8"));

const CHECKS = [
  { book: "admin", key: "node/traffic/month", kind: "month-map" },
  { book: "admin", key: "subscription/traffic", kind: "series" },
  { book: "user", key: "user/subscription/traffic", kind: "series" },
  { book: "admin", key: "kernel/versions", kind: "version-list" },
];

// kernel/versions recordings whose payload comes from the live GitHub
// release list (R8X-006). snell / realm use pinned static lists.
const VERSION_LIST_TYPES = ["xray", "singbox"];

// Universal recorded-code gate (R9 BINFRA, J O-1 通则): per-cell
// whitelist of intentional non-zero recordings — [book, key(path),
// canonical request key, code, msg]. The per-cell reasons live in
// build_fixtures.py's CODE_WHITELIST comments; the two lists must
// stay cell-for-cell equal. Cells: admin config/get 配置不存在 ×3,
// user config/get 配置不存在 ×4, user-book 403 permission probes ×6.
const CODE_WHITELIST = [
  ["admin", "config/get", '{"name":"forward_group_collapsed_map"}', -1, "配置不存在"],
  ["admin", "config/get", '{"name":"forward_group_order_map"}', -1, "配置不存在"],
  ["admin", "config/get", '{"name":"forward_compact_mode"}', -1, "配置不存在"],
  ["user", "config/get", '{"name":"forward_compact_mode"}', -1, "配置不存在"],
  ["user", "config/get", '{"name":"monitor_tunnel_quality_enabled"}', -1, "配置不存在"],
  ["user", "config/get", '{"name":"monitor_tunnel_quality_interval_sec"}', -1, "配置不存在"],
  ["user", "config/get", '{"name":"forward_group_collapsed_map"}', -1, "配置不存在"],
  ["user", "system/check-updates", '{"channel":"stable"}', 403, "权限不足，仅管理员可操作"],
  ["user", "speed-limit/list", "{}", 403, "权限不足，仅管理员可操作"],
  ["user", "tunnel/list", "{}", 403, "权限不足，仅管理员可操作"],
  ["user", "node/list", "{}", 403, "权限不足，仅管理员可操作"],
  ["user", "federation/share/list", "{}", 403, "权限不足，仅管理员可操作"],
  ["user", "federation/share/remote-usage/list", "{}", 403, "权限不足，仅管理员可操作"],
];
const CODE_WHITELIST_SET = new Set(CODE_WHITELIST.map((c) => JSON.stringify(c)));

let problems = 0;
let codeProblems = 0;
let codeCells = 0;
for (const book of ["admin", "user"]) {
  for (const [key, recordings] of Object.entries(fixtures?.[book] ?? {})) {
    if (!Array.isArray(recordings)) continue;
    recordings.forEach((rec, i) => {
      const res = rec && rec.res ? rec.res : {};
      if (res.code === 0) return;
      codeCells += 1;
      const cell = JSON.stringify([book, key, rec?.key ?? "", res.code, res.msg ?? ""]);
      if (!CODE_WHITELIST_SET.has(cell)) {
        problems += 1;
        codeProblems += 1;
        console.log(`FAIL  ${book} "${key}" recording #${i} key=${rec?.key ?? ""}: res.code=${res.code} msg=${JSON.stringify(res.msg ?? "")} is not code 0 and not a whitelisted cell (per-cell whitelist only — see CODE_WHITELIST)`);
      }
    });
  }
}
console.log(`code gate: ${codeCells} non-zero recording(s) against the ${CODE_WHITELIST.length}-cell whitelist: ${codeProblems === 0 ? "ok" : `${codeProblems} cell(s) outside the whitelist — see FAIL lines above`}`);
for (const { book, key, kind } of CHECKS) {
  const recordings = fixtures?.[book]?.[key];
  if (!Array.isArray(recordings) || recordings.length === 0) {
    problems += 1;
    console.log(`FAIL  ${book} "${key}": key missing or has no recordings`);
    continue;
  }
  if (kind === "version-list") {
    const seen = new Set();
    recordings.forEach((rec, i) => {
      let kernelType = "";
      try {
        kernelType = JSON.parse(rec?.key || "{}").kernelType || "";
      } catch {
        kernelType = "";
      }
      if (!VERSION_LIST_TYPES.includes(kernelType)) {
        console.log(`skip  ${book} "${key}" recording #${i}: kernelType=${kernelType || "?"} (static list, out of scope)`);
        return;
      }
      seen.add(kernelType);
      const res = rec && rec.res ? rec.res : {};
      const data = res.data;
      if (res.code !== 0) {
        problems += 1;
        console.log(`FAIL  ${book} "${key}" recording #${i} (kernelType=${kernelType}): res.code=${res.code} is a recorded failure response (msg=${JSON.stringify(res.msg ?? "")}), not a version list`);
        return;
      }
      if (!Array.isArray(data) || data.length < 1) {
        problems += 1;
        console.log(`FAIL  ${book} "${key}" recording #${i} (kernelType=${kernelType}): data is not a non-empty version list`);
        return;
      }
      const bad = data.findIndex((v) => !v || typeof v.version !== "string" || v.version === "");
      if (bad >= 0) {
        problems += 1;
        console.log(`FAIL  ${book} "${key}" recording #${i} (kernelType=${kernelType}): data[${bad}] has no non-empty version string`);
        return;
      }
      console.log(`ok    ${book} "${key}" recording #${i} (kernelType=${kernelType}): code 0, ${data.length} version(s), first=${data[0].version}`);
    });
    for (const t of VERSION_LIST_TYPES) {
      if (!seen.has(t)) {
        problems += 1;
        console.log(`FAIL  ${book} "${key}": no recording for kernelType=${t}`);
      }
    }
    continue;
  }
  recordings.forEach((rec, i) => {
    const data = rec && rec.res ? rec.res.data : undefined;
    if (kind === "month-map") {
      const n = data && typeof data === "object" && !Array.isArray(data) ? Object.keys(data).length : 0;
      if (n < 1) {
        problems += 1;
        console.log(`FAIL  ${book} "${key}" recording #${i}: data is an empty node map (0 nodes)`);
      } else {
        console.log(`ok    ${book} "${key}" recording #${i}: data covers ${n} node(s)`);
      }
    } else {
      const pts = data && typeof data === "object" && Array.isArray(data.points) ? data.points.length : 0;
      if (pts < 1) {
        problems += 1;
        console.log(`FAIL  ${book} "${key}" recording #${i}: data.points is empty`);
      } else {
        console.log(`ok    ${book} "${key}" recording #${i}: data.points has ${pts} point(s)`);
      }
    }
  });
}

console.log(
  problems
    ? `\nfixtures content: ${problems} failing key payload(s). Re-record with a seed that produces the fact — see scripts/demo/README.md.`
    : `\nfixtures content ok: every key on the content list carries non-empty data.`,
);
process.exit(problems ? 1 : 0);
