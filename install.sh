#!/bin/sh
# shellcheck shell=bash

# Alpine 默认不带 Bash。先用系统自带的 /bin/sh 安装/切换到 Bash，
# 后续主体继续使用 Bash 语法，避免要求用户手动准备运行环境。
if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  fi

  if [ -f /etc/alpine-release ] && command -v apk >/dev/null 2>&1; then
    if [ "$(id -u)" -eq 0 ]; then
      apk add --no-cache bash
    elif command -v sudo >/dev/null 2>&1; then
      sudo apk add --no-cache bash
    elif command -v doas >/dev/null 2>&1; then
      doas apk add --no-cache bash
    else
      echo "❌ Alpine 安装需要 root 权限，或已配置 sudo/doas。" >&2
      exit 1
    fi

    exec bash "$0" "$@"
  fi

  echo "❌ 此安装脚本需要 Bash。" >&2
  exit 1
fi

# GitHub repo used for release downloads
REPO="iiiiiiiii7125-art/flmx-max"

# 固定版本号（Release 构建时自动填充，留空则获取最新版）
PINNED_VERSION=""

# 获取系统架构
get_architecture() {
    ARCH=$(uname -m)
    case $ARCH in
        x86_64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            # AUDIT-R1 (SCR-007): 未知架构不得静默回退 amd64（releases 只有
            # amd64/arm64 资产），否则下载的二进制无法执行，失败定位极晚。
            echo "❌ 不支持的系统架构：$ARCH（仅支持 x86_64/amd64、aarch64/arm64）" >&2
            return 1
            ;;
    esac
}

# 安装目录（现行布局）
INSTALL_DIR="/etc/flmx_agent"
FLMX_AGENT_SYSTEMD_SERVICE_FILE="/etc/systemd/system/flmx_agent.service"
FLMX_AGENT_OPENRC_SERVICE_FILE="/etc/init.d/flmx_agent"
# OpenRC 服务文件里 pidfile="/run/${RC_SVCNAME}.pid"，服务名恒为 flmx_agent。
# pidfile 覆盖变量改名后以 FLMX_ 为准，旧 FLUX_ 变量仅回退读。
FLMX_AGENT_OPENRC_PIDFILE="${FLMX_AGENT_OPENRC_PIDFILE:-${FLUX_AGENT_OPENRC_PIDFILE:-/run/flmx_agent.pid}}"
# 改名前的存量布局（识别集，本轮只加不删）：存量节点经脚本更新
# 迁移到新布局之前一直是这套目录/服务名。更新路据此定位并整体
# 迁移；卸载与足迹判定对未迁存量仍须认它，故下列旧名常量只用于
# 「识别/迁移/清除存量」，绝不作为新装落点。
LEGACY_INSTALL_DIR="/etc/flux_agent"
LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="/etc/systemd/system/flux_agent.service"
LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE="/etc/init.d/flux_agent"
# 用户取消标志（R2-F7-01，与 panel_install.sh 的 PANEL_OP_CANCELLED
# 同口径）：卸载确认答非 y 时由 uninstall_flmx_agent 置 "1"，main
# 见标志不调 delete_self、以 0 退出——取消不是「操作已完成」，脚
# 本保留以便直接重跑。
AGENT_OP_CANCELLED=""
# 卸载时待清理的自定义内核目录（R2-F7-I02），由
# resolve_custom_kernel_dir 在卸载入口判定后写入；空＝无需单独清理。
CUSTOM_KERNEL_DIR=""
LEGACY_GOST_BINARY="/usr/local/bin/gost"
LEGACY_GOST_CONFIG_DIR="/etc/gost"
LEGACY_GOST_SERVICE_FILE_ETC="/etc/systemd/system/gost.service"
LEGACY_GOST_SERVICE_FILE_LIB="/lib/systemd/system/gost.service"
LEGACY_GOST_SERVICE_FILE_USR_LIB="/usr/lib/systemd/system/gost.service"
SERVICE_MANAGER="${SERVICE_MANAGER:-}"

# 宿主级调优残留路径：agent 运行时写在安装目录之外的文件，与 agent
# 侧 go-gost/x/socket/tuning.go 的路径常量一一对应（tuneConfPath /
# legacyTuneConfPath / tuneBBRModule / tuneShapeUnitPath）。脚本卸载
# 必须与面板自卸载路径（runAgentUninstall）对齐清理这些文件，见
# uninstall_flmx_agent_host_cleanup。做成变量以便测试夹具改指临时
# 目录。
FLMX_TUNING_SYSCTL_CONF="/etc/sysctl.d/99-flmx-tuning.conf"
FLMX_TUNING_SYSCTL_CONF_LEGACY="/etc/sysctl.d/99-flvx-tuning.conf"
FLMX_TUNING_BBR_MODULE_FILE="/etc/modules-load.d/flmx-bbr.conf"
FLMX_TUNING_QDISC_UNIT_FILE="/etc/systemd/system/flmx-tuning-qdisc.service"
FLMX_TUNING_QDISC_UNIT="flmx-tuning-qdisc.service"
FLMX_PROC_SYS_DIR="/proc/sys"
# agent 自建 nft 表全集，与 agent 侧 x/kernel/nft.go 的 removeTables
# 逐字对齐（计数/白名单主表、改名前的旧表、端口跳跃探针表）。表驻
# 留在内核里、不随进程退出消失，必须在卸载时显式删除。
FLMX_AGENT_NFT_TABLES="flmx_kernel flvx_kernel flmx_hop_probe"

# GitHub 下载路线配置（1.7.7）：默认走内置候选测速直选（见下方
# PROXY_CANDIDATES 与 ensure_download_routes），不再交互询问、不再
# 读面板的「GitHub 加速」设置（该设置已整体移除）。环境变量仅作
# 运维逃生口：PROXY_ENABLED=false 强制直连；PROXY_URL=<前缀> 为
# 手动覆盖，固定单条代理前缀路线（并落盘到 agent 配置）。
PROXY_ENABLED="${PROXY_ENABLED:-}"
PROXY_URL="${PROXY_URL:-}"

