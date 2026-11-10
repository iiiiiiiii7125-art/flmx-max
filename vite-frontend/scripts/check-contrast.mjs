// check-contrast.mjs — 文字对比度回归校验（无测试框架版）。
//
// 定案纪律：亮色单模＋哑光玻璃面＋托盘承载小字，正文文字用所属
// 色族最深档（-700 起），禁灰字，对比度目标 7、底线 4.5（WCAG AA
// 正文）。2026-11-04 玻璃恢复后，「面」的定义分三层：实色面
// （--content1 表单填充／--background 页底）沿用下方 25 对钉值；
// 玻璃面（三级配方）按文末「玻璃合成网格」段复算——梯度停点与四
// 种底色（纯黑／纯白／内置壁纸明暗双区采样）逐格合成后断言正文
// 对；小字恒在不透明托盘上（托盘六对在 25 对内），不入玻璃网格。
// R2-S1-01/02/05 扩集与 ghost 同族尾项（文末各段）：托盘 hover 六对入断言（>= 7）；
// 语义色文字在玻璃语境下经 --glass-text-* 深档渲染，其全格网格与
// src 普查（fail-closed 差集）入断言；桥 Button 的半透明白 wash
// 载体族退役、由托盘与实色 -800 档承接，退役签名入断言。
// R3 扩集（文末三段）：① 语义文字普查扩至全档位（100–900）并对
// 透明后缀按 alpha 合成值复算（R3-C6-02，此前 100–400 浅档看不见、
// /60 等后缀按不透明值算、均恒绿）；② 中性 default 玻璃面登记
// 收窄（R3-S1-01：承载主要内容的 -700 已改 -800，留置 -700 逐文件
// 钉数、仅限不透明钉面/内嵌安全格/枚举的次级信息，边界格 3.67
// 只作登记记录）；③ 图表系列色入册（R3-S1-02：按非文本 3:1 对
// 默认底常态格断言，纯黑壁纸界格钉值登记、靠图例/tooltip 冗余）。
// 此前该自检只是
// 审计时的一次性临时脚本、未落仓库（A-front 复核时独立复算 25 对
// 全过），本脚本把它沉淀为常设门：解析 src/styles/globals.css 的
// :root token 实值，按 WCAG 相对亮度公式复算 25 对正文/托盘文字
// 对，逐对断言 ① 不低于 4.5 底线；② 与钉值偏差 ≤ 0.1——token 被
// 改浅（或改深到改变设计值）都会点名，改值须同步改钉值（有意变更
// 留痕）。主题系统已随 2.1.0 删除、全站只剩一套默认主题，token 全
// 部住在 globals.css :root——本门守的就是这套唯一 token。
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
// 块交回分发。求值器自含、不跨文件引用（原与主题包校验门内嵌的
// resolveRootTokens 逐字同构；该门已随主题系统于 2.1.0 一并删除，
// 本求值器自此独立存续）。
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
import { readFileSync, readdirSync } from "node:fs";
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


// ── 玻璃合成网格（2026-11-04 玻璃恢复，I-C5 注记 2 承接）─────
// 层叠变更必须重建采样网格：本段即随哑光玻璃层叠重建的网格。
// 模型：三级玻璃配方（卡片 --glass-card／浮层 --glass-bg／内嵌
// .glass-inset）的三个白色梯度停点，逐个与四种底合成（sRGB 空间
// alpha 合成，与浏览器一致）——纯黑、纯白（自定义壁纸双极端）、
// 内置壁纸 bg-light.jpg 的暗区/明区代表色（8×8 瓦片均值采样，
// 2026-11-04 实测：暗区 #01043c、明区 #d4d2b7；资源更换须重采样
// 并改钉值，资源存在性由 check-wallpaper-chain 门守）。内嵌层不
// 直接贴壁纸：其底为卡片层最低停点先行合成的结果（最差载体）。
// 断言：① 三级停点与 backdrop blur 逐值钉住（14/18/10px），改浅
// 或降 blur 都会点名；② 正文色 --foreground 在全部 36 格 ≥ 4.5
// 底线；③ 卡片中位停点在四种底上 ≥ 7 目标、浮层最低停点在黑/
// 暗区底上 ≥ 7；④ 四个最差格钉值 ±0.1。模型边界（明示）：中性
// 文字 default-700 在卡片最低停点×纯黑底一格为 3.67——原登记
// 边界（中性次级信息）经 R3-S1-01 复核后收窄：37 处用量中承载
// 主要内容者（表单标签/整段说明/正文行/名称/标题/数据值/透明
// 底占位符/桥 Link 默认色等 22 处）已改 -800，留置 -700 仅限
// 不透明钉面（content1 占位符、托盘面）、玻璃内嵌安全格
// （5.15）与逐文件钉数的次级信息面，登记与普查钉数见文末
// 「中性 default 玻璃面登记」段；3.67 格在本门只作边界记录
// 钉值、不作通过断言。语义色族文字不在此列——R2-S1-01 后它
// 们在玻璃语境下经深档渲染、全格入下文断言。
const lumRgb = (rgb) => {
  const f = (c) => {
    const s = c / 255;

    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  };

  return 0.2126 * f(rgb[0]) + 0.7152 * f(rgb[1]) + 0.0722 * f(rgb[2]);
};
const contrastRgb = (a, b) => {
  const [hi, lo] = [lumRgb(a), lumRgb(b)].sort((x, y) => y - x);

  return (hi + 0.05) / (lo + 0.05);
};
const hexToRgb = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const compositeOver = (alpha, bg) =>
  [0, 1, 2].map((i) => alpha * 255 + (1 - alpha) * bg[i]);
const gradientStops = (value) =>
  typeof value === "string"
    ? [...value.matchAll(/rgb\(255 255 255 \/ ([\d.]+)\)/g)].map((m) =>
        parseFloat(m[1]),
      )
    : [];
const classBlock = (cls) => {
  const m = stripCssComments(css).match(
    new RegExp(`\\.${cls}\\s*\\{([^}]*)\\}`),
  );

  return m ? m[1] : "";
};
// 类内嵌渐变的停点只从 background 声明取（边框色同为白色 alpha，
// 不限定声明面会把 border 的 0.5 误作第四停点）。
const blockGradientStops = (block) => {
  const m = block.match(/background:\s*linear-gradient\(([^;]*)\)/);

  return m ? gradientStops(m[1]) : [];
};
const blurOf = (cls) => {
  const m = classBlock(cls).match(/backdrop-filter:\s*blur\((\d+)px\)/);

  return m ? parseInt(m[1], 10) : null;
};

const GLASS_TIERS = [
  { name: "卡片 glass-card", stops: gradientStops(resolvedTokens["--glass-card"]), pinned: [0.78, 0.66, 0.6], blur: blurOf("glass-card"), pinnedBlur: 14 },
  { name: "浮层 glass-surface", stops: gradientStops(resolvedTokens["--glass-bg"]), pinned: [0.85, 0.75, 0.7], blur: blurOf("glass-surface"), pinnedBlur: 18 },
  { name: "内嵌 glass-inset", stops: blockGradientStops(classBlock("glass-inset")), pinned: [0.5, 0.35, 0.28], blur: blurOf("glass-inset"), pinnedBlur: 10 },
];
const GLASS_BACKDROPS = [
  ["纯黑", [0, 0, 0]],
  ["壁纸暗区 #01043c", hexToRgb("#01043c")],
  ["壁纸明区 #d4d2b7", hexToRgb("#d4d2b7")],
  ["纯白", [255, 255, 255]],
];
const fgRgb = tokens["--foreground"] ? hexToRgb(tokens["--foreground"]) : null;

for (const tier of GLASS_TIERS) {
  check(
    `玻璃停点钉值：${tier.name} = [${tier.pinned.join(", ")}]、blur ${tier.pinnedBlur}px`,
    JSON.stringify(tier.stops) === JSON.stringify(tier.pinned) &&
      tier.blur === tier.pinnedBlur,
    `实测停点 [${tier.stops.join(", ")}]、blur ${tier.blur}px`,
  );
}

check(
  "玻璃合成：--toaster-bg 与浮层 --glass-bg 同停点（浮层档）",
  JSON.stringify(gradientStops(resolvedTokens["--toaster-bg"])) ===
    JSON.stringify(GLASS_TIERS[1].stops),
  `实测 [${gradientStops(resolvedTokens["--toaster-bg"]).join(", ")}]`,
);

const glassCell = {};
if (fgRgb && GLASS_TIERS.every((t) => t.stops.length === 3)) {
  for (const tier of GLASS_TIERS) {
    for (const [bgName, bg] of GLASS_BACKDROPS) {
      tier.stops.forEach((stop, si) => {
        const carrier =
          tier.name.startsWith("内嵌")
            ? compositeOver(GLASS_TIERS[0].stops[2], bg)
            : bg;
        const ratio = contrastRgb(fgRgb, compositeOver(stop, carrier));

        glassCell[`${tier.name}|${bgName}|${si}`] = ratio;
        check(
          `玻璃合成格：--foreground on ${tier.name} 停点${si + 1}(${stop}) × ${bgName} = ${ratio.toFixed(2)}（底线 4.5）`,
          ratio >= 4.5,
          `实测 ${ratio.toFixed(2)}`,
        );
      });
    }
  }
  for (const [bgName] of GLASS_BACKDROPS) {
    const mid = glassCell[`卡片 glass-card|${bgName}|1`];

    check(
      `玻璃合成目标格：--foreground on 卡片中位停点 × ${bgName} = ${mid?.toFixed(2)}（目标 7）`,
      mid !== undefined && mid >= 7,
      `实测 ${mid?.toFixed(2)}`,
    );
  }
  const WORST_PINS = [
    ["卡片 glass-card|纯黑|2", 6.22],
    ["卡片 glass-card|壁纸暗区 #01043c|2", 6.46],
    ["浮层 glass-surface|纯黑|2", 8.4],
    ["内嵌 glass-inset|纯黑|2", 8.69],
  ];

  for (const [key, pinned] of WORST_PINS) {
    const v = glassCell[key];

    check(
      `玻璃合成最差格钉值：${key} = ${v?.toFixed(2)}（钉值 ${pinned}）`,
      v !== undefined && Math.abs(v - pinned) <= 0.1,
      `实测 ${v?.toFixed(2)}`,
    );
  }
} else {
  check("玻璃合成网格可复算（--foreground 与三级停点齐备）", false, "token 或停点缺失");
}

