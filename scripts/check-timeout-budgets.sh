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
# 前端提取口径（修复轮 7，R7-26）：index.ts 调用点普查、公式助
# 手参数、network.ts 默认值与 diagnosis-stream 常量改由
# scripts/timeout-fe-extract.mjs 以 TS 编译器 API 提取（旧
# grep/awk 首命中面可被模板字符串内的假 helper 定义遮蔽，见
# 脚本内「前端提取」节）；本门自此需 node＋typescript 可解析
# （与 vite-frontend 其余 check 同前提），助手不可用即前提失效
# exit 2，不回退文本提取。Go 侧影子树口径不变。
#
# 用法：check-timeout-budgets.sh [REPO_ROOT]（默认脚本所在仓库根）。
# 退出码：0 = 全格对账通过；1 = 存在漂移/缺格；2 = 前提失效。
set -u

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
ORIG_ROOT="$ROOT"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ── 代码面影子（修复轮 6 机制处置）──────────────────────────────
# 本门对 Go 源码的提取（go_const_s 首命中取值、WriteTimeout、各
# Timeout/WithTimeout 值锚、extendWriteDeadline 武装点计数、路由
# 与函数体普查）旧实现直接读原文：注释里的同形文本可满足取值型
# 断言——把期望值写进块注释、真身改值，首命中仍取注释值而全绿
# （第六轮审计观察项 go_const_s 即此形态）。RR4-C6-01 同族第二
# 形态：原始字符串内容跨行保留时，其中的声明形文本同样能冒充
# ——go_strip 现一并置空原始字符串内容（见 GO_STRIP_AWK 注）。
# 逐个 grep 点改造成本高，本轮按机制统一处置：开工先建影子树
# ——全树以符号链接镜
# 像（跳过 .git 与 vite-frontend/node_modules），go-backend 与
# go-gost 下每个 .go 文件替换为 go_strip 词法剥注释后的实文件；
# 此后本脚本的全部判定只对影子代码面进行。前端 TS、登记表与其
# 他文件经符号链接读原文，口径不变；影子在退出时清除。
# GO_STRIP_AWK：Go 词法剥注释状态机（注释整体丢弃，解释型字符
# 串/符文字面量内容原样保留——路由与字面量锚点提取需要；原始字
# 符串内容置空，仅保留首尾反引号）。原始字符串可跨行，其内容里
# 的声明形文本能以行首形态冒充真声明、遮蔽 go_const_s 的首命中
# 取值（RR4-C6-01：真常量前置一行原始字符串内的「SYM = 登记值」
# 即可让漂移全绿）——置空后行首锚定的取值型提取只可能命中真实
# 代码行。batch=1 时逐文件重置状态并把结果写到 <文件>.stripped；
# 否则写标准输出。
GO_STRIP_AWK='
  BEGIN { st = 0; q = sprintf("%c", 39); dest = "/dev/stdout" }
  FNR == 1 && batch { st = 0; dest = FILENAME ".stripped" }
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
        if (c == "`") { st = 0; out = out c }
      } else if (st == 5) {
        out = out c
        if (c == "\\") { out = out nx; i++ }
        else if (c == q) st = 0
      }
    }
    if (st == 1) st = 0
    print out > dest
  }'
