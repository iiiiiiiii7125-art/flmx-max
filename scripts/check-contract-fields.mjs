#!/usr/bin/env node
/**
 * check-contract-fields.mjs — 前后端契约门禁（FLMX 审计 B2 批落地）
 *
 * 目的：把 X2 逐字段比对的结论固化成可重复执行的集合 diff，防止 types.ts
 * 与后端实际响应再次漂移。检查分七组：
 *
 *   C1  nullable 贯彻：后端 repository.go 的 List* 出口凡经 nullableString /
 *       nullableInt64 / nullableForwardIngress 下发的字段（JSON null），
 *       前端对应 interface 必须显式声明 `| null`（声明缺失同样算失败）。
 *   C2  userPackage.userInfo：updatedTime 可空、expTime 为 number、无 maxConn。
 *   C3  TunnelQuality 双形态：后端快照 success=bool、历史 success=int 的既成
 *       事实下，前端必须有 TunnelQualityHistoryApiItem（success: number），
 *       且 api 层 getMonitorTunnelQualityHistory 必须做归一化再交出
 *       TunnelQualityApiItem[]。
 *   C4  备份类型集合：后端 ExportPartial / Import 的 typeSet 键集合，必须与
 *       前端 BackupTypes（api/index.ts）键、BACKUP_TYPE_OPTIONS（config.tsx）
 *       值三方相等。
 *   C5  UpdatePasswordPayload：confirmPassword / newUsername 必填
 *       （后端 changePasswordRequest 两者空值即拒绝）。
 *   C6  幻影/缺失字段簇：types.ts 各 interface 的禁入字段与必备字段。
 *   C7  页面本地类型副本同步：limit.tsx / group.tsx /
 *       dashboard/use-dashboard-data.ts / forward.tsx / node.tsx /
 *       src/types/index.ts 中与线缆类型同形的本地声明不得保留旧错误。
 *
 * 已登记的设计性差异见 scripts/contract-whitelist.json（只登记、不拦截）。
 *
 * 提取卫生（修复轮 6，R6-C4-16）：全部源码读取先经词法剥注释，
 * interface 块与 C3 函数体以字符串/模板感知的括号配平截取——旧
 * 实现的大括号计数无字符串/注释意识、C3 为无界尾切片，注释拷贝
 * 与注释内的 } 都能满足或截断判定，现整体失效。
 *
 * 用法：node scripts/check-contract-fields.mjs [repoRoot]
 * 退出码：0 全过；1 任一检查失败。
 */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root =
  process.argv[2] ||
  join(dirname(fileURLToPath(import.meta.url)), "..");

const read = (rel) => readFileSync(join(root, rel), "utf8");

// 词法剥注释（修复轮 6，R6-C4-16 机制处置）：注释逐字替换为空格
// （换行保留），字符串内容原样保留。goMode 下反引号是无插值原始
// 字符串、单引号是符文字面量；JS/TS 模式下反引号是带 ${} 插值的
// 模板字面量。全部源码读取先经此处理：注释里的字段形行、函数形
// 文本不再被 fieldDecl / goFuncSpan / 正则命中，也不能截断块提取。
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

const readCode = (rel, goMode) => stripComments(read(rel), goMode);

const FE = "vite-frontend/src";
const typesTs = readCode(`${FE}/api/types.ts`, false);
const indexTs = readCode(`${FE}/api/index.ts`, false);
const configTsx = readCode(`${FE}/pages/config.tsx`, false);
const repositoryGo = readCode("go-backend/internal/store/repo/repository.go", true);
const handlerGo = readCode("go-backend/internal/http/handler/handler.go", true);
const proberGo = readCode(
  "go-backend/internal/http/handler/tunnel_quality_prober.go",
  true,
);
const modelGo = readCode("go-backend/internal/store/model/model.go", true);
const whitelist = JSON.parse(read("scripts/contract-whitelist.json"));

let failures = 0;
const fail = (id, msg) => {
  failures += 1;
  console.log(`FAIL ${id}: ${msg}`);
};
const pass = (id, msg) => console.log(`PASS ${id}: ${msg}`);

/** 括号配平的块端点扫描：自 openIdx 的 { 起，跳过字符串/模板内容，
 *  返回匹配 } 的下标（找不到返回 -1）。输入已剥注释。 */