// ── 低透明前景普查（F-W1-06 同族复发，2026-11-10 修复波路 5）──
// Round 6 已清零的 text-foreground/60 在 2.1.2 新文件
// group-manage-modal.tsx:175 复发：玻璃载体最差格复算 3.48–4.11 ＜ 4.5，
// 而本门此前只钉全不透明 --foreground 的 36 格合成值，带透明后缀的
// 小字绕过了门。本段把该族迁入常设门：机械普查 src 内全部
// text-foreground/<alpha> 字面用法，逐处按 alpha 把 --foreground 合成
// 到载体上再与载体算比值，取玻璃 36 格＋实色两底的最差格断言 ≥ 4.5
// （底线）；命中 0 处时仍断言普查面有效，扫描面失效即红，不许空集
// 冒充通过。变异验活：任一处新植 text-foreground/60 须红（见本段
// 末尾注记；修复波 W5 验活记录在 W5-report.md）。
{
  const fg = tokens["--foreground"] ? hexToRgb(tokens["--foreground"]) : null;
  const carriers = [];
  if (fg && GLASS_TIERS.every((t) => t.stops.length === 3)) {
    for (const tier of GLASS_TIERS) {
      for (const [bgName, bg] of GLASS_BACKDROPS) {
        tier.stops.forEach((stop, si) => {
          const base =
            tier.name.startsWith("内嵌")
              ? compositeOver(GLASS_TIERS[0].stops[2], bg)
              : bg;
          carriers.push([
            `${tier.name} 停点${si + 1} × ${bgName}`,
            compositeOver(stop, base),
          ]);
        });
      }
    }
  }
  for (const t of ["--content1", "--background"]) {
    if (tokens[t]) carriers.push([`实色 ${t}`, hexToRgb(tokens[t])]);
  }
  const hits = [];
  let scannedFiles = 0;
  const fgWalk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        fgWalk(path);
      } else if (/\.tsx?$/.test(entry.name)) {
        scannedFiles++;
        const lines = readFileSync(path, "utf8").split("\n");
        lines.forEach((line, i) => {
          for (const m of line.matchAll(
            /text-foreground\/(?:(\d+)|\[([\d.]+)\])/g,
          )) {
            const alpha = m[1]
              ? parseInt(m[1], 10) / 100
              : parseFloat(m[2]);
            hits.push({ file: path, line: i + 1, alpha });
          }
        });
      }
    }
  };
  fgWalk(join(here, "..", "src"));
  if (!fg || carriers.length === 0) {
    check(
      "低透明前景普查可复算（--foreground 与载体网格齐备）",
      false,
      "token 或载体缺失",
    );
  } else {
    const bad = [];
    for (const h of hits) {
      let worst = Infinity;
      let worstCarrier = "";
      for (const [cname, surface] of carriers) {
        const eff = [0, 1, 2].map(
          (i) => h.alpha * fg[i] + (1 - h.alpha) * surface[i],
        );
        const r = contrastRgb(eff, surface);
        if (r < worst) {
          worst = r;
          worstCarrier = cname;
        }
      }
      if (worst < 4.5) {
        bad.push(
          `${h.file.split("/src/")[1]}:${h.line} /${Math.round(h.alpha * 100)} 最差 ${worst.toFixed(2)}（${worstCarrier}）`,
        );
      }
    }
    check(
      `低透明前景普查差集 ∅（text-foreground/<alpha> 实扫 ${scannedFiles} 文件、命中 ${hits.length} 处，最差格底线 4.5）`,
      bad.length === 0,
      bad.slice(0, 5).join("；"),
    );
  }
  check(
    "低透明前景普查面有效（实扫文件数 > 0）",
    scannedFiles > 0,
    "普查扫描面失效",
  );
}

// ── 托盘 hover 对（R2-S1-05）──────────────────────────────
// 此前 PAIRS 只钉托盘静止对，hover 对不在断言内，tray-danger
// hover 一度跌到 6.93（底线虽守、目标 7 未达）无人报。现 hover
// 六对同入断言：>= 7 目标＋钉值 ±0.1，改浅任一 hover 底色点名。
const TRAY_HOVER_PAIRS = [
  ["--tray-primary-foreground", "--tray-primary-hover", 7.66],
  ["--tray-secondary-foreground", "--tray-secondary-hover", 7.89],
  ["--tray-success-foreground", "--tray-success-hover", 7.52],
  ["--tray-warning-foreground", "--tray-warning-hover", 7.28],
  ["--tray-danger-foreground", "--tray-danger-hover", 7.18],
  ["--tray-default-foreground", "--tray-default-hover", 8.23],
];

for (const [fg, bg, pinned] of TRAY_HOVER_PAIRS) {
  const fgHex = tokens[fg];
  const bgHex = tokens[bg];

  if (!fgHex || !bgHex) {
    check(`托盘 hover 对 ${fg} on ${bg}`, false, "token 未解析到 #rrggbb 生效值");
    continue;
  }
  const ratio = contrast(fgHex, bgHex);

  check(
    `托盘 hover 对：${fg} on ${bg} = ${ratio.toFixed(2)}（钉值 ${pinned}、目标 7）`,
    ratio >= 7 && Math.abs(ratio - pinned) <= 0.1,
    `实测 ${ratio.toFixed(2)}`,
  );
}

// ── 托盘类使用点普查（F-W1-02）────────────────────────────
// tray-plate 是小字的不透明托盘载体（Round 6 定规：小字恒在不
// 透明托盘上）。审计终局登记「仅 2 处」却无门禁钉册，新增使用
// 点静默放行。本段机械扫 src 全部 .ts/.tsx 字符串字面量内的
// tray-plate，按 file:line 与登记集双向相等断言（多一处/少一
// 处都红；新增使用点先登记再合入）。字面量口径：注释内提及
// 不计数（与 G5 门同口径）；globals.css 的 .tray-plate 定义
// 住在 .css 内，天然不在 .ts/.tsx 普查面内。
const TRAY_PLATE_REGISTERED = [
  "src/pages/inbound/kernel-panel.tsx:358",
  "src/pages/config.tsx:1249",
];
{
  const foundPlates = [];
  const plateWalk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        plateWalk(path);
      } else if (/\.tsx?$/.test(entry.name)) {
        const text = readFileSync(path, "utf8");
        const rel = path.slice(join(here, "..").length + 1);
        for (const m of text.matchAll(/"([^"\n]*)"|'([^'\n]*)'|`([^`]*)`/g)) {
          const body = m[1] ?? m[2] ?? m[3] ?? "";
          if (!body.includes("tray-plate")) continue;
          const line = text.slice(0, m.index).split("\n").length;
          foundPlates.push(`${rel}:${line}`);
        }
      }
    }
  };
  plateWalk(join(here, "..", "src"));
  const plateExtra = foundPlates.filter((f) => !TRAY_PLATE_REGISTERED.includes(f));
  const plateMissing = TRAY_PLATE_REGISTERED.filter((f) => !foundPlates.includes(f));
  check(
    `托盘类 tray-plate 使用点普查闭合（实扫 ${foundPlates.length} 处、登记 ${TRAY_PLATE_REGISTERED.length} 处）`,
    plateExtra.length === 0 && plateMissing.length === 0,
    [
      plateExtra.length ? `未登记：${plateExtra.join(", ")}` : "",
      plateMissing.length ? `登记未用：${plateMissing.join(", ")}` : "",
    ]
      .filter(Boolean)
      .join("；"),
  );
}

