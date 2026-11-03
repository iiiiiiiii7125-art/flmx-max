#!/usr/bin/env bash
# check-private-ranges-selftest.sh — 私网段集门禁的变体自检
# （R8-004 / R8-022 回归门）：把门禁所需文件镜像到临时目录，先验证
# 未变异镜像 PASS，再分别变异 v4 一段（面板拷贝的 CGNAT 前缀长度）
# 与 v6 一段（agent 拷贝的文档段），断言门禁必须 FAIL 且点名漂移段。
# 若哪天对账规则被误删/改坏，本自检先红。
# 变体 6–9（R3-C6-01）：netguard 谱系对账门——谱系一面单侧加段、
# 规范集与 Snell 拷贝同步删一文档段（第一部分保持一致、仅第二
# 部分差集形态可抓）、socket 保留段漂移、bypass 翻译段漂移。
#
# 用法：check-private-ranges-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且全部变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-private-ranges.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"

mkdir -p "$MIR/go-backend/internal/kernel" "$MIR/go-gost/x/kernel" \
  "$MIR/go-backend/internal/netguard" "$MIR/go-gost/x/bypass" "$MIR/go-gost/x/socket"
cp "$ROOT/go-backend/internal/kernel/private_ranges.go" "$MIR/go-backend/internal/kernel/"
cp "$ROOT/go-gost/x/kernel/snell_guard.go" "$MIR/go-gost/x/kernel/"
cp "$ROOT/go-backend/internal/netguard/netguard.go" "$MIR/go-backend/internal/netguard/"
cp "$ROOT/go-gost/x/bypass/flmx_no_private.go" "$MIR/go-gost/x/bypass/"
cp "$ROOT/go-gost/x/socket/websocket_reporter.go" "$MIR/go-gost/x/socket/"

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
cp "$ROOT/go-backend/internal/kernel/private_ranges.go" "$MIR/go-backend/internal/kernel/private_ranges.go"

echo "== variant 6: netguard lineage gains one segment one-sidedly (203.0.113.0/24) must FAIL and name it (R3-C6-01) =="
sed -i 's|^\t{netip.MustParsePrefix("240.0.0.0/4"), ClassReserved},|\t{netip.MustParsePrefix("240.0.0.0/4"), ClassReserved},\n\t{netip.MustParsePrefix("203.0.113.0/24"), ClassReserved},|' "$MIR/go-backend/internal/netguard/netguard.go"
if bash "$GATE" "$MIR" > "$TMP/v6.log" 2>&1; then
  echo "FAIL: variant 6 was not caught (one-sided lineage segment addition passed)"; exit 1
fi
if grep -q "203.0.113.0/24" "$TMP/v6.log"; then
  echo "OK: variant 6 caught and named"
else
  echo "FAIL: variant 6 caught but the segment was not named"; sed 's/^/    /' "$TMP/v6.log"; exit 1
fi
cp "$ROOT/go-backend/internal/netguard/netguard.go" "$MIR/go-backend/internal/netguard/"

echo "== variant 7: canonical and Snell copies both drop one documentation segment (192.0.2.0/24) must FAIL via the adjudicated diff (R3-C2-01) =="
sed -i '/"192.0.2.0\/24"/d' "$MIR/go-backend/internal/kernel/private_ranges.go" "$MIR/go-gost/x/kernel/snell_guard.go"
if bash "$GATE" "$MIR" > "$TMP/v7.log" 2>&1; then
  echo "FAIL: variant 7 was not caught (adjudicated documentation-segment diff changed silently)"; exit 1
fi
if grep -q "192.0.2.0/24" "$TMP/v7.log"; then
  echo "OK: variant 7 caught and named"
else
  echo "FAIL: variant 7 caught but the segment was not named"; sed 's/^/    /' "$TMP/v7.log"; exit 1
fi
cp "$ROOT/go-backend/internal/kernel/private_ranges.go" "$MIR/go-backend/internal/kernel/"
cp "$ROOT/go-gost/x/kernel/snell_guard.go" "$MIR/go-gost/x/kernel/snell_guard.go"

echo "== variant 8: socket reserved-table drift (240.0.0.0/4 -> /5) must FAIL and name it (R3-C6-01) =="
sed -i 's|netip.MustParsePrefix("240.0.0.0/4")|netip.MustParsePrefix("240.0.0.0/5")|' "$MIR/go-gost/x/socket/websocket_reporter.go"
if bash "$GATE" "$MIR" > "$TMP/v8.log" 2>&1; then
  echo "FAIL: variant 8 was not caught (socket reserved-table drift passed)"; exit 1
fi
if grep -q "240.0.0.0" "$TMP/v8.log"; then
  echo "OK: variant 8 caught and named"
else
  echo "FAIL: variant 8 caught but the segment was not named"; sed 's/^/    /' "$TMP/v8.log"; exit 1
fi
cp "$ROOT/go-gost/x/socket/websocket_reporter.go" "$MIR/go-gost/x/socket/"

echo "== variant 9: bypass translation prefix drift (2002::/16 -> /17) must FAIL and name it (R3-C6-01) =="
sed -i 's|"2002::/16"|"2002::/17"|' "$MIR/go-gost/x/bypass/flmx_no_private.go"
if bash "$GATE" "$MIR" > "$TMP/v9.log" 2>&1; then
  echo "FAIL: variant 9 was not caught (bypass translation-prefix drift passed)"; exit 1
fi
if grep -q "2002::" "$TMP/v9.log"; then
  echo "OK: variant 9 caught and named"
else
  echo "FAIL: variant 9 caught but the drifted prefix was not named"; sed 's/^/    /' "$TMP/v9.log"; exit 1
fi
cp "$ROOT/go-gost/x/bypass/flmx_no_private.go" "$MIR/go-gost/x/bypass/"

echo "SELFTEST PASS"