function scanBlockEnd(source, openIdx) {
  let depth = 0;
  for (let i = openIdx; i < source.length; i += 1) {
    const c = source[i];
    if (c === '"' || c === "'" || c === "`") {
      i += 1;
      while (i < source.length && source[i] !== c) {
        if (source[i] === "\\") i += 1;
        i += 1;
      }
      continue;
    }
    if (c === "{") depth += 1;
    else if (c === "}") {
      depth -= 1;
      if (depth === 0) return i;
    }
  }
  return -1;
}

/** 提取 TS interface 块（从 `interface NAME {` 起按花括号深度配平，
 *  配平跳过字符串/模板内容）。 */
function tsInterfaceBlock(source, name) {
  const start = source.search(
    new RegExp(`(?:export\\s+)?interface\\s+${name}\\b`),
  );
  if (start < 0) return null;
  const open = source.indexOf("{", start);
  if (open < 0) return null;
  const end = scanBlockEnd(source, open);
  return end < 0 ? source.slice(start) : source.slice(start, end + 1);
}

/** 在 interface 块里取某字段的声明文本（无声明返回 null）。 */
function fieldDecl(block, field) {
  if (!block) return null;
  const m = block.match(new RegExp(`^\\s*${field}\\??:\\s*([^;]+);`, "m"));
  return m ? m[1].trim() : null;
}

/** 提取 Go 函数体（从签名到下一个顶层 func）。 */
function goFuncSpan(source, signature) {
  const start = source.indexOf(signature);
  if (start < 0) return null;
  const rest = source.slice(start + signature.length);
  const next = rest.search(/\nfunc /);
  return next < 0 ? rest : rest.slice(0, next);
}

/* ---------------- C1: nullable 贯彻 ---------------- */
const listToInterface = {
  ListNodes: "NodeApiItem",
  ListUsers: "UserApiItem",
  ListSpeedLimits: "SpeedLimitApiItem",
  ListForwards: "ForwardApiItem",
  ListTunnels: "TunnelApiItem",
  ListGroupPermissions: "GroupPermissionApiItem",
};