// ── 半透黑遮罩载体登记（F-W1-07）────────────────────────────
// bg-black/<alpha> 是半透明载体：其有效底由 alpha 与身后内容共同
// 决定，alpha 无声漂移会改写遮罩强度与其上文字的对比下限。Modal
// 遮罩分档（blur 档 bg-black/40＋blur、opaque 档 bg-black/70）与
// config 壁纸预览标签芯片（白字坐 bg-black/70）在首轮审计终局
// 登记缺位、仓内门禁对 bg-black 零命中；同族另有三处同形载体
// （Dialog 遮罩、admin 遮罩、登录页验证码遮罩），按机制全集一并
// 入册，不只登记被点名的两处。登记集按 file:line＋token 双向相等
// 断言（多一处未登记载体 / 少一处登记载体都红；新增载体先登记再
// 合入），与 tray-plate 普查同口径：只计字符串字面量内的 token，
// 注释内提及不计数。另钉 config 芯片白字对比下限：bg-black/70 覆
// 纯白底的合成底为 #4c4c4c，白字对比 8.52（钉值 ±0.1）；alpha 降
// 至 0.3 即跌破 4.5（实测 2.11），故 alpha 值本身须钉住。
const BLACK_SCRIM_REGISTERED = [
  { loc: "src/shadcn-bridge/heroui/modal.tsx:56", token: "bg-black/40", note: "Modal blur 档遮罩" },
  { loc: "src/shadcn-bridge/heroui/modal.tsx:59", token: "bg-black/70", note: "Modal opaque 档遮罩" },
  { loc: "src/pages/config.tsx:1219", token: "bg-black/70", note: "config 壁纸预览标签芯片（白字）" },
  { loc: "src/components/ui/dialog.tsx:39", token: "bg-black/40", note: "Dialog 遮罩" },
  { loc: "src/layouts/admin.tsx:428", token: "bg-black/40", note: "admin 遮罩" },
  { loc: "src/pages/index.tsx:320", token: "bg-black/60", note: "登录页验证码遮罩" },
];
{
  const foundScrims = [];
  const scrimWalk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        scrimWalk(path);
      } else if (/\.tsx?$/.test(entry.name)) {
        const text = readFileSync(path, "utf8");
        const rel = path.slice(join(here, "..").length + 1);
        for (const m of text.matchAll(/"([^"\n]*)"|'([^'\n]*)'|`([^`]*)`/g)) {
          const body = m[1] ?? m[2] ?? m[3] ?? "";
          for (const t of body.matchAll(/bg-black\/\d+/g)) {
            const line = text.slice(0, m.index).split("\n").length;
            foundScrims.push(`${rel}:${line} ${t[0]}`);
          }
        }
      }
    }
  };
  scrimWalk(join(here, "..", "src"));
  const registeredKeys = BLACK_SCRIM_REGISTERED.map((r) => `${r.loc} ${r.token}`);
  const scrimExtra = foundScrims.filter((f) => !registeredKeys.includes(f));
  const scrimMissing = registeredKeys.filter((f) => !foundScrims.includes(f));
  check(
    `半透黑遮罩载体普查闭合（实扫 ${foundScrims.length} 处、登记 ${registeredKeys.length} 处）`,
    scrimExtra.length === 0 && scrimMissing.length === 0,
    [
      scrimExtra.length ? `未登记：${scrimExtra.join(", ")}` : "",
      scrimMissing.length ? `登记未用：${scrimMissing.join(", ")}` : "",
    ]
      .filter(Boolean)
      .join("；"),
  );
  // 黑 scrim 合成底：alpha 份黑覆在底色上（与玻璃网格的白 overlay
  // compositeOver 相反方向，勿混用）。
  const blackOver = (alpha, bg) => [0, 1, 2].map((i) => (1 - alpha) * bg[i]);
  const chipWorst = contrastRgb([255, 255, 255], blackOver(0.7, [255, 255, 255]));
  check(
    `config 预览芯片白字对比下限（bg-black/70 覆纯白底 = ${chipWorst.toFixed(2)}、底线 4.5）`,
    chipWorst >= 4.5 && Math.abs(chipWorst - 8.52) <= 0.1,
    `实测 ${chipWorst.toFixed(2)}`,
  );
}

// ── 玻璃语境语义文字（R2-S1-01）────────────────────────────
// 玻璃恢复后语义色阶梯档（-700 为主）直坐玻璃的最差格只有
// 1.76–2.77：已登记边界只量化过中性 default-700（3.67），而语义
// -700 亮度更高、且含表单错误行/到期徽标等主信号文字。整改不逐
// 点打补丁、也不动色阶本体（实色面 25 对与实色填充的身份靠它）：
// globals.css 在 @layer utilities 设玻璃语境覆盖块——玻璃载体
// （.glass-card/.glass-surface/.glass-inset/手风琴玻璃项）内的语
// 义文字类一律改用每族深档 --glass-text-<family> 渲染（背景/边框
// 不受影响）。本段断言四件：① 12 个深档 token 存在且为 hex；
// ② 每族深档在玻璃 36 格全 >= 4.5、卡片中位停点×纯黑 >= 7、最
// 差格钉值 ±0.1；③ /10 tint 载体格（tint@10% 叠卡片最低停点×
// 纯黑）>= 4.5；④ 普查闭合——机械扫 src 全部语义文字类字面量，
// 每个（变体，色族，档位）必须落在覆盖集内（变体仅裸/hover/
// focus，色族恰 17 族，中性 default 为登记边界族除外），且覆盖
// 块原文含每族的类与深档引用；普查为空或出现集外成员即红
// （fail-closed：新色族/新变体先扩覆盖块与本段清单再合入）。
const GLASS_TEXT_TOKEN_FAMS = [
  "primary", "secondary", "success", "warning", "danger",
  "rose", "orange", "sky", "emerald", "blue", "purple", "teal",
];
const GLASS_TEXT_ALIAS = {
  indigo: "primary",
  violet: "secondary",
  green: "success",
  amber: "warning",
  red: "danger",
};
const GLASS_TEXT_WORST_PINS = {
  primary: 5.95, secondary: 5.9, success: 5.93, warning: 5.91,
  danger: 5.91, rose: 5.91, orange: 5.92, sky: 5.94,
  emerald: 5.92, blue: 5.95, purple: 5.91, teal: 5.93,
};

const glassTextRegion = (() => {
  const begin = css.indexOf("GLASS-TEXT-OVERRIDES:BEGIN");
  const end = css.indexOf("GLASS-TEXT-OVERRIDES:END");

  return begin >= 0 && end > begin ? css.slice(begin, end) : "";
})();

check(
  "玻璃语境覆盖块在册（GLASS-TEXT-OVERRIDES 标记对完整）",
  glassTextRegion.length > 0,
  "globals.css 未找到标记对",
);

const glassTextHex = {};
for (const fam of GLASS_TEXT_TOKEN_FAMS) {
  const hex = tokens[`--glass-text-${fam}`];

  glassTextHex[fam] = hex ?? null;
  check(
    `--glass-text-${fam} 已定义为 #rrggbb`,
    typeof hex === "string",
    "token 缺失或非 hex 生效值",
  );
}

if (GLASS_TIERS.every((t) => t.stops.length === 3)) {
  for (const fam of GLASS_TEXT_TOKEN_FAMS) {
    const hex = glassTextHex[fam];

    if (!hex) continue;
    const rgb = hexToRgb(hex);
    let min = Infinity;
    let worstKey = "";

    for (const tier of GLASS_TIERS) {
      for (const [bgName, bg] of GLASS_BACKDROPS) {
        tier.stops.forEach((stop, si) => {
          const carrier =
            tier.name.startsWith("内嵌")
              ? compositeOver(GLASS_TIERS[0].stops[2], bg)
              : bg;
          const ratio = contrastRgb(rgb, compositeOver(stop, carrier));

          if (ratio < min) {
            min = ratio;
            worstKey = `${tier.name}|${bgName}|${si}`;
          }
        });
      }
    }
    const midBlack = contrastRgb(
      rgb,
      compositeOver(GLASS_TIERS[0].stops[1], [0, 0, 0]),
    );
    const worstPin = GLASS_TEXT_WORST_PINS[fam];

    check(
      `玻璃语境深档：--glass-text-${fam} 全 36 格最低 = ${min.toFixed(2)}（${worstKey}，底线 4.5）`,
      min >= 4.5,
      `实测最低 ${min.toFixed(2)}`,
    );
    check(
      `玻璃语境深档：--glass-text-${fam} 最差格钉值 = ${min.toFixed(2)}（钉值 ${worstPin}）、卡片中位×纯黑 = ${midBlack.toFixed(2)}（目标 7）`,
      Math.abs(min - worstPin) <= 0.1 && midBlack >= 7,
      `实测最差 ${min.toFixed(2)}、中位 ${midBlack.toFixed(2)}`,
    );
  }
  // /10 tint 载体格：徽标/列表的 bg-<family>-500/10 叠在卡片最低
  // 停点×纯黑之上，深档文字须仍 >= 4.5（别名族与本族同格）。
  const floorOverBlack = compositeOver(GLASS_TIERS[0].stops[2], [0, 0, 0]);
  const TINT_CELLS = [
    ["danger", "#ef4444"],
    ["warning", "#f59e0b"],
    ["primary", "#6366f1"],
    ["success", "#22c55e"],
    ["orange", "#f97316"],
    ["blue", "#3b82f6"],
    ["teal", "#14b8a6"],
  ];

  for (const [fam, tintHex] of TINT_CELLS) {
    const hex = glassTextHex[fam];

    if (!hex) continue;
    const tintRgb = hexToRgb(tintHex);
    const carrier = tintRgb.map((v, i) => 0.1 * v + 0.9 * floorOverBlack[i]);
    const ratio = contrastRgb(hexToRgb(hex), carrier);

    check(
      `玻璃语境 tint 格：--glass-text-${fam} on ${fam}-500/10 叠卡片最低停点×纯黑 = ${ratio.toFixed(2)}（底线 4.5）`,
      ratio >= 4.5,
      `实测 ${ratio.toFixed(2)}`,
    );
  }
} else {
  check("玻璃语境深档网格可复算（三级停点齐备）", false, "停点缺失");
}

// 覆盖块原文闭合：每族（含别名）的裸/hover/focus 类与深档引用在册。
for (const fam of [
  ...GLASS_TEXT_TOKEN_FAMS,
  ...Object.keys(GLASS_TEXT_ALIAS),
]) {
  const tokenFam = GLASS_TEXT_TOKEN_FAMS.includes(fam)
    ? fam
    : GLASS_TEXT_ALIAS[fam];

  check(
    `玻璃语境覆盖块含 ${fam} 族（裸/hover/focus 类＋--glass-text-${tokenFam} 引用）`,
    glassTextRegion.includes(`.text-${fam}-700`) &&
      glassTextRegion.includes(`.hover\\:text-${fam}-700`) &&
      glassTextRegion.includes(`.focus\\:text-${fam}-700`) &&
      glassTextRegion.includes(`var(--glass-text-${tokenFam})`),
    "覆盖块缺该族的类或引用",
  );
}

