// check-contrast.mjs — 文字对比度回归校验（无测试框架版）。
//
// 定案纪律：全站素面亮色单模，正文文字用所属色族最深档（-700 起），
// 禁灰字，对比度目标 7、底线 4.5（WCAG AA 正文）。此前该自检只是
// 审计时的一次性临时脚本、未落仓库（A-front 复核时独立复算 25 对
// 全过），本脚本把它沉淀为常设门：解析 src/styles/globals.css 的
// :root token 实值，按 WCAG 相对亮度公式复算 25 对正文/托盘文字
// 对，逐对断言 ① 不低于 4.5 底线；② 与钉值偏差 ≤ 0.1——token 被
// 改浅（或改深到改变设计值）都会点名，改值须同步改钉值（有意变更
// 留痕）。主题包（themes/）是管理员主动启用的资产，不在本门范围；
// 本门守的是出厂默认 token。
//
// 提取面（修复轮 6，R6-C4-09 机制处置）：旧实现以整文件正则收集
// 声明、后声明胜出且不剥注释——把某 token 改浅后在文件尾以注释
// 存档旧值声明，门会按旧值算出全过（渲染实值对比度可已跌破
// 4.5）。现提取先词法剥除 CSS 注释，再只在 :root { } 块内按块内
// 顺序合并声明（后声明胜出，与级联同义），与头注「解析 :root
// token 实值」的口径一致；注释存档不再构成满足点。
//
// 级联条件口径（修复轮 7，R7-09 / G-C4-03 机制处置）：轮 6 的块合
// 并对块的上下文无条件——把改浅的新值写进无条件 :root、再在文件
// 尾追加一个永假 @media 块存档旧值 :root，后声明胜出仍按旧值算
// 全过，而真实渲染用的是新值。现提取改为级联求值：解析规则时追
// 踪 at-rule 上下文，仅无条件 :root（顶层、或仅被 @layer 包裹）
// 的声明入集；@media/@supports/@container 等条件块（含未知块级
// at-rule）整棵子树排除。胜出顺序按 CSS 级联：!important 层级
// 高于普通级；普通级中无层声明高于 @layer 内声明、层间按首次提
// 及顺序后者胜；同级按源码顺序后者胜。token 只存在于条件块时
// 视为未定义（fail-closed，断言点名缺失）。
//
// 嵌套与匿名层口径（修复轮 8，R8-02/R8-03 / H-C4-01/02 机制处
// 置）：轮 7 求值器仍有两处与浏览器级联不一致。其一，匿名
// @layer 块的层名落为 null、被按无层声明计权（普通级最高优
// 先）——把改浅值写进无条件 :root、再用匿名层存档旧值即可骗
// 绿；现匿名层分配合成层名、按出现位置参与层序。其二，求值
// 不建模规则嵌套：永不匹配祖先选择器内的 :root 被当顶层计、
// :root 体内嵌套条件块第二位起的声明被逐片当作无条件声明
// 计——现分发携带祖先选择器上下文（有规则祖先后，内层 :root
// 恒不匹配、不计数），parseDecls 按花括号深度切分、嵌套块整
// 块交回分发。与 check-theme-light-required.mjs 内嵌的
// resolveRootTokens 同口径（各门自含、不跨文件引用），两门
// 同修、逐字同构。//
// 条件求值与选择器口径（修复轮 9，R9-16～R9-20 机制处置）：轮 8
// 后仍有五格与浏览器级联不一致——恒真条件块（@media all 等）
// 整棵排除、合成层名可被作者预登记非法同名劫持、:ROOT 大小写
// 伪类不计数、html:root 等复合根选择器整体不可见、嵌套 &:root
// 不展开。现：条件 at-rule 三值求值（恒真按无条件走查、恒假与
// 不可判维持整棵排除）；层名按自定义标识符合法性校验，非法层
// 名经 problems 通道由两门点名报红（fail-closed），合成层名与
// 作者层名空间就此隔离；选择器按复合解析，含正向 :root（伪类
// 名大小写不敏感）且其余组件仅类型 html/* 与伪类者命中根元
// 素、按特异度参与胜出（层序仍先于特异度）；嵌套 & 复合按展
// 开后选择器判定。求值器文末附合成 CSS 钉格逐格钉住五条。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-contrast.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(
  join(here, "..", "src/styles/globals.css"),
  "utf8",
);

// 词法剥除 CSS 注释：/* */ 逐字替换为空格（换行保留），字符串
// 内容原样保留。注释里的旧值存档声明在剥离后不复存在。
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

