#!/usr/bin/env bash
# check-agent-image-tools-selftest.sh — agent 镜像工具集门的变体
# 自检（AG-03）：把门禁所需两文件镜像到临时目录，先验证未变异
# 镜像 PASS，再分别变异一处工具包（删 procps）、一条入口不变量
# （改 entrypoint 的 cd 目标）与内核状态根锚点（删
# FLMX_KERNEL_DIR 的 ENV 行），断言门禁必须 FAIL 且点名该项。
# 若哪天断言规则被误删/改坏，本自检先红。
#
# 用法：check-agent-image-tools-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且两变体均被抓；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/check-agent-image-tools.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
MIR="$TMP/repo"

mkdir -p "$MIR/go-gost"
cp "$ROOT/go-gost/Dockerfile" "$MIR/go-gost/"
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/"

echo "== baseline: unmutated mirror must PASS =="
if bash "$GATE" "$MIR" > "$TMP/base.log" 2>&1; then
  echo "OK: baseline PASS"
else
  echo "FAIL: baseline did not pass"; sed 's/^/    /' "$TMP/base.log"; exit 1
fi

echo "== variant 1: drop procps from the apt-get install block must FAIL and name it =="
sed -i 's/ procps//' "$MIR/go-gost/Dockerfile"
if bash "$GATE" "$MIR" > "$TMP/v1.log" 2>&1; then
  echo "FAIL: variant 1 was not caught"; exit 1
fi
if grep -q "procps" "$TMP/v1.log"; then
  echo "OK: variant 1 caught and named"
else
  echo "FAIL: variant 1 caught but the missing package was not named"; sed 's/^/    /' "$TMP/v1.log"; exit 1
fi
cp "$ROOT/go-gost/Dockerfile" "$MIR/go-gost/Dockerfile"

echo "== variant 2: move the entrypoint cd target must FAIL and name the anchor =="
sed -i 's|cd /etc/flmx_agent|cd /|' "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v2.log" 2>&1; then
  echo "FAIL: variant 2 was not caught"; exit 1
fi
if grep -q 'cd /etc/flmx_agent' "$TMP/v2.log"; then
  echo "OK: variant 2 caught and named"
else
  echo "FAIL: variant 2 caught but the drifted anchor was not named"; sed 's/^/    /' "$TMP/v2.log"; exit 1
fi

echo "== variant 3: drop the kernel state anchor ENV must FAIL and name it =="
sed -i '/^ENV FLMX_KERNEL_DIR=/d' "$MIR/go-gost/Dockerfile"
if bash "$GATE" "$MIR" > "$TMP/v3.log" 2>&1; then
  echo "FAIL: variant 3 was not caught"; exit 1
fi
if grep -q 'FLMX_KERNEL_DIR' "$TMP/v3.log"; then
  echo "OK: variant 3 caught and named"
else
  echo "FAIL: variant 3 caught but the missing anchor was not named"; sed 's/^/    /' "$TMP/v3.log"; exit 1
fi

echo "SELFTEST PASS: baseline green, all variants caught and named"
exit 0
