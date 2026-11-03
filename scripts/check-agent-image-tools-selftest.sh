#!/usr/bin/env bash
# check-agent-image-tools-selftest.sh — agent 镜像工具集门的变体
# 自检（AG-03）：把门禁所需两文件镜像到临时目录，先验证未变异
# 镜像 PASS，再分别变异一处工具包（删 procps）、一条入口不变量
# （改 entrypoint 的 cd 目标）与内核状态根锚点（删
# FLMX_KERNEL_DIR 的 ENV 行），断言门禁必须 FAIL 且点名该项。
# 若哪天断言规则被误删/改坏，本自检先红。
#
# 修复轮 7（R7-07）追加变体 6–8：锚行之后追加覆盖行（ENV 末值
# 覆盖、ulimit -c unlimited 覆盖、cd /tmp 覆盖），钉死三锚点的
# 生效值口径——存在性锚在追加形态下全绿即为自检红。
#
# 修复轮 8（R8-04）追加变体 9–13：子壳复合 exec 行与顶层复合
# exec 行（废值须点名）、设置子壳在 exec 前的对照（子壳赋值不
# 外泄、必须 PASS）、env 包装 exec（模型外形态须点名 FAIL）、
# 包名移入同块 echo 命令（安装跨度外token 不计数、须点名缺包）。
#
# 用法：check-agent-image-tools-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 基线 PASS 且全部变体均被抓；1 = 任一断言不成立。
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
cp "$ROOT/go-gost/Dockerfile" "$MIR/go-gost/Dockerfile"

echo "== variant 4: comment out the entrypoint ulimit line must FAIL and name it (R6-C4-07) =="
sed -i 's|^ulimit -c 0|# ulimit -c 0|' "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v4.log" 2>&1; then
  echo "FAIL: variant 4 was not caught (commented-out line still counted)"; exit 1
fi
if grep -q 'ulimit -c 0' "$TMP/v4.log"; then
  echo "OK: variant 4 caught and named"
else
  echo "FAIL: variant 4 caught but the missing line was not named"; sed 's/^/    /' "$TMP/v4.log"; exit 1
fi
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/docker-entrypoint.sh"

echo "== variant 5: comment out the ENV anchor must FAIL and name it (R6-C4-07) =="
sed -i 's|^ENV FLMX_KERNEL_DIR=|# ENV FLMX_KERNEL_DIR=|' "$MIR/go-gost/Dockerfile"
if bash "$GATE" "$MIR" > "$TMP/v5.log" 2>&1; then
  echo "FAIL: variant 5 was not caught (commented-out ENV still counted)"; exit 1
fi
if grep -q 'FLMX_KERNEL_DIR' "$TMP/v5.log"; then
  echo "OK: variant 5 caught and named"
else
  echo "FAIL: variant 5 caught but the missing anchor was not named"; sed 's/^/    /' "$TMP/v5.log"; exit 1
fi

cp "$ROOT/go-gost/Dockerfile" "$MIR/go-gost/Dockerfile"

echo "== variant 6: append an overriding ENV at the Dockerfile tail must FAIL and name it (R7-07) =="
printf '\nENV FLMX_KERNEL_DIR=/tmp/elsewhere\n' >> "$MIR/go-gost/Dockerfile"
if bash "$GATE" "$MIR" > "$TMP/v6.log" 2>&1; then
  echo "FAIL: variant 6 was not caught (appended ENV override still counted as anchored)"; exit 1
fi
if grep -q 'FLMX_KERNEL_DIR' "$TMP/v6.log"; then
  echo "OK: variant 6 caught and named"
else
  echo "FAIL: variant 6 caught but the overridden anchor was not named"; sed 's/^/    /' "$TMP/v6.log"; exit 1
fi
cp "$ROOT/go-gost/Dockerfile" "$MIR/go-gost/Dockerfile"

echo "== variant 7: append ulimit -c unlimited after the guard line must FAIL and name it (R7-07) =="
printf 'ulimit -c unlimited\n' >> "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v7.log" 2>&1; then
  echo "FAIL: variant 7 was not caught (appended ulimit override still counted as guarded)"; exit 1
fi
if grep -q 'ulimit -c 0' "$TMP/v7.log"; then
  echo "OK: variant 7 caught and named"
else
  echo "FAIL: variant 7 caught but the overridden guard was not named"; sed 's/^/    /' "$TMP/v7.log"; exit 1
fi
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/docker-entrypoint.sh"

