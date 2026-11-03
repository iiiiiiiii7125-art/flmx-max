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

# 入口不变量逐行核对（代码面、行首锚定整行）。
if ! printf '%s\n' "$ENTRYPOINT_CODE" | grep -Eq '^[[:space:]]*ulimit -c 0[[:space:]]*$'; then
  echo "FAIL: docker-entrypoint.sh lost the core-dump guard line: ulimit -c 0"
  FAIL=1
fi
if ! printf '%s\n' "$ENTRYPOINT_CODE" | grep -Eq '^[[:space:]]*cd /etc/flmx_agent[[:space:]]*$'; then
  echo "FAIL: docker-entrypoint.sh lost the data-directory anchor line: cd /etc/flmx_agent"
  FAIL=1
fi

# 内核状态根锚点按 Dockerfile 指令形态核对（代码面、行首锚定）。
if ! printf '%s\n' "$DOCKERFILE_CODE" | grep -Eq '^[[:space:]]*ENV[[:space:]]+FLMX_KERNEL_DIR=/etc/flmx_agent/kernels[[:space:]]*$'; then
  echo "FAIL: Dockerfile lost the kernel state anchor: ENV FLMX_KERNEL_DIR=/etc/flmx_agent/kernels"
  FAIL=1
fi

if [ "$FAIL" -ne 0 ]; then
  exit 1
fi
echo "PASS: agent image tool set complete ($REQUIRED_PACKAGES), entrypoint invariants and kernel state anchor intact"
exit 0
