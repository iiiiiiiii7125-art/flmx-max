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
// A4-01 提取面补全（形态普查对位 GitHub 接受的全部写法）：旧实现
// 只有一行正则 `^\s*-?\s*uses:`，flow 映射形（- {uses: …}）与
// 块标量形（uses: >- 换行写 ref）根本不进普查——同一 PR 里换个
// 写法就能带可变 ref 绕过本门且计数都不增。现提取覆盖三形：
//   (a) 块映射形：`- uses: <ref>` / `uses: <ref>`（含引号）；
//   (b) flow 映射形：行内 `{uses: <ref>, …}` / `{ uses: <ref> }`；
//   (c) 块标量形：`uses: >-`（或 |、>+ 等）后首个非空行即 ref。
// 并加前提对账（premise）：逐文件以笨办法数「代码位 uses: 键出现
// 数」（整行注释不计、行尾注释段不计），必须与提取数逐文件相等，
// 否则 PREMISE FAIL（exit 2）点名文件——将来出现第四种写法时本门
// 先红在前提上，而不是静默漏计。
//
// 运行方法（仓库根目录）：
//   node scripts/check-workflow-pins.mjs [repoRoot]
// 退出码 0 = 全部钉版；1 = 有未钉版 uses:（逐行点名）；
// 2 = 前提失效（提取面与原文出现数对不上 / 普查为空，见 A4-02）。
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root =
  process.argv[2] || join(dirname(fileURLToPath(import.meta.url)), "..");
const wfDir = join(root, ".github", "workflows");

const failures = [];
const premiseFailures = [];
let total = 0;
let pinned = 0;
let dockerPinned = 0;
let local = 0;

// 代码位片段：整行注释（trim 后以 # 起头）整行不计；其余行取首个
// 「空白+#」之前的部分（行尾版本注释段不计入出现数）。
function codeSegment(line) {
  if (line.trimStart().startsWith("#")) return "";
  const idx = line.search(/\s#/);
  return idx >= 0 ? line.slice(0, idx) : line;
}

// 笨办法出现数：键位 uses（行首或空白/{/, 之后）出现几次。
function rawUsesCount(lines) {
  let n = 0;
  for (const line of lines) {
    const seg = codeSegment(line);
    const hits = seg.match(/(?:^|[\s{,])uses\s*:/g);
    if (hits) n += hits.length;
  }
  return n;
}

function judge(name, lineNo, ref, rest) {
  total += 1;
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
      `${name}:${lineNo} uses: ${ref} 为 docker:// 引用但未以 @sha256:<64 位 digest> 钉版`,
    );
    return;
  }
  if (/^[\w.-]+\/[\w./-]+@[0-9a-f]{40}$/.test(ref)) {
    // SHA 本体合格后还须验行尾版本注释（纪律自述含 `# vX`）：
    // 注释须紧随其后、以 # v 开头，后接主版本数字（可带 .次段）。
    if (!/#\s*v\d+(\.\d+){0,2}\b/.test(rest)) {
      failures.push(
        `${name}:${lineNo} uses: ${ref} 已 SHA 钉版但缺行尾版本注释（须 # vX，如 # v4）`,
      );
      return;
    }
    pinned += 1;
    return;
  }
  failures.push(
    `${name}:${lineNo} uses: ${ref} 未按完整 SHA 钉版（须 owner/repo@<40 位 SHA> # vX）`,
  );
}

for (const name of readdirSync(wfDir).sort()) {
  if (!/\.ya?ml$/.test(name)) continue;
  const lines = readFileSync(join(wfDir, name), "utf8").split("\n");
  let extractedHere = 0;
  let pendingBlock = null; // { line } — 块标量形 uses: 等待下一非空行作 ref

  lines.forEach((line, i) => {
    if (pendingBlock) {
      if (line.trim() === "") return; // 块标量空行，继续等 ref 行
      const ref = line.trim().split(/\s+/)[0];
      const rest = line.trim().slice(ref.length);
      judge(name, pendingBlock.line, ref, rest);
      extractedHere += 1;
      pendingBlock = null;
      return;
    }
    if (line.trimStart().startsWith("#")) return;

    // (a) 块映射形：- uses: <ref> / uses: <ref>
    const plain = line.match(/^\s*-?\s*uses\s*:\s*(\S+)(.*)$/);
    if (plain) {
      const value = plain[1];
      if (/^[>|][+-]?$/.test(value)) {
        pendingBlock = { line: i + 1 }; // (c) 块标量形：ref 在后续行
        return;
      }
      judge(name, i + 1, value, plain[2] ?? "");
      extractedHere += 1;
      return;
    }
    // (b) flow 映射形：{uses: <ref>, …} / { uses: <ref> }
    const flow = line.match(
      /(?:\{\s*|,\s*)uses\s*:\s*("[^"]+"|'[^']+'|[^\s,}]+)(.*)$/,
    );
    if (flow) {
      judge(name, i + 1, flow[1], flow[2] ?? "");
      extractedHere += 1;
    }
  });

  const raw = rawUsesCount(lines);
  if (raw !== extractedHere) {
    premiseFailures.push(
      `${name}: 原始 uses: 键出现 ${raw} 处、门提取 ${extractedHere} 处——存在未识别的 uses 写法，提取面与原文对不上（A4-01 前提）`,
    );
  }
}

for (const f of premiseFailures) console.log(`PREMISE FAIL  ${f}`);
for (const f of failures) console.log(`FAIL  ${f}`);
console.log(
  `workflow-pins: uses 全集 ${total} 处（SHA 钉版 ${pinned}、docker digest 钉版 ${dockerPinned}、仓内本地 ${local}、未钉版 ${failures.length}）`,
);
if (premiseFailures.length > 0) {
  console.log("RESULT: FAIL (premise)");
  process.exit(2);
}
if (failures.length > 0) {
  console.log("RESULT: FAIL");
  process.exit(1);
}
console.log("RESULT: PASS");
