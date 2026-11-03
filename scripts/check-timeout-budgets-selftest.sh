#!/usr/bin/env bash
# check-timeout-budgets-selftest.sh — 超时预算门禁的变体自检（B05-001
# 回归门）：把门禁脚本所需文件镜像到临时目录，先验证未变异镜像 PASS，
# 再把 C080 行变异回当年的误分类形态（check=class、后端界改回 class
# 天花板 12s，其余字段——尤其前端真实值 150s——保持不动，使 class
# 分支自身的断言全部满足），断言门禁必须 FAIL 且点名 C080 与
# B05-001 规则。若哪天「版本发现禁登 class」规则被误删/改坏，本
# 自检先红。
#
# 另含 R2-C6-02 回归变体：kernel_external literal 锚点漂移
# （20s→500s）必须 FAIL 且点名 T061——守 literal/const 提取的
# fail-closed 口径与 T061–T063 锚点 case 不被删改。
#
# 用法：check-timeout-budgets-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且变体被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-timeout-budgets.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"

mkdir -p "$MIR/doc/audit" \
  "$MIR/vite-frontend/src/api" "$MIR/vite-frontend/src/utils" \
  "$MIR/go-backend/internal/app" "$MIR/go-backend/internal/monitoring" \
  "$MIR/go-backend/internal/health" \
  "$MIR/go-gost/x/kernel" "$MIR/go-gost/x/socket"
cp "$ROOT/doc/audit/timeout-budgets.tsv" "$MIR/doc/audit/"
cp "$ROOT/vite-frontend/src/api/index.ts" \
   "$ROOT/vite-frontend/src/api/diagnosis-stream.ts" \
   "$ROOT/vite-frontend/src/api/network.ts" "$MIR/vite-frontend/src/api/"
cp "$ROOT/vite-frontend/src/utils/version-update.ts" "$MIR/vite-frontend/src/utils/" 2>/dev/null || true
cp "$ROOT/go-backend/internal/app/app.go" "$MIR/go-backend/internal/app/"
cp -r "$ROOT/go-backend/internal/http" "$MIR/go-backend/internal/"
cp "$ROOT/go-backend/internal/monitoring/limits.go" "$MIR/go-backend/internal/monitoring/"
cp "$ROOT/go-backend/internal/health/checker.go" "$MIR/go-backend/internal/health/"
cp "$ROOT/go-gost/x/kernel/install.go" "$MIR/go-gost/x/kernel/"
cp "$ROOT/go-gost/x/socket/websocket_reporter.go" "$MIR/go-gost/x/socket/"

echo "== baseline: unmutated mirror must PASS =="
if bash "$GATE" "$MIR" > "$TMP/base.log" 2>&1; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline mirror did not pass the gate:"
  cat "$TMP/base.log"
  exit 1
fi

echo "== variant: C080 re-misclassified as class must FAIL, naming C080 =="
python3 - "$MIR/doc/audit/timeout-budgets.tsv" <<'PYEOF'
import sys
p = sys.argv[1]
lines = open(p, encoding='utf-8').read().split('\n')
for i, l in enumerate(lines):
    if l.startswith('C080\t'):
        f = l.split('\t')
        # 只变分类：后端界/类别/来源/校验方式改回 class 形态；前端值与
        # 服务端值保持真实登记，使 class 分支断言本身全部满足——能抓
        # 住这个变体的只剩「版本发现禁登 class」规则。
        f[11] = '12'; f[12] = 'class'; f[13] = 'class-ceiling'; f[14] = 'class'
        lines[i] = '\t'.join(f)
        break
else:
    sys.exit('C080 row not found in mirror registry')
open(p, 'w', encoding='utf-8').write('\n'.join(lines))
PYEOF
if bash "$GATE" "$MIR" > "$TMP/var.log" 2>&1; then
  echo "FAIL: gate PASSED on the C080 class variant — misclassification not caught"
  exit 1
fi
grep -q 'C080' "$TMP/var.log" || { echo "FAIL: gate failed but did not name C080:"; cat "$TMP/var.log"; exit 1; }
grep -q 'B05-001' "$TMP/var.log" || { echo "FAIL: gate failure does not cite the discovery-class rule (B05-001):"; cat "$TMP/var.log"; exit 1; }
echo "OK: variant caught, C080 named"

