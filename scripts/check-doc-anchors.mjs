#!/usr/bin/env node
// check-doc-anchors.mjs — 文档站内锚点对账门（R8-L7-02 常设门）。
//
// VitePress 构建不校验锚点：链接里的 slug 写错时页面可达但落不到节，
// 只能靠对账发现。本脚本机械复算每篇文档的标题 id 集（slug 规则与
// 构建一致：非字母/数字/下划线/连字符的字符run折叠为单个「-」、去首尾、
// 小写、同名重复追加 -1/-2），再与全部站内链接的目标锚点逐条对账，
// 差集非空即失败。只读，不修改任何文件。
//
// 用法：node scripts/check-doc-anchors.mjs [docDir]
// 退出码：0 = 全部锚点命中；1 = 存在失效锚点或目标文件缺失。
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { join, dirname, basename } from "node:path";

const docDir = process.argv[2] || new URL("../doc", import.meta.url).pathname;

function slugify(raw) {
  let s = raw
    .replace(/[`*_~]/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/<[^>]+>/g, "")
    .trim()
    .toLowerCase();
  s = s.replace(/[^\p{L}\p{N}_-]+/gu, "-").replace(/-{2,}/g, "-");
  s = s.replace(/^-+|-+$/g, "");
  if (/^\d/.test(s)) s = "_" + s;
  return s;
}

function headingsOf(file) {
  const lines = readFileSync(file, "utf8").split("\n");
  const ids = new Map();
  const used = new Map();
  let inFence = false;
  let inFront = false;
  lines.forEach((line, i) => {
    if (i === 0 && line.trim() === "---") inFront = true;
    else if (inFront && line.trim() === "---") inFront = false;
    else if (inFront) return;
    if (/^\s*```/.test(line)) inFence = !inFence;
    if (inFence) return;
    const m = line.match(/^(#{1,6})\s+(.*?)\s*#*\s*$/);
    if (m) {
      const base = slugify(m[2]);
      let id = base;
      if (used.has(base)) {
        const n = used.get(base) + 1;
        used.set(base, n);
        id = `${base}-${n}`;
      } else used.set(base, 0);
      ids.set(id, i + 1);
    }
  });
  return ids;
}

function mdFiles(dir) {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...mdFiles(p));
    else if (e.name.endsWith(".md")) out.push(p);
  }
  return out;
}

const files = mdFiles(docDir).filter(
  (f) => !f.includes(".vitepress") && !f.includes("node_modules"),
);
const idSets = new Map(files.map((f) => [f, headingsOf(f)]));

let total = 0;
let bad = 0;
for (const f of files) {
  const text = readFileSync(f, "utf8");
  const linkRe = /\]\(([^)\s]+)\)/g;
  let m;
  while ((m = linkRe.exec(text)) !== null) {
    const target = m[1];
    if (/^(https?:|mailto:)/.test(target)) continue;
    const hashIdx = target.indexOf("#");
    if (hashIdx < 0) continue;
    const pathPart = target.slice(0, hashIdx);
    const anchor = decodeURIComponent(target.slice(hashIdx + 1));
    if (!anchor) continue;
    let targetFile = f;
    if (pathPart) {
      const clean = pathPart.replace(/^\//, "").replace(/\.html$/, ".md");
      targetFile = clean.endsWith(".md")
        ? join(docDir, clean.replace(/^\.\//, ""))
        : join(dirname(f), clean);
      if (!existsSync(targetFile)) {
        // 相对当前文件的路径再试一次
        const alt = join(dirname(f), clean);
        if (existsSync(alt)) targetFile = alt;
      }
    }
    total++;
    if (!existsSync(targetFile)) {
      console.log(`FAIL: ${basename(f)} -> ${target}（目标文件不存在）`);
      bad++;
      continue;
    }
    const ids = idSets.get(targetFile) || headingsOf(targetFile);
    if (!ids.has(anchor)) {
      console.log(
        `FAIL: ${basename(f)} -> ${target}（${basename(targetFile)} 无此锚点）`,
      );
      bad++;
    }
  }
}
console.log(`站内锚点对账：共 ${total} 条，失效 ${bad} 条`);
process.exit(bad ? 1 : 0);
