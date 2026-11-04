#!/usr/bin/env node
// check-warning-consumption.mjs — 信封 warning 生产者 × 前端消费闭合校验
// （RR4-C5-01 机制钉）。
//
// 后端以 response.OKWithWarning 在信封 warning 字段里点名受影响对象
// （src/api/network.ts 的类型合同写明：must be surfaced, never
// silently dropped），但 Network 层不做全局透出、靠各消费点自读——
// FX-A 给删源落的点名回执就曾因唯一消费端 sources.tsx 只读 code 而
// 在 UI 不可达（RR4-C5-01）。本门把「每个 OKWithWarning 生产者必有
// 读 .warning 的前端消费端」落成机械对账：
//   ① 后端普查：go-backend 全部非测试 .go 中 response.OKWithWarning(
//      调用点的文件集与总数必须与下方登记册逐项相等——新增生产者
//      不登记即红，登记册条目失效（生产者被删）同样红。
//   ② 接线：登记册每个端点串必须出现在其 API 函数的定义体内
//      （src/api/*.ts 中该 export 到下一个 export 之间）。
//   ③ 消费：每个 API 函数在 src/（api 层与 demo 适配器除外）的全部
//      调用文件都必须实读 .warning，且登记册点名的消费文件必须确在
//      调用之列——任一调用点只读 code/msg 即红。
// 提取前先词法剥注释（与 check-route-lists 同实现）：注释掉的生产者
// 或 .warning 读不得算数。
//
// 注意：data 通道内的 warning（如 federation share/update 的
// data.warning）不是信封字段、不经 OKWithWarning，不在本门普查内。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-warning-consumption.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");
const repoDir = join(frontendDir, "..");

// 词法剥注释：// 与 /* */ 逐字替换为空格（换行保留），字符串/模板
// 字面量内容原样保留（与 check-route-lists.mjs 同实现）。
const stripComments = (src) => {
  const out = [];
  const n = src.length;
  let i = 0;
  let mode = "code";
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

const walk = (dir, test) => {
  const out = [];

  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);

    if (entry.isDirectory()) out.push(...walk(p, test));
    else if (test(entry.name)) out.push(p);
  }

  return out;
};

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};
const sorted = (arr) => [...arr].sort();
const diff = (a, b) => a.filter((v) => !b.includes(v));

// 登记册（RR4-C5-01 审计实测 3 处）：新增 OKWithWarning 生产者必须
// 在此登记端点、API 函数与消费文件，并保证消费端实读 .warning。
const REGISTRY = [
  {
    producer: "go-backend/internal/http/handler/kernel.go",
    endpoint: "/kernel/install",
    apiFn: "installKernel",
    consumers: ["src/pages/inbound/kernel-panel.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/node/install",
    apiFn: "getNodeInstallCommand",
    consumers: ["src/pages/node.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/remote_source.go",
    endpoint: "/subscription/source/delete",
    apiFn: "deleteRemoteSource",
    consumers: ["src/pages/inbound/sources.tsx"],
  },
];

// ① 后端普查
const goFiles = walk(
  join(repoDir, "go-backend"),
  (name) => name.endsWith(".go") && !name.endsWith("_test.go"),
);
const censusFiles = [];
let censusCount = 0;

for (const file of goFiles) {
  const src = stripComments(readFileSync(file, "utf8"));
  const hits = (src.match(/response\.OKWithWarning\(/g) ?? []).length;

  if (hits > 0) {
    censusFiles.push(relative(repoDir, file).split("\\").join("/"));
    censusCount += hits;
  }
}
const registryProducers = REGISTRY.map((r) => r.producer);

check(
  `OKWithWarning 生产者文件集 ${registryProducers.length} 个（登记册）`,
  sorted(censusFiles).join() === sorted(registryProducers).join(),
  `未登记: ${diff(censusFiles, registryProducers).join(", ") || "无"}；已失效: ${diff(registryProducers, censusFiles).join(", ") || "无"}`,
);
check(
  `OKWithWarning 调用点总数 ${REGISTRY.length}（一处一登记）`,
  censusCount === REGISTRY.length,
  `实际 ${censusCount} 处`,
);

// 合同锚：ApiResponse 仍声明 warning 字段
const network = stripComments(
  readFileSync(join(frontendDir, "src/api/network.ts"), "utf8"),
);

check(
  "ApiResponse 合同仍声明 warning?: string",
  /warning\?:\s*string/.test(network),
  "src/api/network.ts 的 warning 字段声明缺失",
);

// API 层源码（接线对账面）
const apiSrc = walk(join(frontendDir, "src/api"), (name) =>
  /\.tsx?$/.test(name),
)
  .map((f) => stripComments(readFileSync(f, "utf8")))
  .join("\n");

// 前端调用面：src 全域，排除 api 层自身与 demo 适配器（不调用页面 API 函数）
const callerFiles = walk(join(frontendDir, "src"), (name) =>
  /\.tsx?$/.test(name),
).filter((f) => {
  const rel = relative(frontendDir, f).split("\\").join("/");

  return !rel.startsWith("src/api/") && !rel.startsWith("src/demo/");
});
const callerSrc = new Map(
  callerFiles.map((f) => [
    relative(frontendDir, f).split("\\").join("/"),
    stripComments(readFileSync(f, "utf8")),
  ]),
);

for (const entry of REGISTRY) {
  // ② 接线：API 函数定义体内含端点串
  const defAt = apiSrc.indexOf(`export const ${entry.apiFn}`);
  const defEnd =
    defAt < 0 ? -1 : apiSrc.indexOf("\nexport const ", defAt + 1);
  const defBody = defAt < 0 ? "" : apiSrc.slice(defAt, defEnd < 0 ? undefined : defEnd);

  check(
    `${entry.apiFn} 定义存在且接线 ${entry.endpoint}`,
    defAt >= 0 && defBody.includes(`"${entry.endpoint}"`),
    defAt < 0 ? "src/api 中找不到该 export" : `定义体内未见 "${entry.endpoint}"`,
  );

  // ③ 消费：全部调用文件实读 .warning，登记消费文件确在调用之列
  const callRe = new RegExp(`\\b${entry.apiFn}\\s*\\(`);
  const callers = [...callerSrc.entries()]
    .filter(([, src]) => callRe.test(src))
    .map(([rel]) => rel);
  const unread = callers.filter(
    (rel) => !/\.warning\b/.test(callerSrc.get(rel)),
  );

  check(
    `${entry.apiFn} 至少有一个调用点`,
    callers.length > 0,
    "src/ 内无调用文件",
  );
  check(
    `${entry.apiFn} 全部调用文件实读 .warning`,
    unread.length === 0,
    `未读 warning 的调用文件: ${unread.join(", ") || "无"}`,
  );
  check(
    `${entry.apiFn} 登记消费文件 ${entry.consumers.join(", ")} 确在调用之列`,
    entry.consumers.every((c) => callers.includes(c)),
    `实际调用文件: ${callers.join(", ") || "无"}`,
  );
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
