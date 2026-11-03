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
# 提取面（修复轮 6，R6-C4-05/06 机制处置）：旧实现直接在原文上提
# 取，注释掉一行 CIDR 该段仍被计数（门报 14 段一致、运行时实为
# 13），mapped 前提 grep 也会被既存注释里的 ::ffff: 字面量满足。
# 现两文件先经词法剥注释（awk 状态机：//、/* */、字符串/原始字
# 符串/符文字面量内容保留）落临时副本，段集提取与 mapped 前提均
# 只对剥离后的代码面判定；注释形态自本轮起与删除同效。自检新增
# 两变体钉死此口径（注释掉一段必红、删派生代码仅留注释必红）。
# 修复轮 7 普查追加：剥离副本再经 raw_blank 消隐原始字符串内容
# （两文件本无原始字符串），防引号 CIDR 文本藏进原始字符串冒充
# 段集元素；自检变体 5 钉死。
#
# 背景：sing-box 守卫曾用 ip_is_private 谓词，其 netip 段集缺 CGNAT
# 100.64.0.0/10 与基准段 198.18.0.0/15，与 Snell 正本不一致而静默
# 放行（R8-004 / R8-022）；改显式段集后，本门防两处拷贝再次漂移。
#
# 第二部分（文末，R3-C6-01 / R3-C2-01）：netguard 分类器谱系对账。
# 谱系三面——go-backend/internal/netguard/netguard.go（后端正本）、
# go-gost/x/bypass/flmx_no_private.go（agent 镜像）、
# go-gost/x/socket/websocket_reporter.go 的 tcpPing* 部分镜像——此前
# 只有文件头互注同步义务、没有任何门执行（R3-C6-01）；且谱系与本
# 规范集之间存在一处未登记分歧：规范集含 4 个文档段（192.0.2.0/24、
# 198.51.100.0/24、203.0.113.0/24、2001:db8::/32），谱系判其为公网
# （R3-C2-01）。R3 裁定（乙）：分歧属定义层、文档段不可路由风险
# 为零，且导入/分享/探测面要接受字面文档段作占位目标、全仓测试
# 夹具以文档段作标准公网假地址——分类不动，分歧登记并由本门机械
# 钉死。比对分层（只比共担层，部分镜像不假装持有全集）：
#   T 翻译层：NAT64 知名/本地、6to4 三前缀＋169.254/16 链路本地段，
#     三面逐角色全等；
#   M 元数据层：云元数据精确集（3 地址），三面全等；
#   S 段集层：各面「显式段表 ∪ 谓词展开」有效集对账——谓词展开按
#     本门钉住的谓词库存与展开表（Go 1.25 stdlib 语义）计算，库存
#     增减即红、须人工更新本门；断言 bypass 有效集 == netguard 有
#     效集、规范集 − 谱系 == 恰 4 文档段、谱系 − 规范集 == ∅；
#     socket 部分镜像只比其共担子层：保留段表 == netguard 段表中
#     ClassReserved 段集（其拒绝谓词库存另行钉住）；
#   R 残差层：netguard 与 socket 的末端 !IsGlobalUnicast 兜底必须
#     在位（bypass 无此兜底为已登记结构差异——其有效集与 netguard
#     逐段全等，且现行 stdlib 语义下该兜底在枚举层之外从不触发：
#     枚举外特殊段 IsGlobalUnicast 恒为 true，netguard 包探针实证）。
# 三处登记同文：本节、netguard.go 文件头、private_ranges.go 文件头。
#
# 用法：check-private-ranges.sh [REPO_ROOT]（默认脚本所在仓库根）。
# 退出码：0 = 全部对账一致；1 = 存在漂移；2 = 前提失效。
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