ensure_alpine_runtime_dependencies() {
  [[ -f /etc/alpine-release ]] || return 0

  local missing_packages=()
  local privileged_command=""

  command -v curl >/dev/null 2>&1 || missing_packages+=(curl)
  [[ -f /etc/ssl/certs/ca-certificates.crt ]] || missing_packages+=(ca-certificates)

  if [[ ${#missing_packages[@]} -eq 0 ]]; then
    return 0
  fi

  if [[ $EUID -ne 0 ]]; then
    if command -v sudo >/dev/null 2>&1; then
      privileged_command="sudo"
    elif command -v doas >/dev/null 2>&1; then
      privileged_command="doas"
    else
      echo "❌ Alpine 安装需要 root 权限，或已配置 sudo/doas 来安装依赖: ${missing_packages[*]}。" >&2
      return 1
    fi
  fi

  echo "📦 Alpine 缺少运行依赖，正在安装: ${missing_packages[*]}"
  if [[ -n "$privileged_command" ]]; then
    "$privileged_command" apk add --no-cache "${missing_packages[@]}"
  else
    apk add --no-cache "${missing_packages[@]}"
  fi
}

# ---- 内置 GitHub 下载候选与测速直选（1.7.7）----
# 候选清单全产品统一（面板后端 github_routes.go、agent 侧、
# panel_install.sh 同表同序，改一处须同步其余）：9 个代理前缀 +
# 直连（""）。路线选择口径（与面板侧一致）：
#   0) 面板源分发：先单探由 -a 面板地址派生的面板线路（见
#      derive_panel_prefix），通 → 独占使用，不测其余候选；下载
#      全败时经 extend_download_routes 补测代理排名 + 直连兜底；
#   1) 先单独探测直连（真实目标、~3-5s）：通且延迟 ≤800ms → 只用
#      直连，不测代理（与 Go 侧 routes.go 的短路门同口径）；通但
#      慢 → 按实测延迟并入第 2) 步的排名，不丢弃也不短路独占；
#   2) 直连不通/过慢 → 并行探测 9 个代理按延迟排名，未判活的按清
#      单原序殿后，直连兜底；已选路线下载失败时逐个续切；
#   3) 仅直连已定（阶段 1）而下载全败时，补测代理排名续切
#      （extend_download_routes）。
# 判活只认真实目标可解析：探测 releases/latest 的最终重定向末段
# 必须长得像版本号，任意 HTTP 200 落地页不算活。
PROXY_CANDIDATES=(
  "https://ghfast.top"
  "https://gcode.hostcentral.cc"
  "https://gh-proxy.com"
  "https://ghproxy.net"
  "https://hub.gitmirror.com"
  "https://ghps.cc"
  "https://gh.llkk.cc"
  "https://ghp.ci"
  "https://ghproxy.cc"
)

route_mode() {
  # L2-33（R5-FD）：PROXY_ENABLED 改大小写不敏感——False/FALSE/0 与
  # false 同义。旧实现仅精确匹配小写 "false"，运维逃生口误写大小写
  # 时静默落入内置选路且无任何告警；无法识别的非空值在 stderr 点名
  # 后按启用处理，不静默吞掉。
  local proxy_enabled_lc
  proxy_enabled_lc=$(printf '%s' "${PROXY_ENABLED:-}" | tr 'A-Z' 'a-z')
  if [[ "$proxy_enabled_lc" == "false" || "$proxy_enabled_lc" == "0" ]]; then
    echo "direct"
  elif [[ -n "$PROXY_URL" ]]; then
    echo "override"
  else
    if [[ -n "$proxy_enabled_lc" && "$proxy_enabled_lc" != "true" && "$proxy_enabled_lc" != "1" ]]; then
      echo "⚠️ PROXY_ENABLED 取值无法识别（\"${PROXY_ENABLED:-}\"），按启用内置选路处理（仅 false/0 可关闭）" >&2
    fi
    echo "builtin"
  fi
}

normalize_proxy_prefix() {
  local proxy="$1"
  if [[ "$proxy" == https://* || "$proxy" == http://* ]]; then
    proxy="${proxy%/}"
  else
    proxy="https://${proxy%/}"
  fi
  echo "$proxy"
}

# derive_panel_prefix：从 $SERVER_ADDR 派生面板源前缀（面板源分发
# 一期②）。映射与 Go 侧两处实现互为对表，改一处须同步其余：
# agent 的 derivePanelOrigin（go-gost/x/socket）与面板后端的
# panelMirrorPrefixFromAgentAddr（go-backend mutations.go）：
#   wss:// → https://、ws:// → http://、https:// → https://、
#   http:// → http://、裸地址 → https://（默认加密，不静默降级）；
# 只取 origin（scheme://host[:port]），addr 中的路径/query/片段
# 丢弃，非标准端口保留。派生不出（SERVER_ADDR 为空——手动裸跑
# 脚本无 -a 的情形，或 scheme 不认识）时无输出且返回非 0，调用
# 方跳过面板候选、行为与旧版一致。
derive_panel_prefix() {
  local addr="${SERVER_ADDR:-}"
  [[ -n "$addr" ]] || return 1
  local scheme="" rest="$addr"
  case "$addr" in
    *://*)
      scheme="${addr%%://*}"
      rest="${addr#*://}"
      ;;
  esac
  rest="${rest%%/*}"
  rest="${rest%%\?*}"
  rest="${rest%%#*}"
  [[ -n "$rest" ]] || return 1
  case "$scheme" in
    wss|https) scheme="https" ;;
    ws|http) scheme="http" ;;
    "") scheme="https" ;;
    *) return 1 ;;
  esac
  printf '%s://%s/api/v1/open_api/gh' "$scheme" "$rest"
}

route_url() {
  local prefix="$1" url="$2"
  if [[ -z "$prefix" ]]; then
    echo "$url"
  else
    echo "${prefix}/${url}"
  fi
}

# probe_route <前缀>：探测单条路线，活则回显耗时秒数、死则失败。
probe_route() {
  local prefix="$1"
  local out effective tag
  out=$(curl -fsSL -o /dev/null --connect-timeout 3 --max-time 5 -w '%{time_total} %{url_effective}' "$(route_url "$prefix" "https://github.com/${REPO}/releases/latest")" 2>/dev/null) || return 1
  effective="${out#* }"
  tag="${effective##*/}"
  [[ "$tag" =~ ^[0-9][0-9A-Za-z.\-]*$ ]] || return 1
  printf '%s' "${out%% *}"
}

PROXY_RANKED=()
PROXY_RANKED_LATENCY=()

# rank_proxy_routes：并行探测全部代理候选（单候选超时 ~5s、一轮
# 总界有界），活者按延迟升序、未判活者按清单原序殿后，结果写入
# PROXY_RANKED（只含代理，不含直连）；PROXY_RANKED_LATENCY 与之
# 并行记录实测延迟（未判活者为空串），供慢直连按延迟并入排名。
rank_proxy_routes() {
  PROXY_RANKED=()
  PROXY_RANKED_LATENCY=()
  local tmpdir i prefix
  tmpdir=$(mktemp -d) || return 1
  for i in "${!PROXY_CANDIDATES[@]}"; do
    prefix="${PROXY_CANDIDATES[$i]}"
    (
      t=$(probe_route "$prefix") && printf '%s %s\n' "$t" "$i" > "$tmpdir/$i"
    ) &
  done
  wait
  local line idx
  while IFS= read -r line; do
    idx="${line##* }"
    PROXY_RANKED+=("${PROXY_CANDIDATES[$idx]}")
    PROXY_RANKED_LATENCY+=("${line% *}")
  done < <(cat "$tmpdir"/* 2>/dev/null | sort -n)
  rm -rf "$tmpdir"
  local seen=" ${PROXY_RANKED[*]} "
  for prefix in "${PROXY_CANDIDATES[@]}"; do
    case "$seen" in
      *" $prefix "*) ;;
      *) PROXY_RANKED+=("$prefix"); PROXY_RANKED_LATENCY+=("") ;;
    esac
  done
}

# direct_latency_within_gate <秒>：与 Go 侧 routes.go 同口径——直连
# 探测延迟 ≤800ms 才配短路独占；shell 浮点比较走 awk。
direct_latency_within_gate() {
  awk -v t="$1" 'BEGIN { exit !(t + 0 <= 0.8) }'
}

# routes_insert_slow_direct <直连实测延迟秒>：须在 rank_proxy_routes
# 之后调用。把慢直连按实测延迟插入已排名路线表（与 Go 侧口径一
# 致：慢直连不丢弃、按延迟参与排序；未判活代理无延迟值，直连排在
# 已判活段末尾、未判活段之前）。结果写入 DOWNLOAD_ROUTES，
# ROUTES_STAGE 置 2。
routes_insert_slow_direct() {
  local direct_latency="$1"
  DOWNLOAD_ROUTES=()
  local i lat inserted=0
  for i in "${!PROXY_RANKED[@]}"; do
    lat="${PROXY_RANKED_LATENCY[$i]:-}"
    if [[ "$inserted" == "0" ]] && { [[ -z "$lat" ]] || awk -v a="$direct_latency" -v b="$lat" 'BEGIN { exit !(a + 0 <= b + 0) }'; }; then
      DOWNLOAD_ROUTES+=("")
      inserted=1
    fi
    DOWNLOAD_ROUTES+=("${PROXY_RANKED[$i]}")
  done
  if [[ "$inserted" == "0" ]]; then
    DOWNLOAD_ROUTES+=("")
  fi
  ROUTES_STAGE=2
}

DOWNLOAD_ROUTES=()
ROUTES_STAGE=0

# ensure_download_routes：懒初始化路线表（只在真正下载/解析版本时
# 触发，纯本地操作不触网）。状态行走 stderr，避免污染 $(...) 捕获。
ensure_download_routes() {
  if [[ ${#DOWNLOAD_ROUTES[@]} -gt 0 ]]; then
    return 0
  fi
  case "$(route_mode)" in
    direct)
      DOWNLOAD_ROUTES=("")
      ROUTES_STAGE=2
      ;;
    override)
      DOWNLOAD_ROUTES=("$(normalize_proxy_prefix "$PROXY_URL")")
      ROUTES_STAGE=2
      ;;
    *)
      # 面板源分发一期②：先单探面板线路（由 -a 的面板地址派生，
      # 见 derive_panel_prefix）。新节点此刻唯一已证可达的目的地
      # 就是面板本身，通则独占、不再测速其余候选；不通或派生不
      # 出，则原样走下方直连短路 → 代理排名的既有流程。PROXY_URL
      # 显式覆盖与 PROXY_ENABLED=false 在 route_mode 层已先行处
      # 理，不受本步影响（运维逃生口语义不变）。
      local panel_prefix=""
      panel_prefix=$(derive_panel_prefix 2>/dev/null || true)
      local direct_latency=""
      if [[ -n "$panel_prefix" ]] && probe_route "$panel_prefix" >/dev/null; then
        DOWNLOAD_ROUTES=("$panel_prefix")
        ROUTES_STAGE=3
        echo "🌐 GitHub 下载路线: 面板源（经本机面板镜像）" >&2
      elif direct_latency=$(probe_route ""); then
        if direct_latency_within_gate "$direct_latency"; then
          DOWNLOAD_ROUTES=("")
          ROUTES_STAGE=1
          echo "🌐 GitHub 下载路线: 直连" >&2
        else
          # L2-06①（R5-FD）：慢直连门——旧实现 probe_route 回读了
          # time_total 却只用成败，探针耗时近 5s 的直连也短路独占。
          # 与 Go 侧 routes.go 同口径：>800ms 不短路，按实测延迟
          # 并入代理排名（不丢弃）。
          rank_proxy_routes
          routes_insert_slow_direct "$direct_latency"
          echo "🌐 GitHub 直连较慢（${direct_latency}s 超过 0.8s 门限），按测速排名选路（首选: ${DOWNLOAD_ROUTES[0]:-直连}）" >&2
        fi
      else
        rank_proxy_routes
        DOWNLOAD_ROUTES=("${PROXY_RANKED[@]}" "")
        ROUTES_STAGE=2
        echo "🌐 GitHub 直连不通，按测速排名选用代理路线（首选: ${DOWNLOAD_ROUTES[0]}）" >&2
      fi
      ;;
  esac
}

# extend_download_routes：阶段 1（仅直连）路线全败时补测代理排名
# 并追加；阶段 3（面板源独占）全败时同口径补测——代理排名续切、
# 直连殿后兜底（面板线路已在表内，不重复追加）。有新增路线返回
# 0，否则返回 1。
extend_download_routes() {
  if [[ "$(route_mode)" != "builtin" ]]; then
    return 1
  fi
  case "$ROUTES_STAGE" in
    1)
      rank_proxy_routes
      DOWNLOAD_ROUTES+=("${PROXY_RANKED[@]}")
      ROUTES_STAGE=2
      return 0
      ;;
    3)
      rank_proxy_routes
      DOWNLOAD_ROUTES+=("${PROXY_RANKED[@]}" "")
      ROUTES_STAGE=2
      return 0
      ;;
  esac
  return 1
}

resolve_latest_release_tag() {
  local effective_url tag api_tag latest_url api_url tag_pattern

  # J-02: 渠道分流（与 Go 侧 releaseChannelFromTag / stableVersionPattern
  # 同口径）。稳定渠道只接受纯数字 tag；FLMX_RELEASE_CHANNEL=dev|beta
  # 时接受任意数字开头 tag（beta 渠道）。默认稳定渠道，永不解析出 beta；
  # beta 须显式 VERSION 指定或切 dev/beta 渠道安装。
  case "${FLMX_RELEASE_CHANNEL:-stable}" in
    dev|beta) tag_pattern='^[0-9][0-9A-Za-z.\-]*$' ;;
    *) tag_pattern='^[0-9]+(\.[0-9]+)+$' ;;
  esac

  latest_url="https://github.com/${REPO}/releases/latest"
  api_url="https://api.github.com/repos/${REPO}/releases/latest"

  # 1.7.7：逐路线尝试（每条路线内先重定向法、后 API 法），首个解析
  # 出合格 tag 的路线即用；路线全败时按直选口径补测/续切一次。
  # L2-06③（R5-FD）：整轮总量上限（默认 120s，FLMX_RESOLVE_BUDGET_SECONDS
  # 可调）——旧实现每路线重定向法 30s＋API 法 30s 串行，11 线路＋
  # extend 一轮可达 11 分钟以上且无上限。逐次尝试按剩余预算收紧
  # --max-time，耗尽即整轮失败返回。
  local resolve_deadline=$(( $(date +%s) + ${FLMX_RESOLVE_BUDGET_SECONDS:-120} ))
  ensure_download_routes
  local prefix remain max_time
  while :; do
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      remain=$(( resolve_deadline - $(date +%s) ))
      if (( remain <= 0 )); then
        echo "❌ 解析最新版本超时（总预算 ${FLMX_RESOLVE_BUDGET_SECONDS:-120}s 已耗尽）" >&2
        return 1
      fi
      max_time=30
      if (( remain < max_time )); then max_time=$remain; fi
      effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' -L --connect-timeout 5 --max-time "$max_time" "$(route_url "$prefix" "$latest_url")" 2>/dev/null || true)
      tag="${effective_url##*/}"
      # H-24: 无 Release 时 /releases/latest 会 302 到 /releases 索引页，
      # 此时解析出的是垃圾值 "releases" 而非版本号，必须按版本格式校验。
      if [[ "$tag" =~ $tag_pattern ]]; then
        echo "$tag"
        return 0
      fi

      remain=$(( resolve_deadline - $(date +%s) ))
      if (( remain <= 0 )); then
        echo "❌ 解析最新版本超时（总预算 ${FLMX_RESOLVE_BUDGET_SECONDS:-120}s 已耗尽）" >&2
        return 1
      fi
      max_time=30
      if (( remain < max_time )); then max_time=$remain; fi
      api_tag=$(curl -fsSL --connect-timeout 5 --max-time "$max_time" "$(route_url "$prefix" "$api_url")" 2>/dev/null | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)
      if [[ "$api_tag" =~ $tag_pattern ]]; then
        echo "$api_tag"
        return 0
      fi
    done
    extend_download_routes || break
  done

  return 1
}

resolve_version() {
  if [[ -n "${VERSION:-}" ]]; then
    echo "$VERSION"
    return 0
  fi
  if [[ -n "${FLMX_VERSION:-}" ]]; then
    echo "$FLMX_VERSION"
    return 0
  fi
  if [[ -n "${FLUX_VERSION:-}" ]]; then
    echo "$FLUX_VERSION"
    return 0
  fi
  if [[ -n "${PINNED_VERSION:-}" ]]; then
    echo "$PINNED_VERSION"
    return 0
  fi

  if resolve_latest_release_tag; then
    return 0
  fi

  echo "❌ 无法获取最新版本号。你可以手动指定版本，例如：VERSION=<版本号> ./install.sh" >&2
  return 1
}

# 构建下载地址
build_download_url() {
    # AUDIT-R1 (SCR-007): 分开声明与赋值，避免 local 吞掉 get_architecture 的
    # 非零退出码（SC2155）；未知架构直接向上传播失败。
    local ARCH
    ARCH=$(get_architecture) || return 1
    echo "https://github.com/${REPO}/releases/download/${RESOLVED_VERSION}/gost-${ARCH}"
}

# corroborate_binary_anchor <获胜前缀> <第一锚期望哈希>：R7-002 第二锚
# （与 panel_install.sh 的 corroborate_download_anchor、面板自升级的
# corroborateReleaseAssetChecksum、agent 侧 corroborateUpgradeChecksum
# 同口径）：verify_downloaded_binary 的校验值由实际下载地址派生、与
# 本体恒同路线同源，单个作恶线路即可给出自洽的（假二进制，假
# .sha256）对。这里在定版后从路线表内与获胜线路不同的一条线路重取
# 同一资产的 .sha256，与第一锚已采信的期望哈希比对：
#   - 异路给出不同的合法哈希 → 硬失败（返回 1），调用方丢弃 .part
#     整轮切线/失败，不拿该二进制落地；
#   - 异路内容不是合法 64 位 SHA256 → 只废该路，继续问下一路；
#   - 全部异路都给不出合法校验值 → 降级单锚继续（返回 0），并在
#     stderr 点名「跨线路锚复核已降级」。
# 边界（如实写明）：① 路线表只有一条时先按下载主流程同款机制
# extend 补测一次（面板源独占/直连短路形态扩得动；PROXY_URL 覆盖与
# PROXY_ENABLED=false 是运维显式单路，extend 不动、无候选可扩，
# 直接降级点名）；② 整轮复核共用 20s 总界（与 Go 两侧及
# panel_install.sh 同值），逐路 --max-time 取剩余量与 10s 的较小
# 者；③ 本函数只依赖脚本内置路线表（DOWNLOAD_ROUTES /
# DOWNLOAD_URL），手动裸跑 install.sh 同样生效，不依赖面板注入。
corroborate_binary_anchor() {
  local winner="$1" expected="$2"
  local -a candidates=()
  local prefix
  for prefix in "${DOWNLOAD_ROUTES[@]}"; do
    [[ "$prefix" == "$winner" ]] && continue
    candidates+=("$prefix")
  done
  if [[ ${#candidates[@]} -eq 0 ]] && extend_download_routes; then
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      [[ "$prefix" == "$winner" ]] && continue
      candidates+=("$prefix")
    done
  fi
  local deadline=$(( $(date +%s) + 20 ))
  local got tmp remain max_time
  for prefix in "${candidates[@]}"; do
    remain=$(( deadline - $(date +%s) ))
    if (( remain <= 0 )); then
      break
    fi
    max_time=10
    if (( remain < max_time )); then max_time=$remain; fi
    tmp=$(mktemp) || continue
    if curl -fsSL --connect-timeout 5 --max-time "$max_time" -o "$tmp" "$(route_url "$prefix" "$DOWNLOAD_URL").sha256" 2>/dev/null; then
      got=$(awk '{print $1}' "$tmp" | tr -d ' \n' | tr 'A-F' 'a-f')
      rm -f "$tmp"
      if [[ "$got" =~ ^[0-9a-f]{64}$ ]]; then
        if [[ "$got" != "$expected" ]]; then
          echo "❌ 跨线路锚复核失败：二进制校验值经两条不同线路获取的结果不一致，拒绝安装（R7-002 第二锚）" >&2
          echo "   第一锚校验值（已验）: $expected" >&2
          echo "   异路（${prefix:-直连}）校验值: $got" >&2
          return 1
        fi
        echo "✅ 跨线路锚复核通过（第二线路: ${prefix:-直连}）"
        return 0
      fi
    else
      rm -f "$tmp"
    fi
  done
  echo "⚠️ 跨线路锚复核已降级：其他线路均未给出校验值，按单锚继续（R7-002 第二锚）" >&2
  return 0
}

# H-01: Release 与二进制同目录发布 .sha256，下载后必须强校验。
# 校验 URL 从实际下载地址派生（追加 .sha256），代理/直连/file 均适用。
# 校验文件缺失时直接拒绝安装（fail-closed），不允许未校验的 root 二进制落地。
# R7-002: 同源比对只是第一锚——通过后还须经 corroborate_binary_anchor
# 异路复核第二锚（见上），异值拒绝、降级点名。
verify_downloaded_binary() {
    local binary_file="$1"
    local download_url="$2"
    local winner_prefix="${3:-}"
    if ! command -v sha256sum >/dev/null 2>&1; then
        echo "❌ 系统缺少 sha256sum，无法校验二进制完整性，拒绝继续。"
        return 1
    fi
    if [[ -z "$download_url" ]]; then
        echo "❌ 下载地址为空，无法校验，拒绝继续。"
        return 1
    fi
    echo "🔐 下载 SHA256 校验文件..."
    local checksum_file checksum_log
    checksum_file=$(mktemp)
    checksum_log=$(mktemp)
    # R8-J-A: .sha256 是全流程最小的文件，旧实现单次尝试且以
    # 2>/dev/null 吞掉 curl 错误——镜像瞬时故障即整次安装/更新
    # fail-closed 终止且零诊断（2026-10-29 节点 C202610292237590
    # 实例：资产事后验证 5/5 可达）。加有界重试；stderr 改落临时
    # 文件，成功即丢弃（保持无噪音），失败时原文露给用户。
    # fail-closed 不变：校验文件拿不到，仍拒绝未校验的二进制。
    if ! curl -fL --max-time 60 --retry 3 --retry-delay 2 -o "$checksum_file" "${download_url}.sha256" 2>"$checksum_log"; then
        rm -f "$checksum_file"
        echo "❌ 无法获取 .sha256 校验文件，拒绝安装未校验的二进制。"
        if [[ -s "$checksum_log" ]]; then
            echo "   curl 错误信息:"
            sed 's/^/   /' "$checksum_log"
        fi
        rm -f "$checksum_log"
        return 1
    fi
    rm -f "$checksum_log"
    local expected
    # L2-32（R5-FD）：期望值先小写归一再做格式校验（与 panel_install.sh
    # 的 download_verified 同口径）。旧实现直接以 ^[0-9a-f]{64}$ 校验，
    # 镜像给出大写哈希文件时会把合法资产误判「格式无效」拒绝安装
    # （fail-closed 方向无安全问题，但误伤可用性、口径漂移）。
    expected=$(awk '{print $1}' "$checksum_file" | tr 'A-F' 'a-f' | tr -d '\r')
    rm -f "$checksum_file"
    if [[ -z "$expected" ]]; then
        echo "❌ 校验文件为空，拒绝安装未校验的二进制。"
        return 1
    fi
    # 1.7.7：期望哈希必须是 64 位十六进制（与 panel_install.sh 的
    # download_verified 同口径）——代理落地页/错误页即使被当作校验
    # 文件取回，也不得进入比对环节混淆失败形态。
    if [[ ! "$expected" =~ ^[0-9a-f]{64}$ ]]; then
        echo "❌ 校验文件内容格式无效（非 64 位 SHA256），拒绝安装未校验的二进制。"
        return 1
    fi
    local actual
    actual=$(sha256sum "$binary_file" | awk '{print $1}')
    if [[ "$actual" != "$expected" ]]; then
        echo "❌ SHA256 校验失败，文件可能被篡改或下载损坏，拒绝安装。"
        echo "   期望: $expected"
        echo "   实际: $actual"
        return 1
    fi
    echo "✅ SHA256 校验通过"
    # R7-002 第二锚：第一锚的期望值与本体同路同源，定版后须异路
    # 重取 .sha256 复核同一期望值；异值拒绝（调用方丢弃 .part），
    # 无异路应答降级点名后继续。
    if ! corroborate_binary_anchor "$winner_prefix" "$expected"; then
        return 1
    fi
    return 0
}

# download_agent_binary <目标路径>：按路线表逐个下载 agent 二进制，
# 每条路线内完成下载 + 同源 .sha256 校验（verify_downloaded_binary
# 从实际路线 URL 派生校验地址＝第一锚，再经 corroborate_binary_anchor
# 异路复核＝R7-002 第二锚），首个全过者即用；下载错误/校验
# 失败/无进展（--speed-limit/--speed-time 看门狗）自动切下一名，
# 不需要重跑脚本。DOWNLOAD_URL 恒为无前缀的 canonical 地址，路线
# 前缀只在实际请求时拼（route_url）。
# L2-06②（R5-FD）：与 Go 侧 FileTransfer 口径对齐，补两条——
# ① 整轮总预算（默认 240s，与 Go 侧总预算同值，FLMX_DOWNLOAD_BUDGET_SECONDS
# 可调）：逐路线按剩余时间收紧 --max-time，耗尽即整轮失败；旧实现
# 只有 1KB/s 地板，一条 2KB/s 的线路下 10MB 需约 80 分钟且永不切线。
# ② 跨线路续传：本体先落 <dest>.part，后续路线以 curl -C - 接着已
# 落盘字节继续（Range），不再每条路线 rm 后从零重下；校验通过才
# mv 为正式文件。校验失败说明字节本身不对/不完整，丢弃 .part 防
# 坏前缀污染后续路线；服务端忽略 Range 时 curl 可能报错或拼出坏
# 字节，最终由 sha256 校验兜底丢弃，不会装错文件。已知残口：某路
# 已下满全部字节但 curl 以错误退出时，后续路线会收到 416 而非续
# 传成功，整轮按失败处理、重跑脚本从零开始（fail-closed 方向）。
download_agent_binary() {
  local dest="$1"
  ensure_download_routes
  local part="${dest}.part"
  rm -f "$part"
  local deadline=$(( $(date +%s) + ${FLMX_DOWNLOAD_BUDGET_SECONDS:-240} ))
  local prefix url remain
  while :; do
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      url=$(route_url "$prefix" "$DOWNLOAD_URL")
      remain=$(( deadline - $(date +%s) ))
      if (( remain <= 0 )); then
        echo "❌ 下载总预算（${FLMX_DOWNLOAD_BUDGET_SECONDS:-240}s）已耗尽，整轮下载失败" >&2
        rm -f "$part"
        return 1
      fi
      if curl -fL -C - --connect-timeout 10 --max-time "$remain" --speed-limit 1024 --speed-time 30 "$url" -o "$part" && [[ -s "$part" ]]; then
        if verify_downloaded_binary "$part" "$url" "$prefix"; then
          mv -f "$part" "$dest"
          echo "✅ 下载完成（路线: ${prefix:-直连}）"
          return 0
        fi
        rm -f "$part"
      fi
    done
    extend_download_routes || break
  done
  rm -f "$part"
  return 1
}

ensure_download_url_initialized() {
  ensure_alpine_runtime_dependencies || return 1

  if [[ -n "${DOWNLOAD_URL:-}" ]]; then
    return 0
  fi

  RESOLVED_VERSION=$(resolve_version) || return 1
  # AUDIT-R1 (SCR-007): 未知架构时 build_download_url 失败；内层 $() 失败不会
  # 自动中断外层（无 set -e），必须分步检查，否则会拿空 ARCH 拼出错误 URL。
  local _dl_url
  _dl_url=$(build_download_url) || return 1
  # 1.7.7：DOWNLOAD_URL 只存 canonical 地址，路线前缀在下载时拼。
  DOWNLOAD_URL="$_dl_url"
}



# 显示菜单
show_menu() {
  echo "==============================================="
  echo "              管理脚本"
  echo "==============================================="
  echo "请选择操作："
  echo "1. 安装"
  echo "2. 更新"  
  echo "3. 卸载"
  echo "4. 退出"
  echo "==============================================="
}

# 删除脚本自身
# R4B11-005: 只在成功路径自删且文案与成败一致。旧实现被 main 无条件
# 调用：操作失败后脚本仍被删、且恒印「操作已完成」，用户重试须重新
# 下载脚本，自动化消费者也被文案误导。现按 main 传入的返回码分流：
# 失败时保留脚本并给出失败文案。另补与 panel_install.sh 同口径的
# /dev/fd 守卫：进程替换/管道执行时 $0 解析出的是 /dev/fd/* 而非脚本
# 文件，不得对无关路径执行 rm、也不得照印「已删除」。用户取消单独
# 走 AGENT_OP_CANCELLED 标志（见 uninstall_flmx_agent，R2-F7-01）：
# main 见标志不调用本函数、保留脚本并以 0 退出。
delete_self() {
  local rc="${1:-0}"
  if [[ "$rc" != "0" ]]; then
    echo ""
    echo "⚠️ 操作未成功（退出码 $rc），脚本文件已保留，可直接重新运行重试"
    return 0
  fi
  SCRIPT_PATH="$(readlink -f "$0" 2>/dev/null || realpath "$0" 2>/dev/null || echo "$0")"
  if [[ "$SCRIPT_PATH" == /dev/fd/* ]]; then
    return 0
  fi
  echo ""
  echo "🗑️ 操作已完成，正在清理脚本文件..."
  sleep 1
  rm -f "$SCRIPT_PATH" && echo "✅ 脚本文件已删除" || echo "❌ 删除脚本文件失败"
}

# 尝试安装 tcpkill（各发行版分支；安装命令的输出保持静默，成败由
# check_and_install_tcpkill 在事后统一复检并告警，见 R4B11-007）
attempt_install_tcpkill() {
  # 检查 tcpkill 是否已安装
  if command -v tcpkill &> /dev/null; then
    return 0
  fi
  
  # 检测操作系统类型
  OS_TYPE=$(uname -s)
  
  # 检查是否需要 sudo
  if [[ $EUID -ne 0 ]]; then
    SUDO_CMD="sudo"
  else
    SUDO_CMD=""
  fi
  
  if [[ "$OS_TYPE" == "Darwin" ]]; then
    if command -v brew &> /dev/null; then
      brew install dsniff &> /dev/null
    fi
    return 0
  fi
  
  # 检测 Linux 发行版并安装对应的包
  if [ -f /etc/os-release ]; then
    . /etc/os-release
    DISTRO=$ID
  elif [ -f /etc/redhat-release ]; then
    DISTRO="rhel"
  elif [ -f /etc/debian_version ]; then
    DISTRO="debian"
  else
    return 0
  fi
  
  case $DISTRO in
    ubuntu|debian)
      $SUDO_CMD apt update &> /dev/null
      $SUDO_CMD apt install -y dsniff &> /dev/null
      ;;
    centos|rhel|fedora)
      if command -v dnf &> /dev/null; then
        $SUDO_CMD dnf install -y dsniff &> /dev/null
      elif command -v yum &> /dev/null; then
        $SUDO_CMD yum install -y dsniff &> /dev/null
      fi
      ;;
    alpine)
      $SUDO_CMD apk add --no-cache dsniff &> /dev/null
      ;;
    arch|manjaro)
      $SUDO_CMD pacman -S --noconfirm dsniff &> /dev/null
      ;;
    opensuse*|sles)
      $SUDO_CMD zypper install -y dsniff &> /dev/null
      ;;
    gentoo)
      $SUDO_CMD emerge --ask=n net-analyzer/dsniff &> /dev/null
      ;;
    void)
      $SUDO_CMD xbps-install -Sy dsniff &> /dev/null
      ;;
  esac

  return 0
}

# R4B11-007: tcpkill 缺失复检。tcpkill 供 agent 的
# ForceClosePortConnections 使用（强制断开指定端口上的现有连接）；
# 缺失不阻断安装/更新，但不得静默——点名受影响的能力与补救方式。
verify_tcpkill_available() {
  if command -v tcpkill &> /dev/null; then
    return 0
  fi
  echo "⚠️ tcpkill（dsniff 软件包）未能安装：节点将无法强制断开指定端口上的现有连接（规则/用户变更时的连接清理会缺失），其余功能不受影响；如需该能力，请手动安装 dsniff 后重试。"
  return 1
}

# 检查并安装 tcpkill：安装（install_flmx_agent）与更新
# （update_flmx_agent）两个调用点共用本入口，返回前统一复检存在性，
# 缺失时明示告警（不阻断主流程，恒返回 0）。
check_and_install_tcpkill() {
  if command -v tcpkill &> /dev/null; then
    return 0
  fi
  attempt_install_tcpkill
  verify_tcpkill_available || true
  return 0
}

json_escape() {
  local value="$1"
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '%s' "$value"
}

# json_escape 的逆操作：把 JSON 字符串字面值还原为原始文本。
# 不同 JSON 生成器对同一地址的转义风格不同（例如把斜杠写成转义
# 形式、把冒号写成 unicode 转义），逐字比较前必须先归一化，否则
# 自家 agent 会被误判为他人（AI-10）。单遍左到右解码，解出的
# 字符不再二次展开。
json_unescape() {
  local value="$1" out="" ch next hex
  local i=0
  while (( i < ${#value} )); do
    ch=${value:i:1}
    if [[ "$ch" == '\' ]] && (( i + 1 < ${#value} )); then
      next=${value:i+1:1}
      case "$next" in
        '/') out+='/'; i=$((i + 2)) ;;
        '\') out+='\'; i=$((i + 2)) ;;
        '"') out+='"'; i=$((i + 2)) ;;
        n) out+=$'\n'; i=$((i + 2)) ;;
        r) out+=$'\r'; i=$((i + 2)) ;;
        t) out+=$'\t'; i=$((i + 2)) ;;
        b) out+=$'\b'; i=$((i + 2)) ;;
        f) out+=$'\f'; i=$((i + 2)) ;;
        u)
          hex=${value:i+2:4}
          if [[ "$hex" =~ ^[0-9a-fA-F]{4}$ ]] && (( 16#$hex < 128 )); then
            out+=$(printf "\\$(printf '%03o' "$((16#$hex))")")
            i=$((i + 6))
          else
            out+="$ch"
            i=$((i + 1))
          fi
          ;;
        *) out+="$ch"; i=$((i + 1)) ;;
      esac
    else
      out+="$ch"
      i=$((i + 1))
    fi
  done
  printf '%s' "$out"
}

# 从既有 config.json 里取 addr（无文件/无字段时回空）。返回值已做
# JSON 反转义归一化（json_unescape），与原始地址文本同口径，可直接
# 与 SERVER_ADDR 比对。
agent_config_addr() {
  local config="$1/config.json" addr=""
  [[ -f "$config" ]] || return 0
  addr=$(grep -o '"addr"[[:space:]]*:[[:space:]]*"[^"]*"' "$config" | head -n 1 | sed 's/.*"\([^"]*\)"$/\1/' || true)
  json_unescape "$addr"
}

# 从既有 config.json 里取协议计数字段（http/tls/socks）的数值
# （无文件/无字段/非纯数字时回空）。这三字段由面板经 SetProtocol
# 下发、agent 握手时上报，脚本写面本身没有它们的值——整覆重写
# config.json 时若不回读保全，计数会被静默清零（R-3），与 agent
# 侧 updateLocalConfigJSON 的 map 合并保全口径对齐后，脚本侧同
# 样只认纯数字形态，手工键与异常值不回写。
agent_config_protocol_count() {
  local config="$1" field="$2" value=""
  [[ -f "$config" ]] || return 0
  value=$(grep -o "\"$field\"[[:space:]]*:[[:space:]]*[0-9][0-9]*" "$config" | head -n 1 | grep -o '[0-9][0-9]*$' || true)
  printf '%s' "$value"
}

# 足迹判定：某目录是否住着一个 agent——连接配置或任一世代的二进
# 制（现行 flmx_agent／存量 flux_agent）在位即认。归属守卫、布局
# 迁移与卸载的存量识别共用这一口径，不另造第二套。
agent_dir_has_agent() {
  local dir="$1"
  [[ -e "$dir/config.json" || -e "$dir/flmx_agent" || -e "$dir/flux_agent" ]]
}

# 共存守卫（C2-02）：目标目录已有 agent 时，只有其 config 的
# addr 经 JSON 转义归一化后与本次面板地址逐字相等，才认作本面板
# 的旧 agent、允许就地覆盖
# 安装；不相等（或无法判定）一律视为其他面板的 agent——不覆写其
# config、不删其文件、不停其服务。面板地址变更过的存量节点可在交
# 互终端人工确认接管；非交互执行（面板一键命令 -a/-s）直接终止并
# 说明，绝不静默接管。显式逃生口：FLMX_AGENT_TAKEOVER=1 表示运维
# 已确认归属后自担接管。$1=目标目录（缺省 $INSTALL_DIR；存量布局
# 迁移前对 $LEGACY_INSTALL_DIR 同口径判定）。
guard_agent_dir_ownership() {
  local guard_dir="${1:-$INSTALL_DIR}"
  agent_dir_has_agent "$guard_dir" || return 0
  local old_addr
  old_addr=$(agent_config_addr "$guard_dir")
  if [[ -n "$old_addr" && -n "$SERVER_ADDR" && "$old_addr" == "$SERVER_ADDR" ]]; then
    return 0
  fi
  if [[ "${FLMX_AGENT_TAKEOVER:-}" == "1" ]]; then
    echo "⚠️ FLMX_AGENT_TAKEOVER=1：按显式确认接管 $guard_dir（原连接地址：${old_addr:-未知}）"
    return 0
  fi
  echo "⚠️ $guard_dir 已存在一个 agent，其连接地址为 ${old_addr:-无法读取}，与本次面板地址 ${SERVER_ADDR:-未提供} 不一致。"
  echo "   （地址比对已做 JSON 转义归一化，写法差异不会造成误判。）"
  echo "   它可能属于其他面板；继续安装会覆写它的配置、把它改连到本面板，本脚本不会未经确认这样做。"
  if [[ -t 0 ]]; then
    local confirm
    read -p "确认该 agent 就是本面板的旧 agent（例如面板换过地址）并继续安装吗？(y/N): " confirm
    if [[ "$confirm" == "y" || "$confirm" == "Y" ]]; then
      return 0
    fi
  fi
  echo "❌ 安装终止：未改动 $guard_dir 中的任何文件。"
  echo "   如确认该 agent 属于本面板（例如面板换过地址），请在交互终端重跑并在提示时确认，或设置 FLMX_AGENT_TAKEOVER=1 后重试。"
  return 1
}

# 菜单更新/卸载的归属守卫（AI-06）：菜单上下文没有「本次面板地址」
# 可与既有 agent 逐字比对（更新/卸载不接收 -a/-s 参数），无法直接
# 沿用 install 守卫的相等判据；但判定部件必须同口径复用、不另造
# 第二套：同样以 agent_config_addr 读现存连接地址（含转义归一化）、
# 同样提供 FLMX_AGENT_TAKEOVER=1 逃生口并留痕、同样只接受交互终
# 端的人工确认、非交互一律拒绝（fail-closed）。若调用方恰带有面
# 板地址（如先以 -a 单独传入再进菜单）且与现存 addr 相等，按
# install 守卫同一判据认作本面板旧 agent、静默放行。其余情形一律
# 先明示当前连接地址、再要求显式确认——该 agent 可能属于其他面
# 板，更新会替换其二进制、卸载会将其整体删除，绝不静默执行。
# $1=动作名（更新/卸载），$2=继续执行的后果说明，$3=目标目录
# （缺省 $INSTALL_DIR；存量布局由调用方以 $LEGACY_INSTALL_DIR 传入
# 同口径判定）。
guard_agent_dir_ownership_menu() {
  local action="$1" consequence="$2" guard_dir="${3:-$INSTALL_DIR}"
  agent_dir_has_agent "$guard_dir" || return 0
  local old_addr
  old_addr=$(agent_config_addr "$guard_dir")
  if [[ -n "$old_addr" && -n "${SERVER_ADDR:-}" && "$old_addr" == "$SERVER_ADDR" ]]; then
    return 0
  fi
  if [[ "${FLMX_AGENT_TAKEOVER:-}" == "1" ]]; then
    echo "⚠️ FLMX_AGENT_TAKEOVER=1：按显式确认对 $guard_dir 执行${action}（当前连接地址：${old_addr:-无法读取}）"
    return 0
  fi
  echo "⚠️ $guard_dir 中已有 agent，当前连接地址为 ${old_addr:-无法读取}。"
  echo "   （地址读取已做 JSON 转义归一化，写法差异不会造成误判。）"
  echo "   它可能属于其他面板；${consequence}"
  if [[ -t 0 ]]; then
    local confirm
    read -p "确认该 agent 属于本面板并继续${action}吗？(y/N): " confirm
    if [[ "$confirm" == "y" || "$confirm" == "Y" ]]; then
      return 0
    fi
  fi
  echo "❌ ${action}终止：未改动 $guard_dir 中的任何文件。"
  echo "   如确认该 agent 属于本面板，请在交互终端重跑并在提示时确认，或设置 FLMX_AGENT_TAKEOVER=1 后重试。"
  return 1
}

write_flmx_agent_config() {
  local path="$1"
  # GitHub 代理落盘口径（1.7.7）：agent 的下载路由恒由内置候选测速
  # 直选决定——面板内升级不读落盘 proxy 字段，面板下发的 proxyUrl
  # 亦仅作兼容解析、不参与路由。PROXY_URL 手动覆盖（override 模式）
  # 只决定本脚本当次安装的下载路由；该模式下仍落盘 proxy 字段，但
  # 仅为数据兼容保留，agent 侧不消费。direct 与 builtin 模式不写。
  local proxy_json=""
  if [[ "$(route_mode)" == "override" ]]; then
    local proxy
    proxy=$(normalize_proxy_prefix "$PROXY_URL")
    proxy_json=$(printf ',\n  "proxy": "%s"' "$(json_escape "$proxy")")
  fi
  # R-3：整覆前回读保全存量协议计数（http/tls/socks）——本次写
  # 面无这三字段的值，存量有就逐字（数值形态）带回，存量无则
  # 不发明（首次安装输出与旧实现逐字一致）。身份仍由本次参数
  # 决定，保全的只是与身份无关的计数状态。
  local counts_json="" count_field count_value
  if [[ -f "$path" ]]; then
    for count_field in http tls socks; do
      count_value=$(agent_config_protocol_count "$path" "$count_field")
      if [[ -n "$count_value" ]]; then
        counts_json=$(printf '%s,\n  "%s": %s' "$counts_json" "$count_field" "$count_value")
      fi
    done
  fi
  printf '{\n  "addr": "%s",\n  "secret": "%s"%s%s\n}\n' \
    "$(json_escape "$SERVER_ADDR")" \
    "$(json_escape "$SECRET")" \
    "$proxy_json" \
    "$counts_json" > "$path"
}

ensure_service_manager() {
  if [[ -n "$SERVICE_MANAGER" ]]; then
    case "$SERVICE_MANAGER" in
      systemd|openrc)
        return 0
        ;;
      *)
        echo "❌ 不支持的服务管理器: $SERVICE_MANAGER" >&2
        return 1
        ;;
    esac
  fi

  # Alpine uses OpenRC even if a systemctl compatibility command happens to be installed.
  if [[ -f /etc/alpine-release ]]; then
    if command -v rc-service >/dev/null 2>&1 && command -v rc-update >/dev/null 2>&1; then
      SERVICE_MANAGER="openrc"
      return 0
    fi
  elif command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    SERVICE_MANAGER="systemd"
    return 0
  elif command -v rc-service >/dev/null 2>&1 && command -v rc-update >/dev/null 2>&1; then
    SERVICE_MANAGER="openrc"
    return 0
  fi

  echo "❌ 未检测到受支持的服务管理器（systemd 或 OpenRC）。" >&2
  return 1
}

flmx_agent_service_exists() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      [[ -f "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" ]] || \
        systemctl list-units --full -all 2>/dev/null | grep -Fq "flmx_agent.service"
      ;;
    openrc)
      [[ -f "$FLMX_AGENT_OPENRC_SERVICE_FILE" ]]
      ;;
  esac
}

stop_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl stop flmx_agent 2>/dev/null || true
      ;;
    openrc)
      rc-service flmx_agent stop 2>/dev/null || true
      ;;
  esac
}

disable_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl disable flmx_agent 2>/dev/null || true
      ;;
    openrc)
      rc-update del flmx_agent default 2>/dev/null || true
      ;;
  esac
}

write_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      mkdir -p "$(dirname "$FLMX_AGENT_SYSTEMD_SERVICE_FILE")"
      cat > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" <<EOF
[Unit]
Description=Flmx_agent Proxy Service
After=network.target

[Service]
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/flmx_agent
Environment=GODEBUG=disablethp=1
Restart=on-failure
# Core dumps of the agent or its helpers must never pile up in the
# data directory (the binary also zeroes RLIMIT_CORE itself at
# startup and sweeps stockpiled core files, see go-gost/coredump.go).
LimitCORE=0
# R17-C (R17-X-01): stdout/stderr must be retrievable — StandardOutput=null
# discarded the agent's startup logs, so a poisoned gost.json left no
# diagnosable failure reason. Journal keeps them queryable via
# journalctl -u flmx_agent. The binary additionally appends startup
# failures to agent-startup.log next to agent-upgrade.log (go-gost/main.go).
StandardOutput=journal
StandardError=journal
SyslogIdentifier=flmx_agent

[Install]
WantedBy=multi-user.target
EOF
      ;;
    openrc)
      mkdir -p "$(dirname "$FLMX_AGENT_OPENRC_SERVICE_FILE")"
      cat > "$FLMX_AGENT_OPENRC_SERVICE_FILE" <<EOF
#!/sbin/openrc-run

name="flmx_agent"
description="Flmx_agent Proxy Service"
command="$INSTALL_DIR/flmx_agent"
directory="$INSTALL_DIR"
command_background="yes"
pidfile="/run/\${RC_SVCNAME}.pid"
# R17-C (R17-X-01): same rationale as the systemd unit — /dev/null
# discarded startup logs; keep them in files for post-mortem diagnosis.
output_log="/var/log/flmx_agent.log"
error_log="/var/log/flmx_agent.err.log"

depend() {
  need net
}
EOF
      chmod +x "$FLMX_AGENT_OPENRC_SERVICE_FILE"
      ;;
  esac
}

enable_and_start_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl daemon-reload
      systemctl enable flmx_agent
      # R2X-020 (R2-J-02): 与 agent 侧 scriptStartCommand 同口径——
      # start 前先清 failed 态，否则 flap 烧穿启动限速后 start 会被
      # 「Start request repeated too quickly」拒绝。OpenRC 无此限速。
      systemctl reset-failed flmx_agent 2>/dev/null || true
      systemctl start flmx_agent
      ;;
    openrc)
      rc-update add flmx_agent default
      rc-service flmx_agent start
      ;;
  esac
}

start_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      # R2X-020 (R2-J-02): 同 enable_and_start——start 前清 failed 态
      # （更新/回滚三调用点共用本函数，一处收口全覆盖）。
      systemctl reset-failed flmx_agent 2>/dev/null || true
      systemctl start flmx_agent
      ;;
    openrc)
      rc-service flmx_agent start
      ;;
  esac
}

flmx_agent_service_is_active() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl is-active --quiet flmx_agent
      ;;
    openrc)
      rc-service flmx_agent status >/dev/null 2>&1
      ;;
  esac
}

flmx_agent_service_status() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl is-active flmx_agent 2>/dev/null || true
      ;;
    openrc)
      rc-service flmx_agent status 2>/dev/null || true
      ;;
  esac
}

flmx_agent_main_pid() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl show -p MainPID --value flmx_agent 2>/dev/null
      ;;
    openrc)
      cat "${FLMX_AGENT_OPENRC_PIDFILE:-/run/flmx_agent.pid}" 2>/dev/null
      ;;
  esac
}

# X-P2-11 (J-01): 与 agent 侧 flmx_agent_health_ok
# （go-gost/x/socket/websocket_reporter.go）同规则的双采样判活：
# 间隔 2 秒两次采样，每次都必须 is-active，且主 PID（systemd
# MainPID / OpenRC pidfile PID）非空、非 0、两次一致。单次采样会
# 把存活数秒后倒下的 flap 二进制在存活窗口内误判为活（见 R4-N2）。
flmx_agent_health_ok() {
  ensure_service_manager || return 1

  local p1 p2
  p1=$(flmx_agent_main_pid) || p1=""
  p1=$(printf '%s' "$p1" | tr -d '[:space:]')
  if ! flmx_agent_service_is_active; then
    return 1
  fi
  sleep 2
  p2=$(flmx_agent_main_pid) || p2=""
  p2=$(printf '%s' "$p2" | tr -d '[:space:]')
  if ! flmx_agent_service_is_active; then
    return 1
  fi
  [[ -n "$p1" && "$p1" != "0" && "$p1" == "$p2" ]]
}

remove_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      rm -f "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
      systemctl daemon-reload 2>/dev/null || true
      ;;
    openrc)
      rm -f "$FLMX_AGENT_OPENRC_SERVICE_FILE"
      ;;
  esac
}

flmx_agent_service_status_hint() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      echo "systemctl status flmx_agent --no-pager"
      ;;
    openrc)
      echo "rc-service flmx_agent status"
      ;;
  esac
}

# ---- 存量布局（改名前）服务助手：仅供布局迁移与卸载识别存量 ----
# 与上方现行组同形态、对象是旧服务名 flux_agent。存量节点迁移/
# 卸载完成前旧服务必须能被准确停用与摘除；新装路径绝不调用本组。

legacy_flux_agent_service_exists() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      [[ -f "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" ]] || \
        systemctl list-units --full -all 2>/dev/null | grep -Fq "flux_agent.service"
      ;;
    openrc)
      [[ -f "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE" ]]
      ;;
  esac
}

stop_legacy_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl stop flux_agent 2>/dev/null || true
      ;;
    openrc)
      rc-service flux_agent stop 2>/dev/null || true
      ;;
  esac
}

disable_legacy_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl disable flux_agent 2>/dev/null || true
      ;;
    openrc)
      rc-update del flux_agent default 2>/dev/null || true
      ;;
  esac
}

remove_legacy_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      rm -f "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE"
      systemctl daemon-reload 2>/dev/null || true
      ;;
    openrc)
      rm -f "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE"
      ;;
  esac
}

# ---- 存量服务恢复组（仅供布局迁移失败时回滚旧布局，与上方现行
# 组同形态、对象是旧服务名 flux_agent）----

legacy_flux_agent_service_file_path() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd) printf '%s' "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" ;;
    openrc) printf '%s' "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE" ;;
  esac
}

# 旧 unit 文件的同形态重写：迁移回滚时旧 unit 已被摘除且无快照
# 可还原（旧服务只在服务管理器里在册、无落盘文件）才用——内容
# 与现行 write_flmx_agent_service 同模板、仅名与路径换回旧代，
# 保证回滚拉起的旧服务与迁移前等价运行。
write_legacy_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      mkdir -p "$(dirname "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE")"
      cat > "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" <<EOF
[Unit]
Description=Flux_agent Proxy Service
After=network.target

[Service]
WorkingDirectory=$LEGACY_INSTALL_DIR
ExecStart=$LEGACY_INSTALL_DIR/flux_agent
Environment=GODEBUG=disablethp=1
Restart=on-failure
LimitCORE=0
StandardOutput=journal
StandardError=journal
SyslogIdentifier=flux_agent

[Install]
WantedBy=multi-user.target
EOF
      ;;
    openrc)
      mkdir -p "$(dirname "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE")"
      cat > "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE" <<EOF
#!/sbin/openrc-run

name="flux_agent"
description="Flux_agent Proxy Service"
command="$LEGACY_INSTALL_DIR/flux_agent"
directory="$LEGACY_INSTALL_DIR"
command_background="yes"
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/var/log/flux_agent.log"
error_log="/var/log/flux_agent.err.log"

depend() {
  need net
}
EOF
      chmod +x "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE"
      ;;
  esac
}

enable_and_start_legacy_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl daemon-reload
      systemctl enable flux_agent
      # 与现行组同口径：start 前清 failed 态，防启动限速拒绝。
      systemctl reset-failed flux_agent 2>/dev/null || true
      systemctl start flux_agent
      ;;
    openrc)
      rc-update add flux_agent default
      rc-service flux_agent start
      ;;
  esac
}

legacy_flux_agent_service_is_active() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl is-active --quiet flux_agent
      ;;
    openrc)
      rc-service flux_agent status >/dev/null 2>&1
      ;;
  esac
}

legacy_flux_agent_main_pid() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl show -p MainPID --value flux_agent 2>/dev/null
      ;;
    openrc)
      cat "${LEGACY_FLUX_AGENT_OPENRC_PIDFILE:-/run/flux_agent.pid}" 2>/dev/null
      ;;
  esac
}

# 与现行 flmx_agent_health_ok 同规则的双采样判活（旧服务名面）。
legacy_flux_agent_health_ok() {
  ensure_service_manager || return 1

  local p1 p2
  p1=$(legacy_flux_agent_main_pid) || p1=""
  p1=$(printf '%s' "$p1" | tr -d '[:space:]')
  if ! legacy_flux_agent_service_is_active; then
    return 1
  fi
  sleep 2
  p2=$(legacy_flux_agent_main_pid) || p2=""
  p2=$(printf '%s' "$p2" | tr -d '[:space:]')
  if ! legacy_flux_agent_service_is_active; then
    return 1
  fi
  [[ -n "$p1" && "$p1" != "0" && "$p1" == "$p2" ]]
}

legacy_flux_agent_service_status_hint() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      echo "systemctl status flux_agent --no-pager"
      ;;
    openrc)
      echo "rc-service flux_agent status"
      ;;
  esac
}

# 存量布局迁移（改名）：把改名前的安装（$LEGACY_INSTALL_DIR＋
# flux_agent 服务）整体迁到现行布局（$INSTALL_DIR＋flmx_agent
# 服务）。调用前提：归属守卫已对存量目录通过、新目录尚无 agent
# （调用方以 agent_dir_has_agent 判定）。步骤：停用并摘除旧服务
# → 整目录搬移（config.json、gost.json、调优原值备份、内核目录、
# 序号状态等内容逐字随目录走，零丢失）→ 二进制与回滚工件改新名
# （.old/.old.meta/.bak 是回滚源与配对记录，随之改名保持代际对
# 应；升级暂存半成品是未完成操作的残留，清掉不迁）→ 写新服务、
# 启用并启动 → 与安装/更新同口径双采样判活。
# R-1（事故专项审计路 5）：旧实现中途失败无自动回滚——摘旧
# unit 后 mv 失败、或迁后判活失败，节点都停在「旧服务已摘、新
# 服务未起」的离线态靠人工续。现全程可回滚：动手前先快照旧
# unit 文件内容（回滚时逐字还原；旧服务只在册无落盘文件时按
# 同模板重写；快照本身失败则动手前中止，RA2-S-06），任一步失败即经 rollback_legacy_agent_migration
# 把目录搬回原位、文件名改回旧名、恢复旧 unit 并拉起旧服务，
# 终态点名回滚结果（已自动回滚／回滚亦失败需人工）。
migrate_legacy_agent_install() {
  ensure_service_manager || return 1

  echo "🔀 检测到改名前的存量安装（$LEGACY_INSTALL_DIR），正在迁移到新布局 $INSTALL_DIR ..."

  local legacy_unit_file="" legacy_unit_snapshot="" had_legacy_service=""
  legacy_unit_file=$(legacy_flux_agent_service_file_path || true)
  if legacy_flux_agent_service_exists; then
    had_legacy_service=1
  fi
  # RA2-S-06：unit 快照改 fail-closed——旧实现 mktemp/cp 失败只把
  # 快照置空、迁移照常继续，此后回滚落到 write_legacy_flux_agent_
  # service 的模板重写，人工改过的旧 unit 无法逐字还原且全程无
  # 点名。旧 unit 文件在场（快照本可取）而取不到时，迁移必须在
  # 动手前（停/摘旧服务之前）响亮中止。旧服务只在册、无落盘文
  # 件的形态不在此列：那是同模板重写的既定口径，不属快照失败。
  if [[ -n "$legacy_unit_file" && -f "$legacy_unit_file" ]]; then
    legacy_unit_snapshot=$(mktemp) || legacy_unit_snapshot=""
    if [[ -z "$legacy_unit_snapshot" ]] || ! cp -p "$legacy_unit_file" "$legacy_unit_snapshot"; then
      [[ -n "$legacy_unit_snapshot" ]] && rm -f "$legacy_unit_snapshot"
      echo "❌ 旧服务单元文件快照失败（$legacy_unit_file），迁移已在动手前中止：无逐字快照时迁移一旦失败只能按模板重写旧 unit，人工改过的单元文件将无法还原。请检查临时目录空间与权限后重试"
      return 1
    fi
  fi
  local dir_moved="" created_install_dir=""

  # 回滚体：摘新服务 → 已搬目录改回旧名搬回原位 → 还原旧 unit
  # （快照优先，无快照且旧服务此前在册时同模板重写）→ 启用并
  # 启动旧服务 → 双采样判活。旧服务此前不在册（只有文件、无
  # 服务）时，目录归位即状态还原、无服务可拉，直接判成。
  rollback_legacy_agent_migration() {
    stop_flmx_agent_service || true
    disable_flmx_agent_service || true
    remove_flmx_agent_service || true
    if [[ -n "$dir_moved" ]]; then
      [[ -f "$INSTALL_DIR/flmx_agent" ]] && mv -f "$INSTALL_DIR/flmx_agent" "$INSTALL_DIR/flux_agent"
      [[ -f "$INSTALL_DIR/flmx_agent.old" ]] && mv -f "$INSTALL_DIR/flmx_agent.old" "$INSTALL_DIR/flux_agent.old"
      [[ -f "$INSTALL_DIR/flmx_agent.old.meta" ]] && mv -f "$INSTALL_DIR/flmx_agent.old.meta" "$INSTALL_DIR/flux_agent.old.meta"
      [[ -f "$INSTALL_DIR/flmx_agent.bak" ]] && mv -f "$INSTALL_DIR/flmx_agent.bak" "$INSTALL_DIR/flux_agent.bak"
      mv "$INSTALL_DIR" "$LEGACY_INSTALL_DIR" || return 1
    elif [[ -n "$created_install_dir" ]]; then
      rmdir "$INSTALL_DIR" 2>/dev/null || true
    fi
    if [[ -z "$had_legacy_service" ]]; then
      return 0
    fi
    if [[ -n "$legacy_unit_snapshot" && -f "$legacy_unit_snapshot" ]]; then
      mkdir -p "$(dirname "$legacy_unit_file")"
      cp -p "$legacy_unit_snapshot" "$legacy_unit_file" || return 1
      if [[ "$SERVICE_MANAGER" == "openrc" ]]; then
        chmod +x "$legacy_unit_file" || true
      fi
    elif [[ ! -f "$legacy_unit_file" ]]; then
      write_legacy_flux_agent_service || return 1
    fi
    enable_and_start_legacy_flux_agent_service || return 1
    sleep 3
    legacy_flux_agent_health_ok
  }

  if [[ -n "$had_legacy_service" ]]; then
    stop_legacy_flux_agent_service
    disable_legacy_flux_agent_service
  fi
  remove_legacy_flux_agent_service

  if [[ -d "$LEGACY_INSTALL_DIR" ]]; then
    if ! mv "$LEGACY_INSTALL_DIR" "$INSTALL_DIR"; then
      echo "❌ 迁移安装目录失败：$LEGACY_INSTALL_DIR → $INSTALL_DIR，正在自动回滚（恢复旧服务）..."
      if rollback_legacy_agent_migration; then
        echo "↩️ 已自动回滚：存量目录未迁移，旧服务（flux_agent）已恢复运行"
      else
        echo "❌ 自动回滚亦失败：存量目录仍在 $LEGACY_INSTALL_DIR 未迁移，但旧服务未能恢复，请人工执行 $(legacy_flux_agent_service_status_hint) 排查"
      fi
      [[ -n "$legacy_unit_snapshot" ]] && rm -f "$legacy_unit_snapshot"
      return 1
    fi
    dir_moved=1
  else
    mkdir -p "$INSTALL_DIR" && created_install_dir=1
  fi

  [[ -f "$INSTALL_DIR/flux_agent" ]] && mv -f "$INSTALL_DIR/flux_agent" "$INSTALL_DIR/flmx_agent"
  [[ -f "$INSTALL_DIR/flux_agent.old" ]] && mv -f "$INSTALL_DIR/flux_agent.old" "$INSTALL_DIR/flmx_agent.old"
  [[ -f "$INSTALL_DIR/flux_agent.old.meta" ]] && mv -f "$INSTALL_DIR/flux_agent.old.meta" "$INSTALL_DIR/flmx_agent.old.meta"
  [[ -f "$INSTALL_DIR/flux_agent.bak" ]] && mv -f "$INSTALL_DIR/flux_agent.bak" "$INSTALL_DIR/flmx_agent.bak"
  rm -f "$INSTALL_DIR"/.flux_agent.upgrade-* "$INSTALL_DIR"/.flux_agent.rollback-* "$INSTALL_DIR"/.flux_agent.upgrade-rollback-* "$INSTALL_DIR/flux_agent.new"

  local migrate_failed=""
  write_flmx_agent_service || migrate_failed=1
  if [[ -z "$migrate_failed" ]]; then
    enable_and_start_flmx_agent_service || migrate_failed=1
  fi
  if [[ -z "$migrate_failed" ]]; then
    echo "🔄 检查迁移后服务状态..."
    sleep 3
    if flmx_agent_health_ok; then
      [[ -n "$legacy_unit_snapshot" ]] && rm -f "$legacy_unit_snapshot"
      echo "✅ 存量安装已迁移到新布局：$INSTALL_DIR（服务名 flmx_agent，配置与状态原样保留）"
      return 0
    fi
    migrate_failed=1
  fi

  echo "❌ 迁移未成（新布局服务未能正常运行），正在自动回滚到旧布局..."
  if rollback_legacy_agent_migration; then
    [[ -n "$legacy_unit_snapshot" ]] && rm -f "$legacy_unit_snapshot"
    echo "↩️ 已自动回滚：旧布局与旧服务（flux_agent）已恢复运行，迁移可稍后重试"
  else
    [[ -n "$legacy_unit_snapshot" ]] && rm -f "$legacy_unit_snapshot"
    echo "❌ 自动回滚亦失败：节点当前离线，请人工处理（旧服务状态：$(legacy_flux_agent_service_status_hint)；新服务状态：$(flmx_agent_service_status_hint)）"
  fi
  return 1
}

# 旧式 gost 形态 agent 的服务文件指纹扫描：命中「WorkingDirectory
# 指向旧配置目录，且 ExecStart 指向旧 gost 二进制（或 /usr/local/bin
# 形态下旧配置双文件并存）」的服务文件逐行输出。cleanup_legacy_
# gost_installation 与卸载宿主清理的足迹判定共用同一指纹口径，不
# 另造第二套。
legacy_gost_matched_service_files() {
  local service_file=""
  for service_file in "$LEGACY_GOST_SERVICE_FILE_ETC" "$LEGACY_GOST_SERVICE_FILE_LIB" "$LEGACY_GOST_SERVICE_FILE_USR_LIB"; do
    if [[ ! -f "$service_file" ]]; then
      continue
    fi
    if ! grep -Fq "WorkingDirectory=$LEGACY_GOST_CONFIG_DIR" "$service_file"; then
      continue
    fi
    if grep -Fq "ExecStart=$LEGACY_GOST_CONFIG_DIR/gost" "$service_file" || \
      (grep -Fq "ExecStart=$LEGACY_GOST_BINARY" "$service_file" && [[ -f "$LEGACY_GOST_CONFIG_DIR/config.json" && -f "$LEGACY_GOST_CONFIG_DIR/gost.json" ]]); then
      echo "$service_file"
    fi
  done
}

# 机器上是否存在旧式 gost 形态 agent 的足迹（服务文件指纹命中，或
# 旧配置目录内仍有二进制/连接配置）。卸载宿主清理据此判定 legacy
# 同名共享资源（99-flvx-tuning.conf、nft 表 flvx_kernel）是否可能
# 属于它。
legacy_gost_footprint_present() {
  [[ -n "$(legacy_gost_matched_service_files)" ]] && return 0
  [[ -e "$LEGACY_GOST_CONFIG_DIR/gost" || -e "$LEGACY_GOST_CONFIG_DIR/config.json" ]]
}

# 旧式 gost 清理的归属判定（C4-F01）：指纹命中的旧式落位与旧谱系
# 面板在节点上的 agent 落位完全同形，仅凭指纹无法区分「本面板改
# 名前的旧装」与「他人面板现役的 agent」；而清理会停服删文件，故
# 与 guard_agent_dir_ownership 同口径设归属门：旧配置的连接地址
# （agent_config_addr 读取、已做 JSON 转义归一化）与本次面板地址
# （SERVER_ADDR）或现装 agent 的连接地址任一逐字相等，才认作本
# 面板旧装放行；FLMX_AGENT_TAKEOVER=1 为显式逃生口（打印留痕）；
# 交互终端可人工确认。其余情形一律不放行，由调用方跳过清理并已
# 在此明示——绝不静默摘除他人 agent。
legacy_gost_cleanup_ownership_confirmed() {
  local legacy_addr installed_addr
  legacy_addr=$(agent_config_addr "$LEGACY_GOST_CONFIG_DIR")
  if [[ -n "$legacy_addr" ]]; then
    if [[ -n "${SERVER_ADDR:-}" && "$legacy_addr" == "$SERVER_ADDR" ]]; then
      return 0
    fi
    installed_addr=$(agent_config_addr "$INSTALL_DIR")
    if [[ -n "$installed_addr" && "$legacy_addr" == "$installed_addr" ]]; then
      return 0
    fi
  fi
  if [[ "${FLMX_AGENT_TAKEOVER:-}" == "1" ]]; then
    echo "⚠️ FLMX_AGENT_TAKEOVER=1：按显式确认清理旧式 gost 安装（其连接地址：${legacy_addr:-无法读取}）"
    return 0
  fi
  echo "⚠️ 检测到旧式 gost 形态的 agent（连接地址：${legacy_addr:-无法读取}），无法确认它属于本面板。"
  echo "   它可能属于其他面板；清理会停止它的服务并删除它的程序文件，本脚本不会未经确认这样做。"
  if [[ -t 0 ]]; then
    local confirm
    read -p "确认该旧式 agent 属于本面板（例如本面板改名前的旧装）并继续清理吗？(y/N): " confirm
    if [[ "$confirm" == "y" || "$confirm" == "Y" ]]; then
      return 0
    fi
  fi
  echo "ℹ️ 已跳过旧式 gost 安装的清理：未改动它的任何文件，本次安装/更新不受影响。"
  echo "   如确认它属于本面板，请在交互终端重跑并在提示时确认，或设置 FLMX_AGENT_TAKEOVER=1 后重试；也可按其所属面板的方式人工处理。"
  return 1
}

cleanup_legacy_gost_installation() {
  local matched_service_files=()
  local service_file=""
  local removed_service_file="0"

  while IFS= read -r service_file; do
    if [[ -n "$service_file" ]]; then
      matched_service_files+=("$service_file")
    fi
  done < <(legacy_gost_matched_service_files)

  if [[ ${#matched_service_files[@]} -eq 0 ]]; then
    return 0
  fi

  # C4-F01：指纹命中只证明「旧式形态」、不证明归属。无归属确认时
  # 跳过清理（披露已由判定函数输出），安装/更新照常继续。
  legacy_gost_cleanup_ownership_confirmed || return 0

  if [[ "$SERVICE_MANAGER" == "systemd" ]] && \
    systemctl list-units --full -all 2>/dev/null | grep -Fq "gost.service"; then
    systemctl stop gost 2>/dev/null || true
    systemctl disable gost 2>/dev/null || true
  fi

  for service_file in "${matched_service_files[@]}"; do
    if [[ -f "$service_file" ]]; then
      rm -f "$service_file"
      removed_service_file="1"
    fi
  done

  if [[ -f "$LEGACY_GOST_BINARY" ]]; then
    rm -f "$LEGACY_GOST_BINARY"
  fi
  if [[ -f "$LEGACY_GOST_CONFIG_DIR/gost" ]]; then
    rm -f "$LEGACY_GOST_CONFIG_DIR/gost"
  fi

  if [[ "$removed_service_file" == "1" && "$SERVICE_MANAGER" == "systemd" ]]; then
    systemctl daemon-reload 2>/dev/null || true
  fi
}


# 获取用户输入的配置参数
get_config_params() {
  if [[ -z "$SERVER_ADDR" || -z "$SECRET" ]]; then
    echo "请输入配置参数："
    
    if [[ -z "$SERVER_ADDR" ]]; then
      read -p "服务器地址: " SERVER_ADDR
    fi
    
    if [[ -z "$SECRET" ]]; then
      # R4B11-006: 密钥读取关回显（-s），避免明文留在终端回显/
      # 录屏/滚动回溯中；-s 不回显换行，读完补一个换行保持版面。
      read -s -p "密钥: " SECRET
      echo ""
    fi
    
    if [[ -z "$SERVER_ADDR" || -z "$SECRET" ]]; then
      echo "❌ 参数不完整，操作取消。"
      exit 1
    fi
  fi
}

# 解析命令行参数
while getopts "a:s:" opt; do
  case $opt in
    a) SERVER_ADDR="$OPTARG" ;;
    s) SECRET="$OPTARG" ;;
    *) echo "❌ 无效参数"; exit 1 ;;
  esac
done

# 安装功能
install_flmx_agent() {
  echo "🚀 开始安装 flmx_agent..."

  ensure_download_url_initialized || exit 1

  get_config_params

  # 共存守卫（C2-02）：在下载、停服、写文件之前先判定既有 agent 的
  # 归属；不属于本面板时终止，不留任何改动。
  guard_agent_dir_ownership || exit 1

  # 存量布局迁移：新目录无 agent、存量目录有 → 先对存量目录同口
  # 径过一遍归属守卫，再整体迁到新布局，之后按新装流程继续（配
  # 置以本次 -a/-s 参数重写、二进制换新版）。守卫不过则迁移不发
  # 生、存量原样不动。
  if ! agent_dir_has_agent "$INSTALL_DIR" && agent_dir_has_agent "$LEGACY_INSTALL_DIR"; then
    guard_agent_dir_ownership "$LEGACY_INSTALL_DIR" || exit 1
    migrate_legacy_agent_install || exit 1
  fi

  # 检查并安装 tcpkill
  check_and_install_tcpkill
  ensure_service_manager || exit 1

  mkdir -p "$INSTALL_DIR"

  local tmp_binary="$INSTALL_DIR/flmx_agent.new"

  # 下载 flmx_agent（内置路线逐个尝试、首成即用；同源校验在
  # download_agent_binary 内完成，失败自动切下一条路线）
  echo "⬇️ 下载 flmx_agent 中..."
  download_agent_binary "$tmp_binary" || { rm -f "$tmp_binary"; echo "❌ 下载失败，请检查网络或下载链接。"; exit 1; }
  cleanup_legacy_gost_installation

  # F-048: 停止并禁用已有服务移到下载+校验成功之后（与 update_flmx_agent
  # 同序）。旧顺序先 stop+disable 再下载，下载/校验失败即 exit 1，节点
  # agent 停摆且开机不自启、必须人工干预；现在失败时旧服务保持运行、
  # 自启状态不变，失败只是「没装上新版」。
  if flmx_agent_service_exists; then
    echo "🔍 检测到已存在的flmx_agent服务"
    stop_flmx_agent_service
    echo "🛑 停止服务"
    disable_flmx_agent_service
    echo "🚫 禁用自启"
  fi
  mv "$tmp_binary" "$INSTALL_DIR/flmx_agent"
  chmod +x "$INSTALL_DIR/flmx_agent"
  echo "✅ 下载完成"

  # 打印版本
  echo "🔎 flmx_agent 版本：$($INSTALL_DIR/flmx_agent -V)"

  # 写入 config.json (安装时总是创建新的)
  CONFIG_FILE="$INSTALL_DIR/config.json"
  echo "📄 创建新配置: config.json"
  write_flmx_agent_config "$CONFIG_FILE"

  # 写入 gost.json
  GOST_CONFIG="$INSTALL_DIR/gost.json"
  if [[ -f "$GOST_CONFIG" ]]; then
    echo "⏭️ 跳过配置文件: gost.json (已存在)"
  else
    echo "📄 创建新配置: gost.json"
    cat > "$GOST_CONFIG" <<EOF
{}
EOF
  fi

  # 加强权限
  chmod 600 "$INSTALL_DIR"/*.json

  # 创建 systemd 或 OpenRC 服务
  write_flmx_agent_service

  # 启动服务
  enable_and_start_flmx_agent_service

  # 检查状态
  # R2X-019 (R2-J-01): 判活结果必须进返回码——main（X-P3-6）只传播
  # 本函数给出的信号；旧实现失败分支末条是 echo 提示、恒返回 0，
  # 以退出码判断成败的自动化部署（-a/-s）会误判安装成功。
  # R4B11-003: 判活口径与更新/回滚路径统一（X-P2-11）：先沉降 3 秒
  # 让 flap 二进制越过最初的存活窗口，再走 flmx_agent_health_ok 双
  # 采样（间隔 2 秒、主 PID 稳定）。旧实现启动后立即单次 is-active，
  # 同样的 flap 形态在全新安装时会被误报「安装完成」。
  echo "🔄 检查服务状态..."
  sleep 3
  if flmx_agent_health_ok; then
    echo "✅ 安装完成，flmx_agent服务已启动并设置为开机启动。"
    echo "📁 配置目录: $INSTALL_DIR"
    echo "🔧 服务状态: $(flmx_agent_service_status)"
    # OBS-R7-J-01 (folded into R7-J-01): "service active" only proves
    # the process is up, not that the node connected to the panel.
    # Tell the operator the actual online check and where the
    # WebSocket connection log lives when it does not come online.
    echo "📍 节点连接地址: $SERVER_ADDR"
    echo "✔️ 请回面板「节点管理」页确认该节点显示为在线——安装完成只表示服务已启动，不代表已连上面板。"
    echo "   若面板未显示在线，请在节点机执行 journalctl -u flmx_agent -n 100 查看 WebSocket 连接日志（OpenRC 请看 rc-service flmx_agent status 与服务日志），并核对连接地址的 scheme 与面板对外方式是否一致（HTTPS 面板须为 wss/https）。"
    return 0
  else
    echo "❌ flmx_agent服务启动失败，请执行以下命令查看状态："
    flmx_agent_service_status_hint
    return 1
  fi
}

# X-P2-12 (J-02): 与 panel_install.sh 同实现的点分数字版本比较。
# 回显 -1/0/1 表示 a 小于/等于/大于 b；任一侧为空或含非数字段时
# 不可比，回显空并返回 1——调用方不得据此拦截（V-19 既有口径：
# 不可比放行，开发/测试标签等场景保持可用）。
panel_version_cmp() {
  local a="${1#v}" b="${2#v}"
  local -a pa pb
  local i n x y
  if [[ -z "$a" || -z "$b" ]]; then
    return 1
  fi
  IFS='.' read -ra pa <<< "$a"
  IFS='.' read -ra pb <<< "$b"
  n=${#pa[@]}
  if (( ${#pb[@]} > n )); then
    n=${#pb[@]}
  fi
  for (( i = 0; i < n; i++ )); do
    x="${pa[i]:-0}"
    y="${pb[i]:-0}"
    if ! [[ "$x" =~ ^[0-9]+$ && "$y" =~ ^[0-9]+$ ]]; then
      return 1
    fi
    if (( 10#$x < 10#$y )); then
      echo -1
      return 0
    fi
    if (( 10#$x > 10#$y )); then
      echo 1
      return 0
    fi
  done
  echo 0
}

# X-P2-12 (J-02): 按 extractLeadingSemver（system_upgrade.go）同规则
# 从版本串中取语义版本：逐个空白分隔段做严格点分数字校验，首个
# 合格段即为版本。真实 -V 输出形如 "gost 1.3.4 (go1.25
# linux/amd64)"（首段是程序名 gost），握手上报的 fullVersion 形如
# "1.3.4 (debian/amd64)"，两种形态都由此归一；粘连后缀
# （如 1.3.2-beta1）、无数字段、空串一律提取失败，调用方按
# 不可比放行处理。"dev" 原样透传（panel_version_cmp 对其不可比）。
extract_leading_semver() {
  local raw="$1" token candidate
  # shellcheck disable=SC2086 # 此处需要按空白分词逐段校验
  for token in $raw; do
    if [[ "$token" == "dev" ]]; then
      printf '%s' "dev"
      return 0
    fi
    candidate="${token#v}"
    if [[ "$candidate" =~ ^[0-9]+(\.[0-9]+)*$ ]]; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}

# 现装 agent 的语义版本（取自二进制 -V）；取不到/不可解析时失败，
# 调用方按不可比处理。
flmx_agent_installed_version() {
  local raw
  [[ -x "$INSTALL_DIR/flmx_agent" ]] || return 1
  raw=$("$INSTALL_DIR/flmx_agent" -V 2>/dev/null) || return 1
  extract_leading_semver "$raw"
}

# 更新功能
update_flmx_agent() {
  echo "🔄 开始更新 flmx_agent..."

  # 布局解析（改名迁移）：新目录无 agent、存量目录有 → 该节点尚
  # 是改名前的存量布局。先对存量目录过归属守卫（AI-06 同口径），
  # 再整体迁到新布局，然后按现行布局走完本次更新（二进制换新版
  # 本、配置不动）。迁移只换目录与服务名、不换二进制版本，下方
  # 防降级判定仍以迁移后的现装版本为准。
  if ! agent_dir_has_agent "$INSTALL_DIR" && agent_dir_has_agent "$LEGACY_INSTALL_DIR"; then
    guard_agent_dir_ownership_menu "更新" "继续更新会先把它的安装目录与服务名迁移为新名（flmx_agent），再把二进制替换为本面板的构建版本（连接地址不变、仍连其所属面板）。" "$LEGACY_INSTALL_DIR" || return 1
    migrate_legacy_agent_install || return 1
  else
    # 归属守卫（AI-06）：在下载、停服、替换二进制之前先判定既有
    # agent 的归属；无法确认属于本面板时终止，不留任何改动。
    guard_agent_dir_ownership_menu "更新" "继续更新会把它的二进制替换为本面板的构建版本（连接地址不变、仍连其所属面板）。" || return 1
  fi

  if [[ ! -d "$INSTALL_DIR" ]]; then
    echo "❌ flmx_agent 未安装，请先选择安装。"
    return 1
  fi

  ensure_download_url_initialized || return 1

  # X-P2-12 (J-02, BC-09): 下载前防降级。读现装版本（-V 经
  # extract_leading_semver 归一）与目标版本比较，目标更低时拒绝
  # 并说明——其余三条更新路径（面板自升级、节点面板升级单/批量、
  # panel_install 脚本更新）均已有此门，脚本路径此前独缺。任一侧
  # 不可比时沿用 panel_version_cmp 既有放行口径（V-19），不拦截。
  local current_version cmp_result
  current_version=$(flmx_agent_installed_version || true)
  cmp_result=$(panel_version_cmp "${RESOLVED_VERSION:-}" "$current_version" || true)
  if [[ "$cmp_result" == "-1" ]]; then
    echo "❌ 目标版本 ${RESOLVED_VERSION:-} 低于当前版本 $current_version，拒绝降级更新。如确需降级请手动处理。"
    return 1
  fi
  if [[ "$cmp_result" == "0" ]]; then
    echo "ℹ️ 目标版本与当前版本相同（$current_version），将按同版本重装继续"
  fi

  echo "📥 使用下载地址: $DOWNLOAD_URL"

  # 检查并安装 tcpkill
  check_and_install_tcpkill
  ensure_service_manager || return 1

  # 先下载新版本（内置路线逐个尝试、首成即用；同源校验在
  # download_agent_binary 内完成，失败自动切下一条路线）
  echo "⬇️ 下载最新版本..."
  download_agent_binary "$INSTALL_DIR/flmx_agent.new" || { rm -f "$INSTALL_DIR/flmx_agent.new"; echo "❌ 下载失败。"; return 1; }
  cleanup_legacy_gost_installation

  # 停止服务
  if flmx_agent_service_exists; then
    echo "🛑 停止 flmx_agent 服务..."
    stop_flmx_agent_service
  fi

  # A9: 备份旧版本，失败可回滚
  # R9-02: 备份步是回滚源的唯一生成点，此前是全链唯一未守卫步
  # （本脚本无 set -e、cp 无返回码检查）：备份拷贝失败（如磁盘
  # 写满）被静默吞掉、替换照常进行——无陈旧 .bak 时回滚无源、
  # 坏二进制留活且旧版字节永失；有陈旧 .bak 时回滚静默跨代、
  # 回执却报「已回滚成功」。现备份失败即在替换前中止：当前二进
  # 制逐字未动，重启方才停掉的服务恢复更新前状态，并清掉本次
  # 下载的 .new 与可能写了一半的 .bak（半成品备份不得被后续
  # 回滚误认为可用的回滚源）。
  if [[ -f "$INSTALL_DIR/flmx_agent" ]]; then
    echo "💾 备份当前版本..."
    if ! cp -f "$INSTALL_DIR/flmx_agent" "$INSTALL_DIR/flmx_agent.bak"; then
      echo "❌ 备份当前版本失败（磁盘空间不足或拷贝出错），已中止更新：当前版本未被替换、仍是更新前的版本。"
      rm -f "$INSTALL_DIR/flmx_agent.bak" "$INSTALL_DIR/flmx_agent.new"
      if flmx_agent_service_exists; then
        echo "🔄 正在重新启动更新前已停止的服务..."
        start_flmx_agent_service
      fi
      return 1
    fi
  fi

  # 替换文件
  mv "$INSTALL_DIR/flmx_agent.new" "$INSTALL_DIR/flmx_agent"
  chmod +x "$INSTALL_DIR/flmx_agent"

  # 烟雾测试：新二进制必须可执行
  if ! "$INSTALL_DIR/flmx_agent" -V >/dev/null 2>&1; then
    echo "❌ 新版本二进制无法执行，正在回滚..."
    rollback_flmx_agent
    return 1
  fi

  # 打印版本
  echo "🔎 新版本：$($INSTALL_DIR/flmx_agent -V)"

  # 重启服务
  echo "🔄 重启服务..."
  start_flmx_agent_service

  # 回滚检查：新版本服务起不来则恢复备份。
  # X-P2-11 (J-01): 与 agent 侧升级脚本同口径——先等待 3 秒让
  # flap 形态（二进制启动后数秒才倒下）越过最初的存活窗口，再走
  # flmx_agent_health_ok 双采样（间隔 2 秒、PID 稳定，采样点约在
  # 启动后 3 秒与 5 秒）。旧实现 sleep 2 后单次 is-active 恰会命中
  # flap 窗口、误判成功并删掉 .bak 回滚源。
  sleep 3
  if ! flmx_agent_health_ok; then
    echo "❌ 新版本服务启动失败，正在回滚到备份版本..."
    rollback_flmx_agent
    return 1
  fi
  rm -f "$INSTALL_DIR/flmx_agent.bak"

  echo "✅ 更新完成，服务已重新启动。"
  # R8-J-B: 与安装路径完成块同口径（OBS-R7-J-01 的上线确认指引）——
  # 服务重启只证明进程已启动，不代表节点已连上面板；更新路径此前
  # 只有一行成功断言、无确认指引、无日志指针。
  echo "✔️ 请回面板「节点管理」页确认该节点显示为在线——更新完成只表示服务已重新启动，不代表已连上面板。"
  echo "   若面板未显示在线，请在节点机执行 journalctl -u flmx_agent -n 100 查看 WebSocket 连接日志（OpenRC 请看 rc-service flmx_agent status 与服务日志），并核对连接地址的 scheme 与面板对外方式是否一致（HTTPS 面板须为 wss/https）。"
}

# A9: 更新失败时恢复备份的旧版本并重启
rollback_flmx_agent() {
  if [[ -f "$INSTALL_DIR/flmx_agent.bak" ]]; then
    mv -f "$INSTALL_DIR/flmx_agent.bak" "$INSTALL_DIR/flmx_agent"
    chmod +x "$INSTALL_DIR/flmx_agent"
    start_flmx_agent_service
    # 观察 O-1（随 R2X-020 顺手对齐，不计数）：回滚后判活改与更新
    # 路径/agent 侧同口径的双采样（flmx_agent_health_ok），单次采样
    # 会把回滚二进制的短暂存活窗口误报为成功。
    if flmx_agent_health_ok; then
      echo "✅ 已回滚到旧版本并重启成功。"
    else
      echo "❌ 回滚后服务仍未启动，请手动检查：$INSTALL_DIR"
    fi
  else
    echo "⚠️ 无备份可回滚，请手动检查：$INSTALL_DIR"
  fi
}

# R6-02：脚本卸载与面板自卸载路径（go-gost/x/socket/uninstall.go
# 的 runAgentUninstall）对齐的宿主清理。面板路径在删安装目录之前
# 依次：恢复调优（回放整形前队列 original-qdisc.sh、按
# tuning-backup.json 逐键写回 sysctl 原值、删宿主文件）→ 停内核
# 并删 agent 自建 nft 三表 → 才停服务删目录。旧脚本卸载只有停服
# 删目录，残留后果：调优 sysctl conf 与 qdisc unit 开机自动重放、
# nft 白名单 DROP 规则比所有者长寿（R2-001 危害形态）、
# tuning-backup.json 随目录删除后原值永久不可恢复、同机重装还会
# 把已调优值当作原值再备份。本函数在服务已停、安装目录未删时执
# 行同一清理全集。口径与面板路径一致：逐项尽力而为，单项失败只
# 告警并逐项列明、不中断后续项；全部以「存在才处理」为前提，重
# 复执行只剩跳过、无副作用（幂等）。
# 归属圈定：只处理带 flmx 标识的自有产物，不触碰同机其他程序的
# 文件。其中两件 legacy 同名资源（99-flvx-tuning.conf、nft 表
# flvx_kernel）与旧谱系面板的 agent 同名共用——它们既是本产品改
# 名前的自有旧名（仅存量未升级的旧构建 agent 仍以 flvx_kernel
# 计数，现行构建计数只认 flmx_kernel），也可能
# 由同机另一套旧谱系 agent 持有；机器上仍有旧式 gost 形态 agent
# 足迹时无从区分归属，一律跳过并披露（C4-F02），无足迹时才按本
# 产品旧装遗留删除。flvx_kernel 一格与面板路径并不逐字一致：面
# 板驱动的 agent 自卸载对 nft 三表无条件删除（无足迹守卫），本
# 脚本则在有足迹时跳过该表并点名——两路删除集仅在无足迹时相同
# （99-flvx-tuning.conf 两路同有足迹守卫，不在此列）。
uninstall_flmx_agent_host_cleanup() {
  local target_dir="${1:-$INSTALL_DIR}"
  local failures=0
  local engine_dir="$target_dir/tcpfit"
  local backup_file="$target_dir/tuning-backup.json"

  # 1) 队列整形恢复：有整形前队列备份（original-qdisc.sh）则回
  # 放，与面板路径 restoreTuneQueues 执行的是同一份脚本。只有
  # 整形脚本、没有原队列备份时队列无法恢复（面板路径在此情形
  # 拒绝恢复以保配置；脚本卸载的目录即将删除、保配置无从谈起，
  # 故明确告警并继续其余清理——运行时整形在重启后随 tc 状态消
  # 散，不留开机重放面）。
  if [[ -f "$engine_dir/original-qdisc.sh" ]]; then
    if bash "$engine_dir/original-qdisc.sh" >/dev/null 2>&1; then
      echo "🧹 已恢复整形前的出口队列"
    else
      failures=$((failures + 1))
      echo "⚠️ 整形前队列恢复脚本执行失败，请人工检查 tc qdisc 状态"
    fi
  elif [[ -f "$engine_dir/shape.sh" ]]; then
    failures=$((failures + 1))
    echo "⚠️ 找不到整形前的队列备份，当前队列整形未能恢复（重启后随 tc 状态消散）"
  fi

  # 2) sysctl 原值恢复：tuning-backup.json 是 {键: 原值} 的 JSON
  # 对象（agent 的 applyTuning 首次调优时备份、恢复时逐键写回
  # /proc/sys 后删除备份）。必须赶在删安装目录之前读它，否则原
  # 值随备份一并丢失。键名只接受字母/数字/点/下划线（sysctl 键
  # 的字符集），且只写 /proc/sys 下已存在的叶子文件——备份内容
  # 永远拼不出任意路径。备份存在却解析不出任何键值对时按损坏
  # 告警（与 agent 侧「备份损坏即停」的 fail-closed 口径同向，
  # 只是卸载不因此中断）。
  if [[ -f "$backup_file" ]]; then
    local pairs=0 restored=0 unsupported=0
    local key="" value="" target=""
    while IFS=$'\t' read -r key value; do
      [[ -n "$key" ]] || continue
      pairs=$((pairs + 1))
      target="$FLMX_PROC_SYS_DIR/${key//.//}"
      if [[ ! -f "$target" ]]; then
        unsupported=$((unsupported + 1))
        continue
      fi
      if { printf '%s\n' "$value" > "$target"; } 2>/dev/null; then
        restored=$((restored + 1))
      else
        failures=$((failures + 1))
        echo "⚠️ sysctl 参数恢复失败: $key"
      fi
    done < <(grep -oE '"[A-Za-z0-9_.]+": *"[^"]*"' "$backup_file" 2>/dev/null | sed -e 's/^"//' -e 's/"$//' -e 's/": *"/\t/')
    if [[ "$pairs" -eq 0 ]]; then
      failures=$((failures + 1))
      echo "⚠️ 调优原值备份无法解析，调优参数未能恢复: $backup_file"
    elif [[ "$restored" -gt 0 || "$unsupported" -gt 0 ]]; then
      echo "🧹 已按调优备份恢复 sysctl 原值（${restored} 项生效，${unsupported} 项本机内核不支持已跳过）"
    fi
  fi

  # 3) 宿主调优文件：sysctl conf（现行名）、BBR 开机加载 conf。删
  # 掉后调优值不再开机重放；运行时值已在第 2 步恢复（无备份时运
  # 行时值保留至重启，与面板路径无备份时同口径）。
  local host_file=""
  for host_file in "$FLMX_TUNING_SYSCTL_CONF" "$FLMX_TUNING_BBR_MODULE_FILE"; do
    if [[ -e "$host_file" ]]; then
      if rm -f "$host_file"; then
        echo "🧹 删除调优宿主文件: $host_file"
      else
        failures=$((failures + 1))
        echo "⚠️ 删除调优宿主文件失败: $host_file"
      fi
    fi
  done

  # 3b) 改名前 legacy 名的 sysctl conf（99-flvx-tuning.conf）：与
  # 旧谱系 agent 同名共用，归属判定见函数头注（C4-F02）——仍有旧
  # 式 gost 足迹时跳过并披露，无足迹时按本产品旧装遗留删除。
  if [[ -e "$FLMX_TUNING_SYSCTL_CONF_LEGACY" ]]; then
    if legacy_gost_footprint_present; then
      echo "⚠️ 检测到旧式 gost agent 足迹，跳过删除可能由其持有的调优文件: $FLMX_TUNING_SYSCTL_CONF_LEGACY（如确认属本产品旧装可人工删除）"
    elif rm -f "$FLMX_TUNING_SYSCTL_CONF_LEGACY"; then
      echo "🧹 删除调优宿主文件: $FLMX_TUNING_SYSCTL_CONF_LEGACY"
    else
      failures=$((failures + 1))
      echo "⚠️ 删除调优宿主文件失败: $FLMX_TUNING_SYSCTL_CONF_LEGACY"
    fi
  fi

  # 4) 队列整形 systemd unit：先 disable --now 再删文件，否则它
  # 在下次开机仍会去执行安装目录里已被删除的整形脚本（悬空启用
  # 项）。OpenRC 形态没有这个 unit，文件不存在即跳过。
  if [[ -e "$FLMX_TUNING_QDISC_UNIT_FILE" ]]; then
    if [[ "${SERVICE_MANAGER:-}" == "systemd" ]] && command -v systemctl >/dev/null 2>&1; then
      systemctl disable --now "$FLMX_TUNING_QDISC_UNIT" 2>/dev/null || true
    fi
    if rm -f "$FLMX_TUNING_QDISC_UNIT_FILE"; then
      echo "🧹 删除队列整形服务: $FLMX_TUNING_QDISC_UNIT_FILE"
      if [[ "${SERVICE_MANAGER:-}" == "systemd" ]] && command -v systemctl >/dev/null 2>&1; then
        systemctl daemon-reload 2>/dev/null || true
      fi
    else
      failures=$((failures + 1))
      echo "⚠️ 删除队列整形服务失败: $FLMX_TUNING_QDISC_UNIT_FILE"
    fi
  fi

  # 5) agent 自建 nft 三表：沿用 agent 侧 removeTables 的「先建空
  # 表再删」惯例，表不存在时整条脚本也成功（幂等）。没有 nft 命
  # 令时删不了表，明确提示残留与人工清理法，不假装已清理。其中
  # flvx_kernel 与旧谱系 agent 同名共用：仍有旧式 gost 足迹时只
  # 删 flmx 专名两表、该表跳过并披露（C4-F02，归属判定见函数头
  # 注）；无足迹时三表照常全删。
  if command -v nft >/dev/null 2>&1; then
    local nft_script="" table=""
    local nft_tables="$FLMX_AGENT_NFT_TABLES"
    local skipped_legacy_table=""
    if legacy_gost_footprint_present; then
      nft_tables=""
      for table in $FLMX_AGENT_NFT_TABLES; do
        if [[ "$table" == "flvx_kernel" ]]; then
          skipped_legacy_table="$table"
        else
          nft_tables="${nft_tables:+$nft_tables }$table"
        fi
      done
    fi
    if [[ -n "$nft_tables" ]]; then
      for table in $nft_tables; do
        nft_script+="table inet ${table} {}"$'\n'
        nft_script+="delete table inet ${table}"$'\n'
      done
      if printf '%s' "$nft_script" | nft -f - >/dev/null 2>&1; then
        echo "🧹 已删除 agent 自建 nft 表: $nft_tables"
      else
        failures=$((failures + 1))
        echo "⚠️ nft 表删除失败，表会残留至宿主机重启或人工清理（nft delete table inet <表名>）: $nft_tables"
      fi
    fi
    if [[ -n "$skipped_legacy_table" ]]; then
      echo "⚠️ 检测到旧式 gost agent 足迹，跳过删除与其同名的 nft 表: $skipped_legacy_table（如确认属本产品可人工执行 nft delete table inet $skipped_legacy_table）"
    fi
  else
    echo "ℹ️ 未找到 nft 命令，跳过 nft 表清理（如曾建表，表会残留至宿主机重启，或装回 nftables 后人工删除）: $FLMX_AGENT_NFT_TABLES"
  fi

  if [[ "$failures" -gt 0 ]]; then
    echo "⚠️ 宿主清理有 ${failures} 项未完成（见上 ⚠️ 行），其余卸载步骤继续执行"
  fi
  return 0
}

# R2-F7-I02: 解析 agent 服务定义中配置的内核目录覆盖（FLMX_KERNEL_DIR；
# 旧名前缀 FLVX_KERNEL_DIR 的双读已随 agent 侧 ResolveBaseDir 于
# 1.9.10 一并退场，本函数只认新名、同名以最后赋值为准）。来源只限本服务自己的
# systemd 单元文件与 systemctl edit 落盘的 drop-in 覆盖文件
# （<单元>.d/*.conf）——脚本形态下该变量只经这两处进入 agent 进程
# 环境（见 doc/guide/install.md「内核目录覆盖」节）。无配置输出空。
configured_kernel_dir_for() {
  local unit="$1"
  local files="$unit"
  local dropin
  for dropin in "${unit}.d"/*.conf; do
    [[ -f "$dropin" ]] && files="$files $dropin"
  done
  local dir=""
  # files 是本函数拼的服务定义文件清单，须词分割成多个 grep 参数。
  dir=$(grep -hoE 'FLMX_KERNEL_DIR=[^[:space:]]+' $files 2>/dev/null | tail -n 1 | cut -d= -f2- | tr -d "\"'")
  printf '%s' "${dir%/}"
}

# 现行单元优先、存量单元回退（未迁存量节点的覆盖只写在旧单元里）。
configured_kernel_dir() {
  local dir
  dir=$(configured_kernel_dir_for "$FLMX_AGENT_SYSTEMD_SERVICE_FILE")
  if [[ -z "$dir" ]]; then
    dir=$(configured_kernel_dir_for "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE")
  fi
  printf '%s' "$dir"
}

# R2-F7-I02: 判定自定义内核目录是否随卸载清理，结果写入全局
# CUSTOM_KERNEL_DIR（空＝不单独清理）。调用点在 uninstall_flmx_agent
# 已通过归属守卫之后（归属确认前提）；目录值来自 agent 自己的服务
# 定义，仍须过形态校验：绝对路径、至少两级、不在系统目录名单内、
# 不是安装目录或其默认内核子目录（那两处随安装目录一并删除）。配
# 置值形态不可确认时不删、点名披露请人工确认——宁可留痕不误删。
# 目录非空且找不到任何内核状态痕迹（state.json 或子目录）时同理。
resolve_custom_kernel_dir() {
  CUSTOM_KERNEL_DIR=""
  local dir
  dir=$(configured_kernel_dir)
  [[ -n "$dir" ]] || return 0
  if [[ "$dir" == "$INSTALL_DIR" || "$dir" == "$INSTALL_DIR/kernels" || \
        "$dir" == "$LEGACY_INSTALL_DIR" || "$dir" == "$LEGACY_INSTALL_DIR/kernels" ]]; then
    return 0
  fi
  case "$dir" in
    /*/*) ;;
    *)
      echo "⚠️ FLMX_KERNEL_DIR 配置值（$dir）不是两级以上的绝对路径，卸载不自动清理该目录，请人工确认"
      return 0
      ;;
  esac
  case "$dir" in
    /etc|/usr|/var|/bin|/sbin|/lib|/lib64|/boot|/dev|/proc|/sys|/run|/home|/root|/opt|/srv|/mnt|/media|/tmp)
      echo "⚠️ FLMX_KERNEL_DIR 指向系统目录（$dir），卸载不自动清理，请人工确认"
      return 0
      ;;
  esac
  if [[ -d "$dir" && -n "$(ls -A "$dir" 2>/dev/null)" && ! -f "$dir/state.json" ]]; then
    local entry has_subdir=""
    for entry in "$dir"/*; do
      if [[ -d "$entry" ]]; then
        has_subdir="1"
        break
      fi
    done
    if [[ -z "$has_subdir" ]]; then
      echo "⚠️ 自定义内核目录 $dir 内未见内核状态痕迹（state.json 或子目录），卸载不自动清理，请人工确认"
      return 0
    fi
  fi
  CUSTOM_KERNEL_DIR="$dir"
}

# 卸载功能
uninstall_flmx_agent() {
  echo "🗑️ 开始卸载 flmx_agent..."

  # 布局双认（改名）：卸载对象是本机实际在位的布局——现行布局、
  # 改名前的存量布局、或（异常残留时）两者都在位。归属守卫
  # （AI-06）对每个在位布局各跑一次：在停服、删文件之前先判定
  # 归属并明示当前连接地址；任一无法确认属于本面板时终止，不留
  # 改动。无 agent 的目录守卫恒放行，故两遍调用对单布局节点无
  # 额外打扰。
  local has_current="" has_legacy=""
  agent_dir_has_agent "$INSTALL_DIR" && has_current="1"
  agent_dir_has_agent "$LEGACY_INSTALL_DIR" && has_legacy="1"
  guard_agent_dir_ownership_menu "卸载" "继续卸载会停止并删除它，该节点将从其所属面板掉线。" || return 1
  if [[ -n "$has_legacy" ]]; then
    guard_agent_dir_ownership_menu "卸载" "继续卸载会停止并删除它，该节点将从其所属面板掉线。" "$LEGACY_INSTALL_DIR" || return 1
  fi

  ensure_service_manager || return 1

  # R2-F7-I02：先于删服务文件读出自定义内核目录（值在单元/drop-in
  # 里，服务文件删除后无从查起；现行与存量单元都查，见
  # configured_kernel_dir），卸载末尾一并清理。
  resolve_custom_kernel_dir

  local confirm_prompt="确认卸载 flmx_agent 吗？将停止服务，并删除安装目录、agent 服务文件、调优宿主文件与自建 nft 表，同时恢复调优前的系统参数"
  if [[ -n "$has_legacy" ]]; then
    confirm_prompt="$confirm_prompt（检测到改名前的存量布局 $LEGACY_INSTALL_DIR，将一并卸载）"
  fi
  if [[ -n "$CUSTOM_KERNEL_DIR" ]]; then
    confirm_prompt="$confirm_prompt，并删除自定义内核目录 $CUSTOM_KERNEL_DIR"
  fi
  read -p "$confirm_prompt (y/N): " confirm
  if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
    echo "❌ 取消卸载"
    # R2-F7-01（与 panel_install.sh 的 C4-F04 同口径）：用户取消置
    # 标志，main 见标志不自删、退出 0；返回码维持 0 的干净 no-op
    # 口径——什么都没卸，不谎报「操作已完成」、不删脚本。
    AGENT_OP_CANCELLED="1"
    return 0
  fi

  # 停止并禁用服务（在位的布局逐个处理）
  if flmx_agent_service_exists; then
    echo "🛑 停止并禁用服务..."
    stop_flmx_agent_service
    disable_flmx_agent_service
  fi
  if legacy_flux_agent_service_exists; then
    echo "🛑 停止并禁用改名前的存量服务..."
    stop_legacy_flux_agent_service
    disable_legacy_flux_agent_service
  fi

  # R6-02：宿主级残留清理（恢复调优原值与队列、删调优宿主文件
  # 与队列整形 unit、删 agent 自建 nft 表），与面板自卸载路径对
  # 齐。必须在删安装目录之前执行：调优原值备份 tuning-backup.json
  # 与队列回放脚本都在安装目录里，目录删掉原值就永久丢失。双布
  # 局都在位时逐目录各跑一遍（清理本身幂等，宿主文件/nft 表在第
  # 一遍后即不存在，第二遍为空转核对）。
  if [[ -d "$INSTALL_DIR" ]]; then
    uninstall_flmx_agent_host_cleanup "$INSTALL_DIR"
  fi
  if [[ -n "$has_legacy" && -d "$LEGACY_INSTALL_DIR" ]]; then
    uninstall_flmx_agent_host_cleanup "$LEGACY_INSTALL_DIR"
  fi

  # 删除服务文件
  if [[ -f "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" || -f "$FLMX_AGENT_OPENRC_SERVICE_FILE" ]]; then
    remove_flmx_agent_service
    echo "🧹 删除服务文件"
  fi
  if [[ -f "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" || -f "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE" ]]; then
    remove_legacy_flux_agent_service
    echo "🧹 删除改名前的存量服务文件"
  fi

  # 删除安装目录
  if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    echo "🧹 删除安装目录: $INSTALL_DIR"
  fi
  if [[ -d "$LEGACY_INSTALL_DIR" ]]; then
    rm -rf "$LEGACY_INSTALL_DIR"
    echo "🧹 删除改名前的存量安装目录: $LEGACY_INSTALL_DIR"
  fi

  # R2-F7-I02：自定义内核目录在安装目录之外，单独清理（判定见
  # resolve_custom_kernel_dir，已在确认提示中点名）。
  if [[ -n "$CUSTOM_KERNEL_DIR" && -d "$CUSTOM_KERNEL_DIR" ]]; then
    rm -rf "$CUSTOM_KERNEL_DIR"
    echo "🧹 删除自定义内核目录: $CUSTOM_KERNEL_DIR"
  fi

  echo "✅ 卸载完成"
}

# R4B11-004: 前置 root 检查（对齐 panel_install.sh 的 F-125）。安装/
# 更新/卸载都要写 /etc/flmx_agent 与系统服务文件；旧实现无此前置，
# 非 root 运行要到 mkdir/下载落盘时才失败，且失败点落在下载分支、
# 被误报为「下载失败，请检查网络」，掩盖真实根因（权限）。这里在
# 任何落盘动作之前 fail-fast。独立成函数便于测试夹具以恒真桩承接
# （X-P3-6 夹具以非 root 身份实跑 main 的返回码传播）。
require_root_privileges() {
  if [[ $EUID -ne 0 ]]; then
    echo "❌ install.sh 需要 root 权限运行（涉及 /etc/flmx_agent 与系统服务写入）"
    echo "   请使用 root 用户或 sudo 重新执行"
    return 1
  fi
  return 0
}

# 主逻辑
main() {
  require_root_privileges || exit 1

  # X-P3-6 (J-03, BC-12): 各分支捕获操作函数返回码并以其退出。
  # 旧实现无条件 exit 0，菜单更新/卸载失败（函数以 return 1 报告）
  # 也被以退出码判断成败的自动化消费者误判为成功；成功路径仍
  # exit 0。delete_self 接收同一返回码分流（R4B11-005：失败保留
  # 脚本、成功才自删）。install_flmx_agent 内部直接 exit 1 的失败
  # 路径不受影响（进程已以 1 退出）。
  local rc=0

  # 如果提供了命令行参数，直接执行安装
  if [[ -n "$SERVER_ADDR" && -n "$SECRET" ]]; then
    install_flmx_agent || rc=$?
    delete_self "$rc"
    exit "$rc"
  fi

  # 显示交互式菜单
  while true; do
    show_menu
    # R2-F7-03：stdin EOF 时 read 返回失败、choice 为空，旧实现走无
    # 效选项分支后 while true 继续——install.sh 无 set -e 兜底，形
    # 成刷屏忙循环。与 panel_install.sh 同行为：EOF 即退出（其
    # set -e 下同一 read 失败令脚本以 1 退出）。
    if ! read -p "请输入选项 (1-4): " choice; then
      echo ""
      echo "❌ 输入已结束（EOF），退出脚本"
      exit 1
    fi

    case $choice in
      1)
        install_flmx_agent || rc=$?
        delete_self "$rc"
        exit "$rc"
        ;;
      2)
        update_flmx_agent || rc=$?
        delete_self "$rc"
        exit "$rc"
        ;;
      3)
        AGENT_OP_CANCELLED=""
        uninstall_flmx_agent || rc=$?
        # R2-F7-01：用户取消——操作函数已打印取消说明并置标志，
        # 不自删、退出 0（与 panel_install.sh 的 C4-F04 同口径）。
        if [[ "${AGENT_OP_CANCELLED:-}" == "1" ]]; then
          exit 0
        fi
        delete_self "$rc"
        exit "$rc"
        ;;
      4)
        echo "👋 退出脚本"
        delete_self
        exit 0
        ;;
      *)
        echo "❌ 无效选项，请输入 1-4"
        echo ""
        ;;
    esac
  done
}

# 执行主函数
main
