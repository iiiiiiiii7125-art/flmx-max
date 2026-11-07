#!/usr/bin/env node
/**
 * check-contract-fields.mjs — 前后端契约门禁（FLMX 审计 B2 批落地）
 *
 * 目的：把 X2 逐字段比对的结论固化成可重复执行的集合 diff，防止 types.ts
 * 与后端实际响应再次漂移。检查分八组：
 *
 *   C1  nullable 贯彻：后端 repository.go 的 List* 出口凡经 nullable*
 *       辅助函数下发的字段（JSON null），前端对应 interface 必须显式
 *       声明 `| null`（声明缺失同样算失败）。辅助函数集自 R2-01-04 起
 *       机械推导：repository.go 中凡 func nullable…(…) interface{} 者
 *       全入集（含手写名单曾漏列的 nullableInIPString，ListTunnels 的
 *       inIp 在用）；推导集为空即前提失效 exit 2，不许空集静默放行。
 *       候选集自 A4-04 起机械推导：枚举全部 List* 方法，未登记者只要
 *       经 nullable* 下发即点名红（登记表＋明示排除表之外的出口不得
 *       静默绕过），不再只遍历手工登记的 5 个。
 *   C2  userPackage.userInfo：updatedTime 可空、expTime 为 number、无 maxConn。
 *   C3  TunnelQuality 双形态：后端快照 success=bool、历史 success=int 的既成
 *       事实下，前端必须有 TunnelQualityHistoryApiItem（success: number），
 *       且 api 层 getMonitorTunnelQualityHistory 必须做归一化再交出
 *       TunnelQualityApiItem[]。
 *   C4  备份类型集合：后端 ExportPartial / Import 的 typeSet 键集合，必须与
 *       前端 BackupTypes（api/index.ts）键、BACKUP_TYPE_OPTIONS（config.tsx）
 *       值三方相等。
 *   C5  UpdatePasswordPayload：confirmPassword / newUsername 必填
 *       （后端 changePasswordRequest 两者空值即拒绝）。
 *   C6  幻影/缺失字段簇：types.ts 各 interface 的禁入字段与必备字段。
 *   C7  页面本地类型副本同步：limit.tsx /
 *       dashboard/use-dashboard-data.ts / forward.tsx / node.tsx /
 *       src/types/index.ts 中与线缆类型同形的本地声明不得保留旧错误。
 *   C8  TuneProbe 回包字段：agent tuneProbeResult（go-gost/x/socket/
 *       tuning_probe.go）的 json tag 集与前端 NodeTuneProbeResult
 *       ——前端声明字段不得越出 agent tag 集，且 C2-01 裁定的新
 *       字段集（sock_diag 分布三件＋参考腿序列七件）两侧齐在。
 *
 * 已登记的设计性差异见 scripts/contract-whitelist.json（只登记、不拦截）。
 *
 * 提取卫生（修复轮 6，R6-C4-16）：全部源码读取先经词法剥注释，
 * interface 块与 C3 函数体以字符串/模板感知的括号配平截取——旧
 * 实现的大括号计数无字符串/注释意识、C3 为无界尾切片，注释拷贝
 * 与注释内的 } 都能满足或截断判定，现整体失效。
 *
 * AST 迁移（修复轮 7 普查处置）：本门的 JS 剥离器无正则字面量
 * 意识，index.ts:143 的普通正则（转义斜杠与闭合斜杠在原文里
 * 构成字面 //）令剥离器误入行注释态、模板栈污染，该文件 133
 * 条行注释在剥离后文本里存活——C3 的函数体钉可被尾随注释里的
 * 归一化原文满足（复攻实证）。C3 函数体判定与 C4 的前端两面
 * （BackupTypes 接口成员、BACKUP_TYPE_OPTIONS 值集）改走 TS
 * 编译器 API 的 AST 提取；index.ts/config.tsx 不再经剥离器。
 * 其余读取面（types.ts、C7 各页——同法实测剥离同步无污染；
 * 全部 Go 文件——Go 无正则字面量语法）保留剥离＋文本提取，
 * 残面与构造代价登记于 fix7-reports/gatesmech2.md 普查表。
 *
 * 用法：node scripts/check-contract-fields.mjs [repoRoot]
 * 退出码：0 全过；1 任一检查失败。
 */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const root =
  process.argv[2] ||
  join(dirname(fileURLToPath(import.meta.url)), "..");

// 读取面缺失即点名失败（R2-C6-01）：登记面引用的文件被删（如三件套
// 移除时的 pages/group.tsx）时，旧实现以未捕获 ENOENT 崩溃、吞掉崩
// 溃点之后的全部检查与汇总。现统一在此点名并以失败码退出——文件缺
// 失本身就是登记与代码漂移，必须红且点名，不得以堆栈代替结论。
const read = (rel) => {
  try {
    return readFileSync(join(root, rel), "utf8");
  } catch (e) {
    console.error(
      `FAIL 读取面: ${rel} 读取失败（${e.code || e.message}）——登记引用的文件不存在或不可读，先核登记与代码是否同步`,
    );
    process.exit(1);
  }
};

