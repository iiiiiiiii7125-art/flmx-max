#!/usr/bin/env bash
# check-timeout-budgets.sh — 超时预算登记表机械对账（R8 T-GATE 常设门，
# x-verdicts-r8.md §7 批 T-GATE）。与 check-doc-consistency.sh 同 CI 步
# （该脚本 §8 转发本脚本结论）。
#
# 正本：doc/audit/timeout-budgets.tsv（逐格登记 后端界 / 前端值 / 有效
# 服务端时限 / 所属立案号）。本脚本从产品树机械提取三层数值与登记表
# 对账，任一格漂移即 FAIL 并点名该格：
#   - 前端层：vite-frontend/src/api/index.ts 全部导出调用点的有效
#     timeout（字面量 / Network 默认 / 公式助手参数 / 动态下限），以及
#     api/diagnosis-stream.ts 的流中止常量、network.ts 的默认超时；
#   - 服务端层：internal/app/app.go 的全局 WriteTimeout 与各端点
#     SetWriteDeadline 放宽常量（write_deadline.go / gh_mirror.go），
#     并核放宽常量确有 handler 在用（extendWriteDeadline 武装点）；
#   - 后端界：具名常量由 Go 门测试同值钉住（见 registry 头注），本脚本
#     对可机械求值的常量/字面量独立复算，公式/复合/推导界按登记表
#     锚点核存在性与分量值。
# 断言（边距 ≥10s 或 ≥10%，裁定 §7）：strict 格 前端 > 后端界 且
# 有效服务端时限 > 后端界；aligned 格 前端 == 服务端 == 登记对齐值；
# 登记表外的调用点（普查差集非 ∅）即 FAIL；class 格调用点若获得
# 专属 timeout（不再走默认）必须改登专行，否则 FAIL。
# B05-001 常设门：class 格端点的 handler 函数体若直接调用版本发现
# 函数，必须改登 strict 专行——/system/version 曾以 DB/local class
# 误分类逃过三层对账（C080）；仅登记行带命名例外（exception 列）
# 时放行并在输出中点名。C059（/node/install）原为唯一例外
#（discovery-prelude-legacy），已按 C080 同法收口为 strict 专行、
# 例外摘除，现无存续例外。变体自检见
# scripts/check-timeout-budgets-selftest.sh。
# B11-001 常设门（R2-K2）：函数体直调内核下发入口的 handler 必须
# 改登 strict/formula 条件界行并以登记的放宽常量武装——kernel
# 启停与入站家族曾分别以 20s 界 / class 12s 登记，对「内核未装
# 版本时下发升至 kernelApplyTimeout 300s」的条件升级不可见。
#
# 用法：check-timeout-budgets.sh [REPO_ROOT]（默认脚本所在仓库根）。
# 退出码：0 = 全格对账通过；1 = 存在漂移/缺格；2 = 前提失效。
set -u

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
REG="$ROOT/doc/audit/timeout-budgets.tsv"
FE_TS="$ROOT/vite-frontend/src/api/index.ts"
FE_STREAM="$ROOT/vite-frontend/src/api/diagnosis-stream.ts"
FE_NET="$ROOT/vite-frontend/src/api/network.ts"
APP_GO="$ROOT/go-backend/internal/app/app.go"
HANDLER_DIR="$ROOT/go-backend/internal/http/handler"

FAIL=0
fail() { echo "FAIL: $*"; FAIL=1; }
ok()   { echo "OK:   $*"; }

for f in "$REG" "$FE_TS" "$FE_STREAM" "$FE_NET" "$APP_GO"; do
  [ -f "$f" ] || { echo "FAIL: 前提失效，文件不存在：$f"; exit 2; }
done
[ -d "$HANDLER_DIR" ] || { echo "FAIL: 前提失效，目录不存在：$HANDLER_DIR"; exit 2; }

# ── 数值求值助手 ──────────────────────────────────────────────────────
# eval_go_dur "360 * time.Second" -> 360 ；"30 * time.Minute" -> 1800
eval_go_dur() {
  local e="$1"
  e="$(echo "$e" | tr -d '[:space:]')"
  e="${e//time.Minute/*60}"
  e="${e//time.Second/*1}"
  awk -v e="$e" 'BEGIN{ n=split(e,a,"*"); v=1; for(i=1;i<=n;i++){ if(a[i]=="") continue; v*=a[i] } print v }'
}
# eval_ms_expr "1_800_000" / "15 * 60 * 1000" -> 秒
eval_ms_expr() {
  local e="${1//_/}"
  e="$(echo "$e" | tr -d '[:space:]')"
  awk -v e="$e" 'BEGIN{ n=split(e,a,"*"); v=1; for(i=1;i<=n;i++){ if(a[i]=="") continue; v*=a[i] } printf "%g", v/1000 }'
}
# num_eq / margin_ok（边距 ≥10s 或 ≥10%）
num_eq() { awk -v a="$1" -v b="$2" 'BEGIN{ print (a==b) ? 1 : 0 }'; }
margin_ok() { awk -v a="$1" -v b="$2" 'BEGIN{ d=a-b; print (d>0 && (d>=10 || d>=b/10)) ? 1 : 0 }'; }
gt_ok() { awk -v a="$1" -v b="$2" 'BEGIN{ print (a>b) ? 1 : 0 }'; }

