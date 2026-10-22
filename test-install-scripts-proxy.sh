#!/bin/bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

assert_equals() {
  local expected="$1"
  local actual="$2"
  local message="$3"

  if [[ "$actual" != "$expected" ]]; then
    fail "$message (expected: $expected, actual: $actual)"
  fi
}

load_script_without_main() {
  local script_path="$1"
  local temp_file

  temp_file=$(mktemp)
  sed '/^# 执行主函数$/,$d' "$script_path" > "$temp_file"
  VERSION="v-test"
  source "$temp_file"
  rm -f "$temp_file"
}

test_install_script_respects_disabled_proxy() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED="false"
  PROXY_URL=""

  local actual
  actual=$(maybe_proxy_url "https://github.com/example/release")

  assert_equals \
    "https://github.com/example/release" \
    "$actual" \
    "install.sh should bypass the proxy when disabled"
)

test_install_script_asks_for_proxy_config() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""

  ask_proxy_config >/dev/null < <(printf '\nmirror.example.com/\n')

  assert_equals "true" "$PROXY_ENABLED" "install.sh should enable proxy by default"
  assert_equals "mirror.example.com/" "$PROXY_URL" "install.sh should keep the entered proxy URL"

  local actual
  actual=$(maybe_proxy_url "https://github.com/example/release")

  assert_equals \
    "https://mirror.example.com/https://github.com/example/release" \
    "$actual" \
    "install.sh should normalize the entered proxy URL"
)

test_install_script_recomputes_download_url_after_prompt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_URL=""

  ask_proxy_config >/dev/null < <(printf 'n\n')
  ensure_download_url_initialized

  local expected
  expected=$(build_download_url)

  assert_equals \
    "$expected" \
    "$DOWNLOAD_URL" \
    "install.sh should build the final download URL after the interactive proxy choice"
)

test_update_flux_agent_asks_for_proxy_config() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"

  local ask_called="0"
  local cleanup_called="0"

  ask_proxy_config() {
    ask_called="1"
    PROXY_ENABLED="false"
    DOWNLOAD_URL=""
  }

  cleanup_legacy_gost_installation() {
    cleanup_called="1"
  }

  check_and_install_tcpkill() { :; }

  # X-P2-11: 判活改双采样后 systemctl 桩须能答 MainPID（恒定非 0），
  # 且 sleep 桩为空转（采样次数而非墙钟决定结果，套件不被 3+2 秒拖慢）。
  sleep() { :; }
  systemctl() {
    if [[ "$*" == *MainPID* ]]; then
      echo 4242
    fi
    return 0
  }

  curl() {
    local output=""
    local url=""

    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "-o" ]]; then
        output="$2"
        shift 2
        continue
      fi
      case "$1" in
        -*) ;;
        *) url="$1" ;;
      esac
      shift
    done

    # H-01: install.sh 下载后会校验 .sha256；mock 返回 fake 二进制的真实哈希
    if [[ "$url" == *.sha256 ]]; then
      printf '#!/bin/bash\necho "new version"\n' | sha256sum | awk '{print $1"  gost-amd64.sha256"}' > "$output"
      return 0
    fi

    cat > "$output" <<'EOF'
#!/bin/bash
echo "new version"
EOF
    chmod +x "$output"
  }

  update_flux_agent >/dev/null

  assert_equals "1" "$ask_called" "update_flux_agent should ask for proxy config before downloading"
  assert_equals "1" "$cleanup_called" "update_flux_agent should clean up legacy gost before restarting the agent"
  assert_equals "$(build_download_url)" "$DOWNLOAD_URL" "update_flux_agent should honor the prompted proxy choice"
)

test_update_flux_agent_skips_proxy_prompt_when_not_installed() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  INSTALL_DIR=$(mktemp -u)

  local ask_called="0"

  ask_proxy_config() {
    ask_called="1"
  }

  local rc="0"
  update_flux_agent >/dev/null || rc="$?"

  assert_equals "1" "$rc" "update_flux_agent should fail when the agent is not installed"
  assert_equals "0" "$ask_called" "update_flux_agent should not prompt for proxy config when the agent is missing"
)

# X-P2-11 (J-01): 双采样判活的四个关键场景（与 agent 侧
# fix3_health_test 同口径）：PID 稳定才活、第二次采样已死不活、
# 两次 PID 不一致不活、PID 为 0/空不活。采样间隔 sleep 桩为空转，
# 结果由桩的调用计数决定。
test_flux_agent_health_ok_requires_stable_pid() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  local state_dir
  state_dir=$(mktemp -d)
  printf '4242\n4242\n' > "$state_dir/pids"
  echo 0 > "$state_dir/pid_idx"
  echo 0 > "$state_dir/active_cnt"
  local maxok=99
  STATE_DIR="$state_dir"
  MAXOK_FILE="$state_dir/maxok"
  echo 99 > "$MAXOK_FILE"

  sleep() { :; }
  flux_agent_main_pid() {
    local n
    n=$(cat "$STATE_DIR/pid_idx"); n=$((n+1)); echo "$n" > "$STATE_DIR/pid_idx"
    sed -n "${n}p" "$STATE_DIR/pids"
  }
  flux_agent_service_is_active() {
    local n maxok
    n=$(cat "$STATE_DIR/active_cnt"); n=$((n+1)); echo "$n" > "$STATE_DIR/active_cnt"
    maxok=$(cat "$MAXOK_FILE")
    [[ "$n" -le "$maxok" ]]
  }
  reset_health_state() {
    echo 0 > "$STATE_DIR/pid_idx"
    echo 0 > "$STATE_DIR/active_cnt"
  }

  reset_health_state; echo 99 > "$MAXOK_FILE"
  flux_agent_health_ok || fail "X-P2-11: stable PID + always active should be healthy"

  # 第二次采样时已死（flap 形态）
  reset_health_state; echo 1 > "$MAXOK_FILE"
  if flux_agent_health_ok; then
    fail "X-P2-11: service dead at second sample must not be healthy"
  fi

  # 两次 PID 不一致（期间已重启）
  reset_health_state; echo 99 > "$MAXOK_FILE"
  printf '1111\n2222\n' > "$STATE_DIR/pids"
  if flux_agent_health_ok; then
    fail "X-P2-11: PID changing between samples must not be healthy"
  fi

  # PID 为 0 / 空
  printf '0\n0\n' > "$STATE_DIR/pids"
  reset_health_state
  if flux_agent_health_ok; then
    fail "X-P2-11: PID 0 must not be healthy"
  fi
  : > "$STATE_DIR/pids"
  reset_health_state
  if flux_agent_health_ok; then
    fail "X-P2-11: empty PID must not be healthy"
  fi
)