const require = createRequire(import.meta.url);
let ts;
for (const cand of [
  join(root, "vite-frontend/node_modules/typescript"),
  join(
    dirname(fileURLToPath(import.meta.url)),
    "../vite-frontend/node_modules/typescript",
  ),
]) {
  try {
    ts = require(cand);
    break;
  } catch {
    // try next candidate
  }
}
// R4-C6-04 同族残差（集成复扫发现）：本脚本与 timeout-fe-extract.mjs
// 不在 FX-D 原 10 文件集内，此前仍带写死的主检出回退路径——工作树
// 未装依赖时会静默绑定主检出的 TS 版本。现与同族同口径：只许自当
// 前工作树解析；外部副本须 FLMX_CHECK_TYPESCRIPT 显式指向并点名。
if (!ts && process.env.FLMX_CHECK_TYPESCRIPT) {
  ts = require(process.env.FLMX_CHECK_TYPESCRIPT);
  console.log(
    `NOTE  typescript 由 FLMX_CHECK_TYPESCRIPT 显式指向工作树外副本：${process.env.FLMX_CHECK_TYPESCRIPT}（v${ts.version}）`,
  );
}
if (!ts) {
  console.error(
    "FAIL: typescript 不可自当前工作树解析，C3/C4 的 AST 提取无法执行——先在 vite-frontend 装依赖，或以 FLMX_CHECK_TYPESCRIPT=<typescript 路径> 显式指向外部副本",
  );
  process.exit(2);
}

// 词法剥注释（修复轮 6，R6-C4-16 机制处置）：注释逐字替换为空格
// （换行保留），字符串内容原样保留。goMode 下反引号是无插值原始
// 字符串、单引号是符文字面量；JS/TS 模式下反引号是带 ${} 插值的
// 模板字面量。全部源码读取先经此处理：注释里的字段形行、函数形
// 文本不再被 fieldDecl / goFuncSpan / 正则命中，也不能截断块提取。
const stripComments = (src, goMode) => {
  const out = [];
  const n = src.length;
  let i = 0;
  let mode = "code"; // code | line | block | sq | dq | tpl | raw
  const tplDepths = [];
  let braceDepth = 0;
  while (i < n) {
    const c = src[i];
    const d = src[i + 1] ?? "";
    if (mode === "code") {
      if (c === "/" && d === "/") {
        out.push("  ");
        i += 2;
        mode = "line";
        continue;
      }
      if (c === "/" && d === "*") {
        out.push("  ");
        i += 2;
        mode = "block";
        continue;
      }
      if (c === "'") mode = "sq";
      else if (c === '"') mode = "dq";
      else if (c === "`") mode = goMode ? "raw" : "tpl";
      else if (c === "{") braceDepth++;
      else if (c === "}") {
        if (
          !goMode &&
          tplDepths.length &&
          braceDepth === tplDepths[tplDepths.length - 1]
        ) {
          tplDepths.pop();
          braceDepth--;
          mode = "tpl";
          out.push(c);
          i++;
          continue;
        }
        braceDepth--;
      }
      out.push(c);
      i++;
      continue;
    }
    if (mode === "line") {
      if (c === "\n") {
        out.push(c);
        i++;
        mode = "code";
        continue;
      }
      out.push(" ");
      i++;
      continue;
    }
    if (mode === "block") {
      if (c === "*" && d === "/") {
        out.push("  ");
        i += 2;
        mode = "code";
        continue;
      }
      out.push(c === "\n" ? "\n" : " ");
      i++;
      continue;
    }
    if (mode === "sq" || mode === "dq") {
      const q = mode === "sq" ? "'" : '"';
      out.push(c);
      if (c === "\\") {
        out.push(d);
        i += 2;
        continue;
      }
      if (c === q) mode = "code";
      i++;
      continue;
    }
    if (mode === "raw") {
      out.push(c);
      if (c === "`") mode = "code";
      i++;
      continue;
    }
    out.push(c);
    if (c === "\\") {
      out.push(d);
      i += 2;
      continue;
    }
    if (c === "`") {
      mode = "code";
      i++;
      continue;
    }
    if (c === "$" && d === "{") {
      out.push(d);
      i += 2;
      braceDepth++;
      tplDepths.push(braceDepth);
      mode = "code";
      continue;
    }
    i++;
  }
  return out.join("");
};

const readCode = (rel, goMode) => stripComments(read(rel), goMode);

