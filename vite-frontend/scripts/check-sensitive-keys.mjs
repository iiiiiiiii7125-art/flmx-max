#!/usr/bin/env node
// 前后端敏感配置键清单一致性检查（A10-01 防漂移）。
//
// 真源：go-backend/internal/store/repo/config_policy.go 的 sensitiveConfigKeys。
// 前端：src/config/sensitive-keys.ts 的 SENSITIVE_CONFIG_KEYS。
// 两份清单集合不一致时以非零码退出并打印差异，供 CI / 发版前检查调用：
//   pnpm run check:sensitive-keys
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const backendFile = resolve(
  frontendRoot,
  "../go-backend/internal/store/repo/config_policy.go",
);
const frontendFile = resolve(frontendRoot, "src/config/sensitive-keys.ts");

const extractQuotedKeys = (block) =>
  new Set([...block.matchAll(/"([a-z0-9_]+)"/g)].map((m) => m[1]));

const backendSource = readFileSync(backendFile, "utf8");
const backendStart = backendSource.indexOf("var sensitiveConfigKeys");
if (backendStart < 0) {
  console.error(`未在 ${backendFile} 找到 sensitiveConfigKeys 定义`);
  process.exit(2);
}
const backendBlock = backendSource.slice(
  backendStart,
  backendSource.indexOf("\n}", backendStart),
);
const backendKeys = extractQuotedKeys(backendBlock);

const frontendSource = readFileSync(frontendFile, "utf8");
const frontendStart = frontendSource.indexOf("SENSITIVE_CONFIG_KEYS");
if (frontendStart < 0) {
  console.error(`未在 ${frontendFile} 找到 SENSITIVE_CONFIG_KEYS 定义`);
  process.exit(2);
}
const frontendBlock = frontendSource.slice(
  frontendStart,
  frontendSource.indexOf("]);", frontendStart),
);
const frontendKeys = extractQuotedKeys(frontendBlock);

const onlyBackend = [...backendKeys].filter((k) => !frontendKeys.has(k));
const onlyFrontend = [...frontendKeys].filter((k) => !backendKeys.has(k));

if (onlyBackend.length || onlyFrontend.length) {
  console.error("敏感配置键清单前后端不一致：");
  if (onlyBackend.length)
    console.error(`  仅后端有（前端须补进 sensitive-keys.ts）：${onlyBackend.join(", ")}`);
  if (onlyFrontend.length)
    console.error(`  仅前端有（后端已移除或前端多写）：${onlyFrontend.join(", ")}`);
  process.exit(1);
}

console.log(
  `敏感配置键清单一致：共 ${backendKeys.size} 键（${[...backendKeys].join(", ")}）`,
);
