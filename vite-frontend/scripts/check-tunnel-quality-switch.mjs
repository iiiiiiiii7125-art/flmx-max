#!/usr/bin/env node
// 隧道质量开关判读单点门禁（R8-018 防再漏面）。
//
// 口径：monitor_tunnel_quality_enabled 的判读全仓收敛到唯一函数
// src/config/tunnel-quality.ts 的 resolveTunnelQualityEnabled
// （与后端 isTunnelQualityMonitoringEnabled 同口径：缺失＝开启、
// 仅 "false" 为关闭）。R8-018 的教训是 config 页保存事件广播曾单独
// 以 === "true" 判读、键未落库时得出 false，与开关显示/监控页自读/
// 后端三面相反；此类「派生面各自判读」靠本门禁拦截：
//   1. 真源函数必须存在且保持 !== "false" 语义；
//   2. src/ 其余文件不许出现「质量键标识与 "true"/"false" 字面量
//      直接比较」的行；
//   3. 隧道监控页不许再对配置值写 .value === "true" 式判读，且必须
//      调用 resolveTunnelQualityEnabled；
//   4. config 页 monitorTunnelQualityEnabledChanged 广播的 enabled
//      必须经 resolveSwitchSelected / resolveTunnelQualityEnabled。
//
// 证据形态沿革（为什么断言 1 是行为断言）：
//   - 初版以整文件正则钉 !== "false"，被函数头注里的散文字面量拷贝
//     自满足（R5-03）；
//   - 第五轮改「只截函数体、剥注释后判定」，第六轮仍被三种新构造
//     绕过（R6-11）：锚点前的块注释里放假签名＋假函数体使截取落到
//     假体（锚点劫持）、函数体内字符串拷贝字面量满足正向钉、
//     if(false) 死代码支携带被钉形态而活路翻转。共同根因是文本钉
//     桩的证据可由非行为文本（注释/字符串/死代码）满足。
//   - 本轮（修复轮 6，R6-11 机制处置）：断言 1 改为行为断言——把
//     tunnel-quality.ts 转译后实际执行 resolveTunnelQualityEnabled，
//     以输入表逐格断言返回值（缺失/非字符串/空串→开启，仅 trim＋
//     小写后恰为 "false"→关闭）。注释、字符串拷贝、死代码与锚点
//     位置对执行结果均无影响，该族构造整体失效。断言 2–4 的提取
//     面同步改为先经词法剥注释（字符串内容保留、行号不变）再匹
//     配，注释拷贝不再构成满足点、注释中断不再构成漏检点。
//   - 本轮（修复轮 7，R7-25 / G-C4-08 机制处置）：断言 2–4 的逐行
//     正则只认「运算符紧贴引号」的字面形态——括号化字面量
//     === ("true") 与原形态语义等价却整体绕过，正向钉又只是剥注
//     释后的文本在位（死调用/字符串文案可满足）。2–4 改 TS 编译
//     器 API 的 AST 判定：括号表达式先解包、比较形态按二元表达
//     式的操作数类型判定（键标识侧 × "true"/"false" 字面量侧），
//     全部判定只遍历活代码——遍历器按字面真值剪除死分支（if 恒
//     假支、三元恒定支、false && / true || 的短路侧、while(false)
//     体；静态真值只认 false/true 字面量及其 !/&&/|| 组合），死
//     代码里的调用与文案字符串不再构成满足点，真实判读改形
//     （括号化/换位）也无处遁形。词法剥注释器随之退役。
//   - 本轮（修复轮 9，R9-15 / I-C6-01 机制处置）：断言 2–3 的操作
//     数识别仍靠 getText() 正则认键、只认 .value 直接访问——值
//     先提取进局部变量再 === "true"、键经下标/变量间接、监控页
//     .value 别名三形态全部逃逸（原第八轮 H-C4-04 低条本体，
//     LEDGER8 承接时 ② 的全 src 扫描半落空）。2–3 改作用域感知
//     的来源追踪（真解析）：按作用域建声明表（变量初值、解构
//     来源、导入绑定、纯赋值更新），比较的操作数沿声明链回溯
//     数据来源——命中质量键来源（键名字面量、键标识、以键为下
//     标的成员读取，及经局部变量/方法链/包装调用传递的来源）
//     或配置值 .value 来源（含别名、解构与 ["value"] 下标）即
//     点名，判定不再依赖操作数的文本形态。断言 4 的站点绑定边
//     界维持第八轮登记（R8-13）不变。