for (const [fn, iface] of Object.entries(listToInterface)) {
  const span = goFuncSpan(repositoryGo, `func (r *Repository) ${fn}(`);
  if (!span) {
    fail("C1", `后端未找到 ${fn}`);
    continue;
  }
  const nullableKeys = [
    ...span.matchAll(/"(\w+)":\s*nullable(?:String|Int64|ForwardIngress)\(/g),
  ].map((m) => m[1]);
  const block = tsInterfaceBlock(typesTs, iface);
  if (!block) {
    fail("C1", `types.ts 未找到 interface ${iface}`);
    continue;
  }
  const failuresBefore = failures;
  for (const key of nullableKeys) {
    const decl = fieldDecl(block, key);
    if (decl === null) {
      fail(
        "C1",
        `${iface}.${key}：后端 ${fn} 以 nullable 下发（可为 null），types.ts 未声明该字段`,
      );
    } else if (!decl.includes("| null")) {
      fail(
        "C1",
        `${iface}.${key}：后端 ${fn} 以 nullable 下发（可为 null），前端声明为 \`${decl}\`，缺 | null`,
      );
    }
  }
  if (failures === failuresBefore)
    pass(
      "C1",
      `${fn} -> ${iface}：nullable 字段 ${nullableKeys.length} 个已逐个核对`,
    );
}

/* userPackage 的 forwards 出口同样以 nil 起手下发 inPort（handler.go）。 */
{
  const pkgBlock = tsInterfaceBlock(typesTs, "UserPackageInfoApiData");
  const fwdInPort = fieldDecl(tsInterfaceBlock(typesTs, "ForwardApiItem"), "inPort");
  if (!pkgBlock || fwdInPort === null || !fwdInPort.includes("| null")) {
    fail("C1", "ForwardApiItem.inPort 必须含 | null（ListForwards 与 userPackage 双出口均可为 null）");
  } else {
    pass("C1", "ForwardApiItem.inPort | null 覆盖 userPackage 出口");
  }
}

/* ---------------- C2: userPackage.userInfo ---------------- */
{
  const block = tsInterfaceBlock(typesTs, "UserPackageInfoApiData");
  const problems = [];
  if (!block) problems.push("未找到 UserPackageInfoApiData");
  else {
    const exp = fieldDecl(block, "expTime");
    if (exp === null || !/^number/.test(exp) || exp.includes("string"))
      problems.push(`userInfo.expTime 应为 number（毫秒），现为 \`${exp}\``);
    const upd = fieldDecl(block, "updatedTime");
    if (upd === null || !upd.includes("| null"))
      problems.push(`userInfo.updatedTime 应声明 | null，现为 \`${upd}\``);
    if (fieldDecl(block, "maxConn") !== null)
      problems.push("userInfo.maxConn 后端从不下发，应删除（幻影字段）");
    if (!/"updatedTime":\s*nullableNullInt64/.test(handlerGo))
      problems.push("后端锚点丢失：handler.go 未见 userPackage updatedTime nullable 下发");
  }
  if (problems.length) problems.forEach((p) => fail("C2", p));
  else pass("C2", "UserPackageInfoApiData.userInfo 与 handler.go 出口一致");
}

/* ---------------- C3: TunnelQuality 双形态 ---------------- */
{
  const problems = [];
  if (!/Success\s+bool\s+`json:"success"`/.test(proberGo))
    problems.push("后端锚点丢失：快照出口 success 非 bool");
  if (!/Success\s+int\s+.*json:"success"/.test(modelGo))
    problems.push("后端锚点丢失：历史（model）出口 success 非 int");
  const hist = tsInterfaceBlock(typesTs, "TunnelQualityHistoryApiItem");
  if (!hist) problems.push("types.ts 缺 TunnelQualityHistoryApiItem");
  else if (fieldDecl(hist, "success") !== "number")
    problems.push("TunnelQualityHistoryApiItem.success 应为 number");
  // 函数体有界截取（旧实现为自首个 indexOf 起的无界尾切片，尾内
  // 任一注释/代码拷贝都能满足其上正则——R6-C4-16）。现按括号配平
  // 只取该函数本体（输入已剥注释）。
  const fnAnchor = indexTs.indexOf("getMonitorTunnelQualityHistory");
  let fnBody = "";
  if (fnAnchor >= 0) {
    const open = indexTs.indexOf("{", fnAnchor);
    const end = open >= 0 ? scanBlockEnd(indexTs, open) : -1;
    if (end >= 0) fnBody = indexTs.slice(open + 1, end);
  }
  if (!/Network\.get<TunnelQualityHistoryApiItem\[\]>/.test(fnBody))
    problems.push("getMonitorTunnelQualityHistory 未按历史形态取数");
  if (!/success:\s*item\.success !== 0/.test(fnBody))
    problems.push("getMonitorTunnelQualityHistory 未把 success 归一化为 boolean");
  if (problems.length) problems.forEach((p) => fail("C3", p));
  else pass("C3", "TunnelQuality 双形态已在前端分型 + api 层归一化");
}

/* ---------------- C4: 备份类型集合 ---------------- */
{
  const typeSetKeys = (span) =>
    span
      ? [...new Set([...span.matchAll(/typeSet\["(\w+)"\]/g)].map((m) => m[1]))].sort()
      : [];
  const exportKeys = typeSetKeys(goFuncSpan(repositoryGo, "func (r *Repository) ExportPartial("));
  const importKeys = typeSetKeys(goFuncSpan(repositoryGo, "func (r *Repository) Import("));
  const backupTypesBlock = tsInterfaceBlock(indexTs, "BackupTypes");
  const feKeys = backupTypesBlock
    ? [...backupTypesBlock.matchAll(/^\s*(\w+)\?:/gm)].map((m) => m[1]).sort()
    : [];
  const optStart = configTsx.indexOf("BACKUP_TYPE_OPTIONS");
  const optBlock = configTsx.slice(optStart, configTsx.indexOf("] as const", optStart));
  const optKeys = [...optBlock.matchAll(/value:\s*"(\w+)"/g)].map((m) => m[1]).sort();
  const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);
  const problems = [];
  if (!eq(exportKeys, importKeys))
    problems.push(`后端 Export/Import 集合不一致：${exportKeys} vs ${importKeys}`);
  if (!eq(exportKeys, feKeys))
    problems.push(`BackupTypes 与后端集合不一致：前端 ${feKeys} vs 后端 ${exportKeys}`);
  if (!eq(exportKeys, optKeys))
    problems.push(`BACKUP_TYPE_OPTIONS 与后端集合不一致：前端 ${optKeys} vs 后端 ${exportKeys}`);
  if (problems.length) problems.forEach((p) => fail("C4", p));
  else pass("C4", `备份类型集合三方一致（${exportKeys.length} 类）`);
}

