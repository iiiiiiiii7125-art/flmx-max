#!/bin/bash
set -e

# 解决 macOS 下 tr 可能出现的非法字节序列问题
export LANG=en_US.UTF-8
export LC_ALL=C



# GitHub repo used for release downloads
REPO="iiiiiiiii7125-art/flmx-max"

# 脚本版本号：每次修改脚本后递增，确保用户拉到最新版
# 自更新检查会比对仓库中的最新版本，不一致时自动重拉
SCRIPT_VERSION="20261101.03"

# --- 自更新检查 ---
# F-006：旧实现从 main 分支 raw 拉脚本、仅比对 SCRIPT_VERSION 字符串就
# 覆写自身以 root 重执行，无任何完整性校验，绕过同文件 download_verified
# 的 fail-closed 标准与 PINNED_VERSION 定版语义。现改为只走 Release 资产：
#   目标版本由 resolve_version() 解析（VERSION > FLUX_VERSION >
#   PINNED_VERSION > 最新 Release）。Release 副本带 PINNED_VERSION 时目标
#   恒为其自身版本，定版脚本不会被自更新带跑偏到更新的版本。
#   新脚本经 download_verified 拉取同版本资产的 .sha256 校验，通过才覆写。
# 缓存与校验如何兼得：旧实现用 cache-buster 是因为 main 分支 raw 内容
# 可变、CDN 可能发回旧副本。版本号资产 URL 不可变——新版本必然是新 URL；
# 缓存最多发回「该版本自己的脚本 + 该版本自己的 .sha256」，二者自洽且
# 仍须过 sha256 校验，不存在新旧混杂，无需 cache-buster。
# 校验被显式关闭（FLMX_VERIFY_DOWNLOADS!=1）时跳过自更新：绝不执行
# 未校验的新脚本，继续使用当前版本（fail-closed）。
# LANE1（2026-10-21 横跳事故复盘）：sha256 只能证明「下载的脚本与它的
# .sha256 自洽」，证明不了「这份资产就是目标 Release 的定版脚本」；
# 且旧比对只判字符串不等、无方向。镜像缓存混杂/定版注入失效时，
# 未定版副本 + latest 逐次翻转可让两个版本互相「更新」无限横跳，
# latest 回退时还会把脚本静默降级。三条不变量收口（任一不过即跳过
# 本次自更新、继续用当前脚本，fail-closed）：
#   1) 定版一致：下载副本的 PINNED_VERSION 必须等于本次解析的目标
#      版本——资产不是该 Release 的定版脚本就不接管；
#   2) 方向单调：仅当下载副本 SCRIPT_VERSION（YYYYMMDD.NN 定宽格式，
#      字典序即时间序）严格大于当前版才换；格式不可辨认同样拒绝
#      （无法确认方向就不动）；
#   3) 单跳上限：换版 exec 时置 FLMX_SELF_UPDATE_HOPS=1，新进程据此
#      不再自更新——任何未知翻转模式下至多跳一次。
# 注：调用点在本文件末尾（main 之前）——此处仅定义函数，因为
# resolve_version / 路由助手 / download_verified 定义在后面。
self_update_check() {
  # 通过进程替换 bash <(...) 执行时 $0 不是常规文件，跳过自更新；
  # 被 source 加载（测试夹具）时 BASH_SOURCE 与 $0 不一致，同样跳过。
  if [ ! -f "$0" ] || [ "${BASH_SOURCE[0]}" != "$0" ]; then
    return 0
  fi
  if [ "${FLMX_SELF_UPDATE_HOPS:-0}" != "0" ]; then
    return 0
  fi
  if [ "${FLMX_VERIFY_DOWNLOADS:-${FLVX_VERIFY_DOWNLOADS:-1}}" != "1" ]; then
    echo "⚠️ 下载校验未开启，跳过脚本自更新（不执行未校验的新脚本）"
    return 0
  fi
  local version tmp_script latest_version pinned_version
  version=$(resolve_version 2>/dev/null) || {
    echo "⚠️ 无法解析目标版本，跳过脚本自更新"
    return 0
  }
  tmp_script=$(mktemp) || return 0
  # 1.7.7：自更新下载同样走内置路线直选（download_verified_routed），
  # F-006 三不变量（定版一致/方向单调/单跳）不变。
  if ! download_verified_routed "https://github.com/${REPO}/releases/download/${version}/panel_install.sh" "$tmp_script"; then
    rm -f "$tmp_script"
    echo "⚠️ 新版脚本下载或校验失败，继续使用当前版本"
    return 0
  fi
  latest_version=$(grep -m1 '^SCRIPT_VERSION=' "$tmp_script" | cut -d'"' -f2 || true)
  if [ -z "$latest_version" ] || [ "$latest_version" = "$SCRIPT_VERSION" ]; then
    rm -f "$tmp_script"
    return 0
  fi
  pinned_version=$(grep -m1 '^PINNED_VERSION=' "$tmp_script" | cut -d'"' -f2 || true)
  if [ "$pinned_version" != "$version" ]; then
    rm -f "$tmp_script"
    echo "⚠️ 下载的脚本未定版到 v$version（PINNED_VERSION=${pinned_version:-空}），跳过脚本自更新"
    return 0
  fi
  if ! [[ "$SCRIPT_VERSION" =~ ^[0-9]{8}\.[0-9]{2}$ && "$latest_version" =~ ^[0-9]{8}\.[0-9]{2}$ ]]; then
    rm -f "$tmp_script"
    echo "⚠️ 脚本版本号格式不可辨认（当前 v$SCRIPT_VERSION / 目标 v$latest_version），无法确认更新方向，跳过脚本自更新"
    return 0
  fi
  if [[ ! "$latest_version" > "$SCRIPT_VERSION" ]]; then
    rm -f "$tmp_script"
    echo "⚠️ 目标脚本 v$latest_version 不高于当前 v$SCRIPT_VERSION，跳过脚本自更新（防降级）"
    return 0
  fi
  echo "🔄 检测到新版本脚本 (v$latest_version，当前 v$SCRIPT_VERSION)，正在更新..."
  if cat "$tmp_script" > "$0" && chmod +x "$0"; then
    rm -f "$tmp_script"
    echo "✅ 已更新到 v$latest_version，重新执行..."
    FLMX_SELF_UPDATE_HOPS=1 exec bash "$0" "$@"
  fi
  rm -f "$tmp_script"
  echo "⚠️ 自动更新失败，继续使用当前版本"
}

# 固定版本号（Release 构建时自动填充，留空则获取最新版）
PINNED_VERSION=""

# GitHub 下载路线配置（1.7.7）：默认走内置候选测速直选（见下方
# PROXY_CANDIDATES 与 ensure_download_routes），不再交互询问、不再
# 读面板的「GitHub 加速」设置（该设置已整体移除）。环境变量仅作
# 运维逃生口：PROXY_ENABLED=false 强制直连；PROXY_URL=<前缀> 为
# 手动覆盖，固定单条代理前缀路线。
PROXY_ENABLED="${PROXY_ENABLED:-}"
PROXY_URL="${PROXY_URL:-}"
DEFAULT_PANEL_BACKEND_CONTAINER="flux-panel-backend"
DEFAULT_PANEL_POSTGRES_CONTAINER="flux-panel-postgres"
PANEL_SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# ---- 内置 GitHub 下载候选与测速直选（1.7.7）----
# 候选清单全产品统一（面板后端 github_routes.go、agent 侧、
# install.sh 同表同序，改一处须同步其余）：9 个代理前缀 + 直连
# （""）。路线选择口径（与面板侧一致）：
#   1) 先单独探测直连（真实目标、~3-5s）：通 → 只用直连，不测代理；
#   2) 直连不通 → 并行探测 9 个代理按延迟排名，未判活的按清单原
#      序殿后，直连排最末兜底；已选路线下载失败时逐个续切；
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
  if [[ "$PROXY_ENABLED" == "false" ]]; then
    echo "direct"
  elif [[ -n "$PROXY_URL" ]]; then
    echo "override"
  else
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

# rank_proxy_routes：并行探测全部代理候选（单候选超时 ~5s、一轮
# 总界有界），活者按延迟升序、未判活者按清单原序殿后，结果写入
# PROXY_RANKED（只含代理，不含直连）。
rank_proxy_routes() {
  PROXY_RANKED=()
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
  done < <(cat "$tmpdir"/* 2>/dev/null | sort -n)
  rm -rf "$tmpdir"
  local seen=" ${PROXY_RANKED[*]} "
  for prefix in "${PROXY_CANDIDATES[@]}"; do
    case "$seen" in
      *" $prefix "*) ;;
      *) PROXY_RANKED+=("$prefix") ;;
    esac
  done
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
      if probe_route "" >/dev/null; then
        DOWNLOAD_ROUTES=("")
        ROUTES_STAGE=1
        echo "🌐 GitHub 下载路线: 直连" >&2
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
# 并追加；有新增路线返回 0，否则返回 1。
extend_download_routes() {
  if [[ "$(route_mode)" == "builtin" && "$ROUTES_STAGE" == "1" ]]; then
    rank_proxy_routes
    DOWNLOAD_ROUTES+=("${PROXY_RANKED[@]}")
    ROUTES_STAGE=2
    return 0
  fi
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
  ensure_download_routes
  local prefix
  while :; do
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' -L --connect-timeout 5 --max-time 30 "$(route_url "$prefix" "$latest_url")" 2>/dev/null || true)
      tag="${effective_url##*/}"
      # H-24: 无 Release 时 /releases/latest 会 302 到 /releases 索引页，
      # 此时解析出的是垃圾值 "releases" 而非版本号，必须按版本格式校验。
      if [[ "$tag" =~ $tag_pattern ]]; then
        echo "$tag"
        return 0
      fi

      api_tag=$(curl -fsSL --connect-timeout 5 --max-time 30 "$(route_url "$prefix" "$api_url")" 2>/dev/null | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)
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

  echo "❌ 无法获取最新版本号。你可以手动指定版本，例如：VERSION=<版本号> ./panel_install.sh" >&2
  return 1
}

# A11-132-03: 点分数字版本比较（与 self_update_check 的防降级口径对齐）。
# 回显 -1/0/1 表示 a 小于/等于/大于 b；任一侧为空或含非数字段时不可比，
# 回显空并返回 1——调用方不得据此拦截（开发/测试标签等场景保持放行）。
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

# 根据版本号设置 compose 下载地址（1.7.7：只存 canonical 地址，
# 路线前缀在 download_verified_routed 实际下载时按路线表拼）
set_compose_urls_by_version() {
  local version="$1"
  DOCKER_COMPOSEV4_URL="https://github.com/${REPO}/releases/download/${version}/docker-compose-v4.yml"
  DOCKER_COMPOSEV6_URL="https://github.com/${REPO}/releases/download/${version}/docker-compose-v6.yml"
}

ensure_compose_urls_initialized() {
  if [[ -n "${DOCKER_COMPOSEV4_URL:-}" && -n "${DOCKER_COMPOSEV6_URL:-}" ]]; then
    return 0
  fi

  RESOLVED_VERSION=$(resolve_version) || return 1
  set_compose_urls_by_version "$RESOLVED_VERSION"
}



