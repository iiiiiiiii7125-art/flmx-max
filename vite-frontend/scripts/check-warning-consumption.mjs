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
//   ④ 缺口登记（GAP）：T-001 新增的一批生产者，其前端调用点今天
//      只读 code/msg、信封 warning 被静默丢弃（2026-11-10 合并树
//      逐点独立核实，见 GAP_REGISTRY）。这类端点不得假登记为已
//      消费，也不得静默漏登记：以 GAP 条目登记端点/API 函数/调
//      用文件与缺口理由，门反向断言其调用点近处确无 .warning
//      读——消费一旦落地，本门即红、必须把该条升格为正式消费
//      登记（与 load-failure 门 missing→fixed 同口径）。无前端
//      API 封装的端点以 apiFn=null 登记，门断言端点串在 src 全
//      域确不存在——封装一旦出现即红、必须补消费登记。
// 提取前先词法剥注释（与 check-route-lists 同实现）：注释掉的生产者
// 或 .warning 读不得算数。
//
// 近处读断言（REGISTRY 循环内 ④）自 R3-MG-02 起绑接收变量名：旧
// 断言只验「窗内存在任一 .warning 读」，同窗异变量读可掩盖真消费
// 被删（mech-gates 变异 M-A 实证：删 forward.tsx deleteForward 的
// res.warning 块门仍绿）。现由共用函数 extractReceiverVar 提取每
// 次调用的接收变量（直形/三元/跨行三式），读判定改绑该变量名；
// 提取不到的回退点必须逐点登记于 UNBOUND_FALLBACK_REGISTRY 并在
// 断言标签带 unbound-fallback 字样，未登记回退与登记失效（形态
// 已被提取覆盖）均判红。
//
// 注意：data 通道内的 warning（即 data.warning）不是信封字段、
// 不经 OKWithWarning，不在本门普查内。
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

