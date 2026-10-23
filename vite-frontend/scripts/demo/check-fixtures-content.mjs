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
];

let problems = 0;
for (const { book, key, kind } of CHECKS) {
  const recordings = fixtures?.[book]?.[key];
  if (!Array.isArray(recordings) || recordings.length === 0) {
    problems += 1;
    console.log(`FAIL  ${book} "${key}": key missing or has no recordings`);
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
    ? `\nfixtures content: ${problems} empty key payload(s). Re-record with a seed that produces the fact — see scripts/demo/README.md.`
    : `\nfixtures content ok: every key on the content list carries non-empty data.`,
);
process.exit(problems ? 1 : 0);
