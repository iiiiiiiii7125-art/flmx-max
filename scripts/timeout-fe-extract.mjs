#!/usr/bin/env node
// timeout-fe-extract.mjs — check-timeout-budgets.sh 的前端提取助手
// （修复轮 7，R7-26 / G-C4-09 机制处置；修复轮 8，R8-05 /
// H-C4-04 绑定建模补全）。
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
//   - 调用点普查：按导出声明逐个遍历 AST，在声明内全部
//     Network./axios. 调用点各自的实参内按 timeout 属性赋值取证
//     后合并（修复轮 9 起，见下），按初始化表达式形态分类
//     （助手调用/Math.max/静态数值），与旧 fe_extract 输出同口径；
//   - network.ts 默认值：取 timeout ?? <数字> 的 ?? 右值（全点须
//     一致，不一致单独报 netdefault-multi 交门点名）；
//   - diagnosis-stream 常量：按具名顶层声明读初始化表达式。
// 静态数值求值只认数字字面量与 + - * / 括号组合；不可静态求值
// 的形态以 otherexpr 显式输出、由门按前提异常点名（fail-closed，
// 绝不静默归零）。
//
// 绑定口径（修复轮 8，R8-05 / H-C4-04）：轮 7 的 AST 提取仍有三
// 处「按文本/按名取首命中」的残余，均可被同文件内的诱饵形态遮
// 蔽真值（第八轮探针 tfe-t1/t3/t4 实证）：
//   ① helper 定义改为数据流绑定：cap/floor 只读「流向 return 的
//      表达式」的最外层 Math.min/Math.max 结构——块体内的诱饵局
//      部量（携登记值）不再入选；多 return 且取值不一致、或结果
//      表达式内 Math.min 不唯一时不输出该 helper 行，门以「参数
//      无法提取」点名（fail-closed）；
//   ② 调用点 timeout 改为调用内绑定：只在该 Network./axios. 调
//      用自身的实参对象字面量内找 timeout 属性（计算键字符串字
//      面量同认；计算键非字面量、或配置对象含展开元素 → 该调用
//      记 otherexpr）；配置以标识符传参时按作用域解析到其声明
//      的对象字面量。调用之外的同名属性（如同函数体内的诱饵对
//      象）不再冒充调用的 timeout；
//   ③ 「公式助手」分类改为声明绑定：timeout 实参的标识符调用只
//      有在作用域解析（局部声明/形参/导入均构成遮蔽）后确为本
//      文件顶层 helper 声明时才记 formula；同名遮蔽/跨文件同名
//      一律落 otherexpr 交门点名。另：export { x } 列表形态导出
//      的同文件声明同入普查（此前脱集、靠格点查找失败兜底）。
//
// 绑定口径续（修复轮 9，R9-23/24/25 / I-C4-08/09/10）：轮 8 的
// 绑定建模仍有三处「首个/首参」残余，第九轮变异实证均可逃逸：
//   ④ 调用点普查改全调用合并：censusDecl 旧实现只取声明内 AST
//      先序首个 Network./axios. 调用取证，同导出的第二个调用
//      （哪怕是被 return 的生效调用）永不入普查——诱饵首调用携
//      登记公式、真调用携 999s 字面量时提取与基线逐字相同。现
//      逐调用取证后合并：无取证的调用不稀释；具体证据去重后
//      唯一则取之；两种以上具体证据（同声明内 timeout 分歧、
//      登记表一行无从对账）或任一调用 otherexpr → 整声明记
//      otherexpr 交门点名（fail-closed）。
//   ⑤ 配置标识符解析落同一作用域纪律：旧走查不认形参绑定——
//      形参遮蔽顶层同名对象时仍回落 topDecls 取证（运行时生效
//      的是形参默认值、提取却报顶层值，rollbackNode 实证）。
//      现走查逐层定绑定：先遇形参绑定则以其默认值（对象字面
//      量时）取证、无可用默认值则不取证；到达源文件时仅当
//      resolveBinding 确证 "top" 才用 topDecls，"other"（被遮
//      蔽）绝不回落。与 ③ 的 formula 分类同标准。
//   ⑥ helper 参数改全部实参入读集：旧实现只读 Math.min 实参 0
//      （cap）与 Math.max 实参 0（floor），Math.max 的其余实参
//      （舰队增长项本体）从不入读——增长项被整体替换为静态值
//      后 cap/floor 纹丝不动。现逐个实参分类：Math.min 第三起
//      实参静态值压低 cap、动态值令结构不可静态求值；Math.max
//      静态实参参与 floor 取大，且至少须有一个动态增长项在
//      场——增长项缺失即助手退化为常量、非登记的公式形态，
//      不输出该 helper 行、由门以「参数无法提取」点名。
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