// 供 CI / 发版前检查调用：pnpm run check:tunnel-quality-switch
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const srcRoot = join(frontendRoot, "src");
const truthFile = join(srcRoot, "config", "tunnel-quality.ts");

const failures = [];
const fail = (msg) => failures.push(msg);

let ts;
try {
  ts = require("typescript");
} catch {
  // worktree 尚未 pnpm install 时，退回主仓的 node_modules。
  ts = require(
    "/home/hatch/workspace/flvx-max/vite-frontend/node_modules/typescript",
  );
}

// ─── AST 基础设施（断言 2–4 共用，R7-25）────────────────────────
// parseFile：按扩展名选 ScriptKind，保留父指针（断言 4 需上溯）。
const parseFile = (path) => {
  const text = readFileSync(path, "utf8");
  return ts.createSourceFile(
    path,
    text,
    ts.ScriptTarget.ES2020,
    true,
    path.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
};

// 解包括号表达式：=== ("true") 与 === "true" 在 AST 上只差一层
// ParenthesizedExpression，判定前统一剥掉。
const unwrap = (e) => {
  while (ts.isParenthesizedExpression(e)) e = e.expression;
  return e;
};

// 字面静态真值：仅 false/true 字面量及其 !/&&/|| 组合可静态定值，
// 其余一律 undefined（按活代码对待）。供死分支剪除——剪除面只认
// 语言级恒定形态，不做常量传播。
const staticBool = (e) => {
  e = unwrap(e);
  if (e.kind === ts.SyntaxKind.FalseKeyword) return false;
  if (e.kind === ts.SyntaxKind.TrueKeyword) return true;
  if (
    ts.isPrefixUnaryExpression(e) &&
    e.operator === ts.SyntaxKind.ExclamationToken
  ) {
    const v = staticBool(e.operand);
    return v === undefined ? undefined : !v;
  }
  if (ts.isBinaryExpression(e)) {
    const op = e.operatorToken.kind;
    if (op === ts.SyntaxKind.AmpersandAmpersandToken) {
      const l = staticBool(e.left);
      const r = staticBool(e.right);
      if (l === false || r === false) return false;
      if (l === true && r === true) return true;
      return undefined;
    }
    if (op === ts.SyntaxKind.BarBarToken) {
      const l = staticBool(e.left);
      const r = staticBool(e.right);
      if (l === true || r === true) return true;
      if (l === false && r === false) return false;
      return undefined;
    }
  }
  return undefined;
};

// 活代码遍历：回调每个可达节点；按 staticBool 剪除死分支——
// if/三元的恒定支、&& / || 的短路侧、while(false) 循环体不遍历。
const walkLive = (node, cb) => {
  cb(node);
  if (ts.isIfStatement(node)) {
    walkLive(node.expression, cb);
    const c = staticBool(node.expression);
    if (c !== false) walkLive(node.thenStatement, cb);
    if (c !== true && node.elseStatement) walkLive(node.elseStatement, cb);
    return;
  }
  if (ts.isConditionalExpression(node)) {
    walkLive(node.condition, cb);
    const c = staticBool(node.condition);
    if (c !== false) walkLive(node.whenTrue, cb);
    if (c !== true) walkLive(node.whenFalse, cb);
    return;
  }
  if (ts.isWhileStatement(node)) {
    walkLive(node.expression, cb);
    if (staticBool(node.expression) !== false) walkLive(node.statement, cb);
    return;
  }
  if (ts.isBinaryExpression(node)) {
    const op = node.operatorToken.kind;
    if (
      op === ts.SyntaxKind.AmpersandAmpersandToken ||
      op === ts.SyntaxKind.BarBarToken
    ) {
      walkLive(node.left, cb);
      const l = staticBool(node.left);
      const dead =
        op === ts.SyntaxKind.AmpersandAmpersandToken
          ? l === false
          : l === true;
      if (!dead) walkLive(node.right, cb);
      return;
    }
  }
  ts.forEachChild(node, (child) => walkLive(child, cb));
};

// ─── 来源追踪（断言 2–3 共用，R9-15）──────────────────────
// 作用域：{ parent, decls: Map<name, info> }；info 记录声明的初
// 值表达式（init）、解构来源属性名（viaProp，如 const {value}
// = resp 时 value 绑定记 viaProp="value"）、导入的原名
// （imported）与声明所在作用域（scope，初值里的标识符按词法
// 作用域解析）。
const KEY_NAME = "monitor_tunnel_quality_enabled";
const KEY_IDENT = /monitor_tunnel_quality_enabled|TUNNEL_QUALITY_ENABLED/;
const newScope = (parent) => ({ parent, decls: new Map() });
const regDecl = (scope, name, info) => {
  if (!scope.decls.has(name)) scope.decls.set(name, { ...info, scope });
};
const bindPattern = (scope, nameNode, init, viaProp, kind = "var") => {
  if (ts.isIdentifier(nameNode)) {
    regDecl(scope, nameNode.text, { kind, init, viaProp });
    return;
  }
  if (ts.isObjectBindingPattern(nameNode)) {
    for (const el of nameNode.elements) {
      const prop = el.propertyName
        ? el.propertyName.text
        : ts.isIdentifier(el.name)
          ? el.name.text
          : null;
      if (el.initializer) bindPattern(scope, el.name, el.initializer, null, kind);
      else bindPattern(scope, el.name, init, prop, kind);
    }
    return;
  }
  if (ts.isArrayBindingPattern(nameNode)) {
    for (const el of nameNode.elements) {
      if (ts.isBindingElement(el)) {
        bindPattern(scope, el.name, el.initializer ?? init, viaProp, kind);
      }
    }
  }
};
const prescan = (scope, statements) => {
  for (const st of statements) {
    if (ts.isVariableStatement(st)) {
      for (const d of st.declarationList.declarations) {
        bindPattern(scope, d.name, d.initializer ?? null, null);
      }
    } else if (ts.isFunctionDeclaration(st) && st.name) {
      regDecl(scope, st.name.text, { kind: "other", init: null });
    } else if (ts.isClassDeclaration(st) && st.name) {
      regDecl(scope, st.name.text, { kind: "other", init: null });
    } else if (ts.isEnumDeclaration(st)) {
      regDecl(scope, st.name.text, { kind: "other", init: null });
    }
  }
};
const lookupDecl = (scope, name) => {
  for (let s = scope; s; s = s.parent) {
    if (s.decls.has(name)) return s.decls.get(name);
  }
  return null;
};
const isFunctionLikeNode = (node) =>
  ts.isFunctionDeclaration(node) ||
  ts.isFunctionExpression(node) ||
  ts.isArrowFunction(node) ||
  ts.isMethodDeclaration(node) ||
  ts.isConstructorDeclaration(node) ||
  ts.isGetAccessorDeclaration(node) ||
  ts.isSetAccessorDeclaration(node);

// 带作用域与死分支剪除的遍历：回调 (node, scope)。块/函数/循
// 环头/catch 各建子作用域并预登记其直接声明；纯赋值（=）按源
// 序更新绑定初值，使「先赋值后比较」的别名来源不断链。
const scanNode = (node, scope, cb) => {
  cb(node, scope);
  if (ts.isIfStatement(node)) {
    scanNode(node.expression, scope, cb);
    const c = staticBool(node.expression);
    if (c !== false) scanNode(node.thenStatement, scope, cb);
    if (c !== true && node.elseStatement) scanNode(node.elseStatement, scope, cb);
    return;
  }
  if (ts.isConditionalExpression(node)) {
    scanNode(node.condition, scope, cb);
    const c = staticBool(node.condition);
    if (c !== false) scanNode(node.whenTrue, scope, cb);
    if (c !== true) scanNode(node.whenFalse, scope, cb);
    return;
  }
  if (ts.isWhileStatement(node)) {
    scanNode(node.expression, scope, cb);
    if (staticBool(node.expression) !== false) scanNode(node.statement, scope, cb);
    return;
  }
  if (ts.isBinaryExpression(node)) {
    const op = node.operatorToken.kind;
    if (op === ts.SyntaxKind.EqualsToken && ts.isIdentifier(unwrap(node.left))) {
      scanNode(node.right, scope, cb);
      const decl = lookupDecl(scope, unwrap(node.left).text);
      if (decl && (decl.kind === "var" || decl.kind === "param")) {
        decl.init = node.right;
        decl.viaProp = null;
      }
      return;
    }
    if (
      op === ts.SyntaxKind.AmpersandAmpersandToken ||
      op === ts.SyntaxKind.BarBarToken
    ) {
      scanNode(node.left, scope, cb);
      const l = staticBool(node.left);
      const dead =
        op === ts.SyntaxKind.AmpersandAmpersandToken ? l === false : l === true;
      if (!dead) scanNode(node.right, scope, cb);
      return;
    }
  }
  if (isFunctionLikeNode(node)) {
    const fs = newScope(scope);
    for (const p of node.parameters) {
      bindPattern(fs, p.name, p.initializer ?? null, null, "param");
    }
    ts.forEachChild(node, (child) => scanNode(child, fs, cb));
    return;
  }
  if (ts.isBlock(node)) {
    const bs = newScope(scope);
    prescan(bs, node.statements);
    ts.forEachChild(node, (child) => scanNode(child, bs, cb));
    return;
  }
  if (ts.isCaseBlock(node)) {
    const bs = newScope(scope);
    ts.forEachChild(node, (child) => scanNode(child, bs, cb));
    return;
  }
  if (ts.isCatchClause(node)) {
    const cs = newScope(scope);
    if (node.variableDeclaration) {
      bindPattern(cs, node.variableDeclaration.name, null, null, "param");
    }
    scanNode(node.block, cs, cb);
    return;
  }
  if (
    ts.isForStatement(node) ||
    ts.isForInStatement(node) ||
    ts.isForOfStatement(node)
  ) {
    const fs = newScope(scope);
    if (node.initializer && ts.isVariableDeclarationList(node.initializer)) {
      for (const d of node.initializer.declarations) {
        bindPattern(fs, d.name, d.initializer ?? null, null);
      }
    }
    ts.forEachChild(node, (child) => scanNode(child, fs, cb));
    return;
  }
  ts.forEachChild(node, (child) => scanNode(child, scope, cb));
};
const scanFile = (sf, cb) => {
  const fileScope = newScope(null);
  for (const st of sf.statements) {
    if (ts.isImportDeclaration(st) && st.importClause) {
      const clause = st.importClause;
      if (clause.name) {
        regDecl(fileScope, clause.name.text, {
          kind: "import",
          imported: "default",
          init: null,
        });
      }
      const nb = clause.namedBindings;
      if (nb) {
        if (ts.isNamespaceImport(nb)) {
          regDecl(fileScope, nb.name.text, {
            kind: "import",
            imported: "*",
            init: null,
          });
        } else {
          for (const el of nb.elements) {
            regDecl(fileScope, el.name.text, {
              kind: "import",
              imported: (el.propertyName ?? el.name).text,
              init: null,
            });
          }
        }
      }
    }
  }
  prescan(fileScope, sf.statements);
  scanNode(sf, fileScope, cb);
};

// 来源回溯：expr 的数据来源是否命中 mode 指定的源——
//   "key"：质量键（键名字面量、键标识、以键为下标的读取）；
//   "value"：配置值 .value 读取（含 ["value"] 下标）。
// 沿途解包括号/as/await/非空断言；标识符沿作用域声明链回溯初
// 值（解构绑定按 viaProp 判源、导入按原名判源、形参与未声明者
// 无来源）；方法调用回溯其宿主与实参、包装调用回溯实参、三元
// 与 ??/|| 回溯两支。seen 防声明环。
const traceSource = (expr, scope, mode, seen = new Set()) => {
  expr = unwrap(expr);
  while (
    ts.isAsExpression(expr) ||
    ts.isNonNullExpression(expr) ||
    ts.isAwaitExpression(expr) ||
    (typeof ts.isSatisfiesExpression === "function" &&
      ts.isSatisfiesExpression(expr))
  ) {
    expr = unwrap(expr.expression);
  }
  if (mode === "key") {
    if (ts.isStringLiteral(expr) && expr.text === KEY_NAME) return true;
    if (ts.isIdentifier(expr) && KEY_IDENT.test(expr.text)) return true;
    if (ts.isPropertyAccessExpression(expr) && KEY_IDENT.test(expr.name.text)) {
      return true;
    }
  } else {
    if (ts.isPropertyAccessExpression(expr) && expr.name.text === "value") {
      return true;
    }
    if (ts.isElementAccessExpression(expr)) {
      const arg = unwrap(expr.argumentExpression);
      if (ts.isStringLiteral(arg) && arg.text === "value") return true;
    }
  }
  if (ts.isIdentifier(expr)) {
    const decl = lookupDecl(scope, expr.text);
    if (!decl || seen.has(decl)) return false;
    seen.add(decl);
    if (decl.viaProp) {
      if (mode === "value" && decl.viaProp === "value") return true;
      if (mode === "key" && KEY_IDENT.test(decl.viaProp)) return true;
      return false;
    }
    if (decl.kind === "import") {
      return mode === "key" && KEY_IDENT.test(decl.imported ?? "");
    }
    if (!decl.init) return false;
    return traceSource(decl.init, decl.scope ?? scope, mode, seen);
  }
  if (ts.isElementAccessExpression(expr)) {
    return traceSource(expr.argumentExpression, scope, mode, seen);
  }
  if (ts.isCallExpression(expr)) {
    if (ts.isPropertyAccessExpression(expr.expression)) {
      if (traceSource(expr.expression.expression, scope, mode, seen)) {
        return true;
      }
    }
    return expr.arguments.some((arg) => traceSource(arg, scope, mode, seen));
  }
  if (ts.isConditionalExpression(expr)) {
    return (
      traceSource(expr.whenTrue, scope, mode, seen) ||
      traceSource(expr.whenFalse, scope, mode, seen)
    );
  }
  if (ts.isBinaryExpression(expr)) {
    const op = expr.operatorToken.kind;
    if (
      op === ts.SyntaxKind.QuestionQuestionToken ||
      op === ts.SyntaxKind.BarBarToken ||
      op === ts.SyntaxKind.AmpersandAmpersandToken
    ) {
      return (
        traceSource(expr.left, scope, mode, seen) ||
        traceSource(expr.right, scope, mode, seen)
      );
    }
    return false;
  }
  if (ts.isTemplateExpression(expr)) {
    return expr.templateSpans.some((span) =>
      traceSource(span.expression, scope, mode, seen),
    );
  }
  if (ts.isPrefixUnaryExpression(expr)) {
    return traceSource(expr.operand, scope, mode, seen);
  }
  if (ts.isObjectLiteralExpression(expr)) {
    return expr.properties.some((p) => {
      if (ts.isPropertyAssignment(p)) {
        return traceSource(p.initializer, scope, mode, seen);
      }
      if (ts.isShorthandPropertyAssignment(p)) {
        return traceSource(p.name, scope, mode, seen);
      }
      return false;
    });
  }
  if (ts.isArrayLiteralExpression(expr)) {
    return expr.elements.some((el) => traceSource(el, scope, mode, seen));
  }
  return false;
};

const lineOf = (sf, node) =>
  sf.getLineAndCharacterOfPosition(node.getStart()).line + 1;

// 1. 真源函数与语义（行为断言：转译后实测返回值表）。
const truthSource = readFileSync(truthFile, "utf8");
const { outputText } = ts.transpileModule(truthSource, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2020,
  },
  reportDiagnostics: true,
});
const runtimeRequires = [
  ...outputText.matchAll(/require\(["']([^"']+)["']\)/g),
].map((m) => m[1]);
if (runtimeRequires.length > 0) {
  fail(
    `src/config/tunnel-quality.ts 转译产物出现运行时 require（${runtimeRequires.join(", ")}），行为断言无法隔离执行`,
  );
} else {
  const mod = { exports: {} };
  new Function("module", "exports", "require", outputText)(
    mod,
    mod.exports,
    require,
  );
  const resolveFn = mod.exports.resolveTunnelQualityEnabled;
  if (typeof resolveFn !== "function") {
    fail("src/config/tunnel-quality.ts 缺少 resolveTunnelQualityEnabled 导出");
  } else {
    // [输入, 期望]：与后端 isTunnelQualityMonitoringEnabled 同口径——
    // 键缺失/非字符串/空串/任意非 "false" 值＝开启；仅 trim＋小写后
    // 恰为 "false"＝关闭。
    const cases = [
      [undefined, true],
      [null, true],
      [true, true],
      [0, true],
      ["", true],
      ["true", true],
      ["TRUE", true],
      ["0", true],
      ["false", false],
      ["FALSE", false],
      [" false ", false],
    ];
    for (const [input, expected] of cases) {
      let actual;
      try {
        actual = resolveFn(input);
      } catch (e) {
        fail(
          `resolveTunnelQualityEnabled(${JSON.stringify(input)}) 抛错：${e.message}`,
        );
        continue;
      }
      if (actual !== expected) {
        fail(
          `resolveTunnelQualityEnabled(${JSON.stringify(input)}) = ${actual}，期望 ${expected}（口径：缺失＝开启、仅 "false" 为关闭）`,
        );
      }
    }
  }
}

