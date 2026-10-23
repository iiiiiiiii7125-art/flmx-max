// Field-level shape drift check for the demo fixtures (R6 verdicts §6.2).
//
// Why: three audit rounds in a row (R5X-019, R6X-020, R6X-022) found the
// same family by hand — a new wire field lands in api/types.ts, the demo
// fixtures keep the old shape, and nothing fails until a human re-audits.
// This script makes the diff mechanical:
//
//   for every endpoint recorded in src/demo/fixtures.json, resolve the
//   response interface named by the api layer (src/api/index.ts), take
//   its REQUIRED (non-optional) top-level fields from its declaration,
//   and diff them against the key set of every recorded object for that
//   endpoint (array items, `{ items: [...] }` collections, or the data
//   object itself). Any required field missing from a recorded object
//   is reported by field name, fixtures key and book, and the script
//   exits non-zero.
//
// Scope: top-level fields only. Declarations are looked up in BOTH
// src/api/types.ts and src/api/index.ts (R7X-007): response types such
// as SubTraffic / SubscriptionItem are declared in index.ts itself, and
// the R6 §6.2 types.ts-only scope silently skipped every endpoint they
// serve — the SubTraffic.since drift recurred through that blind spot.
// Nested inline objects remain out of scope, as do `Record<string, …>`
// map payloads (their content is covered by the fixtures content check,
// check-fixtures-content.mjs — key presence there is a runtime map key,
// not a declared field).
//
// Usage:
//   node scripts/demo/check-shape-drift.mjs [frontendRoot]
//   frontendRoot defaults to the vite-frontend directory containing
//   this script. Exit code: 0 = no drift, 1 = drift (or input error).
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = path.resolve(
  process.argv[2] || path.join(path.dirname(fileURLToPath(import.meta.url)), "..", ".."),
);
const typesPath = path.join(frontendRoot, "src/api/types.ts");
const indexPath = path.join(frontendRoot, "src/api/index.ts");
const fixturesPath = path.join(frontendRoot, "src/demo/fixtures.json");

for (const p of [typesPath, indexPath, fixturesPath]) {
  if (!fs.existsSync(p)) {
    console.log(`FAIL  input missing: ${p}`);
    process.exit(1);
  }
}
const typesSrc = fs.readFileSync(typesPath, "utf8");
const indexSrc = fs.readFileSync(indexPath, "utf8");
const fixtures = JSON.parse(fs.readFileSync(fixturesPath, "utf8"));

/** Extract the brace-balanced body of `interface NAME { ... }` / `type NAME = { ... }`. */
function typeBody(src, name) {
  const m = new RegExp(`(?:export\\s+)?(?:interface|type)\\s+${name}\\b`).exec(src);
  if (!m) return null;
  const open = src.indexOf("{", m.index);
  if (open < 0) return null;
  let depth = 0;
  for (let i = open; i < src.length; i += 1) {
    if (src[i] === "{") depth += 1;
    else if (src[i] === "}") {
      depth -= 1;
      if (depth === 0) return src.slice(open + 1, i);
    }
  }
  return null;
}

/** Top-level fields of a type body: Map name -> required (no `?`). */
function topLevelFields(body) {
  const fields = new Map();
  let depth = 0;
  for (const line of body.split("\n")) {
    const s = line.trim();
    if (depth === 0) {
      const fm = /^(?:readonly\s+)?([A-Za-z_]\w*)(\?)?:/.exec(s);
      if (fm) fields.set(fm[1], fm[2] !== "?");
    }
    depth += (line.match(/\{/g) || []).length - (line.match(/\}/g) || []).length;
  }
  return fields;
}