// ── 作用域绑定解析（R8-05）──────────────────────────────────────
// 模块级绑定全集：顶层变量/函数声明与导入绑定。同名既有本地声
// 明又有导入属重复绑定错误，按 fail-closed 归「非本地」。
const topDecls = new Map(topVarDecls);
const importBindings = new Set();
for (const st of indexSf.statements) {
  if (ts.isFunctionDeclaration(st) && st.name) topDecls.set(st.name.text, st);
  if (ts.isImportDeclaration(st) && st.importClause) {
    const ic = st.importClause;
    if (ic.name) importBindings.add(ic.name.text);
    if (ic.namedBindings) {
      if (ts.isNamespaceImport(ic.namedBindings)) {
        importBindings.add(ic.namedBindings.name.text);
      } else {
        for (const el of ic.namedBindings.elements) {
          importBindings.add(el.name.text);
        }
      }
    }
  }
}

// 绑定名全集：标识符名、解构模式内的全部标识符。
const boundNames = (nameNode, acc = new Set()) => {
  if (!nameNode) return acc;
  if (ts.isIdentifier(nameNode)) acc.add(nameNode.text);
  else if (ts.isObjectBindingPattern(nameNode) || ts.isArrayBindingPattern(nameNode)) {
    for (const el of nameNode.elements) {
      if (ts.isBindingElement(el)) boundNames(el.name, acc);
    }
  }
  return acc;
};

// 某个父节点是否为其子节点引入名为 name 的作用域绑定（不含源
// 文件级——顶层绑定由 resolveBinding 的终点判定处理）。
const scopeBinds = (parent, name) => {
  if (
    ts.isFunctionDeclaration(parent) ||
    ts.isFunctionExpression(parent) ||
    ts.isArrowFunction(parent) ||
    ts.isMethodDeclaration(parent) ||
    ts.isConstructorDeclaration(parent) ||
    ts.isGetAccessor(parent) ||
    ts.isSetAccessor(parent)
  ) {
    for (const p of parent.parameters) {
      if (boundNames(p.name).has(name)) return true;
    }
    return false;
  }
  if (ts.isBlock(parent) || ts.isModuleBlock(parent)) {
    for (const s of parent.statements) {
      if (ts.isVariableStatement(s)) {
        for (const d of s.declarationList.declarations) {
          if (boundNames(d.name).has(name)) return true;
        }
      } else if (ts.isFunctionDeclaration(s) && s.name && s.name.text === name) {
        return true;
      } else if (
        (ts.isClassDeclaration(s) || ts.isEnumDeclaration(s)) &&
        s.name &&
        s.name.text === name
      ) {
        return true;
      }
    }
    return false;
  }
  if (ts.isVariableDeclaration(parent)) {
    return boundNames(parent.name).has(name);
  }
  if (
    ts.isForStatement(parent) ||
    ts.isForInStatement(parent) ||
    ts.isForOfStatement(parent)
  ) {
    if (parent.initializer && ts.isVariableDeclarationList(parent.initializer)) {
      for (const d of parent.initializer.declarations) {
        if (boundNames(d.name).has(name)) return true;
      }
    }
    return false;
  }
  if (ts.isCatchClause(parent) && parent.variableDeclaration) {
    return boundNames(parent.variableDeclaration.name).has(name);
  }
  if (ts.isCaseBlock(parent)) {
    for (const c of parent.clauses) {
      for (const s of c.statements) {
        if (ts.isVariableStatement(s)) {
          for (const d of s.declarationList.declarations) {
            if (boundNames(d.name).has(name)) return true;
          }
        }
      }
    }
    return false;
  }
  return false;
};