// 级联求值（R7-09）：跟踪 at-rule 上下文，只让无条件 :root 的
// 声明参与胜出竞争；条件块子树整棵排除。返回 token → 生效值
// （原文值，含非 hex 形态——调用方按需过滤）。
const resolveRootTokens = (code) => {
  const problems = [];
  const layerOrder = new Map();
  let layerSeq = 0;
  // 层名合法性（R9-17 / I-C4-02）：层名是点分自定义标识符串，括
  // 号、空格等字符不可能出现在合法层名里。作者写出的非法层名在
  // 浏览器中使整条 @layer 声明/规则块被丢弃，求值器同口径处理
  // 并记为建模问题（两门据此点名报红，fail-closed）；合成层名
  // 沿用带括号形态——合法作者层名永远写不出该形态，两个名空间
  // 就此隔离，预登记同名劫持不再可能。
  const LAYER_WIDE_KEYWORDS = new Set([
    "initial",
    "inherit",
    "unset",
    "revert",
    "revert-layer",
    "default",
  ]);
  const IDENT_SEGMENT =
    /^-?(?:[_a-zA-Z]|[^\x00-\x7F])(?:[_a-zA-Z0-9-]|[^\x00-\x7F])*$/;
  const isValidLayerName = (name) =>
    name !== "" &&
    name
      .split(".")
      .every(
        (seg) =>
          IDENT_SEGMENT.test(seg) && !LAYER_WIDE_KEYWORDS.has(seg.toLowerCase()),
      );
  // 语句形 @layer a, b; 的层名登记：任一名字非法即整条不登记
  // （与浏览器整条丢弃同口径），问题逐个记录。
  const noteLayers = (namesText) => {
    const names = namesText
      .split(",")
      .map((part) => part.trim())
      .filter((name) => name !== "");
    let ok = true;
    for (const name of names) {
      if (!isValidLayerName(name)) {
        problems.push(
          `@layer 层名 "${name}" 不是合法层名，浏览器会整条丢弃该层声明`,
        );
        ok = false;
      }
    }
    if (!ok) return;
    for (const name of names) {
      if (!layerOrder.has(name)) layerOrder.set(name, layerSeq++);
    }
  };
  // 匿名 @layer 块：级联中每个匿名块是一个独立的新层、按出现位
  // 置参与层序，必须分配合成层名——落为 null 会被 consider 当作
  // 无层声明（普通级最高优先），与浏览器正相反（无层恒胜有
  // 层），层内存档旧值就此骗过求值（R8-02 / H-C4-01）。
  const freshAnonLayer = () => {
    const name = `(anon-layer-${layerSeq})`;
    layerOrder.set(name, layerSeq++);
    return name;
  };
  const winners = new Map();
  let srcIdx = 0;
  const rankCmp = (a, b) => {
    for (let i = 0; i < a.length; i++) {
      if (a[i] !== b[i]) return a[i] - b[i];
    }
    return 0;
  };
  const consider = (name, value, important, layerName, spec) => {
    const layered = layerName != null;
    const lo = layered ? (layerOrder.get(layerName) ?? 0) : 0;
    // 胜出顺序按 CSS 级联：!important 级高于普通级；层序先于特
    // 异度——普通级无层 > 有层（层间后提及者胜），!important 级
    // 反转（有层 > 无层、层间先提及者胜）；同层按特异度高者
    // 胜；再同按源码顺序后者胜。
    const rank = important
      ? [1, layered ? 1 : 0, -lo, spec[0], spec[1], spec[2], srcIdx]
      : [0, layered ? 0 : 1, lo, spec[0], spec[1], spec[2], srcIdx];
    srcIdx++;
    const cur = winners.get(name);
    if (!cur || rankCmp(rank, cur.rank) > 0) winners.set(name, { rank, value });
  };
  // 取从 openIdx（指向 "{"）起的花括号配平块：返回 { body, next }
  // （next 为配平 "}" 之后的位置），不配平返回 null。引号内的花
  // 括号不计深度。
  const extractBlock = (text, openIdx) => {
    let d = 0;
    let q = null;
    for (let j = openIdx; j < text.length; j++) {
      const c = text[j];
      if (q) {
        if (c === "\\") j++;
        else if (c === q) q = null;
        continue;
      }
      if (c === '"' || c === "'") { q = c; continue; }
      if (c === "{") d++;
      else if (c === "}") {
        d--;
        if (d === 0) return { body: text.slice(openIdx + 1, j), next: j + 1 };
      }
    }
    return null;
  };
  const applyDecl = (decl, layerName, spec) => {
    const m = decl.match(/^(--[a-zA-Z0-9-]+)\s*:\s*([\s\S]+)$/);
    if (!m) return;
    let value = m[2].trim();
    let important = false;
    const im = value.match(/!\s*important\s*$/i);
    if (im) {
      important = true;
      value = value.slice(0, im.index).trim();
    }
    consider(m[1], value, important, layerName, spec);
  };
  // ── 选择器建模（R9-18/R9-19）──────────────────────────────
  // 在指定分隔符的顶层（括号/方括号深度 0、引号外）切分文本。
  const splitTopLevel = (text, sep) => {
    const parts = [];
    let depth = 0;
    let bdepth = 0;
    let q = null;
    let cur = "";
    for (let i = 0; i < text.length; i++) {
      const c = text[i];
      if (q) {
        cur += c;
        if (c === "\\") {
          cur += text[i + 1] ?? "";
          i++;
        } else if (c === q) q = null;
        continue;
      }
      if (c === '"' || c === "'") { q = c; cur += c; continue; }
      if (c === "(") depth++;
      else if (c === ")") depth--;
      else if (c === "[") bdepth++;
      else if (c === "]") bdepth--;
      if (c === sep && depth === 0 && bdepth === 0) {
        parts.push(cur);
        cur = "";
        continue;
      }
      cur += c;
    }
    parts.push(cur);
    return parts;
  };
  const IDENT_AT = /^-?(?:[_a-zA-Z]|[^\x00-\x7F])(?:[_a-zA-Z0-9-]|[^\x00-\x7F])*/;
  // 解析单个复合选择器为简单选择器组件列；含组合子（空白/>/+/
  // ~）、无法解析的形态返回 null。函数型伪类的实参原文保留。
  const parseCompound = (sel) => {
    const comps = [];
    let i = 0;
    const n = sel.length;
    const readIdent = () => {
      const m = IDENT_AT.exec(sel.slice(i));
      if (!m) return null;
      i += m[0].length;
      return m[0];
    };
    const readParens = () => {
      // sel[i] === "("：返回配平内容并越过 ")"，不配平返回 null。
      let d = 0;
      let q = null;
      const start = i;
      while (i < n) {
        const c = sel[i];
        if (q) {
          if (c === "\\") i++;
          else if (c === q) q = null;
          i++;
          continue;
        }
        if (c === '"' || c === "'") { q = c; i++; continue; }
        if (c === "(") d++;
        else if (c === ")") {
          d--;
          if (d === 0) {
            const inner = sel.slice(start + 1, i);
            i++;
            return inner;
          }
        }
        i++;
      }
      return null;
    };
    while (i < n) {
      const c = sel[i];
      if (c === "*") {
        comps.push({ kind: "universal" });
        i++;
        continue;
      }
      if (c === ".") {
        i++;
        const name = readIdent();
        if (name === null) return null;
        comps.push({ kind: "class", name });
        continue;
      }
      if (c === "#") {
        i++;
        const name = readIdent();
        if (name === null) return null;
        comps.push({ kind: "id", name });
        continue;
      }
      if (c === "[") {
        let q = null;
        let closed = false;
        i++;
        while (i < n) {
          const ch = sel[i];
          if (q) {
            if (ch === "\\") i++;
            else if (ch === q) q = null;
          } else if (ch === '"' || ch === "'") q = ch;
          else if (ch === "]") {
            closed = true;
            i++;
            break;
          }
          i++;
        }
        if (!closed) return null;
        comps.push({ kind: "attr" });
        continue;
      }
      if (c === ":") {
        if (sel[i + 1] === ":") {
          i += 2;
          const name = readIdent();
          if (name === null) return null;
          if (sel[i] === "(") {
            if (readParens() === null) return null;
          }
          comps.push({ kind: "pseudo-el", name });
          continue;
        }
        i++;
        const name = readIdent();
        if (name === null) return null;
        let args = null;
        if (sel[i] === "(") {
          args = readParens();
          if (args === null) return null;
        }
        comps.push({ kind: "pseudo", name, args });
        continue;
      }
      if (/[_a-zA-Z-]|[^\x00-\x7F]/.test(c)) {
        if (comps.length > 0) return null; // 类型选择器只能居首
        const name = readIdent();
        if (name === null) return null;
        comps.push({ kind: "type", name });
        continue;
      }
      return null; // 空白/组合子/其余字符：非单个复合选择器
    }
    return comps;
  };
  const specCmp = rankCmp;
  const compoundSpec = (comps) => {
    let a = 0;
    let b = 0;
    let c = 0;
    for (const comp of comps) {
      if (comp.kind === "id") a++;
      else if (comp.kind === "class" || comp.kind === "attr") b++;
      else if (comp.kind === "type" || comp.kind === "pseudo-el") c++;
      else if (comp.kind === "pseudo") {
        const nm = comp.name.toLowerCase();
        if (nm === "where") continue;
        if (nm === "not" || nm === "is" || nm === "has") {
          // 函数型伪类自身不计、取实参中最高特异度（规范口径）。
          let best = null;
          for (const frag of splitTopLevel(comp.args ?? "", ",")) {
            const inner = parseCompound(frag.trim());
            if (!inner) continue;
            const s = compoundSpec(inner);
            if (!best || specCmp(s, best) > 0) best = s;
          }
          if (best) {
            a += best[0];
            b += best[1];
            c += best[2];
          }
        } else b++;
      }
    }
    return [a, b, c];
  };
  // 状态伪类（静止态求值不计，与 &:hover 先例同口径）。
  const STATE_PSEUDOS = new Set([
    "hover",
    "focus",
    "focus-visible",
    "focus-within",
    "active",
    "visited",
    "link",
    "target",
  ]);
  // 复合选择器是否可静态判定为「命中根元素」：须含正向 :root
  // 组件（伪类名 ASCII 大小写不敏感——:ROOT 与 :root 同效，
  // R9-18）；类型选择器只能缺省、* 或 html；.class/#id/[attr]
  // 组件依赖根元素的运行时属性集、伪元素命中的不是元素本
  // 体、状态伪类属非静止态——含此三类组件的分片静态不可判，
  // 不入集（与条件块不可判面同口径）。
  const isRootCompound = (comps) => {
    let positive = false;
    for (const comp of comps) {
      if (comp.kind === "universal") continue;
      if (comp.kind === "type") {
        if (comp.name.toLowerCase() !== "html") return false;
        continue;
      }
      if (comp.kind === "pseudo") {
        const nm = comp.name.toLowerCase();
        if (STATE_PSEUDOS.has(nm)) return false;
        if (nm === "root") {
          positive = true;
          continue;
        }
        if (nm === "is" || nm === "where") {
          const anyRoot = splitTopLevel(comp.args ?? "", ",")
            .some((frag) => {
              const inner = parseCompound(frag.trim());
              return inner !== null && isRootCompound(inner);
            });
          if (anyRoot) positive = true;
          continue;
        }
        continue;
      }
      return false; // class / id / attr / pseudo-el
    }
    return positive;
  };
  // 选择器列表中命中根元素的分片取最高特异度：返回 {spec,text}
  // 或 null（无分片命中）。html:root 的特异度 (0,1,1) 恒胜裸
  // :root 的 (0,1,0)、与源码位置无关（R9-19 / I-C4-04）。
  const rootMatch = (prelude) => {
    let best = null;
    for (const frag of splitTopLevel(prelude, ",")) {
      const comps = parseCompound(frag.trim());
      if (!comps || !isRootCompound(comps)) continue;
      const spec = compoundSpec(comps);
      if (!best || specCmp(spec, best.spec) > 0) {
        best = { spec, text: frag.trim() };
      }
    }
    return best;
  };
  // ── 恒真条件求值（R9-16 / I-C4-01）──────────────────────
  // 三值语义：true＝在面板渲染语境（屏幕介质、常规视口）下恒
  // 成立；false＝恒不成立；null＝取决于运行时环境、静态不可
  // 判。只有 true 的条件块按无条件走查；false 与 null 整棵排
  // 除（null 排除是既有口径——不可判面不入集，与选择器的不可
  // 判分片同例）。恒真形全集：@media 的 all/screen 介质、not
  // 对恒假式的否定、零下限特征（min-width: 0 等）、逗号列表任
  // 一恒真、and/or 组合；@supports 的恒真布尔式（白名单长青
  // 属性的合法声明原子及其 not/and/or 组合）。
  const zeroLength = (v) => {
    const m = /^(-?\d+(?:\.\d+)?)([a-z%]*)$/i.exec(v.trim());
    return m !== null && parseFloat(m[1]) === 0;
  };
  const posLength = (v) => {
    const m = /^(-?\d+(?:\.\d+)?)([a-z%]*)$/i.exec(v.trim());
    return m !== null && parseFloat(m[1]) > 0;
  };
  const evalMediaFeature = (raw) => {
    const content = raw.trim();
    if (content.startsWith("(") && content.endsWith(")")) {
      return evalMediaFeature(content.slice(1, -1));
    }
    const cmp = /^([a-z-]+)\s*(<=|>=|<|>|=)\s*(\S+)$/i.exec(content) ||
      /^(\S+)\s*(<=|>=|<|>|=)\s*([a-z-]+)$/i.exec(content);
    if (cmp) {
      // 区间语法（width >= 0px 等）：只对「与 0 比较」定值。
      const nameFirst = /^[a-z-]+$/i.test(cmp[1]);
      const name = (nameFirst ? cmp[1] : cmp[3]).toLowerCase();
      const op = nameFirst
        ? cmp[2]
        : { "<": ">", ">": "<", "<=": ">=", ">=": "<=", "=": "=" }[cmp[2]];
      const val = nameFirst ? cmp[3] : cmp[1];
      if (name !== "width" && name !== "height") return null;
      if (zeroLength(val)) {
        // 视口宽高恒 > 0（渲染语境）。
        return op === ">=" || op === ">" ? true : false;
      }
      return null;
    }
    const decl = /^([a-z-]+)\s*:\s*([\s\S]+)$/.exec(content);
    if (!decl) {
      // 布尔上下文特征：(width)/(height) 在真实视口恒非零。
      const name = content.toLowerCase();
      if (name === "width" || name === "height") return true;
      if (/^[a-z-]+$/.test(name)) return false; // 未知特征名恒假（规范）
      return null;
    }
    const name = decl[1].toLowerCase();
    const value = decl[2].trim();
    const base = name.replace(/^(min|max)-/, "");
    const isMin = name.startsWith("min-");
    const isMax = name.startsWith("max-");
    if (base === "width" || base === "height" || base === "resolution") {
      if (isMin) return zeroLength(value) ? true : posLength(value) ? null : false;
      if (isMax) return zeroLength(value) ? false : posLength(value) ? null : false;
      return zeroLength(value) ? false : posLength(value) ? null : false;
    }
    if (
      /^(hover|pointer|any-hover|any-pointer|orientation|prefers-|color|color-index|monochrome|grid|scan|update|overflow-|scripting|inverted-colors|forced-colors|display-mode|aspect-ratio|min-aspect-ratio|max-aspect-ratio|device-)/.test(
        name,
      )
    ) {
      return null; // 已知但取决于设备/用户环境
    }
    return false; // 未知特征名：按规范整条查询恒假
  };
  // 把 media 查询在顶层拆为 ( ) 组与骨架词，组以占位符回填。
  const evalMediaQuery = (query) => {
    const groups = [];
    let skeleton = "";
    let i = 0;
    const n = query.length;
    while (i < n) {
      if (query[i] === "(") {
        let d = 0;
        let q = null;
        const start = i;
        while (i < n) {
          const c = query[i];
          if (q) {
            if (c === "\\") i++;
            else if (c === q) q = null;
            i++;
            continue;
          }
          if (c === '"' || c === "'") { q = c; i++; continue; }
          if (c === "(") d++;
          else if (c === ")") {
            d--;
            if (d === 0) {
              i++;
              break;
            }
          }
          i++;
        }
        groups.push(query.slice(start + 1, i - 1));
        skeleton += ` \u0001${groups.length - 1}\u0002 `;
        continue;
      }
      skeleton += query[i];
      i++;
    }
    const words = skeleton.trim().split(/\s+/).filter(Boolean);
    let pos = 0;
    let negate = false;
    if (/^(only|not)$/i.test(words[0] ?? "")) {
      negate = words[0].toLowerCase() === "not";
      pos = 1;
    }
    // 原子序：[介质类型词?]（组占位符），以 and/or 连接。
    const atoms = [];
    const ops = [];
    let expectAtom = true;
    for (; pos < words.length; pos++) {
      const w = words[pos];
      const ph = /^\u0001(\d+)\u0002$/.exec(w);
      if (ph) {
        if (!expectAtom) return null;
        atoms.push(evalMediaFeature(groups[Number(ph[1])]));
        expectAtom = false;
        continue;
      }
      const lw = w.toLowerCase();
      if (lw === "and" || lw === "or") {
        if (expectAtom) return null;
        ops.push(lw);
        expectAtom = true;
        continue;
      }
      if (/^[a-z][a-z0-9-]*$/i.test(w)) {
        if (!expectAtom || atoms.length > 0) return null; // 类型词只能居首
        // 屏幕渲染语境：all/screen 恒真，print/speech 恒假，未
        // 知介质类型按规范恒不匹配。
        atoms.push(lw === "all" || lw === "screen" ? true : false);
        expectAtom = false;
        continue;
      }
      return null;
    }
    if (atoms.length === 0 || expectAtom) return null;
    // and 优先于 or：先折 and 链、再折 or。
    const orTerms = [];
    let cur = atoms[0];
    for (let k = 0; k < ops.length; k++) {
      const rhs = atoms[k + 1];
      if (ops[k] === "and") {
        cur = cur === false || rhs === false ? false : cur === null || rhs === null ? null : true;
      } else {
        orTerms.push(cur);
        cur = rhs;
      }
    }
    orTerms.push(cur);
    let v = orTerms.some((t) => t === true)
      ? true
      : orTerms.every((t) => t === false)
        ? false
        : null;
    if (negate && v !== null) v = !v;
    return v;
  };
  const evalMediaList = (text) => {
    const queries = splitTopLevel(text, ",").map((s) => s.trim());
    if (queries.length === 1 && queries[0] === "") return true; // 空列表＝all
    let anyUnknown = false;
    for (const q of queries) {
      const v = evalMediaQuery(q);
      if (v === true) return true;
      if (v === null) anyUnknown = true;
    }
    return anyUnknown ? null : false;
  };
  // @supports：白名单长青属性的声明原子（display 另验值在其关
  // 键字集内）、自定义属性原子恒真；未知属性与 selector() 等
  // 原子不可判（null）。原子以 not/and/or 与括号组合、and 优
  // 先于 or。
  const SUPPORTS_PROPS = new Set([
    "display", "position", "visibility", "color", "background",
    "background-color", "font-size", "font-family", "font-weight",
    "line-height", "width", "height", "min-width", "min-height",
    "max-width", "max-height", "margin", "padding", "border",
    "content", "float", "clear", "opacity", "transform",
    "transition", "overflow", "cursor", "text-align",
    "vertical-align", "box-sizing", "flex", "flex-direction",
    "justify-content", "align-items", "gap", "grid", "z-index",
    "top", "left", "right", "bottom",
  ]);
  const DISPLAY_VALUES = new Set([
    "block", "inline", "inline-block", "flex", "inline-flex",
    "grid", "inline-grid", "none", "contents", "flow-root",
    "table", "inline-table", "table-row", "table-cell",
    "list-item",
  ]);
  const evalSupportsAtom = (inner) => {
    const decl = /^(--[-_a-zA-Z0-9]+|[-_a-zA-Z][-_a-zA-Z0-9]*)\s*:\s*([\s\S]+)$/.exec(
      inner.trim(),
    );
    if (!decl) return null;
    const prop = decl[1].toLowerCase();
    const value = decl[2].trim().toLowerCase();
    if (prop.startsWith("--")) return true;
    if (!SUPPORTS_PROPS.has(prop)) return null;
    if (prop === "display") {
      return DISPLAY_VALUES.has(value.split(/\s+/)[0]) ? true : false;
    }
    return value !== "" ? true : false;
  };
  const evalSupports = (text) => {
    // 词法：括号组递归下降（声明原子＝不含顶层 and/or/not 的括
    // 号组）、and/or/not 关键字。
    const parseInner = (s) => {
      const toks = [];
      let i = 0;
      const n = s.length;
      while (i < n) {
        const c = s[i];
        if (/\s/.test(c)) { i++; continue; }
        if (c === "(") {
          let d = 0;
          let q = null;
          const start = i;
          let closed = false;
          while (i < n) {
            const ch = s[i];
            if (q) {
              if (ch === "\\") i++;
              else if (ch === q) q = null;
              i++;
              continue;
            }
            if (ch === '"' || ch === "'") { q = ch; i++; continue; }
            if (ch === "(") d++;
            else if (ch === ")") {
              d--;
              if (d === 0) {
                i++;
                closed = true;
                break;
              }
            }
            i++;
          }
          if (!closed) return null;
          toks.push({ group: s.slice(start + 1, i - 1) });
          continue;
        }
        const m = /^[a-zA-Z-]+/.exec(s.slice(i));
        if (m) {
          toks.push({ word: m[0].toLowerCase() });
          i += m[0].length;
          continue;
        }
        return null;
      }
      return toks;
    };
    const evalTokens = (toks) => {
      let pos = 0;
      const parseUnary = () => {
        const t = toks[pos];
        if (!t) return null;
        if (t.word === "not") {
          pos++;
          const v = parseUnary();
          return v === null ? null : !v;
        }
        if (t.group !== undefined) {
          pos++;
          const inner = t.group;
          // 声明原子（含冒号且无顶层布尔词）先判，否则递归。
          if (!/\b(and|or|not)\b/.test(inner)) {
            const atom = evalSupportsAtom(inner);
            if (atom !== null) return atom;
            if (/:/.test(inner)) return null; // 声明形但属性不可判
          }
          const sub = parseInner(inner);
          if (!sub) return null;
          return evalTokens(sub);
        }
        return null;
      };
      const parseAnd = () => {
        let v = parseUnary();
        while (toks[pos] && toks[pos].word === "and") {
          pos++;
          const rhs = parseUnary();
          v = v === false || rhs === false ? false : v === null || rhs === null ? null : true;
        }
        return v;
      };
      let v = parseAnd();
      while (toks[pos] && toks[pos].word === "or") {
        pos++;
        const rhs = parseAnd();
        v = v === true || rhs === true ? true : v === null || rhs === null ? null : false;
      }
      return pos === toks.length ? v : null;
    };
    const toks = parseInner(text.trim());
    if (!toks || toks.length === 0) return null;
    return evalTokens(toks);
  };
  // 条件 at-rule 的处置：@media/@supports 恒真才走查（上下文不
  // 变——条件不改变选择器/层语境）；其余块级 at-rule（@keyframes
  // /@font-face/@container/未知）整棵排除（既有口径）。
  const condRuleWalks = (prelude) => {
    const m = /^@([a-zA-Z-]+)/.exec(prelude);
    if (!m) return false;
    const name = m[1].toLowerCase();
    const rest = prelude.slice(m[0].length).trim();
    if (name === "media") return evalMediaList(rest) === true;
    if (name === "supports") return evalSupports(rest) === true;
    return false;
  };
  // 单条规则按上下文分发。frames 记 at-rule 与祖先选择器上下文：
  // 存在 type:"rule" 祖先时，内层 :root 的有效选择器是「祖先
  // :root」——根元素不可能是任何元素的后代，恒不匹配，不计数，
  // 仅继续遍历（R8-03 / H-C4-02）。
  const dispatchRule = (prelude, body, frames) => {
    if (/^@layer\b/i.test(prelude)) {
      const names = prelude.replace(/^@layer\b/i, "").trim();
      if (names === "") {
        // 匿名层分配合成层名参与层序（R8-02 / H-C4-01）。
        walk(body, [...frames, { type: "layer", name: freshAnonLayer() }]);
        return;
      }
      const parts = splitTopLevel(names, ",")
        .map((s) => s.trim())
        .filter((s) => s !== "");
      if (parts.length !== 1 || !isValidLayerName(parts[0])) {
        // 具名块只许单名；名单/非法名在浏览器中整块丢弃。
        problems.push(
          `@layer 规则块层名 "${names}" 不是合法单层名，浏览器会整条丢弃该规则块`,
        );
        return;
      }
      noteLayers(parts[0]);
      walk(body, [...frames, { type: "layer", name: parts[0] }]);
    } else if (prelude.startsWith("@")) {
      // 恒真条件块走查、余者整棵排除（R9-16 / I-C4-01）。
      if (condRuleWalks(prelude)) walk(body, frames);
      return;
    } else {
      if (frames.some((f) => f.type === "cond")) return;
      const inner = [...frames, { type: "rule" }];
      if (frames.some((f) => f.type === "rule")) {
        walk(body, inner);
        return;
      }
      const layerFrame = [...frames]
        .reverse()
        .find((f) => f.type === "layer");
      const match = rootMatch(prelude);
      if (match) {
        parseDecls(body, layerFrame ? layerFrame.name : null, frames, match);
      } else {
        walk(body, inner);
      }
    }
  };
  // 解析一个根规则体（R8-03 / H-C4-02 起按花括号深度切分）：
  // 声明以 ";" 收尾；若先遇 "{"，此前分片是嵌套规则的 prelude，
  // 整块交回分发按上下文处置——嵌套条件块内第二位起的声明不再
  // 被当作无条件声明逐片计数。match 是本规则命中根元素的
  // {spec, text}：嵌套 "&" 即父选择器本身、"&:root" 展开为
  // 「父选择器＋:root」的复合（:root:root 特异度 (0,2,0) 恒
  // 胜，R9-20 / I-C4-05），展开后按同一复合判定处置。
  const parseDecls = (body, layerName, frames, match) => {
    let i = 0;
    const n = body.length;
    while (i < n) {
      let paren = 0;
      let q = null;
      const start = i;
      let kind = null;
      while (i < n) {
        const c = body[i];
        if (q) {
          if (c === "\\") i++;
          else if (c === q) q = null;
          i++;
          continue;
        }
        if (c === '"' || c === "'") { q = c; i++; continue; }
        if (c === "(") paren++;
        else if (c === ")") paren--;
        else if (paren === 0 && c === ";") { kind = "decl"; break; }
        else if (paren === 0 && c === "{") { kind = "rule"; break; }
        i++;
      }
      if (kind === null) {
        applyDecl(body.slice(start).trim(), layerName, match.spec);
        break;
      }
      if (kind === "decl") {
        applyDecl(body.slice(start, i).trim(), layerName, match.spec);
        i++;
        continue;
      }
      const prelude = body.slice(start, i).trim();
      const blk = extractBlock(body, i);
      if (!blk) break;
      i = blk.next;
      if (prelude === "&") {
        parseDecls(blk.body, layerName, frames, match);
      } else if (prelude.startsWith("&")) {
        const expanded = match.text + prelude.slice(1);
        const comps = parseCompound(expanded);
        if (comps && isRootCompound(comps)) {
          parseDecls(blk.body, layerName, frames, {
            spec: compoundSpec(comps),
            text: expanded,
          });
        } else {
          dispatchRule(prelude, blk.body, [...frames, { type: "rule" }]);
        }
      } else if (prelude.startsWith("@")) {
        if (/^@layer\b/i.test(prelude)) {
          dispatchRule(prelude, blk.body, [...frames, { type: "rule" }]);
        } else if (condRuleWalks(prelude)) {
          // 恒真条件不改变选择器语境：仍按本规则体解析。
          parseDecls(blk.body, layerName, frames, match);
        }
        // 余者（恒假/不可判条件、其他 at-rule）：整块跳过。
      } else {
        dispatchRule(prelude, blk.body, [...frames, { type: "rule" }]);
      }
    }
  };
  const walk = (text, frames) => {
    let i = 0;
    const n = text.length;
    while (i < n) {
      let pre = "";
      let depth = 0;
      let q = null;
      let term = null;
      while (i < n) {
        const c = text[i];
        if (q) {
          pre += c;
          if (c === "\\") { pre += text[i + 1] ?? ""; i += 2; continue; }
          if (c === q) q = null;
          i++;
          continue;
        }
        if (c === '"' || c === "'") { q = c; pre += c; i++; continue; }
        if (c === "(") depth++;
        else if (c === ")") depth--;
        if (depth === 0 && (c === "{" || c === ";" || c === "}")) {
          term = c;
          i++;
          break;
        }
        pre += c;
        i++;
      }
      const prelude = pre.trim();
      if (term === null || term === "}") break;
      if (term === ";") {
        if (/^@layer\b/i.test(prelude)) {
          noteLayers(prelude.replace(/^@layer\b/i, ""));
        }
        continue;
      }
      const blk = extractBlock(text, i - 1);
      if (!blk) break;
      i = blk.next;
      dispatchRule(prelude, blk.body, frames);
    }
  };
  walk(code, []);
  const out = {};
  for (const [k, v] of winners) out[k] = v.value;
  return { tokens: out, problems };
};