# 根据IPv6支持情况选择docker-compose URL
get_docker_compose_url() {
  if check_ipv6_support > /dev/null 2>&1; then
    echo "$DOCKER_COMPOSEV6_URL"
  else
    echo "$DOCKER_COMPOSEV4_URL"
  fi
}

# 检查 docker-compose 或 docker compose 命令。
# 默认（安装路径）：未安装则自动安装。
# noinstall（更新/迁移/卸载路径）：未检测到 Docker 时直接返回失败——没有
# Docker 就不可能有容器化的面板部署，绝不为这些操作反向安装 Docker。
check_docker() {
  local noinstall="${1:-}"
  if command -v docker-compose &> /dev/null; then
    DOCKER_CMD="docker-compose"
  elif command -v docker &> /dev/null; then
    if docker compose version &> /dev/null; then
      DOCKER_CMD="docker compose"
    else
      echo "错误：检测到 docker，但不支持 'docker compose' 命令。请安装 docker-compose 或更新 docker 版本。"
      exit 1
    fi
  elif [[ "$noinstall" == "noinstall" ]]; then
    return 1
  else
    echo "⚠️  未检测到 Docker，正在自动安装..."
    install_docker || {
      echo "错误：Docker 自动安装失败，请手动安装后重试。"
      echo "  手动安装：curl -fsSL https://get.docker.com | sh"
      exit 1
    }
    # 安装后重新检测
    if command -v docker &> /dev/null && docker compose version &> /dev/null; then
      DOCKER_CMD="docker compose"
    elif command -v docker-compose &> /dev/null; then
      DOCKER_CMD="docker-compose"
    else
      echo "错误：Docker 安装后仍无法使用，请检查安装日志。"
      exit 1
    fi
  fi
  echo "检测到 Docker 命令：$DOCKER_CMD"
}

# F-050: Docker 官方安装脚本的 pin 校验值（get.docker.com 脚本，
# 2026-10-21 实测抓取计算；官方脚本变更后须人工更新此值）。旧实现
# curl|sh 不经任何校验以 root 执行，与本文件对自家资产的 fail-closed
# 标准双标。现改为：下载到临时文件 → sha256 与 pin 值比对 → 通过才
# 执行；校验不过/无校验值/缺 sha256sum 一律不执行，回退系统包管理器
# 签名通道。阿里云镜像脚本暂无可信 pin 值（其 URL 2026-10-21 实测
# 404），默认不执行；确需使用时经 FLMX_DOCKER_INSTALL_SHA256 显式
# 提供该来源脚本的 sha256（该变量对两个来源都可覆盖 pin 值）。
DOCKER_INSTALL_SCRIPT_SHA256="7f966774378d94acf1817eb1aba213c21b576d01d1ee2f0656fe36e75ffa1258"

run_verified_installer_script() {
  local url="$1"
  local expected="$2"
  if [[ -z "$expected" ]]; then
    echo "⚠️ 未配置该来源的脚本校验值，跳过脚本安装: $url"
    return 1
  fi
  if ! command -v sha256sum >/dev/null 2>&1; then
    echo "⚠️ 缺少 sha256sum，无法校验安装脚本，跳过脚本安装: $url"
    return 1
  fi
  local tmp actual rc
  tmp=$(mktemp) || return 1
  if ! curl -fsSL "$url" -o "$tmp"; then
    rm -f "$tmp"
    echo "⚠️ 安装脚本下载失败: $url"
    return 1
  fi
  actual=$(sha256sum "$tmp" | awk '{print $1}')
  if [[ "$actual" != "$expected" ]]; then
    rm -f "$tmp"
    echo "❌ 安装脚本校验失败（期望 $expected，实际 $actual），拒绝执行: $url"
    return 1
  fi
  echo "✅ 安装脚本校验通过: $url"
  sh "$tmp"
  rc=$?
  rm -f "$tmp"
  return $rc
}

# 自动安装 Docker（支持 Debian/Ubuntu、RHEL 系、Alpine）
install_docker() {
  echo "🔧 正在安装 Docker..."

  # 优先使用官方一键脚本（自动识别发行版，经 F-050 pin 校验后才执行）
  # 国内服务器可设置 DOCKER_MIRROR=aliyun 使用阿里云镜像加速
  local docker_url="https://get.docker.com"
  local docker_script_sha="$DOCKER_INSTALL_SCRIPT_SHA256"
  if [ "${DOCKER_MIRROR:-}" = "aliyun" ]; then
    docker_url="https://mirrors.aliyun.com/docker-ce/linux/docker-install.sh"
    docker_script_sha=""
  fi
  docker_script_sha="${FLMX_DOCKER_INSTALL_SHA256:-$docker_script_sha}"

  if command -v curl &> /dev/null; then
    if run_verified_installer_script "$docker_url" "$docker_script_sha"; then
      echo "✅ Docker 安装完成"
      # 启动并设置开机自启
      systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true
      return 0
    fi
  fi

  echo "官方脚本安装失败或未通过校验，尝试系统包管理器..."
  if [ -f /etc/debian_version ]; then
    apt-get update && apt-get install -y docker.io docker-compose-plugin && return 0
  elif [ -f /etc/redhat-release ]; then
    if command -v dnf &> /dev/null; then
      dnf install -y docker docker-compose-plugin && return 0
    else
      yum install -y docker docker-compose-plugin 2>/dev/null || yum install -y docker && return 0
    fi
  elif [ -f /etc/alpine-release ]; then
    apk add --no-cache docker docker-compose && return 0
  fi

  return 1
}

# 检测系统是否支持 IPv6
check_ipv6_support() {
  echo "🔍 检测 IPv6 支持..."

  # 检查是否有 IPv6 地址（排除 link-local 地址）
  if ip -6 addr show | grep -v "scope link" | grep -q "inet6"; then
    echo "✅ 检测到系统支持 IPv6"
    return 0
  elif ifconfig 2>/dev/null | grep -v "fe80:" | grep -q "inet6"; then
    echo "✅ 检测到系统支持 IPv6"
    return 0
  else
    echo "⚠️ 未检测到 IPv6 支持"
    return 1
  fi
}



# 配置 Docker 启用 IPv6
configure_docker_ipv6() {
  echo "🔧 配置 Docker IPv6 支持..."

  # 检查操作系统类型
  OS_TYPE=$(uname -s)

  if [[ "$OS_TYPE" == "Darwin" ]]; then
    # macOS 上 Docker Desktop 已默认支持 IPv6
    echo "✅ macOS Docker Desktop 默认支持 IPv6"
    return 0
  fi

  # Docker daemon 配置文件路径
  DOCKER_CONFIG="/etc/docker/daemon.json"

  # 检查是否需要 sudo
  if [[ $EUID -ne 0 ]]; then
    SUDO_CMD="sudo"
  else
    SUDO_CMD=""
  fi

  # 检查 Docker 配置文件
  if [ -f "$DOCKER_CONFIG" ]; then
    # 检查是否已经配置了 IPv6
    if grep -q '"ipv6"' "$DOCKER_CONFIG"; then
      echo "✅ Docker 已配置 IPv6 支持"
    else
      echo "📝 更新 Docker 配置以启用 IPv6..."
      # 备份原配置
      $SUDO_CMD cp "$DOCKER_CONFIG" "${DOCKER_CONFIG}.backup"

      # 使用 jq 或 sed 添加 IPv6 配置
      if command -v jq &> /dev/null; then
        $SUDO_CMD jq '. + {"ipv6": true, "fixed-cidr-v6": "fd00::/80"}' "$DOCKER_CONFIG" > /tmp/daemon.json && $SUDO_CMD mv /tmp/daemon.json "$DOCKER_CONFIG"
      else
        # 如果没有 jq，使用 sed
        $SUDO_CMD sed -i 's/^{$/{\n  "ipv6": true,\n  "fixed-cidr-v6": "fd00::\/80",/' "$DOCKER_CONFIG"
        # AUDIT-R1 (SCR-009): 该正则只匹配"整行仅为 {" 的多行 JSON；单行压缩
        # JSON 无匹配时 sed 静默无操作但退出码为 0。写后由下方统一校验兜底。
      fi

      # F-052: 写后校验（jq 与 sed 两条路径都过这里）：配置里没有
      # "ipv6": true 就算失败并返回非 0，让安装路径据此降级 v4，
      # 不再带着未生效的配置按 v6 继续（旧行为只告警）。
      if ! $SUDO_CMD grep -q '"ipv6"[[:space:]]*:[[:space:]]*true' "$DOCKER_CONFIG" 2>/dev/null; then
        echo "❌ IPv6 配置写入失败，请手动在 $DOCKER_CONFIG 添加 \"ipv6\": true 后重试"
        return 1
      fi

      echo "🔄 重启 Docker 服务..."
      restart_docker_checked || return 1
      sleep 5
    fi
  else
    # 创建新的配置文件
    echo "📝 创建 Docker 配置文件..."
    $SUDO_CMD mkdir -p /etc/docker
    echo '{
  "ipv6": true,
  "fixed-cidr-v6": "fd00::/80"
}' | $SUDO_CMD tee "$DOCKER_CONFIG" > /dev/null

    echo "🔄 重启 Docker 服务..."
    restart_docker_checked || return 1
    sleep 5
  fi
}

# F-052: 重启 Docker 并检查结果。重启失败/无可用重启方式时返回非 0，
# 让调用方能据此降级，而不是带着未生效的配置继续。
restart_docker_checked() {
  : "${SUDO_CMD:=}"
  if command -v systemctl &> /dev/null; then
    $SUDO_CMD systemctl restart docker
  elif command -v service &> /dev/null; then
    $SUDO_CMD service docker restart
  else
    echo "⚠️ 未找到 systemctl/service，无法自动重启 Docker，请手动重启后重试"
    return 1
  fi
}

# F-052: 验证 Docker IPv6 是否真的可用：daemon 可达且默认 bridge 网络
# 启用了 IPv6。主机有 IPv6 地址不等于 Docker 能建 v6 网络（daemon 未
# 启用、重启失败都会让 v6 compose 在建网一步才炸），选版前先证实。
docker_ipv6_usable() {
  docker info >/dev/null 2>&1 || return 1
  local bridge_v6
  bridge_v6=$(docker network inspect bridge --format '{{.EnableIPv6}}' 2>/dev/null || echo "")
  [[ "$bridge_v6" == "true" ]]
}