const requiredCache = new Map();
function requiredFields(name) {
  if (!requiredCache.has(name)) {
    // Dual source (R7X-007): types.ts first (the bulk of the wire
    // types), then index.ts, where the api layer declares its own
    // response types (SubTraffic, SubscriptionItem, LoginResponse, …).
    const body = typeBody(typesSrc, name) ?? typeBody(indexSrc, name);
    if (body === null) {
      requiredCache.set(name, null);
    } else {
      const all = topLevelFields(body);
      requiredCache.set(name, [...all].filter(([, req]) => req).map(([f]) => f));
    }
  }
  return requiredCache.get(name);
}

/** Endpoint -> response type expression, from Network.<T>("path") calls. */
function endpointTypes(src) {
  const map = new Map();
  const re = /Network\.(?:get|post|put|delete)</g;
  let m;
  while ((m = re.exec(src)) !== null) {
    let i = re.lastIndex;
    let depth = 1;
    const start = i;
    while (i < src.length && depth > 0) {
      if (src[i] === "<") depth += 1;
      else if (src[i] === ">") depth -= 1;
      i += 1;
    }
    const typeExpr = src.slice(start, i - 1);
    const pm = /^\s*\(\s*"([^"]+)"/.exec(src.slice(i));
    if (pm) map.set(pm[1].replace(/^\//, ""), typeExpr);
    re.lastIndex = i;
  }
  return map;
}
const endpoints = endpointTypes(indexSrc);

/**
 * Which (interface, objects) pairs a recorded `data` payload must satisfy:
 *  - `Item[]` / `Item`            -> the items / the object itself
 *  - `{ items: Item[]; ... }`     -> data[prop] for each prop typed by a
 *                                    declared interface (either source)
 *  - `A | null`                   -> A
 * Anything else (inline anonymous types, primitives) is out of scope.
 */
function targetsFor(typeExpr, data) {
  const t = typeExpr.trim();
  const direct = /^([A-Z]\w*)\s*(\[\])?\s*(\|\s*null)?$/.exec(t);
  if (direct) {
    if (requiredFields(direct[1]) === null) return [];
    const objs = Array.isArray(data) ? data : [data];
    return [{ iface: direct[1], objs }];
  }
  if (t.startsWith("{")) {
    const out = [];
    const body = t.slice(1, t.lastIndexOf("}"));
    for (const [, prop, , iname] of body.matchAll(/(\w+)(\?)?:\s*([A-Z]\w*)(\[\])?/g)) {
      if (requiredFields(iname) === null) continue;
      if (data && typeof data === "object" && prop in data) {
        const v = data[prop];
        out.push({ iface: iname, objs: Array.isArray(v) ? v : [v] });
      }
    }
    return out;
  }
  return [];
}

let problems = 0;
let checked = 0;
let skipped = 0;
for (const [book, entries] of Object.entries(fixtures)) {
  if (!entries || typeof entries !== "object") continue;
  for (const [key, recordings] of Object.entries(entries)) {
    const typeExpr = endpoints.get(key.split("?")[0]);
    if (!typeExpr) {
      skipped += 1;
      continue;
    }
    for (const rec of recordings) {
      const data = rec && rec.res ? rec.res.data : undefined;
      for (const { iface, objs } of targetsFor(typeExpr, data)) {
        const required = requiredFields(iface);
        for (const obj of objs) {
          if (!obj || typeof obj !== "object" || Array.isArray(obj)) continue;
          checked += 1;
          for (const field of required) {
            if (!(field in obj)) {
              problems += 1;
              console.log(
                `FAIL  ${iface}.${field}: missing in fixtures ${book} "${key}" ` +
                  `(object id=${obj.id ?? "?"}), file: ${path.relative(process.cwd(), fixturesPath)}`,
              );
            }
          }
        }
      }
    }
  }
}

console.log(
  problems
    ? `\nshape drift: ${problems} missing required field(s) across ${checked} recorded object(s). ` +
        `Sync the fixtures (re-record or backfill) — see scripts/demo/README.md.`
    : `shape ok: ${checked} recorded object(s) cover every required field declared in types.ts / api/index.ts (${skipped} fixture key(s) out of scope).`,
);
process.exit(problems ? 1 : 0);
