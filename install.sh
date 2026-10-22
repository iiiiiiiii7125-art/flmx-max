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

# 安装目录
INSTALL_DIR="/etc/flux_agent"
FLUX_AGENT_SYSTEMD_SERVICE_FILE="/etc/systemd/system/flux_agent.service"
FLUX_AGENT_OPENRC_SERVICE_FILE="/etc/init.d/flux_agent"
# OpenRC 服务文件里 pidfile="/run/${RC_SVCNAME}.pid"，服务名恒为 flux_agent。
FLUX_AGENT_OPENRC_PIDFILE="${FLUX_AGENT_OPENRC_PIDFILE:-/run/flux_agent.pid}"
LEGACY_GOST_BINARY="/usr/local/bin/gost"
LEGACY_GOST_CONFIG_DIR="/etc/gost"
LEGACY_GOST_SERVICE_FILE_ETC="/etc/systemd/system/gost.service"
LEGACY_GOST_SERVICE_FILE_LIB="/lib/systemd/system/gost.service"
LEGACY_GOST_SERVICE_FILE_USR_LIB="/usr/lib/systemd/system/gost.service"
SERVICE_MANAGER="${SERVICE_MANAGER:-}"

# 镜像加速配置（可由面板传入或交互式询问）
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

# H-01: Release 与二进制同目录发布 .sha256，下载后必须强校验。
# 校验 URL 从实际下载地址派生（追加 .sha256），代理/直连/file 均适用。
# 校验文件缺失时直接拒绝安装（fail-closed），不允许未校验的 root 二进制落地。
verify_downloaded_binary() {
    local binary_file="$1"
    local download_url="$2"
    if ! command -v sha256sum >/dev/null 2>&1; then
        echo "❌ 系统缺少 sha256sum，无法校验二进制完整性，拒绝继续。"
        return 1
    fi
    if [[ -z "$download_url" ]]; then
        echo "❌ 下载地址为空，无法校验，拒绝继续。"
        return 1
    fi
    echo "🔐 下载 SHA256 校验文件..."
    local checksum_file
    checksum_file=$(mktemp)
    if ! curl -fL --max-time 60 -o "$checksum_file" "${download_url}.sha256" 2>/dev/null; then
        rm -f "$checksum_file"
        echo "❌ 无法获取 .sha256 校验文件，拒绝安装未校验的二进制。"
        return 1
    fi
    local expected
    expected=$(awk '{print $1}' "$checksum_file")
    rm -f "$checksum_file"
    if [[ -z "$expected" ]]; then
        echo "❌ 校验文件为空，拒绝安装未校验的二进制。"
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
    return 0
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
  DOWNLOAD_URL=$(maybe_proxy_url "$_dl_url")
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
delete_self() {
  echo ""
  echo "🗑️ 操作已完成，正在清理脚本文件..."
  SCRIPT_PATH="$(readlink -f "$0" 2>/dev/null || realpath "$0" 2>/dev/null || echo "$0")"
  sleep 1
  rm -f "$SCRIPT_PATH" && echo "✅ 脚本文件已删除" || echo "❌ 删除脚本文件失败"
}

# 检查并安装 tcpkill
check_and_install_tcpkill() {
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

json_escape() {
  local value="$1"
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '%s' "$value"
}

write_flux_agent_config() {
  local path="$1"
  # GitHub 代理落盘：agent 的面板内升级下载会读 config.json 的 proxy 字段
  # （国内节点直连 GitHub 不通）。归一化与 maybe_proxy_url 一致：
  # 无协议头补 https://、去尾斜杠；未开启代理时不写该字段。
  local proxy_json=""
  if [[ "$PROXY_ENABLED" == "true" ]]; then
    local proxy="${PROXY_URL:-gcode.hostcentral.cc}"
    if [[ "$proxy" == https://* || "$proxy" == http://* ]]; then
      proxy="${proxy%/}"
    else
      proxy="https://${proxy%/}"
    fi
    proxy_json=$(printf ',\n  "proxy": "%s"' "$(json_escape "$proxy")")
  fi
  printf '{\n  "addr": "%s",\n  "secret": "%s"%s\n}\n' \
    "$(json_escape "$SERVER_ADDR")" \
    "$(json_escape "$SECRET")" \
    "$proxy_json" > "$path"
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

flux_agent_service_exists() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      [[ -f "$FLUX_AGENT_SYSTEMD_SERVICE_FILE" ]] || \
        systemctl list-units --full -all 2>/dev/null | grep -Fq "flux_agent.service"
      ;;
    openrc)
      [[ -f "$FLUX_AGENT_OPENRC_SERVICE_FILE" ]]
      ;;
  esac
}

stop_flux_agent_service() {
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

disable_flux_agent_service() {
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

write_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      mkdir -p "$(dirname "$FLUX_AGENT_SYSTEMD_SERVICE_FILE")"
      cat > "$FLUX_AGENT_SYSTEMD_SERVICE_FILE" <<EOF
[Unit]
Description=Flux_agent Proxy Service
After=network.target

[Service]
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/flux_agent
Environment=GODEBUG=disablethp=1
Restart=on-failure
StandardOutput=null
StandardError=null

[Install]
WantedBy=multi-user.target
EOF
      ;;
    openrc)
      mkdir -p "$(dirname "$FLUX_AGENT_OPENRC_SERVICE_FILE")"
      cat > "$FLUX_AGENT_OPENRC_SERVICE_FILE" <<EOF
#!/sbin/openrc-run

name="flux_agent"
description="Flux_agent Proxy Service"
command="$INSTALL_DIR/flux_agent"
directory="$INSTALL_DIR"
command_background="yes"
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="/dev/null"
error_log="/dev/null"

depend() {
  need net
}
EOF
      chmod +x "$FLUX_AGENT_OPENRC_SERVICE_FILE"
      ;;
  esac
}

enable_and_start_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl daemon-reload
      systemctl enable flux_agent
      systemctl start flux_agent
      ;;
    openrc)
      rc-update add flux_agent default
      rc-service flux_agent start
      ;;
  esac
}

start_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl start flux_agent
      ;;
    openrc)
      rc-service flux_agent start
      ;;
  esac
}