const FE = "vite-frontend/src";
const typesTs = readCode(`${FE}/api/types.ts`, false);
// index.ts 与 config.tsx 改走 AST（修复轮 7 普查处置）：本门的
// JS 词法剥离器无正则字面量意识——index.ts:143 的普通正则
// /^\//（转义斜杠与闭合斜杠在原文里构成字面 //）即令剥离器误
// 入行注释态并污染模板栈，该文件 133 条行注释在「剥离后」文本
// 里存活，C3 函数体钉曾被尾随注释里的归一化原文满足（复攻
// CONTRACT-N1 实证绿）。两文件的 C3/C4 提取自此只认 AST 声明
// 与表达式结构，注释/字符串拷贝不再是证据。其余文件（types.ts
// 与 C7 各页）经同法实测剥离同步无污染、Go 文件无正则字面量
// 语法，均保留文本提取（残面登记见 fix7 普查表）。
const indexSf = ts.createSourceFile(
  "index.ts",
  read(`${FE}/api/index.ts`),
  ts.ScriptTarget.ES2020,
  true,
  ts.ScriptKind.TS,
);
const configSf = ts.createSourceFile(
  "config.tsx",
  read(`${FE}/pages/config.tsx`),
  ts.ScriptTarget.ES2020,
  true,
  ts.ScriptKind.TSX,
);
const repositoryGo = readCode("go-backend/internal/store/repo/repository.go", true);

// R2-01-04：nullable 发射集机械推导——repository.go 中一切返回
// interface{} 的 nullable* 辅助函数全入集，下方两处字段提取共用
// NULLABLE_FIELD_RE。此前两处用手写名单（String|Int64|
// ForwardIngress），漏了 nullableInIPString（ListTunnels 的
// "inIp" 在册配对字段正经它下发）——该字段的可空契约此前只靠
// C3 按名硬登记兜底，发射侧改形无门。文件读到却推出空集 =
// 推导面前提失效，按本门惯例 exit 2。
const NULLABLE_HELPERS = [
  ...new Set(
    [...repositoryGo.matchAll(/func (nullable\w+)\([^)]*\) interface\{\}/g)].map((m) => m[1]),
  ),
];
if (repositoryGo !== "" && NULLABLE_HELPERS.length === 0) {
  console.error(
    "PREMISE FAILURE (exit 2): derived zero nullable* interface{} helpers from repository.go - derivation surface broken (R2-01-04)",
  );
  process.exit(2);
}
const NULLABLE_FIELD_RE = new RegExp(
  `"(\\w+)":\\s*(?:${NULLABLE_HELPERS.join("|")})\\(`,
  "g",
);
const handlerGo = readCode("go-backend/internal/http/handler/handler.go", true);
const proberGo = readCode(
  "go-backend/internal/http/handler/tunnel_quality_prober.go",
  true,
);
const modelGo = readCode("go-backend/internal/store/model/model.go", true);
const whitelist = JSON.parse(read("scripts/contract-whitelist.json"));

let failures = 0;
const fail = (id, msg) => {
  failures += 1;
  console.log(`FAIL ${id}: ${msg}`);
};
const pass = (id, msg) => console.log(`PASS ${id}: ${msg}`);

/** 括号配平的块端点扫描：自 openIdx 的 { 起，跳过字符串/模板内容，
 *  返回匹配 } 的下标（找不到返回 -1）。输入已剥注释。 */
function scanBlockEnd(source, openIdx) {
  let depth = 0;
  for (let i = openIdx; i < source.length; i += 1) {
    const c = source[i];
    if (c === '"' || c === "'" || c === "`") {
      i += 1;
      while (i < source.length && source[i] !== c) {
        if (source[i] === "\\") i += 1;
        i += 1;
      }
      continue;
    }
    if (c === "{") depth += 1;
    else if (c === "}") {
      depth -= 1;
      if (depth === 0) return i;
    }
  }
  return -1;
}

/** 提取 TS interface 块（从 `interface NAME {` 起按花括号深度配平，
 *  配平跳过字符串/模板内容）。 */
function tsInterfaceBlock(source, name) {
  const start = source.search(
    new RegExp(`(?:export\\s+)?interface\\s+${name}\\b`),
  );
  if (start < 0) return null;
  const open = source.indexOf("{", start);
  if (open < 0) return null;
  const end = scanBlockEnd(source, open);
  return end < 0 ? source.slice(start) : source.slice(start, end + 1);
}

/** 在 interface 块里取某字段的声明文本（无声明返回 null）。 */
function fieldDecl(block, field) {
  if (!block) return null;
  const m = block.match(new RegExp(`^\\s*${field}\\??:\\s*([^;]+);`, "m"));
  return m ? m[1].trim() : null;
}

/** 提取 Go 函数体（从签名到下一个顶层 func）。 */
function goFuncSpan(source, signature) {
  const start = source.indexOf(signature);
  if (start < 0) return null;
  const rest = source.slice(start + signature.length);
  const next = rest.search(/\nfunc /);
  return next < 0 ? rest : rest.slice(0, next);
}

