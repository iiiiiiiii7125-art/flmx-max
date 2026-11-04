#!/usr/bin/env node
// check-wallpaper-chain.mjs — 壁纸全链防断门（2026-11-04 玻璃恢复新增）。
//
// 立门缘由：1.9.5 的壁纸失效正是「JS 活、CSS 死」型断链——设置面、
// 存储、site.ts 状态、App.tsx 的 class 切换与 CSS 变量写入全是活
// 的，唯独 globals.css 里消费 has-wallpaper-bg / has-custom-bg 的
// 绘制规则被素面化扫荡整体删除，于是选内置壁纸、传自定义图、设
// 纯色背景三种模式全部无声失效（设置页预览只剩占位盒）。任何一
// 代类名/配方扫荡都能再静默重演，本门把全链四环逐环钉死，任一
// 环断裂即点名：
//   ① JS 切换环：App.tsx 的 updateBg 真实函数体经转译后在假根
//      元素上执行（R4-C6-03 行为化，见下），五种背景输入下断言
//      has-wallpaper-bg / has-custom-bg 的实际翻转与
//      --custom-bg-image / --custom-bg-color 的实际写入/清除；
//   ② CSS 绘制环：globals.css 有 :root.has-wallpaper-bg 的
//      .bg-mesh-gradient::before 规则且引用 /images/bg-light.jpg，
//      有 :root.has-custom-bg 规则消费 var(--custom-bg-image) 与
//      var(--custom-bg-color)；
//   ③ 资源环：public/images/bg-light.jpg 存在且体积正常；
//   ④ 设置/存储环：pages/config.tsx 的 CONFIG_ITEMS 数据结构里
//      app_bg_image / login_bg_image 两项在列且 type 为 bg_image，
//      config/site.ts 的 PUBLIC_BRAND_CONFIG_KEYS 键清单含两键、
//      且 preloadWallpaper 定义在位并以 login/app 壁纸值调用
//      （AST 结构断言，非文本存在钉）。
// 知情省略：has-theme-bg 的两条绘制规则不恢复（App.tsx 现行恒剥
// 该 class、无主题图绘制路径），故不在本门断言内——globals.css
// 恢复段注释有登记。
//
// R4-C6-03：①环原为 classList.toggle("has-wallpaper-bg" 文本存
// 在钉——只验调用文本在列、不验生效条件，WP-M1 变异（mode ===
// "wallpaper" 改恒假）下全绿放行，与立门所防的「JS 活、CSS 死」
// 同形。本轮改行为断言：以 TS AST 定位 updateBg 箭头函数、取其
// 真实源码转译执行，条件恒假类变异必然翻红（已变异验活）。
// ④环同批由 includes 存在钉改 AST 结构断言（求值数据结构本身）。
// RR4-C6-04：②环原为规则块内文本 includes——绘制声明被整行注
// 释、url 字符串留在规则体内时仍全绿（CSS 死以注释形态复
// 现）。现改为剥注释后的声明级断言：目标消费必须以真实声明
// （属性＋值）存在于规则体内，注释内的文本不构成满足点。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-wallpaper-chain.mjs
// 退出码 0 = 通过，非 0 = 失败（打印断裂环）。
import { existsSync, readFileSync, statSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const read = (rel) => readFileSync(join(root, rel), "utf8");

const require = createRequire(import.meta.url);

let ts;
try {
  ts = require("typescript");
} catch {
  // R4-C6-04：typescript 只许自当前工作树解析；外部副本须经
  // FLMX_CHECK_TYPESCRIPT 显式指向并点名，不再静默回退写死路径。
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

const parseTsx = (rel) => {
  const text = read(rel);

  return ts.createSourceFile(
    rel,
    text,
    ts.ScriptTarget.ES2020,
    true,
    ts.ScriptKind.TSX,
  );
};
const findVarDecl = (sf, name) => {
  let found = null;
  const visit = (node) => {
    if (found) return;
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.name.text === name &&
      node.initializer
    ) {
      found = node;
      return;
    }
    ts.forEachChild(node, visit);
  };

  visit(sf);

  return found;
};

// ① JS 切换环（App.tsx）：提取 updateBg 真实函数体转译执行。
const makeFakeRoot = () => {
  // 初始带 has-theme-bg：updateBg 无条件 remove 它，带初始值才能
  // 证明 remove 真实执行。
  const classes = new Set(["has-theme-bg"]);
  const props = new Map();

  return {
    classes,
    props,
    classList: {
      toggle: (name, force) => {
        if (force) classes.add(name);
        else classes.delete(name);
      },
      remove: (name) => classes.delete(name),
    },
    style: {
      setProperty: (k, v) => props.set(k, v),
      removeProperty: (k) => props.delete(k),
    },
  };
};

let updateBgFactory = null;
try {
  const appSf = parseTsx("src/App.tsx");
  const decl = findVarDecl(appSf, "updateBg");

  if (!decl || !ts.isArrowFunction(decl.initializer)) {
    throw new Error("App.tsx 中未找到 updateBg 箭头函数（结构已变）");
  }
  const wrapped = `const updateBg = ${decl.initializer.getText(appSf)};`;
  const { outputText } = ts.transpileModule(wrapped, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2020,
    },
    reportDiagnostics: true,
  });

  updateBgFactory = (location, siteConfig, effectiveMode, document) =>
    // eslint-disable-next-line no-new-func
    new Function(
      "location",
      "siteConfig",
      "effectiveMode",
      "document",
      `${outputText}\nreturn updateBg;`,
    )(location, siteConfig, effectiveMode, document);
} catch (e) {
  check("壁纸链①：updateBg 行为求值可用（AST 提取＋转译）", false, String(e));
}

