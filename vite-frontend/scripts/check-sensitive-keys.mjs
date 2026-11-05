#!/usr/bin/env node
// 前后端敏感配置键清单一致性检查（A10-01 防漂移）。
//
// 真源：go-backend/internal/store/repo/config_policy.go 的 sensitiveConfigKeys。
// 前端：src/config/sensitive-keys.ts 的 SENSITIVE_CONFIG_KEYS。
// 两份清单集合不一致时以非零码退出并打印差异，供 CI / 发版前检查调用：
//   pnpm run check:sensitive-keys
//
// 提取面（修复轮 6，R6-C4-08 机制处置）：旧实现对原文切块后收集块
// 内引号键名，无注释意识——把后端某键一行注释掉，该键仍被计入
// 13 键集、门报一致，而它实际已脱离敏感键表。现两侧切块前先按各
// 自语言词法剥注释（后端 Go：//、/* */、"…"、原始字符串、符文
// 字面量；前端 TS：//、/* */、字符串/模板保留），被注释的键行整
// 体消失、集合对账随之点名。
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const backendFile = resolve(
  frontendRoot,
  "../go-backend/internal/store/repo/config_policy.go",
);
const frontendFile = resolve(frontendRoot, "src/config/sensitive-keys.ts");

// 通用词法剥注释：注释逐字替换为空格（换行保留、行号不变），字符
// 串内容原样保留。goMode 下反引号是无插值的原始字符串（读到下一
// 个反引号为止）、单引号是符文字面量；JS/TS 模式下反引号是带
// ${} 插值的模板字面量。
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
    // tpl（仅 JS/TS 模式）
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

const extractQuotedKeys = (block) =>
  new Set([...block.matchAll(/"([a-z0-9_]+)"/g)].map((m) => m[1]));

const backendSource = stripComments(readFileSync(backendFile, "utf8"), true);
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

const frontendSource = stripComments(
  readFileSync(frontendFile, "utf8"),
  false,
);
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

// E1-04 拓宽：敏感键「不落盘/不回读」的不变量此前只靠写侧守卫
// （configCache.set 拒写＋登出 purge），读侧裸读可绕过整表——
// pages/config.tsx 的 getInitialConfigs 曾以 localStorage.getItem(
// "vite_config_" + key) 直读且清单含 cloudflare_secret_key。现钉读
// 面：src 内除缓存层本体（config/site.ts、config/sensitive-keys.ts）
// 外，任何文件不得出现以字面量 vite_config_ 前缀直读 localStorage
// 的形态；配置初始值一律经 configCache / getCachedConfigs 读取，
// 且读取清单须按 SENSITIVE_CONFIG_KEYS 过滤（页面侧已落）。
const directReadPattern = /localStorage\s*\.\s*getItem\s*\(\s*["'`]vite_config_/;
const cacheLayerFiles = new Set([
  resolve(frontendRoot, "src/config/site.ts"),
  resolve(frontendRoot, "src/config/sensitive-keys.ts"),
]);
const walkSources = (dir) =>
  readdirSync(dir).flatMap((name) => {
    const full = resolve(dir, name);

    if (statSync(full).isDirectory()) return walkSources(full);

    return /\.tsx?$/.test(name) ? [full] : [];
  });
const directReadOffenders = walkSources(resolve(frontendRoot, "src"))
  .filter((file) => !cacheLayerFiles.has(file))
  .filter((file) =>
    directReadPattern.test(stripComments(readFileSync(file, "utf8"), false)),
  );

if (directReadOffenders.length) {
  console.error(
    `直读配置缓存绕过敏感键守卫（须经 configCache 读取并过滤敏感键）：${directReadOffenders.join(", ")}`,
  );
  process.exit(1);
}

console.log("配置缓存读面无裸读：直读 vite_config_ 前缀仅存于缓存层本体");
