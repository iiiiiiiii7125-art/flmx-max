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
// Extraction (round-7 gatesmech2): declarations and endpoint type
// arguments are read from TypeScript ASTs, not from comment-stripped
// text. The round-6 lexical stripper has no regex-literal awareness,
// and src/api/index.ts line 143 contains an ordinary regex whose
// escaped slash plus closing delimiter form a literal // in the raw
// text — the stripper derails there (133 line comments survive
// "stripping" in that file), so interface bodies and Network call
// shapes read from the stripped text were evidence-compromised.
// AST nodes cannot be satisfied or hidden by comment/string text.
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
let ts;
try {
  ts = require("typescript");
} catch {
  // R4-C6-04: typescript resolves from the current worktree only.
  // The former hard-coded fallback into another checkout is gone:
  // gate results must not silently depend on a different tree's
  // dependency state. Point FLMX_CHECK_TYPESCRIPT at an external
  // copy explicitly if one is intended; it is named in the output.
  const envTs = process.env.FLMX_CHECK_TYPESCRIPT;

  if (envTs) {
    ts = require(envTs);
    console.log(
      `NOTE  typescript via FLMX_CHECK_TYPESCRIPT (outside this worktree): ${envTs} (v${ts.version})`,
    );
  }
}
if (!ts) {
  console.log(
    "FAIL  typescript not resolvable from this worktree; run pnpm install first, or set FLMX_CHECK_TYPESCRIPT=<path to typescript>",
  );
  process.exit(1);
}

const typesSf = ts.createSourceFile(
  "types.ts",
  fs.readFileSync(typesPath, "utf8"),
  ts.ScriptTarget.ES2020,
  true,
  ts.ScriptKind.TS,
);
const indexSf = ts.createSourceFile(
  "index.ts",
  fs.readFileSync(indexPath, "utf8"),
  ts.ScriptTarget.ES2020,
  true,
  ts.ScriptKind.TS,
);
const fixtures = JSON.parse(fs.readFileSync(fixturesPath, "utf8"));

// Declared top-level fields per interface / object type alias:
// Map name -> Map field -> required (no `?`). AST members are
// exactly the depth-0 fields the old line scanner read.
const declFields = (sf) => {
  const map = new Map();
  for (const st of sf.statements) {
    let members = null;
    if (ts.isInterfaceDeclaration(st)) {
      members = st.members;
    } else if (
      ts.isTypeAliasDeclaration(st) &&
      ts.isTypeLiteralNode(st.type)
    ) {
      members = st.type.members;
    }
    if (!members) continue;
    const fields = new Map();
    for (const mem of members) {
      if (ts.isPropertySignature(mem)) {
        fields.set(mem.name.text, !mem.questionToken);
      }
    }
    map.set(st.name.text, fields);
  }
  return map;
};
const typesDecls = declFields(typesSf);
const indexDecls = declFields(indexSf);

const requiredCache = new Map();
function requiredFields(name) {
  if (!requiredCache.has(name)) {
    // Dual source (R7X-007): types.ts first (the bulk of the wire
    // types), then index.ts, where the api layer declares its own
    // response types (SubTraffic, SubscriptionItem, LoginResponse, …).
    const fields = typesDecls.get(name) ?? indexDecls.get(name);
    requiredCache.set(
      name,
      fields === undefined
        ? null
        : [...fields].filter(([, req]) => req).map(([f]) => f),
    );
  }
  return requiredCache.get(name);
}

/** Endpoint -> response type expression, from Network.<T>("path") calls. */
function endpointTypes(sf, srcText) {
  const map = new Map();
  const visit = (node) => {
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression) &&
      ts.isIdentifier(node.expression.expression) &&
      node.expression.expression.text === "Network" &&
      ["get", "post", "put", "delete"].includes(node.expression.name.text) &&
      node.typeArguments?.length >= 1 &&
      node.arguments.length >= 1 &&
      ts.isStringLiteral(node.arguments[0])
    ) {
      const typeExpr = srcText.slice(
        node.typeArguments[0].getStart(),
        node.typeArguments[0].getEnd(),
      );
      map.set(node.arguments[0].text.replace(/^\//, ""), typeExpr);
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return map;
}
const endpoints = endpointTypes(indexSf, fs.readFileSync(indexPath, "utf8"));


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