flux_agent_service_is_active() {
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

flux_agent_service_status() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl is-active flux_agent 2>/dev/null || true
      ;;
    openrc)
      rc-service flux_agent status 2>/dev/null || true
      ;;
  esac
}

flux_agent_main_pid() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      systemctl show -p MainPID --value flux_agent 2>/dev/null
      ;;
    openrc)
      cat "${FLUX_AGENT_OPENRC_PIDFILE:-/run/flux_agent.pid}" 2>/dev/null
      ;;
  esac
}

# X-P2-11 (J-01): 与 agent 侧 flmx_agent_health_ok
# （go-gost/x/socket/websocket_reporter.go）同规则的双采样判活：
# 间隔 2 秒两次采样，每次都必须 is-active，且主 PID（systemd
# MainPID / OpenRC pidfile PID）非空、非 0、两次一致。单次采样会
# 把存活数秒后倒下的 flap 二进制在存活窗口内误判为活（见 R4-N2）。
flux_agent_health_ok() {
  ensure_service_manager || return 1

  local p1 p2
  p1=$(flux_agent_main_pid) || p1=""
  p1=$(printf '%s' "$p1" | tr -d '[:space:]')
  if ! flux_agent_service_is_active; then
    return 1
  fi
  sleep 2
  p2=$(flux_agent_main_pid) || p2=""
  p2=$(printf '%s' "$p2" | tr -d '[:space:]')
  if ! flux_agent_service_is_active; then
    return 1
  fi
  [[ -n "$p1" && "$p1" != "0" && "$p1" == "$p2" ]]
}

remove_flux_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      rm -f "$FLUX_AGENT_SYSTEMD_SERVICE_FILE"
      systemctl daemon-reload 2>/dev/null || true
      ;;
    openrc)
      rm -f "$FLUX_AGENT_OPENRC_SERVICE_FILE"
      ;;
  esac
}

