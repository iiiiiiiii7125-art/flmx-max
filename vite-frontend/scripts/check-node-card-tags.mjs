#!/usr/bin/env node
// check-node-card-tags.mjs — 节点卡片三标签＋流量进度条回归校验（X06）。
//
// 1.2.0 的「用户/分组/限速三标签＋流量进度条」在后续卡片重构中被
// 移除、无替代面（AUDIT-HIST L5 X06）。本门断言：
//   ① 推导纯函数 computeNodeCardTags 的口径（活路转发＋启用隧道
//      的去重用户数；限速名解名/兜底；暂停转发与禁用隧道不计入；
//      无关节点不受牵连）；
//   ② 流量进度条刻度 trafficDownShare（零流量不渲染；上下行分段）；
//   ③ 渲染接线：三标签组件与进度条在卡片、列表两视图均在位
//      （F-211-02：只放一面等于没恢复），节点页拉取三张清单并
//      把推导结果传给两视图。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-node-card-tags.mjs
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
  const envTs = process.env.FLMX_CHECK_TYPESCRIPT;
  if (envTs) {
    ts = require(envTs);
    console.log(
      `NOTE  typescript 由 FLMX_CHECK_TYPESCRIPT 显式指向工作树外副本：${envTs}（v${ts.version}）`,
    );
  } else {
    console.log(
      "FAIL  typescript 无法自当前工作树解析（vite-frontend/node_modules 缺失）——先 pnpm install，或以 FLMX_CHECK_TYPESCRIPT=<typescript 路径> 显式指向外部副本",
    );
    process.exit(1);
  }
}

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

const srcPath = join(frontendDir, "src/pages/node/card-tags.ts");
const source = readFileSync(srcPath, "utf8");
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
  reportDiagnostics: true,
});

const runtimeRequires = [...outputText.matchAll(/require\(["']([^"']+)["']\)/g)].map(
  (m) => m[1],
);
check("推导模块无运行时 require（纯函数）", runtimeRequires.length === 0,
  `发现 require: ${runtimeRequires.join(", ")}`);

const mod = { exports: {} };
new Function("module", "exports", "require", outputText)(
  mod,
  mod.exports,
  require,
);
const { computeNodeCardTags, trafficDownShare } = mod.exports;
check("导出 computeNodeCardTags", typeof computeNodeCardTags === "function",
  `typeof = ${typeof computeNodeCardTags}`);
check("导出 trafficDownShare", typeof trafficDownShare === "function",
  `typeof = ${typeof trafficDownShare}`);

// —— ① 推导口径 ——
if (typeof computeNodeCardTags === "function") {
  const tunnel = (over) => ({
    id: 10,
    name: "t",
    type: 1,
    status: 1,
    inNodeId: [{ nodeId: 1 }],
    outNodeId: [{ nodeId: 2 }],
    chainNodes: [[{ nodeId: 3 }]],
    ...over,
  });
  const forward = (over) => ({
    id: 100,
    name: "f",
    status: 1,
    userId: 7,
    tunnelId: 10,
    speedId: 5,
    ...over,
  });
  const rules = [{ id: 5, name: "限速A", speed: 1024, status: 1, createdTime: 0, updatedTime: null }];

  // 活路：同一用户两条转发去重；链上入口/出口/中间跳三节点都计入。
  const live = computeNodeCardTags(
    [1, 2, 3, 9],
    [forward({ id: 100 }), forward({ id: 101, speedId: null })],
    [tunnel()],
    rules,
  );
  for (const nodeId of [1, 2, 3]) {
    const tags = live.get(nodeId);
    check(`活路节点 ${nodeId}：用户去重为 1 人`,
      tags?.userCount === 1, `实际 ${JSON.stringify(tags)}`);
    check(`活路节点 ${nodeId}：限速解名为限速A`,
      JSON.stringify(tags?.speedLimitNames) === JSON.stringify(["限速A"]),
      `实际 ${JSON.stringify(tags)}`);
  }
  const untouched = live.get(9);
  check("链外节点：用户 0 人、无限速名",
    untouched?.userCount === 0 && untouched?.speedLimitNames.length === 0,
    `实际 ${JSON.stringify(untouched)}`);

  // 暂停转发不计入（用户与限速都不落）。
  const paused = computeNodeCardTags(
    [1],
    [forward({ status: 0 })],
    [tunnel()],
    rules,
  );
  check("暂停转发：用户 0 人、无限速名",
    paused.get(1)?.userCount === 0 && paused.get(1)?.speedLimitNames.length === 0,
    `实际 ${JSON.stringify(paused.get(1))}`);

  // 禁用隧道不计入。
  const disabledTunnel = computeNodeCardTags(
    [1],
    [forward()],
    [tunnel({ status: 0 })],
    rules,
  );
  check("禁用隧道：用户 0 人、无限速名",
    disabledTunnel.get(1)?.userCount === 0 &&
      disabledTunnel.get(1)?.speedLimitNames.length === 0,
    `实际 ${JSON.stringify(disabledTunnel.get(1))}`);

  // 限速 id 解不出时以「规则#id」原样示出，不静默丢弃；IP 限速名直取。
  const unknownRule = computeNodeCardTags(
    [1],
    [forward({ speedId: 99, ipSpeedId: null, ipSpeedLimitName: "IP限速B" })],
    [tunnel()],
    rules,
  );
  check("未知限速 id 兜底为规则#id，IP 限速名直取",
    JSON.stringify(unknownRule.get(1)?.speedLimitNames) ===
      JSON.stringify(["规则#99", "IP限速B"]),
    `实际 ${JSON.stringify(unknownRule.get(1))}`);

  // 同名限速去重。
  const dupNames = computeNodeCardTags(
    [1],
    [forward({ speedId: 5 }), forward({ id: 102, speedId: 5 })],
    [tunnel()],
    rules,
  );
  check("同名限速去重为一条",
    JSON.stringify(dupNames.get(1)?.speedLimitNames) === JSON.stringify(["限速A"]),
    `实际 ${JSON.stringify(dupNames.get(1))}`);
}

