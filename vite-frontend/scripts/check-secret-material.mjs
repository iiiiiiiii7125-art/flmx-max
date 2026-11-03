#!/usr/bin/env node
// check-secret-material.mjs — 入库文件秘密材料扫描门（fix10 S4，
// I-C3-01 落地）。
//
// 背景：1.3.x 时代的审计取证工件整树入库（audit-out-132/，803 文
// 件），其中含捕获时真实秘密材料——环境检查/xtrace 日志里的
// JWT_SECRET= 赋值行、Xray 配置快照里的 Reality privateKey 与
// PEM 私钥块——随仓库分发、凡克隆即带出。整树已移出，本门守今
// 后：任何同形态材料再进 git 跟踪集即点名拦截。
//
// 口径（三条规则，阈值按现行树的合成夹具形态校准）：
//  1. PEM_PRIVATE_KEY：完整的 BEGIN/END PRIVATE KEY 块且块内
//     base64 字符数 ≥ 96（真实密钥材料的量级）。仓库内合法存量
//     均为合成短桩（golden/test 夹具的块内仅 4 字符），不误报；
//     无 END 的截断示例（如设计文档里的 "..." 形态）不成块、不
//     报。
//  2. PRIVATE_KEY_FIELD：JSON 形态 "privateKey": "<≥40 字符的
//     base64 类字面量>"（r17 快照的泄漏形态）。testdata/ 与
//     *_test.go 豁免：渲染器 golden 夹具按其功能必须内嵌结构真
//     实的合成密钥（43–44 字符的测试向量），逐文件豁免清单不可
//     维护；捕获工件落在普通目录、不在此列。
//  3. SECRET_ASSIGNMENT：JWT_SECRET/POSTGRES_PASSWORD/
//     PGPASSWORD/GITHUB_TOKEN/GH_TOKEN 的明文赋值（值长 ≥ 9、
//     非 $ 变量引用、非占位词、非单一字符集填充），及连接串内
//     嵌口令（口令长 ≥ 8、主机非 localhost 类示例、口令段非
//     ${} 变量展开）。测试/文档中的合成与示例值经下方
//     ALLOWLIST 逐条登记豁免（路径＋规则＋理由），不做目录级
//     放行。
//
// 纪律：违规输出只给 文件:行＋规则名，绝不打印匹配到的值。每
// 次运行先跑合成自检（正例必点名、反例必放行），自检不过以退
// 出码 3 失败——门坏了必须比违规更响。退出码：0 通过、1 有违
// 规、3 自检失败/枚举失败。
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

// 已知合法存量：逐条（路径＋规则＋理由）。新增豁免必须说明该值
// 为何是合成/示例/一次性，而非「它看起来不像真的」。
const ALLOWLIST = [
  {
    path: ".github/workflows/ci-build.yml",
    rule: "SECRET_ASSIGNMENT",
    reason: "CI e2e 专用一次性凭据，被测面板使用 /tmp 临时库、用完即弃",
  },
  {
    path: "doc/guide/postgresql.md",
    rule: "SECRET_ASSIGNMENT",
    reason: "用户文档中的 .env 示例值，读者被指引替换为自生成值",
  },
  {
    path: "go-backend/internal/app/app_postgres_password_test.go",
    rule: "SECRET_ASSIGNMENT",
    reason: "口令强度校验的合成测试向量（强/弱口令样本），非真实凭据",
  },
  {
    path: "test-install-scripts-proxy.sh",
    rule: "SECRET_ASSIGNMENT",
    reason: "安装脚本测试套件写入临时 .env 的合成夹具值",
  },
];

const PLACEHOLDER_RE = /your|example|change|xxx|placeholder|dummy|sample|redacted|test/i;

function base64Count(text) {
  const m = text.match(/[A-Za-z0-9+/=]/g);
  return m ? m.length : 0;
}

function looksLikeFiller(value) {
  return new Set(value).size <= 2;
}

function checkPEM(text) {
  const hits = [];
  const re = /-----BEGIN ([A-Z0-9 ]*?)PRIVATE KEY-----([\s\S]*?)-----END \1PRIVATE KEY-----/g;
  let m;
  while ((m = re.exec(text)) !== null) {
    if (base64Count(m[2]) >= 96) {
      hits.push({ rule: "PEM_PRIVATE_KEY", index: m.index });
    }
  }
  return hits;
}

function isFixturePath(path) {
  return /(^|\/)testdata\//.test(path) || /_test\.go$/.test(path);
}

function checkPrivateKeyField(text, path) {
  if (isFixturePath(path)) return [];
  const hits = [];
  const re = /"privateKey"\s*:\s*"([A-Za-z0-9+/=_-]{40,})"/g;
  let m;
  while ((m = re.exec(text)) !== null) {
    hits.push({ rule: "PRIVATE_KEY_FIELD", index: m.index });
  }
  return hits;
}

const ASSIGNMENT_KEYS = ["JWT_SECRET", "POSTGRES_PASSWORD", "PGPASSWORD", "GITHUB_TOKEN", "GH_TOKEN"];

