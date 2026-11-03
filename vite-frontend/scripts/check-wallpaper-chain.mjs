#!/usr/bin/env node
// check-wallpaper-chain.mjs — 壁纸全链防断门（2026-11-04 玻璃恢复新增）。
//
// 立门缘由：1.9.5 的壁纸失效正是「JS 活、CSS 死」型断链——设置面、
// 存储、site.ts 状态、App.tsx 的 class 切换与 CSS 变量写入全是活的，
// 唯独 globals.css 里消费 has-wallpaper-bg / has-custom-bg 的绘制规则
// 被素面化扫荡整体删除，于是选内置壁纸、传自定义图、设纯色背景三
// 种模式全部无声失效（设置页预览只剩占位盒）。任何一代类名/配方扫
// 荡都能再静默重演，本门把全链四环逐环钉死，任一环断裂即点名：
//   ① JS 切换环：App.tsx 在根元素上 toggle has-wallpaper-bg /
//      has-custom-bg，并写 --custom-bg-image / --custom-bg-color；
//   ② CSS 绘制环：globals.css 有 :root.has-wallpaper-bg 的
//      .bg-mesh-gradient::before 规则且引用 /images/bg-light.jpg，
//      有 :root.has-custom-bg 规则消费 var(--custom-bg-image) 与
//      var(--custom-bg-color)；
//   ③ 资源环：public/images/bg-light.jpg 存在且体积正常；
//   ④ 设置/存储环：pages/config.tsx 的设置项在列 app_bg_image /
//      login_bg_image 两键，config/site.ts 的键清单与预加载在列。
// 知情省略：has-theme-bg 的两条绘制规则不恢复（App.tsx 现行恒剥
// 该 class、无主题图绘制路径），故不在本门断言内——globals.css
// 恢复段注释有登记。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-wallpaper-chain.mjs
// 退出码 0 = 通过，非 0 = 失败（打印断裂环）。
import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const read = (rel) => readFileSync(join(root, rel), "utf8");

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

// ① JS 切换环（App.tsx）
const app = read("src/App.tsx");

check(
  "壁纸链①：App.tsx toggle has-wallpaper-bg / has-custom-bg",
  app.includes('classList.toggle("has-wallpaper-bg"') &&
    app.includes('classList.toggle("has-custom-bg"'),
);
check(
  "壁纸链①：App.tsx 写 --custom-bg-image / --custom-bg-color",
  app.includes('"--custom-bg-image"') && app.includes('"--custom-bg-color"'),
);

// ② CSS 绘制环（globals.css）：按规则块提取后逐块断言消费面。
const css = read("src/styles/globals.css");
const ruleBody = (selectorRe) => {
  const m = css.match(selectorRe);

  return m ? m[1] : "";
};
const wallpaperRule = ruleBody(
  /:root\.has-wallpaper-bg\s+\.bg-mesh-gradient::before\s*\{([^}]*)\}/,
);
const customRule = ruleBody(
  /:root\.has-custom-bg\s+\.bg-mesh-gradient::before\s*\{([^}]*)\}/,
);
const customBaseRule = ruleBody(
  /:root\.has-custom-bg\s+\.bg-mesh-gradient\s*\{([^}]*)\}/,
);

check(
  "壁纸链②：globals.css has-wallpaper-bg 绘制规则引用 bg-light.jpg",
  wallpaperRule.includes("url('/images/bg-light.jpg')"),
  wallpaperRule ? "规则在但未引用内置壁纸图" : "规则缺失",
);
check(
  "壁纸链②：globals.css has-custom-bg 绘制规则消费 --custom-bg-image",
  customRule.includes("var(--custom-bg-image"),
  customRule ? "规则在但未消费变量" : "规则缺失",
);
check(
  "壁纸链②：globals.css has-custom-bg 基座消费 --custom-bg-color",
  customBaseRule.includes("var(--custom-bg-color"),
  customBaseRule ? "规则在但未消费变量" : "规则缺失",
);

// ③ 资源环
const assetPath = join(root, "public/images/bg-light.jpg");

check(
  "壁纸链③：public/images/bg-light.jpg 存在且体积 > 100KB",
  existsSync(assetPath) && statSync(assetPath).size > 100 * 1024,
  existsSync(assetPath)
    ? `实测 ${statSync(assetPath).size} B`
    : "资源缺失",
);

// ④ 设置/存储环
const configPage = read("src/pages/config.tsx");
const site = read("src/config/site.ts");

check(
  "壁纸链④：config.tsx 设置项在列 app_bg_image / login_bg_image",
  configPage.includes('key: "app_bg_image"') &&
    configPage.includes('key: "login_bg_image"'),
);
check(
  "壁纸链④：site.ts 键清单含 app_bg_image / login_bg_image 且有预加载",
  site.includes("app_bg_image") &&
    site.includes("login_bg_image") &&
    site.includes("preloadWallpaper"),
);

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