// 普查闭合：src 全部语义文字类字面量 ⊆ 覆盖集。
// R3-C6-02 扩面：旧正则只认 [5-9]00 无后缀——100–400 更浅档
// 完全看不见、/60 等透明后缀按不透明值计数，均恒绿放行（本轮
// 实证现行零实例、纯潜伏）。现扩至 [1-9]00 全档并捕获透明后缀：
// 浅档（-500 及以下）无玻璃覆盖块、直坐玻璃只会更差，点名红；
// 透明后缀只在 -700 玻璃渲染面（深档 token）按 alpha 合成复算
// ——以深档色在玻璃 36 格载体上逐格合成后取最低格，>= 4.5 才
// 算覆盖，其余档位的透明后缀不在任何已建模面内、点名红。
// R4-C6-02：方括号 alpha（/[0.12]）此前永不捕获——正则尾部的
// \b 位于 alpha 组之后，而 ] 是非单词字符、其后（引号/空格/类
// 名分隔）不构成单词边界，整组回退为不透明命中、按 -700 放行，
// parseFloat 解析分支成死代码。现把 \b 移到档位数字之后、alpha
// 组随后，数字与方括号两形态同等可达。
// R4-C6-01：任意值形态 text-[#hex] / text-[rgb(...)] /
// text-[hsl(...)] / text-[var(--x)] 此前完全不入普查（字面量正
// 则只认 族-档 形态）——另行收集（censusArbitrary），字面色即渲
// 染色、无语义覆盖块可借，按实值在玻璃格合成复算，见下方任意
// 值段。
// RR4-C6-02：同族四形态补全——① 主普查档位组原为 ([1-9]00)，
// -50 档（主题六族真定义、--warning-50 近白）不入册、text-warning
// -50 探针计数纹丝不动全绿，现档位组扩为 (50|[1-9]00)，-50 与其
// 他浅档同按「浅档无覆盖块」点名；② 任意值准入原只认 #/rgb/hsl
// /var 开头，Tailwind v4 的 oklch() 不入册，现准入 oklch( 并在
// parseCssColorValue 按 OKLCH→sRGB 实值求解；③ 任意值的 /alpha
// 后缀（text-[#0f172a]/10）原在方括号外不被捕获、按不透明求值
// 放行，现捕获后缀并与任意值段同法做 alpha 合成（值内 alpha 与
// 后缀 alpha 相乘）；④ Tailwind v4 变量简写 text-(--x)（等价
// text-[var(--x)]）三条普查正则均不匹配，现专形收集、按 var 实
// 值求值。
const SEMANTIC_TEXT_FAMS = new Set([
  ...GLASS_TEXT_TOKEN_FAMS,
  ...Object.keys(GLASS_TEXT_ALIAS),
]);
const censusHits = [];
const censusArbitrary = [];
const censusWalk = (dir) => {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);

    if (entry.isDirectory()) {
      censusWalk(path);
    } else if (/\.tsx?$/.test(entry.name)) {
      const text = readFileSync(path, "utf8");
      const re =
        /(?:(hover|focus|focus-visible|group-hover|dark):)?text-([a-z]+)-(50|[1-9]00)\b(\/(?:\d+|\[[\d.]+\]))?/g;
      let m;

      while ((m = re.exec(text)) !== null) {
        const alpha =
          m[4] === undefined
            ? null
            : m[4].startsWith("/[")
              ? parseFloat(m[4].slice(2, -1))
              : parseInt(m[4].slice(1), 10) / 100;

        censusHits.push({
          variant: m[1] ?? "",
          fam: m[2],
          shade: m[3],
          alpha,
          path,
        });
      }
      // 裸语义文字类（无档位，如 text-warning）全族扫描（R4-S1-01）：
      // 此前只硬编码探 text-warning 一族且 base 档在覆盖判定里无条件
      // 放行，裸类遂逃逸全门——裸 token 不在 globals.css 玻璃覆盖块
      // 的带档位枚举内，任何载体上都低于 4.5（见 censusCoverReason
      // 的 base 判罚）。按 SEMANTIC_TEXT_FAMS 全集机械枚举，差集 ∅。
      // （集成注：C6-01 原带的 text-warning 单族硬编码探针已被本全族
      // 扫描完全覆盖，合并时去除以免同格重复入册。）
      const bareRe = new RegExp(
        `(?:(hover|focus|focus-visible|group-hover|dark):)?text-(${[...SEMANTIC_TEXT_FAMS].join("|")})(?![-\\w])`,
        "g",
      );
      let bm;

      while ((bm = bareRe.exec(text)) !== null) {
        censusHits.push({ variant: bm[1] ?? "", fam: bm[2], shade: "base", alpha: null, path });
      }
      // 任意值文字色（R4-C6-01）：只收颜色形态——以 # / rgb( /
      // hsl( / oklch( / var( 开头、color: 类型提示、或纯颜色关键
      // 词；长度形态（text-[14px]、text-[length:...]）是字号、不
      // 入册。RR4-C6-02③：方括号后的 /alpha 后缀一并捕获
      // （text-[#0f172a]/10 的 /10 在括号外、旧捕获不可见，按不
      // 透明放行）——后缀 alpha 与值内 alpha 在求值时相乘合成。
      const suffixAlphaOf = (s) =>
        s === undefined
          ? null
          : s.startsWith("/[")
            ? parseFloat(s.slice(2, -1))
            : parseInt(s.slice(1), 10) / 100;
      const arbRe =
        /(?:(hover|focus|focus-visible|group-hover|dark):)?text-\[([^\]]+)\](\/(?:\d+|\[[\d.]+\]))?/g;
      let am;

      while ((am = arbRe.exec(text)) !== null) {
        const raw = am[2].trim();
        const value = raw.startsWith("color:") ? raw.slice(6).trim() : raw;

        if (
          /^(#|rgba?\(|hsla?\(|oklch\(|var\()/.test(value) ||
          /^[a-zA-Z]+$/.test(value)
        ) {
          censusArbitrary.push({
            variant: am[1] ?? "",
            raw,
            value,
            suffix: am[3] ?? "",
            suffixAlpha: suffixAlphaOf(am[3]),
            path,
          });
        }
      }
      // Tailwind v4 变量简写（RR4-C6-02④）：text-(--x) 等价于
      // text-[var(--x)]——按 var 形态入册、求值同任意值段；后缀
      // /alpha 同样捕获合成。
      const shortRe =
        /(?:(hover|focus|focus-visible|group-hover|dark):)?text-\((--[\w-]+)\)(\/(?:\d+|\[[\d.]+\]))?/g;
      let sm;

      while ((sm = shortRe.exec(text)) !== null) {
        censusArbitrary.push({
          variant: sm[1] ?? "",
          raw: `(${sm[2]})`,
          value: `var(${sm[2]})`,
          suffix: sm[3] ?? "",
          suffixAlpha: suffixAlphaOf(sm[3]),
          path,
        });
      }
    }
  }
};

censusWalk(join(here, "..", "src"));
const censusSemantic = censusHits.filter((h) => h.fam !== "default");

// 玻璃 36 格载体面（与上文网格同模型）：透明文字以 alpha 在
// 载体面上合成，取全格最低对比度判定。
const glassSurfaces = [];
if (GLASS_TIERS.every((t) => t.stops.length === 3)) {
  for (const tier of GLASS_TIERS) {
    for (const [, bg] of GLASS_BACKDROPS) {
      tier.stops.forEach((stop) => {
        const carrier = tier.name.startsWith("内嵌")
          ? compositeOver(GLASS_TIERS[0].stops[2], bg)
          : bg;

        glassSurfaces.push(compositeOver(stop, carrier));
      });
    }
  }
}
const alphaGlassMin = (hex, alpha) => {
  const rgb = hexToRgb(hex);
  let min = Infinity;

  for (const s of glassSurfaces) {
    const eff = rgb.map((v, i) => alpha * v + (1 - alpha) * s[i]);

    min = Math.min(min, contrastRgb(eff, s));
  }

  return min;
};

