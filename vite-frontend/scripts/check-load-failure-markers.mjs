// check-load-failure-markers.mjs — IRL-3 初载失败标记门禁。
//
// 机制（R2-C3-01）：任一初载列表请求失败时，页面仅弹一次 toast
// （数秒即消失），随后按 `list.length === 0` 渲染与「本就没数
// 据」逐字相同的空态——无持久失败标记、无重试入口、无已加载／
// 加载失败的状态区分，故障态与真空态事后不可区分。首轮审计的
// 普查是页面清单驱动（只覆盖 7 面），R2 换加载点普查驱动——从
// API 调用点反推每个初载列表的四态（loading/error/empty/data），
// 才挖出同族新成员 8 面。本门把加载点普查方法沉淀为常设门：
//
//   1. 普查：按文件枚举自 `@/api` 与 `@/config/site` 的具名导入
//      （别名取本地名，`type` 导入剔除），再按调用点
//      `\bName\s*\(` 全集枚举（C3 原法；另补三元分发形态
//      `(cond ? fnA : fnB)(args)`——C3 普查器漏此形态，
//      traffic-stats.tsx:103 为证）；
//   2. 登记表 `load-failure-markers.json` 以（file, apiFn）为键，
//      登记每个初载列表面及其失败标记标识符（fixed 者标记必须
//      在场、missing 者期望标记必须仍缺席）；另有 exempt 者：
//      经逐项裁定的谓词外调用点（picker／aux 数据源、动作型
//      明细加载、权限探针等），不设标记断言，但必须写明机制
//      理由（reason 非空），锚点同样不得腐烂——静默豁免不许；
//   3. 双向断言：登记为 fixed 者标记缺席即红（回归）；登记为
//      missing 者标记出现即红（修复落地未同步登记，须同提交把
//      本条翻为 fixed）；登记项在普查中消失（文件改名／apiFn
//      改名／调用点删除）即红（登记腐烂）；
//   4. 普查基数下限：调用点总数／文件数／不同 apiFn 数设 sanity
//      下限（现行实数见末尾日志），普查口径损坏致基数缩水即红，
//      门不得以小集合假绿。
//
// 覆盖边界（明示）：登记表覆盖初载列表面（fixed）与逐项裁定
// 的谓词外同批调用点（exempt，理由见登记表）；其余调用点多为
// 动作调用（创建／删除／探测／保存等），其失败 UX 是动作上的
// toast，本就不适用持久标记，不入登记。
// 标记存在性判定剥除注释后按整词匹配（注释内提及不计数——
// S3 记 modal:48 注释行假阳性同族）；字符串字面量内的提及计
// 入（标记名只出现在文案里仍算在场，属宽松口径、由行为审计兜
// 底）。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-load-failure-markers.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
// 本脚本由 run-checks.mjs 按目录自动入册，无需别处登记。
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const srcRoot = join(here, "..", "src");
const registry = JSON.parse(
  readFileSync(join(here, "load-failure-markers.json"), "utf8"),
);

// 普查基数下限（2026-11-10 立门时实测：调用点 226、文件 47、
// 不同 apiFn 161；与 C3 普查原文 216／42／156 的对账：本门普
// 查是 C3 的严格超集——差 10 行＝@/config/site 8 行（config.tsx
// 的 getCachedConfigs 在列，另 App.tsx／brand-logo／navbar／
// subscription-front.ts 4 文件）＋三元分发 2 行
// （traffic-stats.tsx:103 两分支）。下限取实数约九成，留正常
// 增减余量；口径损坏致基数塌缩即红）。
const FLOOR_CALL_SITES = 190;
const FLOOR_FILES = 38;
const FLOOR_API_FNS = 140;

let failures = 0;
const check = (label, ok, detail = "") => {
  if (ok) {
    console.log(`PASS  ${label}`);
  } else {
    failures++;
    console.log(`FAIL  ${label} — ${detail}`);
  }
};

const escapeRegExp = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// ── 普查 ──────────────────────────────────────────────
// 每文件：具名导入（@/api 与 @/config/site，别名取本地名）＋
// 调用点 `\bName\s*\(`＋三元分发 `(cond ? fnA : fnB)(args)`。
// import 语句行不计入调用点（多行 import 的起止行按字符偏移算）。
function censusFile(relPath) {
  const text = readFileSync(join(srcRoot, relPath), "utf8");
  const importLineSet = new Set();
  const localNames = new Set();
  const importRe =
    /import\s*\{([^}]*)\}\s*from\s*["'](@\/api|@\/config\/site)["']/g;
  for (const m of text.matchAll(importRe)) {
    const startLine = text.slice(0, m.index).split("\n").length;
    const endLine = text.slice(0, m.index + m[0].length).split("\n").length;
    for (let l = startLine; l <= endLine; l++) importLineSet.add(l);
    for (const item of m[1].split(",")) {
      const part = item.trim();
      if (!part || part.startsWith("type ")) continue;
      const local = part.split(/\s+as\s+/)[1] ?? part.split(/\s+/)[0];
      if (local) localNames.add(local.trim());
    }
  }
  if (localNames.size === 0) return [];
  const sites = [];
  const lines = text.split("\n");
  lines.forEach((line, idx) => {
    const lineNo = idx + 1;
    if (importLineSet.has(lineNo)) return;
    for (const name of localNames) {
      const esc = escapeRegExp(name);
      if (new RegExp(`\\b${esc}\\s*\\(`).test(line)) {
        sites.push({ file: `src/${relPath}`, line: lineNo, apiFn: name, form: "call" });
      } else if (
        new RegExp(`\\?\\s*${esc}\\s*:|:\\s*${esc}\\s*\\)\\s*\\(`).test(line)
      ) {
        sites.push({ file: `src/${relPath}`, line: lineNo, apiFn: name, form: "dispatch" });
      }
    }
  });
  return sites;
}