// resolveBinding(name, useNode)：useNode 处标识符 name 的绑定归属
// ——"top"（本文件顶层同名声明）｜"other"（局部遮蔽/导入/同名其
// 他顶层形态）｜"none"（无任何绑定）。自 useNode 沿父链上行，首
// 个引入该名绑定的作用域决定归属（与 TS 作用域解析同序）。
const resolveBinding = (name, useNode) => {
  let node = useNode;
  while (node.parent) {
    const p = node.parent;
    if (ts.isSourceFile(p)) {
      if (topDecls.has(name) && !importBindings.has(name)) return "top";
      if (topDecls.has(name) || importBindings.has(name)) return "other";
      return "none";
    }
    if (scopeBinds(p, name)) return "other";
    node = p;
  }
  return "none";
};

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

// helper 的结果表达式集（R8-05 数据流绑定）：箭头/函数表达式的
// 表达式体本身，或块体内全部属于该函数自身的 return 表达式
// （不下潜嵌套函数）。诱饵局部量不流向 return、永不入选。
const resultExprs = (fn) => {
  if (!fn.body) return [];
  if (!ts.isBlock(fn.body)) return [fn.body];
  const rets = [];
  const walk = (n) => {
    if (ts.isReturnStatement(n)) {
      rets.push(n.expression ?? null);
      return;
    }
    if (
      ts.isFunctionDeclaration(n) ||
      ts.isFunctionExpression(n) ||
      ts.isArrowFunction(n) ||
      ts.isClassDeclaration(n)
    ) {
      return;
    }
    ts.forEachChild(n, walk);
  };
  for (const s of fn.body.statements) walk(s);
  return rets;
};

// 从一个结果表达式读 (cap, floor)：结果表达式自身（解括号后）
// 须为 Math.min 调用，或其子树内 Math.min 恰一处；否则 null。
const helperParamsOf = (expr) => {
  expr = unwrap(expr);
  if (!expr) return null;
  let minCall = null;
  if (isMathCall(expr, "min")) {
    minCall = expr;
  } else {
    const mins = [];
    const find = (n) => {
      if (isMathCall(n, "min")) mins.push(n);
      ts.forEachChild(n, find);
    };
    find(expr);
    if (mins.length !== 1) return null;
    minCall = mins[0];
  }
  if (!minCall || minCall.arguments.length < 2) return null;
  // 全部实参入读集（修复轮 9，R9-25）：见文件头注 ⑥。
  let cap = numEval(minCall.arguments[0]);
  if (cap === null) return null;
  for (let k = 2; k < minCall.arguments.length; k++) {
    const extra = numEval(minCall.arguments[k]);
    if (extra === null) return null;
    if (extra < cap) cap = extra;
  }
  const floorArg = unwrap(minCall.arguments[1]);
  if (!floorArg || !isMathCall(floorArg, "max")) return null;
  if (floorArg.arguments.length < 2) return null;
  let floor = null;
  let hasDynamic = false;
  for (let k = 0; k < floorArg.arguments.length; k++) {
    const v = numEval(floorArg.arguments[k]);
    if (v === null) {
      hasDynamic = true;
    } else if (floor === null || v > floor) {
      floor = v;
    }
  }
  if (floor === null || !hasDynamic) return null;
  return [cap, floor];
};