// ── 任意值文字色求值（R4-C6-01）──────────────────────────────
// 任意值的字面色就是渲染色（无玻璃覆盖块改写），按 CSS 实值解
// 析：#hex（3/4/6/8 位）、rgb()/rgba()、hsl()/hsla()（逗号或空
// 格分隔、/alpha 后缀）、oklch()（RR4-C6-02②，OKLCH→sRGB 换
// 算）、var(--x)（经 :root 已解析 token 求值，带回退则用回
// 退）、基础颜色关键词。解析后以其 alpha 在玻璃
// 36 格载体上合成取最低格（与透明后缀同模型）。无法静态求值
// （未定义且无回退的 var、currentColor 等）即点名红——声称覆
// 盖的形态不许静默放行。
const CSS_COLOR_KEYWORDS = {
  black: { rgb: [0, 0, 0], alpha: 1 },
  white: { rgb: [255, 255, 255], alpha: 1 },
  red: { rgb: [255, 0, 0], alpha: 1 },
  green: { rgb: [0, 128, 0], alpha: 1 },
  blue: { rgb: [0, 0, 255], alpha: 1 },
  yellow: { rgb: [255, 255, 0], alpha: 1 },
  gray: { rgb: [128, 128, 128], alpha: 1 },
  grey: { rgb: [128, 128, 128], alpha: 1 },
  transparent: { rgb: [0, 0, 0], alpha: 0 },
};
const hslToRgb = (h, s, l) => {
  const sn = s / 100;
  const ln = l / 100;
  const k = (n) => (n + h / 30) % 12;
  const a = sn * Math.min(ln, 1 - ln);
  const f = (n) =>
    ln - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));

  return [f(0), f(8), f(4)].map((v) => v * 255);
};
// OKLCH → sRGB（RR4-C6-02②）：经 OKLab 与线性 sRGB 矩阵换算后
// 做 gamma 编码并按渲染口径钳到 [0,255]（超色域分量浏览器同样
// 钳制/降级，本门按钳后实值算对比度）。
const oklchToRgb = (L, C, Hdeg) => {
  const hRad = (Hdeg * Math.PI) / 180;
  const a = C * Math.cos(hRad);
  const b = C * Math.sin(hRad);
  const l_ = L + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = L - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = L - 0.0894841775 * a - 1.291485548 * b;
  const l = l_ ** 3;
  const m = m_ ** 3;
  const s = s_ ** 3;
  const lin = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];

  return lin.map((v) => {
    const g = v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055;

    return Math.min(255, Math.max(0, g * 255));
  });
};
const parseAlphaPart = (aRaw) => {
  if (aRaw === null || aRaw === undefined) return 1;
  const t = String(aRaw).trim();
  const a = t.endsWith("%") ? parseFloat(t) / 100 : parseFloat(t);

  return Number.isNaN(a) ? null : a;
};
const parseCssColorValue = (rawValue, depth = 0) => {
  if (depth > 4) return null;
  const v = String(rawValue).replace(/_/g, " ").trim();
  let m = v.match(
    /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/,
  );

  if (m) {
    const h = m[1];

    if (h.length <= 4) {
      return {
        rgb: [0, 1, 2].map((i) => parseInt(h[i] + h[i], 16)),
        alpha: h.length === 4 ? parseInt(h[3] + h[3], 16) / 255 : 1,
      };
    }

    return {
      rgb: [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)),
      alpha: h.length === 8 ? parseInt(h.slice(6, 8), 16) / 255 : 1,
    };
  }
  m = v.match(/^rgba?\(([\s\S]+)\)$/);
  if (m) {
    const [body, slashAlpha] = m[1].split("/");
    const comps = body.split(/[,\s]+/).filter(Boolean);

    if (comps.length < 3) return null;
    const chan = (p) =>
      p.endsWith("%") ? (parseFloat(p) / 100) * 255 : parseFloat(p);
    const rgb = comps.slice(0, 3).map(chan);

    if (rgb.some(Number.isNaN)) return null;
    const alpha = parseAlphaPart(
      slashAlpha !== undefined ? slashAlpha : comps.length > 3 ? comps[3] : null,
    );

    return alpha === null ? null : { rgb, alpha };
  }
  m = v.match(/^hsla?\(([\s\S]+)\)$/);
  if (m) {
    const [body, slashAlpha] = m[1].split("/");
    const parts = body.split(/[,\s]+/).filter(Boolean);

    if (parts.length < 3) return null;
    const [hDeg, sPct, lPct] = parts.map((p) => parseFloat(p));

    if ([hDeg, sPct, lPct].some(Number.isNaN)) return null;
    const alpha = parseAlphaPart(
      slashAlpha !== undefined ? slashAlpha : parts.length > 3 ? parts[3] : null,
    );

    return alpha === null
      ? null
      : {
          rgb: hslToRgb(((hDeg % 360) + 360) % 360, sPct, lPct),
          alpha,
        };
  }
  m = v.match(/^oklch\(([\s\S]+)\)$/);
  if (m) {
    const [body, slashAlpha] = m[1].split("/");
    const parts = body.split(/[,\s]+/).filter(Boolean);

    if (parts.length < 3) return null;
    // L：百分数按 /100、无单位数即 0..1 本值；C：百分数按 CSS
    // 口径 100% = 0.4；H：deg 本值、turn ×360、rad 换算弧度制。
    const L = parts[0].endsWith("%")
      ? parseFloat(parts[0]) / 100
      : parseFloat(parts[0]);
    const C = parts[1].endsWith("%")
      ? (parseFloat(parts[1]) / 100) * 0.4
      : parseFloat(parts[1]);
    let H = parseFloat(parts[2]);

    if (/turn$/i.test(parts[2])) H = parseFloat(parts[2]) * 360;
    else if (/rad$/i.test(parts[2])) H = (parseFloat(parts[2]) * 180) / Math.PI;
    if ([L, C, H].some(Number.isNaN)) return null;
    const alpha = parseAlphaPart(
      slashAlpha !== undefined ? slashAlpha : parts.length > 3 ? parts[3] : null,
    );

    return alpha === null ? null : { rgb: oklchToRgb(L, C, H), alpha };
  }
  m = v.match(/^var\(\s*(--[\w-]+)\s*(?:,([\s\S]*))?\)$/);
  if (m) {
    const resolved = resolvedTokens[m[1]];

    if (resolved !== undefined) return parseCssColorValue(resolved, depth + 1);
    if (m[2] !== undefined) return parseCssColorValue(m[2], depth + 1);

    return null;
  }

  return CSS_COLOR_KEYWORDS[v.toLowerCase()] ?? null;
};
const arbitraryCoverReason = (h) => {
  if (!["", "hover", "focus"].includes(h.variant)) return "集外变体";
  const parsed = parseCssColorValue(h.value);

  if (!parsed) return "任意值无法静态求值";
  if (glassSurfaces.length === 0) return "玻璃格缺失";
  // RR4-C6-02③：/alpha 后缀与值内 alpha 相乘才是渲染 alpha——
  // text-[#0f172a]/10 的实渲染是 10% 合成，旧求值按不透明全色
  // 判过（浅底上实为 ≈1.x）。
  const effAlpha = parsed.alpha * (h.suffixAlpha ?? 1);
  let min = Infinity;

  for (const s of glassSurfaces) {
    const eff = parsed.rgb.map((c, i) => effAlpha * c + (1 - effAlpha) * s[i]);

    min = Math.min(min, contrastRgb(eff, s));
  }

  return min >= 4.5 ? null : `任意值实值合成最低格 ${min.toFixed(2)} < 4.5`;
};
const arbitraryUncovered = censusArbitrary
  .map((h) => ({ h, reason: arbitraryCoverReason(h) }))
  .filter((x) => x.reason !== null);

// 覆盖判定：返回 null = 已覆盖，否则为点名原因。
const censusCoverReason = (h) => {
  if (!SEMANTIC_TEXT_FAMS.has(h.fam)) return "集外色族";
  if (!["", "hover", "focus"].includes(h.variant)) return "集外变体";
  // 裸类（无档位）恒不覆盖（R4-S1-01）：globals.css 玻璃语境覆盖块
  // 只枚举带档位类 .text-<fam>-500..900，裸类保留原始 token 色
  // （如 --warning #d97706），在玻璃三停点×三底的每一格都 < 4.5、
  // 在不透明面上也只有约 3.3——写法必须带档位，无登记豁免。
  if (h.shade === "base") return "裸语义文字类（无档位）：不在玻璃覆盖块枚举内、任何载体均不达 4.5，须带档位";
  if (Number(h.shade) <= 500) return "浅档无玻璃覆盖块（-500 及以下）";
  if (h.alpha !== null) {
    if (h.shade !== "700") return "透明后缀仅 -700 玻璃渲染面已建模";
    const tokenFam = GLASS_TEXT_TOKEN_FAMS.includes(h.fam)
      ? h.fam
      : GLASS_TEXT_ALIAS[h.fam];
    const hex = glassTextHex[tokenFam];

    if (!hex || glassSurfaces.length === 0) return "深档 token 或玻璃格缺失";
    const min = alphaGlassMin(hex, h.alpha);

    return min >= 4.5
      ? null
      : `透明后缀合成最低格 ${min.toFixed(2)} < 4.5`;
  }

  return null;
};
const censusUncovered = censusSemantic
  .map((h) => ({ h, reason: censusCoverReason(h) }))
  .filter((x) => x.reason !== null);
const censusShallow = censusSemantic.filter(
  (h) => h.shade !== "base" && Number(h.shade) <= 500,
);
const censusAlpha = censusSemantic.filter((h) => h.alpha !== null);

check(
  `语义文字类普查非空（实扫 ${censusSemantic.length} 处，含浅档 ${censusShallow.length} 处、透明后缀 ${censusAlpha.length} 处，中性 default 登记边界除外）`,
  censusSemantic.length >= 150,
  `仅扫到 ${censusSemantic.length} 处，扫描面疑似失效`,
);
check(
  "语义文字类普查差集 ∅（集外色族/变体、浅档、裸类无档位、未建模或合成不达标的透明后缀即红）",
  censusUncovered.length === 0,
  censusUncovered
    .slice(0, 5)
    .map(
      ({ h, reason }) =>
        `${h.variant ? `${h.variant}:` : ""}text-${h.fam}-${h.shade}${h.alpha !== null ? `/${Math.round(h.alpha * 100)}` : ""}（${reason}）@ ${h.path}`,
    )
    .join("；"),
);
check(
  `任意值文字色普查差集 ∅（实扫 ${censusArbitrary.length} 处：#/rgb/hsl/oklch/var/关键词形态与 v4 变量简写按实值在玻璃格合成复算、/alpha 后缀相乘合成，< 4.5 或无法静态求值即红）`,
  arbitraryUncovered.length === 0,
  arbitraryUncovered
    .slice(0, 5)
    .map(
      ({ h, reason }) =>
        `${h.variant ? `${h.variant}:` : ""}text-${h.raw.startsWith("(") ? h.raw : `[${h.raw}]`}${h.suffix ?? ""}（${reason}）@ ${h.path}`,
    )
    .join("；"),
);