# go_const_s SYMBOL FILE...：取具名 const/var 的简单乘积 RHS 并求值（秒）；
# RHS 含其他标识符（如 latencyCommandTimeout 的表达式）时输出 DEFER，
# 其值由 Go 门测试钉住；符号不存在输出空。
go_const_s() {
  local sym="$1"; shift
  local rhs
  rhs="$(grep -rhoP --exclude='*_test.go' "^\s*(?:const\s+|var\s+)?$sym\s*=\s*\K[^/]+" "$@" 2>/dev/null | head -1 | sed 's/[[:space:]]*$//')"
  [ -n "$rhs" ] || { echo ""; return; }
  if echo "$rhs" | grep -qP '^[0-9][0-9\s\*\.timeA-Za-z]*$' && ! echo "$rhs" | grep -qP '\b(latency|Ping|Waves|Count|MS)\b'; then
    eval_go_dur "$rhs"
  else
    echo "DEFER"
  fi
}

GLOBAL_SV="$(grep -oP 'WriteTimeout:\s*\K[^,]+' "$APP_GO" | head -1)"
[ -n "$GLOBAL_SV" ] || { echo "FAIL: 前提失效，app.go 未找到 WriteTimeout"; exit 2; }
GLOBAL_SV="$(eval_go_dur "$GLOBAL_SV")"

NET_DEFAULT_MS="$(grep -oP 'timeout \?\? \K\d+' "$FE_NET" | head -1)"
[ -n "$NET_DEFAULT_MS" ] || { echo "FAIL: 前提失效，network.ts 未找到默认 timeout"; exit 2; }
NET_DEFAULT_S="$(awk -v v="$NET_DEFAULT_MS" 'BEGIN{ printf "%g", v/1000 }')"
[ "$(num_eq "$NET_DEFAULT_S" 30)" = 1 ] || fail "network.ts 默认超时漂移：提取值 ${NET_DEFAULT_S}s（登记表 default 格按 30s 登记）"

# sv_value SV_SRC：global -> 全局 WriteTimeout；其余按具名常量求值
sv_value() {
  case "$1" in
    global) echo "$GLOBAL_SV" ;;
    ghMirrorServeFor) go_const_s "ghMirrorServeFor" "$HANDLER_DIR/gh_mirror.go" ;;
    *) go_const_s "$1" "$HANDLER_DIR" ;;
  esac
}

# ── 前端调用点普查：name -> timeout 原始表达式（Network./axios. 之后首个 timeout:）──
FE_MAP="$(mktemp)"
awk '
  /^export (const|function) / { flush(); name=$3; sub(/=$/,"",name); span=""; next }
  /^export (interface|type) / { flush(); name=""; next }
  { if (name != "") span = span $0 "\n" }
  function flush(    p1,p2,p,sub2) {
    if (name == "") return
    p1 = index(span, "Network."); p2 = index(span, "axios.")
    p = 0; if (p1 > 0) p = p1; if (p2 > 0 && (p == 0 || p2 < p)) p = p2
    sub2 = (p > 0) ? substr(span, p) : span
    if (match(sub2, /timeout:[ \t]*[^,}]+/)) {
      expr = substr(sub2, RSTART, RLENGTH); sub(/^timeout:[ \t]*/, "", expr)
    } else expr = ""
    printf "%s\t%s\n", name, expr
    name = ""; span = ""
  }
  END { flush() }
' "$FE_TS" > "$FE_MAP"

