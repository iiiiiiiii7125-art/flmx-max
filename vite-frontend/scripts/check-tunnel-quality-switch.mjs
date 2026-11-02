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
// 供 CI / 发版前检查调用：pnpm run check:tunnel-quality-switch
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const srcRoot = join(frontendRoot, "src");
const truthFile = join(srcRoot, "config", "tunnel-quality.ts");

const failures = [];
const fail = (msg) => failures.push(msg);

// 1. 真源函数与语义。
const truthSource = readFileSync(truthFile, "utf8");
if (!/export const resolveTunnelQualityEnabled/.test(truthSource)) {
  fail("src/config/tunnel-quality.ts 缺少 resolveTunnelQualityEnabled 导出");
}
if (!/!==\s*"false"/.test(truthSource)) {
  fail('resolveTunnelQualityEnabled 语义漂移：应为 !== "false"（缺失＝开启）');
}

// 2. 全 src 扫描：质量键标识与布尔字面量的直接比较。
const keyIdentifier =
  /monitor_tunnel_quality_enabled|TUNNEL_QUALITY_ENABLED/;
const boolCompare =
  /(===|!==)\s*["'](true|false)["']|["'](true|false)["']\s*(===|!==)/;
const sourceFiles = readdirSync(srcRoot, { recursive: true })
  .map((entry) => join(srcRoot, entry))
  .filter((path) => /\.(ts|tsx)$/.test(path));

for (const file of sourceFiles) {
  if (file === truthFile) continue;
  const lines = readFileSync(file, "utf8").split("\n");
  lines.forEach((line, idx) => {
    if (keyIdentifier.test(line) && boolCompare.test(line)) {
      fail(
        `${relative(frontendRoot, file)}:${idx + 1} 质量键被直接与布尔字面量比较，应改走 resolveTunnelQualityEnabled`,
      );
    }
  });
}

// 3. 隧道监控页自读面。
const monitorViewFile = join(srcRoot, "pages", "node", "tunnel-monitor-view.tsx");
const monitorViewSource = readFileSync(monitorViewFile, "utf8");
monitorViewSource.split("\n").forEach((line, idx) => {
  if (/\.value\s*(===|!==)\s*["'](true|false)["']/.test(line)) {
    fail(
      `src/pages/node/tunnel-monitor-view.tsx:${idx + 1} 对配置值 .value 直接判读，应改走 resolveTunnelQualityEnabled`,
    );
  }
});
if (!/resolveTunnelQualityEnabled\(/.test(monitorViewSource)) {
  fail("tunnel-monitor-view.tsx 未调用 resolveTunnelQualityEnabled");
}

// 4. config 页保存广播面。
const configPageFile = join(srcRoot, "pages", "config.tsx");
const configPageSource = readFileSync(configPageFile, "utf8");
const broadcastAt = configPageSource.indexOf("monitorTunnelQualityEnabledChanged");
if (broadcastAt < 0) {
  fail("config.tsx 未找到 monitorTunnelQualityEnabledChanged 广播");
} else {
  const block = configPageSource.slice(broadcastAt, broadcastAt + 900);
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
  "隧道质量开关判读单点门禁通过：全仓判读收敛 resolveTunnelQualityEnabled",
);