# X-P2-11 (J-01): flap 二进制（-V 通过、启动后短时存活即倒下，
# 此处以第二次 is-active 采样转失败模拟）走更新路径：必须判死、
# 回滚到旧二进制且回滚后服务 active；.bak 已被回滚消费。
# 正常二进制更新成功为防过修对照（见
# test_update_flux_agent_asks_for_proxy_config 的成功路径）。
test_update_flux_agent_rolls_back_flapping_binary() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
  : > "$FLUX_AGENT_SYSTEMD_SERVICE_FILE"
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi
echo "old good"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"

  local state_dir
  state_dir=$(mktemp -d)
  echo good > "$state_dir/mode"
  echo 0 > "$state_dir/active_cnt"
  STATE_DIR="$state_dir"

  ask_proxy_config() { PROXY_ENABLED="false"; DOWNLOAD_URL=""; }
  ensure_download_url_initialized() {
    RESOLVED_VERSION="1.3.5"
    DOWNLOAD_URL="https://example.com/gost"
  }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() { :; }
  sleep() { :; }
  systemctl() {
    case "$1" in
      start)
        echo 0 > "$STATE_DIR/active_cnt"
        if grep -q FLAP_MARKER "$INSTALL_DIR/flux_agent"; then
          echo flap > "$STATE_DIR/mode"
        else
          echo good > "$STATE_DIR/mode"
        fi
        return 0 ;;
      show)
        echo 4242; return 0 ;;
      is-active)
        if [[ "$(cat "$STATE_DIR/mode")" == "flap" ]]; then
          local n
          n=$(cat "$STATE_DIR/active_cnt"); n=$((n+1)); echo "$n" > "$STATE_DIR/active_cnt"
          [[ "$n" -le 1 ]]
          return $?
        fi
        return 0 ;;
      list-units)
        echo "flux_agent.service loaded active running"; return 0 ;;
      *) return 0 ;;
    esac
  }
  curl() {
    local output="" url="" prev=""
    for a in "$@"; do
      if [[ "$prev" == "-o" ]]; then output="$a"; fi
      case "$a" in http*) url="$a";; esac
      prev="$a"
    done
    local body='#!/bin/bash
# FLAP_MARKER
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.5 (go1.25 linux/amd64)"; exit 0; fi
sleep 4; exit 1'
    if [[ "$url" == *.sha256 ]]; then
      printf '%s\n' "$body" | sha256sum | awk '{print $1"  gost"}' > "$output"
      return 0
    fi
    printf '%s\n' "$body" > "$output"; chmod +x "$output"
  }

  local rc="0"
  update_flux_agent >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P2-11: flapping binary update should fail and roll back"
  grep -q "old good" "$INSTALL_DIR/flux_agent" || \
    fail "X-P2-11: rollback should restore the old binary"
  [[ ! -f "$INSTALL_DIR/flux_agent.bak" ]] || \
    fail "X-P2-11: rollback should consume the .bak backup"
  flux_agent_service_is_active || \
    fail "X-P2-11: service should be active after rollback"
)

test_install_flux_agent_preserves_legacy_gost_when_download_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"
  SERVER_ADDR="panel.example.com:443"
  SECRET="secret"
  DOWNLOAD_URL="https://example.com/gost"

  local cleanup_called="0"
  local rc="0"

  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() {
    cleanup_called="1"
  }
  systemctl() { return 0; }
  curl() { return 0; }

  ( install_flux_agent >/dev/null ) || rc="$?"

  assert_equals "1" "$rc" "install_flux_agent should fail when the download artifact is missing"
  assert_equals "0" "$cleanup_called" "install_flux_agent should preserve legacy gost when download fails"
  [[ -f "$INSTALL_DIR/flux_agent" ]] || fail "install_flux_agent should keep the existing flux_agent binary when download fails"
)

test_update_flux_agent_preserves_legacy_gost_when_download_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"
  cat > "$INSTALL_DIR/flux_agent.new" <<'EOF'
#!/bin/bash
echo "stale version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent.new"

  local cleanup_called="0"
  local rc="0"

  ask_proxy_config() {
    PROXY_ENABLED="false"
    DOWNLOAD_URL="https://example.com/gost"
  }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() {
    cleanup_called="1"
  }
  systemctl() { return 0; }
  curl() { return 0; }

  update_flux_agent >/dev/null || rc="$?"

  assert_equals "1" "$rc" "update_flux_agent should fail when the download artifact is missing"
  assert_equals "0" "$cleanup_called" "update_flux_agent should preserve legacy gost when download fails"
  [[ ! -f "$INSTALL_DIR/flux_agent.new" ]] || fail "update_flux_agent should remove stale download artifacts before retrying"
)