// 2. 全 src 扫描：与布尔字面量严格比较的操作数若数据来源是质
//    量键（含经局部变量提取/下标/包装传递的来源），即为自写判
//    读（AST 活代码＋来源追踪，R9-15）。
const isBoolLiteral = (e) =>
  ts.isStringLiteral(e) && (e.text === "true" || e.text === "false");
const isStrictCompare = (node) =>
  ts.isBinaryExpression(node) &&
  (node.operatorToken.kind === ts.SyntaxKind.EqualsEqualsEqualsToken ||
    node.operatorToken.kind === ts.SyntaxKind.ExclamationEqualsEqualsToken);
const sourceFiles = readdirSync(srcRoot, { recursive: true })
  .map((entry) => join(srcRoot, entry))
  .filter((path) => /\.(ts|tsx)$/.test(path));

for (const file of sourceFiles) {
  if (file === truthFile) continue;
  const sf = parseFile(file);
  scanFile(sf, (node, scope) => {
    if (!isStrictCompare(node)) return;
    const l = unwrap(node.left);
    const r = unwrap(node.right);
    const hit =
      (isBoolLiteral(l) && traceSource(r, scope, "key")) ||
      (isBoolLiteral(r) && traceSource(l, scope, "key"));
    if (hit) {
      fail(
        `${relative(frontendRoot, file)}:${lineOf(sf, node)} 质量键的判读值被直接与布尔字面量比较（来源追踪命中，含局部变量提取等间接来源），应改走 resolveTunnelQualityEnabled`,
      );
    }
  });
}