/* ---------------- C5: UpdatePasswordPayload ---------------- */
{
  const block = tsInterfaceBlock(typesTs, "UpdatePasswordPayload");
  const problems = [];
  if (!block) problems.push("未找到 UpdatePasswordPayload");
  else {
    if (!/^\s*confirmPassword:\s*string;/m.test(block))
      problems.push("confirmPassword 应为必填 string（后端空值即拒绝）");
    if (!/^\s*newUsername:\s*string;/m.test(block))
      problems.push("newUsername 应为必填 string（后端空值即拒绝）");
  }
  if (!/ConfirmPassword\s+string/.test(handlerGo))
    problems.push("后端锚点丢失：changePasswordRequest 无 ConfirmPassword");
  if (problems.length) problems.forEach((p) => fail("C5", p));
  else pass("C5", "UpdatePasswordPayload 与 changePasswordRequest 对齐");
}

/* ---------------- C6: 幻影/缺失字段簇 ---------------- */
{
  const problems = [];
  const mustAbsent = [
    ["SpeedLimitApiItem", "uploadSpeed", "后端从不下发（幻影）"],
    ["SpeedLimitApiItem", "downloadSpeed", "后端从不下发（幻影）"],
    ["TunnelApiItem", "entryNodeId", "后端从不下发（幻影必填）"],
    ["TunnelApiItem", "exitNodeId", "后端从不下发（幻影必填）"],
    ["TunnelApiItem", "forwardMode", "forwardMode 是节点属性，隧道出口无此键"],
    ["ForwardApiItem", "forwardMode", "forwardMode 是节点属性，转发出口无此键"],
    ["UserTunnelApiItem", "inNodePortSta", "真实字段为 portRangeMin/portRangeMax"],
    ["UserTunnelApiItem", "inNodePortEnd", "真实字段为 portRangeMin/portRangeMax"],
    ["UserTunnelApiItem", "tunnelName", "/tunnel/user/tunnel 只下发 id/name/端口范围"],
  ];
  for (const [iface, field, why] of mustAbsent) {
    if (fieldDecl(tsInterfaceBlock(typesTs, iface), field) !== null)
      problems.push(`${iface}.${field} 应删除：${why}`);
  }
  const mustPresent = [
    ["UserTunnelApiItem", "portRangeMin"],
    ["UserTunnelApiItem", "portRangeMax"],
    ["BatchOperationResult", "pendingCount"],
    ["BatchOperationResult", "pending"],
    ["ServiceMonitorLimitsApiData", "workerLimit"],
    ["SystemUpgradeVersionApiData", "pendingMigrations"],
    ["SystemUpgradeVersionApiData", "adminBootstrapRequired"],
    ["ServiceMonitorResultApiItem", "nodeId"],
    ["TunnelDiagnosisApiItem", "diagnosing"],
    ["InboundSecurity", "certPem"],
    ["InboundSecurity", "keyPem"],
  ];
  for (const [iface, field] of mustPresent) {
    if (fieldDecl(tsInterfaceBlock(typesTs, iface), field) === null)
      problems.push(`${iface}.${field} 缺失：后端有下发/请求侧存在，前端未声明`);
  }
  const certModes = fieldDecl(tsInterfaceBlock(typesTs, "KernelInfo"), "certModes");
  if (certModes === null || !certModes.includes("| null"))
    problems.push(`KernelInfo.certModes 应为 string[] | null（后端无 omitempty，未配置时为 null），现为 \`${certModes}\``);
  const slCreated = fieldDecl(tsInterfaceBlock(typesTs, "SpeedLimitApiItem"), "createdTime");
  if (slCreated === null || !/^number/.test(slCreated))
    problems.push(`SpeedLimitApiItem.createdTime 应为 number（后端 int64），现为 \`${slCreated}\``);
  const expDecl = fieldDecl(tsInterfaceBlock(typesTs, "UserMutationPayload"), "expTime");
  if (expDecl !== null && expDecl.includes("string"))
    problems.push(`UserMutationPayload.expTime 不应含 string（ISO 字符串会被后端静默回退默认到期），现为 \`${expDecl}\``);
  if (problems.length) problems.forEach((p) => fail("C6", p));
  else pass("C6", "幻影/缺失字段簇全部对齐");
}

