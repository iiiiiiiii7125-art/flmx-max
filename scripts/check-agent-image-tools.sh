#!/usr/bin/env bash
# check-agent-image-tools.sh — agent 镜像工具集与入口不变量常设门
# （AG-03 / A-ops-02）。
#
# go-gost/Dockerfile 的运行时工具集是 agent 在容器内的实际 exec
# 依赖：删掉任一包，构建与既有测试仍全绿、真机功能才炸——
#   nft (nftables)     内核 nftables 规则
#   modprobe (kmod)    调优模块加载
#   tcpkill (dsniff)   端口释放
#   ip/tc (iproute2)、pkill (procps)、iperf3
#                      调优引擎 prepareTuneEngine 的硬性 LookPath
#                      清单（连同基础镜像自带的 bash/timeout/awk）；
#                      缺装时引擎靠运行时 apt 自愈，离线节点直接失败
#   ca-certificates    TLS 出站
# 本门从 Dockerfile 的 apt-get install 块机械提取包名逐个断言，
# 缺一即 FAIL 并点名该包。
#
# 同时钉住 docker-entrypoint.sh 的两条入口不变量：ulimit -c 0
# （core 防护的镜像层）与 cd /etc/flmx_agent（数据目录锚点——
# agent 以相对路径加载 ./config.json、自卸载的清扫与标记都以该
# 目录为锚，cd 目标漂移会让容器形态自卸载再次整体失效）。
#
# 再钉住 Dockerfile 的内核状态根锚点：ENV
# FLMX_KERNEL_DIR=/etc/flmx_agent/kernels——内核二进制、state.json
# 与 geodata 必须落挂载卷，缺了这行就回落到脚本形态的默认根，
# 在镜像里只是容器可写层，容器一重建全部蒸发（R2-003）。
#
# 提取面（修复轮 6，R6-C4-07 机制处置）：旧实现对两文件整文件
# grep -F，三处不变量均可被「把该行注释掉」满足（注释行仍含字面
# 量）。现判定前先滤除注释行（Dockerfile 与 sh 同口径：首个非空
# 白字符为 # 的行），入口两条另剥行尾注释后按行首锚定整行匹配，
# ENV 锚按 Dockerfile 指令形态锚定；apt 块提取同样在滤除注释行
# 的文本上进行（块内的注释行包名不计数）。自检新增注释形态两
# 变体钉死此口径。
#
# 生效值口径（修复轮 7，R7-07 / G-C4-01 机制处置）：轮 6 的存在性
# 锚仍可被「锚行之后追加覆盖行」整体架空——Dockerfile 尾部再写
# 一行 ENV FLMX_KERNEL_DIR=…、entrypoint 尾部再写 ulimit -c
# unlimited / cd /tmp，三条不变量运行时全灭而门全绿。本轮三锚点
# 改按生效状态求值：ENV 按 Dockerfile 指令流求最后阶段的末次赋
# 值（FROM 引用已命名阶段时继承该阶段环境、引用外部基础镜像时
# 清空，与 docker build 的阶段语义一致）；entrypoint 两锚按代码
# 面末次出现值判定（锚行之后出现覆盖行即 FAIL 并点名生效值）。
# apt 包集仍是集合存在性（包无覆盖语义），维持轮 6 注释滤除。
#
# 命令流口径（修复轮 8，R8-04 / H-C4-03 机制处置）：轮 7 的
# entrypoint 求值只认行首命令形态——把 exec 行改写为子壳复合形
# 态 (cd /tmp && ulimit -c unlimited && exec …) 后两锚整体不可
# 见、废值照绿。本轮改按 shell 命令流求值：代码面先拼续行，再
# 逐字扫描切分命令序列（&&/||/;/&/|/换行/子壳括号/命令替换，
# 引号与转义感知），cd 与 ulimit -c 的生效值取「启动 agent 的
# exec 时点、沿其命令链的末次实际赋值」：子壳内赋值随子壳消亡
# 而丢弃、管道与后台段的赋值不外泄（/bin/sh 语义），exec 之前
# 的同链赋值（含复合行内形态）全部入选；经 env/sudo 等包装器转
# 手的 exec、命令替换内的 exec 等模型外形态以不可解析 FAIL 点
# 名（fail-closed），不静默放行。
# 同轮另补两面建模：① ENV 求值解析同阶段 ARG 默认值（ARG KDIR=
# …＋ENV …=${KDIR} 的间接形态按默认值求生效值；引用不可静态
# 解析的变量 → 点名 FAIL，不再与字面值混报）；② apt 包名只取
# apt-get install 命令自身实参跨度内的 token（首个 &&/||/; 之
# 前），同块后续命令（如 echo）里的包名不再冒充已装（关闭第七
# 轮登记 R-11 的门内逃逸面；包集的真机兜底口径不变）。
# 命令流模型保留轮 7 的文件尾纪律：exec 时点快照之外，流结束
# 时仍存活帧内的末次赋值（即 exec 之后追加的顶层覆盖行）同样
# 按覆盖 FAIL 点名——快照管到达 exec 的链、存活值管文件尾，
# 任一与钉值不符即红。
#
# 用法：check-agent-image-tools.sh [REPO_ROOT]（默认脚本所在仓库根）。
# 退出码：0 = 工具集与入口不变量齐备；1 = 存在缺失；2 = 前提失效。
set -u

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
DOCKERFILE="$ROOT/go-gost/Dockerfile"
ENTRYPOINT="$ROOT/go-gost/docker-entrypoint.sh"

