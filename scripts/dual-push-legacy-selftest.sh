#!/usr/bin/env bash
# dual-push-legacy-selftest.sh — 双推止点判定的边界自检（XC4-B2）。
#
# 背景：双推截止（≤2.0.0）原本只是 docker-build.yml 内联 bash，
# 无自测覆盖，判定改坏要等真发版才暴露。判定抽成
# scripts/dual-push-legacy.sh 后，本自检钉住边界格，并核 workflow
# 确实调用该脚本（删掉调用/改回内联，本自检先红）。
#
# 用法：dual-push-legacy-selftest.sh（无参数，作用于本仓库）。
# 退出码：0 = 全部断言成立；1 = 任一断言不成立。
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DECIDE="$ROOT/scripts/dual-push-legacy.sh"
WF="$ROOT/.github/workflows/docker-build.yml"

fail=0
expect() { # $1=version $2=expected
  got="$(bash "$DECIDE" "$1")"
  if [ "$got" = "$2" ]; then
    echo "OK: $1 -> $got"
  else
    echo "FAIL: $1 -> $got (expected $2)"; fail=1
  fi
}

echo "== boundary grid =="
expect "1.9.8"        true   # 双推窗口起点一带
expect "1.9.9"        true   # 双推第 1 个版本
expect "1.9.10"       true   # 原止点（改号后落入窗口内）
expect "1.9.11"       true   # 原停推点（随止点后移入窗）
expect "1.10.0"       true   # 窗口内（≤2.0.0 一律双推）
expect "2.0.0"        true   # 止点本身（含）：双推第 2 个版本
expect "2.0.0-beta"   true   # 后缀剥除后等于止点
expect "2.0.1"        false  # 止点+1：停推
expect "2.0.1-rc1"    false
expect "2.1.0"        false  # 次版本进位
expect "3.0.0"        false  # 大版本跳变
expect "1.9"          true   # 缺段补 0 → 1.9.0 ≤ 止点

echo "== fail-closed on empty version =="
if bash "$DECIDE" "" > /dev/null 2>&1; then
  echo "FAIL: empty version must exit non-zero"; fail=1
else
  echo "OK: empty version rejected"
fi

echo "== workflow wiring: docker-build.yml must call the script and gate both legacy tags on its output =="
if grep -q 'scripts/dual-push-legacy.sh' "$WF"; then
  echo "OK: workflow calls scripts/dual-push-legacy.sh"
else
  echo "FAIL: docker-build.yml does not call scripts/dual-push-legacy.sh"; fail=1
fi
# 旧仓版本 tag 与 :latest 两个 TAGS+= 必须都落在 dual_push_legacy
# 条件内（FX-F 4903bad1 的门形）：数条件块内的旧仓推送行。
legacy_pushes="$(grep -c 'flmx-flux-panel-backend' "$WF" || true)"
if [ "$legacy_pushes" -ge 2 ]; then
  echo "OK: legacy repo still referenced in workflow ($legacy_pushes lines, census only)"
else
  echo "FAIL: legacy repo references dropped below census ($legacy_pushes) — dual-push block may have been deleted instead of gated"; fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "SELFTEST FAIL"; exit 1
fi
echo "SELFTEST PASS"