/* ---------------- C1: nullable 贯彻 ---------------- */
// 登记表随功能移除同步（R2-C6-01）：分组三件套移除后 ListGroupPermissions
// 已删，本表对应行与 C7 的 group.tsx 副本校验一并摘除；删功能时同步
// 删行——残行会令本门在当前树上恒红（登记行在下方对位循环点名）。
const listToInterface = {
  ListNodes: "NodeApiItem",
  ListUsers: "UserApiItem",
  ListSpeedLimits: "SpeedLimitApiItem",
  ListForwards: "ForwardApiItem",
  ListTunnels: "TunnelApiItem",
};

// A4-04 普查推导：候选集不再只靠这张手工登记表——旧实现只遍历登记
// 的 5 个出口，repository.go 实有 22 个 List* 方法，新增 List* 出口
// 若经 nullable* 下发字段而无人登记，前端 interface 的 | null 漂移
// 无门可红（登记集＝修复集、非机制集的典型失效）。现机械枚举全部
// List* 并逐个归类：登记表内→下方对位核；明示排除表内→跳过并公
// 示理由；其余→span 内出现 nullable* 下发即点名红，无 nullable 下
// 发者自动归类并计入普查输出。排除表登记纪律与登记表同：代码里
// 已不存在的排除行同样点名红。
const listExclusions = {
  // 当前无条目：其余 17 个 List* 经机械核对均不经 nullable* 下发
  // （模型切片 / 标量 / map[string]string 出口），自动归类即可。
  // 将来某 List* 经 nullable* 下发但消费面不在 types.ts（如纯内部
  // 出口）时，在此登记并写明理由。
};
const allListFns = [
  ...new Set(
    [...repositoryGo.matchAll(/func \(r \*Repository\) (List\w+)\(/g)].map(
      (m) => m[1],
    ),
  ),
];
{
  const failuresBefore = failures;
  let autoClassified = 0;
  for (const fn of allListFns) {
    if (fn in listToInterface || fn in listExclusions) continue;
    const span = goFuncSpan(repositoryGo, `func (r *Repository) ${fn}(`);
    const keys = span
      ? [
          ...span.matchAll(NULLABLE_FIELD_RE),
        ].map((m) => m[1])
      : [];
    if (keys.length > 0) {
      fail(
        "C1",
        `${fn}：未登记的 List* 出口经 nullable 下发字段（${keys.join(", ")}），前端 interface 无从对位——请在 listToInterface 登记对应 interface，或在 listExclusions 写明排除理由（A4-04）`,
      );
    } else {
      autoClassified += 1;
    }
  }
  for (const fn of Object.keys(listExclusions)) {
    if (!allListFns.includes(fn))
      fail(
        "C1",
        `listExclusions 登记的 ${fn} 在 repository.go 中已不存在，残行须删（A4-04）`,
      );
  }
  if (failures === failuresBefore)
    pass(
      "C1",
      `List* 出口普查共 ${allListFns.length} 个：登记对位 ${Object.keys(listToInterface).length}、明示排除 ${Object.keys(listExclusions).length}、无 nullable 下发自动归类 ${autoClassified}`,
    );
}

for (const [fn, iface] of Object.entries(listToInterface)) {
  const span = goFuncSpan(repositoryGo, `func (r *Repository) ${fn}(`);
  if (!span) {
    fail("C1", `后端未找到 ${fn}`);
    continue;
  }
  const nullableKeys = [
    ...span.matchAll(NULLABLE_FIELD_RE),
  ].map((m) => m[1]);
  const block = tsInterfaceBlock(typesTs, iface);
  if (!block) {
    fail("C1", `types.ts 未找到 interface ${iface}`);
    continue;
  }
  const failuresBefore = failures;
  for (const key of nullableKeys) {
    const decl = fieldDecl(block, key);
    if (decl === null) {
      fail(
        "C1",
        `${iface}.${key}：后端 ${fn} 以 nullable 下发（可为 null），types.ts 未声明该字段`,
      );
    } else if (!decl.includes("| null")) {
      fail(
        "C1",
        `${iface}.${key}：后端 ${fn} 以 nullable 下发（可为 null），前端声明为 \`${decl}\`，缺 | null`,
      );
    }
  }
  if (failures === failuresBefore)
    pass(
      "C1",
      `${fn} -> ${iface}：nullable 字段 ${nullableKeys.length} 个已逐个核对`,
    );
}

/* userPackage 的 forwards 出口同样以 nil 起手下发 inPort（handler.go）。 */
{
  const pkgBlock = tsInterfaceBlock(typesTs, "UserPackageInfoApiData");
  const fwdInPort = fieldDecl(tsInterfaceBlock(typesTs, "ForwardApiItem"), "inPort");
  if (!pkgBlock || fwdInPort === null || !fwdInPort.includes("| null")) {
    fail("C1", "ForwardApiItem.inPort 必须含 | null（ListForwards 与 userPackage 双出口均可为 null）");
  } else {
    pass("C1", "ForwardApiItem.inPort | null 覆盖 userPackage 出口");
  }
}

/* ---------------- C2: userPackage.userInfo ---------------- */
{
  const block = tsInterfaceBlock(typesTs, "UserPackageInfoApiData");
  const problems = [];
  if (!block) problems.push("未找到 UserPackageInfoApiData");
  else {
    const exp = fieldDecl(block, "expTime");
    if (exp === null || !/^number/.test(exp) || exp.includes("string"))
      problems.push(`userInfo.expTime 应为 number（毫秒），现为 \`${exp}\``);
    const upd = fieldDecl(block, "updatedTime");
    if (upd === null || !upd.includes("| null"))
      problems.push(`userInfo.updatedTime 应声明 | null，现为 \`${upd}\``);
    if (fieldDecl(block, "maxConn") !== null)
      problems.push("userInfo.maxConn 后端从不下发，应删除（幻影字段）");
    if (!/"updatedTime":\s*nullableNullInt64/.test(handlerGo))
      problems.push("后端锚点丢失：handler.go 未见 userPackage updatedTime nullable 下发");
  }
  if (problems.length) problems.forEach((p) => fail("C2", p));
  else pass("C2", "UserPackageInfoApiData.userInfo 与 handler.go 出口一致");
}

/* ---------------- C3: TunnelQuality 双形态 ---------------- */
{
  const problems = [];
  if (!/Success\s+bool\s+`json:"success"`/.test(proberGo))
    problems.push("后端锚点丢失：快照出口 success 非 bool");
  if (!/Success\s+int\s+.*json:"success"/.test(modelGo))
    problems.push("后端锚点丢失：历史（model）出口 success 非 int");
  const hist = tsInterfaceBlock(typesTs, "TunnelQualityHistoryApiItem");
  if (!hist) problems.push("types.ts 缺 TunnelQualityHistoryApiItem");
  else if (fieldDecl(hist, "success") !== "number")
    problems.push("TunnelQualityHistoryApiItem.success 应为 number");
  // 函数体判定改 AST 结构（修复轮 7 普查处置）：旧实现以剥注释
  // 文本＋括号配平截取函数体再正则——而 index.ts 的剥离面已被
  // 该文件自身的正则字面量污染（见文件头 C3/C4 注），尾随注释
  // 里的归一化原文即可满足。现于顶层声明
  // getMonitorTunnelQualityHistory 的 AST 子树内要求：(a) 一次
  // 带 TunnelQualityHistoryApiItem[] 类型实参的 Network.get 调
  // 用；(b) success 属性赋值的初值是 item.success !== 0 二元
  // 表达式。注释/字符串中的同形文本不构成节点，无从满足。
  let histFn = null;
  for (const st of indexSf.statements) {
    if (!ts.isVariableStatement(st)) continue;
    for (const d of st.declarationList.declarations) {
      if (
        ts.isIdentifier(d.name) &&
        d.name.text === "getMonitorTunnelQualityHistory"
      ) {
        histFn = d;
      }
    }
  }
  let callsHistoryShape = false;
  let normalizesSuccess = false;
  if (histFn) {
    const visit = (node) => {
      if (
        ts.isCallExpression(node) &&
        ts.isPropertyAccessExpression(node.expression) &&
        ts.isIdentifier(node.expression.expression) &&
        node.expression.expression.text === "Network" &&
        node.expression.name.text === "get" &&
        node.typeArguments?.length === 1 &&
        ts.isArrayTypeNode(node.typeArguments[0]) &&
        ts.isTypeReferenceNode(node.typeArguments[0].elementType) &&
        node.typeArguments[0].elementType.typeName.text ===
          "TunnelQualityHistoryApiItem"
      ) {
        callsHistoryShape = true;
      }
      if (
        ts.isPropertyAssignment(node) &&
        node.name.text === "success" &&
        ts.isBinaryExpression(node.initializer) &&
        node.initializer.operatorToken.kind ===
          ts.SyntaxKind.ExclamationEqualsEqualsToken &&
        ts.isPropertyAccessExpression(node.initializer.left) &&
        ts.isIdentifier(node.initializer.left.expression) &&
        node.initializer.left.expression.text === "item" &&
        node.initializer.left.name.text === "success" &&
        ts.isNumericLiteral(node.initializer.right) &&
        node.initializer.right.text === "0"
      ) {
        normalizesSuccess = true;
      }
      ts.forEachChild(node, visit);
    };
    visit(histFn);
  }
  if (!callsHistoryShape)
    problems.push("getMonitorTunnelQualityHistory 未按历史形态取数");
  if (!normalizesSuccess)
    problems.push("getMonitorTunnelQualityHistory 未把 success 归一化为 boolean");
  if (problems.length) problems.forEach((p) => fail("C3", p));
  else pass("C3", "TunnelQuality 双形态已在前端分型 + api 层归一化");
}

/* ---------------- C4: 备份类型集合 ---------------- */
{
  const typeSetKeys = (span) =>
    span
      ? [...new Set([...span.matchAll(/typeSet\["(\w+)"\]/g)].map((m) => m[1]))].sort()
      : [];
  // GATE-FIX G 提取面迁移登记：C1-05 把 ExportPartial 改为快照
  // 事务包装、分发本体移入小写 exportPartial 后，本行原锚点只剩
  // 包装体、提取为空集而 C4 恒红（产品集合经人工三方核对无漂
  // 移）。提取面随分发本体迁到 exportPartial，口径不变（仍取
  // 该函数 span 内的 typeSet["..."] 键集）；此后分发再迁须同步
  // 迁锚，变异自证（改 exportPartial 一类名）门必红。
  const exportKeys = typeSetKeys(goFuncSpan(repositoryGo, "func (r *Repository) exportPartial("));
  const importKeys = typeSetKeys(goFuncSpan(repositoryGo, "func (r *Repository) Import("));
  // 前端两面改 AST 提取（修复轮 7 普查处置，同 C3 注）：接口成
  // 员名与选项字面量值只认声明节点，注释/字符串拷贝不入集。
  const feKeys = [];
  for (const st of indexSf.statements) {
    if (ts.isInterfaceDeclaration(st) && st.name.text === "BackupTypes") {
      for (const mem of st.members) {
        if (ts.isPropertySignature(mem)) feKeys.push(mem.name.text);
      }
    }
  }
  feKeys.sort();
  const optKeys = [];
  for (const st of configSf.statements) {
    if (!ts.isVariableStatement(st)) continue;
    for (const d of st.declarationList.declarations) {
      let init = d.initializer;
      while (
        init &&
        (ts.isAsExpression(init) || ts.isParenthesizedExpression(init))
      ) {
        init = init.expression;
      }
      if (
        ts.isIdentifier(d.name) &&
        d.name.text === "BACKUP_TYPE_OPTIONS" &&
        init &&
        ts.isArrayLiteralExpression(init)
      ) {
        for (const el of init.elements) {
          if (!ts.isObjectLiteralExpression(el)) continue;
          for (const prop of el.properties) {
            if (
              ts.isPropertyAssignment(prop) &&
              prop.name.text === "value" &&
              ts.isStringLiteral(prop.initializer)
            ) {
              optKeys.push(prop.initializer.text);
            }
          }
        }
      }
    }
  }
  optKeys.sort();
  const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);
  const problems = [];
  if (!eq(exportKeys, importKeys))
    problems.push(`后端 Export/Import 集合不一致：${exportKeys} vs ${importKeys}`);
  if (!eq(exportKeys, feKeys))
    problems.push(`BackupTypes 与后端集合不一致：前端 ${feKeys} vs 后端 ${exportKeys}`);
  if (!eq(exportKeys, optKeys))
    problems.push(`BACKUP_TYPE_OPTIONS 与后端集合不一致：前端 ${optKeys} vs 后端 ${exportKeys}`);
  if (problems.length) problems.forEach((p) => fail("C4", p));
  else pass("C4", `备份类型集合三方一致（${exportKeys.length} 类）`);
}