test_install_flux_agent_writes_json_safe_config() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
  SERVER_ADDR='panel"addr'
  SECRET='sec\ret"1'
  DOWNLOAD_URL="https://example.com/gost"

  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() { :; }
  systemctl() { return 0; }
  curl() {
    local output=""
    local url=""
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "-o" ]]; then
        output="$2"
        shift 2
        continue
      fi
      case "$1" in
        -*) ;;
        *) url="$1" ;;
      esac
      shift
    done

    # H-01: install.sh 下载后会校验 .sha256；mock 返回 fake 二进制的真实哈希
    if [[ "$url" == *.sha256 ]]; then
      printf '#!/bin/bash\necho "new version"\n' | sha256sum | awk '{print $1"  gost-amd64.sha256"}' > "$output"
      return 0
    fi

    cat > "$output" <<'EOF'
#!/bin/bash
echo "new version"
EOF
    chmod +x "$output"
  }

  ( install_flux_agent >/dev/null 2>/dev/null ) || true

  local actual
  actual=$(<"$INSTALL_DIR/config.json")
  local expected=$'{\n  "addr": "panel\\"addr",\n  "secret": "sec\\\\ret\\"1"\n}'

  assert_equals "$expected" "$actual" "install_flux_agent should JSON-escape config values"
  grep -Fqx 'Environment=GODEBUG=disablethp=1' "$FLUX_AGENT_SYSTEMD_SERVICE_FILE" || \
    fail "systemd service should disable transparent huge pages for the Go heap"
)

test_install_script_bootstraps_bash_for_alpine() (
  set -euo pipefail

  local shebang
  shebang=$(head -n 1 "$ROOT_DIR/install.sh")
  assert_equals "#!/bin/sh" "$shebang" "install.sh should start with Alpine's default shell"

  grep -Fq 'apk add --no-cache bash' "$ROOT_DIR/install.sh" || \
    fail "install.sh should bootstrap Bash through apk on Alpine"
)

test_install_flux_agent_uses_openrc() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local temp_root
  temp_root=$(mktemp -d)
  INSTALL_DIR="$temp_root/flux_agent"
  FLUX_AGENT_OPENRC_SERVICE_FILE="$temp_root/init.d/flux_agent"
  SERVICE_MANAGER="openrc"
  SERVER_ADDR="panel.example.com:443"
  SECRET="secret"
  DOWNLOAD_URL="https://example.com/gost"

  local rc_service_calls=""
  local rc_update_calls=""

  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() { :; }
  curl() {
    local output=""
    local url=""
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "-o" ]]; then
        output="$2"
        shift 2
        continue
      fi
      case "$1" in
        -*) ;;
        *) url="$1" ;;
      esac
      shift
    done

    # H-01: install.sh 下载后会校验 .sha256；mock 返回 fake 二进制的真实哈希
    if [[ "$url" == *.sha256 ]]; then
      printf '#!/bin/sh\necho "new version"\n' | sha256sum | awk '{print $1"  gost-amd64.sha256"}' > "$output"
      return 0
    fi

    cat > "$output" <<'EOF'
#!/bin/sh
echo "new version"
EOF
    chmod +x "$output"
  }
  rc-service() {
    rc_service_calls+=$'\n'"$*"
    if [[ "$2" == "status" ]]; then
      echo "status: started"
    fi
    return 0
  }
  rc-update() {
    rc_update_calls+=$'\n'"$*"
    return 0
  }

  install_flux_agent >/dev/null

  [[ -x "$FLUX_AGENT_OPENRC_SERVICE_FILE" ]] || fail "OpenRC service file should be executable"
  grep -Fq '#!/sbin/openrc-run' "$FLUX_AGENT_OPENRC_SERVICE_FILE" || \
    fail "OpenRC service should use openrc-run"
  grep -Fq "command=\"$INSTALL_DIR/flux_agent\"" "$FLUX_AGENT_OPENRC_SERVICE_FILE" || \
    fail "OpenRC service should launch the installed flux_agent binary"
  grep -Fq 'command_background="yes"' "$FLUX_AGENT_OPENRC_SERVICE_FILE" || \
    fail "OpenRC service should run flux_agent in the background"
  if command -v openrc-run >/dev/null 2>&1; then
    "$FLUX_AGENT_OPENRC_SERVICE_FILE" describe >/dev/null 2>&1
  fi
  [[ "$rc_update_calls" == *"add flux_agent default"* ]] || \
    fail "OpenRC install should enable flux_agent in the default runlevel"
  [[ "$rc_service_calls" == *"start"* ]] || fail "OpenRC install should start flux_agent"
  [[ "$rc_service_calls" == *"status"* ]] || fail "OpenRC install should verify flux_agent status"
)

test_remove_flux_agent_service_uses_openrc() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local temp_root
  temp_root=$(mktemp -d)
  SERVICE_MANAGER="openrc"
  FLUX_AGENT_OPENRC_SERVICE_FILE="$temp_root/init.d/flux_agent"
  mkdir -p "$(dirname "$FLUX_AGENT_OPENRC_SERVICE_FILE")"
  : > "$FLUX_AGENT_OPENRC_SERVICE_FILE"

  local rc_service_calls=""
  local rc_update_calls=""
  rc-service() {
    rc_service_calls+=$'\n'"$*"
    return 0
  }
  rc-update() {
    rc_update_calls+=$'\n'"$*"
    return 0
  }

  stop_flux_agent_service
  disable_flux_agent_service
  remove_flux_agent_service

  [[ "$rc_service_calls" == *"stop"* ]] || fail "OpenRC uninstall should stop flux_agent"
  [[ "$rc_update_calls" == *"del flux_agent default"* ]] || \
    fail "OpenRC uninstall should remove flux_agent from the default runlevel"
  [[ ! -e "$FLUX_AGENT_OPENRC_SERVICE_FILE" ]] || fail "OpenRC uninstall should remove its service file"
)