if (updateBgFactory) {
  const runScenario = (pathname, config) => {
    const fakeRoot = makeFakeRoot();
    const siteConfig = {
      app_bg_image: "",
      app_bg_image_light: "",
      app_bg_image_dark: "",
      login_bg_image: "",
      ...config,
    };
    const updateBg = updateBgFactory(
      { pathname },
      siteConfig,
      "light",
      { documentElement: fakeRoot },
    );

    updateBg();

    return fakeRoot;
  };
  const scenarios = [
    {
      label: "内置壁纸 wallpaper → has-wallpaper-bg 置位、custom 变量清除",
      pathname: "/dashboard",
      config: { app_bg_image: "wallpaper" },
      expect: (r) =>
        r.classes.has("has-wallpaper-bg") &&
        !r.classes.has("has-custom-bg") &&
        !r.classes.has("has-theme-bg") &&
        !r.props.has("--custom-bg-image") &&
        !r.props.has("--custom-bg-color"),
    },
    {
      label: "自定义图片 URL → has-custom-bg 置位、image=url(...)、color=transparent",
      pathname: "/dashboard",
      config: { app_bg_image: "https://cdn.example.com/bg.jpg" },
      expect: (r) =>
        !r.classes.has("has-wallpaper-bg") &&
        r.classes.has("has-custom-bg") &&
        r.props.get("--custom-bg-image") ===
          "url(https://cdn.example.com/bg.jpg)" &&
        r.props.get("--custom-bg-color") === "transparent",
    },
    {
      label: "自定义纯色 → has-custom-bg 置位、image=none、color=色值",
      pathname: "/dashboard",
      config: { app_bg_image: "#334155" },
      expect: (r) =>
        !r.classes.has("has-wallpaper-bg") &&
        r.classes.has("has-custom-bg") &&
        r.props.get("--custom-bg-image") === "none" &&
        r.props.get("--custom-bg-color") === "#334155",
    },
    {
      label: "未设置（空）→ 两 class 皆不置位、变量清除",
      pathname: "/dashboard",
      config: {},
      expect: (r) =>
        !r.classes.has("has-wallpaper-bg") &&
        !r.classes.has("has-custom-bg") &&
        !r.classes.has("has-theme-bg") &&
        r.props.size === 0,
    },
    {
      label: "登录页独立壁纸优先 → login_bg_image=wallpaper 时置 has-wallpaper-bg",
      pathname: "/",
      config: { login_bg_image: "wallpaper", app_bg_image: "#334155" },
      expect: (r) =>
        r.classes.has("has-wallpaper-bg") && !r.classes.has("has-custom-bg"),
    },
  ];

  for (const s of scenarios) {
    let ok = false;
    let detail = "";

    try {
      ok = s.expect(runScenario(s.pathname, s.config));
    } catch (e) {
      detail = String(e);
    }
    check(`壁纸链①行为：${s.label}`, ok, detail);
  }
}

// ② CSS 绘制环（globals.css）：剥注释后按规则块提取、逐块做声
// 明级断言（RR4-C6-04）。旧实现对规则块原文做 includes 文本判
// 定——把绘制声明整行注释掉、url 字符串留在规则体内即全绿，
// 「JS 活、CSS 死」以注释形态在 CSS 侧复现。现先词法剥除 CSS
// 注释（字符串内容保留、注释逐字替空格，与 check-contrast 的
// stripCssComments 同口径），再把规则体解析为声明列，断言目
// 标消费以真实声明（属性名＋值）存在：注释掉的声明在剥离后不
// 复存在、仅存于注释的值不构成满足点。
const stripCssComments = (src) => {
  const out = [];
  const n = src.length;
  let i = 0;
  let quote = null;

  while (i < n) {
    const c = src[i];

    if (quote) {
      out.push(c);
      if (c === "\\") {
        out.push(src[i + 1] ?? "");
        i += 2;
        continue;
      }
      if (c === quote) quote = null;
      i++;
      continue;
    }
    if (c === '"' || c === "'") {
      quote = c;
      out.push(c);
      i++;
      continue;
    }
    if (c === "/" && src[i + 1] === "*") {
      out.push("  ");
      i += 2;
      while (i < n && !(src[i] === "*" && src[i + 1] === "/")) {
        out.push(src[i] === "\n" ? "\n" : " ");
        i++;
      }
      if (i < n) {
        out.push("  ");
        i += 2;
      }
      continue;
    }
    out.push(c);
    i++;
  }

  return out.join("");
};
const css = stripCssComments(read("src/styles/globals.css"));
const ruleBody = (selectorRe) => {
  const m = css.match(selectorRe);

  return m ? m[1] : "";
};
// 规则体 → 声明列 [{prop, value}]：按 ; 切分后逐条取首个冒号
// 前的属性名与余下值（值内的冒号如 url(http:) 不影响属性判定）。
const declarationsOf = (body) =>
  body
    .split(";")
    .map((d) => {
      const idx = d.indexOf(":");

      if (idx < 0) return null;

      return {
        prop: d.slice(0, idx).trim().toLowerCase(),
        value: d.slice(idx + 1).trim(),
      };
    })
    .filter(Boolean);