function checkAssignment(text) {
  const hits = [];
  const lines = text.split("\n");
  lines.forEach((line, i) => {
    for (const key of ASSIGNMENT_KEYS) {
      const re = new RegExp(`\\b${key}\\s*=\\s*"?([^\\s"'\x60]+)`);
      const m = line.match(re);
      if (!m) continue;
      const value = m[1];
      if (value.length < 9) continue;
      if (value.startsWith("$") || value.startsWith("<") || value.startsWith("{")) continue;
      if (PLACEHOLDER_RE.test(value)) continue;
      if (looksLikeFiller(value)) continue;
      hits.push({ rule: "SECRET_ASSIGNMENT", index: i });
    }
    const url = line.match(/postgres(?:ql)?:\/\/[^:\s/@]+:([^\s/@]+)@([^\s/:]+)/);
    if (url) {
      const [, password, host] = url;
      if (
        password.length >= 8 &&
        !password.includes("${") &&
        !password.includes("$(") &&
        !PLACEHOLDER_RE.test(password) &&
        !looksLikeFiller(password) &&
        host !== "localhost" &&
        host !== "127.0.0.1"
      ) {
        hits.push({ rule: "SECRET_ASSIGNMENT", index: i });
      }
    }
  });
  return hits;
}

function scanText(text, path) {
  return [...checkPEM(text), ...checkPrivateKeyField(text, path), ...checkAssignment(text)];
}

// ---- 合成自检：检测器对构造样本的裁决必须与预期逐格一致 ----
function selfTest() {
  const b64 = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY3ODkw";
  const cases = [
    {
      name: "PEM 正例（真实量级块）",
      path: "probe/snapshot.json",
      text: `-----BEGIN EC PRIVATE KEY-----\n${b64}\n${b64}\n-----END EC PRIVATE KEY-----`,
      expect: 1,
    },
    {
      name: "PEM 反例（合成短桩块）",
      path: "probe/fixture.json",
      text: `-----BEGIN EC PRIVATE KEY-----\nMHcC\n-----END EC PRIVATE KEY-----`,
      expect: 0,
    },
    {
      name: "PEM 反例（无 END 的截断示例）",
      path: "probe/doc.md",
      text: `"privateKey": "-----BEGIN OPENSSH PRIVATE KEY-----...",`,
      expect: 0,
    },
    {
      name: "PRIVATE_KEY_FIELD 正例（普通目录快照）",
      path: "probe/xray-config.json",
      text: `"privateKey": "${b64.slice(0, 44)}"`,
      expect: 1,
    },
    {
      name: "PRIVATE_KEY_FIELD 反例（testdata 夹具豁免）",
      path: "go-backend/internal/kernel/testdata/xray.golden.json",
      text: `"privateKey": "${b64.slice(0, 44)}"`,
      expect: 0,
    },
    {
      name: "SECRET_ASSIGNMENT 正例（日志赋值行）",
      path: "probe/envcheck.log",
      text: `+ JWT_SECRET=${b64.slice(0, 18)}`,
      expect: 1,
    },
    {
      name: "SECRET_ASSIGNMENT 反例（变量引用）",
      path: "probe/install.sh",
      text: `JWT_SECRET=$JWT_SECRET`,
      expect: 0,
    },
    {
      name: "SECRET_ASSIGNMENT 反例（占位示例）",
      path: "probe/guide.md",
      text: `JWT_SECRET=your-secret-here`,
      expect: 0,
    },
  ];
  const bad = [];
  for (const c of cases) {
    const got = scanText(c.text, c.path).length > 0 ? 1 : 0;
    if (got !== c.expect) bad.push(c.name);
  }
  return bad;
}

const badSelf = selfTest();
if (badSelf.length > 0) {
  console.log(`FAIL  check-secret-material 合成自检未过：${badSelf.join("；")}`);
  process.exit(3);
}

// ---- 枚举 git 跟踪集并逐个扫描 ----
const top = spawnSync("git", ["rev-parse", "--show-toplevel"], { cwd: here, encoding: "utf8" });
if (top.status !== 0) {
  console.log("FAIL  check-secret-material 无法定位仓库根（git rev-parse 失败）");
  process.exit(3);
}
const root = top.stdout.trim();
const ls = spawnSync("git", ["ls-files", "-z"], { cwd: root, encoding: "utf8" });
if (ls.status !== 0) {
  console.log("FAIL  check-secret-material 无法枚举 git 跟踪文件");
  process.exit(3);
}
const files = ls.stdout.split("\0").filter(Boolean);

const allowed = (path, rule) =>
  ALLOWLIST.some((e) => e.path === path && e.rule === rule);

const violations = [];
let scanned = 0;
for (const path of files) {
  let buf;
  try {
    buf = readFileSync(join(root, path));
  } catch {
    continue; // 跟踪集与工作树不一致（已删未暂存等）时跳过，不冒充违规
  }
  if (buf.length > 20 * 1024 * 1024) continue;
  if (buf.subarray(0, 8192).includes(0)) continue; // 二进制
  const text = buf.toString("utf8");
  scanned++;
  for (const hit of scanText(text, path)) {
    if (allowed(path, hit.rule)) continue;
    const line =
      hit.rule === "SECRET_ASSIGNMENT"
        ? hit.index + 1
        : text.slice(0, hit.index).split("\n").length;
    violations.push(`${path}:${line} ${hit.rule}`);
  }
}

// 同文件同规则只报前 3 处并汇总计数，避免 xtrace 类日志刷屏。
const grouped = new Map();
for (const v of violations) {
  const key = v.replace(/:\d+ /, ": ");
  if (!grouped.has(key)) grouped.set(key, []);
  grouped.get(key).push(v);
}
const report = [];
for (const [, list] of grouped) {
  report.push(...list.slice(0, 3));
  if (list.length > 3) report.push(`${list[0].split(" ")[0]} 同规则共 ${list.length} 处（余略）`);
}

if (report.length > 0) {
  console.log(`FAIL  check-secret-material 在 ${scanned} 个入库文本文件中发现秘密材料形态：`);
  for (const line of report) console.log(`  ${line}`);
  process.exit(1);
}
console.log(`PASS  check-secret-material：${scanned} 个入库文本文件无秘密材料形态（合成自检 8 格全过）`);