// 3. 隧道监控页自读面：与布尔字面量严格比较的操作数若数据来源
//    是配置值 .value 读取（含别名、解构、["value"] 下标），即为
//    自写判读；且必须有对 resolveTunnelQualityEnabled 的活调用
//    （死分支里的调用不算）。
const monitorViewFile = join(srcRoot, "pages", "node", "tunnel-monitor-view.tsx");
const monitorSf = parseFile(monitorViewFile);
let monitorCallsResolver = false;
scanFile(monitorSf, (node, scope) => {
  if (isStrictCompare(node)) {
    const l = unwrap(node.left);
    const r = unwrap(node.right);
    if (
      (isBoolLiteral(l) && traceSource(r, scope, "value")) ||
      (isBoolLiteral(r) && traceSource(l, scope, "value"))
    ) {
      fail(
        `src/pages/node/tunnel-monitor-view.tsx:${lineOf(monitorSf, node)} 对配置值直接判读（.value 来源追踪命中，含别名），应改走 resolveTunnelQualityEnabled`,
      );
    }
  }
  if (
    ts.isCallExpression(node) &&
    ts.isIdentifier(node.expression) &&
    node.expression.text === "resolveTunnelQualityEnabled"
  ) {
    monitorCallsResolver = true;
  }
});
if (!monitorCallsResolver) {
  fail("tunnel-monitor-view.tsx 未调用 resolveTunnelQualityEnabled");
}

