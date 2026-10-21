#!/bin/bash
set -e

# 解决 macOS 下 tr 可能出现的非法字节序列问题
export LANG=en_US.UTF-8
export LC_ALL=C



# GitHub repo used for release downloads
REPO="iiiiiiiii7125-art/flmx-max"

# 脚本版本号：每次修改脚本后递增，确保用户拉到最新版
# 自更新检查会比对仓库中的最新版本，不一致时自动重拉
SCRIPT_VERSION="20261022.01"

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
# resolve_version / maybe_proxy_url / download_verified 定义在后面。
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
  local version tmp_script url latest_version pinned_version
  version=$(resolve_version 2>/dev/null) || {
    echo "⚠️ 无法解析目标版本，跳过脚本自更新"
    return 0
  }
  tmp_script=$(mktemp) || return 0
  url=$(maybe_proxy_url "https://github.com/${REPO}/releases/download/${version}/panel_install.sh")
  if ! download_verified "$url" "$tmp_script"; then
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

# 镜像加速配置（可由面板传入或交互式询问）
PROXY_ENABLED="${PROXY_ENABLED:-}"
PROXY_URL="${PROXY_URL:-}"
DEFAULT_PANEL_BACKEND_CONTAINER="flux-panel-backend"
DEFAULT_PANEL_POSTGRES_CONTAINER="flux-panel-postgres"
PANEL_SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# 镜像加速
maybe_proxy_url() {
  local url="$1"

  if [[ "$PROXY_ENABLED" == "false" ]]; then
    echo "$url"
    return
  fi

  local proxy="${PROXY_URL:-gcode.hostcentral.cc}"

  if [[ "$proxy" == https://* || "$proxy" == http://* ]]; then
    proxy="${proxy%/}"
  else
    proxy="https://${proxy%/}"
  fi

  echo "${proxy}/${url}"
}

ask_proxy_config() {
  if [[ -n "$PROXY_ENABLED" ]]; then
    return
  fi

  if [[ -n "$PROXY_URL" ]]; then
    PROXY_ENABLED="true"
    return
  fi

  echo ""
  echo "==============================================="
  echo "           GitHub 加速配置"
  echo "==============================================="
  if ! read -r -p "是否开启 GitHub 加速? (Y/n): " proxy_choice; then
    proxy_choice=""
  fi
  case "$proxy_choice" in
    n|N)
      PROXY_ENABLED="false"
      echo "已关闭加速，将直连 GitHub"
      ;;
    *)
      PROXY_ENABLED="true"
      if ! read -r -p "加速地址 (默认 gcode.hostcentral.cc): " input_url; then
        input_url=""
      fi
      PROXY_URL="${input_url:-gcode.hostcentral.cc}"
      echo "已开启加速: $PROXY_URL"
      ;;
  esac
  echo "==============================================="
}

