#!/usr/bin/env bash
# dual-push-legacy.sh — 改名过渡双推的机械止点判定（A4-05 / XC4-B2）。
#
# 旧仓 flmx-flux-panel-backend 随 1.9.8 起与新仓双推、至 1.9.10 止
# （含），1.9.11 起停推。判定原本只是 docker-build.yml check-version
# 作业里的一段内联 bash：改坏（比较方向、止点数字、剥后缀规则）无
# 任何本地可跑的门会红，只能等真发版暴露（XC-4 差集 B-2）。现抽到
# 本脚本成为唯一判定源：workflow 只负责调用并把结论写进
# dual_push_legacy 输出；scripts/dual-push-legacy-selftest.sh 钉住
# 边界格（止点含/止点+1/后缀形态/大版本跳变）。
#
# 口径：待发版本剥 -beta 等后缀取数字核心，与止点按三段逐位数字
# 比较（缺段补 0、段内非数字字符剥除），≤ 止点为 true（双推）。
#
# 用法：bash scripts/dual-push-legacy.sh <VERSION>
# 输出：stdout 一行 true|false。版本为空或不以数字开头时报错退出 2
# （fail-closed：调用方不得在判定失败时默认双推或默认停推）。
set -u

CUTOFF_MAJOR=1
CUTOFF_MINOR=9
CUTOFF_PATCH=10

version="${1:-}"
if [ -z "$version" ]; then
  echo "dual-push-legacy: 版本号为空" >&2
  exit 2
fi

core="${version%%-*}"
IFS='.' read -r -a parts <<< "$core"
cmp=0
cutoff=("$CUTOFF_MAJOR" "$CUTOFF_MINOR" "$CUTOFF_PATCH")
for i in 0 1 2; do
  n="${parts[$i]:-0}"
  n="${n//[!0-9]/}"
  n="${n:-0}"
  if [ "$n" -gt "${cutoff[$i]}" ]; then cmp=1; break; fi
  if [ "$n" -lt "${cutoff[$i]}" ]; then cmp=-1; break; fi
done

if [ "$cmp" -le 0 ]; then
  echo "true"
else
  echo "false"
fi