# fe_extract CALL -> "KIND VALUE"：KIND = default|literal|formula:<helper>|dynamic
fe_extract() {
  local call="$1" expr
  if ! awk -F'\t' -v n="$call" '$1==n{ f=1 } END{ exit !f }' "$FE_MAP"; then
    echo "MISSING"; return
  fi
  expr="$(awk -F'\t' -v n="$call" '$1==n{ print $2; exit }' "$FE_MAP")"
  [ -n "$expr" ] || { echo "default $NET_DEFAULT_S"; return; }
  case "$expr" in
    batchCallTimeout*) echo "formula:batchCallTimeout" ;;
    latencyProbeTimeout*) echo "formula:latencyProbeTimeout" ;;
    relayProbeTimeout*) echo "formula:relayProbeTimeout" ;;
    Math.max*) echo "dynamic $(eval_ms_expr "$(echo "$expr" | grep -oP 'Math\.max\(\s*\K[\d_]+' | head -1)")" ;;
    *) echo "literal $(eval_ms_expr "$expr")" ;;
  esac
}
# helper_params HELPER -> "CAP FLOOR"（秒）
helper_params() {
  local body cap floor
  body="$(grep -A10 -P "^const $1\b" "$FE_TS" | head -12)"
  cap="$(echo "$body" | grep -oP 'Math\.min\(\s*\K[\d_]+' | head -1)"
  floor="$(echo "$body" | grep -oP 'Math\.max\(\s*\K[\d_]+' | head -1)"
  [ -n "$cap" ] && [ -n "$floor" ] || { echo ""; return; }
  echo "$(eval_ms_expr "$cap") $(eval_ms_expr "$floor")"
}
STREAM_S="$(grep -oP 'DIAGNOSIS_STREAM_TIMEOUT_MS\s*=\s*\K[^;]+' "$FE_STREAM" | head -1)"
[ -n "$STREAM_S" ] && STREAM_S="$(eval_ms_expr "$STREAM_S")"

# be_literal_anchor CELL -> 提取的后端界（秒），无锚点输出空
be_literal_anchor() {
  case "$1" in
    T006) grep -qP '"RollbackAgent".*,\s*30\s*\*\s*time\.Second' "$HANDLER_DIR/upgrade.go" && echo 30 ;;
    T010) grep -qP 'WithTimeout\(ctx,\s*60\s*\*\s*time\.Second\)' "$HANDLER_DIR/gist_backup.go" && echo 60 ;;
    T017|T018|T024) grep -qP 'WithTimeout\(ctx,\s*20\s*\*\s*time\.Second\)' "$HANDLER_DIR/kernel_external.go" && echo 20 ;;
    A001) grep -oP 'downloadTimeout\s*=\s*\K4\s*\*\s*time\.Minute' "$ROOT/go-gost/x/kernel/install.go" >/dev/null && echo 240 ;;
  esac
}
# be_component_check CELL：expr 界的分量锚点（分量值由 Go 门测试/脚本常量复算）
be_component_check() {
  case "$1" in
    T005)
      # B05-002（R2-K2）：界 = 两段发现链前奏 120s（API 段与重定向
      # 段各两腿 × 30s 逐尝试客户端）+ upgradeTimeout 300s；任一分量
      # 锚点漂移即界漂移。
      grep -qP 'Timeout:\s*30 \* time\.Second' "$HANDLER_DIR/secure_fetch.go" &&
      grep -qP 'Timeout:\s*30 \* time\.Second' "$HANDLER_DIR/upgrade.go" &&
      [ "$(go_const_s upgradeTimeout "$HANDLER_DIR")" = 300 ] ;;
    T007|T008)
      grep -qP 'Timeout:\s*30 \* time\.Second' "$HANDLER_DIR/secure_fetch.go" ;;
    T009|C080|C059)
      # 两段链界（B05-001/B05-003）：版本发现 API 段与 stable 重定向
      # 兜底段各两腿 × 30s，两个逐尝试客户端锚点缺一即界漂移。
      # C059（/node/install）无条件走同一解析链，按 C080 同法收口后
      # 同列（原 class + discovery-prelude-legacy 例外已摘除）。
      grep -qP 'Timeout:\s*30 \* time\.Second' "$HANDLER_DIR/secure_fetch.go" &&
      grep -qP 'Timeout:\s*30 \* time\.Second' "$HANDLER_DIR/upgrade.go" ;;
    T015)
      grep -qP 'MaxTimeoutSec:\s*60' "$ROOT/go-backend/internal/monitoring/limits.go" &&
      grep -qP 'cmdTimeout = cmdTimeout \+ 2\*time\.Second' "$ROOT/go-backend/internal/health/checker.go" ;;
    T016)
      [ "$(go_const_s notifySendTimeout "$HANDLER_DIR")" = 10 ] &&
      [ "$(go_const_s notifyRetryDelay "$HANDLER_DIR")" = 3 ] ;;
    *) return 0 ;;
  esac
}