/* ---------------- C5: UpdatePasswordPayload ---------------- */
{
  const block = tsInterfaceBlock(typesTs, "UpdatePasswordPayload");
  const problems = [];
  if (!block) problems.push("未找到 UpdatePasswordPayload");
  else {
    if (!/^\s*confirmPassword:\s*string;/m.test(block))
      problems.push("confirmPassword 应为必填 string（后端空值即拒绝）");
    if (!/^\s*newUsername:\s*string;/m.test(block))
      problems.push("newUsername 应为必填 string（后端空值即拒绝）");
  }
  if (!/ConfirmPassword\s+string/.test(handlerGo))
    problems.push("后端锚点丢失：changePasswordRequest 无 ConfirmPassword");
  if (problems.length) problems.forEach((p) => fail("C5", p));
  else pass("C5", "UpdatePasswordPayload 与 changePasswordRequest 对齐");
}

/* ---------------- C6: 幻影/缺失字段簇 ---------------- */
{
  const problems = [];
  const mustAbsent = [
    ["SpeedLimitApiItem", "uploadSpeed", "后端从不下发（幻影）"],
    ["SpeedLimitApiItem", "downloadSpeed", "后端从不下发（幻影）"],
    ["TunnelApiItem", "entryNodeId", "后端从不下发（幻影必填）"],
    ["TunnelApiItem", "exitNodeId", "后端从不下发（幻影必填）"],
    ["TunnelApiItem", "forwardMode", "forwardMode 是节点属性，隧道出口无此键"],
    ["ForwardApiItem", "forwardMode", "forwardMode 是节点属性，转发出口无此键"],
    ["UserTunnelApiItem", "inNodePortSta", "真实字段为 portRangeMin/portRangeMax"],
    ["UserTunnelApiItem", "inNodePortEnd", "真实字段为 portRangeMin/portRangeMax"],
    ["UserTunnelApiItem", "tunnelName", "/tunnel/user/tunnel 只下发 id/name/端口范围"],
  ];
  for (const [iface, field, why] of mustAbsent) {
    if (fieldDecl(tsInterfaceBlock(typesTs, iface), field) !== null)
      problems.push(`${iface}.${field} 应删除：${why}`);
  }
  const mustPresent = [
    ["UserTunnelApiItem", "portRangeMin"],
    ["UserTunnelApiItem", "portRangeMax"],
    ["BatchOperationResult", "pendingCount"],
    ["BatchOperationResult", "pending"],
    ["ServiceMonitorLimitsApiData", "workerLimit"],
    ["SystemUpgradeVersionApiData", "pendingMigrations"],
    ["SystemUpgradeVersionApiData", "adminBootstrapRequired"],
    ["ServiceMonitorResultApiItem", "nodeId"],
    ["TunnelDiagnosisApiItem", "diagnosing"],
    ["InboundSecurity", "certPem"],
    ["InboundSecurity", "keyPem"],
  ];
  for (const [iface, field] of mustPresent) {
    if (fieldDecl(tsInterfaceBlock(typesTs, iface), field) === null)
      problems.push(`${iface}.${field} 缺失：后端有下发/请求侧存在，前端未声明`);
  }
  const certModes = fieldDecl(tsInterfaceBlock(typesTs, "KernelInfo"), "certModes");
  if (certModes === null || !certModes.includes("| null"))
    problems.push(`KernelInfo.certModes 应为 string[] | null（后端无 omitempty，未配置时为 null），现为 \`${certModes}\``);
  const slCreated = fieldDecl(tsInterfaceBlock(typesTs, "SpeedLimitApiItem"), "createdTime");
  if (slCreated === null || !/^number/.test(slCreated))
    problems.push(`SpeedLimitApiItem.createdTime 应为 number（后端 int64），现为 \`${slCreated}\``);
  const expDecl = fieldDecl(tsInterfaceBlock(typesTs, "UserMutationPayload"), "expTime");
  if (expDecl !== null && expDecl.includes("string"))
    problems.push(`UserMutationPayload.expTime 不应含 string（ISO 字符串会被后端静默回退默认到期），现为 \`${expDecl}\``);
  if (problems.length) problems.forEach((p) => fail("C6", p));
  else pass("C6", "幻影/缺失字段簇全部对齐");
}

