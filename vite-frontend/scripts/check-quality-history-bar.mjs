// check-quality-history-bar.mjs — 质量历史条段级着色回归校验（无测试框架版）。
//
// R3-B07-003：UptimeHistoryBar 旧实现以整行 errorMessage 首判，把同一
// 行两段同时涂红——仅出口→目标段失败时，实测健康的入口→出口段被
// 误报为错误。本脚本把 src/pages/node/quality-history-bar.ts 用
// typescript.transpileModule 转译后在 Node 里执行并断言（与
// check-pure-enc-preset.mjs 同型做法；该模块只有 import type，
// 转译产物无运行时 import，可直接跑）。
//
// 断言覆盖：
//   ① 本条缺陷形态：入口段实测健康 + 整行 errorMessage → 入口段仍
//      「优秀」绿、出口段（哨兵）「错误」红；
//   ② 入口段失败 + 出口段实测健康（入口离线时出口段仍独立探测）
//      → 入口红、出口绿，归因不反串；
//   ③ Round 2 口径不回退：成功行里未测段（类型1 不存在的入口→
//      出口段，-1/100 哨兵、无 errorMessage）→ 中性「无数据」；
//   ④ 整轮失败（两段皆哨兵 + errorMessage）→ 两段皆「错误」；
//   ⑤ 实测着色梯度不变：丢包红、>200ms 极差红、>100ms 较差黄。
//   ⑥ 部分未测（R8-024）：入口段部分和行着色仍按实测值，但状态
//      文案附「（部分未测）」，与全实测行可区分；出口段不受影响。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-quality-history-bar.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
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

const srcPath = join(frontendDir, "src/pages/node/quality-history-bar.ts");
const source = readFileSync(srcPath, "utf8");

const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
  reportDiagnostics: true,
});

// 纯函数不得有运行时依赖：转译产物里不应出现 require("...")。
const runtimeRequires = [...outputText.matchAll(/require\(["']([^"']+)["']\)/g)].map(
  (m) => m[1],
);
const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

check("转译产物无运行时 require", runtimeRequires.length === 0,
  `发现 require: ${runtimeRequires.join(", ")}`);

const mod = { exports: {} };
new Function("module", "exports", "require", outputText)(
  mod,
  mod.exports,
  require,
);
const { classifyQualityHistoryBar } = mod.exports;
check("导出 classifyQualityHistoryBar", typeof classifyQualityHistoryBar === "function",
  `typeof = ${typeof classifyQualityHistoryBar}`);

if (typeof classifyQualityHistoryBar === "function") {
  const row = (over) => ({
    tunnelId: 1,
    entryToExitLatency: -1,
    entryToExitLoss: 100,
    exitToBingLatency: -1,
    exitToBingLoss: 100,
    success: false,
    timestamp: 1700000000000,
    ...over,
  });
  const expectBar = (label, q, segment, colorClass, statusText) => {
    const got = classifyQualityHistoryBar(q, segment);
    check(label,
      got.colorClass === colorClass && got.statusText === statusText,
      `实际 ${JSON.stringify(got)}，期望 ${colorClass}/${statusText}`);
  };

  // ① 缺陷形态：出口→目标段失败落库的真实行（入口段实测 30ms/0%）。
  const exitFailed = row({
    entryToExitLatency: 30,
    entryToExitLoss: 0,
    errorMessage: "探测失败",
  });
  expectBar("①入口段实测健康不被整行错误牵连", exitFailed, "entryToExit", "bg-success", "优秀");
  expectBar("①出口段哨兵+行错误 → 错误", exitFailed, "exitToBing", "bg-danger", "错误");

  // ② 入口离线：入口段未测、出口段独立探测健康。
  const entryOffline = row({
    exitToBingLatency: 40,
    exitToBingLoss: 0,
    errorMessage: "入口节点均不在线",
  });
  expectBar("②入口段未测+行错误 → 错误", entryOffline, "entryToExit", "bg-danger", "错误");
  expectBar("②出口段实测健康 → 优秀", entryOffline, "exitToBing", "bg-success", "优秀");

  // ③ 类型1 成功行：入口→出口段不存在（恒哨兵、无行错误）。
  const type1OK = row({
    exitToBingLatency: 12.5,
    exitToBingLoss: 0,
    success: true,
  });
  delete type1OK.errorMessage;
  expectBar("③未测段无错误 → 中性无数据", type1OK, "entryToExit",
    "bg-default-200 dark:bg-default-100/50", "无数据");
  expectBar("③实测段 → 优秀", type1OK, "exitToBing", "bg-success", "优秀");

  // ④ 整轮前置失败：两段皆哨兵 + 行错误。
  const wholeRowFailed = row({ errorMessage: "隧道配置不完整" });
  expectBar("④整轮失败入口段 → 错误", wholeRowFailed, "entryToExit", "bg-danger", "错误");
  expectBar("④整轮失败出口段 → 错误", wholeRowFailed, "exitToBing", "bg-danger", "错误");

  // ⑤ 实测梯度。
  const lossy = row({ entryToExitLatency: 50, entryToExitLoss: 25, success: true });
  delete lossy.errorMessage;
  expectBar("⑤丢包段 → 红/丢包", lossy, "entryToExit", "bg-danger", "丢包 25.0%");
  const slow = row({ exitToBingLatency: 150, exitToBingLoss: 0, success: true });
  delete slow.errorMessage;
  expectBar("⑤150ms → 黄/较差", slow, "exitToBing", "bg-warning", "较差");
  const awful = row({ exitToBingLatency: 250, exitToBingLoss: 0, success: true });
  delete awful.errorMessage;
  expectBar("⑤250ms → 红/极差", awful, "exitToBing", "bg-danger", "极差");

  // ⑥ 部分未测（R8-024）：入口段总值为部分和——着色按实测值、
  // 文案附部分未测；同行出口段与无标记行不受影响。
  const partial = row({
    entryToExitLatency: 30,
    entryToExitLoss: 0,
    entryToExitPartialUntested: true,
    exitToBingLatency: 40,
    exitToBingLoss: 0,
    success: true,
  });
  delete partial.errorMessage;
  expectBar("⑥部分未测入口段 → 绿/优秀（部分未测）", partial, "entryToExit",
    "bg-success", "优秀（部分未测）");
  expectBar("⑥同行出口段不附注记", partial, "exitToBing", "bg-success", "优秀");
  const partialSlow = row({
    entryToExitLatency: 150,
    entryToExitLoss: 0,
    entryToExitPartialUntested: true,
    success: true,
  });
  delete partialSlow.errorMessage;
  expectBar("⑥部分未测较差段 → 黄/较差（部分未测）", partialSlow, "entryToExit",
    "bg-warning", "较差（部分未测）");
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