test_cleanup_legacy_gost_installation_removes_service_and_binary() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  LEGACY_GOST_BINARY=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_ETC=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_LIB=$(mktemp -u)
  LEGACY_GOST_SERVICE_FILE_USR_LIB=$(mktemp -u)
  LEGACY_GOST_CONFIG_DIR=$(mktemp -d)
  SERVICE_MANAGER="systemd"
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<EOF
[Unit]
Description=Gost Proxy Service

[Service]
WorkingDirectory=$LEGACY_GOST_CONFIG_DIR
ExecStart=$LEGACY_GOST_CONFIG_DIR/gost
EOF
  : > "$LEGACY_GOST_CONFIG_DIR/config.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost.json"

  local systemctl_calls=""

  systemctl() {
    systemctl_calls+=$'\n'"$*"
    if [[ "$1" == "list-units" ]]; then
      printf 'gost.service loaded active running\n'
    fi
    return 0
  }

  cleanup_legacy_gost_installation >/dev/null

  if [[ -e "$LEGACY_GOST_BINARY" ]]; then
    fail "cleanup_legacy_gost_installation should remove the legacy gost binary"
  fi
  if [[ -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]]; then
    fail "cleanup_legacy_gost_installation should remove the legacy gost service file"
  fi
  [[ "$systemctl_calls" == *"stop gost"* ]] || fail "cleanup_legacy_gost_installation should stop the legacy gost service"
  [[ "$systemctl_calls" == *"disable gost"* ]] || fail "cleanup_legacy_gost_installation should disable the legacy gost service"
  [[ "$systemctl_calls" == *"daemon-reload"* ]] || fail "cleanup_legacy_gost_installation should reload systemd after removing the legacy service"
)

test_cleanup_legacy_gost_installation_preserves_unrelated_gost() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  LEGACY_GOST_BINARY=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_ETC=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_LIB=$(mktemp -u)
  LEGACY_GOST_SERVICE_FILE_USR_LIB=$(mktemp -u)
  LEGACY_GOST_CONFIG_DIR=$(mktemp -d)
  SERVICE_MANAGER="systemd"
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<'EOF'
[Unit]
Description=Unrelated Gost Service

[Service]
WorkingDirectory=/srv/custom-gost
ExecStart=/usr/local/bin/gost -C /srv/custom-gost/gost.yaml
EOF

  local systemctl_calls=""

  systemctl() {
    systemctl_calls+=$'\n'"$*"
    if [[ "$1" == "list-units" ]]; then
      printf 'gost.service loaded active running\n'
    fi
    return 0
  }

  cleanup_legacy_gost_installation >/dev/null

  [[ -e "$LEGACY_GOST_BINARY" ]] || fail "cleanup_legacy_gost_installation should preserve unrelated gost binaries"
  [[ -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "cleanup_legacy_gost_installation should preserve unrelated gost service files"
  [[ "$systemctl_calls" != *"stop gost"* ]] || fail "cleanup_legacy_gost_installation should not stop unrelated gost services"
  [[ "$systemctl_calls" != *"disable gost"* ]] || fail "cleanup_legacy_gost_installation should not disable unrelated gost services"
)

test_cleanup_legacy_gost_installation_skips_systemd_on_openrc() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  LEGACY_GOST_SERVICE_FILE_ETC=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_LIB=$(mktemp -u)
  LEGACY_GOST_SERVICE_FILE_USR_LIB=$(mktemp -u)
  LEGACY_GOST_CONFIG_DIR=$(mktemp -d)
  SERVICE_MANAGER="openrc"
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<EOF
[Unit]
WorkingDirectory=$LEGACY_GOST_CONFIG_DIR
ExecStart=$LEGACY_GOST_CONFIG_DIR/gost
EOF
  : > "$LEGACY_GOST_CONFIG_DIR/config.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost.json"

  local systemctl_calls=""
  systemctl() {
    systemctl_calls+=$'\n'"$*"
    return 1
  }

  cleanup_legacy_gost_installation >/dev/null

  [[ -z "$systemctl_calls" ]] || fail "OpenRC cleanup should not invoke systemctl"
  [[ ! -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "OpenRC cleanup should still remove the legacy service file"
)

test_install_script_accepts_proxy_url_env_without_prompt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL="mirror.example.com"

  ask_proxy_config >/dev/null < <(printf '\n\n')

  assert_equals "true" "$PROXY_ENABLED" "install.sh should treat PROXY_URL as enabling the proxy"
  assert_equals "mirror.example.com" "$PROXY_URL" "install.sh should preserve PROXY_URL when provided via env"
)

test_panel_install_script_can_disable_proxy() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""

  ask_proxy_config >/dev/null < <(printf 'n\n')

  assert_equals "false" "$PROXY_ENABLED" "panel_install.sh should allow disabling proxy"

  local actual
  actual=$(maybe_proxy_url "https://github.com/example/release")

  assert_equals \
    "https://github.com/example/release" \
    "$actual" \
    "panel_install.sh should bypass the proxy after disabling it"
)

test_panel_install_script_recomputes_compose_urls_after_prompt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOCKER_COMPOSEV4_URL=""
  DOCKER_COMPOSEV6_URL=""

  ask_proxy_config >/dev/null < <(printf 'n\n')
  ensure_compose_urls_initialized

  assert_equals \
    "https://github.com/${REPO}/releases/download/${RESOLVED_VERSION}/docker-compose-v4.yml" \
    "$DOCKER_COMPOSEV4_URL" \
    "panel_install.sh should build the compose URL after the interactive proxy choice"
)