# R4B11-001: 抽出 Caddyfile 中「指定域名站点块之外」的全部内容，供菜单 5
# 保留式重写：其他站点块（含菜单 6 追加的订阅前置块）、全局选项块与块外
# 行（注释/import 等）逐字保留，仅剔除地址命中 domain 的站点块（支持与
# 菜单 6 幂等检查相同的多地址行与 scheme/端口写法）。按花括号深度切块，
# 结构不完整的尾部内容按保留处理，宁可多留不可误删。
caddyfile_without_site() {
  local file="$1" domain="$2"
  awk -v d="$domain" '
    function flush_block(   line, n, parts, i, tok, keep) {
      if (buf == "") return
      keep = 1
      if (first != "{") {
        line = first
        sub(/\{[[:space:]]*$/, "", line)
        n = split(line, parts, /[[:space:],]+/)
        for (i = 1; i <= n; i++) {
          tok = parts[i]
          sub(/^https?:\/\//, "", tok)
          sub(/:[0-9]+$/, "", tok)
          if (tok == d) { keep = 0; break }
        }
      }
      if (keep) printf "%s", buf
      buf = ""
    }
    {
      line = $0
      tmp = line; opens = gsub(/\{/, "{", tmp)
      tmp = line; closes = gsub(/\}/, "}", tmp)
      if (!inblock) {
        if (opens > 0) {
          first = line; buf = line "\n"; depth = opens - closes
          if (depth <= 0) flush_block(); else inblock = 1
        } else {
          print line
        }
      } else {
        buf = buf line "\n"
        depth += opens - closes
        if (depth <= 0) { flush_block(); inblock = 0 }
      }
    }
    END { if (inblock) printf "%s", buf }
  ' "$file"
}

# 配置域名反向代理（Caddy 自动 HTTPS）
setup_domain_proxy() {
  echo "🌐 配置域名反向代理"
  echo ""
  read -p "请输入域名（如 panel.example.com）: " DOMAIN
  DOMAIN=$(echo "$DOMAIN" | tr -d ' \n\r\t')
  if [ -z "$DOMAIN" ]; then
    echo "❌ 域名不能为空"
    return 1
  fi
  # AUDIT-R1 (SCR-019): 动态证实 DOMAIN 中的 $()/反引号不构成命令注入
  #（heredoc 单遍展开），但非法字符串会原样写入 Caddyfile 导致 caddy
  # 重启失败。写文件前做字符集校验，非法输入直接拒绝。
  if [[ ! "$DOMAIN" =~ ^[A-Za-z0-9.*-]+$ ]]; then
    echo "❌ 域名含非法字符（仅允许字母/数字/./-/*）：$DOMAIN"
    return 1
  fi

  # 检查域名是否解析到本机
  echo "🔍 检查域名解析..."
  local resolved_ip
  resolved_ip=$(getent hosts "$DOMAIN" | awk '{print $1}' | head -1 || true)
  if [ -z "$resolved_ip" ]; then
    echo "⚠️  域名 $DOMAIN 未解析到任何 IP，请先配置 DNS 解析后再继续"
    read -p "仍要继续吗? (y/N): " _cont
    [[ "$_cont" =~ ^[Yy]$ ]] || return 1
  else
    echo "✅ 域名解析到: $resolved_ip"
  fi

  # 安装 Caddy（如未安装）
  if ! command -v caddy &> /dev/null; then
    echo "🔧 正在安装 Caddy..."
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl 2>/dev/null || true
    # F-050 变体（X1-407）：Caddy 官方源 GPG key 不再直灌 keyring——先落
    # 临时文件核对主指纹（65760C51…EA34，Caddy stable 发布密钥，
    # 2026-10-21 实测），不符即视为官方源不可用、走 Debian 仓库回退，
    # 避免用未核对的 key 当后续 apt 验签的信任锚。
    local caddy_key_tmp caddy_key_fp=""
    caddy_key_tmp=$(mktemp)
    if curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' -o "$caddy_key_tmp" 2>/dev/null; then
      caddy_key_fp=$(gpg --show-keys --with-colons "$caddy_key_tmp" 2>/dev/null | awk -F: '/^fpr:/{print $10; exit}')
    fi
    if [[ "$caddy_key_fp" == "65760C51EDEA2017CEA2CA15155B6D79CA56EA34" ]] \
      && gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg "$caddy_key_tmp" 2>/dev/null; then
      echo "✅ Caddy 官方源 GPG key 指纹校验通过"
    else
      echo "❌ Caddy 官方源 GPG key 获取失败或指纹不符，尝试 Debian 仓库版本..."
      apt-get update && apt-get install -y caddy || {
        rm -f "$caddy_key_tmp"
        echo "❌ Caddy 安装失败"
        return 1
      }
    fi
    rm -f "$caddy_key_tmp"
    if ! command -v caddy &> /dev/null; then
      # R8-J-D: 源列表先落临时文件，下载成功且非空才移入持久路径——
      # 旧实现未检查结果、吞 stderr 的 curl 直通 tee 写目标文件，
      # 失败/半截内容落盘并在运行结束后留存，持续污染本机后续所有
      # apt 操作（与上方 GPG key 的临时文件处置同形）。
      local caddy_list_tmp
      caddy_list_tmp=$(mktemp)
      if curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o "$caddy_list_tmp" 2>/dev/null && [[ -s "$caddy_list_tmp" ]]; then
        mv -f "$caddy_list_tmp" /etc/apt/sources.list.d/caddy-stable.list
        chmod 644 /etc/apt/sources.list.d/caddy-stable.list
      else
        rm -f "$caddy_list_tmp"
        echo "⚠️  Caddy 官方源列表下载失败，未写入 apt 源文件，尝试使用现有软件源安装..."
      fi
      apt-get update && apt-get install -y caddy || {
        echo "❌ Caddy 安装失败"
        return 1
      }
    fi
    echo "✅ Caddy 安装完成"
  else
    echo "✅ 检测到 Caddy 已安装"
  fi

  # 写 Caddyfile（用 127.0.0.1 避免 docker-proxy IPv6 转发问题）
  echo "📝 配置 Caddy..."
  # CADDYFILE_PATH 仅供测试夹具覆盖（与菜单 6 同钩子），默认即现行路径。
  local caddyfile="${CADDYFILE_PATH:-/etc/caddy/Caddyfile}"
  # AUDIT-R1 (SCR-018): caddy 手动二进制安装时 /etc/caddy 不存在，
  # 此处 cat 会 No such file or directory。先建目录（包管理器安装自带该目录，无害）。
  mkdir -p "$(dirname "$caddyfile")"
  # 备份已有配置（保留备份路径供下方 validate/restart 失败回滚使用）
  local caddyfile_bak=""
  if [ -f "$caddyfile" ]; then
    caddyfile_bak="${caddyfile}.bak.$(date +%s)"
    cp "$caddyfile" "$caddyfile_bak"
    echo "🗄️  已备份现有配置到 $caddyfile_bak"
  fi
  # AUDIT-R1 (SCR-003): 菜单 5 是独立流程，不经过 get_config_params，
  # 此处 FRONTEND_PORT 恒未设置。改为从部署目录 .env 读取用户实际安装的
  # 前端端口（更新流程的 resolve_panel_deployment 已证明该信息可得）；
  # 找不到才回退 6366。
  local _env_file _port
  if [[ -z "${FRONTEND_PORT:-}" ]]; then
    for _env_file in "${PANEL_DEPLOY_DIR:-}/.env" "./.env" "/opt/flmx-panel/.env" "/opt/flvx-panel/.env"; do
      [[ "$_env_file" == "/.env" ]] && continue
      _port=$(get_env_var "FRONTEND_PORT" "$_env_file" 2>/dev/null | tr -d ' \r\n\t"' | tr -d "'" || true)
      if [[ "$_port" =~ ^[0-9]+$ ]] && (( 10#$_port >= 1 && 10#$_port <= 65535 )); then
        FRONTEND_PORT="$_port"
        echo "📁 从 $_env_file 读取前端端口：$FRONTEND_PORT"
        break
      fi
    done
  fi
  # R4B11-001: 保留式写入。旧实现整文件覆盖，会静默抹掉菜单 6 已追加的
  # 订阅前置站点块（先 6 后 5 的运维序列下订阅前置随即失效、仅存于 .bak）。
  # 改为按站点块合并：先从刚备份的旧文件中抽出面板域名之外的全部内容，
  # 写完新的面板块后逐字附回，与菜单 6 的追加语义对齐。
  local preserved_blocks=""
  if [ -n "$caddyfile_bak" ]; then
    preserved_blocks=$(caddyfile_without_site "$caddyfile_bak" "$DOMAIN")
  fi
  cat > "$caddyfile" <<EOF
$DOMAIN {
    reverse_proxy 127.0.0.1:${FRONTEND_PORT:-6366}
}
EOF
  if [ -n "$preserved_blocks" ]; then
    printf '%s\n' "$preserved_blocks" >> "$caddyfile"
    echo "♻️ 已保留原配置中的其他站点块（订阅前置等配置不受本次修改影响）"
  fi

  # R4B11-002: 与菜单 6 同口径（SCR-020）先校验后生效：validate 不过即
  # 回滚备份且不 restart；restart 本身失败时不得被 set -e 在下方
  # is-active 检查之前吞掉——用 if 守卫接住返回码，失败即回滚坏文件、
  # 尝试以旧配置恢复服务，并落到日志指引分支。
  if command -v caddy &> /dev/null; then
    if ! caddy validate --config "$caddyfile" > /dev/null 2>&1; then
      if [ -n "$caddyfile_bak" ]; then
        echo "❌ Caddy 配置校验失败，已回滚到 $caddyfile_bak ，未做任何生效变更"
        cp "$caddyfile_bak" "$caddyfile"
      else
        echo "❌ Caddy 配置校验失败，未做任何生效变更"
        rm -f "$caddyfile"
      fi
      return 1
    fi
    echo "✅ Caddy 配置校验通过"
  fi

  if ! systemctl restart caddy; then
    echo "❌ Caddy 重启失败，正在回滚到之前的配置..."
    if [ -n "$caddyfile_bak" ]; then
      cp "$caddyfile_bak" "$caddyfile"
    else
      rm -f "$caddyfile"
    fi
    systemctl restart caddy || true
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
  sleep 3

  if systemctl is-active --quiet caddy; then
    echo ""
    echo "🎉 域名配置完成"
    echo "🌐 访问地址: https://$DOMAIN"
    echo "📜 HTTPS 证书由 Caddy 自动申请和续期"
    echo ""
    echo "⚠️  重要：请在面板 → 网站配置 中将「面板后端地址」改为 https://$DOMAIN"
    echo "   否则节点无法通过域名回连面板"
  else
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
}

# 配置订阅前置域名（Caddy 自动 HTTPS）：为订阅拉取另设一个前置域名，
# 与面板域名反代到同一前端端口，配合 Cloudflare 橙云承接订阅拉取，
# 绕开部分运营商对面板域名的阻断/烂路由。源站侧只是在现有 Caddyfile
# 上追加一个站点块（手工流程已在生产验证：追加→自动签证书→直连 200）。
# SCR-020: 与菜单 5 的整文件覆盖不同，本函数是追加语义：必须幂等
# （同名站点块已存在即拒绝重复追加）且先校验后生效（caddy validate
# 不过即回滚备份、不 restart），否则重复运行会写出同名站点块、坏配置
# 会在 restart 时连面板域名的反代一起打断。
setup_subscription_front_domain() {
  echo "🌐 配置订阅前置域名"
  echo ""
  read -p "请输入订阅前置域名（如 sub.example.com）: " SUB_DOMAIN
  SUB_DOMAIN=$(echo "$SUB_DOMAIN" | tr -d ' \n\r\t')
  if [ -z "$SUB_DOMAIN" ]; then
    echo "❌ 域名不能为空"
    return 1
  fi
  # 字符集校验同 SCR-019：非法字符串会原样写入 Caddyfile 导致 caddy
  # 加载失败，写文件前直接拒绝。
  if [[ ! "$SUB_DOMAIN" =~ ^[A-Za-z0-9.*-]+$ ]]; then
    echo "❌ 域名含非法字符（仅允许字母/数字/./-/*）：$SUB_DOMAIN"
    return 1
  fi

  # 前置检查：前置域名复用面板的 Caddy 与前端端口，要求菜单 5 已跑过
  # （Caddyfile 存在且至少含一个站点块）。CADDYFILE_PATH 仅供测试夹具
  # 覆盖，默认即现行硬编码路径，菜单 5 与本函数共用同一钩子。
  local caddyfile="${CADDYFILE_PATH:-/etc/caddy/Caddyfile}"
  if [ ! -f "$caddyfile" ]; then
    echo "❌ 未找到 $caddyfile ，请先运行菜单 5「配置域名反向代理（自动 HTTPS）」后再配置订阅前置域名"
    return 1
  fi
  if ! awk '/\{[[:space:]]*$/ { line = $0; sub(/\{[[:space:]]*$/, "", line); if (line ~ /[^[:space:],]/) found = 1 } END { exit(found ? 0 : 1) }' "$caddyfile"; then
    echo "❌ $caddyfile 中未找到任何站点块，请先运行菜单 5「配置域名反向代理（自动 HTTPS）」"
    return 1
  fi
  # 幂等：输入域名已是任一站点块的地址（含面板域名本身、带 scheme/端口
  # 的写法）时拒绝重复追加。
  if awk -v d="$SUB_DOMAIN" '/\{[[:space:]]*$/ { line = $0; sub(/\{[[:space:]]*$/, "", line); n = split(line, parts, /[[:space:],]+/); for (i = 1; i <= n; i++) { tok = parts[i]; sub(/^https?:\/\//, "", tok); sub(/:[0-9]+$/, "", tok); if (tok == d) found = 1 } } END { exit(found ? 0 : 1) }' "$caddyfile"; then
    echo "❌ 域名 $SUB_DOMAIN 已存在于 $caddyfile 的站点块中，请勿重复配置"
    return 1
  fi

  # 前端端口解析与菜单 5 完全一致（SCR-003）：独立流程不经过
  # get_config_params，从部署目录 .env 读取实际前端端口，找不到回退 6366。
  local _env_file _port
  if [[ -z "${FRONTEND_PORT:-}" ]]; then
    for _env_file in "${PANEL_DEPLOY_DIR:-}/.env" "./.env" "/opt/flmx-panel/.env" "/opt/flvx-panel/.env"; do
      [[ "$_env_file" == "/.env" ]] && continue
      _port=$(get_env_var "FRONTEND_PORT" "$_env_file" 2>/dev/null | tr -d ' \r\n\t"' | tr -d "'" || true)
      if [[ "$_port" =~ ^[0-9]+$ ]] && (( 10#$_port >= 1 && 10#$_port <= 65535 )); then
        FRONTEND_PORT="$_port"
        echo "📁 从 $_env_file 读取前端端口：$FRONTEND_PORT"
        break
      fi
    done
  fi

  # 写前备份（命名沿用菜单 5），追加站点块而非整文件覆盖，原有站点
  # 块（面板域名等）逐字保留。
  echo "📝 配置 Caddy..."
  local caddyfile_bak="${caddyfile}.bak.$(date +%s)"
  cp "$caddyfile" "$caddyfile_bak"
  echo "🗄️  已备份现有配置到 $caddyfile_bak"
  cat >> "$caddyfile" <<EOF
$SUB_DOMAIN {
    reverse_proxy 127.0.0.1:${FRONTEND_PORT:-6366}
}
EOF

  # SCR-020: 先校验后生效。validate 不过即回滚备份并退出，此时尚未
  # restart，正在运行的 Caddy 仍用旧配置，不受影响。caddy 不在 PATH
  # 时跳过校验，靠下方 restart + is-active 兜底（同菜单 5）。
  if command -v caddy &> /dev/null; then
    if ! caddy validate --config "$caddyfile" > /dev/null 2>&1; then
      echo "❌ Caddy 配置校验失败，已回滚到 $caddyfile_bak ，未做任何生效变更"
      cp "$caddyfile_bak" "$caddyfile"
      return 1
    fi
    echo "✅ Caddy 配置校验通过"
  fi

  # R4B11-002: restart 失败不得被 set -e 在下方 is-active 检查之前吞掉
  # （原实现的日志指引分支在 restart 返回非 0 时永不可达）。用 if 守卫
  # 接住返回码，失败即回滚到追加前的备份、尝试以旧配置恢复服务，并
  # 落到日志指引分支——与上方 validate 失败回滚同口径。
  if ! systemctl restart caddy; then
    echo "❌ Caddy 重启失败，正在回滚到之前的配置..."
    cp "$caddyfile_bak" "$caddyfile"
    systemctl restart caddy || true
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
  sleep 3

  if systemctl is-active --quiet caddy; then
    echo ""
    echo "🎉 订阅前置域名配置完成"
    echo "🌐 前置地址: https://$SUB_DOMAIN"
    echo "📜 HTTPS 证书由 Caddy 自动申请和续期"
    echo ""
    echo "⚠️  后续步骤（缺一不可）："
    echo "   1）在 Cloudflare 为 $SUB_DOMAIN 添加 A 记录，指向本机公网 IP（与面板域名相同），"
    echo "      先保持灰云（仅 DNS），待 Caddy 完成证书签发后再开启橙云（代理）。"
    echo "   2）在 Cloudflare 为路径 /api/v1/open_api/sub/* 创建 Cache Rule 并设置为绕过缓存，"
    echo "      避免订阅内容被 CDN 缓存。"
    echo "   3）在面板设置「订阅前置地址」中填入 https://$SUB_DOMAIN ，"
    echo "      之后订阅链接即可按该前置地址复制给用户。"
  else
    echo "❌ Caddy 启动失败，请检查日志: journalctl -u caddy -n 50"
    return 1
  fi
}

# 显示菜单
show_menu() {
  echo "==============================================="
  echo "          面板管理脚本"
  echo "==============================================="
  echo "请选择操作："
  echo "1. 安装面板"
  echo "2. 更新面板"
  echo "3. 卸载面板"
  echo "4. 迁移到 PostgreSQL"
  echo "5. 配置域名反向代理（自动 HTTPS）"
  echo "6. 配置订阅前置域名（自动 HTTPS）"
  echo "7. 退出"
  echo "==============================================="
}

generate_random() {
  # F-120: 16 字符（约 95 bit）对 JWT_SECRET/PG 口令余量偏薄，提升到 32
  # 字符（约 190 bit）。字符集不变（URL 安全，进 DATABASE_URL 无需转义）。
  LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c32
}

upsert_env_var() {
  local file="$1"
  local key="$2"
  local value="$3"
  local tmp_file

  tmp_file=$(mktemp)
  if [ -f "$file" ]; then
    awk -v k="$key" -v v="$value" '
      BEGIN { found=0 }
      $0 ~ ("^" k "=") { print k "=" v; found=1; next }
      { print }
      END { if (!found) print k "=" v }
    ' "$file" > "$tmp_file"
  else
    printf '%s=%s\n' "$key" "$value" > "$tmp_file"
  fi

  mv "$tmp_file" "$file"
}

get_env_var() {
  local key="$1"
  local file="${2:-.env}"

  if [[ ! -f "$file" ]]; then
    return 0
  fi

  grep -m1 "^${key}=" "$file" | cut -d= -f2-
}

get_current_db_type() {
  local db_type database_url

  db_type=$(get_env_var "DB_TYPE" || true)
  database_url=$(get_env_var "DATABASE_URL" || true)

  if [[ "$db_type" == "sqlite" ]]; then
    echo "sqlite"
  elif [[ "$db_type" == "postgres" || "$database_url" == postgres://* || "$database_url" == postgresql://* ]]; then
    echo "postgres"
  else
    echo "sqlite"
  fi
}

get_container_compose_label() {
  local container="$1"
  local label="$2"
  local value

  value=$(docker inspect -f "{{ index .Config.Labels \"$label\" }}" "$container" 2>/dev/null || true)
  if [[ "$value" == "<no value>" ]]; then
    value=""
  fi
  printf '%s' "$value"
}

resolve_panel_deployment() {
  local backend_container="${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}"
  local requested_dir="${PANEL_DEPLOY_DIR:-}"
  local label_dir label_project deploy_dir project_name

  label_dir=$(get_container_compose_label "$backend_container" "com.docker.compose.project.working_dir")
  label_project=$(get_container_compose_label "$backend_container" "com.docker.compose.project")

  # Older in-panel upgrades ran Compose inside a helper container, which left
  # the label pointing at the container path (/opt/flmx-panel) instead of the
  # real deployment. Prefer the current directory when only it has the files.
  if [[ -n "$label_dir" && ! -f "$label_dir/docker-compose.yml" && -f ".env" && -f "docker-compose.yml" ]]; then
    echo "⚠️ 容器标签记录的部署目录 $label_dir 中没有 docker-compose.yml，改用当前目录 $(pwd -P)"
    label_dir=""
  fi

  if [[ -n "$requested_dir" ]]; then
    deploy_dir="$requested_dir"
  elif [[ -n "$label_dir" ]]; then
    deploy_dir="$label_dir"
  elif [[ -f ".env" && -f "docker-compose.yml" ]]; then
    deploy_dir=$(pwd -P)
  else
    echo "❌ 无法识别面板部署目录：未找到容器 Compose 标签，当前目录也没有完整部署配置"
    return 1
  fi

  if [[ ! -d "$deploy_dir" ]]; then
    echo "❌ 面板部署目录不存在：$deploy_dir"
    return 1
  fi
  deploy_dir=$(cd "$deploy_dir" && pwd -P)
  if [[ -n "$label_dir" && -d "$label_dir" ]]; then
    label_dir=$(cd "$label_dir" && pwd -P)
  fi
  if [[ -n "$requested_dir" && -n "$label_dir" && "$deploy_dir" != "$label_dir" ]]; then
    echo "❌ 指定部署目录与运行中容器标签不一致：$deploy_dir != $label_dir"
    return 1
  fi

  project_name="${COMPOSE_PROJECT_NAME:-}"
  if [[ -z "$project_name" && -n "$label_project" ]]; then
    project_name="$label_project"
  fi
  if [[ -n "$project_name" && ! "$project_name" =~ ^[a-z0-9][a-z0-9_-]*$ ]]; then
    echo "❌ Compose 项目名不合法：$project_name"
    return 1
  fi

  PANEL_DEPLOY_DIR="$deploy_dir"
  PANEL_BACKEND_CONTAINER="$backend_container"
  PANEL_POSTGRES_CONTAINER="${PANEL_POSTGRES_CONTAINER:-$DEFAULT_PANEL_POSTGRES_CONTAINER}"
  PANEL_COMPOSE_PROJECT="$project_name"
  export PANEL_DEPLOY_DIR PANEL_BACKEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_COMPOSE_PROJECT
  if [[ -n "$project_name" ]]; then
    COMPOSE_PROJECT_NAME="$project_name"
    export COMPOSE_PROJECT_NAME
  fi

  cd "$PANEL_DEPLOY_DIR"
  echo "📁 部署目录：$PANEL_DEPLOY_DIR"
  if [[ -n "$PANEL_COMPOSE_PROJECT" ]]; then
    echo "📦 Compose 项目：$PANEL_COMPOSE_PROJECT"
  fi
}

run_panel_compose() {
  $DOCKER_CMD "$@"
}

validate_panel_update_environment() {
  local key value backend_port frontend_port configured_db_type db_type database_url postgres_password

  if [[ ! -f ".env" ]]; then
    echo "❌ 部署目录缺少 .env，更新终止"
    return 1
  fi
  if [[ ! -f "docker-compose.yml" ]]; then
    echo "❌ 部署目录缺少 docker-compose.yml，更新终止"
    return 1
  fi

  for key in JWT_SECRET BACKEND_PORT FRONTEND_PORT; do
    value=$(get_env_var "$key" || true)
    if [[ -z "${value//[[:space:]]/}" ]]; then
      echo "❌ .env 中 $key 缺失或为空，更新终止"
      return 1
    fi
  done

  backend_port=$(get_env_var "BACKEND_PORT" || true)
  frontend_port=$(get_env_var "FRONTEND_PORT" || true)
  if [[ ! "$backend_port" =~ ^[0-9]+$ ]] || (( 10#$backend_port < 1 || 10#$backend_port > 65535 )); then
    echo "❌ BACKEND_PORT 不是有效端口：$backend_port"
    return 1
  fi
  if [[ ! "$frontend_port" =~ ^[0-9]+$ ]] || (( 10#$frontend_port < 1 || 10#$frontend_port > 65535 )); then
    echo "❌ FRONTEND_PORT 不是有效端口：$frontend_port"
    return 1
  fi

  configured_db_type=$(get_env_var "DB_TYPE" || true)
  if [[ -n "$configured_db_type" && "$configured_db_type" != "sqlite" && "$configured_db_type" != "postgres" ]]; then
    echo "❌ DB_TYPE 仅支持 sqlite 或 postgres：$configured_db_type"
    return 1
  fi
  db_type=$(get_current_db_type)
  if [[ "$db_type" == "postgres" ]]; then
    database_url=$(get_env_var "DATABASE_URL" || true)
    postgres_password=$(get_env_var "POSTGRES_PASSWORD" || true)
    if [[ -z "${database_url//[[:space:]]/}" || -z "${postgres_password//[[:space:]]/}" ]]; then
      echo "❌ PostgreSQL 模式要求 DATABASE_URL 和 POSTGRES_PASSWORD 均非空"
      return 1
    fi
  fi

  if ! run_panel_compose -f docker-compose.yml config -q; then
    echo "❌ 当前 Compose 配置校验失败，更新终止"
    return 1
  fi
}

# R5-B5B D19: 安全下载（带 .sha256 校验，缺失即拒）。
# FLMX_VERIFY_DOWNLOADS=0 可跳过（过渡期），默认开启；旧名 FLVX_VERIFY_DOWNLOADS 仍兼容。
# F-138: 边角组合收紧为 fail-closed——仅显式 "0" 可关闭校验（误填
# true/yes 等不再静默跳过）；缺少 sha256sum 时拒绝（否则空 .sha256 与
# 空 actual 会 ""=="" 误判通过）；.sha256 内容必须是 64 位十六进制。
download_verified() {
  local url="$1"
  local dest="$2"
  local verify="${FLMX_VERIFY_DOWNLOADS:-${FLVX_VERIFY_DOWNLOADS:-1}}"

  # R8-J-A: 本体与下方 .sha256 均加有界重试——旧实现单次尝试，
  # 镜像瞬时故障即整次操作终止（校验腿失败还会删掉已下好的本体）。
  if ! curl -fL --retry 3 --retry-delay 2 -o "$dest" "$url"; then
    echo "❌ 下载失败: $url" >&2
    return 1
  fi

  if [ "$verify" = "0" ]; then
    echo "⚠️  跳过校验 (FLMX_VERIFY_DOWNLOADS=0): $dest" >&2
    return 0
  fi
  if [ "$verify" != "1" ]; then
    echo "⚠️  FLMX_VERIFY_DOWNLOADS 取值无法识别（\"$verify\"），按开启校验处理（仅 0 可关闭）" >&2
  fi
  if ! command -v sha256sum >/dev/null 2>&1; then
    rm -f "$dest"
    echo "❌ 缺少 sha256sum，无法校验下载完整性，拒绝安装（fail-closed）" >&2
    return 1
  fi

  # 下载 .sha256 并校验。
  local sha_url="${url}.sha256"
  local sha_file="${dest}.sha256"
  if ! curl -fL --retry 3 --retry-delay 2 -o "$sha_file" "$sha_url"; then
    rm -f "$dest" "$sha_file"
    echo "❌ 校验文件缺失，拒绝安装: $sha_url" >&2
    echo "   （设置 FLMX_VERIFY_DOWNLOADS=0 可跳过，仅用于过渡期）" >&2
    return 1
  fi

  local expected=$(awk '{print $1}' "$sha_file" | tr -d ' \n' | tr 'A-F' 'a-f')
  if ! [[ "$expected" =~ ^[0-9a-f]{64}$ ]]; then
    rm -f "$dest" "$sha_file"
    echo "❌ 校验文件内容无效（不是 64 位 SHA256），拒绝安装: $sha_url" >&2
    return 1
  fi
  local actual=$(sha256sum "$dest" | awk '{print $1}')
  rm -f "$sha_file"

  if [ "$expected" != "$actual" ]; then
    rm -f "$dest"
    echo "❌ 校验失败: $dest" >&2
    echo "   期望: $expected" >&2
    echo "   实际: $actual" >&2
    return 1
  fi

  echo "✅ 校验通过: $dest" >&2
  return 0
}

# download_verified_routed <canonical URL> <dest>：按内置路线表逐个
# 以 download_verified 下载（资产 + .sha256 同源同腿、fail-closed
# 口径不变），首个成功即用；阶段 1 路线全败时补测代理排名续切。
download_verified_routed() {
  local url="$1"
  local dest="$2"
  ensure_download_routes
  local prefix
  while :; do
    for prefix in "${DOWNLOAD_ROUTES[@]}"; do
      if download_verified "$(route_url "$prefix" "$url")" "$dest"; then
        return 0
      fi
    done
    extend_download_routes || break
  done
  return 1
}

download_panel_update_compose() {
  local compose_url="$1"

  UPDATE_COMPOSE_CANDIDATE=$(mktemp "$PANEL_DEPLOY_DIR/.docker-compose.yml.update.XXXXXX")
  if ! download_verified_routed "$compose_url" "$UPDATE_COMPOSE_CANDIDATE"; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    echo "❌ 下载最新 Compose 配置失败，更新终止"
    return 1
  fi
  if [[ ! -s "$UPDATE_COMPOSE_CANDIDATE" ]]; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    echo "❌ 下载的 Compose 配置为空，更新终止"
    return 1
  fi
}

validate_panel_update_compose() {
  if ! FLUX_VERSION="$LATEST_VERSION" run_panel_compose -f "$UPDATE_COMPOSE_CANDIDATE" config -q; then
    echo "❌ 新 Compose 配置校验失败，更新终止"
    return 1
  fi
}

backup_sqlite_for_update() (
  local destination="$1"
  local running paused="false"

  cleanup_paused_backend() {
    if [[ "$paused" == "true" ]]; then
      docker unpause "$PANEL_BACKEND_CONTAINER" >/dev/null 2>&1 || true
    fi
  }
  trap cleanup_paused_backend EXIT

  running=$(docker inspect -f '{{.State.Running}}' "$PANEL_BACKEND_CONTAINER" 2>/dev/null || true)
  if [[ "$running" == "true" ]]; then
    if ! docker pause "$PANEL_BACKEND_CONTAINER" >/dev/null; then
      echo "❌ 暂停后端以创建一致性 SQLite 备份失败"
      return 1
    fi
    paused="true"
  fi

  mkdir -p "$destination"
  if ! docker cp "$PANEL_BACKEND_CONTAINER:/app/data/." "$destination"; then
    echo "❌ SQLite 数据备份失败"
    return 1
  fi

  if [[ "$paused" == "true" ]] && ! docker unpause "$PANEL_BACKEND_CONTAINER" >/dev/null; then
    echo "❌ SQLite 备份完成，但恢复后端运行失败"
    return 1
  fi
  paused="false"
)

backup_postgres_for_update() {
  local destination="$1"
  local postgres_db postgres_user

  postgres_db=$(get_env_var "POSTGRES_DB" || true)
  postgres_user=$(get_env_var "POSTGRES_USER" || true)
  postgres_db=${postgres_db:-flux_panel}
  postgres_user=${postgres_user:-flux_panel}

  if ! docker exec "$PANEL_POSTGRES_CONTAINER" pg_dump -U "$postgres_user" "$postgres_db" > "$destination"; then
    rm -f "$destination"
    echo "❌ PostgreSQL 数据备份失败"
    return 1
  fi
}

create_panel_update_backup() {
  local db_type="$1"
  local timestamp

  timestamp=$(date '+%Y%m%d-%H%M%S')
  if ! (umask 077 && mkdir -p "$PANEL_DEPLOY_DIR/backups"); then
    echo "❌ 创建更新备份目录失败"
    return 1
  fi
  UPDATE_BACKUP_DIR=$(umask 077 && mktemp -d "$PANEL_DEPLOY_DIR/backups/panel-update-$timestamp.XXXXXX") || {
    echo "❌ 创建更新备份目录失败"
    return 1
  }
  if ! cp -p .env docker-compose.yml "$UPDATE_BACKUP_DIR/"; then
    echo "❌ 备份面板配置失败"
    return 1
  fi

  if [[ "$db_type" == "postgres" ]]; then
    backup_postgres_for_update "$UPDATE_BACKUP_DIR/postgres.sql" || return 1
  else
    backup_sqlite_for_update "$UPDATE_BACKUP_DIR/sqlite" || return 1
  fi
  echo "💾 更新备份：$UPDATE_BACKUP_DIR"
}

pull_panel_update_images() {
  local db_type="$1"

  if [[ "$db_type" == "postgres" ]]; then
    FLUX_VERSION="$LATEST_VERSION" run_panel_compose -f "$UPDATE_COMPOSE_CANDIDATE" pull backend frontend postgres
  else
    FLUX_VERSION="$LATEST_VERSION" run_panel_compose -f "$UPDATE_COMPOSE_CANDIDATE" pull backend frontend
  fi
}

# FIX2-C #1（V6-01）：升级替换 compose 前，把存量 compose 的网络名
# 与子网搬进候选文件。FIX-G 已把模板子网改为 172.30.0.0/16，但那
# 只应作用于全新安装；整份覆写会连在用子网一起换掉，等于升级即
# 重建面板网络（容器 IP 全变、可信代理判定随子网是否 RFC1918 静默
# 翻转）。规则与面板内升级（preserveComposeNetworks）一致：存量有
# 子网就整体搬入；存量无显式子网则删掉候选的子网行、不强加模板
# 子网；存量无 networks 段则不动候选。best-effort：任何一步失败都
# 不阻断升级本身（行为不劣于此前的整份覆写）。
# 用法：preserve_compose_networks <candidate> <current>
preserve_compose_networks() {
  local candidate="$1" current="$2"
  [[ -f "$candidate" && -f "$current" ]] || return 0
  grep -q "^networks:" "$current" || return 0

  # networks 是模板的最后一个顶层段；段内抽网络名与全部子网。
  local old_name old_subnets
  old_name=$(awk '
    /^networks:/ { in_net=1; next }
    in_net && /^[^[:space:]#]/ { in_net=0 }
    in_net && /^    name:/ { sub(/^    name:[[:space:]]*/, ""); print; exit }
  ' "$current")
  old_subnets=$(awk '
    /^networks:/ { in_net=1; next }
    in_net && /^[^[:space:]#]/ { in_net=0 }
    in_net && /- subnet:/ { sub(/.*- subnet:[[:space:]]*/, ""); print }
  ' "$current")

  local tmp
  tmp=$(mktemp) || return 0
  if ! OLD_NAME="$old_name" OLD_SUBNETS="$old_subnets" awk '
    BEGIN {
      old_name = ENVIRON["OLD_NAME"]
      n = split(ENVIRON["OLD_SUBNETS"], subs, "\n")
      if (n == 1 && subs[1] == "") n = 0
      emitted = 0
    }
    /^networks:/ { in_net=1; print; next }
    in_net && /^[^[:space:]#]/ { in_net=0 }
    in_net && /^    name:/ && old_name != "" {
      print "    name: " old_name
      next
    }
    in_net && /- subnet:/ {
      if (!emitted) {
        indent = $0
        sub(/- subnet:.*/, "", indent)
        for (i = 1; i <= n; i++) print indent "- subnet: " subs[i]
        emitted = 1
      }
      next
    }
    { print }
  ' "$candidate" > "$tmp"; then
    rm -f "$tmp"
    return 0
  fi
  cat "$tmp" > "$candidate"
  rm -f "$tmp"
  if [[ -n "$old_subnets" ]]; then
    echo "🌐 已保留现有部署的网络子网（${old_subnets//$'\n'/, }），模板子网仅对全新安装生效"
  fi
}

activate_panel_update_files() {
  preserve_compose_networks "$UPDATE_COMPOSE_CANDIDATE" docker-compose.yml
  if ! chmod 0644 "$UPDATE_COMPOSE_CANDIDATE" || ! mv "$UPDATE_COMPOSE_CANDIDATE" docker-compose.yml; then
    echo "❌ 替换 Compose 配置失败"
    return 1
  fi
  UPDATE_COMPOSE_CANDIDATE=""
  if ! upsert_env_var ".env" "FLUX_VERSION" "$LATEST_VERSION"; then
    echo "❌ 更新版本配置失败"
    return 1
  fi
}

start_panel_after_update() {
  local db_type="$1"

  if [[ "$db_type" == "postgres" ]]; then
    run_panel_compose up -d postgres || return 1
    wait_for_postgres_healthy || return 1
  fi
  run_panel_compose up -d --force-recreate --remove-orphans backend frontend || return 1
  wait_for_backend_healthy
}

rollback_panel_update() {
  local db_type="$1"

  echo "↩️ 正在恢复更新前配置..."
  if ! cp -p "$UPDATE_BACKUP_DIR/.env" .env || ! cp -p "$UPDATE_BACKUP_DIR/docker-compose.yml" docker-compose.yml; then
    echo "❌ 配置回滚失败，请从 $UPDATE_BACKUP_DIR 手动恢复"
    return 1
  fi
  if [[ "$db_type" == "postgres" ]]; then
    run_panel_compose up -d postgres || return 1
    wait_for_postgres_healthy || return 1
  fi
  run_panel_compose up -d --force-recreate --remove-orphans backend frontend || return 1
  wait_for_backend_healthy
}

wait_for_postgres_healthy() {
  local pg_health
  local postgres_container="${PANEL_POSTGRES_CONTAINER:-$DEFAULT_PANEL_POSTGRES_CONTAINER}"

  echo "🔍 检查 PostgreSQL 服务状态..."
  for i in {1..90}; do
    if docker ps --format "{{.Names}}" | grep -Fxq "$postgres_container"; then
      pg_health=$(docker inspect -f '{{.State.Health.Status}}' "$postgres_container" 2>/dev/null || echo "unknown")
      if [[ "$pg_health" == "healthy" ]]; then
        echo "✅ PostgreSQL 服务健康检查通过"
        return 0
      elif [[ "$pg_health" == "unhealthy" ]]; then
        echo "⚠️ PostgreSQL 健康状态：$pg_health"
      fi
    else
      pg_health="not_running"
    fi

    if [ $i -eq 90 ]; then
      echo "❌ PostgreSQL 启动超时（90秒）"
      echo "🔍 当前状态：$(docker inspect -f '{{.State.Health.Status}}' "$postgres_container" 2>/dev/null || echo '容器不存在')"
      return 1
    fi

    if [ $((i % 15)) -eq 1 ]; then
      echo "⏳ 等待 PostgreSQL 启动... ($i/90) 状态：${pg_health:-unknown}"
    fi
    sleep 1
  done
}

wait_for_backend_healthy() {
  local backend_health
  local backend_container="${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}"

  echo "🔍 检查后端服务状态..."
  for i in {1..90}; do
    if docker ps --format "{{.Names}}" | grep -Fxq "$backend_container"; then
      backend_health=$(docker inspect -f '{{.State.Health.Status}}' "$backend_container" 2>/dev/null || echo "unknown")
      if [[ "$backend_health" == "healthy" ]]; then
        echo "✅ 后端服务健康检查通过"
        return 0
      elif [[ "$backend_health" == "unhealthy" ]]; then
        echo "⚠️ 后端健康状态：$backend_health"
      fi
    else
      backend_health="not_running"
    fi

    if [ $i -eq 90 ]; then
      echo "❌ 后端服务启动超时（90秒）"
      echo "🔍 当前状态：$(docker inspect -f '{{.State.Health.Status}}' "$backend_container" 2>/dev/null || echo '容器不存在')"
      return 1
    fi

    if [ $((i % 15)) -eq 1 ]; then
      echo "⏳ 等待后端服务启动... ($i/90) 状态：${backend_health:-unknown}"
    fi
    sleep 1
  done
}

# 删除脚本自身
delete_self() {
  # 通过 bash <(curl ...) 进程替换执行时，$0 是 /dev/fd/63，无需清理
  if [[ "$PANEL_SCRIPT_PATH" == /dev/fd/* ]]; then
    return 0
  fi
  echo ""
  echo "🗑️ 操作已完成，正在清理脚本文件..."
  sleep 1
  rm -f "$PANEL_SCRIPT_PATH" && echo "✅ 脚本文件已删除" || echo "❌ 删除脚本文件失败"
}

# 获取本机公网 IP（用于显示访问地址）
get_public_ip() {
  local ip=""
  # 依次尝试多个服务，取第一个成功的结果
  ip=$(curl -fsSL --max-time 5 "https://api.ipify.org" 2>/dev/null || true)
  if [ -z "$ip" ]; then
    ip=$(curl -fsSL --max-time 5 "https://ifconfig.me" 2>/dev/null || true)
  fi
  if [ -z "$ip" ]; then
    ip=$(curl -fsSL --max-time 5 "https://icanhazip.com" 2>/dev/null | tr -d ' \n' || true)
  fi
  # 兜底：取本机第一块非回环网卡的 IPv4
  if [ -z "$ip" ]; then
    # AUDIT-R1 (SCR-011): 原 grep -oP 在 busybox/Alpine 下恒失败（无 -P 支持）。
    # 改用 awk 提取，POSIX 兼容。
    ip=$(ip -4 addr show scope global 2>/dev/null | awk '$1=="inet"{split($2,a,"/"); print a[1]; exit}' || true)
  fi
  echo "${ip:-服务器IP}"
}



# 获取用户输入的配置参数
get_config_params() {
  echo "🔧 请输入配置参数："

  local _pname _pval
  read -p "前端端口（默认 6366）: " FRONTEND_PORT
  FRONTEND_PORT=${FRONTEND_PORT:-6366}

  read -p "后端端口（默认 6365）: " BACKEND_PORT
  BACKEND_PORT=${BACKEND_PORT:-6365}

  # AUDIT-R1 (SCR-008): 安装时即校验端口（与更新流程 validate_panel_update_environment
  # 一致），避免非法值写入 .env 后在 docker compose up 时才暴露，报错远离输入点。
  for _pname in FRONTEND_PORT BACKEND_PORT; do
    _pval="${!_pname}"
    # AUDIT-R1 (R5R1-001): 算术比较前加 10# 强制十进制。前导零输入（如 080）
    # 否则被 bash 当八进制解析，stderr 报错但仍被接受，体验差。
    if [[ ! "$_pval" =~ ^[0-9]+$ ]] || (( 10#$_pval < 1 || 10#$_pval > 65535 )); then
      echo "❌ $_pname 不是有效端口（1-65535）：$_pval"
      return 1
    fi
  done

  echo "请选择数据库类型："
  echo "1. SQLite（默认）"
  echo "2. PostgreSQL"
  read -p "数据库类型（1/2，默认 1）: " DB_CHOICE
  case "$DB_CHOICE" in
    2)
      DB_TYPE="postgres"
      ;;
    ""|1)
      DB_TYPE="sqlite"
      ;;
    *)
      echo "⚠️ 输入无效，默认使用 SQLite"
      DB_TYPE="sqlite"
      ;;
  esac

  POSTGRES_DB="flux_panel"
  POSTGRES_USER="flux_panel"
  POSTGRES_PASSWORD=$(generate_random)

  if [[ "$DB_TYPE" == "postgres" ]]; then
    DATABASE_URL="postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=disable"
  else
    DATABASE_URL=""
  fi

  # 生成JWT密钥
  JWT_SECRET=$(generate_random)
}

# 安装功能
install_panel() {
  echo "🚀 开始安装面板..."

  # F-049: 已有部署守卫。安装路径会重生成 POSTGRES_PASSWORD/JWT_SECRET
  # 并无条件覆写 .env 与 docker-compose.yml；而 PG 数据卷里的口令只在
  # 首次初始化时生效——在已部署机器上误走安装，.env 新口令与旧卷脱钩，
  # 后端必然以 28P01 连不上库。检测到同目录已有部署时默认拒绝并引导
  # 去更新；确需重装须显式 PANEL_FORCE_REINSTALL=1，且先备份原 .env。
  if [[ -f .env && -f docker-compose.yml ]]; then
    if [[ "${PANEL_FORCE_REINSTALL:-}" != "1" ]]; then
      echo "❌ 检测到当前目录已有面板部署（.env 与 docker-compose.yml 同时存在）"
      echo "   如需升级请使用菜单「2. 更新面板」；继续安装将覆写 .env 并轮换密钥，"
      echo "   PostgreSQL 部署会因口令与数据卷脱钩而无法启动。"
      echo "   确需重装请先自行备份 .env，或设置 PANEL_FORCE_REINSTALL=1 后重试。"
      return 1
    fi
    local env_bak=".env.bak.$(date +%Y%m%d%H%M%S)"
    cp .env "$env_bak"
    echo "⚠️  PANEL_FORCE_REINSTALL=1：已备份原 .env 到 $env_bak，继续重装"
  fi

  ensure_compose_urls_initialized || return 1

  check_docker
  get_config_params

  echo "🔽 下载必要文件..."
  # F-052: 选版时机后移——主机有 IPv6 地址不等于 Docker 能用 IPv6。
  # 先配置并验证 Docker IPv6（configure_docker_ipv6 写后校验+重启检查、
  # docker_ipv6_usable 查 daemon 与 bridge），确认可用才选 v6 版；
  # 任一步不通过都明确降级 v4 版并告知，不再让安装死在建网一步、
  # 报错还远离根因。
  local use_ipv6=0
  if check_ipv6_support; then
    echo "🚀 系统支持 IPv6，尝试启用并验证 Docker IPv6..."
    if configure_docker_ipv6 && docker_ipv6_usable; then
      use_ipv6=1
      echo "✅ Docker IPv6 验证可用，使用 IPv6 版 Compose"
    else
      echo "⚠️ Docker IPv6 配置/验证未通过，降级使用 IPv4 版 Compose（面板功能不受影响）"
    fi
  fi
  if [[ "$use_ipv6" == "1" ]]; then
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV6_URL"
  else
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV4_URL"
  fi
  echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
  # R8-J-A 附带清理：不再靠 set -e 隐式中止，显式判返回值并给出
  # 路径信息，与更新路径（download_panel_update_compose）的口径对齐。
  if ! download_verified_routed "$DOCKER_COMPOSE_URL" docker-compose.yml; then
    echo "❌ 下载 Compose 配置失败，安装终止: $DOCKER_COMPOSE_URL"
    return 1
  fi
  echo "✅ 文件准备完成"

  cat > .env <<EOF
JWT_SECRET=$JWT_SECRET
FRONTEND_PORT=$FRONTEND_PORT
BACKEND_PORT=$BACKEND_PORT
FLUX_VERSION=$RESOLVED_VERSION

DB_TYPE=$DB_TYPE
DATABASE_URL=$DATABASE_URL

POSTGRES_DB=$POSTGRES_DB
POSTGRES_USER=$POSTGRES_USER
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
EOF
  chmod 600 .env # M-01: .env 含 JWT_SECRET/数据库密码，禁止全局可读

  echo "🚀 启动 docker 服务..."
  if [[ "$DB_TYPE" == "postgres" ]]; then
    $DOCKER_CMD up -d postgres
    wait_for_postgres_healthy
    $DOCKER_CMD up -d backend frontend
  else
    $DOCKER_CMD up -d backend frontend
  fi

  # R8-J-C: 完成块之前先过既有后端健康等待——更新（start_panel_
  # after_update）与迁移路径均有此把关，安装路径此前独缺，up -d 后
  # 直达成功块：后端 crash-loop 时用户拿到的是打不开的访问地址与
  # 默认凭据、且无日志指引。失败时不印成功块，改印失败 + logs
  # 指引（与迁移路径的处置风格一致）。
  if ! wait_for_backend_healthy; then
    echo "❌ 部署未完成：后端服务未通过健康检查，已跳过成功信息与默认凭据打印。"
    echo "🔍 排查指引：在部署目录执行 $DOCKER_CMD logs backend 查看后端日志；修复后重新运行本脚本、选择「2. 更新面板」即可完成启动。"
    return 1
  fi

  echo "🎉 部署完成"
  local _pub_ip
  _pub_ip=$(get_public_ip)
  echo "🌐 访问地址: http://${_pub_ip}:$FRONTEND_PORT"
  echo "📖 部署完成后请阅读下使用文档，求求了啊，不要上去就是一顿操作"
  echo "📚 文档地址: https://iiiiiiiii7125-art.github.io/flmx-max/"
  echo "💡 默认管理员账号: admin@user / admin@user"
  echo "⚠️  登录后请立即修改默认密码！"


}

# 更新功能
# FIX2-C #9（V6 P3）：读取实际运行中的后端容器镜像 tag 作为部署
# 版本。.env 的 FLUX_VERSION 是「打算跑的版本」，可能与正在跑的
# 镜像不一致（手改 .env、升级中途失败）；防降级判定若只信 .env
# 会被这种不一致绕过。取不到（容器不在、无 tag 等）时返回非 0，
# 调用方回退 .env 口径。
get_deployed_panel_version() {
  local container="${PANEL_BACKEND_CONTAINER:-$DEFAULT_PANEL_BACKEND_CONTAINER}"
  local image tag
  image=$(docker inspect -f '{{.Config.Image}}' "$container" 2>/dev/null) || return 1
  [[ -n "$image" ]] || return 1
  image="${image%%@*}"   # 去 digest
  tag="${image##*:}"     # 去 registry/repo 前缀，只留 tag
  # 无 tag（tag 与整串相同）、或误把注册表端口当 tag（含 /）都算取不到。
  if [[ -z "$tag" || "$tag" == "$image" || "$tag" == */* ]]; then
    return 1
  fi
  printf '%s' "$tag"
}

update_panel() {
  echo "🔄 开始更新面板..."
  check_docker noinstall || {
    echo "❌ 未检测到 Docker，本机没有面板部署，无法更新"
    return 1
  }
  resolve_panel_deployment || return 1
  validate_panel_update_environment || return 1

  CURRENT_DB_TYPE=$(get_current_db_type)
  echo "🗄️ 当前数据库类型：$CURRENT_DB_TYPE"

  # A11-132-03: 目标版本感知 + 防降级。旧实现恒取 latest 且不看当前版本：
  # ① VERSION 定版意图被静默忽略、直升 latest；② 本机版本高于 latest 时
  # 构成静默降级覆写。现与面板内升级（system_upgrade）口径对齐：
  # VERSION 非空时以其为目标；写入前与 .env 的 FLUX_VERSION 比较，
  # 两侧都可解析且目标更低时拒绝；同版本按重装继续（与面板口径一致）。
  local current_version cmp_result
  current_version=$(get_env_var "FLUX_VERSION" || true)
  # FIX2-C #9：与实际运行版本交叉核对——两者不一致时告警并以实际
  # 运行版本为准做防降级判定（.env 可能被手改或停留在失败的升级
  # 目标上，只信它会放过真实降级）。取不到实际版本时保持 .env 口径。
  local deployed_version
  deployed_version=$(get_deployed_panel_version || true)
  if [[ -n "$deployed_version" && "$deployed_version" != "$current_version" ]]; then
    echo "⚠️ .env 记录版本（${current_version:-未知}）与实际运行版本（$deployed_version）不一致，防降级判定以实际运行版本为准"
    current_version="$deployed_version"
  fi
  if [[ -n "${VERSION:-}" ]]; then
    LATEST_VERSION="$VERSION"
    echo "📌 指定更新版本：$LATEST_VERSION（当前版本：${current_version:-未知}）"
  else
    echo "🔍 获取最新版本号..."
    LATEST_VERSION=$(resolve_latest_release_tag) || {
      echo "❌ 无法获取最新版本号，更新终止"
      return 1
    }
    echo "🆕 最新版本：$LATEST_VERSION（当前版本：${current_version:-未知}）"
  fi
  cmp_result=$(panel_version_cmp "$LATEST_VERSION" "$current_version" || true)
  if [[ "$cmp_result" == "-1" ]]; then
    echo "❌ 目标版本 $LATEST_VERSION 低于当前版本 $current_version，拒绝降级更新。如确需降级请手动处理。"
    return 1
  fi
  if [[ "$cmp_result" == "0" ]]; then
    echo "ℹ️ 目标版本与当前版本相同，将按同版本重装继续"
  fi
  set_compose_urls_by_version "$LATEST_VERSION"

  echo "🔽 下载最新配置文件..."
  # F-052 变体：更新不随主机 IPv6 现状擅自切换 compose 版本——已有部署
  # 的网络是按原版本创建的，v4→v6（或反之）切换会重建网络。现有
  # compose 带 v6 子网标记就继续 v6、否则继续 v4；无现有文件时才按
  # 主机能力选版。
  if [[ -f "$PANEL_DEPLOY_DIR/docker-compose.yml" ]] && grep -q "fd00:dead:beef" "$PANEL_DEPLOY_DIR/docker-compose.yml"; then
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV6_URL"
  elif [[ -f "$PANEL_DEPLOY_DIR/docker-compose.yml" ]]; then
    DOCKER_COMPOSE_URL="$DOCKER_COMPOSEV4_URL"
  else
    DOCKER_COMPOSE_URL=$(get_docker_compose_url)
  fi
  echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
  download_panel_update_compose "$DOCKER_COMPOSE_URL" || return 1
  if ! validate_panel_update_compose; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    return 1
  fi

  echo "💾 备份当前配置和数据库..."
  if ! create_panel_update_backup "$CURRENT_DB_TYPE"; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    return 1
  fi

  echo "⬇️ 拉取最新镜像..."
  if ! pull_panel_update_images "$CURRENT_DB_TYPE"; then
    rm -f "$UPDATE_COMPOSE_CANDIDATE"
    UPDATE_COMPOSE_CANDIDATE=""
    echo "❌ 镜像拉取失败，现有服务保持运行"
    return 1
  fi

  if ! activate_panel_update_files; then
    rollback_panel_update "$CURRENT_DB_TYPE" || true
    return 1
  fi

  echo "🚀 原地重建更新后的服务..."
  if ! start_panel_after_update "$CURRENT_DB_TYPE"; then
    echo "🛑 新版本启动失败"
    if rollback_panel_update "$CURRENT_DB_TYPE"; then
      echo "✅ 已恢复更新前版本"
    else
      echo "❌ 自动回滚失败，请从 $UPDATE_BACKUP_DIR 手动恢复"
    fi
    return 1
  fi

  echo "✅ 更新完成"
}


migrate_to_postgres() {
  local current_db_type postgres_db postgres_user postgres_password database_url
  local env_snapshot pgloader_source

  echo "🔄 开始迁移 SQLite -> PostgreSQL..."
  check_docker noinstall || {
    echo "❌ 未检测到 Docker，本机没有面板部署，无法迁移"
    return 1
  }

  if [[ ! -f ".env" ]]; then
    echo "❌ 未找到 .env 文件，请先安装面板"
    return 1
  fi

  if [[ ! -f "docker-compose.yml" ]]; then
    echo "⚠️ 未找到 docker-compose.yml 文件，正在下载..."
    ensure_compose_urls_initialized || return 1
    DOCKER_COMPOSE_URL=$(get_docker_compose_url)
    echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
    # R8-J-A 附带清理：显式判返回值（原靠 set -e 隐式中止，无路径信息）。
    if ! download_verified_routed "$DOCKER_COMPOSE_URL" docker-compose.yml; then
      echo "❌ 下载 Compose 配置失败，迁移终止: $DOCKER_COMPOSE_URL"
      return 1
    fi
    echo "✅ docker-compose.yml 下载完成"
  fi

  current_db_type=$(get_current_db_type)
  if [[ "$current_db_type" == "postgres" ]]; then
    echo "ℹ️ 当前已使用 PostgreSQL，无需迁移"
    return 0
  fi

  postgres_db=$(get_env_var "POSTGRES_DB")
  postgres_user=$(get_env_var "POSTGRES_USER")
  postgres_password=$(get_env_var "POSTGRES_PASSWORD")

  postgres_db=${postgres_db:-flux_panel}
  postgres_user=${postgres_user:-flux_panel}
  postgres_password=${postgres_password:-$(generate_random)}

  # A11-132-04: 口令前置校验。后端对空/默认口令拒绝启动（1.2.0 起），
  # 旧实现把默认口令原样保留并一路写进 .env，pgloader 成功后面板却
  # 起不来且配置已切到 PG（见下方的失败回滚）。这里在停服务之前处理：
  # 口令为空或仍是已知默认值/弱口令时，当场生成随机口令并写回 .env。
  if [[ -z "$postgres_password" || "$postgres_password" == "flux_panel_change_me" || "$postgres_password" == "postgres" ]]; then
    postgres_password=$(generate_random)
    echo "⚠️ POSTGRES_PASSWORD 为空或仍是默认值，1.2.0 起后端会拒绝启动；已生成随机口令并写入 .env，迁移后请勿改回默认值"
  fi

  # A11-132-04: 迁移前快照 .env，后续任一步失败时用它回滚配置，
  # 避免面板被留在「.env 已切 postgres 但服务未起来」的坏配置上。
  env_snapshot="$(pwd)/.env.pre-postgres-migration"
  cp ".env" "$env_snapshot"

  upsert_env_var ".env" "POSTGRES_DB" "$postgres_db"
  upsert_env_var ".env" "POSTGRES_USER" "$postgres_user"
  upsert_env_var ".env" "POSTGRES_PASSWORD" "$postgres_password"

  echo "🛑 停止当前服务..."
  docker stop -t 30 flux-panel-backend 2>/dev/null || true
  docker stop -t 10 vite-frontend 2>/dev/null || true
  echo "⏳ 等待数据同步..."
  sleep 5
  $DOCKER_CMD down

  echo "💾 备份 SQLite 数据到当前目录..."
  if ! docker run --rm -v sqlite_data:/data -v "$(pwd)":/backup alpine sh -c "cp /data/gost.db /backup/gost.db.bak"; then
    echo "❌ SQLite 备份失败，迁移终止，正在恢复原服务..."
    $DOCKER_CMD up -d backend frontend || true
    return 1
  fi

  echo "🚀 启动 PostgreSQL..."
  $DOCKER_CMD up -d postgres
  if ! wait_for_postgres_healthy; then
    echo "🛑 PostgreSQL 未就绪，迁移终止，正在恢复原服务..."
    $DOCKER_CMD up -d backend frontend || true
    return 1
  fi

  # R4-P1 变通：1.3.2 及更早版本的 SQLite 库里，peer_share_resource 的
  # release_legacy_family 列是 numeric + DEFAULT false；pgloader 把
  # 默认值翻成 'false' 文本，PG 拒绝建表，迁移在建表阶段整体 FATAL。
  # 先复制一份库、把该列的模式默认值改成 0 再迁移（与模型侧修复互为
  # 双保险；模型已修复的版本里该文本不存在，替换不命中、照常迁移）。
  pgloader_source="/sqlite/gost.db"
  echo "🧹 预处理 SQLite 副本（release_legacy_family 默认值兼容）..."
  if docker run --rm -v sqlite_data:/sqlite alpine sh -c '
    set -e
    apk add --no-cache sqlite >/dev/null 2>&1
    sqlite3 /sqlite/gost.db .dump > /tmp/dump.sql
    sed "/CREATE TABLE.*peer_share_resource/,/);/ s/DEFAULT false/DEFAULT 0/g" /tmp/dump.sql | sed "/CREATE TABLE.*peer_share_resource/,/);/ s/default false/default 0/g" > /tmp/dump-fixed.sql
    rm -f /sqlite/gost-migrate.db
    sqlite3 /sqlite/gost-migrate.db < /tmp/dump-fixed.sql
    case "$(sqlite3 /sqlite/gost-migrate.db ".schema peer_share_resource")" in
      *"DEFAULT false"*|*"default false"*) exit 4 ;;
    esac
  '; then
    pgloader_source="/sqlite/gost-migrate.db"
    echo "✅ 迁移副本已生成：$pgloader_source"
  else
    echo "⚠️ 副本预处理未完成（例如容器内无法安装 sqlite），改用原始库迁移；若 pgloader 报 numeric \"false\" 错误，请按 doc/guide/postgresql.md「迁移被 release_legacy_family 堵住」一节手动处理"
  fi

  echo "🔄 执行 pgloader 迁移..."
  # R4B11-006: 目标连接串含 PG 口令，旧实现直接拼进 docker run 的进程
  # 参数，迁移持续的数分钟内可被本机 ps / docker inspect 读到明文。
  # 改走 pgloader 命令文件：连接串只落一张 0600 临时文件、只读挂载进
  # 容器，docker run 参数里只剩文件路径；无论成败用后立即删除。
  local pgloader_load_file pgloader_rc=0
  pgloader_load_file=$(mktemp)
  chmod 600 "$pgloader_load_file"
  # F-R4-01: INTO 行尾的分号是 pgloader 命令文件的语句终止符，不可省——
  # 缺失时现行 dimitri/pgloader:latest（3.6.7~devel）解析直接 KABOOM
  # （ESRAP-PARSE-ERROR），整个迁移不可用（真机三变体判别实验已证）。
  cat > "$pgloader_load_file" <<EOF
LOAD DATABASE
     FROM sqlite://${pgloader_source}
     INTO postgresql://${postgres_user}:${postgres_password}@postgres:5432/${postgres_db};
EOF
  docker run --rm --network gost-network -v sqlite_data:/sqlite -v "$pgloader_load_file":/tmp/flmx-migrate.load:ro dimitri/pgloader:latest pgloader /tmp/flmx-migrate.load || pgloader_rc=$?
  rm -f "$pgloader_load_file"
  if [[ $pgloader_rc -ne 0 ]]; then
    echo "❌ pgloader 迁移失败（如报 28P01，可执行 docker volume rm postgres_data 后重试）"
    echo "↩️ .env 尚未切换，正在恢复迁移前的 SQLite 服务..."
    $DOCKER_CMD up -d backend frontend || true
    return 1
  fi

  database_url="postgresql://${postgres_user}:${postgres_password}@postgres:5432/${postgres_db}?sslmode=disable"
  upsert_env_var ".env" "DB_TYPE" "postgres"
  upsert_env_var ".env" "DATABASE_URL" "$database_url"

  echo "🚀 启动迁移后的服务..."
  $DOCKER_CMD up -d postgres backend frontend

  echo "⏳ 等待服务启动..."
  if ! wait_for_backend_healthy; then
    # A11-132-04: 启动失败自动回滚 .env 到迁移前快照并恢复 SQLite 服务，
    # 不把面板留在「配置已切 PG、服务起不来」的坏状态。
    echo "🛑 迁移后服务启动失败，正在回滚 .env 并恢复迁移前的服务..."
    cp "$env_snapshot" ".env"
    $DOCKER_CMD up -d backend frontend || true
    if wait_for_backend_healthy; then
      echo "✅ 已回滚：面板恢复为迁移前的 SQLite 部署（数据未动）。请排查 PostgreSQL 侧问题后重试迁移"
    else
      echo "❌ 回滚后服务仍未恢复，请人工检查：.env 已恢复为 $env_snapshot 的内容，SQLite 数据在 sqlite_data 卷中未动"
    fi
    return 1
  fi

  rm -f "$env_snapshot"
  docker run --rm -v sqlite_data:/sqlite alpine rm -f /sqlite/gost-migrate.db 2>/dev/null || true
  echo "✅ SQLite -> PostgreSQL 迁移完成"
}



# 卸载功能
uninstall_panel() {
  echo "🗑️ 开始卸载面板..."
  if ! check_docker noinstall; then
    # 没有 Docker 就不可能有容器化的面板部署：只清理本地残留配置文件，
    # 不为卸载反向安装 Docker，也不下载 compose 对空项目执行 down。
    echo "未检测到 Docker，本机没有容器化的面板部署，无需卸载。"
    echo "🧹 清理残留配置文件..."
    rm -f docker-compose.yml .env
    echo "✅ 清理完成"
    return 0
  fi

  if [[ ! -f "docker-compose.yml" ]]; then
    echo "⚠️ 未找到 docker-compose.yml 文件，正在下载以完成卸载..."
    ensure_compose_urls_initialized || return 1
    DOCKER_COMPOSE_URL=$(get_docker_compose_url)
    echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
    # R8-J-A 附带清理：显式判返回值（原靠 set -e 隐式中止，无路径信息）。
    if ! download_verified_routed "$DOCKER_COMPOSE_URL" docker-compose.yml; then
      echo "❌ 下载 Compose 配置失败，卸载终止: $DOCKER_COMPOSE_URL"
      return 1
    fi
    echo "✅ docker-compose.yml 下载完成"
  fi

  read -p "确认卸载面板吗？此操作将停止并删除所有容器和数据 (y/N): " confirm
  if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
    echo "❌ 取消卸载"
    return 0
  fi

  echo "🛑 停止并删除容器、镜像、卷..."
  $DOCKER_CMD down --rmi all --volumes --remove-orphans
  echo "🧹 删除配置文件..."
  rm -f docker-compose.yml .env
  echo "✅ 卸载完成"
}

# 主逻辑
main() {
  # F-006: 自更新检查放在 main 入口（自更新依赖 resolve_version /
  # 路由助手 / download_verified，均已在上文定义）。放在 main 内
  # 而非顶层还有一层考虑：测试夹具以「截断到 main 之前 + source」方式
  # 加载本脚本的函数，顶层调用会误入夹具；main 不会被夹具调用。
  if [ "${SELF_UPDATE_SKIP:-}" != "1" ]; then
    self_update_check "$@"
  fi

  # F-125: 前置 root 检查。非 root 运行会在 apt/systemctl/docker 写操作
  # 处中途失败、留下半成品（已下载的 compose、改了一半的 daemon.json），
  # 这里在任何落盘动作之前 fail-fast。
  if [[ $EUID -ne 0 ]]; then
    echo "❌ panel_install.sh 需要 root 权限运行（涉及 Docker 与系统配置写入）"
    echo "   请使用 root 用户或 sudo 重新执行"
    exit 1
  fi

  # 显示交互式菜单
  while true; do
    show_menu
    read -p "请输入选项 (1-7): " choice

    case $choice in
      1)
        install_panel
        delete_self
        exit 0
        ;;
      2)
        update_panel
        delete_self
        exit 0
        ;;
      3)
        uninstall_panel
        delete_self
        exit 0
        ;;
      4)
        migrate_to_postgres
        delete_self
        exit 0
        ;;
      5)
        setup_domain_proxy
        delete_self
        exit 0
        ;;
      6)
        setup_subscription_front_domain
        delete_self
        exit 0
        ;;
      7)
        echo "👋 退出脚本"
        delete_self
        exit 0
        ;;
      *)
        echo "❌ 无效选项，请输入 1-7"
        echo ""
        ;;
    esac
  done
}

# 执行主函数
main "$@"
