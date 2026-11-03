#!/usr/bin/env node
// timeout-fe-extract.mjs — check-timeout-budgets.sh 的前端提取助手
// （修复轮 7，R7-26 / G-C4-09 机制处置）。
//
// 背景：超时门的前端提取旧实现是 grep/awk 文本首命中——
// helper_params 以 `grep -A10 '^const HELPER'` 取首命中窗口内的
// 第一个 Math.min/Math.max 数字：在 index.ts 顶部放一个模板字
// 符串、内含一行同名假 helper 定义（携登记值），真 helper 的下
// 限漂移即被假值遮蔽、九格全绿（PROBE7 实证）。FE_MAP 的 awk 跨
// 度提取（export 起止切分＋子串定位 Network./axios.＋首个
// timeout: 正则）同为文本面：字符串/注释里的形态可满足或遮蔽
// 提取。本助手改走 TypeScript 编译器 API 的真解析器：
//   - helper 参数：按顶层变量声明名定位（模板字符串里的同名文
//     本不是声明），从其初始化表达式的 Math.min/Math.max 调用
//     结构读 cap/floor；
//   - 调用点普查：按导出声明逐个遍历 AST，自首个 Network./axios.
//     调用点起找首个 timeout 属性赋值，按初始化表达式形态分类
//     （助手调用/Math.max/静态数值），与旧 fe_extract 输出同口径；
//   - network.ts 默认值：取 timeout ?? <数字> 的 ?? 右值（全点须
//     一致，不一致单独报 netdefault-multi 交门点名）；
//   - diagnosis-stream 常量：按具名顶层声明读初始化表达式。
// 静态数值求值只认数字字面量与 + - * / 括号组合；不可静态求值
// 的形态以 otherexpr 显式输出、由门按前提异常点名（fail-closed，
// 绝不静默归零）。
//
// 用法：node scripts/timeout-fe-extract.mjs <REPO_ROOT>
// 输出（TSV）：
//   netdefault \t <ms>            或 netdefault-multi \t v1,v2
//   helper \t <name> \t <capMs> \t <floorMs>
//   stream \t <ms>
//   call \t <name> \t <kind> \t <value>
//     kind = none|formula|dynamic|literal|otherexpr；formula 时
//     value 为 fe 口径助手名（batchCallTimeout 三助手同归
//     batchCallTimeout，与旧 glob 口径一致），dynamic/literal 时
//     value 为秒（%g 形态，与旧 eval_ms_expr 一致）。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const root = process.argv[2];
if (!root) {
  console.error("usage: timeout-fe-extract.mjs <REPO_ROOT>");
  process.exit(2);
}
const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

let ts = null;
for (const cand of [
  join(root, "vite-frontend/node_modules/typescript"),
  join(here, "../vite-frontend/node_modules/typescript"),
  "/home/hatch/workspace/flvx-max/vite-frontend/node_modules/typescript",
]) {
  try {
    ts = require(cand);
    break;
  } catch {
    // try next candidate
  }
}
if (!ts) {
  console.error("typescript not resolvable from any known location");
  process.exit(2);
}

const apiDir = join(root, "vite-frontend/src/api");
const parse = (path) =>
  ts.createSourceFile(
    path,
    readFileSync(path, "utf8"),
    ts.ScriptTarget.ES2020,
    true,
    ts.ScriptKind.TS,
  );

const unwrap = (e) => {
  while (e && ts.isParenthesizedExpression(e)) e = e.expression;
  return e;
};

// 静态数值求值：数字字面量（允许下划线分隔）与 + - * / 组合、
// 一元负号；其余形态返回 null。
const numEval = (e) => {
  e = unwrap(e);
  if (!e) return null;
  if (ts.isNumericLiteral(e)) {
    const v = Number(e.text.replace(/_/g, ""));
    return Number.isFinite(v) ? v : null;
  }
  if (
    ts.isPrefixUnaryExpression(e) &&
    e.operator === ts.SyntaxKind.MinusToken
  ) {
    const v = numEval(e.operand);
    return v === null ? null : -v;
  }
  if (ts.isBinaryExpression(e)) {
    const l = numEval(e.left);
    const r = numEval(e.right);
    if (l === null || r === null) return null;
    switch (e.operatorToken.kind) {
      case ts.SyntaxKind.AsteriskToken:
        return l * r;
      case ts.SyntaxKind.PlusToken:
        return l + r;
      case ts.SyntaxKind.MinusToken:
        return l - r;
      case ts.SyntaxKind.SlashToken:
        return r === 0 ? null : l / r;
      default:
        return null;
    }
  }
  return null;
};

// 与 awk printf "%g" 对齐的秒数格式（整数不带小数点）。
const fmtSec = (ms) => {
  const s = ms / 1000;
  return Number.isInteger(s) ? String(s) : String(parseFloat(s.toFixed(6)));
};

const out = [];
const indexSf = parse(join(apiDir, "index.ts"));

// 顶层声明索引（变量声明按名；模板/注释里的同名文本不是声明）。
const topVarDecls = new Map();
for (const st of indexSf.statements) {
  if (!ts.isVariableStatement(st)) continue;
  for (const d of st.declarationList.declarations) {
    if (ts.isIdentifier(d.name)) topVarDecls.set(d.name.text, d);
  }
}

// ── helper 参数：Math.min(cap, Math.max(floor, …)) 结构 ──────────
const HELPERS = [
  "batchCallTimeout",
  "batchCallTimeoutUpgrade",
  "batchCallTimeoutKernelApply",
  "latencyProbeTimeout",
  "relayProbeTimeout",
];
const isMathCall = (node, prop) =>
  ts.isCallExpression(node) &&
  ts.isPropertyAccessExpression(node.expression) &&
  ts.isIdentifier(node.expression.expression) &&
  node.expression.expression.text === "Math" &&
  node.expression.name.text === prop;