# ── 放宽常量武装核查：登记的每个 relief 常量必须确有 handler 在用 ──────
declare -A ARM_SEEN=()
while IFS= read -r svsrc; do
  [ -n "${svsrc:-}" ] || continue
  case "$svsrc" in
    global|"") ;;
    ghMirrorServeFor)
      grep -q "SetWriteDeadline(time.Now().Add(ghMirrorServeFor))" "$HANDLER_DIR/gh_mirror.go" \
        || fail "ghMirrorServeFor 已登记但 gh_mirror.go 未见其 SetWriteDeadline 武装点" ;;
    *)
      if [ -z "${ARM_SEEN[$svsrc]:-}" ]; then
        ARM_SEEN[$svsrc]=1
        n="$(grep -rho --exclude='*_test.go' "extendWriteDeadline(w, $svsrc)" "$HANDLER_DIR" | wc -l)"
        [ "$n" -ge 1 ] || fail "放宽常量 $svsrc 已登记但无任何 handler 武装（extendWriteDeadline）"
      fi ;;
  esac
done < <(grep -v '^#' "$REG" | tail -n +2 | cut -f11)

# ── 反向闭合：write_deadline.go 的每个放宽常量必须已登记 ──────────────
if [ -f "$HANDLER_DIR/write_deadline.go" ]; then
  for sym in $(grep -oP '^\t\K[A-Za-z]+WriteDeadline(?=\s*=)' "$HANDLER_DIR/write_deadline.go"); do
    grep -qP "\t${sym}\t" "$REG" || fail "write_deadline.go 常量 $sym 未登记（新增放宽常量必须先入登记表）"
  done
fi

# ── 登记表逐格对账 ────────────────────────────────────────────────────
# 注意：登记表含空字段，bash 的 IFS 制表符属空白类会吞掉空字段造成列
# 错位——必须先经 awk 转为 \037（非空白）分隔再 read -a 保真切分。
ROWS=0
SEEN_CALLS="$(mktemp)"
CLASS_ROWS="$(mktemp)"
while IFS= read -r _line; do
  [ -n "$_line" ] || continue
  _conv="$(printf '%s' "$_line" | awk -F'\t' 'BEGIN{OFS="\037"} { out=$1; for(i=2;i<=18;i++) out=out OFS $i; print out }')"
  IFS=$'\x1f' read -ra F <<< "$_conv"
  cell="${F[0]:-}"; call="${F[1]:-}"; surface="${F[2]:-}"; endpoint="${F[3]:-}"
  [ "${F[14]:-}" = "class" ] && printf '%s\t%s\t%s\n' "$cell" "$endpoint" "${F[15]:-}" >> "$CLASS_ROWS"
  fe_s="${F[4]:-}"; fe_kind="${F[5]:-}"; fe_src="${F[6]:-}"; fe_floor="${F[7]:-}"; fe_cap="${F[8]:-}"
  sv_s="${F[9]:-}"; sv_src="${F[10]:-}"; be_s="${F[11]:-}"; be_kind="${F[12]:-}"; be_src="${F[13]:-}"
  check="${F[14]:-}"; exception="${F[15]:-}"
  ROWS=$((ROWS+1))
  tag="$cell($call)"
  # 前端层提取（fe / fe-raw / fe-stream 表面）
  fe_x="" fe_k=""
  case "$surface" in
    fe|fe-raw)
      echo "$call" >> "$SEEN_CALLS"
      read -r fe_k fe_x <<EOF