for (const name of HELPERS) {
  const decl = topVarDecls.get(name);
  if (!decl || !decl.initializer) continue;
  const fn = unwrap(decl.initializer);
  if (!ts.isArrowFunction(fn) && !ts.isFunctionExpression(fn)) continue;
  const exprs = resultExprs(fn);
  if (exprs.length === 0) continue;
  const first = helperParamsOf(exprs[0]);
  if (!first) continue;
  // 多个 return 表达式取值必须一致，否则不输出（fail-closed）。
  let consistent = true;
  for (let i = 1; i < exprs.length; i++) {
    const p = helperParamsOf(exprs[i]);
    if (!p || p[0] !== first[0] || p[1] !== first[1]) {
      consistent = false;
      break;
    }
  }
  if (!consistent) continue;
  out.push(`helper\t${name}\t${first[0]}\t${first[1]}`);
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
    // 声明绑定（R8-05）：仅当调用标识符解析到本文件顶层 helper
    // 声明才记 formula；同名遮蔽/导入同名落 otherexpr。
    if (fe && resolveBinding(init.expression.text, init) === "top") {
      return ["formula", fe];
    }
  }
  if (isMathCall(init, "max")) {
    const v = numEval(init.arguments[0]);
    return v === null ? ["otherexpr", ""] : ["dynamic", fmtSec(v)];
  }
  const v = numEval(init);
  return v === null ? ["otherexpr", ""] : ["literal", fmtSec(v)];
};

// 在配置对象字面量内找 timeout（R8-05 调用内绑定）：只认该对象
// 的顶层属性。返回 [kind, value] 或 null（未见 timeout 面）。
// 计算键字符串字面量同认；见计算键非字面量或展开元素时，无法
// 排除 timeout 藏匿 → otherexpr（fail-closed）。
const scanConfigObject = (obj) => {
  let sawOpaque = false;
  for (const prop of obj.properties) {
    if (ts.isSpreadAssignment(prop)) {
      sawOpaque = true;
      continue;
    }
    if (ts.isPropertyAssignment(prop)) {
      const nm = prop.name;
      if (
        (ts.isIdentifier(nm) || ts.isStringLiteral(nm)) &&
        nm.text === "timeout"
      ) {
        return classifyTimeoutInit(prop.initializer);
      }
      if (ts.isComputedPropertyName(nm)) {
        const e = unwrap(nm.expression);
        if (ts.isStringLiteral(e) && e.text === "timeout") {
          return classifyTimeoutInit(prop.initializer);
        }
        sawOpaque = true;
      }
      continue;
    }
    if (
      ts.isShorthandPropertyAssignment(prop) &&
      prop.name.text === "timeout"
    ) {
      // { timeout } 简写：值即同名标识符，按其表达式分类。
      return classifyTimeoutInit(prop.name);
    }
  }
  return sawOpaque ? ["otherexpr", ""] : null;
};

// 引入参数绑定的函数样节点（与 scopeBinds 的函数面同集）。
const isFunctionLikeNode = (n) =>
  ts.isFunctionDeclaration(n) ||
  ts.isFunctionExpression(n) ||
  ts.isArrowFunction(n) ||
  ts.isMethodDeclaration(n) ||
  ts.isConstructorDeclaration(n) ||
  ts.isGetAccessor(n) ||
  ts.isSetAccessor(n);

// paramDefaultFor(fn, name)：fn 的形参是否绑定 name；绑定且形参
// 本人是标识符时给出其默认初始化式（无默认则 init=null）；解构
// 形参的元素默认值不是配置对象本体，按无可用默认值论。
const paramDefaultFor = (fn, name) => {
  for (const p of fn.parameters) {
    if (ts.isIdentifier(p.name)) {
      if (p.name.text === name) return { found: true, init: p.initializer ?? null };
    } else if (boundNames(p.name).has(name)) {
      return { found: true, init: null };
    }
  }
  return { found: false, init: null };
};

// resolveConfigObject(ident)：配置标识符按词法作用域逐层定绑
// 定，返回其对象字面量初始化式（无则 null）。作用域纪律见文件
// 头注 ⑤：形参绑定先于一切回落、被遮蔽的名字绝不回落顶层。
const resolveConfigObject = (a) => {
  let node = a;
  while (node.parent) {
    const p = node.parent;
    if (isFunctionLikeNode(p)) {
      const pr = paramDefaultFor(p, a.text);
      if (pr.found) {
        if (!pr.init) return null;
        const init = unwrap(pr.init);
        return ts.isObjectLiteralExpression(init) ? init : null;
      }
    } else if (ts.isVariableDeclaration(p) && boundNames(p.name).has(a.text)) {
      if (!p.initializer) return null;
      const init = unwrap(p.initializer);
      return ts.isObjectLiteralExpression(init) ? init : null;
    } else if (ts.isSourceFile(p)) {
      if (resolveBinding(a.text, a) !== "top") return null;
      const decl = topDecls.get(a.text);
      if (!decl || !ts.isVariableDeclaration(decl) || !decl.initializer) {
        return null;
      }
      const init = unwrap(decl.initializer);
      return ts.isObjectLiteralExpression(init) ? init : null;
    }
    node = p;
  }
  return null;
};