flux_agent_service_status_hint() {
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

cleanup_legacy_gost_installation() {
  local matched_service_files=()
  local service_file=""
  local removed_service_file="0"

  for service_file in "$LEGACY_GOST_SERVICE_FILE_ETC" "$LEGACY_GOST_SERVICE_FILE_LIB" "$LEGACY_GOST_SERVICE_FILE_USR_LIB"; do
    if [[ ! -f "$service_file" ]]; then
      continue
    fi
    if ! grep -Fq "WorkingDirectory=$LEGACY_GOST_CONFIG_DIR" "$service_file"; then
      continue
    fi
    if grep -Fq "ExecStart=$LEGACY_GOST_CONFIG_DIR/gost" "$service_file" || \
      (grep -Fq "ExecStart=$LEGACY_GOST_BINARY" "$service_file" && [[ -f "$LEGACY_GOST_CONFIG_DIR/config.json" && -f "$LEGACY_GOST_CONFIG_DIR/gost.json" ]]); then
      matched_service_files+=("$service_file")
    fi
  done

  if [[ ${#matched_service_files[@]} -eq 0 ]]; then
    return 0
  fi

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
      read -p "密钥: " SECRET
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
install_flux_agent() {
  echo "🚀 开始安装 flux_agent..."

  ask_proxy_config
  ensure_download_url_initialized || exit 1

  get_config_params

  # 检查并安装 tcpkill
  check_and_install_tcpkill
  ensure_service_manager || exit 1

  mkdir -p "$INSTALL_DIR"

  local tmp_binary="$INSTALL_DIR/flux_agent.new"

  # 下载 flux_agent
  echo "⬇️ 下载 flux_agent 中..."
  rm -f "$tmp_binary"
  curl -fL "$DOWNLOAD_URL" -o "$tmp_binary"
  if [[ ! -f "$tmp_binary" || ! -s "$tmp_binary" ]]; then
    rm -f "$tmp_binary"
    echo "❌ 下载失败，请检查网络或下载链接。"
    exit 1
  fi
  verify_downloaded_binary "$tmp_binary" "$DOWNLOAD_URL" || { rm -f "$tmp_binary"; exit 1; }
  cleanup_legacy_gost_installation

  # F-048: 停止并禁用已有服务移到下载+校验成功之后（与 update_flux_agent
  # 同序）。旧顺序先 stop+disable 再下载，下载/校验失败即 exit 1，节点
  # agent 停摆且开机不自启、必须人工干预；现在失败时旧服务保持运行、
  # 自启状态不变，失败只是「没装上新版」。
  if flux_agent_service_exists; then
    echo "🔍 检测到已存在的flux_agent服务"
    stop_flux_agent_service
    echo "🛑 停止服务"
    disable_flux_agent_service
    echo "🚫 禁用自启"
  fi
  mv "$tmp_binary" "$INSTALL_DIR/flux_agent"
  chmod +x "$INSTALL_DIR/flux_agent"
  echo "✅ 下载完成"

  # 打印版本
  echo "🔎 flux_agent 版本：$($INSTALL_DIR/flux_agent -V)"

  # 写入 config.json (安装时总是创建新的)
  CONFIG_FILE="$INSTALL_DIR/config.json"
  echo "📄 创建新配置: config.json"
  write_flux_agent_config "$CONFIG_FILE"

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
  write_flux_agent_service

  # 启动服务
  enable_and_start_flux_agent_service

  # 检查状态
  echo "🔄 检查服务状态..."
  if flux_agent_service_is_active; then
    echo "✅ 安装完成，flux_agent服务已启动并设置为开机启动。"
    echo "📁 配置目录: $INSTALL_DIR"
    echo "🔧 服务状态: $(flux_agent_service_status)"
  else
    echo "❌ flux_agent服务启动失败，请执行以下命令查看状态："
    flux_agent_service_status_hint
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
flux_agent_installed_version() {
  local raw
  [[ -x "$INSTALL_DIR/flux_agent" ]] || return 1
  raw=$("$INSTALL_DIR/flux_agent" -V 2>/dev/null) || return 1
  extract_leading_semver "$raw"
}

# 更新功能
update_flux_agent() {
  echo "🔄 开始更新 flux_agent..."
  
  if [[ ! -d "$INSTALL_DIR" ]]; then
    echo "❌ flux_agent 未安装，请先选择安装。"
    return 1
  fi

  ask_proxy_config
  ensure_download_url_initialized || return 1

  # X-P2-12 (J-02, BC-09): 下载前防降级。读现装版本（-V 经
  # extract_leading_semver 归一）与目标版本比较，目标更低时拒绝
  # 并说明——其余三条更新路径（面板自升级、节点面板升级单/批量、
  # panel_install 脚本更新）均已有此门，脚本路径此前独缺。任一侧
  # 不可比时沿用 panel_version_cmp 既有放行口径（V-19），不拦截。
  local current_version cmp_result
  current_version=$(flux_agent_installed_version || true)
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

  # 先下载新版本
  echo "⬇️ 下载最新版本..."
  rm -f "$INSTALL_DIR/flux_agent.new"
  curl -fL "$DOWNLOAD_URL" -o "$INSTALL_DIR/flux_agent.new"
  if [[ ! -f "$INSTALL_DIR/flux_agent.new" || ! -s "$INSTALL_DIR/flux_agent.new" ]]; then
    echo "❌ 下载失败。"
    return 1
  fi
  verify_downloaded_binary "$INSTALL_DIR/flux_agent.new" "$DOWNLOAD_URL" || { rm -f "$INSTALL_DIR/flux_agent.new"; return 1; }
  cleanup_legacy_gost_installation

  # 停止服务
  if flux_agent_service_exists; then
    echo "🛑 停止 flux_agent 服务..."
    stop_flux_agent_service
  fi

  # A9: 备份旧版本，失败可回滚
  if [[ -f "$INSTALL_DIR/flux_agent" ]]; then
    echo "💾 备份当前版本..."
    cp -f "$INSTALL_DIR/flux_agent" "$INSTALL_DIR/flux_agent.bak"
  fi

  # 替换文件
  mv "$INSTALL_DIR/flux_agent.new" "$INSTALL_DIR/flux_agent"
  chmod +x "$INSTALL_DIR/flux_agent"

  # 烟雾测试：新二进制必须可执行
  if ! "$INSTALL_DIR/flux_agent" -V >/dev/null 2>&1; then
    echo "❌ 新版本二进制无法执行，正在回滚..."
    rollback_flux_agent
    return 1
  fi

  # 打印版本
  echo "🔎 新版本：$($INSTALL_DIR/flux_agent -V)"

  # 重启服务
  echo "🔄 重启服务..."
  start_flux_agent_service

  # 回滚检查：新版本服务起不来则恢复备份。
  # X-P2-11 (J-01): 与 agent 侧升级脚本同口径——先等待 3 秒让
  # flap 形态（二进制启动后数秒才倒下）越过最初的存活窗口，再走
  # flux_agent_health_ok 双采样（间隔 2 秒、PID 稳定，采样点约在
  # 启动后 3 秒与 5 秒）。旧实现 sleep 2 后单次 is-active 恰会命中
  # flap 窗口、误判成功并删掉 .bak 回滚源。
  sleep 3
  if ! flux_agent_health_ok; then
    echo "❌ 新版本服务启动失败，正在回滚到备份版本..."
    rollback_flux_agent
    return 1
  fi
  rm -f "$INSTALL_DIR/flux_agent.bak"

  echo "✅ 更新完成，服务已重新启动。"
}

# A9: 更新失败时恢复备份的旧版本并重启
rollback_flux_agent() {
  if [[ -f "$INSTALL_DIR/flux_agent.bak" ]]; then
    mv -f "$INSTALL_DIR/flux_agent.bak" "$INSTALL_DIR/flux_agent"
    chmod +x "$INSTALL_DIR/flux_agent"
    start_flux_agent_service
    if flux_agent_service_is_active; then
      echo "✅ 已回滚到旧版本并重启成功。"
    else
      echo "❌ 回滚后服务仍未启动，请手动检查：$INSTALL_DIR"
    fi
  else
    echo "⚠️ 无备份可回滚，请手动检查：$INSTALL_DIR"
  fi
}

# 卸载功能
uninstall_flux_agent() {
  echo "🗑️ 开始卸载 flux_agent..."
  ensure_service_manager || return 1
  
  read -p "确认卸载 flux_agent 吗？此操作将删除所有相关文件 (y/N): " confirm
  if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
    echo "❌ 取消卸载"
    return 0
  fi

  # 停止并禁用服务
  if flux_agent_service_exists; then
    echo "🛑 停止并禁用服务..."
    stop_flux_agent_service
    disable_flux_agent_service
  fi

  # 删除服务文件
  if [[ -f "$FLUX_AGENT_SYSTEMD_SERVICE_FILE" || -f "$FLUX_AGENT_OPENRC_SERVICE_FILE" ]]; then
    remove_flux_agent_service
    echo "🧹 删除服务文件"
  fi

  # 删除安装目录
  if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    echo "🧹 删除安装目录: $INSTALL_DIR"
  fi

  echo "✅ 卸载完成"
}

# 主逻辑
main() {
  # X-P3-6 (J-03, BC-12): 各分支捕获操作函数返回码并以其退出。
  # 旧实现无条件 exit 0，菜单更新/卸载失败（函数以 return 1 报告）
  # 也被以退出码判断成败的自动化消费者误判为成功；成功路径仍
  # exit 0、delete_self 行为不变。install_flux_agent 内部直接
  # exit 1 的失败路径不受影响（进程已以 1 退出）。
  local rc=0

  # 如果提供了命令行参数，直接执行安装
  if [[ -n "$SERVER_ADDR" && -n "$SECRET" ]]; then
    install_flux_agent || rc=$?
    delete_self
    exit "$rc"
  fi

  # 显示交互式菜单
  while true; do
    show_menu
    read -p "请输入选项 (1-4): " choice

    case $choice in
      1)
        install_flux_agent || rc=$?
        delete_self
        exit "$rc"
        ;;
      2)
        update_flux_agent || rc=$?
        delete_self
        exit "$rc"
        ;;
      3)
        uninstall_flux_agent || rc=$?
        delete_self
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