$(fe_extract "$call")
EOF
      [ "$fe_k" = "MISSING" ] && { fail "$tag 登记表调用点在 api/index.ts 中不存在"; continue; }
      ;;
    fe-stream)
      fe_k="const"; fe_x="$STREAM_S" ;;
  esac
  # 后端界提取（可机械求值者）
  be_x=""
  case "$be_kind" in
    const) be_x="$(go_const_s "$be_src" "$HANDLER_DIR")" ;;
    literal) be_x="$(be_literal_anchor "$cell")" ;;
  esac

  case "$check" in
    strict)
      [ "$fe_k" = "formula:"* ] && { fail "$tag strict 格前端不得为公式值（提取 $fe_k）"; }
      if [ "$fe_kind" = "dynamic" ]; then
        [ "$(num_eq "$fe_x" "$fe_s")" = 1 ] || fail "$tag 前端动态下限漂移：提取 ${fe_x}s ≠ 登记 ${fe_s}s"
      else
        [ "$(num_eq "$fe_x" "$fe_s")" = 1 ] || fail "$tag 前端值漂移：提取 ${fe_x}s ≠ 登记 ${fe_s}s"
      fi
      sv_x="$(sv_value "$sv_src")"
      [ "$sv_x" = "DEFER" ] || [ "$(num_eq "$sv_x" "$sv_s")" = 1 ] || fail "$tag 服务端时限漂移：提取 ${sv_x}s ≠ 登记 ${sv_s}s（来源 $sv_src）"
      if [ -n "$be_x" ] && [ "$be_x" != "DEFER" ]; then
        [ "$(num_eq "$be_x" "$be_s")" = 1 ] || fail "$tag 后端界漂移：提取 ${be_x}s ≠ 登记 ${be_s}s（来源 $be_src）"
      fi
      be_component_check "$cell" || fail "$tag 后端界分量锚点缺失（$be_src）"
      [ "$(margin_ok "$fe_s" "$be_s")" = 1 ] || fail "$tag 前端 ${fe_s}s 对后端界 ${be_s}s 边距不足（须 ≥10s 或 ≥10%）"
      [ "$(margin_ok "$sv_s" "$be_s")" = 1 ] || fail "$tag 服务端时限 ${sv_s}s 对后端界 ${be_s}s 边距不足（须 ≥10s 或 ≥10%）"
      ;;
    aligned)
      [ "$(num_eq "$fe_x" "$fe_s")" = 1 ] || fail "$tag 前端值漂移：提取 ${fe_x}s ≠ 登记对齐值 ${fe_s}s"
      sv_x="$(sv_value "$sv_src")"
      [ "$(num_eq "$sv_x" "$sv_s")" = 1 ] || fail "$tag 服务端时限漂移：提取 ${sv_x}s ≠ 登记对齐值 ${sv_s}s"
      [ "$(num_eq "$fe_s" "$sv_s")" = 1 ] || fail "$tag aligned 格前端与服务端登记值不一致（${fe_s} vs ${sv_s}）"
      ;;
    formula)
      case "$fe_src" in
        helper:*)
          hp="$(helper_params "${fe_src#helper:}")"
          [ -n "$hp" ] || { fail "$tag 公式助手 ${fe_src#helper:} 参数无法提取"; hp="0 0"; }
          read -r cap_x floor_x <<EOF
$hp
EOF
          [ "$(num_eq "$floor_x" "$fe_floor")" = 1 ] || fail "$tag 公式下限漂移：提取 ${floor_x}s ≠ 登记 ${fe_floor}s"
          [ "$(num_eq "$cap_x" "$fe_cap")" = 1 ] || fail "$tag 公式上限漂移：提取 ${cap_x}s ≠ 登记 ${fe_cap}s"
          ;;
        const:*)
          cx="$(go_const_s "${fe_src#const:}" "$HANDLER_DIR")"
          want="${fe_floor:-$fe_s}"
          [ "$cx" = "DEFER" ] || [ "$(num_eq "$cx" "$want")" = 1 ] || fail "$tag 常量漂移：提取 ${cx}s ≠ 登记 ${want}s（来源 ${fe_src#const:}）"
          ;;
        literal)
          [ "$(num_eq "$fe_x" "$fe_s")" = 1 ] || fail "$tag 前端值漂移：提取 ${fe_x}s ≠ 登记 ${fe_s}s" ;;
      esac
      if [ -n "$sv_src" ]; then
        sv_x="$(sv_value "$sv_src")"
        [ "$sv_x" = "DEFER" ] || [ "$(num_eq "$sv_x" "$sv_s")" = 1 ] || fail "$tag 服务端时限漂移：提取 ${sv_x}s ≠ 登记 ${sv_s}s"
      fi
      if [ "$be_kind" = "const" ] && [ -n "$be_x" ] && [ "$be_x" != "DEFER" ]; then
        [ "$(num_eq "$be_x" "$be_s")" = 1 ] || fail "$tag 后端界漂移：提取 ${be_x}s ≠ 登记 ${be_s}s（来源 $be_src）"
      fi
      ;;
    composite|exception)
      if [ "$surface" = "fe" ] || [ "$surface" = "fe-raw" ] || [ "$surface" = "fe-stream" ]; then
        [ "$(num_eq "$fe_x" "$fe_s")" = 1 ] || fail "$tag 前端值漂移：提取 ${fe_x}s ≠ 登记 ${fe_s}s"
      fi
      if [ -n "$sv_src" ]; then
        sv_x="$(sv_value "$sv_src")"
        [ "$sv_x" = "DEFER" ] || [ "$(num_eq "$sv_x" "$sv_s")" = 1 ] || fail "$tag 服务端时限漂移：提取 ${sv_x}s ≠ 登记 ${sv_s}s"
      fi
      [ "$check" = "exception" ] && [ -z "$exception" ] && fail "$tag exception 格缺少命名例外（例外必须是登记行属性）"
      ;;
    class)
      [ "$fe_k" = "default" ] || [ "$fe_k" = "literal" ] || { fail "$tag class 格调用点获得了公式/动态 timeout（提取 $fe_k），必须改登专行"; }
      [ "$(num_eq "$fe_x" "$fe_s")" = 1 ] || fail "$tag 前端值漂移：提取 ${fe_x}s ≠ 登记 ${fe_s}s"
      [ "$(gt_ok "$fe_s" "$be_s")" = 1 ] || fail "$tag class 格前端 ${fe_s}s 不大于类界 ${be_s}s"
      ;;
    observed)
      case "$surface" in
        fe-external)
          [ -f "$ROOT/vite-frontend/src/utils/version-update.ts" ] || fail "$tag 登记的外部表面 utils/version-update.ts 不存在" ;;
        fe|fe-raw)
          if [ "$fe_kind" = "default" ]; then
            [ "$fe_k" = "default" ] || fail "$tag observed 格（默认预算）调用点获得了专属 timeout（提取 $fe_k），必须重新裁定登记"
          fi ;;
        federation)
          case "$cell" in
            F004) grep -qP 'newFederationHTTPClient\(10 \* time\.Second\)' "$ROOT/go-backend/internal/http/client/federation.go" \
                    || fail "$tag 联邦默认 client 10s 锚点缺失" ;;
            F005) grep -qP 'NewFederationClientWithTimeout\(5 \* time\.Second\)' "$HANDLER_DIR/federation.go" \
                    || fail "$tag 联邦状态同步 5s 锚点缺失" ;;
          esac ;;
      esac
      ;;
    pin)
      case "$cell" in
        P001) [ "$(go_const_s idemWaitTimeout "$HANDLER_DIR")" = "$be_s" ] || fail "$tag idemWaitTimeout 漂移（登记 ${be_s}s）" ;;
        P002)
          j="$(go_const_s addServiceGuardJoinTimeout "$HANDLER_DIR")"; d="$(go_const_s defaultNodeCommandTimeout "$HANDLER_DIR")"
          [ "$(awk -v a="$j" -v b="$d" 'BEGIN{ print a+b }')" = "$be_s" ] || fail "$tag guard join 界漂移：${j}+${d} ≠ 登记 ${be_s}s" ;;
      esac
      ;;
    agent)
      case "$cell" in
        A001)
          [ "$(go_const_s kernelApplyTimeout "$HANDLER_DIR")" = "$fe_s" ] || fail "$tag 面板耐心 kernelApplyTimeout 漂移（登记 ${fe_s}s）"
          [ "$be_x" = "$be_s" ] || fail "$tag agent 下载界漂移：提取 ${be_x}s ≠ 登记 ${be_s}s" ;;
        A002)
          [ "$(go_const_s upgradeTimeout "$HANDLER_DIR")" = "$fe_s" ] || fail "$tag 面板耐心 upgradeTimeout 漂移（登记 ${fe_s}s）"
          grep -qP 'upgradeDownloadTimeout\s*=\s*4\s*\*\s*time\.Minute' "$ROOT/go-gost/x/socket/websocket_reporter.go" \
            || fail "$tag agent upgradeDownloadTimeout 锚点漂移（登记 ${be_s}s = 4min）" ;;
        A003) [ "$(go_const_s nodeTuneProbeTimeout "$HANDLER_DIR")" = "$fe_s" ] || fail "$tag 面板预算 nodeTuneProbeTimeout 漂移" ;;
        A004) [ "$(go_const_s routeCommandTimeout "$HANDLER_DIR")" = "$fe_s" ] || fail "$tag 面板预算 routeCommandTimeout 漂移" ;;
        M001)
          sv_x="$(sv_value ghMirrorServeFor)"
          [ "$(num_eq "$sv_x" "$sv_s")" = 1 ] || fail "$tag gh-mirror 放宽漂移：提取 ${sv_x}s ≠ 登记 ${sv_s}s"
          grep -qP 'ghMirrorHTTP\s*=\s*&http\.Client\{Timeout:\s*10 \* time\.Minute' "$HANDLER_DIR/gh_mirror.go" \
            || fail "$tag gh-mirror 上游 client 10min 锚点漂移（登记界 ${be_s}s）"
          ;;
      esac
      if [ -n "$fe_s" ] && [ -n "$be_s" ]; then
        [ "$(gt_ok "$fe_s" "$be_s")" = 1 ] || fail "$tag agent 界 ${be_s}s 不小于面板耐心 ${fe_s}s"
      fi
      ;;
  esac
