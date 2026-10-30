#!/usr/bin/env bash
# check-timeout-budgets-selftest.sh — 超时预算门禁的变体自检（B05-001
# 回归门）：把门禁脚本所需文件镜像到临时目录，先验证未变异镜像 PASS，
# 再把 C080 行变异回当年的误分类形态（check=class、后端界改回 class
# 天花板 12s，其余字段——尤其前端真实值 150s——保持不动，使 class
# 分支自身的断言全部满足），断言门禁必须 FAIL 且点名 C080 与
# B05-001 规则。若哪天「版本发现禁登 class」规则被误删/改坏，本
# 自检先红。
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
echo "SELFTEST: PASS"