REQUIRED_PACKAGES="ca-certificates nftables kmod dsniff iproute2 procps iperf3"

if [ ! -f "$DOCKERFILE" ] || [ ! -f "$ENTRYPOINT" ]; then
  echo "PREMISE FAIL: go-gost/Dockerfile or go-gost/docker-entrypoint.sh missing under $ROOT"
  exit 2
fi

# 代码面：滤除注释行后的文本（注释行＝首个非空白字符为 #）。
DOCKERFILE_CODE="$(grep -v '^[[:space:]]*#' "$DOCKERFILE")"
ENTRYPOINT_CODE="$(grep -v '^[[:space:]]*#' "$ENTRYPOINT" | sed 's/[[:space:]]#.*$//')"

FAIL=0

# 包名核对（R8-04 ②）：把代码面拼成逻辑行，取每处 apt-get
# install 命令的实参跨度（自 apt-get install 起、到首个命令终
# 结符 &&/||/; 或逻辑行尾止），只在跨度内按 token 核对包名——
# 后续命令的文本不计数。选项 token（- 开头）与赋值形态（含 =）
# 不作包名。
INSTALL_TOKENS="$(printf '%s\n' "$DOCKERFILE_CODE" | awk '
  {
    line = $0
    while (line ~ /\\[[:space:]]*$/) {
      sub(/\\[[:space:]]*$/, "", line)
      if ((getline nxt) > 0) line = line nxt; else break
    }
    rest = line
    while ((idx = index(rest, "apt-get install")) > 0) {
      span = substr(rest, idx)
      cut = length(span) + 1
      for (k = 1; k <= length(span); k++) {
        two = substr(span, k, 2)
        if (two == "&&" || two == "||" || substr(span, k, 1) == ";") { cut = k; break }
      }
      span = substr(span, 1, cut - 1)
      m = split(span, toks, /[[:space:]]+/)
      for (i = 1; i <= m; i++) {
        t = toks[i]
        gsub(/^"|"$/, "", t)
        if (t == "" || t == "apt-get" || t == "install") continue
        if (t ~ /^-/) continue
        if (index(t, "=") > 0) continue
        print t
      }
      rest = substr(rest, idx + 13)
    }
  }
')"
if [ -z "$INSTALL_TOKENS" ]; then
  echo "PREMISE FAIL: no apt-get install command found in $DOCKERFILE"
  exit 2
fi
for pkg in $REQUIRED_PACKAGES; do
  if ! printf '%s\n' "$INSTALL_TOKENS" | grep -Fxq "$pkg"; then
    echo "FAIL: agent image tool missing from Dockerfile apt-get install block: $pkg"
    FAIL=1
  fi
done