/* ---------------- C7: 页面本地类型副本 ---------------- */
{
  const problems = [];
  const check = (rel, iface, field, { mustNull = false, mustNotContain = null, absent = false } = {}) => {
    const block = tsInterfaceBlock(readCode(rel, false), iface);
    if (!block) {
      problems.push(`${rel} 未找到本地 interface ${iface}（字段 ${field}）`);
      return;
    }
    const decl = fieldDecl(block, field);
    if (absent) {
      if (decl !== null) problems.push(`${rel} ${iface}.${field} 应删除（幻影字段），现为 \`${decl}\``);
      return;
    }
    if (decl === null) {
      problems.push(`${rel} ${iface}.${field} 未声明`);
      return;
    }
    if (mustNull && !decl.includes("| null"))
      problems.push(`${rel} ${iface}.${field} 应含 | null，现为 \`${decl}\``);
    if (mustNotContain && decl.includes(mustNotContain))
      problems.push(`${rel} ${iface}.${field} 不应含 ${mustNotContain}，现为 \`${decl}\``);
  };

  // limit.tsx 本地 SpeedLimitRule（与 SpeedLimitApiItem 同形）
  check(`${FE}/pages/limit.tsx`, "SpeedLimitRule", "createdTime", { mustNotContain: "string" });
  check(`${FE}/pages/limit.tsx`, "SpeedLimitRule", "updatedTime", { mustNull: true, mustNotContain: "string" });
  // dashboard 本地 DashboardUserInfo（expTime 后端 int64 毫秒）
  check(`${FE}/pages/dashboard/use-dashboard-data.ts`, "DashboardUserInfo", "expTime", { mustNotContain: "string" });
  // forward.tsx 本地 Tunnel（由 getTunnelList 直接赋值，inIp 可为 null）
  check(`${FE}/pages/forward.tsx`, "Tunnel", "inIp", { mustNull: true });
  // forward.tsx / tunnel.tsx 本地 Node（serverIpV4/V6/extraIPs 后端 nullable 下发）
  for (const rel of [`${FE}/pages/forward.tsx`, `${FE}/pages/tunnel.tsx`]) {
    check(rel, "Node", "serverIpV4", { mustNull: true });
    check(rel, "Node", "serverIpV6", { mustNull: true });
    check(rel, "Node", "extraIPs", { mustNull: true });
  }
  // node.tsx 本地 Node（列表经 any 映射透传，null 会原样进入本地模型）
  for (const f of ["remark", "expiryTime", "renewalCycle", "serverIpV4", "serverIpV6", "extraIPs", "version", "remoteUrl"])
    check(`${FE}/pages/node/types.ts`, "Node", f, { mustNull: true });
  // src/types/index.ts 第二套类型：Tunnel 不得再声明幻影必填 entry/exitNodeId
  check(`${FE}/types/index.ts`, "Tunnel", "entryNodeId", { absent: true });
  check(`${FE}/types/index.ts`, "Tunnel", "exitNodeId", { absent: true });

  if (problems.length) problems.forEach((p) => fail("C7", p));
  else pass("C7", "页面本地类型副本与线缆契约同步");
}