const { tokens: resolvedTokens, problems: evalProblems } =
  resolveRootTokens(stripCssComments(css));
const tokens = {};
for (const [k, v] of Object.entries(resolvedTokens)) {
  if (/^#[0-9a-fA-F]{6}$/.test(v)) tokens[k] = v.toLowerCase();
}

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

const luminance = (hex) => {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;

    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  });

  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

const contrast = (fgHex, bgHex) => {
  const [hi, lo] = [luminance(fgHex), luminance(bgHex)].sort((a, b) => b - a);

  return (hi + 0.05) / (lo + 0.05);
};

// [前景 token, 背景 token, 钉值]——钉值由本脚本首跑实测并与 A-front
// 审计的独立复算逐项对齐（正文 17.72/16.97、次级 -700 10.44/10.01、
// primary-700 7.90、success/warning-700 5.02/4.81、danger-700
// 6.47/6.20、托盘六对 8.15–9.50）。
const PAIRS = [
  ["--foreground", "--content1", 17.72],
  ["--foreground", "--background", 16.97],
  ["--default-700", "--content1", 10.44],
  ["--default-700", "--background", 10.01],
  ["--default-800", "--content1", 14.89],
  ["--default-900", "--background", 16.97],
  ["--primary-700", "--content1", 7.9],
  ["--primary-700", "--background", 7.57],
  ["--primary-800", "--content1", 9.93],
  ["--secondary-700", "--content1", 7.1],
  ["--success-700", "--content1", 5.02],
  ["--success-700", "--background", 4.81],
  ["--warning-700", "--content1", 5.02],
  ["--warning-700", "--background", 4.81],
  ["--danger-700", "--content1", 6.47],
  ["--danger-700", "--background", 6.2],
  ["--tray-primary-foreground", "--tray-primary", 9.27],
  ["--tray-secondary-foreground", "--tray-secondary", 9.23],
  ["--tray-success-foreground", "--tray-success", 8.3],
  ["--tray-warning-foreground", "--tray-warning", 8.15],
  ["--tray-danger-foreground", "--tray-danger", 8.2],
  ["--tray-default-foreground", "--tray-default", 9.5],
  ["#ffffff", "--primary-800", 9.93],
  ["#ffffff", "--success-800", 7.13],
  ["#ffffff", "--danger-800", 8.31],
];