# 入口不变量按命令流生效值核对（R8-04）：扫描器逐字切分命令序
# 列、在 exec 时点快照沿链生效的 cd / ulimit -c（模型见文件头注
# ）。输出四行（exec 快照 ulimit/cd、流尾存活 ulimit/cd；无
# exec 时首两行即存活值）；模型外 exec 形态输出
# __UNPARSEABLE__ ＋命中的命令段文本。
ENTRY_EFF="$(printf '%s\n' "$ENTRYPOINT_CODE" | awk '
  BEGIN {
    sp = 1; cdv[1] = ""; ulv[1] = ""; substf[1] = 0; fid[1] = 1; fidseq = 1
    bq = 0; q = 0; esc = 0; expd = 0
    seg = ""; tok = ""; intok = 0
    inpipe = 0; sv_ul = ""; sv_cd = ""
    have_snap = 0; snap_ul = ""; snap_cd = ""
    unpar = ""
    ncc = 0; ncu = 0
    nw = split("env sudo xargs nohup setsid stdbuf timeout nice ionice flock", wtmp, " ")
    for (wi = 1; wi <= nw; wi++) wrap[wtmp[wi]] = 1
  }
  function endtok() {
    if (intok) { seg = seg (seg == "" ? "" : "\034") tok; tok = ""; intok = 0 }
  }
  function process(    n, i, j, head, segtext) {
    if (seg == "") return
    n = split(seg, pt, "\034")
    segtext = pt[1]
    for (j = 2; j <= n; j++) segtext = segtext " " pt[j]
    i = 1
    while (i <= n && pt[i] ~ /^[A-Za-z_][A-Za-z0-9_]*=/) i++
    while (i <= n && (pt[i] == "if" || pt[i] == "then" || pt[i] == "else" || pt[i] == "elif" || pt[i] == "while" || pt[i] == "until" || pt[i] == "do" || pt[i] == "!" || pt[i] == "{" || pt[i] == "}")) i++
    if (i > n) return
    head = pt[i]
    if (head == "builtin" || head == "command") {
      i++
      if (i > n) return
      head = pt[i]
    }
    if (head in wrap) {
      for (j = i + 1; j <= n; j++) {
        if (pt[j] == "exec" && unpar == "") unpar = segtext
      }
      return
    }
    if (head == "exec") {
      # 纯重定向 exec（无启动目标）不构成启动点，不快照。
      has = 0
      for (j = i + 1; j <= n; j++) {
        if (pt[j] ~ /^[0-9]*>/ || pt[j] ~ /^[0-9]*</) continue
        has = 1; break
      }
      if (!has) return
      for (j = 1; j <= sp; j++) {
        if (substf[j]) { if (unpar == "") unpar = segtext; return }
      }
      snap_ul = ulv[sp]; snap_cd = cdv[sp]; have_snap = 1
      return
    }
    if (head == "cd") {
      cdv[sp] = (i < n ? pt[i + 1] : "($HOME)")
      ncc++; cc_fid[ncc] = fid[sp]; cc_val[ncc] = cdv[sp]
      return
    }
    if (head == "ulimit") {
      found = 0
      for (j = i + 1; j <= n; j++) {
        if (pt[j] == "-c" && j < n) { ulv[sp] = pt[j + 1]; found = 1 }
        else if (pt[j] ~ /^-c./) { ulv[sp] = substr(pt[j], 3); found = 1 }
      }
      if (found) { ncu++; cu_fid[ncu] = fid[sp]; cu_val[ncu] = ulv[sp] }
      return
    }
  }
  function flush(sep, sf,    sul, scd) {
    endtok()
    sul = ulv[sp]; scd = cdv[sp]
    process()
    seg = ""
    if (sep == "PIPE") {
      if (!inpipe) { sv_ul = sul; sv_cd = scd; inpipe = 1 }
      ulv[sp] = sv_ul; cdv[sp] = sv_cd
    } else if (sep == "BG") {
      ulv[sp] = sul; cdv[sp] = scd
    } else {
      if (inpipe) { ulv[sp] = sv_ul; cdv[sp] = sv_cd; inpipe = 0 }
    }
    if (sep == "LP") {
      sp++
      cdv[sp] = cdv[sp - 1]; ulv[sp] = ulv[sp - 1]; substf[sp] = sf
      fidseq++; fid[sp] = fidseq
      qsave[sp] = q; q = 0; esc = 0
    } else if (sep == "RP") {
      if (sp > 1) { q = qsave[sp]; sp-- }
      esc = 0
    }
  }
  function addc(c) { tok = tok c; intok = 1 }
  {
    line = $0
    while (line ~ /\\[[:space:]]*$/) {
      sub(/\\[[:space:]]*$/, "", line)
      if ((getline nxt) > 0) line = line nxt; else break
    }
    text = line "\n"
    L = length(text)
    for (p = 1; p <= L; p++) {
      c = substr(text, p, 1)
      nx = (p < L ? substr(text, p + 1, 1) : "")
      if (esc) { addc(c); esc = 0; continue }
      if (q == 1) {
        if (c == "'\''") q = 0; else addc(c)
        continue
      }
      if (expd > 0) {
        # ${...} 展开内部不切分命令。
        if (c == "$" && nx == "{") { expd++; addc(c); continue }
        if (c == "}") { expd--; addc(c); continue }
        addc(c); continue
      }
      if (c == "\\" && q != 1) { esc = 1; continue }
      if (c == "'\''" && q == 0) { q = 1; intok = 1; continue }
      if (c == "\"") {
        if (q == 0) { q = 2; intok = 1; continue }
        if (q == 2) { q = 0; continue }
      }
      if (q == 2 && c == "$" && nx == "(") { flush("LP", 1); p++; continue }
      if (q == 2 && c == "`") {
        if (!bq) { flush("LP", 1); bq = 1 } else { flush("RP", 0); bq = 0 }
        continue
      }
      if (q != 0) {
        if (c == "\n") addc(" "); else addc(c)
        continue
      }
      # 无引号区。
      if (c == "$" && nx == "{") { expd = 1; addc(c); continue }
      if (c == "$" && nx == "(") { flush("LP", 1); p++; continue }
      if (c == "`") {
        if (!bq) { flush("LP", 1); bq = 1 } else { flush("RP", 0); bq = 0 }
        continue
      }
      if (c == " " || c == "\t") { endtok(); continue }
      if (c == "\n") { flush("NL", 0); continue }
      if (c == ";") { flush("SEMI", 0); continue }
      if (c == "&") {
        if (nx == "&") { flush("AND", 0); p++ } else { flush("BG", 0) }
        continue
      }
      if (c == "|") {
        if (nx == "|") { flush("OR", 0); p++ } else { flush("PIPE", 0) }
        continue
      }
      if (c == "(") { flush("LP", 0); continue }
      if (c == ")") { flush("RP", 0); continue }
      addc(c)
    }
  }
  END {
    flush("EOF", 0)
    # 存活末值：流结束时仍在栈帧内的赋值中序号最大者（按帧实例
    # 存活判定，已弹出帧的赋值不算）。
    surv_ul = ""; surv_cd = ""
    for (k = ncu; k >= 1; k--) {
      live = 0
      for (j = 1; j <= sp; j++) if (fid[j] == cu_fid[k]) { live = 1; break }
      if (live) { surv_ul = cu_val[k]; break }
    }
    for (k = ncc; k >= 1; k--) {
      live = 0
      for (j = 1; j <= sp; j++) if (fid[j] == cc_fid[k]) { live = 1; break }
      if (live) { surv_cd = cc_val[k]; break }
    }
    if (unpar != "") {
      printf "__UNPARSEABLE__\n%s\n", unpar
    } else if (have_snap) {
      printf "%s\n%s\n%s\n%s\n", snap_ul, snap_cd, surv_ul, surv_cd
    } else {
      printf "%s\n%s\n\n\n", surv_ul, surv_cd
    }
  }