test_update_panel_asks_for_proxy_config() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local ask_called="0"
  local calls=""

  ask_proxy_config() {
    ask_called="1"
    PROXY_ENABLED="false"
    DOCKER_COMPOSEV4_URL=""
    DOCKER_COMPOSEV6_URL=""
  }

  check_docker() {
    DOCKER_CMD="true"
  }

  resolve_panel_deployment() {
    calls+=" resolve"
    PANEL_DEPLOY_DIR=$(mktemp -d)
  }

  validate_panel_update_environment() {
    calls+=" validate-current"
  }

  get_current_db_type() {
    echo "sqlite"
  }

  resolve_latest_release_tag() {
    echo "v-test"
  }

  download_panel_update_compose() {
    calls+=" download"
    UPDATE_COMPOSE_CANDIDATE="candidate"
  }

  validate_panel_update_compose() {
    calls+=" validate-new"
  }

  create_panel_update_backup() {
    calls+=" backup"
    UPDATE_BACKUP_DIR="backup"
  }

  pull_panel_update_images() {
    calls+=" pull"
  }

  activate_panel_update_files() {
    calls+=" activate"
  }

  start_panel_after_update() {
    calls+=" start"
  }

  check_ipv6_support() { return 1; }

  update_panel >/dev/null

  assert_equals "1" "$ask_called" "update_panel should ask for proxy config before downloading"
  assert_equals \
    "https://github.com/${REPO}/releases/download/v-test/docker-compose-v4.yml" \
    "$DOCKER_COMPOSEV4_URL" \
    "update_panel should honor the prompted proxy choice"
  assert_equals \
    " resolve validate-current download validate-new backup pull activate start" \
    "$calls" \
    "update_panel should validate, back up, and pull before activating the update"
)

test_resolve_panel_deployment_uses_container_labels() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local expected_dir
  expected_dir=$(mktemp -d)
  expected_dir=$(cd "$expected_dir" && pwd -P)
  printf 'JWT_SECRET=test\nBACKEND_PORT=6365\nFRONTEND_PORT=6366\nDB_TYPE=sqlite\n' > "$expected_dir/.env"
  printf 'services: {}\n' > "$expected_dir/docker-compose.yml"

  unset PANEL_DEPLOY_DIR COMPOSE_PROJECT_NAME
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *working_dir*) printf '%s\n' "$expected_dir" ;;
        *) printf '%s\n' "flvx-panel" ;;
      esac
    fi
  }

  resolve_panel_deployment >/dev/null

  assert_equals "$expected_dir" "$PANEL_DEPLOY_DIR" "update should discover the Compose working directory from container labels"
  assert_equals "flvx-panel" "$COMPOSE_PROJECT_NAME" "update should preserve the existing Compose project name"
  assert_equals "$expected_dir" "$(pwd -P)" "update should run from the discovered deployment directory"
)

test_validate_panel_update_environment_rejects_empty_required_values() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local deploy_dir rc="0"
  deploy_dir=$(mktemp -d)
  printf 'JWT_SECRET=\nBACKEND_PORT=6365\nFRONTEND_PORT=6366\nDB_TYPE=sqlite\n' > "$deploy_dir/.env"
  printf 'services: {}\n' > "$deploy_dir/docker-compose.yml"
  cd "$deploy_dir"
  DOCKER_CMD="true"

  validate_panel_update_environment >/dev/null || rc="$?"

  assert_equals "1" "$rc" "update should reject an empty JWT secret before touching the running service"
)

test_backup_sqlite_for_update_pauses_copies_and_unpauses() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local destination calls log_path
  destination=$(mktemp -d)/sqlite
  log_path=$(mktemp)
  PANEL_BACKEND_CONTAINER="flux-panel-backend"
  docker() {
    if [[ "$1" == "inspect" ]]; then
      printf 'true\n'
      return 0
    fi
    printf ' %s' "$1" >> "$log_path"
  }

  backup_sqlite_for_update "$destination" >/dev/null
  calls=$(cat "$log_path")

  assert_equals " pause cp unpause" "$calls" "SQLite backup should pause writes, copy the database volume, then resume the backend"
)

test_panel_install_script_accepts_proxy_url_env_without_prompt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED=""
  PROXY_URL="mirror.example.com"

  ask_proxy_config >/dev/null < <(printf '\n\n')

  assert_equals "true" "$PROXY_ENABLED" "panel_install.sh should treat PROXY_URL as enabling the proxy"
  assert_equals "mirror.example.com" "$PROXY_URL" "panel_install.sh should preserve PROXY_URL when provided via env"
)

test_panel_install_script_defaults_proxy_on_eof() {
  local rc="0"
  local output
  local temp_script

  temp_script=$(mktemp)
  sed '/^# 执行主函数$/,$d' "$ROOT_DIR/panel_install.sh" > "$temp_script"
  cat >> "$temp_script" <<'EOF'
PROXY_ENABLED=""
PROXY_URL=""
ask_proxy_config >/dev/null < /dev/null
printf '%s\n%s\n' "$PROXY_ENABLED" "$PROXY_URL"
EOF

  output=$(bash "$temp_script") || rc="$?"
  rm -f "$temp_script"

  assert_equals "0" "$rc" "panel_install.sh should not fail when proxy prompt receives EOF"
  assert_equals $'true\ngcode.hostcentral.cc' "$output" "panel_install.sh should fall back to the default proxy on EOF"
}

test_panel_install_script_uses_default_proxy() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED="true"
  PROXY_URL=""

  local actual
  actual=$(maybe_proxy_url "https://github.com/example/release")

  assert_equals \
    "https://gcode.hostcentral.cc/https://github.com/example/release" \
    "$actual" \
    "panel_install.sh should keep the default proxy when enabled"
)

