#!/usr/bin/env node
// check-small-text-weight.mjs — G5 小字字重门（仓内等价物）。
//
// 口径（Round 6 用户定规，UI 视觉审计常设 G5）：12px 级小字说明
// 正文必须带字重——text-xs / text-[10px] / text-[11px] 字面量内
// 无 font-semibold 及更重字重（font-semibold|bold|extrabold|
// black）即「小字无字重」族：小字以常规字重呈现时在实色面上发
// 虚，用户定规以字号/字重而非灰度区分层级，小字一律加粗。
//
// 来历：G5 此前只活在仓外审计脚本（audit/ui-refresh/
// check-tray-contrast.sh 的 G5 节，随审计轮次手工实跑），未接
// 入 run-checks/CI——第九轮 I-C5-02 在移植新增的节点弹窗里攻
// 出 3 处正文位（run-checks 13 门无一命中）。本门是其仓内等价
// 物：谓词与外置版逐项一致（全 src 的 .ts/.tsx 字符串字面量扫
// 描——工厂函数在同一字面量内组字重，整文件字面量扫是闭合
// 形），随 run-checks 目录枚举自动入册常跑。
//
// 边界（LEDGER9 R9-13 裁定，随 I-C5-02 定性）：表单控件本体
// 内的等宽配置文本不在本门口径内——控件里是用户配置原文、字
// 色随控件前景，非说明正文。命中字面量若经 TS 语法树归属为
// 表单控件元素（原生 textarea/input/select 或桥接组件
// Textarea/Input/Select）的 className，列为登记保留并逐条打
// 印（不计失败）；其余命中一律失败点名。登记保留的新增只能
// 来自新的控件本体用途，扩展 FORM_CONTROL_TAGS 须有裁定依据。
//
// typescript 不可解析时退出码 2 fail-closed（与 timeout 门同
// 口径）：归属判定依赖真解析器，缺解析器不许假绿。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-small-text-weight.mjs
// 退出码 0 = 通过，1 = 有违规，2 = 环境不全（解析器缺失/普查为空）。
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");
const srcDir = join(frontendDir, "src");

let ts;

try {
  ts = require("typescript");
} catch {
  // R4-C6-04：同其它 TS 类门——只许当前工作树解析；外部副本须经
  // FLMX_CHECK_TYPESCRIPT 显式指向并点名，不再静默回退写死路径。
  const envTs = process.env.FLMX_CHECK_TYPESCRIPT;

  if (envTs) {
    ts = require(envTs);
    console.log(
      `NOTE  typescript 由 FLMX_CHECK_TYPESCRIPT 显式指向工作树外副本：${envTs}（v${ts.version}）`,
    );
  } else {
    console.log(
      "FAIL  typescript 无法自当前工作树解析——G5 归属判定依赖真解析器，fail-closed（先 pnpm install，或以 FLMX_CHECK_TYPESCRIPT=<typescript 路径> 显式指向外部副本）",
    );
    process.exit(2);
  }
}

// ─── 普查面：src 全树 .ts/.tsx ─────────────────────────────────────────────

const collectSources = (dir) => {
  const out = [];

  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);

    if (entry.isDirectory()) out.push(...collectSources(path));
    else if (/\.tsx?$/.test(entry.name)) out.push(path);
  }

  return out;
};

// ─── 谓词（与外置 G5 逐项一致） ────────────────────────────────────────────

const SIZE_RE = /text-xs\b|text-\[10px\]|text-\[11px\]/;
const WEIGHT_RE = /font-(semibold|bold|extrabold|black)/;
const LITERAL_RE = /"([^"\n]*)"|'([^'\n]*)'|`([^`]*)`/g;

// 表单控件元素（原生 + 桥接组件）：其 className 内的文本是控件
// 本体内容（用户配置原文），按 R9-13 裁定登记保留。
const FORM_CONTROL_TAGS = new Set([
  "textarea",
  "input",
  "select",
  "Textarea",
  "Input",
  "Select",
]);

// 命中字面量若落在某表单控件元素的 className 属性初始化式范围
// 内，归属该控件。JSX 归属经 TS 语法树取 JsxOpeningElement /
// JsxSelfClosingElement 的 className 属性节点跨度判定。
const formControlClassNameSpans = (path, text) => {
  const kind = path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, kind);
  const spans = [];
  const visit = (node) => {
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      if (FORM_CONTROL_TAGS.has(node.tagName.getText(sf))) {
        for (const attr of node.attributes.properties) {
          if (
            ts.isJsxAttribute(attr) &&
            attr.name.getText(sf) === "className" &&
            attr.initializer
          ) {
            spans.push([attr.initializer.getStart(sf), attr.initializer.getEnd()]);
          }
        }
      }
    }
    ts.forEachChild(node, visit);
  };

  visit(sf);

  return spans;
};

// ─── 扫描 ──────────────────────────────────────────────────────────────────

const files = collectSources(srcDir).sort();

if (files.length === 0) {
  console.log("FAIL  src 普查为空——门禁清单异常，fail-closed");
  process.exit(2);
}

let literalCount = 0;
const keeps = [];
const violations = [];

for (const path of files) {
  const text = readFileSync(path, "utf8");
  const rel = path.slice(frontendDir.length + 1);
  let spans = null;

  for (const m of text.matchAll(LITERAL_RE)) {
    const body = m[1] ?? m[2] ?? m[3] ?? "";

    literalCount++;
    if (!SIZE_RE.test(body) || WEIGHT_RE.test(body)) continue;
    const start = m.index;
    const end = start + m[0].length;
    const line = text.slice(0, start).split("\n").length;

    if (spans === null) spans = formControlClassNameSpans(path, text);
    const isControl = spans.some(([s, e]) => start >= s && end <= e);
    const record = `${rel}:${line}: ${body.slice(0, 80)}`;

    if (isControl) keeps.push(record);
    else violations.push(record);
  }
}

console.log(
  `G5 普查：${files.length} 个源文件、${literalCount} 个字符串字面量，` +
    `小字无字重命中 ${keeps.length + violations.length} 处` +
    `（登记保留 ${keeps.length} 处＝表单控件本体，违规 ${violations.length} 处）`,
);
for (const k of keeps) console.log(`REGISTERED  ${k}（表单控件本体，R9-13 裁定保留）`);
for (const v of violations) console.log(`FAIL  G5 小字无字重：${v}`);

if (violations.length > 0) {
  console.log(
    `RESULT: FAIL（${violations.length} 处说明正文小字无 semibold+ 字重）`,
  );
  process.exit(1);
}
console.log("RESULT: PASS");