// 登记册（RR4-C5-01 审计实测 3 处；W8 补登记 3 处；2026-11-10
// 合并树独立复核）：新增 OKWithWarning 生产者必须在此登记端点、
// API 函数与消费文件，并保证消费端实读 .warning。登记粒度为
// 「一端点一登记」：同一生产者文件含多个端点时逐端点各登一条，
// calls 记该端点处理函数内的调用点数；调用点总数＝各条 calls
// 之和（含 GAP 登记），须与后端普查实数相等。
//   补登记（W8，修复波漏登记）：
//   - mutations.go nodeDelete（/node/delete，F2-21 未勾卸载的残留
//     说明）与 forwardForceDelete（/forward/force-delete，强制删
//     除残留规则说明）——W2 新增生产者未登记；
//   - user_subscription.go userSubscriptionSet（/user-subscription/set，
//     S-001 撤销分配的「已复制链接不断」说明）——W3 新增生产者未登记。
//   2026-11-10 复核订正：
//   - nodeDelete 的 OKWithWarning 调用点实为 2 处（1622/1627，
//     T-001 后其中一处带白名单刷新失败 note），calls 订正为 2；
//   - W3 的 T-001 新增 12 处生产者（handler.go 3、kernel_relay.go
//     2、mutations.go 7）的前端消费逐点核实后均未成立——调用点
//     只读 code/msg，信封 warning 被丢弃；/tunnel/delete 更是无
//     前端 API 封装（UI 走 /tunnel/delete-with-forwards 的 data
//     通道 warnings，不在本门普查内）。这 12 处以 GAP_REGISTRY
//     缺口登记（见下），不假登记为已消费；消费接线是产品修复、
//     落地后须升格回本册。
//   2026-11-10 消费接线波（fix/r2-warn）处置完毕：
//   - 8 个端点共 10 处调用点的消费已落地并升格回本册：
//     /config/update、/config/update-single、/inbound/relay/create、
//     /inbound/relay/delete、/user/delete、/node/update、
//     /tunnel/user/remove、/forward/delete——呈现与既有消费点同形
//     （成功分支内 res.warning 原样 toast 透出）；
//   - /tunnel/delete（2 处）逐项核实定性为前端不可达（无封装、
//     无调用面，UI 走 delete-with-forwards 的 data 通道），保留
//     GAP 登记并改登记为不可达口径，见 GAP_REGISTRY 该条。
//   2026-11-11 R2-02 防静默补登记：
//   - remote_source.go remoteSourceSave 新增 2 处 OKWithWarning
//     生产者（/subscription/source/save，纯中转源提示），消费接线
//     同步落地：sources.tsx 三个调用点（save/uploadAgain 直读
//     res.warning，refresh 走 refreshRemoteSource 同形直读）与
//     external.tsx addSource 直读——登记 consumers 为两个实际调
//     用文件。同函数内公网 http 分支的手工 R 信封同带 Warning 字
//     段（非 OKWithWarning 调用点，不入本门普查，消费同点成立）。
const REGISTRY = [
  {
    producer: "go-backend/internal/http/handler/kernel.go",
    endpoint: "/kernel/install",
    apiFn: "installKernel",
    calls: 1,
    consumers: ["src/pages/inbound/kernel-panel.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/node/install",
    apiFn: "getNodeInstallCommand",
    calls: 1,
    consumers: ["src/pages/node.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/remote_source.go",
    endpoint: "/subscription/source/delete",
    apiFn: "deleteRemoteSource",
    calls: 1,
    consumers: ["src/pages/inbound/sources.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/remote_source.go",
    endpoint: "/subscription/source/save",
    apiFn: "saveRemoteSource",
    calls: 2,
    consumers: ["src/pages/inbound/sources.tsx", "src/pages/inbound/external.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/node/delete",
    apiFn: "deleteNode",
    calls: 2,
    consumers: ["src/pages/node.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/forward/force-delete",
    apiFn: "forceDeleteForward",
    calls: 1,
    consumers: ["src/pages/forward.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/user_subscription.go",
    endpoint: "/user-subscription/set",
    apiFn: "setUserSubscriptions",
    calls: 1,
    consumers: ["src/pages/subscription/assign.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/handler.go",
    endpoint: "/config/update",
    apiFn: "updateConfigs",
    calls: 1,
    consumers: ["src/pages/config.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/handler.go",
    endpoint: "/config/update-single",
    apiFn: "updateConfig",
    calls: 2,
    consumers: ["src/pages/forward.tsx", "src/pages/node.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/kernel_relay.go",
    endpoint: "/inbound/relay/create",
    apiFn: "createInboundRelay",
    calls: 1,
    consumers: ["src/pages/inbound/relay-modal.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/kernel_relay.go",
    endpoint: "/inbound/relay/delete",
    apiFn: "deleteInboundRelay",
    calls: 1,
    consumers: ["src/pages/inbound/relay-modal.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/user/delete",
    apiFn: "deleteUser",
    calls: 1,
    consumers: ["src/pages/user.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/node/update",
    apiFn: "updateNode",
    // 1 处：尾部唯一的 OKWithWarning（白名单同步、连接日志重下发、
    // 改址刷新三处失败点名合并进同一回执，B-P3-1＋B-P3-2；此前
    // 白名单与改址各一处共 2 处，合并后只剩 1 处）。
    calls: 1,
    consumers: ["src/pages/node.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/tunnel/user/remove",
    apiFn: "removeUserTunnel",
    calls: 1,
    consumers: ["src/pages/user.tsx"],
  },
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/forward/delete",
    apiFn: "deleteForward",
    calls: 2,
    consumers: ["src/pages/forward.tsx"],
  },
];

// GAP 登记册（2026-11-10 合并树逐点独立核实）：T-001 新增生产者中
// 前端消费未成立的端点。每条登记 producer/endpoint/apiFn/calls 与
// 实际调用文件 callers，并以 gap 写明缺口形态；门反向断言：
//   - 条目必须带非空 gap 理由（不得静默豁免）；
//   - apiFn 非空者：接线成立、实际调用文件集与 callers 逐项相等、
//     且每个调用点后 60 行内确无对本次回执的 .warning 读——出现
//     近处读即红，须把该条升格为 REGISTRY 消费登记；
//   - apiFn=null 者（无前端封装）：端点串（含收尾引号的完整形）
//     在 src/api 与全部调用面确不存在——出现即红，须补封装与
//     消费登记。
// 缺口本身是产品问题（信封 warning 被 UI 丢弃），修复归产品波次；
// 本册只负责让缺口不可静默、不可腐烂。
const GAP_REGISTRY = [
  {
    producer: "go-backend/internal/http/handler/mutations.go",
    endpoint: "/tunnel/delete",
    apiFn: null,
    calls: 2,
    callers: [],
    gap: "前端不可达（2026-11-10 消费接线波逐项核实定性）：后端路由 /api/v1/tunnel/delete 在册（handler.go Register），但前端无 API 封装、无任何调用面——src/api 只有 delete-preview／delete-with-forwards／batch-delete-preview／batch-delete-with-forwards 四个删除封装，端点完整串 \"/tunnel/delete\" 在 src 全域出现 0 次；UI 删隧道（tunnel.tsx）只走 deleteTunnelWithForwards，其 data.warnings 已在 tunnel.tsx 消费。故本端点信封 warning 无 UI 消费面属不可达而非被丢弃，不为过门硬造无用封装；本条保留 GAP 登记（apiFn=null），门继续以反向断言守：端点串一旦在前端出现即红，必须补封装＋消费并升格回 REGISTRY",
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
const registryProducers = [
  ...new Set([
    ...REGISTRY.map((r) => r.producer),
    ...GAP_REGISTRY.map((r) => r.producer),
  ]),
];
const registryCalls = REGISTRY.reduce((n, r) => n + (r.calls ?? 1), 0);
const gapCalls = GAP_REGISTRY.reduce((n, r) => n + (r.calls ?? 1), 0);

check(
  `OKWithWarning 生产者文件集 ${registryProducers.length} 个（登记册＋GAP 登记）`,
  sorted(censusFiles).join() === sorted(registryProducers).join(),
  `未登记: ${diff(censusFiles, registryProducers).join(", ") || "无"}；已失效: ${diff(registryProducers, censusFiles).join(", ") || "无"}`,
);
check(
  `OKWithWarning 调用点总数 ${registryCalls + gapCalls}（消费登记 ${registryCalls}＋GAP 登记 ${gapCalls}，一处一登记）`,
  censusCount === registryCalls + gapCalls,
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

const escapeRegExp = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// extractReceiverVar（④ 近处读与 ⑤ GAP 反向断言共用，R3-MG-02）：
// 从调用点所在语句提取 `const <var> = [await] fn(...)` 的接收变量
// <var>。覆盖三式：
//   (1) 同行直形：const res = await fn( —— 调用行前缀直接匹配；
//   (2) 同行三元中段：const res = cond ? await fn( —— 前缀含三元
//       中段，按 `= … await$` 宽式匹配取声明名；
//   (3) 跨行赋值：声明起始行在调用行上方至多 3 行内（声明与调用
//       之间不得有 ; 收束上一语句），拼合前缀后按 (1)(2) 同式
//       匹配——updateNode 的 `const res = isEdit` 换行 `? await
//       updateNode(` 即此形。
// 三式皆不中返回 null：调用方回退任一 .warning 读判定，但回退点
// 必须逐点登记进 UNBOUND_FALLBACK_REGISTRY（见下），不许静默。
const extractReceiverVar = (lines, lineNo, apiFn) => {
  const line = lines[lineNo];
  const at = line.indexOf(apiFn);

  if (at < 0) return null;
  const prefix = line.slice(0, at);
  const directRe =
    /(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:await\s*)?$/;
  const ternaryRe =
    /(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=[\s\S]*(?:await\s*)$/;
  const same = directRe.exec(prefix) ?? ternaryRe.exec(prefix);

  if (same) return same[1];

  for (let up = 1; up <= 3 && lineNo - up >= 0; up++) {
    const joined = [...lines.slice(lineNo - up, lineNo), prefix].join("\n");
    const m = directRe.exec(joined) ?? ternaryRe.exec(joined);

    if (!m) continue;
    // 声明起始之后、调用之前的文本若出现 ; ，说明该声明语句已在
    // 本调用之前收束（声明属上一条语句），此候选作废。
    const declHead = /(?:const|let|var)\s+[A-Za-z_$][\w$]*\s*=/.exec(joined);
    const afterDecl = joined.slice(declHead.index + declHead[0].length);

    if (!afterDecl.includes(";")) return m[1];
  }

  return null;
};

// UNBOUND_FALLBACK_REGISTRY：extractReceiverVar 三式仍提取不到接收
// 变量的调用点逐点登记（file 为 src 相对路径、line 为调用点行号、
// reason 写明形态与为何不扩提取式）。登记条目与实际回退点双向对
// 账：实际回退未登记即红，登记条目已无对应回退（形态已被提取覆
// 盖）同样即红，须摘除登记（与端口哨兵反向检查同纪律）。终树现
// 行 16 个调用点三式全覆盖，本册应为空。
const UNBOUND_FALLBACK_REGISTRY = [];

// ④ 循环内收集的实际回退点（"src/...:line"），循环后汇总对账。
const unboundFallbackSites = [];

for (const entry of REGISTRY) {
  // ② 接线：API 函数定义体内含端点串
  const defAt = apiSrc.search(
    new RegExp(`export const ${entry.apiFn}\\b`),
  );
  const defEnd =
    defAt < 0 ? -1 : apiSrc.indexOf("\nexport const ", defAt + 1);
  const defBody = defAt < 0 ? "" : apiSrc.slice(defAt, defEnd < 0 ? undefined : defEnd);

  check(
    `${entry.apiFn} 定义存在且接线 ${entry.endpoint}`,
    defAt >= 0 && defBody.includes(`"${entry.endpoint}"`),
    defAt < 0 ? "src/api 中找不到该 export" : `定义体内未见 "${entry.endpoint}"`,
  );

  // ③ 消费：全部调用文件实读 .warning，登记消费文件确在调用之列
  // 调用点须同时从 @/api 导入该函数——同名 prop 回调（如 node-row.tsx
  // 的 actions.deleteNode）不是 API 调用，不得算入（W8）。
  const callRe = new RegExp(`\\b${entry.apiFn}\\s*\\(`);
  const importRe = new RegExp(
    `import\\s*\\{[^}]*\\b${entry.apiFn}\\b[^}]*\\}\\s*from\\s*["']@/api["']`,
  );
  const callers = [...callerSrc.entries()]
    .filter(([, src]) => callRe.test(src) && importRe.test(src))
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

  // ④ 近处读（绑接收变量名，R3-MG-02）：文件级 .warning 存在不等
  //    于本次调用读了——同一文件调两个登记 API 时，只读其一的调用
  //    点会被文件级检查漏放（W8 实证：node.tsx 调 deleteNode 与
  //    getNodeInstallCommand，文件级有 .warning 而 deleteNode 调
  //    用点实未读）。再进一步，窗内任一 .warning 读也不等于本次
  //    回执的读：同窗异变量读可掩盖真消费被删（mech-gates 变异
  //    M-A 实证：forward.tsx 删 deleteForward 的 res.warning 块
  //    后，43 行外 forceDeleteForward 的 forceRes.warning 读仍使
  //    旧断言 PASS）。故每次调用点之后 60 行内必须出现对「本次调
  //    用接收变量」的 .warning 读——变量名由 extractReceiverVar
  //    提取（直形/三元/跨行三式，与 ⑤ 共用）；提取不到的调用点
  //    回退任一读判定，其回退事实记入 unboundFallbackSites 并在
  //    本断言标签带 unbound-fallback 字样，循环后统一对账（现行
  //    调用点最远 13 行，窗口留足余量；超窗的正当写法须重构到
  //    调用附近或带理由调窗）。
  const callSiteLines = [...callerSrc.entries()]
    .filter(([, src]) => callRe.test(src) && importRe.test(src))
    .flatMap(([rel, src]) => {
      const lines = src.split("\n");
      const hits = [];
      const re = new RegExp(`\\b${entry.apiFn}\\s*\\(`, "g");
      let m;
      while ((m = re.exec(src)) !== null) {
        const lineNo = src.slice(0, m.index).split("\n").length - 1;
        const windowText = lines.slice(lineNo, lineNo + 61).join("\n");
        const varName = extractReceiverVar(lines, lineNo, entry.apiFn);
        const readRe = varName
          ? new RegExp(`(?<![\\w$])${escapeRegExp(varName)}\\.warning\\b`)
          : /\.warning\b/;
        const hit = {
          rel,
          lineNo: lineNo + 1,
          ok: readRe.test(windowText),
          bound: varName !== null,
        };

        if (!hit.bound) unboundFallbackSites.push(`${rel}:${lineNo + 1}`);
        hits.push(hit);
      }
      return hits;
    });
  const farSites = callSiteLines.filter((h) => !h.ok);
  const entryHasFallback = callSiteLines.some((h) => !h.bound);
  check(
    `${entry.apiFn} 每次调用附近 60 行内实读 .warning${entryHasFallback ? " [unbound-fallback]" : ""}`,
    farSites.length === 0,
    farSites.length === 0
      ? ""
      : `未读的调用点: ${farSites.map((h) => `${h.rel}:${h.lineNo}`).join(", ")}`,
  );
}

// ④附：变量名绑定回退点汇总对账——实际回退点必须逐点在
// UNBOUND_FALLBACK_REGISTRY 登记理由，未登记者判红；登记条目若
// 已无对应实际回退（形态已被提取式覆盖）同样判红，须摘除登记
// （防死登记滞留，与 R2-C5-03 反向检查同纪律）。终树现行回退点
// 应为 0：16 个调用点三式全覆盖（含 updateNode 三元跨行形）。
const registeredFallbackKeys = UNBOUND_FALLBACK_REGISTRY.map(
  (r) => `${r.file}:${r.line}`,
);
const unregisteredFallbacks = unboundFallbackSites.filter(
  (s) => !registeredFallbackKeys.includes(s),
);
const staleFallbacks = registeredFallbackKeys.filter(
  (s) => !unboundFallbackSites.includes(s),
);

for (const r of UNBOUND_FALLBACK_REGISTRY) {
  console.log(
    `NOTE  unbound-fallback 已登记回退点 ${r.file}:${r.line} — ${r.reason}`,
  );
}
check(
  `warning 近处读 unbound-fallback 回退点 ${unboundFallbackSites.length} 个，全部已登记且登记无失效`,
  unregisteredFallbacks.length === 0 && staleFallbacks.length === 0,
  `未登记回退点: ${unregisteredFallbacks.join(", ") || "无"}；已失效登记: ${staleFallbacks.join(", ") || "无"}——须扩 extractReceiverVar 形态覆盖、逐点登记理由，或摘除失效登记`,
);

// ⑤ GAP 登记双向断言：缺口在册且形态未变；消费落地即红、必须升格。
for (const entry of GAP_REGISTRY) {
  check(
    `GAP 登记带缺口理由：${entry.endpoint}`,
    typeof entry.gap === "string" && entry.gap.trim().length > 0,
    "GAP 条目必须写明缺口形态，不得静默豁免",
  );

  if (entry.apiFn === null) {
    // 无前端封装：端点完整串（含收尾引号，区别于 -preview /
    // -with-forwards 等同前缀端点）在 src/api 与调用面确不存在。
    const needle = `"${entry.endpoint}"`;
    const inApi = apiSrc.includes(needle);
    const inCallers = [...callerSrc.values()].some((src) =>
      src.includes(needle),
    );

    check(
      `GAP ${entry.endpoint} 确无前端封装与调用面`,
      !inApi && !inCallers,
      "端点串已出现在前端——封装已落地，须补消费并把本条升格为 REGISTRY 登记",
    );
    continue;
  }

  // 接线（与 ② 同口径）
  const defAt = apiSrc.search(
    new RegExp(`export const ${entry.apiFn}\\b`),
  );
  const defEnd =
    defAt < 0 ? -1 : apiSrc.indexOf("\nexport const ", defAt + 1);
  const defBody = defAt < 0 ? "" : apiSrc.slice(defAt, defEnd < 0 ? undefined : defEnd);

  check(
    `GAP ${entry.apiFn} 定义存在且接线 ${entry.endpoint}`,
    defAt >= 0 && defBody.includes(`"${entry.endpoint}"`),
    defAt < 0 ? "src/api 中找不到该 export" : `定义体内未见 "${entry.endpoint}"`,
  );

  // 实际调用文件集与登记 callers 逐项相等（与 ③ 同口径的导入感知）
  const callRe = new RegExp(`\\b${entry.apiFn}\\s*\\(`);
  const importRe = new RegExp(
    `import\\s*\\{[^}]*\\b${entry.apiFn}\\b[^}]*\\}\\s*from\\s*["']@/api["']`,
  );
  const callers = [...callerSrc.entries()]
    .filter(([, src]) => callRe.test(src) && importRe.test(src))
    .map(([rel]) => rel);

  check(
    `GAP ${entry.apiFn} 调用文件集与登记一致（${entry.callers.join(", ")}）`,
    sorted(callers).join() === sorted(entry.callers).join(),
    `实际调用文件: ${callers.join(", ") || "无"}`,
  );

  // 反向近处读：调用点后 60 行内出现对本次回执的 .warning 读＝
  // 消费已落地，本条必须升格为 REGISTRY 消费登记（与 ④ 同窗口
  // 口径）。按接收变量名匹配（extractReceiverVar 与 ④ 共用，
  // 直形/三元/跨行三式）：同窗内其他调用回执的 .warning 读（如
  // forward.tsx 删除失败转 forceDeleteForward 后读
  // forceRes.warning）不是本调用的消费，不得误报；提取不到变量
  // 名的调用点回退为任一 .warning 读即点名（保守口径，须人工
  // 裁定后升格或带理由登记）。
  const landed = [];

  for (const rel of callers) {
    const src = callerSrc.get(rel);
    const lines = src.split("\n");
    const re = new RegExp(`\\b${entry.apiFn}\\s*\\(`, "g");
    let m;

    while ((m = re.exec(src)) !== null) {
      const lineNo = src.slice(0, m.index).split("\n").length - 1;
      const windowText = lines.slice(lineNo, lineNo + 61).join("\n");
      const varName = extractReceiverVar(lines, lineNo, entry.apiFn);
      const readRe = varName
        ? new RegExp(`(?<![\\w$])${escapeRegExp(varName)}\\.warning\\b`)
        : /\.warning\b/;

      if (readRe.test(windowText)) landed.push(`${rel}:${lineNo + 1}`);
    }
  }
  check(
    `GAP ${entry.apiFn} 调用点近处确无 .warning 读（缺口形态未变）`,
    landed.length === 0,
    `以下调用点已实读 .warning——消费已落地，须把本条升格为 REGISTRY 消费登记: ${landed.join(", ")}`,
  );
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