# F-006: panel_install 自更新必须走带 sha256 校验的 Release 通道。
# 夹具：stub curl 按 URL 供数——releases/latest 提供版本重定向，
# 版本化资产提供脚本本体与 .sha256。
#   tamper：脚本本体与 .sha256 不符（被篡改）→ 不得覆写 $0、不得执行新脚本。
#   legit ：脚本与 .sha256 自洽 → 应正常自更新并执行新脚本（哨兵出现）。
panel_self_update_fixture() {
  local mode="$1"
  local work stubbin good_script
  work=$(mktemp -d)
  stubbin=$(mktemp -d)
  good_script="$work/good_panel.sh"
  cat > "$good_script" <<EOF
#!/bin/bash
SCRIPT_VERSION="${STUB_GOOD_SV:-99999999.99}"
PINNED_VERSION="${STUB_GOOD_PINNED-9.9.9}"
echo "updated" > "\$SELF_UPDATE_SENTINEL"
EOF
  if [[ "$mode" == "legit" ]]; then
    STUB_SHA256=$(sha256sum "$good_script" | awk '{print $1}')
  else
    STUB_SHA256="0000000000000000000000000000000000000000000000000000000000000000"
  fi
  export STUB_SHA256
  export STUB_GOOD_SCRIPT="$good_script"
  cat > "$stubbin/curl" <<'EOF'
#!/bin/bash
out=""; url=""; want_effective=0
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  a="${args[i]}"
  case "$a" in
    -o) out="${args[i+1]}" ;;
    *url_effective*) want_effective=1 ;;
    http*://*) url="$a" ;;
  esac
done
if [[ "$want_effective" == "1" ]]; then
  printf 'https://github.com/iiiiiiiii7125-art/flmx-max/releases/tag/9.9.9'
  exit 0
fi
case "$url" in
  *.sha256*)
    if [[ -n "$out" ]]; then printf '%s' "$STUB_SHA256" > "$out"; else printf '%s' "$STUB_SHA256"; fi
    ;;
  *panel_install.sh*)
    if [[ -n "$out" ]]; then cat "$STUB_GOOD_SCRIPT" > "$out"; else cat "$STUB_GOOD_SCRIPT"; fi
    ;;
  *)
    exit 22
    ;;
esac
exit 0
EOF
  chmod +x "$stubbin/curl"
  cp "$ROOT_DIR/panel_install.sh" "$work/panel_install.sh"
  export SELF_UPDATE_SENTINEL="$work/sentinel"
  rm -f "$SELF_UPDATE_SENTINEL"
  ( cd "$work" && printf '6\n' | PATH="$stubbin:$PATH" PROXY_ENABLED=false bash ./panel_install.sh >/dev/null 2>&1 ) || true
  echo "$work"
}

test_panel_install_self_update_rejects_tampered_script() {
  local work
  work=$(panel_self_update_fixture tamper)
  [[ ! -f "$work/sentinel" ]] || fail "F-006: tampered self-update script must not be executed (sentinel present)"
  if [[ -f "$work/panel_install.sh" ]]; then
    grep -q 'SCRIPT_VERSION="99999999.99"' "$work/panel_install.sh" && fail "F-006: tampered self-update must not overwrite \$0"
    :
  fi
  echo "ok: self-update rejects tampered script"
}

test_panel_install_self_update_accepts_verified_script() {
  local work
  work=$(panel_self_update_fixture legit)
  [[ -f "$work/sentinel" ]] || fail "F-006: verified self-update script should be executed (sentinel missing)"
  echo "ok: self-update accepts verified script"
}

test_panel_install_self_update_rejects_tampered_script
test_panel_install_self_update_accepts_verified_script

# LANE1: 自更新三不变量——定版一致、方向单调、（单跳由 HOPS 保证）。
# 以下三种「sha 自洽但不应接管」的资产都必须被拒：降级、未定版、定版不符。
test_panel_install_self_update_rejects_downgrade() {
  local work
  work=$(STUB_GOOD_SV="20200101.01" panel_self_update_fixture legit)
  [[ ! -f "$work/sentinel" ]] || fail "LANE1: downgrade self-update must not be executed (sentinel present)"
  echo "ok: self-update rejects downgrade script"
}

test_panel_install_self_update_rejects_unpinned() {
  local work
  work=$(STUB_GOOD_PINNED="" panel_self_update_fixture legit)
  [[ ! -f "$work/sentinel" ]] || fail "LANE1: unpinned self-update must not be executed (sentinel present)"
  echo "ok: self-update rejects unpinned script"
}

test_panel_install_self_update_rejects_pin_mismatch() {
  local work
  work=$(STUB_GOOD_PINNED="8.8.8" panel_self_update_fixture legit)
  [[ ! -f "$work/sentinel" ]] || fail "LANE1: pin-mismatched self-update must not be executed (sentinel present)"
  echo "ok: self-update rejects pin-mismatched script"
}

test_panel_install_self_update_rejects_downgrade
test_panel_install_self_update_rejects_unpinned
test_panel_install_self_update_rejects_pin_mismatch

# F-048: install 路径下载失败时不得先停/禁用已有服务（与 update 路径同序）。
test_install_flux_agent_keeps_service_when_download_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  printf '#!/bin/bash\necho "old version"\n' > "$INSTALL_DIR/flux_agent"
  chmod +x "$INSTALL_DIR/flux_agent"
  SERVER_ADDR="panel.example.com:443"
  SECRET="<redacted>"
  DOWNLOAD_URL="https://example.com/gost"

  # 调用记录走文件：install_flux_agent 在子 shell 里跑，变量赋值传不回来
  local calls_log
  calls_log=$(mktemp)
  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  ensure_service_manager() { return 0; }
  cleanup_legacy_gost_installation() { :; }
  flux_agent_service_exists() { return 0; }
  stop_flux_agent_service() { echo stop >> "$calls_log"; }
  disable_flux_agent_service() { echo disable >> "$calls_log"; }
  curl() { return 0; }

  local rc="0"
  ( install_flux_agent >/dev/null ) || rc="$?"

  assert_equals "1" "$rc" "F-048: install should fail when the download artifact is missing"
  if grep -q '^stop$' "$calls_log"; then
    fail "F-048: install must not stop the running service when download fails"
  fi
  if grep -q '^disable$' "$calls_log"; then
    fail "F-048: install must not disable the running service when download fails"
  fi
)