// ── 中性 default 玻璃面登记（R3-S1-01）────────────────────
// R2 登记边界（「中性次级信息」）经 R3 复核时用量已漂移：37 处
// -700 中含表单标签与整段说明文字，最差格 3.67、卡片中位 4.41
// 均低于 4.5。本轮按内容角色收窄：承载主要内容者 22 处改 -800
// （表单标签/说明段/正文行/名称/标题/数据值/透明底占位符/
// 桥 Link 默认色/切换钮 hover 态等），留置 -700 仅三类面——
// ① 不透明钉面（content1 占位符 PAIRS 已钉 10.44、bg-tray
// 徽标/芯片、bg-default-300 实色徽标）；② 玻璃内嵌面（最差格
// 5.15 >= 4.5，下方钉值断言）；③ 枚举的次级示意（应用名预
// 览小字）。本段断言：① -700 卡片直坐最差格只作边界记录钉值
// （token 漂移即点名，不作通过断言）；② 修复档 -800 同格
// >= 4.5 且钉值；③ -700 内嵌全底最低格 >= 4.5 且钉值（留置
// 面 ② 的机械依据）；④ src 普查逐文件钉数——留置文件与计数
// 之外任何 -700 出现即红（把已改标签翻回 -700 必被抓），
// -500/-600 用量恒为 0（纪律面）。
const DEFAULT_700_FILE_PINS = {
  "components/ui/input.tsx": 1, // bg-content1 占位符（PAIRS 钉 10.44）
  "components/ui/textarea.tsx": 1, // 同上
  "pages/config.tsx": 1, // 应用名预览小字（次级示意）
  // pages/dashboard.tsx 与 pages/subscription/controls.tsx 两条已销：
  // 前者的唯一 -700 在已删的到期提醒卡内（AP-10 死代码删除），后者
  // 在已删的 EditorTabs 徽标内（C3-01），实扫归零、登记同步撤销。
  "pages/dashboard/admin-overview.tsx": 1, // bg-tray 芯片
  "pages/forward.tsx": 5, // glass-inset 地址钮 ×4＋inset 页眉组标题 ×1
  "pages/node/node-row.tsx": 4, // bg-tray 芯片 hover ×2＋glass-inset 信息盒 ×2
};
{
  const d700Hex = tokens["--default-700"];
  const d800Hex = tokens["--default-800"];

  if (d700Hex && d800Hex && GLASS_TIERS.every((t) => t.stops.length === 3)) {
    const cardFloorOverBlack = compositeOver(GLASS_TIERS[0].stops[2], [0, 0, 0]);
    const d700Worst = contrastRgb(hexToRgb(d700Hex), cardFloorOverBlack);

    check(
      `中性 default-700 玻璃边界格记录：卡片最低停点×纯黑 = ${d700Worst.toFixed(2)}（登记值 3.67，仅记录 token 漂移、非通过断言）`,
      Math.abs(d700Worst - 3.67) <= 0.1,
      `实测 ${d700Worst.toFixed(2)}`,
    );
    const d800Worst = contrastRgb(hexToRgb(d800Hex), cardFloorOverBlack);

    check(
      `中性 default-800 玻璃最差格：卡片最低停点×纯黑 = ${d800Worst.toFixed(2)}（钉值 5.23、底线 4.5）`,
      d800Worst >= 4.5 && Math.abs(d800Worst - 5.23) <= 0.1,
      `实测 ${d800Worst.toFixed(2)}`,
    );
    let d700InsetMin = Infinity;

    for (const [, bg] of GLASS_BACKDROPS) {
      const carrier = compositeOver(GLASS_TIERS[0].stops[2], bg);

      for (const stop of GLASS_TIERS[2].stops) {
        d700InsetMin = Math.min(
          d700InsetMin,
          contrastRgb(hexToRgb(d700Hex), compositeOver(stop, carrier)),
        );
      }
    }
    check(
      `中性 default-700 内嵌安全格：全底最低 = ${d700InsetMin.toFixed(2)}（钉值 5.15、底线 4.5）`,
      d700InsetMin >= 4.5 && Math.abs(d700InsetMin - 5.15) <= 0.1,
      `实测 ${d700InsetMin.toFixed(2)}`,
    );
  } else {
    check("中性 default 玻璃面登记可复算（--default-700/800 与三级停点齐备）", false, "token 或停点缺失");
  }

  const d700Counts = new Map();
  let dShallowCount = 0;
  const dWalk = (dir, rel) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      const r = rel ? `${rel}/${entry.name}` : entry.name;

      if (entry.isDirectory()) {
        dWalk(path, r);
      } else if (/\.tsx?$/.test(entry.name)) {
        const text = readFileSync(path, "utf8");
        const n = (text.match(/text-default-700/g) ?? []).length;

        if (n > 0) d700Counts.set(r, n);
        dShallowCount += (text.match(/text-default-[56]00/g) ?? []).length;
      }
    }
  };

  dWalk(join(here, "..", "src"), "");
  const d700Drift = [];

  for (const [file, n] of d700Counts) {
    if (DEFAULT_700_FILE_PINS[file] !== n) {
      d700Drift.push(`${file} 实扫 ${n} 处（钉数 ${DEFAULT_700_FILE_PINS[file] ?? "无登记"}）`);
    }
  }
  for (const file of Object.keys(DEFAULT_700_FILE_PINS)) {
    if (!d700Counts.has(file)) d700Drift.push(`${file} 实扫 0 处（钉数 ${DEFAULT_700_FILE_PINS[file]}）`);
  }
  const d700Total = [...d700Counts.values()].reduce((a, b) => a + b, 0);

  check(
    `中性 default-700 留置普查（实扫 ${d700Total} 处、逐文件钉数对表）`,
    d700Drift.length === 0,
    d700Drift.slice(0, 5).join("；") || "计数漂移",
  );
  check(
    `中性 default-500/-600 用量恒为 0（实扫 ${dShallowCount} 处）`,
    dShallowCount === 0,
    `实扫 ${dShallowCount} 处——浅档中性文字不在纪律面内`,
  );
}