done < <(grep -v '^#' "$REG" | tail -n +2)

# ── 版本发现面禁登 class（B05-001 常设门）────────────────────────────
# class 格的后端界只是类天花板（12s），不做三层对账；/system/version
# 曾以此逃过对账（C080：实际界 120s、前端仅 30s）。规则：class 格
# 端点的 handler 函数体若直接调用任一版本发现函数，必须改登 strict
# 专行；登记行带命名例外（exception 列非空）时放行但在此点名。路由
# 按 handler.go 的两种注册形态（mux.HandleFunc 与路由表字面量）解析，
# 函数体取自 handler 目录；无法机械解析的格（模板路径等）跳过。
DISCOVERY_FN_RE='(fetchSystemUpgradeReleases|resolveSystemUpgradeLatestReleaseByChannel|fetchGitHubReleasesWithProxy|fetchGitHubReleases|resolveLatestReleaseByChannelWithProxy|resolveLatestReleaseByChannel|resolveLatestTagByRedirectWithProxy|kernelReleaseVersionsWithProxy|fetchKernelReleaseVersionsPage)[[:space:]]*\('
# 一次性建索引：路由表（端点 -> handler 函数名）与函数体拆分文件，
# 避免逐格重复全树 grep。
ROUTE_MAP="$(mktemp)"; BODY_DIR="$(mktemp -d)"
grep -rhoP --exclude='*_test.go' '"/api/v1[^"]*", h\.[A-Za-z0-9]+' "$ROOT/go-backend/internal/http" 2>/dev/null \
  | sed -E 's#"/api/v1([^"]*)", h\.(.*)#\1\t\2#' | sort -u > "$ROUTE_MAP"