/* ---------------- C8: TuneProbe 回包字段 ---------------- */
// 线路探针回包结构 tuneProbeResult（agent，go-gost/x/socket/
// tuning_probe.go）与前端消费类型 NodeTuneProbeResult（api/index.ts）：
//   (a) 前端声明字段 ⊆ agent json tag 集——前端查不存在的字段恒
//       为 undefined，渲染恒空，属静默漂移；
//   (b) C2-01 裁定的新字段集两侧齐在（sock_diag 分布三件＋参考
//       腿序列七件）。agent 另有 suggested/bandwidth* 等前端不声
//       明的字段，故只单向断言，不反向要求 agent ⊆ 前端。
{
  const problems = [];
  const agentProbeGo = readCode("go-gost/x/socket/tuning_probe.go", true);
  const structStart = agentProbeGo.search(/type tuneProbeResult struct \{/);
  let agentTags = null;
  if (structStart >= 0) {
    const open = agentProbeGo.indexOf("{", structStart);
    const end = scanBlockEnd(agentProbeGo, open);
    const body = end < 0 ? agentProbeGo.slice(open) : agentProbeGo.slice(open, end + 1);
    agentTags = new Set(
      [...body.matchAll(/json:"([A-Za-z0-9_]+)/g)].map((m) => m[1]),
    );
  }
  if (agentTags === null) {
    problems.push("go-gost/x/socket/tuning_probe.go 未找到 tuneProbeResult 结构");
  } else if (agentTags.size < 20) {
    problems.push(`tuneProbeResult json tag 提取异常（仅 ${agentTags.size} 条，疑似定位失效）`);
  }
  const feBlock = tsInterfaceBlock(
    readCode(`${FE}/api/index.ts`, false),
    "NodeTuneProbeResult",
  );
  const feFields = new Set();
  if (feBlock) {
    for (const m of feBlock.matchAll(/^\s+([A-Za-z0-9_]+)\??:/gm))
      feFields.add(m[1]);
  }
  if (!feBlock) {
    problems.push("api/index.ts 未找到 NodeTuneProbeResult interface");
  } else if (feFields.size < 10) {
    problems.push(`NodeTuneProbeResult 字段提取异常（仅 ${feFields.size} 条，疑似定位失效）`);
  }
  if (agentTags && feBlock) {
    for (const f of feFields)
      if (!agentTags.has(f))
        problems.push(`NodeTuneProbeResult.${f} 在 agent tuneProbeResult 无对应 json tag（前端恒 undefined）`);
    const probeNewFields = [
      "rttMinMs",
      "rttP95Ms",
      "rttMaxMs",
      "panelRttSent",
      "panelRttReceived",
      "panelRttLossRate",
      "panelRttMinMs",
      "panelRttP95Ms",
      "panelRttMaxMs",
      "panelRttJitterMs",
    ];
    for (const f of probeNewFields) {
      if (!agentTags.has(f))
        problems.push(`C2-01 新探针字段 ${f} 在 agent tuneProbeResult 缺失`);
      if (!feFields.has(f))
        problems.push(`C2-01 新探针字段 ${f} 在前端 NodeTuneProbeResult 未声明`);
    }
  }
  if (problems.length) problems.forEach((p) => fail("C8", p));
  else pass("C8", "TuneProbe 回包字段与前端消费类型零漂移（C2-01 新字段双向齐）");
}

/* ---------------- 白名单（已登记设计差异，仅公示） ---------------- */
console.log("\n已登记的设计性差异（白名单，不拦截）：");
for (const item of whitelist.registeredDesignDifferences || [])
  console.log(`  - ${item.item}：${item.basis}`);

console.log(
  failures === 0
    ? "\n契约门禁：全部通过"
    : `\n契约门禁：${failures} 项失败`,
);
process.exit(failures === 0 ? 0 : 1);
