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

# 提取 apt-get install 块：自含 "apt-get install" 的行起，到第一个
# 不以反斜杠结尾的行止（块的最后一行）。
BLOCK="$(printf '%s\n' "$DOCKERFILE_CODE" | awk '/apt-get install/{f=1} f{print} f && $0 !~ /\\[[:space:]]*$/{exit}')"
if [ -z "$BLOCK" ]; then
  echo "PREMISE FAIL: no apt-get install block found in $DOCKERFILE"
  exit 2
fi

FAIL=0

# 包名按 token 逐个核对（去续行符后按空白切词、整词相等）。
TOKENS="$(printf '%s\n' "$BLOCK" | tr -d '\\' | tr -s ' \t' '\n')"
for pkg in $REQUIRED_PACKAGES; do
  if ! printf '%s\n' "$TOKENS" | grep -Fxq "$pkg"; then
    echo "FAIL: agent image tool missing from Dockerfile apt-get install block: $pkg"
    FAIL=1
  fi
done

# 入口不变量按生效值核对（R7-07）：在代码面上取 ulimit -c 与 cd
# 的末次出现值——锚行之后再出现覆盖行时，生效值即覆盖值，FAIL
# 并点名；一处都不出现同样 FAIL（不变量缺失）。
ENTRY_EFF="$(printf '%s\n' "$ENTRYPOINT_CODE" | awk '
  /^[[:space:]]*ulimit[[:space:]]+-c[[:space:]]+[^[:space:]]+/ {
    v = $0; sub(/^[[:space:]]*ulimit[[:space:]]+-c[[:space:]]+/, "", v)
    split(v, a, /[[:space:]]/); ulimit_v = a[1]
  }
  /^[[:space:]]*cd[[:space:]]+[^[:space:]]+/ {
    v = $0; sub(/^[[:space:]]*cd[[:space:]]+/, "", v)
    split(v, a, /[[:space:]]/); cd_v = a[1]
  }
  END { printf "%s\n%s\n", ulimit_v, cd_v }
')"
EFF_ULIMIT="$(printf '%s\n' "$ENTRY_EFF" | sed -n '1p')"
EFF_CD="$(printf '%s\n' "$ENTRY_EFF" | sed -n '2p')"
if [ -z "$EFF_ULIMIT" ]; then
  echo "FAIL: docker-entrypoint.sh lost the core-dump guard line: ulimit -c 0"
  FAIL=1
elif [ "$EFF_ULIMIT" != "0" ]; then
  echo "FAIL: docker-entrypoint.sh core-dump guard overridden: effective ulimit -c is '$EFF_ULIMIT', expected ulimit -c 0 as the last ulimit -c line"
  FAIL=1
fi
if [ -z "$EFF_CD" ]; then
  echo "FAIL: docker-entrypoint.sh lost the data-directory anchor line: cd /etc/flmx_agent"
  FAIL=1
elif [ "$EFF_CD" != "/etc/flmx_agent" ]; then
  echo "FAIL: docker-entrypoint.sh data-directory anchor overridden: effective cd target is '$EFF_CD', expected cd /etc/flmx_agent as the last cd line"
  FAIL=1
fi

# 内核状态根锚点按 Dockerfile 生效 ENV 求值（R7-07）：把代码面
# 指令流（续行已拼接）按阶段求值——FROM 引用先前命名阶段则继承
# 其环境、引用外部镜像则清空；ENV 支持 K=V 多对与旧式 K V 两形
# 态；最终阶段 FLMX_KERNEL_DIR 的末次赋值即生效值。
KERNEL_DIR_EFF="$(printf '%s\n' "$DOCKERFILE_CODE" | awk '
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
      # 先把上一阶段环境按其阶段名存档，再切换。
      if (stage_name != "") {
        for (k in env) saved[stage_name SUBSEP k] = env[k]
        known[stage_name] = 1
      }
      delete env
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
            env[k] = v
          }
        }
      } else {
        v = rest
        sub(/^[^[:space:]]+[[:space:]]+/, "", v)
        env[first] = v
      }
    }
  }
  END { print env["FLMX_KERNEL_DIR"] }
')"
if [ -z "$KERNEL_DIR_EFF" ]; then
  echo "FAIL: Dockerfile lost the kernel state anchor: ENV FLMX_KERNEL_DIR=/etc/flmx_agent/kernels"
  FAIL=1
elif [ "$KERNEL_DIR_EFF" != "/etc/flmx_agent/kernels" ]; then
  echo "FAIL: Dockerfile kernel state anchor overridden: effective ENV FLMX_KERNEL_DIR is '$KERNEL_DIR_EFF', expected /etc/flmx_agent/kernels as the final-stage last assignment"
  FAIL=1
fi

if [ "$FAIL" -ne 0 ]; then
  exit 1
fi
echo "PASS: agent image tool set complete ($REQUIRED_PACKAGES), entrypoint invariants and kernel state anchor intact"
exit 0