awk -v dir="$BODY_DIR" '
  /^func \(h \*Handler\) [A-Za-z0-9]+\(/ {
    if (out) close(out)
    name = $0; sub(/^func \(h \*Handler\) /, "", name); sub(/\(.*/, "", name)
    out = dir "/" name; print > out; next
  }
  /^func / { if (out) { close(out); out = "" } }
  out { print >> out }
' "$HANDLER_DIR"/*.go 2>/dev/null
while IFS=$'\t' read -r d_cell d_ep d_exc; do
  [ -n "$d_cell" ] || continue
  d_fn="$(awk -F'\t' -v ep="$d_ep" '$1==ep { print $2; exit }' "$ROUTE_MAP")"
  [ -n "$d_fn" ] || continue
  [ -f "$BODY_DIR/$d_fn" ] || continue
  if grep -qE "$DISCOVERY_FN_RE" "$BODY_DIR/$d_fn"; then
    if [ -n "$d_exc" ]; then
      echo "NOTE: $d_cell($d_ep) 的 handler $d_fn 含版本发现调用，以命名例外「$d_exc」保留 class 登记（待收口）"
    else
      fail "$d_cell($d_ep) 登记为 class，但其 handler $d_fn 含版本发现调用——版本发现端点必须改登 strict 专行（B05-001）"
    fi
  fi
done < "$CLASS_ROWS"
rm -rf "$BODY_DIR"; rm -f "$ROUTE_MAP" "$CLASS_ROWS"

# ── 内核下发家族禁登小界（B11-001 常设门）──────────────────────────
# B11-001 家族：handler 函数体直接调用内核下发入口（applyKernel /
# applyKernelWith / applyChainReferrers / applyChangedTargets /
# applyWhitelistedTarget / applyKernelsForInbounds）的端点，其下发
# 在所钉内核未装版本时条件性升至 kernelApplyTimeout（300s）；此
# 前 kernelControl 与入站家族六格分别以 20s 界 / class 12s 登记，
# 对该条件升级全不可见。此类端点必须：①登记为 strict（单内核）
# 或 formula（批量）行；②sv 登记为内核下发放宽常量
#（kernelApplyWriteDeadline / inboundBatchWriteDeadline）；③
# handler 体以同一常量 extendWriteDeadline 武装。登记行带命名例
# 外（exception 列）时放行并在此点名（当前无存续例外）。机械定
# 义仅限函数体直调；传递性可达面（refreshInboundWhitelist 白名
# 单刷新——经其触达的端点共 12 个、realm 同步链、订阅凭据轮换
# 等）不在本规则内，仍按其各自登记行对账。变体自检见
# check-timeout-budgets-selftest.sh。
APPLY_ENTRY_RE='h\.(applyKernel|applyKernelWith|applyChainReferrers|applyChangedTargets|applyWhitelistedTarget|applyKernelsForInbounds)[[:space:]]*\('
REG_ROW_MAP="$(mktemp)"; ROUTE_MAP2="$(mktemp)"; BODY_DIR2="$(mktemp -d)"
grep -v '^#' "$REG" | tail -n +2 | awk -F'\t' '{ printf "%s\t%s\t%s\t%s\t%s\n", $4, $1, $15, $11, $16 }' > "$REG_ROW_MAP"
grep -rhoP --exclude='*_test.go' '"/api/v1[^"]*", h\.[A-Za-z0-9]+' "$ROOT/go-backend/internal/http" 2>/dev/null \
  | sed -E 's#"/api/v1([^"]*)", h\.(.*)#\1\t\2#' | sort -u > "$ROUTE_MAP2"
awk -v dir="$BODY_DIR2" '
  /^func \(h \*Handler\) [A-Za-z0-9]+\(/ {
    if (out) close(out)
    name = $0; sub(/^func \(h \*Handler\) /, "", name); sub(/\(.*/, "", name)
    out = dir "/" name; print > out; next
  }
  /^func / { if (out) { close(out); out = "" } }
  out { print >> out }
' "$HANDLER_DIR"/*.go 2>/dev/null
while IFS=$'\t' read -r a_ep a_fn; do
  [ -n "$a_fn" ] || continue
  [ -f "$BODY_DIR2/$a_fn" ] || continue
  grep -qE "$APPLY_ENTRY_RE" "$BODY_DIR2/$a_fn" || continue
  a_row="$(awk -F'\t' -v ep="$a_ep" '$1==ep { print; exit }' "$REG_ROW_MAP")"
  if [ -z "$a_row" ]; then
    fail "B11-001 家族：端点 $a_ep（handler $a_fn）函数体直调内核下发入口但未登记"
    continue
  fi
  a_cell="$(printf '%s' "$a_row" | cut -f2)"
  a_check="$(printf '%s' "$a_row" | cut -f3)"
  a_svsrc="$(printf '%s' "$a_row" | cut -f4)"
  a_exc="$(printf '%s' "$a_row" | cut -f5)"
  if [ -n "$a_exc" ]; then
    echo "NOTE: $a_cell($a_ep) 的 handler $a_fn 直调内核下发入口，以命名例外「$a_exc」保留 $a_check 登记（待收口）"
    continue
  fi
  case "$a_check" in
    strict|formula) ;;
    *) fail "$a_cell($a_ep) 的 handler $a_fn 函数体直调内核下发入口，却登记为 $a_check——内核下发家族必须改登 strict/formula 条件界行（B11-001）" ;;
  esac
  case "$a_svsrc" in
    kernelApplyWriteDeadline|inboundBatchWriteDeadline) ;;
    *) fail "$a_cell($a_ep) 服务端放宽登记为 ${a_svsrc:-（空）}，不是内核下发放宽常量（B11-001）" ;;
  esac
  grep -q "extendWriteDeadline(w, $a_svsrc)" "$BODY_DIR2/$a_fn" \
    || fail "$a_cell($a_ep) 登记放宽 $a_svsrc 但 handler $a_fn 未以该常量 extendWriteDeadline 武装（B11-001）"
