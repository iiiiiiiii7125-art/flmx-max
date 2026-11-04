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
// RR4-C6-I02 / RR4-C4-02 口径补全（判定面对齐 GitHub 语义）：
//   - 带引号的 uses（uses: "owner/repo@<SHA>"）是合法 YAML 形态，
//     旧正则把引号算进 ref 一律判红（误红方向）——现先剥引号。
//   - docker:// 形态以 digest 钉版者（docker://image@sha256:<64
//     位 digest>，可带 tag 前缀）是不可变引用，与 SHA 钉版同等
//     接受；docker:// 无 digest 者仍是可变引用、照旧判红。
//   - 行尾 `# vX` 版本注释此前只在门头与失败文案里声称、不在校
//     验内——现 SHA 钉版行必须带 `# v<主版本>`（可带 .次/修订
//     段）注释，缺失或形态不对即点名红（注释是 Dependabot 更新
//     时保留可读版本的人读锚，纪律自述含它就必须验它）。
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
let dockerPinned = 0;
let local = 0;

for (const name of readdirSync(wfDir).sort()) {
  if (!/\.ya?ml$/.test(name)) continue;
  const lines = readFileSync(join(wfDir, name), "utf8").split("\n");

  lines.forEach((line, i) => {
    const m = line.match(/^\s*-?\s*uses:\s*(\S+)(.*)$/);

    if (!m) return;
    total += 1;
    let ref = m[1];
    const rest = m[2] ?? "";

    // 引号化 uses：YAML 合法形态，剥成对引号后按同一口径判定。
    if (
      ref.length >= 2 &&
      ((ref.startsWith('"') && ref.endsWith('"')) ||
        (ref.startsWith("'") && ref.endsWith("'")))
    ) {
      ref = ref.slice(1, -1);
    }

    if (ref.startsWith("./")) {
      local += 1; // 仓内 reusable workflow，无外部引用可钉
      return;
    }
    if (ref.startsWith("docker://")) {
      // Docker action：仅 @sha256:<64 位 digest> 为不可变钉版；
      // 只带 tag 的 docker:// 与 @v4 同为可变引用，判红。
      if (/^docker:\/\/[\w./-]+(:[\w.-]+)?@sha256:[0-9a-f]{64}$/.test(ref)) {
        dockerPinned += 1;
        return;
      }
      failures.push(
        `${name}:${i + 1} uses: ${ref} 为 docker:// 引用但未以 @sha256:<64 位 digest> 钉版`,
      );
      return;
    }
    if (/^[\w.-]+\/[\w./-]+@[0-9a-f]{40}$/.test(ref)) {
      // SHA 本体合格后还须验行尾版本注释（纪律自述含 `# vX`）：
      // 注释须紧随其后、以 # v 开头，后接主版本数字（可带 .次段）。
      if (!/#\s*v\d+(\.\d+){0,2}\b/.test(rest)) {
        failures.push(
          `${name}:${i + 1} uses: ${ref} 已 SHA 钉版但缺行尾版本注释（须 # vX，如 # v4）`,
        );
        return;
      }
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
  `workflow-pins: uses 全集 ${total} 处（SHA 钉版 ${pinned}、docker digest 钉版 ${dockerPinned}、仓内本地 ${local}、未钉版 ${failures.length}）`,
);
if (failures.length > 0) {
  console.log("RESULT: FAIL");
  process.exit(1);
}
console.log("RESULT: PASS");