')"
EFF_ULIMIT="$(printf '%s\n' "$ENTRY_EFF" | sed -n '1p')"
EFF_CD="$(printf '%s\n' "$ENTRY_EFF" | sed -n '2p')"
SURV_ULIMIT="$(printf '%s\n' "$ENTRY_EFF" | sed -n '3p')"
SURV_CD="$(printf '%s\n' "$ENTRY_EFF" | sed -n '4p')"
if [ "$EFF_ULIMIT" = "__UNPARSEABLE__" ]; then
  echo "FAIL: docker-entrypoint.sh exec form is outside the gate command-flow model: '$EFF_CD'"
  FAIL=1
else
  if [ -z "$EFF_ULIMIT" ]; then
    echo "FAIL: docker-entrypoint.sh lost the core-dump guard line: ulimit -c 0"
    FAIL=1
  elif [ "$EFF_ULIMIT" != "0" ]; then
    echo "FAIL: docker-entrypoint.sh core-dump guard overridden: effective ulimit -c is '$EFF_ULIMIT', expected ulimit -c 0 as the last ulimit -c on the exec chain"
    FAIL=1
  fi
  if [ -z "$EFF_CD" ]; then
    echo "FAIL: docker-entrypoint.sh lost the data-directory anchor line: cd /etc/flmx_agent"
    FAIL=1
  elif [ "$EFF_CD" != "/etc/flmx_agent" ]; then
    echo "FAIL: docker-entrypoint.sh data-directory anchor overridden: effective cd target is '$EFF_CD', expected cd /etc/flmx_agent as the last cd on the exec chain"
    FAIL=1
  fi
  # 文件尾纪律（轮 7 存续）：exec 之后仍存活的覆盖赋值同按覆盖论。
  if [ -n "$SURV_ULIMIT" ] && [ "$SURV_ULIMIT" != "0" ]; then
    echo "FAIL: docker-entrypoint.sh core-dump guard overridden after the exec point: surviving ulimit -c is '$SURV_ULIMIT', expected ulimit -c 0"
    FAIL=1
  fi
  if [ -n "$SURV_CD" ] && [ "$SURV_CD" != "/etc/flmx_agent" ]; then
    echo "FAIL: docker-entrypoint.sh data-directory anchor overridden after the exec point: surviving cd target is '$SURV_CD', expected cd /etc/flmx_agent"
    FAIL=1
  fi