go_strip() { awk "$GO_STRIP_AWK"; }
if [ -d "$ROOT/go-backend" ]; then
  SHADOW="$(mktemp -d)"
  trap 'rm -rf "$SHADOW"' EXIT
  # 只镜像本门实际读取的顶层目录（doc 登记表、前端 TS、两 Go 树）。
  for name in doc vite-frontend go-backend go-gost; do
    e="$ROOT/$name"
    [ -e "$e" ] || continue
    if [ "$name" = "vite-frontend" ] && [ -d "$e" ]; then
      mkdir -p "$SHADOW/vite-frontend"
      for sub in "$e"/*; do
        [ -e "$sub" ] || continue
        [ "$(basename "$sub")" = "node_modules" ] && continue
        cp -rs "$sub" "$SHADOW/vite-frontend/" 2>/dev/null || true
      done
    else
      cp -rs "$e" "$SHADOW/" 2>/dev/null || true
    fi
  done
  for d in go-backend go-gost; do
    [ -d "$SHADOW/$d" ] || continue
    find "$SHADOW/$d" -name '*.go' -type l -print0 \
      | xargs -0 -n 100 awk -v batch=1 "$GO_STRIP_AWK"
    find "$SHADOW/$d" -name '*.go.stripped' | while IFS= read -r s; do
      mv "$s" "${s%.stripped}"
    done
  done
  ROOT="$SHADOW"
fi

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

# ── 前端提取（修复轮 7，R7-26 / G-C4-09 机制处置）──────────────
# 前端三面（index.ts 调用点普查＋公式助手参数、network.ts 默认
# 值、diagnosis-stream 常量）旧实现为 grep/awk 文本首命中：顶部
# 模板字符串内放一行同名假 helper 定义（携登记值）即可遮蔽真
# helper 的参数漂移（PROBE7 实证九格全绿）。现改由
# scripts/timeout-fe-extract.mjs 以 TypeScript 编译器 API 按声
# 明与 AST 结构提取（模板/注释中的同名文本不是声明），结果 TSV
# 驱动下方全部前端判定。助手不可用（无 node / typescript 不可
# 解析 / 提取报错）属前提失效 exit 2——绝不回退文本提取静默缩
# 小证据面。助手读 ORIG_ROOT 的真实文件（影子树中的前端文件本
# 就是指向原文的符号链接，内容同一）。
FE_EXTRACT="$(mktemp)"
if ! command -v node >/dev/null 2>&1; then
  echo "FAIL: 前提失效，无 node 运行时，前端 AST 提取（timeout-fe-extract.mjs）不可用"
  exit 2
fi
if ! node "$SCRIPT_DIR/timeout-fe-extract.mjs" "$ORIG_ROOT" > "$FE_EXTRACT" 2>"$FE_EXTRACT.err"; then
  echo "FAIL: 前提失效，前端 AST 提取失败：$(cat "$FE_EXTRACT.err")"
  exit 2
fi
rm -f "$FE_EXTRACT.err"

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

if grep -q '^netdefault-multi' "$FE_EXTRACT"; then
  echo "FAIL: 前提失效，network.ts 各 timeout ?? 默认值不一致：$(grep '^netdefault-multi' "$FE_EXTRACT" | cut -f2)"
  exit 2
fi
NET_DEFAULT_MS="$(grep '^netdefault' "$FE_EXTRACT" | cut -f2 | head -1)"
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

# ── 前端调用点普查：name -> timeout 形态（AST 提取，见上） ──────
# FE_MAP 行格式：name \t kind \t value（kind/value 由助手按旧
# fe_extract 口径产出：formula 时 value 为 fe 口径助手名、
# dynamic/literal 时 value 为秒）。
FE_MAP="$(mktemp)"
grep '^call' "$FE_EXTRACT" | cut -f2-4 > "$FE_MAP"
# otherexpr：timeout 表达式非静态数值/助手形态，登记表无从对账
# ——按漂移点名（fail-closed，不静默归零）。
while IFS=$'\t' read -r _n _k _v; do
  [ "$_k" = "otherexpr" ] && fail "普查提取：api/index.ts 导出 $_n 的 timeout 表达式非静态数值/公式助手形态，登记表无法对账"
done < "$FE_MAP"

# fe_extract CALL -> "KIND VALUE"：KIND = default|literal|formula:<helper>|dynamic
fe_extract() {
  local call="$1" row kind value
  row="$(awk -F'\t' -v n="$call" '$1==n{ print; exit }' "$FE_MAP")"
  [ -n "$row" ] || { echo "MISSING"; return; }
  kind="$(printf '%s' "$row" | cut -f2)"
  value="$(printf '%s' "$row" | cut -f3)"
  case "$kind" in
    none) echo "default $NET_DEFAULT_S" ;;
    formula) echo "formula:$value" ;;
    dynamic) echo "dynamic $value" ;;
    literal) echo "literal $value" ;;
    *) echo "MISSING" ;;
  esac
}
# helper_params HELPER -> "CAP FLOOR"（秒；助手按声明结构提取）
helper_params() {
  local row cap floor
  row="$(awk -F'\t' -v n="$1" '$1=="helper" && $2==n{ print $3, $4; exit }' "$FE_EXTRACT")"
  [ -n "$row" ] || { echo ""; return; }
  read -r cap floor <<EOF
$row
EOF
  echo "$(eval_ms_expr "$cap") $(eval_ms_expr "$floor")"
}
STREAM_MS="$(grep '^stream' "$FE_EXTRACT" | cut -f2 | head -1)"
STREAM_S=""
[ -n "$STREAM_MS" ] && STREAM_S="$(awk -v v="$STREAM_MS" 'BEGIN{ printf "%g", v/1000 }')"

# be_literal_anchor CELL -> 提取的后端界（秒），无锚点输出空
be_literal_anchor() {
  case "$1" in
    T006) grep -qP '"RollbackAgent".*,\s*30\s*\*\s*time\.Second' "$HANDLER_DIR/upgrade.go" && echo 30 ;;
    T017|T018|T024|T061|T062|T063) grep -qP 'WithTimeout\(ctx,\s*20\s*\*\s*time\.Second\)' "$HANDLER_DIR/kernel_external.go" && echo 20 ;;
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
    # const 的 be_src 个别行带括注（如 F001/F003 的
    # "defaultNodeCommandTimeout (peer per-item)"）：符号名取首个
    # 空格前部分，括注只是说明——旧口径下整串作符号名提取恒空、
    # 这两格的后端界从未被验（与 R2-C6-02 同族）。
    const) be_x="$(go_const_s "${be_src%% (*}" "$HANDLER_DIR")" ;;
    literal) be_x="$(be_literal_anchor "$cell")" ;;
  esac
  # 提取为空即 fail-closed 点名（R2-C6-02）：const/literal 界的锚点或
  # 符号漂移/缺失时提取为空，旧口径下 strict/formula 的漂移比对被
  # 「空即跳过」静默吞掉（实证：kernel_external 20s→500s 门仍 PASS），
  # 边距还继续按登记值算。可机械求值的界提取不到只能是漂移或登记
  # 失效，必须红且点名；不可机械求值者的显式出口是 go_const_s 的
  # DEFER（值由 Go 门测试钉住），不受此条影响。
  if { [ "$be_kind" = "const" ] || [ "$be_kind" = "literal" ]; } && [ -z "$be_x" ]; then
    fail "$tag 后端界提取为空：锚点/符号「$be_src」未命中（漂移或缺失），fail-closed（R2-C6-02）"
  fi

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
      esac
      ;;
    pin)
      case "$cell" in
        P001) [ "$(go_const_s idemWaitTimeout "$HANDLER_DIR")" = "$be_s" ] || fail "$tag idemWaitTimeout 漂移（登记 ${be_s}s）" ;;
        P003)
          [ "$(go_const_s nodeTuneProbeTimeout "$HANDLER_DIR")" = "$be_s" ] || fail "$tag 面板 probe 界 nodeTuneProbeTimeout 漂移（登记 ${be_s}s）"
          # R4-C2-D-01：probe 链序对账。登记链序 agent(~25s) < FE < panel：
          # FE 的 nodeTune 导出对全部动作恒用同一字面值（T019 提取面），
          # 该值必须严格小于本格面板 probe 界，否则前端先于面板截断、
          # 链序与登记相反。FE 值经提取面机械取，不硬编码；提取非
          # 字面量即 fail-closed（链序无从对账）。
          read -r _p3k _p3x <<EOF
$(fe_extract nodeTune)
EOF
          [ "$_p3k" = "literal" ] || fail "$tag FE nodeTune 提取非字面量（${_p3k}），probe 链序无法对账，fail-closed"
          [ "$(gt_ok "$be_s" "$_p3x")" = 1 ] || fail "$tag probe 链序反转：FE nodeTune ${_p3x}s 不小于面板 probe 界 ${be_s}s（登记链序 agent < FE < panel）" ;;
        P002)
          j="$(go_const_s addServiceGuardJoinTimeout "$HANDLER_DIR")"; d="$(go_const_s defaultNodeCommandTimeout "$HANDLER_DIR")"
          [ "$(awk -v a="$j" -v b="$d" 'BEGIN{ print a+b }')" = "$be_s" ] || fail "$tag guard join 界漂移：${j}+${d} ≠ 登记 ${be_s}s" ;;
        P004) [ "$(go_const_s defaultNodeCommandTimeout "$HANDLER_DIR")" = "$be_s" ] || fail "$tag defaultNodeCommandTimeout 漂移（登记 ${be_s}s）" ;;
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
# 单刷新经其触达的端点、realm 同步链、订阅凭据轮换
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
  grep -qx "streamexport	$s" "$FE_EXTRACT" || fail "普查差集：diagnosis-stream.ts 表面 $s 不存在"
done
rm -f "$FE_MAP" "$SEEN_CALLS" "$FE_EXTRACT"

echo "----"
echo "timeout-budgets: $ROWS 格登记，普查 $MAP_COUNT 个 index.ts 调用点"
if [ "$FAIL" -eq 0 ]; then
  echo "RESULT: PASS"
else
  echo "RESULT: FAIL"
fi
exit "$FAIL"
