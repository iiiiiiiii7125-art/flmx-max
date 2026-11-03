#!/usr/bin/env node
// check-split-referrer-filter.mjs — 删除确认弹窗引用清单口径钉（R2-039）。
//
// node.tsx 的 computeBrokenReferrers 曾对任何带 outboundId 的 splitRules
// 计数，而后端权威口径 outboundTargets（kernel_chain.go）只计
// Action == SplitProxy 的规则：direct/block 规则残留的非零 outboundId
// 不是有效引用，前端弹窗会多报「将被打断的引用」。
//
// 提取面（修复轮 6，R6-C4-15 机制处置）：旧实现把函数体切到下一
// 个顶层声明、对原文做链形正则——真实谓词翻成 "direct" 后，在函
// 数体内以块注释逐字引下被钉链形即可满足正向钉（三断言全绿；行
// 注释形态因 // 混入链节不满足，块注释可满足）。现提取先词法剥
// 注释（字符串保留），再按括号配平（跳过字符串/模板）截取函数
// 体，注释引链整体消失、谓词翻转随之点名。后端口径本身另有后端
// 测试钉住 outboundTargets，不在本门范围。
//
// 行为口径（修复轮 7，R7-11 / G-C4-05 机制处置）：轮 6 的剥注释＋
// 链形正则仍可被死代码满足——真实谓词翻成 "direct"、再在函数体
// 内加一段 if (false) 死分支逐字写下被钉链形，四断言全绿而行为
// 已反转（与 tunnel 门轮 6 的 TUN-N3 同族；该谓词是全族最后一个
// 纯文本钉的语义谓词，tunnel ① 已于轮 6 改行为断言）。本轮改
// 行为断言：以 TypeScript 编译器 API 在 AST 上定位 node.tsx 的
// 顶层函数声明 computeBrokenReferrers（模板字符串/注释里的同名
// 文本不是声明、天然不入集；同名顶层声明多于一份即前提失效），
// 取其原文单独转译执行，以七格夹具实测谓词行为：proxy 规则计
// 入、direct/block 残留不计、本体 outboundId 计入、引用方自身
// 在被删节点时排除、规则 outboundId 缺失按 0、同引用方多命中
// 去重。文本形态（注释引链、死分支、字符串拷贝）自此与判定无
// 关。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-split-referrer-filter.mjs
// 退出码 0 = 通过，非 0 = 失败。
// 本脚本由 run-checks.mjs 按目录自动枚举接线，无需另行登记。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");

let ts;
try {
  ts = require("typescript");
} catch {
  // worktree 尚未 pnpm install 时，退回主仓的 node_modules。
  ts = require(
    "/home/hatch/workspace/flvx-max/vite-frontend/node_modules/typescript",
  );
}

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

const srcPath = join(frontendDir, "src/pages/node.tsx");
const src = readFileSync(srcPath, "utf8");
const sf = ts.createSourceFile(
  "node.tsx",
  src,
  ts.ScriptTarget.ES2020,
  true,
  ts.ScriptKind.TSX,
);
const decls = sf.statements.filter(
  (s) =>
    ts.isFunctionDeclaration(s) && s.name?.text === "computeBrokenReferrers",
);
check(
  "computeBrokenReferrers 顶层函数声明恰一份（AST 定位）",
  decls.length === 1,
  `实测 ${decls.length} 份`,
);

let fn = null;
if (decls.length === 1) {
  const fnText =
    src.slice(decls[0].getStart(), decls[0].getEnd()) +
    "\nmodule.exports = computeBrokenReferrers;";
  const { outputText } = ts.transpileModule(fnText, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2020,
    },
  });
  check(
    "函数转译产物无运行时 require（自包含可执行）",
    !/require\(/.test(outputText),
  );
  if (!/require\(/.test(outputText)) {
    const mod = { exports: {} };
    try {
      new Function("module", "exports", "require", outputText)(
        mod,
        mod.exports,
        () => {
          throw new Error("runtime require not allowed");
        },
      );
      fn = mod.exports;
    } catch (e) {
      check("computeBrokenReferrers 可执行", false, String(e));
    }
  }
}
check("computeBrokenReferrers 可执行", typeof fn === "function");

if (typeof fn === "function") {
  const target = {
    id: 10,
    nodeId: 2,
    name: "tgt",
    nodeName: "N2",
    outboundId: 0,
    splitRules: [],
  };
  const deleted = new Set([2]);
  const referrer = (over) => ({
    id: 1,
    nodeId: 1,
    name: "A",
    nodeName: "N1",
    outboundId: 0,
    splitRules: [],
    ...over,
  });
  const run = (inbounds) => fn(inbounds, deleted);
  const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);

  check(
    "proxy 规则指向被删节点入站 → 计入，呈现 名称（节点名）",
    eq(
      run([
        target,
        referrer({ splitRules: [{ action: "proxy", outboundId: 10 }] }),
      ]),
      ["A（N1）"],
    ),
  );
  check(
    "direct 规则残留 outboundId → 不计入（R2-039）",
    eq(
      run([
        target,
        referrer({ splitRules: [{ action: "direct", outboundId: 10 }] }),
      ]),
      [],
    ),
  );
  check(
    "block 规则残留 outboundId → 不计入",
    eq(
      run([
        target,
        referrer({ splitRules: [{ action: "block", outboundId: 10 }] }),
      ]),
      [],
    ),
  );
  check(
    "本体 outboundId 指向目标 → 计入",
    eq(run([target, referrer({ outboundId: 10 })]), ["A（N1）"]),
  );
  check(
    "引用方自身在被删节点 → 不计入",
    eq(
      run([
        target,
        referrer({
          nodeId: 2,
          outboundId: 10,
          splitRules: [{ action: "proxy", outboundId: 10 }],
        }),
      ]),
      [],
    ),
  );
  check(
    "proxy 规则 outboundId 缺失（按 0）→ 不计入",
    eq(run([target, referrer({ splitRules: [{ action: "proxy" }] })]), []),
  );
  check(
    "同一引用方多条命中 → 按入站 id 去重只报一次",
    eq(
      run([
        target,
        referrer({
          outboundId: 10,
          splitRules: [
            { action: "proxy", outboundId: 10 },
            { action: "proxy", outboundId: 10 },
          ],
        }),
      ]),
      ["A（N1）"],
    ),
  );
}

if (failures.length > 0) {
  console.log(`\n失败 ${failures.length} 项：${failures.join("；")}`);
  process.exit(1);
}
console.log("\n删除引用清单口径钉：行为实测与后端 outboundTargets 对齐");