# F-049: 已有部署时 install_panel 必须拒绝（除非显式强制并先备份 .env）。
test_install_panel_refuses_existing_deployment() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  printf 'JWT_SECRET=keepme\nPOSTGRES_PASSWORD=keeppw\n' > "$work/.env"
  printf 'services: {}\n' > "$work/docker-compose.yml"

  ask_proxy_config() { :; }
  ensure_compose_urls_initialized() {
    DOCKER_COMPOSEV4_URL="https://example.com/docker-compose-v4.yml"
    DOCKER_COMPOSEV6_URL="https://example.com/docker-compose-v6.yml"
    return 0
  }
  check_docker() { DOCKER_CMD="docker"; return 0; }
  get_config_params() {
    FRONTEND_PORT=6366; BACKEND_PORT=6365; RESOLVED_VERSION="9.9.9"
    DB_TYPE="sqlite"; DATABASE_URL="file:/data/panel.db"
    POSTGRES_DB="flux_panel"; POSTGRES_USER="flux_panel"
    POSTGRES_PASSWORD="brandnew"; JWT_SECRET="brandnewsecret"
  }
  get_docker_compose_url() { echo "https://example.com/docker-compose-v4.yml"; }
  download_verified() { return 0; }
  check_ipv6_support() { return 1; }
  configure_docker_ipv6() { return 0; }
  docker_ipv6_usable() { return 1; }
  get_public_ip() { echo "203.0.113.1"; }
  docker() { return 0; }

  local rc="0"
  ( cd "$work" && install_panel >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "F-049: install_panel must refuse when a deployment already exists"
  grep -q "keepme" "$work/.env" || fail "F-049: install_panel must not overwrite .env of an existing deployment"

  # 显式强制重装：放行，但原 .env 必须先备份
  local rc2="0"
  ( cd "$work" && PANEL_FORCE_REINSTALL=1 install_panel >/dev/null 2>&1 ) || rc2="$?"
  assert_equals "0" "$rc2" "F-049: PANEL_FORCE_REINSTALL=1 should allow reinstall"
  ls "$work"/.env.bak.* >/dev/null 2>&1 || fail "F-049: forced reinstall must back up the old .env first"
  grep -q "keepme" "$work"/.env.bak.* || fail "F-049: .env backup must contain the old secrets"
)

# F-050: Docker 安装脚本未通过 pin 校验时不得执行（哨兵法）。
test_install_docker_rejects_unverified_script() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local sentinel
  sentinel=$(mktemp -u)
  export DOCKER_INSTALL_SENTINEL="$sentinel"
  curl() {
    local out="" prev=""
    for a in "$@"; do
      if [[ "$prev" == "-o" ]]; then out="$a"; fi
      prev="$a"
    done
    if [[ -n "$out" ]]; then
      printf '#!/bin/sh\ntouch "%s"\n' "$DOCKER_INSTALL_SENTINEL" > "$out"
    else
      printf '#!/bin/sh\ntouch "%s"\n' "$DOCKER_INSTALL_SENTINEL"
    fi
    return 0
  }
  systemctl() { return 0; }
  service() { return 0; }
  apt-get() { return 1; }
  dnf() { return 1; }
  yum() { return 1; }
  apk() { return 1; }

  local rc="0"
  ( install_docker >/dev/null 2>&1 ) || rc="$?"
  [[ ! -f "$sentinel" ]] || fail "F-050: unverified Docker installer script must not be executed"
  assert_equals "1" "$rc" "F-050: install_docker should fail when script is unverified and package fallback fails"
)

# F-138: download_verified 边角——空 .sha256 必须拒；逃生口误填 yes 不得静默跳过校验。
test_download_verified_fail_closed_edges() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work dest
  work=$(mktemp -d)
  dest="$work/f.bin"
  curl() {
    local out="" prev="" url=""
    for a in "$@"; do
      if [[ "$prev" == "-o" ]]; then out="$a"; fi
      case "$a" in http*://*) url="$a";; esac
      prev="$a"
    done
    case "$url" in
      *.sha256) : > "$out" ;;
      *) printf 'payload' > "$out" ;;
    esac
    return 0
  }

  local rc="0"
  ( FLMX_VERIFY_DOWNLOADS=1 download_verified "https://example.com/f.bin" "$dest" >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "F-138: empty .sha256 must be rejected"
  [[ ! -f "$dest" ]] || fail "F-138: rejected download must be removed"

  # 误填 yes：不得跳过校验（.sha256 仍为空 → 必须拒）
  rc="0"
  ( FLMX_VERIFY_DOWNLOADS=yes download_verified "https://example.com/f.bin" "$dest" >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "F-138: FLMX_VERIFY_DOWNLOADS=yes must not silently skip verification"
)

test_preserve_compose_networks_keeps_existing_subnet() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/current.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.3.2
networks:
  gost-network:
    name: gost-network
    driver: bridge
    ipam:
      config:
        - subnet: 172.80.0.0/16
YAML
  cat > "$work/candidate.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.3.3
networks:
  gost-network:
    name: gost-network
    driver: bridge
    ipam:
      config:
        - subnet: 172.30.0.0/16
YAML

  preserve_compose_networks "$work/candidate.yml" "$work/current.yml"

  grep -q "subnet: 172.80.0.0/16" "$work/candidate.yml" \
    || fail "FIX2-C#1: existing subnet must be carried into the update candidate"
  if grep -q "172.30.0.0/16" "$work/candidate.yml"; then
    fail "FIX2-C#1: template subnet must not overwrite an existing deployment"
  fi
  grep -q "example/backend:1.3.3" "$work/candidate.yml" \
    || fail "FIX2-C#1: candidate content must otherwise be preserved"
)

test_preserve_compose_networks_no_subnet_existing() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/current.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.3.2
networks:
  gost-network:
    name: gost-network
    driver: bridge