# go_strip：Go 词法剥注释（状态机跨行携带块注释/原始字符串状态；
# 字符串、原始字符串、符文字面量内容原样保留，注释整体丢弃）。
go_strip() {
  awk '
    BEGIN { st = 0; q = sprintf("%c", 39) }
    {
      line = $0; out = ""; n = length(line)
      for (i = 1; i <= n; i++) {
        c = substr(line, i, 1); nx = substr(line, i + 1, 1)
        if (st == 0) {
          if (c == "/" && nx == "/") { st = 1; i++; continue }
          if (c == "/" && nx == "*") { st = 2; i++; continue }
          if (c == "\"") st = 3
          else if (c == "`") st = 4
          else if (c == q) st = 5
          out = out c
        } else if (st == 1) {
          break
        } else if (st == 2) {
          if (c == "*" && nx == "/") { st = 0; i++ }
        } else if (st == 3) {
          out = out c
          if (c == "\\") { out = out nx; i++ }
          else if (c == "\"") st = 0
        } else if (st == 4) {
          out = out c
          if (c == "`") st = 0
        } else if (st == 5) {
          out = out c
          if (c == "\\") { out = out nx; i++ }
          else if (c == q) st = 0
        }
      }
      if (st == 1) st = 0
      print out
    }'
}

# raw_blank：把原始字符串（反引号）的内容逐字替换为空格（换行
# 保留）。go_strip 按词法保留原始字符串内容——段集提取的
# '"…"' grep 与 mapped 前提 grep 都会把原始字符串里引号包裹的
# CIDR 形文本当作元素/派生在位：把某段元素改写成内容为带引号
# CIDR 的原始字符串，运行时该元素是带引号的无效段、门却仍计
# 数一致（修复轮 7 普查登记的同族残面，R7-27 机制在 doc 门之
# 外的第二落点）。本门两文件不含任何原始字符串（段集元素与派
# 生均以解释型字符串写出），故对剥注释副本再消隐原始字符串内
# 容，提取面只剩代码与解释型字面量。输入已剥注释，只需跟踪
# 字符串/符文/原始字符串三态。
raw_blank() {
  awk '
    BEGIN { st = 0; q = sprintf("%c", 39) }
    {
      line = $0; out = ""; n = length(line)
      for (i = 1; i <= n; i++) {
        c = substr(line, i, 1); nx = substr(line, i + 1, 1)
        if (st == 0) {
          if (c == "\"") st = 3
          else if (c == "`") { st = 4; out = out " "; continue }
          else if (c == q) st = 5
          out = out c
        } else if (st == 3) {
          out = out c
          if (c == "\\") { out = out nx; i++ }
          else if (c == "\"") st = 0
        } else if (st == 4) {
          if (c == "`") { st = 0; out = out " " } else out = out " "
        } else if (st == 5) {
          out = out c
          if (c == "\\") { out = out nx; i++ }
          else if (c == q) st = 0
        }
      }
      print out
    }'
}

# 后续全部判定只对剥注释＋消隐原始字符串后的代码面副本进行。
BE_S="$(mktemp)"; AG_S="$(mktemp)"
trap 'rm -f "$BE_S" "$AG_S"' EXIT
go_strip < "$BE" | raw_blank > "$BE_S"
go_strip < "$AG" | raw_blank > "$AG_S"

# extract <file> <varname>：打印 var <varname> = []string{ ... } 块内
# 全部 CIDR 形字符串字面量（去重排序）。输入已剥注释，注释掉的段
# 行不在提取面内。
extract() {
  awk -v v="$2" '
    index($0, "var " v " = []string{") { inblk = 1; next }
    inblk && /^}/ { inblk = 0 }
    inblk { print }
  ' "$1" | grep -oE '"[^"]+"' | tr -d '"' | grep -E '^[0-9a-fA-F:.]+/[0-9]+$' | sort -u
}

