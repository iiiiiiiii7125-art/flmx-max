// check-route-lists.mjs — 前端路由三清单一致性校验（AG-06，无测试框架版）。
//
// src/App.tsx 里有三份手写清单必须逐项相等，否则漂移无声：
//   ① 懒加载 loader 集（const loadXxxPage = () => import(...)）
//   ② React.lazy 组件集（每个 loader 恰被一个 lazy 组件包装）
//   ③ 空闲预加载集（COMMON_ROUTE_LOADERS ∪ ADMIN_ROUTE_LOADERS）
// 另对路由表本身钉基线：<Route> 总数与 path 集逐项对账（新增/删除
// 页面必须同步改本脚本的基线，变更留痕）；路由 element 用到的页面
// 组件必须全部出自 lazy 集（登录页 LoginRoute 与兜底 Navigate 除外），
// 且每个 lazy 组件至少被一条路由使用（无死组件、无漏登路由）。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-route-lists.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const app = readFileSync(join(here, "..", "src/App.tsx"), "utf8");

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

const sorted = (arr) => [...arr].sort();
const sameSet = (a, b) =>
  a.length === b.length && sorted(a).every((v, i) => v === sorted(b)[i]);
const diff = (a, b) => a.filter((v) => !b.includes(v));

// 基线（AG-06 审计实测 18/14/14）：页面增删须同步改此处并说明。
const EXPECTED_ROUTE_COUNT = 18;
const EXPECTED_LOADER_COUNT = 14;
const EXPECTED_PATHS = [
  "/",
  "/change-password",
  "/dashboard",
  "/monitor",
  "/my-subscription",
  "/forward",
  "/tunnel",
  "/node",
  "/user",
  "/group",
  "/profile",
  "/limit",
  "/inbound",
  "/relay",
  "/subscription",
  "/config",
  "/panel-sharing",
  "*",
];

// ① loader 集
const loaders = [...app.matchAll(/const (load\w+) = \(\) => import\(/g)].map(
  (m) => m[1],
);

// ② lazy 组件集（组件名 → loader）
const lazyPairs = [...app.matchAll(/const (\w+) = lazy\((load\w+)\)/g)].map(
  (m) => ({ component: m[1], loader: m[2] }),
);
const lazyComponents = lazyPairs.map((p) => p.component);
const lazyLoaders = lazyPairs.map((p) => p.loader);

// ③ 预加载集
const preloadNames = (listName) => {
  const m = app.match(
    new RegExp(`const ${listName} = \\[([\\s\\S]*?)\\];`),
  );

  if (!m) return null;

  return [...m[1].matchAll(/(load\w+)/g)].map((x) => x[1]);
};
const commonLoaders = preloadNames("COMMON_ROUTE_LOADERS");
const adminLoaders = preloadNames("ADMIN_ROUTE_LOADERS");
const preloadUnion = [...(commonLoaders ?? []), ...(adminLoaders ?? [])];

check(
  `loader 集 ${EXPECTED_LOADER_COUNT} 项（基线）`,
  loaders.length === EXPECTED_LOADER_COUNT,
  `实际 ${loaders.length} 项: ${loaders.join(", ")}`,
);
check(
  "每个 loader 恰被一个 lazy 组件包装（无遗漏、无重复）",
  sameSet(lazyLoaders, loaders) && lazyLoaders.length === loaders.length,
  `未包装: ${diff(loaders, lazyLoaders).join(", ") || "无"}；多余: ${diff(lazyLoaders, loaders).join(", ") || "无"}`,
);
check(
  "预加载两批清单均存在",
  commonLoaders !== null && adminLoaders !== null,
  "COMMON_ROUTE_LOADERS / ADMIN_ROUTE_LOADERS 有缺失",
);
check(
  "预加载集与 loader 集逐项相等（COMMON 与 ADMIN 不重叠）",
  sameSet(preloadUnion, loaders) && preloadUnion.length === loaders.length,
  `未预加载: ${diff(loaders, preloadUnion).join(", ") || "无"}；多余/重复: ${diff(preloadUnion, loaders).join(", ") || "无"}`,
);

// 路由表
const routesBlock = app.slice(app.indexOf("<Routes"), app.indexOf("</Routes>"));
const routeCount = (routesBlock.match(/<Route\b/g) ?? []).length;
const paths = [...routesBlock.matchAll(/path="([^"]+)"/g)].map((m) => m[1]);
const usedComponents = [
  ...routesBlock.matchAll(/<([A-Z]\w+) \/>/g),
].map((m) => m[1]);

check(
  `<Route> 总数 ${EXPECTED_ROUTE_COUNT} 条`,
  routeCount === EXPECTED_ROUTE_COUNT,
  `实际 ${routeCount} 条`,
);
check(
  "path 集与基线逐项相等",
  sameSet(paths, EXPECTED_PATHS),
  `新增: ${diff(paths, EXPECTED_PATHS).join(", ") || "无"}；缺失: ${diff(EXPECTED_PATHS, paths).join(", ") || "无"}`,
);
check(
  "路由 element 的页面组件全部出自 lazy 集（LoginRoute 除外）",
  usedComponents.every(
    (c) => c === "LoginRoute" || lazyComponents.includes(c),
  ),
  `域外组件: ${usedComponents.filter((c) => c !== "LoginRoute" && !lazyComponents.includes(c)).join(", ")}`,
);
check(
  "每个 lazy 组件至少被一条路由使用（无死组件）",
  lazyComponents.every((c) => usedComponents.includes(c)),
  `未被路由使用: ${lazyComponents.filter((c) => !usedComponents.includes(c)).join(", ") || "无"}`,
);

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