echo "== variant: C113 re-misclassified as class must FAIL, naming C113 =="
cp "$ROOT/doc/audit/timeout-budgets.tsv" "$MIR/doc/audit/timeout-budgets.tsv"
python3 - "$MIR/doc/audit/timeout-budgets.tsv" <<'PYEOF'
import sys
p = sys.argv[1]
lines = open(p, encoding='utf-8').read().split('\n')
for i, l in enumerate(lines):
    if l.startswith('C113\t'):
        f = l.split('\t')
        # 只变分类与登记口径：服务端改回 global、后端界改回 class
        # 天花板、校验改回 class；前端真实值 330s 保持不动，使 class
        # 分支自身断言全部满足——能抓住这个变体的只剩「内核下发家
        # 族禁登小界」规则（handler 体仍直调 applyKernel 且仍以
        # kernelApplyWriteDeadline 武装，与登记口径脱钩）。
        f[9] = '120'; f[10] = 'global'
        f[11] = '12'; f[12] = 'class'; f[13] = 'class-ceiling'; f[14] = 'class'
        lines[i] = '\t'.join(f)
        break
else:
    sys.exit('C113 row not found in mirror registry')
open(p, 'w', encoding='utf-8').write('\n'.join(lines))
PYEOF
if bash "$GATE" "$MIR" > "$TMP/var2.log" 2>&1; then
  echo "FAIL: gate PASSED on the C113 class variant — kernel-apply misclassification not caught"
  exit 1
fi
grep -q 'C113' "$TMP/var2.log" || { echo "FAIL: gate failed but did not name C113:"; cat "$TMP/var2.log"; exit 1; }
grep -q 'B11-001' "$TMP/var2.log" || { echo "FAIL: gate failure does not cite the kernel-apply family rule (B11-001):"; cat "$TMP/var2.log"; exit 1; }
echo "OK: variant caught, C113 named"

# ── 修复轮 9（R9-23/24/25）提取器绑定变异：三构造在修复前提取
# 输出与基线逐字相同、门全绿（I-C4-08/09/10 实证）；修复后必须
# 分别以点名/真值/助手行缺失被抓。变异只动镜像 index.ts。
FE_API="$MIR/vite-frontend/src/api/index.ts"

echo "== variant R9-23: second Network call in one export must FAIL, naming probeNodeLatency =="
cp "$ROOT/vite-frontend/src/api/index.ts" "$FE_API"
python3 - "$FE_API" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
old = '''export const probeNodeLatency = (ids: number[] = []) =>
  Network.post<Record<string, InboundProbe>>(
    "/node/latency/probe",
    { ids },
    // R8-T-08：扇出作业按公式放宽（见 latencyProbeTimeout）。
    { timeout: latencyProbeTimeout(ids.length) },
  );'''
new = '''export const probeNodeLatency = (ids: number[] = []) => {
Network.post("/node/latency/targets", { ids}, { timeout: latencyProbeTimeout(ids.length)});

return Network.post<Record<string, InboundProbe>>(
"/node/latency/probe",
{ ids},
{ timeout: 999_000},
);
};'''
assert old in s, 'probeNodeLatency anchor not found'
open(p, 'w', encoding='utf-8').write(s.replace(old, new, 1))
PYEOF
if bash "$GATE" "$MIR" > "$TMP/var3.log" 2>&1; then
  echo "FAIL: gate PASSED on the second-call variant — later Network calls still invisible"
  exit 1
fi
grep -q 'probeNodeLatency' "$TMP/var3.log" || { echo "FAIL: gate failed but did not name probeNodeLatency:"; cat "$TMP/var3.log"; exit 1; }
echo "OK: variant caught, probeNodeLatency named"

echo "== variant R9-24: parameter-shadowed config identifier must FAIL, naming rollbackNode =="
cp "$ROOT/vite-frontend/src/api/index.ts" "$FE_API"
python3 - "$FE_API" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
old = '''export const rollbackNode = (id: number) =>
  // R8-T-04：后端界 30s，须破恰等。
  Network.post("/node/rollback", { id }, { timeout: 45_000 });'''