echo "== variant 8: append cd /tmp after the anchor line must FAIL and name it (R7-07) =="
printf 'cd /tmp\n' >> "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v8.log" 2>&1; then
  echo "FAIL: variant 8 was not caught (appended cd override still counted as anchored)"; exit 1
fi
if grep -q 'cd /etc/flmx_agent' "$TMP/v8.log"; then
  echo "OK: variant 8 caught and named"
else
  echo "FAIL: variant 8 caught but the overridden anchor was not named"; sed 's/^/    /' "$TMP/v8.log"; exit 1
fi
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/docker-entrypoint.sh"

echo "== variant 9: subshell compound exec line must FAIL and name the effective values (R8-04) =="
sed -i 's|^exec .*|(cd /tmp \&\& ulimit -c unlimited \&\& exec /usr/local/bin/flmx-agent "$@")|' "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v9.log" 2>&1; then
  echo "FAIL: variant 9 was not caught (subshell compound exec still counted as guarded)"; exit 1
fi
if grep -q '/tmp' "$TMP/v9.log" && grep -q 'unlimited' "$TMP/v9.log"; then
  echo "OK: variant 9 caught and named"
else
  echo "FAIL: variant 9 caught but the effective values were not named"; sed 's/^/    /' "$TMP/v9.log"; exit 1
fi
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/docker-entrypoint.sh"

echo "== variant 10: top-level compound exec line must FAIL and name the effective values (R8-04) =="
sed -i 's|^exec .*|cd /tmp \&\& ulimit -c unlimited \&\& exec /usr/local/bin/flmx-agent "$@\"|' "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v10.log" 2>&1; then
  echo "FAIL: variant 10 was not caught (compound exec line still counted as guarded)"; exit 1
fi
if grep -q '/tmp' "$TMP/v10.log" && grep -q 'unlimited' "$TMP/v10.log"; then
  echo "OK: variant 10 caught and named"
else
  echo "FAIL: variant 10 caught but the effective values were not named"; sed 's/^/    /' "$TMP/v10.log"; exit 1
fi
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/docker-entrypoint.sh"

echo "== variant 11: setup subshell before the exec line must still PASS (R8-04 control) =="
sed -i 's|^exec .*|(cd /tmp \&\& ulimit -c unlimited)\nexec /usr/local/bin/flmx-agent "$@\"|' "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v11.log" 2>&1; then
  echo "OK: variant 11 control PASS (subshell assignments do not leak)"
else
  echo "FAIL: variant 11 control did not pass (subshell assignments wrongly counted as effective)"; sed 's/^/    /' "$TMP/v11.log"; exit 1
fi
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/docker-entrypoint.sh"

echo "== variant 12: env-wrapped exec must FAIL as outside the command-flow model (R8-04) =="
sed -i 's|^exec .*|env FLMX_PROBE=1 exec /usr/local/bin/flmx-agent "$@\"|' "$MIR/go-gost/docker-entrypoint.sh"
if bash "$GATE" "$MIR" > "$TMP/v12.log" 2>&1; then
  echo "FAIL: variant 12 was not caught (wrapped exec silently accepted)"; exit 1
fi
if grep -q 'command-flow model' "$TMP/v12.log"; then
  echo "OK: variant 12 caught and named"
else
  echo "FAIL: variant 12 caught but the unparseable form was not named"; sed 's/^/    /' "$TMP/v12.log"; exit 1
fi
cp "$ROOT/go-gost/docker-entrypoint.sh" "$MIR/go-gost/docker-entrypoint.sh"

echo "== variant 13: package name moved into an echo in the same block must FAIL and name it (R8-04, ex R-11) =="
sed -i 's| iperf3 \\| \\|' "$MIR/go-gost/Dockerfile"
sed -i 's|&& rm -rf|\&\& echo iperf3 \&\& rm -rf|' "$MIR/go-gost/Dockerfile"
if bash "$GATE" "$MIR" > "$TMP/v13.log" 2>&1; then
  echo "FAIL: variant 13 was not caught (package name in echo still counted as installed)"; exit 1
fi
if grep -q 'iperf3' "$TMP/v13.log"; then
  echo "OK: variant 13 caught and named"
else
  echo "FAIL: variant 13 caught but the missing package was not named"; sed 's/^/    /' "$TMP/v13.log"; exit 1
fi
cp "$ROOT/go-gost/Dockerfile" "$MIR/go-gost/Dockerfile"

echo "SELFTEST PASS: baseline green, all variants caught and named"
exit 0
