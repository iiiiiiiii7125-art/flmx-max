#!/usr/bin/env node
// check-workflow-pins.mjs — 工作流 action 钉版纪律门（R4-C4-01）。
//
// 仓库纪律：.github/workflows 里的每个外部 action 都按完整提交
// SHA 钉版（uses: owner/repo@<40 位 SHA> # vX）。R4 审计机械普查
// 出唯一例外——ci-build 的 actions/setup-python@v5 用可变大版本
// tag，而该作业随后 pip 安装依赖并跑 e2e，tag 被移动/污染即直
// 达执行面。此前此纪律无机械门守护（靠 Dependabot 与人工），
// 本门把全集普查常设化：任一 uses: 不是「本地路径（./ 开头，仓
// 内 reusable workflow）」或「完整 40 位 SHA 钉版」即点名红。
//
// 运行方法（仓库根目录）：
//   node scripts/check-workflow-pins.mjs
// 退出码 0 = 全部钉版，非 0 = 有未钉版 uses:（逐行点名）。
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const wfDir = join(root, ".github", "workflows");

const failures = [];
let total = 0;
let pinned = 0;
let local = 0;

for (const name of readdirSync(wfDir).sort()) {
  if (!/\.ya?ml$/.test(name)) continue;
  const lines = readFileSync(join(wfDir, name), "utf8").split("\n");

  lines.forEach((line, i) => {
    const m = line.match(/^\s*-?\s*uses:\s*(\S+)/);

    if (!m) return;
    total += 1;
    const ref = m[1];

    if (ref.startsWith("./")) {
      local += 1; // 仓内 reusable workflow，无外部引用可钉
      return;
    }
    if (/^[\w.-]+\/[\w./-]+@[0-9a-f]{40}$/.test(ref)) {
      pinned += 1;
      return;
    }
    failures.push(
      `${name}:${i + 1} uses: ${ref} 未按完整 SHA 钉版（须 owner/repo@<40 位 SHA> # vX）`,
    );
  });
}

for (const f of failures) console.log(`FAIL  ${f}`);
console.log(
  `workflow-pins: uses 全集 ${total} 处（SHA 钉版 ${pinned}、仓内本地 ${local}、未钉版 ${failures.length}）`,
);
if (failures.length > 0) {
  console.log("RESULT: FAIL");
  process.exit(1);
}
console.log("RESULT: PASS");