cmp_family() {
  local name="$1" be ag
  be="$(extract "$BE_S" "$2")"
  ag="$(extract "$AG_S" "$3")"
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
# 派生字面量必须在代码面在位——只对剥注释后的副本判定，注释里的
# ::ffff: 提及不算数；派生行为的逐格正确性另由后端
# TestSnellMappedSetDerivedFromV4 与私网实拨测试钉住）。
for pair in "$BE:$BE_S" "$AG:$AG_S"; do
  f="${pair%%:*}"; fs="${pair##*:}"
  if grep -q '::ffff:' "$fs"; then
    ok "$f 保留 IPv4-mapped 派生"
  else
    fail "$f 缺失 IPv4-mapped（::ffff:）派生，mapped 形态将绕过 v4 段集"
  fi
done

# ═══ 第二部分：netguard 谱系对账（层定义与裁定见文件头）═══

NG="$ROOT/go-backend/internal/netguard/netguard.go"
BP="$ROOT/go-gost/x/bypass/flmx_no_private.go"
SK="$ROOT/go-gost/x/socket/websocket_reporter.go"

for f in "$NG" "$BP" "$SK"; do
  [ -f "$f" ] || { echo "FAIL: 前提失效，文件不存在：$f"; exit 2; }
done

NG_S="$(mktemp)"; BP_S="$(mktemp)"; SK_S="$(mktemp)"
trap 'rm -f "$BE_S" "$AG_S" "$NG_S" "$BP_S" "$SK_S"' EXIT
go_strip < "$NG" | raw_blank > "$NG_S"
go_strip < "$BP" | raw_blank > "$BP_S"
go_strip < "$SK" | raw_blank > "$SK_S"

# set_diff A B：打印 A − B（入参为换行分隔集合，内部排序去重去空行）。
set_diff() {
  comm -23 <(printf '%s\n' "$1" | grep -v '^$' | sort -u) <(printf '%s\n' "$2" | grep -v '^$' | sort -u)
}
set_union() { printf '%s\n%s\n' "$1" "$2" | grep -v '^$' | sort -u; }

# func_body <剥离副本> <函数名>：打印函数体（签名行至首个 ^}）。
func_body() {
  awk -v fn="$2" '
    index($0, "func " fn "(") == 1 { inblk = 1 }
    inblk { print }
    inblk && /^}/ { exit }
  ' "$1"
}

# preds_in：stdin 中的 .IsXxx 谓词库存（去重排序）。
preds_in() { grep -oE '\.Is[A-Z][A-Za-z]*' | sed 's/^\.//' | sort -u; }

# expand_pred <谓词>：谓词展开段集（本门钉住的展开表，Go 1.25
# stdlib 语义；net.IP 与 netip 同名谓词在本表各段上一致）。
# IsUnspecified 的 v4 形态 0.0.0.0 已被各面 0.0.0.0/8 段覆盖，不另
# 展开 /32；IsLinkLocalMulticast 是 ff00::/8 的子集，由 IsMulticast
# 展开覆盖；IsGlobalUnicast 是残差层标记（层 R），不展开。
# 未知谓词返回 1（前提失效：谓词库存已先行钉住，走到这里即门坏）。
expand_pred() {
  case "$1" in
    IsUnspecified) printf '%s\n' "::/128" ;;
    IsLoopback) printf '%s\n' "127.0.0.0/8" "::1/128" ;;
    IsMulticast) printf '%s\n' "224.0.0.0/4" "ff00::/8" ;;
    IsLinkLocalMulticast) : ;;
    IsLinkLocalUnicast) printf '%s\n' "169.254.0.0/16" "fe80::/10" ;;
    IsPrivate) printf '%s\n' "10.0.0.0/8" "172.16.0.0/12" "192.168.0.0/16" "fc00::/7" ;;
    IsGlobalUnicast) : ;;
    *) return 1 ;;
  esac
}

# expand_preds <谓词库存>：全部谓词的展开并集；未知谓词返回 1
# （库存已由 check_preds 先行钉住，走到失败即门自身损坏）。
expand_preds() {
  local p seg out=""
  for p in $1; do
    seg="$(expand_pred "$p")" || return 1
    out="$(set_union "$out" "$seg")"
  done
  printf '%s\n' "$out" | grep -v '^$' | sort -u
}