/* ---------------- C7: 页面本地类型副本 ---------------- */
{
  const problems = [];
  const check = (rel, iface, field, { mustNull = false, mustNotContain = null, absent = false } = {}) => {
    const block = tsInterfaceBlock(readCode(rel, false), iface);
    if (!block) {
      problems.push(`${rel} 未找到本地 interface ${iface}（字段 ${field}）`);
      return;
    }
    const decl = fieldDecl(block, field);
    if (absent) {
      if (decl !== null) problems.push(`${rel} ${iface}.${field} 应删除（幻影字段），现为 \`${decl}\``);
      return;
    }
    if (decl === null) {
      problems.push(`${rel} ${iface}.${field} 未声明`);
      return;
    }
    if (mustNull && !decl.includes("| null"))
      problems.push(`${rel} ${iface}.${field} 应含 | null，现为 \`${decl}\``);
    if (mustNotContain && decl.includes(mustNotContain))
      problems.push(`${rel} ${iface}.${field} 不应含 ${mustNotContain}，现为 \`${decl}\``);
  };

  // limit.tsx 本地 SpeedLimitRule（与 SpeedLimitApiItem 同形）
  check(`${FE}/pages/limit.tsx`, "SpeedLimitRule", "createdTime", { mustNotContain: "string" });
  check(`${FE}/pages/limit.tsx`, "SpeedLimitRule", "updatedTime", { mustNull: true, mustNotContain: "string" });
  // group.tsx 本地 GroupPermission（名称可为 null，渲染处已有 || id 兜底）
  check(`${FE}/pages/group.tsx`, "GroupPermission", "userGroupName", { mustNull: true });
  check(`${FE}/pages/group.tsx`, "GroupPermission", "tunnelGroupName", { mustNull: true });
  // dashboard 本地 DashboardUserInfo（expTime 后端 int64 毫秒）
  check(`${FE}/pages/dashboard/use-dashboard-data.ts`, "DashboardUserInfo", "expTime", { mustNotContain: "string" });
  // forward.tsx 本地 Tunnel（由 getTunnelList 直接赋值，inIp 可为 null）
  check(`${FE}/pages/forward.tsx`, "Tunnel", "inIp", { mustNull: true });
  // forward.tsx / tunnel.tsx 本地 Node（serverIpV4/V6/extraIPs 后端 nullable 下发）
  for (const rel of [`${FE}/pages/forward.tsx`, `${FE}/pages/tunnel.tsx`]) {
    check(rel, "Node", "serverIpV4", { mustNull: true });
    check(rel, "Node", "serverIpV6", { mustNull: true });
    check(rel, "Node", "extraIPs", { mustNull: true });
  }
  // node.tsx 本地 Node（列表经 any 映射透传，null 会原样进入本地模型）
  for (const f of ["remark", "expiryTime", "renewalCycle", "serverIpV4", "serverIpV6", "extraIPs", "version", "remoteUrl"])
    check(`${FE}/pages/node/types.ts`, "Node", f, { mustNull: true });
  // src/types/index.ts 第二套类型：Tunnel 不得再声明幻影必填 entry/exitNodeId
  check(`${FE}/types/index.ts`, "Tunnel", "entryNodeId", { absent: true });
  check(`${FE}/types/index.ts`, "Tunnel", "exitNodeId", { absent: true });

  if (problems.length) problems.forEach((p) => fail("C7", p));
  else pass("C7", "页面本地类型副本与线缆契约同步");
}

/* ---------------- 白名单（已登记设计差异，仅公示） ---------------- */
console.log("\n已登记的设计性差异（白名单，不拦截）：");
for (const item of whitelist.registeredDesignDifferences || [])
  console.log(`  - ${item.item}：${item.basis}`);

console.log(
  failures === 0
    ? "\n契约门禁：全部通过"
    : `\n契约门禁：${failures} 项失败`,
);
process.exit(failures === 0 ? 0 : 1);