fi

# 内核状态根锚点按 Dockerfile 生效 ENV 求值（R7-07；ARG 解析为
# R8-04 ①）：把代码面指令流（续行已拼接）按阶段求值——FROM 引
# 用先前命名阶段则继承其环境、引用外部镜像则清空；ENV 支持
# K=V 多对与旧式 K V 两形态；ENV 值中的 $VAR/${VAR} 按同阶段
# 先行 ENV 与 ARG 默认值解析（ARG 在阶段内重声明裸名时继承全
# 局默认值），不可解析者以 UNRESOLVED 前缀原样输出、由门点名
# FAIL；最终阶段 FLMX_KERNEL_DIR 的末次赋值即生效值。
KERNEL_DIR_EFF="$(printf '%s\n' "$DOCKERFILE_CODE" | awk '
  function resolve(val,    out, pre, rest, name, rep, k) {
    out = ""
    rest = val
    while (length(rest) > 0) {
      if (substr(rest, 1, 1) != "$") {
        k = index(rest, "$")
        if (k == 0) { out = out rest; rest = "" }
        else { out = out substr(rest, 1, k - 1); rest = substr(rest, k) }
        continue
      }
      if (substr(rest, 2, 1) == "{") {
        k = index(rest, "}")
        if (k == 0) return "\034UNRESOLVED\034" val
        name = substr(rest, 3, k - 3)
        if (name ~ /[^A-Za-z0-9_]/) return "\034UNRESOLVED\034" val
        rest = substr(rest, k + 1)
      } else if (substr(rest, 2, 1) ~ /[A-Za-z_]/) {
        if (!match(rest, /^\$[A-Za-z_][A-Za-z0-9_]*/)) return "\034UNRESOLVED\034" val
        name = substr(rest, 2, RLENGTH - 1)
        rest = substr(rest, RLENGTH + 1)
      } else {
        return "\034UNRESOLVED\034" val
      }
      if (name in env) rep = env[name]
      else if (name in arg) rep = arg[name]
      else if (name in garg) rep = garg[name]
      else return "\034UNRESOLVED\034" val
      if (substr(rep, 1, 1) == "\034") return "\034UNRESOLVED\034" val
      out = out rep
    }
    return out
  }
  {
    line = $0
    while (line ~ /\\[[:space:]]*$/) {
      sub(/\\[[:space:]]*$/, "", line)
      if ((getline nxt) > 0) line = line nxt; else break
    }
    instr = line
    sub(/^[[:space:]]+/, "", instr)
    if (instr == "") next
    wn = split(instr, w, /[[:space:]]+/)
    kw = toupper(w[1])
    if (kw == "FROM") {
      # 先把上一阶段环境按其阶段名存档，再切换；ARG 不跨阶段。
      if (stage_name != "") {
        for (k in env) saved[stage_name SUBSEP k] = env[k]
        known[stage_name] = 1
      }
      delete env
      delete arg
      img = w[2]
      if (img in known) {
        for (key in saved) {
          split(key, parts, SUBSEP)
          if (parts[1] == img) env[parts[2]] = saved[key]
        }
      }
      stage_name = ""
      for (i = 3; i < wn; i++) {
        if (toupper(w[i]) == "AS") { stage_name = w[i + 1]; break }
      }
      seen_from = 1
    } else if (kw == "ARG") {
      rest = instr
      sub(/^[[:space:]]*[^[:space:]]+[[:space:]]+/, "", rest)
      first = rest; sub(/[[:space:]].*$/, "", first)
      eq = index(first, "=")
      if (eq > 0) {
        aname = substr(first, 1, eq - 1)
        aval = substr(rest, eq + 1)
        sub(/[[:space:]].*$/, "", aval)
        gsub(/^"|"$/, "", aval)
        if (seen_from) arg[aname] = resolve(aval); else garg[aname] = resolve(aval)
      } else {
        aname = first
        if (seen_from) {
          if (aname in garg) arg[aname] = garg[aname]
          else arg[aname] = "\034UNRESOLVED\034" aname
        } else {
          garg[aname] = "\034UNRESOLVED\034" aname
        }
      }
    } else if (kw == "ENV") {
      rest = instr
      sub(/^[[:space:]]*[^[:space:]]+[[:space:]]+/, "", rest)
      first = rest; sub(/[[:space:]].*$/, "", first)
      if (index(first, "=") > 0) {
        m = split(rest, toks, /[[:space:]]+/)
        for (i = 1; i <= m; i++) {
          eq = index(toks[i], "=")
          if (eq > 0) {
            k = substr(toks[i], 1, eq - 1)
            v = substr(toks[i], eq + 1)
            gsub(/^"|"$/, "", v)
            env[k] = resolve(v)
          }
        }
      } else {
        v = rest
        sub(/^[^[:space:]]+[[:space:]]+/, "", v)
        env[first] = resolve(v)
      }
    }
  }
  END { print env["FLMX_KERNEL_DIR"] }
')"
case "$KERNEL_DIR_EFF" in
  $'\034'UNRESOLVED$'\034'*)
    echo "FAIL: Dockerfile kernel state anchor is not statically resolvable: ENV FLMX_KERNEL_DIR references a variable with no in-stage default ('${KERNEL_DIR_EFF#$'\034'UNRESOLVED$'\034'}')"
    FAIL=1
    ;;
  "")
    echo "FAIL: Dockerfile lost the kernel state anchor: ENV FLMX_KERNEL_DIR=/etc/flmx_agent/kernels"
    FAIL=1
    ;;
  "/etc/flmx_agent/kernels")
    ;;
  *)
    echo "FAIL: Dockerfile kernel state anchor overridden: effective ENV FLMX_KERNEL_DIR is '$KERNEL_DIR_EFF', expected /etc/flmx_agent/kernels as the final-stage last assignment"
    FAIL=1
    ;;
esac

if [ "$FAIL" -ne 0 ]; then
  exit 1
fi
echo "PASS: agent image tool set complete ($REQUIRED_PACKAGES), entrypoint invariants and kernel state anchor intact"
exit 0