// ── 图表系列色登记（R3-S1-02）────────────────────────────
// 图表绘图区无自有底色、系列折线直坐玻璃卡片，此前无任何对比
// 度登记。口径：按 WCAG 1.4.11 非文本 3:1 对「默认底常态格」
// （无壁纸、卡片最低停点叠 --background）断言并钉值；纯黑壁
// 纸界格全系列低于 3:1，只钉登记值作漂移记录——壁纸为 opt-in
// 且 blur 匀化使界格为保守下界，系列靠色相＋位置区分、图例
// 文字与 glass tooltip 提供数值冗余（R3-S1-02 缓解面）。
// R3 修复：常态格不达标的五色由 -500 提至 -600 档（emerald
// 2.49→#059669、warning 2.11→#d97706、cyan 2.39→#0891b2、
// lime 1.94→#65a30d、orange 2.76→#ea580c），色相不变、系列间
// 可区分。本段普查闭合：src 全树走查收集的图表系列色须恰
// 等于登记集（多一个未登记色、少一个登记色都红）。
// [hex, 默认底常态格钉值, 纯黑界格登记值]
// F-W1-05：手写图表文件清单已删——普查改为 src 全树目录走查
// （见下方 foundHexes 段），新图表文件带未登记色即红，不再
// 依赖清单记得加文件。
const CHART_SERIES = [
  ["#007aff", 3.95, 1.41],
  ["#6366f1", 4.39, 1.57],
  ["#059669", 3.7, 1.32],
  ["#d97706", 3.13, 1.12],
  ["#8b5cf6", 4.16, 1.49],
  ["#ef4444", 3.7, 1.32],
  ["#0891b2", 3.62, 1.29],
  ["#ec4899", 3.47, 1.24],
  ["#65a30d", 3.04, 1.08],
  ["#ea580c", 3.5, 1.25],
  ["#475569", 7.45, 2.66],
  ["#3b82f6", 3.62, 1.29],
];
{
  const bgHex = tokens["--background"];

  if (bgHex && GLASS_TIERS.every((t) => t.stops.length === 3)) {
    const floorStop = GLASS_TIERS[0].stops[2];
    const defaultCarrier = compositeOver(floorStop, hexToRgb(bgHex));
    const blackCarrier = compositeOver(floorStop, [0, 0, 0]);

    for (const [hex, pinDefault, pinBlack] of CHART_SERIES) {
      const rgb = hexToRgb(hex);
      const rDefault = contrastRgb(rgb, defaultCarrier);
      const rBlack = contrastRgb(rgb, blackCarrier);

      check(
        `图表系列色 ${hex} 默认底常态格 = ${rDefault.toFixed(2)}（钉值 ${pinDefault}、非文本底线 3）`,
        rDefault >= 3 && Math.abs(rDefault - pinDefault) <= 0.1,
        `实测 ${rDefault.toFixed(2)}`,
      );
      check(
        `图表系列色 ${hex} 纯黑壁纸界格记录 = ${rBlack.toFixed(2)}（登记值 ${pinBlack}，靠图例/tooltip 冗余、非通过断言）`,
        Math.abs(rBlack - pinBlack) <= 0.1,
        `实测 ${rBlack.toFixed(2)}`,
      );
    }
  } else {
    check("图表系列色网格可复算（--background 与卡片停点齐备）", false, "token 或停点缺失");
  }

  // 图表系列色普查（F-W1-05）：目录走查 src 全树，只收集图表
  // 系列色句法形态——(a) stroke/fill="#hex" 或
  // stroke/fill={"#hex"}；(b) color: "#hex"（style: {...} 徽标
  // 样式行除外：dashboard 数据徽标 background/color 非图表色）；
  // (c) 整行仅一个 hex 字面量（数组元素，如 latency-trend-modal
  // 的 COLORS）；(d) fill 与 (a) 同形；(e) const IDENT="#hex"
  // 且同文件内有 stroke={IDENT} / fill={IDENT} /
  // borderColor: IDENT / background: IDENT 引用（traffic-stats
  // 的 DOWN/UP）。已知污染源已逐项排除：distro-icon 品牌色对象
  // （键值对行，非裸字面量行）、config.tsx "#ffffff" 比较/入参
  // （非裸字面量行）、use-dashboard-data.ts 徽标 style 行。
  const foundHexes = new Map(); // hex → [来源 file:line ...]
  const chartWalk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        chartWalk(path);
      } else if (/\.tsx?$/.test(entry.name)) {
        const text = readFileSync(path, "utf8");
        const rel = path.slice(join(here, "..").length + 1);
        const addHex = (hex, line) => {
          hex = hex.toLowerCase();
          if (!foundHexes.has(hex)) foundHexes.set(hex, []);
          foundHexes.get(hex).push(`${rel}:${line}`);
        };
        const lines = text.split("\n");
        lines.forEach((line, i) => {
          for (const m of line.matchAll(
            /(?:stroke|fill)=(?:"(#[0-9a-fA-F]{6})"|\{"(#[0-9a-fA-F]{6})"\})/g,
          )) {
            addHex(m[1] ?? m[2], i + 1);
          }
          if (!line.includes("style:")) {
            for (const m of line.matchAll(/color:\s*["'](#[0-9a-fA-F]{6})["']/g)) {
              addHex(m[1], i + 1);
            }
          }
          const bare = line.match(/^\s*["'`](#[0-9a-fA-F]{6})["'`],?\s*$/);
          if (bare) addHex(bare[1], i + 1);
        });
        for (const m of text.matchAll(
          /const\s+([A-Za-z_$][\w$]*)\s*=\s*["'](#[0-9a-fA-F]{6})["']/g,
        )) {
          const ref = new RegExp(
            `(?:stroke|fill)=\\{${m[1]}\\}|borderColor:\\s*${m[1]}\\b|background:\\s*${m[1]}\\b`,
          );
          if (ref.test(text)) {
            const line = text.slice(0, m.index).split("\n").length;
            addHex(m[2], line);
          }
        }
      }
    }
  };
  chartWalk(join(here, "..", "src"));
  const registeredHexes = new Set(CHART_SERIES.map(([hex]) => hex));
  const unregistered = [...foundHexes.keys()].filter((h) => !registeredHexes.has(h));
  const unused = [...registeredHexes].filter((h) => !foundHexes.has(h));

  check(
    `图表系列色普查闭合（实扫 ${foundHexes.size} 色、登记 ${registeredHexes.size} 色）`,
    unregistered.length === 0 && unused.length === 0,
    [
      unregistered.length
        ? `未登记：${unregistered.map((h) => `${h}（${foundHexes.get(h).slice(0, 3).join(", ")}）`).join(", ")}`
        : "",
      unused.length ? `登记未用：${unused.join(", ")}` : "",
    ]
      .filter(Boolean)
      .join("；"),
  );
}

// ── 桥 Button 载体（R2-S1-02）─────────────────────────────
// bordered 六色与 solid/flat 的 default 曾用半透明白 wash 作载体
// （white/25、white/30＋blur 3px）：叠卡片最低停点后四色标签
// < 4.5、直坐页面背景（PageHeader secondary 槽的 flat default）
// 全色 ~1.9，且从未入任何门。整改：bordered/flat/light 全色走不
// 透明托盘、solid default 走实色 default-800＋白字。本段断言：
// ① wash 退役签名（类串原文）在桥 button.tsx 中不存在；② 三张
// 托盘映射表（bordered/flat/light）逐表含 tray- 且不含 bg-white；
// ③ solid default 的白字对（#ffffff on --default-800）钉值。
const buttonSrc = readFileSync(
  join(here, "..", "src/shadcn-bridge/heroui/button.tsx"),
  "utf8",
);

for (const sig of ["bg-white/25", "bg-white/30", "backdrop-blur-[3px]"]) {
  check(
    `桥 Button wash 退役签名不存在：${sig}`,
    !buttonSrc.includes(sig),
    "wash 载体类串回潮——须改走托盘/实色并重建本段断言",
  );
}
for (const mapName of [
  "borderedColorClassMap",
  "flatColorClassMap",
  "lightColorClassMap",
]) {
  const block =
    buttonSrc.match(
      new RegExp(`${mapName}[^=]*=\\s*\\{([\\s\\S]*?)\\};`),
    )?.[1] ?? "";

  check(
    `桥 Button ${mapName} 全走托盘（六色 tray 引用在册、无 bg-white）`,
    (block.match(/tray-/g) ?? []).length >= 6 && !block.includes("bg-white"),
    `tray 引用 ${(block.match(/tray-/g) ?? []).length} 处`,
  );
}
{
  const ratio = tokens["--default-800"]
    ? contrast("#ffffff", tokens["--default-800"])
    : 0;

  check(
    `solid default 标签对：#ffffff on --default-800 = ${ratio.toFixed(2)}（钉值 14.89、底线 4.5）`,
    ratio >= 4.5 && Math.abs(ratio - 14.89) <= 0.1,
    `实测 ${ratio.toFixed(2)}`,
  );
}

// ── 桥 Button ghost 变体（R2-S1-02 同族尾项）────────────────
// ghost 静止态无载体（透明底＋继承色）、hover 只有 white/40
// wash：按本门数学（零 blur 抵扣）复算，继承正文色直坐壁纸暗
// 区静止仅 1.09、hover wash 叠纯黑/暗区仅 3.09/3.32，与已退役
// wash 族同形——S1-02 整改时登记为范围外观察，本尾项按同族不
// 漏口径根治。修法在定义级：ui/button 的 ghost 类串加 btn-ghost
// 标记（标记本身无样式），globals.css 覆盖块在两类暴露语境给
// 它中性托盘载体——玻璃语境内（标签否则骑玻璃网格 6.22 的底
// 线级最差格）、壁纸模式下（has-wallpaper-bg/has-custom-bg，可
// 直坐图像）。覆盖语境外无任何规则命中，渲染逐值不变。本段断
// 言：① ghost 类串逐字钉住（标记＋原 wash hover 在册）；
// ② 桥 mapVariant 的 ghost→ghost 定义链在册；③ .btn-ghost 在
// globals.css 只出现在覆盖块区域内，且区域内托盘三声明与两
// 类语境选择器齐备（撤覆盖即红）；④ 覆盖后有效对按 token 复
// 算：静止/hover 均 >= 7 目标并对平钉值 9.50/8.23；⑤ 使用面
// 普查——整改在定义级，任何 variant="ghost" 使用面自动落入
// 覆盖，普查报告计数并以定义链在册为覆盖依据。
const uiButtonSrc = readFileSync(
  join(here, "..", "src/components/ui/button.tsx"),
  "utf8",
);

check(
  "ghost 类串逐字钉住（btn-ghost 标记＋原 hover wash，覆盖语境外逐值不变）",
  uiButtonSrc.includes('ghost: "btn-ghost hover:bg-white/40 hover:text-foreground"'),
  "ui/button.tsx 的 ghost 变体类串已漂移——须同步评估覆盖块与本段",
);
check(
  "桥 mapVariant ghost→ghost 定义链在册",
  buttonSrc.includes('return "ghost";'),
  "shadcn-bridge/heroui/button.tsx 的 ghost 映射缺失",
);
{
  const ghostRegion =
    glassTextRegion.length > 0
      ? glassTextRegion.slice(glassTextRegion.indexOf(".btn-ghost"))
      : "";
  const cssOutsideRegion = glassTextRegion
    ? css.split(glassTextRegion).join("")
    : css;

  check(
    "ghost 覆盖规则只在 GLASS-TEXT-OVERRIDES 区域内（区域外 .btn-ghost 零命中）",
    ghostRegion.length > 0 && !cssOutsideRegion.includes(".btn-ghost"),
    "标记类样式泄漏到覆盖块之外——实色面行为将不再逐值不变",
  );
  check(
    "ghost 覆盖块齐备（玻璃语境＋壁纸双模式选择器＋托盘三声明）",
    ghostRegion.includes(".glass-card") &&
      ghostRegion.includes("has-wallpaper-bg .btn-ghost") &&
      ghostRegion.includes("has-custom-bg .btn-ghost") &&
      ghostRegion.includes("background: var(--tray-default);") &&
      ghostRegion.includes("color: var(--tray-default-foreground);") &&
      ghostRegion.includes("background: var(--tray-default-hover);"),
    "覆盖块缺选择器或托盘声明——撤覆盖/改浅即应由本断言点名",
  );
  const ghostStatic =
    tokens["--tray-default-foreground"] && tokens["--tray-default"]
      ? contrast(tokens["--tray-default-foreground"], tokens["--tray-default"])
      : 0;
  const ghostHover =
    tokens["--tray-default-foreground"] && tokens["--tray-default-hover"]
      ? contrast(
          tokens["--tray-default-foreground"],
          tokens["--tray-default-hover"],
        )
      : 0;

  check(
    `ghost 玻璃/壁纸语境有效对（静止）：--tray-default-foreground on --tray-default = ${ghostStatic.toFixed(2)}（钉值 9.50、目标 7）`,
    ghostStatic >= 7 && Math.abs(ghostStatic - 9.5) <= 0.1,
    `实测 ${ghostStatic.toFixed(2)}`,
  );
  check(
    `ghost 玻璃/壁纸语境有效对（hover）：--tray-default-foreground on --tray-default-hover = ${ghostHover.toFixed(2)}（钉值 8.23、目标 7）`,
    ghostHover >= 7 && Math.abs(ghostHover - 8.23) <= 0.1,
    `实测 ${ghostHover.toFixed(2)}`,
  );
}
{
  let ghostUses = 0;
  let scannedFiles = 0;
  const ghostWalk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);

      if (entry.isDirectory()) {
        ghostWalk(path);
      } else if (/\.tsx?$/.test(entry.name)) {
        scannedFiles++;
        const text = readFileSync(path, "utf8");

        ghostUses += (
          text.match(/variant=\{?["']ghost["']\}?/g) ?? []
        ).length;
      }
    }
  };

  ghostWalk(join(here, "..", "src"));
  check(
    `ghost 使用面普查（实扫 ${scannedFiles} 文件、variant="ghost" ${ghostUses} 处，定义级覆盖自动落入）`,
    scannedFiles > 0,
    "普查扫描面失效",
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

// ── 实色面登记（R4-S1-02；RR4-C6-03 补全同机制全集）────────
// 机制谓词：静止态不透明浅实色填充充当页面/弹窗内面板。globals.css
// 自述口径钉死：--content1 只作小型表单控件的填充（输入框、原生
// select、选择触发器），永不作表面；弹窗内板走 glass-inset、卡片
// 走 glass-card/glass-surface。此前 content1 准入名单无仓库内门禁，
// 仪表盘指标条（metric-card 的 bg-content1 白板面）与节点卸载确认
// 内板（confirm-modals 的 bg-default-50）自诞生起无门可拦——本轮
// 两面均已按玻璃口径整改（glass-card / glass-inset），不登记为
// 实色豁免。本段把剩余全集钉册：bg-content1 逐文件计数与下表恰
// 等（新增文件、计数漂移即红，登记时须逐处分类为控件填充；表面
// 用途不得登记、必须改玻璃层）；bg-default-50 登记集为空（该
// token 的任何出现都须先登记分类；bg-default-500/10 是另一
// token 的已批准洗色，不在本段口径内）。
// RR4-C6-03：原登记只钉 bg-content1 与 bg-default-50 两个
// token，同机制的浅实色兄弟 token（bg-default-100/200/300、
// bg-content2/3/4）不在普查内——探针以其作面板面全绿逃逸。本
// 轮按机制机械收全：普查面为 content 色阶全档（bg-content<N>
// 任意 N）与 default 色阶的浅档全集；浅档边界由 token 实值机
// 械判定（相对亮度 ≥ 0.5），门内另断言主题实测浅档集恰为
// {50,100,200,300} 且 -400 在界外（主题改值跨界即点名重登
// 记）。处置口径：content1 与 default-100/200/300 的现行用法
// 全是组件填充（控件／进度条轨道／状态点／开关与进度构件／
// 小徽标），逐文件钉册，新文件出现或计数漂移即红，登记时须
// 分类为组件填充、表面用途不得登记；content2 起与 default-50
// 登记集为空，任何出现即红、先登记分类。
const SOLID_FILL_PINS = {
  // 控件填充：输入框/文本域本体
  "components/ui/input.tsx": 1,
  "components/ui/textarea.tsx": 1,
  // 控件填充：选择触发器（桥接 Select 的触发面与内嵌输入）
  "shadcn-bridge/heroui/select.tsx": 2,
  // 控件填充：次级按钮面（ui 按钮变体）
  "components/ui/button.tsx": 1,
  // 控件填充：搜索框 inputWrapper 与页面搜索标签
  "components/search-bar.tsx": 1,
  "components/page-kit.tsx": 1,
  "pages/user.tsx": 1,
  // 控件填充：原生 select / 输入（页面级小控件）
  "pages/inbound.tsx": 1,
  "pages/inbound/target-picker.tsx": 1,
  "pages/subscription.tsx": 4,
  // pages/subscription/rename-rules.tsx 已整文件删除（C3-01 死代码），登记撤销。
  "pages/node/tune-modal.tsx": 1,
  // 控件填充：模板页两处原生 select（templates 与 builder 的
  // selectCls）——R4-S1-02 点名一并登记，属控件口径、非表面。
  "pages/subscription/templates.tsx": 1,
  "pages/subscription/template-builder.tsx": 1,
};
// 组件填充：进度条轨道（default-100 全数用途）
const SOLID_FILL_PINS_DEFAULT_100 = {
  "pages/dashboard/admin-overview.tsx": 2,
  "pages/inbound/inbound-form-modal.tsx": 1, // hover 态轨道填充
  "pages/node/monitor-view.tsx": 5,
  "pages/node/node-live-stats.tsx": 1,
  "pages/node/tune-modal.tsx": 1,
  "pages/subscription/traffic-stats.tsx": 1,
  "pages/subscription.tsx": 1,
  "pages/user.tsx": 1,
};
// 组件填充：进度条轨道／细条／图标小片
const SOLID_FILL_PINS_DEFAULT_200 = {
  "pages/config.tsx": 1, // h-7 w-7 图标小片
  "pages/dashboard.tsx": 1, // 进度条轨道
  "pages/node/quality-history-bar.ts": 1,
  "pages/node/tunnel-monitor-view.tsx": 2,
};
// 组件填充：状态点／开关与进度构件／小徽标／代码片底
const SOLID_FILL_PINS_DEFAULT_300 = {
  "components/page-kit.tsx": 1,
  "components/ui/progress.tsx": 1,
  "components/ui/switch.tsx": 1,
  "pages/config.tsx": 1,
  "pages/inbound/kernel-panel.tsx": 1,
  "pages/inbound/sources.tsx": 1,
  "pages/inbound.tsx": 2,
  "pages/limit.tsx": 1,
  "pages/node/monitor-view.tsx": 3,
  "pages/node/node-row.tsx": 1,
  // pages/subscription/controls.tsx 的 -300 徽标随 EditorTabs 删除（C3-01），登记撤销。
  "pages/subscription.tsx": 1,
};
{
  // 浅档边界自证：default 色阶各档相对亮度按 :root 实值复算——
  // 普查浅档集（亮度 ≥ 0.5）必须恰为登记的四档、-400 必须在界
  // 外；主题改值使任一档跨界时本断言先红，要求同步重登记。
  const LIGHT_DEFAULT_SHADES = ["50", "100", "200", "300"];
  const lumProblems = [];

  for (const shade of [...LIGHT_DEFAULT_SHADES, "400"]) {
    const hex = tokens[`--default-${shade}`];

    if (!hex) {
      lumProblems.push(`--default-${shade} token 缺失`);
      continue;
    }
    const L = luminance(hex);

    if (LIGHT_DEFAULT_SHADES.includes(shade) && L < 0.5)
      lumProblems.push(`--default-${shade} 实测亮度 ${L.toFixed(2)} < 0.5 已不属浅档，须重登记普查面`);
    if (!LIGHT_DEFAULT_SHADES.includes(shade) && L >= 0.5)
      lumProblems.push(`--default-${shade} 实测亮度 ${L.toFixed(2)} ≥ 0.5 已入浅档，须纳入普查登记`);
  }
  check(
    "实色面浅档边界自证（default 色阶实值亮度 ≥0.5 恰为 50/100/200/300、-400 在界外）",
    lumProblems.length === 0,
    lumProblems.join("；"),
  );

  const solidPinTables = {
    "bg-content1": SOLID_FILL_PINS,
    "bg-default-100": SOLID_FILL_PINS_DEFAULT_100,
    "bg-default-200": SOLID_FILL_PINS_DEFAULT_200,
    "bg-default-300": SOLID_FILL_PINS_DEFAULT_300,
  };
  const solidFound = {};
  const solidEmptyHits = [];
  const solidWalk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);

      if (entry.isDirectory()) {
        solidWalk(path);
      } else if (/\.tsx?$/.test(entry.name)) {
        const text = readFileSync(path, "utf8");
        const rel = path
          .slice(join(here, "..", "src").length + 1)
          .split("\\")
          .join("/");

        for (const m of text.matchAll(/bg-content(\d+)(?!\d)/g)) {
          const token = `bg-content${m[1]}`;

          if (token === "bg-content1") {
            (solidFound[token] ??= {})[rel] =
              (solidFound[token]?.[rel] ?? 0) + 1;
          } else {
            solidEmptyHits.push(
              `${token} ${rel}（content 色阶仅 content1 有登记且仅限控件填充，其余档登记集为空——须先登记分类）`,
            );
          }
        }
        for (const m of text.matchAll(/bg-default-(\d+)(?!\d)/g)) {
          const token = `bg-default-${m[1]}`;

          if (token in solidPinTables) {
            (solidFound[token] ??= {})[rel] =
              (solidFound[token]?.[rel] ?? 0) + 1;
          } else if (token === "bg-default-50") {
            solidEmptyHits.push(
              `${token} ${rel}（登记集为空——静止实色内板须改 glass-inset 或先登记分类）`,
            );
          }
          // -400 起非浅档（边界由上方亮度自证钉住），不在本段口径内。
        }
      }
    }
  };

  solidWalk(join(here, "..", "src"));
  const solidDrift = [...solidEmptyHits];

  for (const [token, pins] of Object.entries(solidPinTables)) {
    for (const [file, pin] of Object.entries(pins)) {
      const got = solidFound[token]?.[file] ?? 0;

      if (got !== pin) solidDrift.push(`${token} ${file} 实扫 ${got} 处（钉数 ${pin}）`);
    }
    for (const [file, got] of Object.entries(solidFound[token] ?? {})) {
      if (!(file in pins))
        solidDrift.push(`${token} ${file} 实扫 ${got} 处（无登记——须分类为组件填充后登记，表面用途改玻璃层）`);
    }
  }
  const solidPinTotal = Object.values(solidPinTables)
    .flatMap((t) => Object.values(t))
    .reduce((a, b) => a + b, 0);

  check(
    `实色面登记差集 ∅（浅实色全集钉册 ${solidPinTotal} 处组件填充：bg-content1 ${Object.values(SOLID_FILL_PINS).reduce((a, b) => a + b, 0)}＋bg-default-100/200/300，bg-content2+/bg-default-50 登记 0 处）`,
    solidDrift.length === 0,
    solidDrift.slice(0, 5).join("；"),
  );
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
