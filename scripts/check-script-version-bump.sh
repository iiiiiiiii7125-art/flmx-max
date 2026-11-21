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
# 在发版路径上执行本门。G-R4-05 下半：本门同时断言仓库树内
# install.sh / panel_install.sh 的 PINNED_VERSION 保持空占位——
# 发版时 docker-build 以 sed 注入版本号再 grep 兜底；占位若在
# 日常提交里被误填，sed 匹配不上、要等到发版才拦。CI 路径上
# 的本作业（ci-build script-version-bump）使两半都在日常提交
# 即查。CI 路径的 wiring 断言见本门自检。
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

# G-R4-05 下半：PINNED_VERSION 空占位断言（树态，HEAD ref）。
for f in install.sh panel_install.sh; do
  line="$(git show "$head_ref:$f" 2>/dev/null | grep -m1 '^PINNED_VERSION=' || true)"
  [ -n "$line" ] || {
    echo "FAIL  $f 缺少 PINNED_VERSION 占位行（发版 sed 注入无锚点），fail-closed"
    exit 1
  }
  val="$(printf '%s' "$line" | sed 's/^PINNED_VERSION=//; s/^"//; s/"$//')"
  [ -z "$val" ] || {
    echo "FAIL  $f 的 PINNED_VERSION 占位已被填值（\"$val\"）：仓库树内须保持空占位，发版 sed 才能注入版本号（G-R4-05）"
    exit 1
  }
done
echo "script-version-bump: PINNED_VERSION 占位在场且为空（install.sh / panel_install.sh）"

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

commit_touches_body() {
  # $1 = commit。返回 0 = 该 commit 的 panel_install.sh diff 含正文行
  # （SCRIPT_VERSION 行除外）；只含印记行或无 +/- 行时返回 1。
  git show --format= --unified=0 "$1" -- "$SCRIPT_FILE" 2>/dev/null |
    grep '^[-+][^-+]' | cut -c2- | grep -vq '^SCRIPT_VERSION='
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
  # R18-R3-A1-02：两端比较只看净值，盖不住跨 commit 的情形——
  # bf6d4ebb 改本体未同 commit bump，却被后来的 43216b7b 的 bump
  # 在净值上掩盖。补一条覆盖检查：取区间内最近一次改动正文的
  # 非 merge commit L，要求 HEAD 印记严格大于 L 的父提交印记，
  # 即 L 之后（含自身）至少有一次 bump；L 之后再改本体不 bump
  # 即红。merge commit 不计入（其内容来自被合入分支的各 commit；
  # 合入冲突解出的正文改动不在此检查内，仍由两端净值兜底）。
  latest_body=""
  while IFS= read -r c; do
    [ -n "$c" ] || continue
    if commit_touches_body "$c"; then
      latest_body="$c"
      break
    fi
  done <<EOF
$(git rev-list --no-merges "$base_ref..$head_ref" -- "$SCRIPT_FILE")
EOF
  if [ -n "$latest_body" ]; then
    parent_stamp=""
    if git rev-parse --verify --quiet "$latest_body^^{commit}" >/dev/null; then
      parent_stamp="$(git show "$latest_body^:$SCRIPT_FILE" 2>/dev/null | grep -m1 '^SCRIPT_VERSION=' | sed 's/^SCRIPT_VERSION=//; s/^"//; s/"$//')"
    fi
    if [ -z "$parent_stamp" ] || [ "$head_stamp" \> "$parent_stamp" ]; then
      echo "script-version-bump: 最近一次正文改动 $latest_body 已被其后 bump 覆盖（父提交印记 ${parent_stamp:-无} → $head_stamp）"
    else
      echo "FAIL  $SCRIPT_FILE 最近一次正文改动 $latest_body 之后印记未再递增（父提交印记 $parent_stamp → HEAD $head_stamp）：改本体的 commit 须同 commit bump SCRIPT_VERSION（R18-R3-A1-02；bf6d4ebb 同类再犯）"
      echo "RESULT: FAIL"
      exit 1
    fi
  fi
  echo "RESULT: PASS"
  exit 0
fi

echo "FAIL  $SCRIPT_FILE 正文相对上一发版 $base_ref 已变更，但 SCRIPT_VERSION 未递增（$base_stamp → $head_stamp）：存量副本自更新会被相等短路吞掉（R4-F7-01）"
echo "RESULT: FAIL"
exit 1