// 单个 Network./axios. 调用的 timeout 取证：只在该调用自身内取
// 证（实参对象字面量、或以标识符传参时解析到的声明对象字面
// 量）；调用之外一概不看（R8-05 调用内绑定）。
const evidenceOfCall = (callNode) => {
  let sawOpaque = false;
  for (const arg of callNode.arguments) {
    const a = unwrap(arg);
    if (ts.isObjectLiteralExpression(a)) {
      const r = scanConfigObject(a);
      if (r) {
        if (r[0] === "otherexpr") {
          sawOpaque = true;
          continue;
        }
        return r;
      }
    } else if (ts.isIdentifier(a)) {
      // 配置以标识符传参：按作用域解析到其声明的对象字面量
      // （resolveConfigObject，作用域纪律见文件头注 ⑤）。
      if (resolveBinding(a.text, a) !== "none") {
        const configObj = resolveConfigObject(a);
        if (configObj) {
          const r = scanConfigObject(configObj);
          if (r) {
            if (r[0] === "otherexpr") {
              sawOpaque = true;
              continue;
            }
            return r;
          }
        }
      }
    }
  }
  return sawOpaque ? ["otherexpr", ""] : ["none", ""];
};

// 单个导出声明的 timeout 形态：声明内全部 Network./axios. 调
// 用逐个取证后合并（修复轮 9，R9-23，合并口径见文件头注 ④）。
const censusDecl = (declNode) => {
  const ordered = [];
  const collect = (n) => {
    ordered.push(n);
    ts.forEachChild(n, collect);
  };
  collect(declNode);
  const callNodes = ordered.filter(
    (n) =>
      ts.isCallExpression(n) &&
      ts.isPropertyAccessExpression(n.expression) &&
      ts.isIdentifier(n.expression.expression) &&
      (n.expression.expression.text === "Network" ||
        n.expression.expression.text === "axios"),
  );
  if (callNodes.length === 0) return ["none", ""];
  let concrete = null;
  for (const cn of callNodes) {
    const ev = evidenceOfCall(cn);
    if (ev[0] === "otherexpr") return ["otherexpr", ""];
    if (ev[0] === "none") continue;
    if (concrete && (concrete[0] !== ev[0] || concrete[1] !== ev[1])) {
      return ["otherexpr", ""];
    }
    concrete = ev;
  }
  return concrete ?? ["none", ""];
};

const emittedCalls = new Set();
const emitCall = (name, declNode) => {
  if (emittedCalls.has(name)) return;
  emittedCalls.add(name);
  const [kind, value] = censusDecl(declNode);
  out.push(`call\t${name}\t${kind}\t${value}`);
};

for (const st of indexSf.statements) {
  const exported =
    ts.canHaveModifiers(st) &&
    (ts.getModifiers(st) ?? []).some(
      (m) => m.kind === ts.SyntaxKind.ExportKeyword,
    );
  if (!exported) continue;
  if (ts.isVariableStatement(st)) {
    for (const d of st.declarationList.declarations) {
      if (ts.isIdentifier(d.name)) emitCall(d.name.text, d);
    }
  } else if (ts.isFunctionDeclaration(st) && st.name) {
    emitCall(st.name.text, st);
  }
}
// export { x } / export { x as y } 列表形态（R8-05）：同文件顶层
// 声明经导出列表对外者同入普查，输出名取导出名。
for (const st of indexSf.statements) {
  if (!ts.isExportDeclaration(st) || !st.exportClause) continue;
  if (!ts.isNamedExports(st.exportClause)) continue;
  for (const el of st.exportClause.elements) {
    const local = (el.propertyName ?? el.name).text;
    const decl = topDecls.get(local);
    if (decl) emitCall(el.name.text, decl);
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