for (const name of HELPERS) {
  const decl = topVarDecls.get(name);
  if (!decl || !decl.initializer) continue;
  let minCall = null;
  const findMin = (node) => {
    if (minCall) return;
    if (isMathCall(node, "min")) {
      minCall = node;
      return;
    }
    ts.forEachChild(node, findMin);
  };
  findMin(decl.initializer);
  if (!minCall || minCall.arguments.length < 2) continue;
  const cap = numEval(minCall.arguments[0]);
  const floorArg = unwrap(minCall.arguments[1]);
  const floor =
    floorArg && isMathCall(floorArg, "max")
      ? numEval(floorArg.arguments[0])
      : null;
  if (cap === null || floor === null) continue;
  out.push(`helper\t${name}\t${cap}\t${floor}`);
}

// ── 调用点普查：导出声明 → 首个 timeout 形态 ─────────────────────
const HELPER_FE_NAME = new Map([
  ["batchCallTimeout", "batchCallTimeout"],
  ["batchCallTimeoutUpgrade", "batchCallTimeout"],
  ["batchCallTimeoutKernelApply", "batchCallTimeout"],
  ["latencyProbeTimeout", "latencyProbeTimeout"],
  ["relayProbeTimeout", "relayProbeTimeout"],
]);

const classifyTimeoutInit = (init) => {
  init = unwrap(init);
  if (ts.isCallExpression(init) && ts.isIdentifier(init.expression)) {
    const fe = HELPER_FE_NAME.get(init.expression.text);
    if (fe) return ["formula", fe];
  }
  if (isMathCall(init, "max")) {
    const v = numEval(init.arguments[0]);
    return v === null ? ["otherexpr", ""] : ["dynamic", fmtSec(v)];
  }
  const v = numEval(init);
  return v === null ? ["otherexpr", ""] : ["literal", fmtSec(v)];
};

for (const st of indexSf.statements) {
  const exported =
    ts.canHaveModifiers(st) &&
    (ts.getModifiers(st) ?? []).some(
      (m) => m.kind === ts.SyntaxKind.ExportKeyword,
    );
  if (!exported) continue;
  const decls = [];
  if (ts.isVariableStatement(st)) {
    for (const d of st.declarationList.declarations) {
      if (ts.isIdentifier(d.name)) decls.push([d.name.text, d]);
    }
  } else if (ts.isFunctionDeclaration(st) && st.name) {
    decls.push([st.name.text, st]);
  }
  for (const [name, declNode] of decls) {
    // 文档序收集子树节点，自首个 Network./axios. 调用起找首个
    // timeout 属性赋值（与旧 awk 的子串定位口径对齐）。
    const ordered = [];
    const collect = (n) => {
      ordered.push(n);
      ts.forEachChild(n, collect);
    };
    collect(declNode);
    const callIdx = ordered.findIndex(
      (n) =>
        ts.isCallExpression(n) &&
        ts.isPropertyAccessExpression(n.expression) &&
        ts.isIdentifier(n.expression.expression) &&
        (n.expression.expression.text === "Network" ||
          n.expression.expression.text === "axios"),
    );
    let kind = "none";
    let value = "";
    if (callIdx >= 0) {
      for (let i = callIdx; i < ordered.length; i++) {
        const n = ordered[i];
        if (
          ts.isPropertyAssignment(n) &&
          (ts.isIdentifier(n.name) || ts.isStringLiteral(n.name)) &&
          n.name.text === "timeout"
        ) {
          [kind, value] = classifyTimeoutInit(n.initializer);
          break;
        }
      }
    }
    out.push(`call\t${name}\t${kind}\t${value}`);
  }
}

// ── network.ts 默认 timeout：?? 右值全点一致 ─────────────────────
const netSf = parse(join(apiDir, "network.ts"));
const netValues = new Set();
const visitNet = (node) => {
  if (
    ts.isBinaryExpression(node) &&
    node.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken
  ) {
    const l = unwrap(node.left);
    const r = unwrap(node.right);
    const leftIsTimeout =
      (ts.isPropertyAccessExpression(l) && l.name.text === "timeout") ||
      (ts.isIdentifier(l) && l.text === "timeout");
    if (leftIsTimeout && ts.isNumericLiteral(r)) {
      netValues.add(Number(r.text.replace(/_/g, "")));
    }
  }
  ts.forEachChild(node, visitNet);
};
visitNet(netSf);
if (netValues.size === 1) {
  out.push(`netdefault\t${[...netValues][0]}`);
} else if (netValues.size > 1) {
  out.push(`netdefault-multi\t${[...netValues].join(",")}`);
}

// ── diagnosis-stream 常量＋导出名集 ──────────────────────────────
const streamSf = parse(join(apiDir, "diagnosis-stream.ts"));
for (const st of streamSf.statements) {
  if (!ts.isVariableStatement(st)) continue;
  const exported =
    ts.canHaveModifiers(st) &&
    (ts.getModifiers(st) ?? []).some(
      (m) => m.kind === ts.SyntaxKind.ExportKeyword,
    );
  for (const d of st.declarationList.declarations) {
    if (!ts.isIdentifier(d.name)) continue;
    if (d.name.text === "DIAGNOSIS_STREAM_TIMEOUT_MS") {
      const v = d.initializer ? numEval(d.initializer) : null;
      if (v !== null) out.push(`stream\t${v}`);
    }
    if (exported) out.push(`streamexport\t${d.name.text}`);
  }
}

console.log(out.join("\n"));