resolve_latest_release_tag() {
  local effective_url tag api_tag latest_url api_url

  latest_url="https://github.com/${REPO}/releases/latest"
  api_url="https://api.github.com/repos/${REPO}/releases/latest"

  effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' -L "$(maybe_proxy_url "$latest_url")" 2>/dev/null || true)
  tag="${effective_url##*/}"
  # H-24: 无 Release 时 /releases/latest 会 302 到 /releases 索引页，
  # 此时解析出的是垃圾值 "releases" 而非版本号，必须按版本格式校验。
  if [[ "$tag" =~ ^[0-9][0-9A-Za-z.\-]*$ ]]; then
    echo "$tag"
    return 0
  fi

  api_tag=$(curl -fsSL "$(maybe_proxy_url "$api_url")" 2>/dev/null | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)
  if [[ "$api_tag" =~ ^[0-9][0-9A-Za-z.\-]*$ ]]; then
    echo "$api_tag"
    return 0
  fi

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

# 根据版本号设置 compose 下载地址
set_compose_urls_by_version() {
  local version="$1"
  DOCKER_COMPOSEV4_URL=$(maybe_proxy_url "https://github.com/${REPO}/releases/download/${version}/docker-compose-v4.yml")
  DOCKER_COMPOSEV6_URL=$(maybe_proxy_url "https://github.com/${REPO}/releases/download/${version}/docker-compose-v6.yml")
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
    if curl -1sLf 'https://dl.cloudflare.com/content/v1/e2qwFJ2fRP2b2q/stable/gpg.key' -o "$caddy_key_tmp" 2>/dev/null; then
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
      curl -1sLf 'https://dl.cloudflare.com/content/v1/e2qwFJ2fRP2b2q/stable/debian.deb.txt' 2>/dev/null | tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null
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
  # AUDIT-R1 (SCR-018): caddy 手动二进制安装时 /etc/caddy 不存在，
  # 此处 cat 会 No such file or directory。先建目录（包管理器安装自带该目录，无害）。
  mkdir -p /etc/caddy
  # 备份已有配置
  [ -f /etc/caddy/Caddyfile ] && cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak.$(date +%s)"
  # AUDIT-R1 (SCR-003): 菜单 5 是独立流程，不经过 get_config_params，
  # 此处 FRONTEND_PORT 恒未设置。改为从部署目录 .env 读取用户实际安装的
  # 前端端口（更新流程的 resolve_panel_deployment 已证明该信息可得）；
  # 找不到才回退 6366。
  local _env_file _port
  if [[ -z "${FRONTEND_PORT:-}" ]]; then
    for _env_file in "${PANEL_DEPLOY_DIR:-}/.env" "./.env" "/opt/flmx-panel/.env" "/opt/flvx-panel/.env"; do
      [[ "$_env_file" == "/.env" ]] && continue
      _port=$(get_env_var "FRONTEND_PORT" "$_env_file" 2>/dev/null | tr -d ' \r\n\t"' | tr -d "'" || true)
      if [[ "$_port" =~ ^[0-9]+$ ]] && (( _port >= 1 && _port <= 65535 )); then
        FRONTEND_PORT="$_port"
        echo "📁 从 $_env_file 读取前端端口：$FRONTEND_PORT"
        break
      fi
    done
  fi
  cat > /etc/caddy/Caddyfile <<EOF
$DOMAIN {
    reverse_proxy 127.0.0.1:${FRONTEND_PORT:-6366}
}
EOF

  systemctl restart caddy
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
  echo "6. 退出"
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
  if [[ ! "$backend_port" =~ ^[0-9]+$ || "$backend_port" -lt 1 || "$backend_port" -gt 65535 ]]; then
    echo "❌ BACKEND_PORT 不是有效端口：$backend_port"
    return 1
  fi
  if [[ ! "$frontend_port" =~ ^[0-9]+$ || "$frontend_port" -lt 1 || "$frontend_port" -gt 65535 ]]; then
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

  if ! curl -fL -o "$dest" "$url"; then
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
  if ! curl -fL -o "$sha_file" "$sha_url"; then
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

download_panel_update_compose() {
  local compose_url="$1"

  UPDATE_COMPOSE_CANDIDATE=$(mktemp "$PANEL_DEPLOY_DIR/.docker-compose.yml.update.XXXXXX")
  if ! download_verified "$compose_url" "$UPDATE_COMPOSE_CANDIDATE"; then
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

activate_panel_update_files() {
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

  ask_proxy_config
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
  download_verified "$DOCKER_COMPOSE_URL" docker-compose.yml
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

  echo "🎉 部署完成"
  local _pub_ip
  _pub_ip=$(get_public_ip)
  echo "🌐 访问地址: http://${_pub_ip}:$FRONTEND_PORT"
  echo "📖 部署完成后请阅读下使用文档，求求了啊，不要上去就是一顿操作"
  echo "📚 文档地址: https://iiiiiiiii7125-art.github.io/flmx-max/guide.html"
  echo "💡 默认管理员账号: admin@user / admin@user"
  echo "⚠️  登录后请立即修改默认密码！"


}

# 更新功能
update_panel() {
  echo "🔄 开始更新面板..."
  check_docker noinstall || {
    echo "❌ 未检测到 Docker，本机没有面板部署，无法更新"
    return 1
  }
  resolve_panel_deployment || return 1
  ask_proxy_config
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
    ask_proxy_config
    ensure_compose_urls_initialized || return 1
    DOCKER_COMPOSE_URL=$(get_docker_compose_url)
    echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
    download_verified "$DOCKER_COMPOSE_URL" docker-compose.yml
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
  if ! docker run --rm --network gost-network -v sqlite_data:/sqlite dimitri/pgloader:latest pgloader "$pgloader_source" "postgresql://${postgres_user}:${postgres_password}@postgres:5432/${postgres_db}"; then
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
    ask_proxy_config
    ensure_compose_urls_initialized || return 1
    DOCKER_COMPOSE_URL=$(get_docker_compose_url)
    echo "📡 选择配置文件：$(basename "$DOCKER_COMPOSE_URL")"
    download_verified "$DOCKER_COMPOSE_URL" docker-compose.yml
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
  # maybe_proxy_url / download_verified，均已在上文定义）。放在 main 内
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
    read -p "请输入选项 (1-6): " choice

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
        echo "👋 退出脚本"
        delete_self
        exit 0
        ;;
      *)
        echo "❌ 无效选项，请输入 1-6"
        echo ""
        ;;
    esac
  done
}

# 执行主函数
main "$@"