# check_preds <面名> <函数体> <钉住库存>：谓词库存对账，增减即红。
check_preds() {
  local name="$1" body="$2" pinned="$3" got
  got="$(printf '%s\n' "$body" | preds_in)"
  if [ "$got" = "$(printf '%s\n' $pinned | sort -u)" ]; then
    ok "$name 谓词库存与钉住集一致（$(printf '%s\n' "$got" | grep -c .) 个）"
  else
    fail "$name 谓词库存漂移（< 仅钉住集 / > 仅代码面；库存变化须同步更新本门展开表与层定义）："
    diff <(printf '%s\n' $pinned | sort -u) <(printf '%s\n' "$got") | sed 's/^/    /'
  fi
}

# ── 层 T / M：翻译前缀（逐角色）与元数据精确集，三面全等 ──
role_val() { # <剥离副本> <变量名候选（| 分隔）>
  grep -E "($2)[[:space:]]*=[[:space:]]*netip\.MustParsePrefix\(\"[^\"]+\"\)" "$1" \
    | grep -oE '"[0-9a-fA-F:.]+/[0-9]+"' | tr -d '"' | head -1
}
check_role() { # <角色名> <三面变量名候选（| 分隔，同一候选集逐面提取）>
  local role="$1" vng vbp vsk
  vng="$(role_val "$NG_S" "$2")"; vbp="$(role_val "$BP_S" "$2")"; vsk="$(role_val "$SK_S" "$2")"
  if [ -n "$vng" ] && [ "$vng" = "$vbp" ] && [ "$vng" = "$vsk" ]; then
    ok "翻译层 $role 三面一致（$vng）"
  else
    fail "翻译层 $role 三面漂移：netguard=$vng bypass=$vbp socket=$vsk"
  fi
}
check_role "NAT64 知名段" 'nat64WellKnown|mirrorNAT64WellKnown|tcpPingNAT64WellKnown'
check_role "NAT64 本地段" 'nat64Local|mirrorNAT64Local|tcpPingNAT64Local'
check_role "6to4 段" 'sixToFour|mirrorSixToFour|tcpPingSixToFour'
check_role "链路本地 v4 段" 'linkLocalV4|mirrorLinkLocalV4|tcpPingLinkLocalV4'

# metadata_set <剥离副本>：MetadataExact 块内 MustParseAddr 集。
metadata_set() {
  awk '
    index($0, "etadataExact") && index($0, "[]netip.Addr{") { inblk = 1; next }
    inblk && /^[[:space:]]*\}[[:space:]]*$/ { inblk = 0 }
    inblk { print }
  ' "$1" | grep -oE 'MustParseAddr\("[^"]+"\)' | grep -oE '"[^"]+"' | tr -d '"' | sort -u
}
NG_MD="$(metadata_set "$NG_S")"; BP_MD="$(metadata_set "$BP_S")"; SK_MD="$(metadata_set "$SK_S")"
if [ -n "$NG_MD" ] && [ "$NG_MD" = "$BP_MD" ] && [ "$NG_MD" = "$SK_MD" ]; then
  ok "元数据层精确集三面一致（$(printf '%s\n' "$NG_MD" | grep -c .) 个）"
else
  fail "元数据层精确集三面漂移（netguard / bypass / socket）："
  printf '%s\n' "  netguard: $(printf '%s' "$NG_MD" | tr '\n' ' ')" "  bypass:   $(printf '%s' "$BP_MD" | tr '\n' ' ')" "  socket:   $(printf '%s' "$SK_MD" | tr '\n' ' ')" | sed 's/^/    /'
fi