done < "$ROUTE_MAP2"
rm -rf "$BODY_DIR2"; rm -f "$ROUTE_MAP2" "$REG_ROW_MAP"

# ── 普查差集：api/index.ts 全部导出调用点必须恰登记一次 ────────────────
CEN_FAIL=0
while read -r name; do
  [ -n "$name" ] || continue
  if ! grep -qx "$name" "$SEEN_CALLS"; then
    fail "普查差集：api/index.ts 调用点 $name 未登记（新增长操作调用点入库前先登记）"
    CEN_FAIL=1
  fi
done < <(cut -f1 "$FE_MAP")
REG_FE_COUNT="$(grep -c . "$SEEN_CALLS")"
MAP_COUNT="$(grep -c . "$FE_MAP")"
[ "$REG_FE_COUNT" = "$MAP_COUNT" ] || fail "普查计数不一致：登记表 fe/fe-raw 行 $REG_FE_COUNT ≠ index.ts 导出 $MAP_COUNT（重复登记或缺格）"
for s in diagnoseTunnelStream diagnoseForwardStream; do
  grep -qP "^export const $s\b" "$FE_STREAM" || fail "普查差集：diagnosis-stream.ts 表面 $s 不存在"
done
rm -f "$FE_MAP" "$SEEN_CALLS"

echo "----"
echo "timeout-budgets: $ROWS 格登记，普查 $MAP_COUNT 个 index.ts 调用点"
if [ "$FAIL" -eq 0 ]; then
  echo "RESULT: PASS"
else
  echo "RESULT: FAIL"
fi
exit "$FAIL"