new = '''const rollbackCfg = { timeout: 45_000 };
export const rollbackNode = (id: number, rollbackCfg = { timeout: 999_000 }) =>
  // R8-T-04：后端界 30s，须破恰等。
  Network.post("/node/rollback", { id }, rollbackCfg);'''
assert old in s, 'rollbackNode anchor not found'
open(p, 'w', encoding='utf-8').write(s.replace(old, new, 1))
PYEOF
if bash "$GATE" "$MIR" > "$TMP/var4.log" 2>&1; then
  echo "FAIL: gate PASSED on the parameter-shadowed config variant — top-level fallback still in effect"
  exit 1
fi
grep -q 'rollbackNode' "$TMP/var4.log" || { echo "FAIL: gate failed but did not name rollbackNode:"; cat "$TMP/var4.log"; exit 1; }
grep -q '999' "$TMP/var4.log" || { echo "FAIL: gate failure does not show the true value 999:"; cat "$TMP/var4.log"; exit 1; }
echo "OK: variant caught, rollbackNode named with true value"

echo "== variant R9-25: helper growth term replaced by a static value must FAIL, naming latencyProbeTimeout =="
cp "$ROOT/vite-frontend/src/api/index.ts" "$FE_API"
python3 - "$FE_API" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
old = 'return Math.min(600_000, Math.max(90_000, waves * 22_600 + 20_000));'
new = 'return Math.min(600_000, Math.max(90_000, 20_000));'
assert old in s, 'latencyProbeTimeout anchor not found'
open(p, 'w', encoding='utf-8').write(s.replace(old, new, 1))
PYEOF
if bash "$GATE" "$MIR" > "$TMP/var5.log" 2>&1; then
  echo "FAIL: gate PASSED on the static growth-term variant — helper dynamic args still unread"
  exit 1
fi
grep -q 'latencyProbeTimeout' "$TMP/var5.log" || { echo "FAIL: gate failed but did not name latencyProbeTimeout:"; cat "$TMP/var5.log"; exit 1; }
echo "OK: variant caught, latencyProbeTimeout named"
cp "$ROOT/vite-frontend/src/api/index.ts" "$FE_API"

# ── R2-C6-02 回归变体：literal 后端界锚点漂移必须 FAIL 点名。
# kernel_external.go 的抓取腿界 20s→500s 后，be_literal_anchor 的
# 值钉 grep 必然失配、提取为空——旧口径下 strict 漂移比对「空即跳
# 过」、门全绿（真实界已是前端 60s 的 8 倍仍无人看见）；且
# T061/T062/T063 出厂时不在锚点 case 内、从未被验。本变体断言修
# 复后门必须 FAIL 且点名 T061 与「提取为空」fail-closed 结论。
echo "== variant R2-C6-02: kernel_external literal anchor drift 20s->500s must FAIL, naming T061 =="
KE_GO="$MIR/go-backend/internal/http/handler/kernel_external.go"
python3 - "$KE_GO" <<'PYEOF'
import sys
p = sys.argv[1]
s = open(p, encoding='utf-8').read()
old = 'context.WithTimeout(ctx, 20*time.Second)'
new = 'context.WithTimeout(ctx, 500*time.Second)'
assert old in s, 'kernel_external WithTimeout anchor not found'
open(p, 'w', encoding='utf-8').write(s.replace(old, new, 1))
PYEOF
if bash "$GATE" "$MIR" > "$TMP/var6.log" 2>&1; then
  echo "FAIL: gate PASSED on the literal-anchor drift variant — backend bound drift still invisible"
  exit 1
fi
grep -q 'T061' "$TMP/var6.log" || { echo "FAIL: gate failed but did not name T061:"; cat "$TMP/var6.log"; exit 1; }
grep -q '提取为空' "$TMP/var6.log" || { echo "FAIL: gate failure is not the fail-closed empty-extraction verdict:"; cat "$TMP/var6.log"; exit 1; }
echo "OK: variant caught, T061 named with fail-closed empty-extraction verdict"
cp "$ROOT/go-backend/internal/http/handler/kernel_external.go" "$MIR/go-backend/internal/http/handler/kernel_external.go"
echo "SELFTEST: PASS"