# ── 层 S：段集有效集对账 ──
# netguard 段表 (段, 类) 对：nonPublicPrefixes 块逐行提取。
NG_ENTRIES="$(
  awk '
    index($0, "nonPublicPrefixes = []struct") { inblk = 1; next }
    inblk && /^[[:space:]]*\}[[:space:]]*$/ { inblk = 0 }
    inblk { print }
  ' "$NG_S" | grep -oE 'MustParsePrefix\("[^"]+"\), Class[A-Za-z]+' \
    | sed -E 's/MustParsePrefix\("([^"]+)"\), (Class[A-Za-z]+)/\1 \2/' | sort -u
)"
NG_LIT="$(printf '%s\n' "$NG_ENTRIES" | awk '{print $1}' | grep -v '^$' | sort -u)"
NG_RESV="$(printf '%s\n' "$NG_ENTRIES" | awk '$2 == "ClassReserved" {print $1}' | sort -u)"
NG_LIT_RC="$(printf '%s\n' "$NG_ENTRIES" | awk '$2 == "ClassReserved" || $2 == "ClassCGNAT" {print $1}' | sort -u)"
if [ -z "$NG_LIT" ] || [ -z "$NG_RESV" ]; then
  fail "netguard 段表提取为空或无 ClassReserved 段（前提失效：段表形态变化）"
fi

NG_BODY="$(func_body "$NG_S" classifyPlain)"
BP_BODY="$(func_body "$BP_S" isNonPublicIP)"
SK_BODY="$(func_body "$SK_S" tcpPingResolvedDenied)"

PIN_PREDS_FULL="IsGlobalUnicast IsLinkLocalMulticast IsLinkLocalUnicast IsLoopback IsMulticast IsPrivate IsUnspecified"
PIN_PREDS_BP="IsLinkLocalMulticast IsLinkLocalUnicast IsLoopback IsMulticast IsPrivate IsUnspecified"
check_preds "netguard classifyPlain" "$NG_BODY" "$PIN_PREDS_FULL"
check_preds "bypass isNonPublicIP" "$BP_BODY" "$PIN_PREDS_BP"
check_preds "socket tcpPingResolvedDenied" "$SK_BODY" "$PIN_PREDS_FULL"

# eff_of <显式段集> <函数体> <面名>：有效集 = 显式段 ∪ 谓词展开。
eff_of() {
  local exp
  exp="$(expand_preds "$(printf '%s\n' "$2" | preds_in)")" || { fail "$3 谓词展开失败（展开表未覆盖库存内谓词，门自身损坏）"; exp=""; }
  set_union "$1" "$exp"
}

NG_EFF="$(eff_of "$NG_LIT" "$NG_BODY" "netguard")"

# bypass 显式段表：nonPublicCIDRs 的 []string 字面量块。
BP_LIT="$(
  awk '
    index($0, "range []string{") { inblk = 1; next }
    inblk && /\} \{/ { inblk = 0 }
    inblk { print }
  ' "$BP_S" | grep -oE '"[0-9a-fA-F:.]+/[0-9]+"' | tr -d '"' | sort -u
)"
if [ -z "$BP_LIT" ]; then
  fail "bypass 显式段表提取为空（前提失效：nonPublicCIDRs 形态变化）"
fi
BP_EFF="$(eff_of "$BP_LIT" "$BP_BODY" "bypass")"

if [ "$BP_LIT" = "$NG_LIT_RC" ]; then
  ok "bypass 显式段表 == netguard 段表的 Reserved∪CGNAT 段（其余类由谓词覆盖）"
else
  fail "bypass 显式段表与 netguard Reserved∪CGNAT 段漂移（< 仅 netguard / > 仅 bypass）："
  diff <(printf '%s\n' "$NG_LIT_RC") <(printf '%s\n' "$BP_LIT") | sed 's/^/    /'
fi

if [ "$BP_EFF" = "$NG_EFF" ]; then
  ok "netguard 谱系全集：bypass 有效集 == netguard 有效集（$(printf '%s\n' "$NG_EFF" | grep -c .) 段）"
else
  fail "netguard 谱系全集漂移（< 仅 netguard / > 仅 bypass）："
  diff <(printf '%s\n' "$NG_EFF") <(printf '%s\n' "$BP_EFF") | sed 's/^/    /'
fi

