#!/usr/bin/env bash
# check-private-ranges-selftest.sh — 私网段集门禁的变体自检
# （R8-004 / R8-022 回归门）：把门禁所需两文件镜像到临时目录，先验证
# 未变异镜像 PASS，再分别变异 v4 一段（面板拷贝的 CGNAT 前缀长度）
# 与 v6 一段（agent 拷贝的文档段），断言门禁必须 FAIL 且点名漂移段。
# 若哪天对账规则被误删/改坏，本自检先红。
#
# 用法：check-private-ranges-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且两变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-private-ranges.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"

mkdir -p "$MIR/go-backend/internal/kernel" "$MIR/go-gost/x/kernel"
cp "$ROOT/go-backend/internal/kernel/private_ranges.go" "$MIR/go-backend/internal/kernel/"
cp "$ROOT/go-gost/x/kernel/snell_guard.go" "$MIR/go-gost/x/kernel/"

echo "== baseline: unmutated mirror must PASS =="
if bash "$GATE" "$MIR" > "$TMP/base.log" 2>&1; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: panel copy CGNAT 100.64.0.0/10 -> /11 must FAIL and name the segment =="
sed -i 's|"100.64.0.0/10"|"100.64.0.0/11"|' "$MIR/go-backend/internal/kernel/private_ranges.go"
if bash "$GATE" "$MIR" > "$TMP/v1.log" 2>&1; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
if grep -q "100.64.0.0/1" "$TMP/v1.log"; then
  echo "OK: variant 1 caught and named"
else
  echo "FAIL: variant 1 caught but the drifted segment was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1
fi
cp "$ROOT/go-backend/internal/kernel/private_ranges.go" "$MIR/go-backend/internal/kernel/"

echo "== variant 2: agent copy 2001:db8::/32 -> /33 must FAIL and name the segment =="
sed -i 's|"2001:db8::/32"|"2001:db8::/33"|' "$MIR/go-gost/x/kernel/snell_guard.go"
if bash "$GATE" "$MIR" > "$TMP/v2.log" 2>&1; then
  echo "FAIL: variant 2 was not caught"; exit 1
fi
if grep -q "2001:db8::/3" "$TMP/v2.log"; then
  echo "OK: variant 2 caught and named"
else
  echo "FAIL: variant 2 caught but the drifted segment was not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1
fi
cp "$ROOT/go-gost/x/kernel/snell_guard.go" "$MIR/go-gost/x/kernel/snell_guard.go"

echo "== variant 3: comment out one panel CIDR line must FAIL and name the segment (R6-C4-05) =="
sed -i 's|^\t"198.18.0.0/15",|\t// "198.18.0.0/15",|' "$MIR/go-backend/internal/kernel/private_ranges.go"
if bash "$GATE" "$MIR" > "$TMP/v3.log" 2>&1; then
  echo "FAIL: variant 3 was not caught (commented-out CIDR still counted)"; exit 1
fi
if grep -q "198.18.0.0/15" "$TMP/v3.log"; then
  echo "OK: variant 3 caught and named"
else
  echo "FAIL: variant 3 caught but the segment was not named"; sed 's/^/    /' "$TMP/v3.log"; exit 1
fi
cp "$ROOT/go-backend/internal/kernel/private_ranges.go" "$MIR/go-backend/internal/kernel/"

echo "== variant 4: delete the ::ffff: derivation code (comments keep the word) must FAIL (R6-C4-06) =="
sed -i 's|fmt.Sprintf("::ffff:%s/%d"|fmt.Sprintf("%s/%d"|' "$MIR/go-backend/internal/kernel/private_ranges.go" "$MIR/go-gost/x/kernel/snell_guard.go"
if bash "$GATE" "$MIR" > "$TMP/v4.log" 2>&1; then
  echo "FAIL: variant 4 was not caught (comment mentions satisfied the mapped premise)"; exit 1
fi
if grep -q "IPv4-mapped" "$TMP/v4.log"; then
  echo "OK: variant 4 caught and named"
else
  echo "FAIL: variant 4 caught but the missing derivation was not named"; sed 's/^/    /' "$TMP/v4.log"; exit 1
fi

echo "== variant 5: rewrite one panel CIDR as a quoted-content raw string must FAIL and name the segment (R7 census) =="
cp "$ROOT/go-backend/internal/kernel/private_ranges.go" "$MIR/go-backend/internal/kernel/private_ranges.go"
cp "$ROOT/go-gost/x/kernel/snell_guard.go" "$MIR/go-gost/x/kernel/snell_guard.go"
sed -i 's|"198.18.0.0/15",|`"198.18.0.0/15"`,|' "$MIR/go-backend/internal/kernel/private_ranges.go"
if bash "$GATE" "$MIR" > "$TMP/v5.log" 2>&1; then
  echo "FAIL: variant 5 was not caught (raw-string phantom CIDR still counted)"; exit 1
fi
if grep -q "198.18.0.0/15" "$TMP/v5.log"; then
  echo "OK: variant 5 caught and named"
else
  echo "FAIL: variant 5 caught but the segment was not named"; sed 's/^/    /' "$TMP/v5.log"; exit 1
fi

echo "SELFTEST PASS"