check("对比度基线恰 25 对", PAIRS.length === 25, `实际 ${PAIRS.length} 对`);

for (const [fg, bg, pinned] of PAIRS) {
  const fgHex = fg.startsWith("#") ? fg : tokens[fg];
  const bgHex = bg.startsWith("#") ? bg : tokens[bg];

  if (!fgHex || !bgHex) {
    check(
      `${fg} on ${bg}`,
      false,
      "token 未在 globals.css 的无条件 :root 中解析到 #rrggbb 生效值",
    );

    continue;
  }
  const ratio = contrast(fgHex, bgHex);

  check(
    `${fg} on ${bg} = ${ratio.toFixed(2)}（钉值 ${pinned}、底线 4.5）`,
    ratio >= 4.5 && Math.abs(ratio - pinned) <= 0.1,
    `实测 ${ratio.toFixed(2)}`,
  );
}


// 级联求值建模问题（R9-17 的 fail-closed 通道）：求值中记录的
// 非法层名等问题在两门逐个点名报红，不并入 token 胜出判定。
for (const p of evalProblems) {
  check("globals.css 级联求值建模问题", false, p);
}

// ── 求值器钉格（修复轮 9，R9-16～R9-20）────────────────────
// 以合成 CSS 直接实测 resolveRootTokens 的级联语义，逐格对应
// 五条立案的浏览器真值（构造原文：I-C4 §2、VERIFY8-A §2）。
// 任一格回归即点名报红；对照格钉住排除方向（不可判面不入集、
// 静止态不计状态伪类、祖先内 :root 恒不匹配）防过度入集回归。
const fxResolve = (cssText) => resolveRootTokens(stripCssComments(cssText));
const FX_BASE = ":root { --fx-probe: #111111; }";
const fxVal = (cssText) => fxResolve(cssText).tokens["--fx-probe"];

