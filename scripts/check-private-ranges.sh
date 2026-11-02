#!/usr/bin/env bash
# check-private-ranges.sh — 私网拦截正本段集跨模块对账（R8-004 / R8-022
# 常设门）。与 check-doc-consistency.sh 同 CI 步（该脚本第 12 项转发
# 本脚本结论）。
#
# 产品正本段集（flmx-no-private：节点侧出口守卫必须拦截的非公网段）
# 有两处实现拷贝，分属两个 Go 模块、无法共享常量：
#   - go-backend/internal/kernel/private_ranges.go
#       guardPrivateCIDRsV4 / guardPrivateCIDRsV6（面板渲染 sing-box
#       ip_cidr 守卫规则，字面量与域名两形态共用）
#   - go-gost/x/kernel/snell_guard.go
#       snellPrivateV4 / snellPrivateV6（agent 的 Snell nft 丢弃集）
# 本脚本从两处源码机械提取段集字面量逐段对账，任一段漂移即 FAIL 并
# 点名该段；并核两边都保留 IPv4-mapped 形式（::ffff:…）的派生（段集
# 本体之外的第二处同族拷贝面）。变体自检见
# scripts/check-private-ranges-selftest.sh。
#
# 背景：sing-box 守卫曾用 ip_is_private 谓词，其 netip 段集缺 CGNAT
# 100.64.0.0/10 与基准段 198.18.0.0/15，与 Snell 正本不一致而静默
# 放行（R8-004 / R8-022）；改显式段集后，本门防两处拷贝再次漂移。
#
# 用法：check-private-ranges.sh [REPO_ROOT]（默认脚本所在仓库根）。
# 退出码：0 = 两处段集逐段一致；1 = 存在漂移；2 = 前提失效。
set -u

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
BE="$ROOT/go-backend/internal/kernel/private_ranges.go"
AG="$ROOT/go-gost/x/kernel/snell_guard.go"

FAIL=0
fail() { echo "FAIL: $*"; FAIL=1; }
ok()   { echo "OK:   $*"; }

for f in "$BE" "$AG"; do
  [ -f "$f" ] || { echo "FAIL: 前提失效，文件不存在：$f"; exit 2; }
done

# extract <file> <varname>：打印 var <varname> = []string{ ... } 块内
# 全部 CIDR 形字符串字面量（去重排序）。注释里的引号串（如
# "this host on this network"）不合 CIDR 形态，自然滤除。
extract() {
  awk -v v="$2" '
    index($0, "var " v " = []string{") { inblk = 1; next }
    inblk && /^}/ { inblk = 0 }
    inblk { print }
  ' "$1" | grep -oE '"[^"]+"' | tr -d '"' | grep -E '^[0-9a-fA-F:.]+/[0-9]+$' | sort -u
}

cmp_family() {
  local name="$1" be ag
  be="$(extract "$BE" "$2")"
  ag="$(extract "$AG" "$3")"
  if [ -z "$be" ] || [ -z "$ag" ]; then
    fail "$name 段集提取为空（前提失效：变量改名或字面量形态变化）"
    return
  fi
  if [ "$be" = "$ag" ]; then
    ok "$name 段集两处一致（$(printf '%s\n' "$be" | wc -l) 段）"
  else
    fail "$name 段集漂移（< 仅面板 sing-box 拷贝 / > 仅 agent Snell 拷贝）："
    diff <(printf '%s\n' "$be") <(printf '%s\n' "$ag") | sed 's/^/    /'
  fi
}

cmp_family "IPv4" guardPrivateCIDRsV4 snellPrivateV4
cmp_family "IPv6" guardPrivateCIDRsV6 snellPrivateV6

# mapped 派生：两边都必须由 v4 段集派生 ::ffff: 形式（函数名可不同，
# 派生字面量必须在）。
for f in "$BE" "$AG"; do
  if grep -q '::ffff:' "$f"; then
    ok "$f 保留 IPv4-mapped 派生"
  else
    fail "$f 缺失 IPv4-mapped（::ffff:）派生，mapped 形态将绕过 v4 段集"
  fi
done

n4="$(extract "$BE" guardPrivateCIDRsV4 | wc -l)"
n6="$(extract "$BE" guardPrivateCIDRsV6 | wc -l)"
echo "private-ranges: $((n4 + n6)) segments (v4 $n4 + v6 $n6; mapped forms derived on both sides)"
exit "$FAIL"
