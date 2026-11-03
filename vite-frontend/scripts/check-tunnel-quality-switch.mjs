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

// 词法剥注释：把 // 与 /* */ 注释逐字替换为空格（换行保留，故行号
// 与列位不变），字符串/模板字面量内容原样保留。提取面先经此处理，
// 注释中的拷贝既不能满足正向钉、也不能打断调用点形态。
const stripComments = (src) => {
  const out = [];
  const n = src.length;
  let i = 0;
  let mode = "code"; // code | line | block | sq | dq | tpl
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
      else if (c === "`") mode = "tpl";
      else if (c === "{") braceDepth++;
      else if (c === "}") {
        if (tplDepths.length && braceDepth === tplDepths[tplDepths.length - 1]) {
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
    // tpl
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

// 2. 全 src 扫描：质量键标识与布尔字面量的直接比较（剥注释后逐行）。
const keyIdentifier =
  /monitor_tunnel_quality_enabled|TUNNEL_QUALITY_ENABLED/;
const boolCompare =
  /(===|!==)\s*["'](true|false)["']|["'](true|false)["']\s*(===|!==)/;
const sourceFiles = readdirSync(srcRoot, { recursive: true })
  .map((entry) => join(srcRoot, entry))
  .filter((path) => /\.(ts|tsx)$/.test(path));

for (const file of sourceFiles) {
  if (file === truthFile) continue;
  const lines = stripComments(readFileSync(file, "utf8")).split("\n");
  lines.forEach((line, idx) => {
    if (keyIdentifier.test(line) && boolCompare.test(line)) {
      fail(
        `${relative(frontendRoot, file)}:${idx + 1} 质量键被直接与布尔字面量比较，应改走 resolveTunnelQualityEnabled`,
      );
    }
  });
}

// 3. 隧道监控页自读面（剥注释后判定：注释拷贝不算调用、注释中断
//    不再漏检 .value 直读）。
const monitorViewFile = join(srcRoot, "pages", "node", "tunnel-monitor-view.tsx");
const monitorViewCode = stripComments(readFileSync(monitorViewFile, "utf8"));
monitorViewCode.split("\n").forEach((line, idx) => {
  if (/\.value\s*(===|!==)\s*["'](true|false)["']/.test(line)) {
    fail(
      `src/pages/node/tunnel-monitor-view.tsx:${idx + 1} 对配置值 .value 直接判读，应改走 resolveTunnelQualityEnabled`,
    );
  }
});
if (!/resolveTunnelQualityEnabled\(/.test(monitorViewCode)) {
  fail("tunnel-monitor-view.tsx 未调用 resolveTunnelQualityEnabled");
}

// 4. config 页保存广播面（剥注释后定位锚点并截窗：注释里的广播名
//    拷贝不能劫持锚点、注释里的判读形态不能满足窗口断言）。
const configPageFile = join(srcRoot, "pages", "config.tsx");
const configPageCode = stripComments(readFileSync(configPageFile, "utf8"));
const broadcastAt = configPageCode.indexOf("monitorTunnelQualityEnabledChanged");
if (broadcastAt < 0) {
  fail("config.tsx 未找到 monitorTunnelQualityEnabledChanged 广播");
} else {
  const block = configPageCode.slice(broadcastAt, broadcastAt + 900);
  if (
    !/enabled:\s*(resolveSwitchSelected|resolveTunnelQualityEnabled)\(/.test(
      block,
    )
  ) {
    fail(
      "config.tsx 保存广播的 enabled 未经 resolveSwitchSelected/resolveTunnelQualityEnabled 判读",
    );
  }
}

if (failures.length) {
  console.error("隧道质量开关判读单点门禁失败：");
  failures.forEach((msg) => console.error(`  - ${msg}`));
  process.exit(1);
}

console.log(
  "隧道质量开关判读单点门禁通过：真源函数行为实测合口径，全仓判读收敛 resolveTunnelQualityEnabled",
);