// —— ② 流量进度条刻度 ——
if (typeof trafficDownShare === "function") {
  check("零流量不渲染（null）", trafficDownShare([0, 0]) === null,
    `实际 ${trafficDownShare([0, 0])}`);
  check("无数据不渲染（null）", trafficDownShare(undefined) === null,
    `实际 ${trafficDownShare(undefined)}`);
  check("上下行各半为 50", trafficDownShare([100, 100]) === 50,
    `实际 ${trafficDownShare([100, 100])}`);
  check("纯下行为 100", trafficDownShare([100, 0]) === 100,
    `实际 ${trafficDownShare([100, 0])}`);
  check("纯上行为 0", trafficDownShare([0, 100]) === 0,
    `实际 ${trafficDownShare([0, 100])}`);
}

// —— ③ 渲染接线（源码断言） ——
const rowSource = readFileSync(
  join(frontendDir, "src/pages/node/node-row.tsx"),
  "utf8",
);
const pageSource = readFileSync(
  join(frontendDir, "src/pages/node.tsx"),
  "utf8",
);
const has = (src, needle, label) =>
  check(label, src.includes(needle), `缺失：${needle}`);
has(rowSource, "data-node-card-tags", "三标签容器锚点 data-node-card-tags 在位");
has(rowSource, "data-traffic-progress", "流量进度条锚点 data-traffic-progress 在位");
has(rowSource, 'role="progressbar"', "流量进度条 role=progressbar 在位");
check("卡片视图渲染三标签",
  (rowSource.match(/<NodeCardTagChips/g) || []).length >= 2,
  `NodeCardTagChips 出现 ${(rowSource.match(/<NodeCardTagChips/g) || []).length} 次（卡片＋列表各一）`);
check("卡片视图渲染流量进度条",
  (rowSource.match(/<NodeTrafficProgressBar/g) || []).length >= 2,
  `NodeTrafficProgressBar 出现 ${(rowSource.match(/<NodeTrafficProgressBar/g) || []).length} 次（卡片＋列表各一）`);
has(rowSource, "用户 ", "用户标签文案在位");
has(rowSource, "分组：", "分组标签文案在位");
has(rowSource, "不限速", "限速标签兜底文案在位");
has(pageSource, "getForwardList", "节点页拉取转发清单");
has(pageSource, "getTunnelList", "节点页拉取隧道清单");
has(pageSource, "getSpeedLimitList", "节点页拉取限速规则清单");
has(pageSource, "computeNodeCardTags", "节点页调用标签推导");
check("两视图均传入 cardTags",
  (pageSource.match(/cardTags=\{cardTags\?\.get\(node\.id\)/g) || []).length >= 2,
  "cardTags 只传了一面或未传");

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