// 4. config 页保存广播面（AST 活代码）：定位广播名活字面量，上溯
//    至最近的调用/构造表达式，在其子树内要求 detail.enabled 的
//    赋值初值是对 resolveSwitchSelected / resolveTunnelQualityEnabled
//    的活调用——字符串文案里的判读形态、死代码里的拷贝均不满足。
const configPageFile = join(srcRoot, "pages", "config.tsx");
const configSf = parseFile(configPageFile);
let broadcastFound = false;
let broadcastOk = false;
walkLive(configSf, (node) => {
  if (!ts.isStringLiteral(node)) return;
  if (node.text !== "monitorTunnelQualityEnabledChanged") return;
  broadcastFound = true;
  let call = node.parent;
  while (call && !ts.isCallExpression(call) && !ts.isNewExpression(call)) {
    call = call.parent;
  }
  if (!call) return;
  walkLive(call, (inner) => {
    if (
      ts.isPropertyAssignment(inner) &&
      inner.name.getText() === "enabled" &&
      ts.isCallExpression(unwrap(inner.initializer)) &&
      ts.isIdentifier(unwrap(inner.initializer).expression) &&
      (unwrap(inner.initializer).expression.text ===
        "resolveSwitchSelected" ||
        unwrap(inner.initializer).expression.text ===
          "resolveTunnelQualityEnabled")
    ) {
      broadcastOk = true;
    }
  });
});
if (!broadcastFound) {
  fail("config.tsx 未找到 monitorTunnelQualityEnabledChanged 广播");
} else if (!broadcastOk) {
  fail(
    "config.tsx 保存广播的 enabled 未经 resolveSwitchSelected/resolveTunnelQualityEnabled 判读",
  );
}

if (failures.length) {
  console.error("隧道质量开关判读单点门禁失败：");
  failures.forEach((msg) => console.error(`  - ${msg}`));
  process.exit(1);
}

console.log(
  "隧道质量开关判读单点门禁通过：真源函数行为实测合口径，全仓判读收敛 resolveTunnelQualityEnabled",
);