YAML
  cat > "$work/candidate.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.3.3
networks:
  gost-network:
    name: gost-network
    driver: bridge
    ipam:
      config:
        - subnet: 172.30.0.0/16
YAML

  preserve_compose_networks "$work/candidate.yml" "$work/current.yml"

  if grep -q "subnet:" "$work/candidate.yml"; then
    fail "FIX2-C#1: a deployment without an explicit subnet must not gain the template subnet"
  fi
)

test_activate_panel_update_files_preserves_subnet() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cd "$work"
  printf 'FLUX_VERSION=1.3.2\nJWT_SECRET=x\n' > .env
  cat > docker-compose.yml <<'YAML'
networks:
  gost-network:
    name: gost-network
    driver: bridge
    ipam:
      config:
        - subnet: 172.80.0.0/16
YAML
  cat > candidate.yml <<'YAML'
networks:
  gost-network:
    name: gost-network
    driver: bridge
    ipam:
      config:
        - subnet: 172.30.0.0/16
YAML

  UPDATE_COMPOSE_CANDIDATE="$work/candidate.yml"
  LATEST_VERSION="1.3.3"
  activate_panel_update_files

  grep -q "subnet: 172.80.0.0/16" docker-compose.yml \
    || fail "FIX2-C#1: activate_panel_update_files must keep the deployed subnet"
  if grep -q "172.30.0.0/16" docker-compose.yml; then
    fail "FIX2-C#1: activate_panel_update_files must not switch the deployed subnet"
  fi
  grep -q "FLUX_VERSION=1.3.3" .env \
    || fail "activate_panel_update_files must still bump FLUX_VERSION"
)

test_get_deployed_panel_version_parses_image_tag() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  docker() {
    if [[ "$1" == "inspect" ]]; then
      echo "ghcr.io/example/flmx-panel-backend:1.9.9"
    fi
  }
  assert_equals "1.9.9" "$(get_deployed_panel_version)" \
    "deployed version should come from the running backend image tag"

  docker() {
    if [[ "$1" == "inspect" ]]; then
      echo "ghcr.io/example/flmx-panel-backend@sha256:deadbeef"
    fi
  }
  if get_deployed_panel_version >/dev/null; then
    fail "FIX2-C#9: an image without a tag must not yield a deployed version"
  fi
)

test_update_panel_crosschecks_deployed_image_version() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  # load_script_without_main 预设了 VERSION="v-test"（定版分支），
  # 本例要走 latest 分支，必须先清掉。
  unset VERSION

  local calls=""
  local deploy_dir
  deploy_dir=$(mktemp -d)
  printf 'FLUX_VERSION=1.0.0\nJWT_SECRET=x\n' > "$deploy_dir/.env"
  printf 'services: {}\n' > "$deploy_dir/docker-compose.yml"

  docker() {
    if [[ "$1" == "inspect" ]]; then
      echo "ghcr.io/example/flmx-panel-backend:1.9.9"
    fi
  }
  check_docker() { DOCKER_CMD="true"; }
  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$deploy_dir"
    cd "$deploy_dir"
  }
  ask_proxy_config() { PROXY_ENABLED="false"; }
  validate_panel_update_environment() { :; }
  get_current_db_type() { echo "sqlite"; }
  resolve_latest_release_tag() { echo "1.5.0"; }
  download_panel_update_compose() { calls+=" download"; }

  # .env 自称 1.0.0、实际运行 1.9.9、目标 1.5.0：按 .env 判定会
  # 放行，按实际运行版本判定必须以「降级」拒绝。
  if update_panel >/dev/null; then
    fail "FIX2-C#9: update to 1.5.0 must be rejected when the deployed image is 1.9.9"
  fi
  assert_equals "" "$calls" \
    "FIX2-C#9: a downgrade against the deployed version must be rejected before downloading"
)

test_preserve_compose_networks_keeps_existing_subnet
test_preserve_compose_networks_no_subnet_existing
test_activate_panel_update_files_preserves_subnet
test_get_deployed_panel_version_parses_image_tag
test_update_panel_crosschecks_deployed_image_version
test_install_flux_agent_keeps_service_when_download_fails
test_install_panel_refuses_existing_deployment
test_install_docker_rejects_unverified_script
test_download_verified_fail_closed_edges
test_install_script_respects_disabled_proxy
test_install_script_asks_for_proxy_config
test_install_script_recomputes_download_url_after_prompt
test_update_flux_agent_asks_for_proxy_config
test_update_flux_agent_skips_proxy_prompt_when_not_installed
test_flux_agent_health_ok_requires_stable_pid
test_update_flux_agent_rolls_back_flapping_binary
test_install_flux_agent_preserves_legacy_gost_when_download_fails
test_update_flux_agent_preserves_legacy_gost_when_download_fails
test_install_flux_agent_writes_json_safe_config
test_install_script_bootstraps_bash_for_alpine
test_install_flux_agent_uses_openrc
test_remove_flux_agent_service_uses_openrc
test_cleanup_legacy_gost_installation_removes_service_and_binary
test_cleanup_legacy_gost_installation_preserves_unrelated_gost
test_cleanup_legacy_gost_installation_skips_systemd_on_openrc
test_install_script_accepts_proxy_url_env_without_prompt
test_panel_install_script_can_disable_proxy
test_panel_install_script_recomputes_compose_urls_after_prompt
test_update_panel_asks_for_proxy_config
test_resolve_panel_deployment_uses_container_labels
test_validate_panel_update_environment_rejects_empty_required_values
test_backup_sqlite_for_update_pauses_copies_and_unpauses
test_panel_install_script_uses_default_proxy
test_panel_install_script_accepts_proxy_url_env_without_prompt
test_panel_install_script_defaults_proxy_on_eof

echo "install script tests passed"
