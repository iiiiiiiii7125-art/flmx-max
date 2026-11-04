#!/usr/bin/env bash
# check-script-version-bump.sh — panel_install.sh 印记联动门（R4-F7-01）。
#
# 机制：panel_install.sh 的自更新以 SCRIPT_VERSION 相等短路
# （印记相同即视为已是最新）。凡正文（剔除 SCRIPT_VERSION 行）
# 相对上一发版 tag 有变更、而印记未严格递增的发版，未定版存量
# 副本永远收不到这次脚本修复——RR-F7-01 正是全门绿地漏 bump。
# 树态门无从判定（无基线），按 R4-C6-I01 的口径此联动只能在
# CI 的 diff 侧机械化：本门比对两个 git ref（默认：上一发版
# tag → HEAD），正文有差异时要求印记严格递增，否则点名红。
#
# 用法：
#   bash scripts/check-script-version-bump.sh [BASE_REF [HEAD_REF]]
# BASE_REF 缺省 = 候选集（数字开头 tag、排除 HEAD 自身 tag）中
# 版本序最大且为 HEAD 祖先者（无祖先时取版本序最大者）；HEAD_REF
# 缺省 = HEAD。无任何候选 tag（首个发版）时 NOTE 放行。
# docker-build 的 check-version 作业（fetch-depth: 0、tag 齐全）
# 在发版路径上执行本门。
set -u
export LC_ALL=C
cd "$(git rev-parse --show-toplevel)" || exit 1

SCRIPT_FILE="panel_install.sh"

base_ref="${1:-}"
head_ref="${2:-HEAD}"

git rev-parse --verify --quiet "$head_ref^{commit}" >/dev/null || {
  echo "FAIL  HEAD_REF「$head_ref」不可解析"
  exit 1
}

if [ -z "$base_ref" ]; then
  cur_tag="$(git describe --tags --exact-match "$head_ref" 2>/dev/null || true)"
  best=""
  best_any=""
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    [ "$t" = "$cur_tag" ] && continue
    best_any="$t" # sort -V 升序迭代，最后一个候选即版本序最大者
    if git merge-base --is-ancestor "$t" "$head_ref" 2>/dev/null; then
      best="$t" # 最后一个祖先即版本序最大祖先
    fi
  done <<EOF
$(git tag --list '[0-9]*' | sort -V)
EOF
  # RR4-C4-01/C6-05：旧实现以 elif 在 best 为空时存入第一个（最小）
  # tag，无祖先时最终保留最小者、与门头自述相反，对着最旧印记
  # 比较近乎恒真。现分开追踪：有祖先取最大祖先，无祖先回退取
  # 版本序最大者（best_any），与自述一致。
  base_ref="${best:-$best_any}"
fi

if [ -z "$base_ref" ]; then
  echo "NOTE  无上一发版 tag 可比对（首个发版），印记联动门放行"
  echo "RESULT: PASS"
  exit 0
fi

stamp_at() {
  # 提取口径与 check-doc-consistency.sh §11 统一（RR4-C6-I01）：
  # 认 ^SCRIPT_VERSION= 前缀行、值剥可选双引号——无引号形态不再
  # 一门当印记行、另一门视而不见。
  git show "$1:$SCRIPT_FILE" 2>/dev/null | grep -m1 '^SCRIPT_VERSION=' | sed 's/^SCRIPT_VERSION=//; s/^"//; s/"$//'
}
body_hash() {
  git show "$1:$SCRIPT_FILE" 2>/dev/null | grep -v '^SCRIPT_VERSION=' | git hash-object --stdin
}

base_stamp="$(stamp_at "$base_ref")"
head_stamp="$(stamp_at "$head_ref")"
[ -n "$base_stamp" ] && [ -n "$head_stamp" ] || {
  echo "FAIL  SCRIPT_VERSION 提取为空（base=$base_ref:${base_stamp:-空} head=$head_ref:${head_stamp:-空}），fail-closed"
  exit 1
}

if [ "$(body_hash "$base_ref")" = "$(body_hash "$head_ref")" ]; then
  echo "script-version-bump: $SCRIPT_FILE 正文相对 $base_ref 无变更（印记 $base_stamp → $head_stamp），无需递增"
  echo "RESULT: PASS"
  exit 0
fi

# 印记为定宽 YYYYMMDD.NN，字典序即版本序。
if [ "$head_stamp" \> "$base_stamp" ]; then
  echo "script-version-bump: 正文相对 $base_ref 有变更，印记已递增 $base_stamp → $head_stamp"
  echo "RESULT: PASS"
  exit 0
fi

echo "FAIL  $SCRIPT_FILE 正文相对上一发版 $base_ref 已变更，但 SCRIPT_VERSION 未递增（$base_stamp → $head_stamp）：存量副本自更新会被相等短路吞掉（R4-F7-01）"
echo "RESULT: FAIL"
exit 1