const hasDecl = (body, props, needle) =>
  declarationsOf(body).some(
    (d) => props.includes(d.prop) && d.value.includes(needle),
  );
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
  "壁纸链②：globals.css has-wallpaper-bg 绘制规则以 background 声明引用 bg-light.jpg（剥注释后声明级判定）",
  hasDecl(wallpaperRule, ["background", "background-image"], "/images/bg-light.jpg"),
  wallpaperRule ? "规则在但无生效的 background 声明引用内置壁纸图" : "规则缺失",
);
check(
  "壁纸链②：globals.css has-custom-bg 绘制规则以 background 声明消费 --custom-bg-image（剥注释后声明级判定）",
  hasDecl(customRule, ["background", "background-image"], "var(--custom-bg-image"),
  customRule ? "规则在但无生效的 background 声明消费变量" : "规则缺失",
);
check(
  "壁纸链②：globals.css has-custom-bg 基座以 background 声明消费 --custom-bg-color（剥注释后声明级判定）",
  hasDecl(customBaseRule, ["background", "background-color"], "var(--custom-bg-color"),
  customBaseRule ? "规则在但无生效的 background 声明消费变量" : "规则缺失",
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

// ④ 设置/存储环（AST 结构断言）
{
  const configSf = parseTsx("src/pages/config.tsx");
  const itemsDecl = findVarDecl(configSf, "CONFIG_ITEMS");
  const itemTypes = new Map();

  if (itemsDecl && ts.isArrayLiteralExpression(itemsDecl.initializer)) {
    for (const el of itemsDecl.initializer.elements) {
      if (!ts.isObjectLiteralExpression(el)) continue;
      const props = new Map();

      for (const p of el.properties) {
        if (
          ts.isPropertyAssignment(p) &&
          (ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) &&
          ts.isStringLiteral(p.initializer)
        ) {
          props.set(p.name.text, p.initializer.text);
        }
      }
      if (props.has("key")) itemTypes.set(props.get("key"), props.get("type"));
    }
  }
  check(
    "壁纸链④：config.tsx CONFIG_ITEMS 含 app_bg_image / login_bg_image 且 type=bg_image",
    itemTypes.get("app_bg_image") === "bg_image" &&
      itemTypes.get("login_bg_image") === "bg_image",
    `实测 app_bg_image=${itemTypes.get("app_bg_image")}、login_bg_image=${itemTypes.get("login_bg_image")}`,
  );

  const siteSf = parseTsx("src/config/site.ts");
  const keysDecl = findVarDecl(siteSf, "PUBLIC_BRAND_CONFIG_KEYS");
  let keyInit = keysDecl?.initializer;

  if (keyInit && ts.isAsExpression(keyInit)) keyInit = keyInit.expression;
  const keyList =
    keyInit && ts.isArrayLiteralExpression(keyInit)
      ? keyInit.elements
          .filter((e) => ts.isStringLiteral(e))
          .map((e) => e.text)
      : [];

  check(
    "壁纸链④：site.ts PUBLIC_BRAND_CONFIG_KEYS 键清单含 app_bg_image / login_bg_image",
    keyList.includes("app_bg_image") && keyList.includes("login_bg_image"),
    `实测键清单 ${keyList.length} 项`,
  );

  let preloadDefined = false;
  let preloadWired = false;
  const visitSite = (node) => {
    if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.name.text === "preloadWallpaper"
    ) {
      preloadDefined = true;
    }
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "preloadWallpaper"
    ) {
      const argText = node.arguments.map((a) => a.getText(siteSf)).join(" ");

      if (argText.includes("login_bg_image") && argText.includes("app_bg_image")) {
        preloadWired = true;
      }
    }
    ts.forEachChild(node, visitSite);
  };

  visitSite(siteSf);
  check(
    "壁纸链④：site.ts preloadWallpaper 定义在位且以 login/app 壁纸值调用（预加载接线）",
    preloadDefined && preloadWired,
    `定义=${preloadDefined}、接线=${preloadWired}`,
  );
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