check(
  "求值器钉格：@media all 尾置坏值按恒真走查胜出（R9-16）",
  fxVal(`${FX_BASE}\n@media all { :root { --fx-probe: #222222; } }`) ===
    "#222222",
  `实得 ${fxVal(`${FX_BASE}\n@media all { :root { --fx-probe: #222222; } }`)}`,
);
check(
  "求值器钉格：@media all 存档好值胜出、顶层坏值不假红（R9-16）",
  fxVal(
    ":root { --fx-probe: #222222; }\n@media all { :root { --fx-probe: #111111; } }",
  ) === "#111111",
);
check(
  "求值器钉格：@media screen 与 (min-width: 0px) 恒真走查（R9-16）",
  fxVal(`${FX_BASE}\n@media screen { :root { --fx-probe: #222222; } }`) ===
    "#222222" &&
    fxVal(
      `${FX_BASE}\n@media (min-width: 0px) { :root { --fx-probe: #222222; } }`,
    ) === "#222222",
);
check(
  "求值器钉格：@media print 恒假排除、视口条件不可判维持排除（R9-16 对照）",
  fxVal(`${FX_BASE}\n@media print { :root { --fx-probe: #222222; } }`) ===
    "#111111" &&
    fxVal(
      `${FX_BASE}\n@media (max-width: 768px) { :root { --fx-probe: #222222; } }`,
    ) === "#111111",
);
check(
  "求值器钉格：@supports (display: block) 恒真走查、未知属性不可判排除（R9-16）",
  fxVal(
    `${FX_BASE}\n@supports (display: block) { :root { --fx-probe: #222222; } }`,
  ) === "#222222" &&
    fxVal(
      `${FX_BASE}\n@supports (made-up-prop: 1) { :root { --fx-probe: #222222; } }`,
    ) === "#111111",
);
check(
  "求值器钉格：:root 内嵌 @media all 仍按本规则体走查（R9-16）",
  fxVal(`${FX_BASE}\n:root { @media all { --fx-probe: #222222; } }`) ===
    "#222222",
);
const fxCollision = fxResolve(`@layer (anon-layer-1);\n${FX_BASE}`);
check(
  "求值器钉格：预登记非法层名记为建模问题且不入层序（R9-17）",
  fxCollision.problems.some((p) => p.includes("(anon-layer-1)")) &&
    fxCollision.tokens["--fx-probe"] === "#111111",
  `problems=${JSON.stringify(fxCollision.problems)}`,
);
check(
  "求值器钉格：匿名层存档不胜无层出厂值（R8-02 对照）",
  fxVal(`${FX_BASE}\n@layer { :root { --fx-probe: #222222; } }`) === "#111111",
);
check(
  "求值器钉格：:ROOT 大写伪类按大小写不敏感计数（R9-18）",
  fxVal(`${FX_BASE}\n:ROOT { --fx-probe: #222222; }`) === "#222222",
);
check(
  "求值器钉格：html:root 特异度恒胜裸 :root、与位置无关（R9-19）",
  fxVal(`html:root { --fx-probe: #222222; }\n${FX_BASE}`) === "#222222" &&
    fxVal(`:root:not(.fx-off) { --fx-probe: #222222; }\n${FX_BASE}`) ===
      "#222222",
);
check(
  "求值器钉格：嵌套 &:root 展开 :root:root 恒胜（R9-20）",
  fxVal(`${FX_BASE}\n:root { &:root { --fx-probe: #222222; } }`) === "#222222",
);
check(
  "求值器钉格：&:hover 静止态不计、祖先内 :root 恒不匹配（对照）",
  fxVal(
    ":root { --fx-probe: #222222; }\n:root { &:hover { --fx-probe: #111111; } }",
  ) === "#222222" &&
    fxVal(`${FX_BASE}\n.fx-wrap { :root { --fx-probe: #222222; } }`) ===
      "#111111",
);

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