function walk(dir, rel) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    const relPath = rel ? `${rel}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      out.push(...walk(path, relPath));
    } else if (/\.tsx?$/.test(entry.name)) {
      out.push(...censusFile(relPath));
    }
  }
  return out;
}

const sites = walk(srcRoot, "");
const siteKeys = new Set(sites.map((s) => `${s.file}\t${s.apiFn}`));
const distinctFiles = new Set(sites.map((s) => s.file)).size;
const distinctFns = new Set(sites.map((s) => s.apiFn)).size;

check(
  `加载点普查基数（调用点 ${sites.length} ≥ ${FLOOR_CALL_SITES}、文件 ${distinctFiles} ≥ ${FLOOR_FILES}、不同 apiFn ${distinctFns} ≥ ${FLOOR_API_FNS}）`,
  sites.length >= FLOOR_CALL_SITES &&
    distinctFiles >= FLOOR_FILES &&
    distinctFns >= FLOOR_API_FNS,
  `实测调用点 ${sites.length}、文件 ${distinctFiles}、apiFn ${distinctFns}`,
);

// 三元分发形态锚点：C3 普查器漏计的 traffic-stats.tsx:103 必须
// 在本普查内（形态覆盖退化即红）。
{
  const dispatchAnchor = sites.find(
    (s) =>
      s.file === "src/pages/subscription/traffic-stats.tsx" &&
      s.apiFn === "getSubscriptionTraffic" &&
      s.form === "dispatch",
  );
  check(
    "三元分发形态覆盖锚点（traffic-stats.tsx getSubscriptionTraffic 以 dispatch 形态入普查）",
    Boolean(dispatchAnchor),
    "普查未命中该分发点——分发形态覆盖已退化",
  );
}

// ── 标记存在性（剥除注释后整词匹配）────────────────────
function stripComments(src) {
  const out = [];
  const n = src.length;
  let i = 0;
  let quote = null;
  while (i < n) {
    const c = src[i];
    if (quote) {
      out.push(c);
      if (c === "\\") {
        out.push(src[i + 1] ?? "");
        i += 2;
        continue;
      }
      if (c === quote) quote = null;
      i++;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      quote = c;
      out.push(c);
      i++;
      continue;
    }
    if (c === "/" && src[i + 1] === "/") {
      while (i < n && src[i] !== "\n") i++;
      continue;
    }
    if (c === "/" && src[i + 1] === "*") {
      const end = src.indexOf("*/", i + 2);
      const stop = end === -1 ? n : end + 2;
      const chunk = src.slice(i, stop);
      out.push(chunk.replace(/[^\n]/g, " "));
      i = stop;
      continue;
    }
    out.push(c);
    i++;
  }
  return out.join("");
}

const markerPresentCache = new Map();
function markerPresent(file, marker) {
  const key = `${file}\t${marker}`;
  if (markerPresentCache.has(key)) return markerPresentCache.get(key);
  const text = readFileSync(join(here, "..", file), "utf8");
  const found = new RegExp(`\\b${escapeRegExp(marker)}\\b`).test(
    stripComments(text),
  );
  markerPresentCache.set(key, found);
  return found;
}

// ── 登记表双向断言 ─────────────────────────────────────
{
  const surfaces = registry.surfaces ?? [];
  const seenKeys = new Set();
  let dup = null;
  for (const s of surfaces) {
    const key = `${s.file}\t${s.apiFn}`;
    if (seenKeys.has(key)) dup = key;
    seenKeys.add(key);
  }
  check("登记表无重复（file, apiFn）键", dup === null, dup ? `重复键 ${dup}` : "");

  for (const s of surfaces) {
    const key = `${s.file}\t${s.apiFn}`;
    const inCensus = siteKeys.has(key);
    check(
      `登记锚：${s.file} 的 ${s.apiFn} 在普查内`,
      inCensus,
      "登记项已从普查消失（文件改名／apiFn 改名／调用点删除）——登记腐烂，须同步更新",
    );
    if (!inCensus) continue;
    if (s.status === "exempt") {
      check(
        `豁免登记带机制理由：${s.file} ${s.apiFn}`,
        typeof s.reason === "string" && s.reason.trim().length > 0,
        "exempt 登记必须写明机制理由，不得静默豁免",
      );
      continue;
    }
    const present = markerPresent(s.file, s.marker);
    if (s.status === "fixed") {
      check(
        `已修面标记在场：${s.file} ${s.marker}`,
        present,
        `标记缺席——已修面回归：${s.note ?? ""}`,
      );
    } else if (s.status === "missing") {
      check(
        `未修面标记仍缺席：${s.file} 期望 ${s.marker}`,
        !present,
        `标记已出现——修复已落地但登记未翻为 fixed，须同提交更新登记表：${s.note ?? ""}`,
      );
    } else {
      check(`登记状态合法：${s.file} ${s.apiFn}`, false, `未知 status ${s.status}`);
    }
  }
  console.log(
    `\n加载点普查：${sites.length} 调用点、${distinctFiles} 文件、${distinctFns} 不同 apiFn；登记 ${surfaces.length} 面（fixed ${surfaces.filter((s) => s.status === "fixed").length}／missing ${surfaces.filter((s) => s.status === "missing").length}／exempt ${surfaces.filter((s) => s.status === "exempt").length}）`,
  );
}

if (failures > 0) {
  console.log(`\n${failures} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