# 已裁定文档段（R3-C2-01）：规范集独有、方向固定，恰此 4 段。
DOC_SEGS="192.0.2.0/24
198.51.100.0/24
2001:db8::/32
203.0.113.0/24"
CN_ALL="$(set_union "$(extract "$BE_S" guardPrivateCIDRsV4)" "$(extract "$BE_S" guardPrivateCIDRsV6)")"
CN_MINUS_NG="$(set_diff "$CN_ALL" "$NG_EFF")"
NG_MINUS_CN="$(set_diff "$NG_EFF" "$CN_ALL")"
if [ "$CN_MINUS_NG" = "$DOC_SEGS" ] && [ -z "$NG_MINUS_CN" ]; then
  ok "规范集 − 谱系 == 已裁定文档段 4 段、谱系 − 规范集 == ∅（R3-C2-01 登记分歧钉死）"
else
  fail "规范集与 netguard 谱系的差集偏离已裁定形态（应为规范集独有恰 4 文档段、谱系独有 ∅）："
  echo "    规范集 − 谱系（实际）：$(printf '%s' "$CN_MINUS_NG" | tr '\n' ' ')"
  echo "    谱系 − 规范集（实际）：$(printf '%s' "$NG_MINUS_CN" | tr '\n' ' ')"
  echo "    文档段期望集：$(printf '%s' "$DOC_SEGS" | tr '\n' ' ')"
  d1="$(set_diff "$DOC_SEGS" "$CN_MINUS_NG")"; d2="$(set_diff "$CN_MINUS_NG" "$DOC_SEGS")"
  [ -n "$d1" ] && echo "    期望差集中缺失（谱系侧已收或规范集侧已删）：$d1"
  [ -n "$d2" ] && echo "    差集中多出期望外段：$d2"
fi

# socket 部分镜像共担子层：保留段表 == netguard ClassReserved 段集。
SK_RESV="$(
  awk '
    index($0, "tcpPingReservedPrefixes = []netip.Prefix{") { inblk = 1; next }
    inblk && /^[[:space:]]*\}[[:space:]]*$/ { inblk = 0 }
    inblk { print }
  ' "$SK_S" | grep -oE 'MustParsePrefix\("[^"]+"\)' | grep -oE '"[^"]+"' | tr -d '"' | sort -u
)"
if [ -z "$SK_RESV" ]; then
  fail "socket 保留段表提取为空（前提失效：tcpPingReservedPrefixes 形态变化）"
elif [ "$SK_RESV" = "$NG_RESV" ]; then
  ok "socket 部分镜像保留段表 == netguard ClassReserved 段集（$(printf '%s\n' "$SK_RESV" | grep -c .) 段）"
else
  fail "socket 保留段表与 netguard ClassReserved 段集漂移（< 仅 netguard / > 仅 socket）："
  diff <(printf '%s\n' "$NG_RESV") <(printf '%s\n' "$SK_RESV") | sed 's/^/    /'
fi

# ── 层 R：残差兜底在位（bypass 无兜底为已登记结构差异，见文件头）──
if printf '%s\n' "$NG_BODY" | grep -q '!a\.IsGlobalUnicast()'; then
  ok "netguard classifyPlain 保留 !IsGlobalUnicast 残差兜底"
else
  fail "netguard classifyPlain 缺失 !IsGlobalUnicast 残差兜底（层 R 前提失效）"
fi
if printf '%s\n' "$SK_BODY" | grep -q '!eff\.IsGlobalUnicast()'; then
  ok "socket tcpPingResolvedDenied 保留 !IsGlobalUnicast 残差兜底"
else
  fail "socket tcpPingResolvedDenied 缺失 !IsGlobalUnicast 残差兜底（层 R 前提失效）"
fi

n4="$(extract "$BE_S" guardPrivateCIDRsV4 | wc -l)"
n6="$(extract "$BE_S" guardPrivateCIDRsV6 | wc -l)"
echo "private-ranges: $((n4 + n6)) segments (v4 $n4 + v6 $n6; mapped forms derived on both sides)"
exit "$FAIL"
