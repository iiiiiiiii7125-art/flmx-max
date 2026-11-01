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

  assert_equals "direct" "$(route_mode)" "install.sh should use direct mode when the proxy is disabled"

  ensure_download_routes

  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "direct mode should produce exactly one route"
  assert_equals "" "${DOWNLOAD_ROUTES[0]}" "direct mode route should be the empty (direct) prefix"
  assert_equals \
    "https://github.com/example/release" \
    "$(route_url "${DOWNLOAD_ROUTES[0]}" "https://github.com/example/release")" \
    "install.sh should bypass the proxy when disabled"
)

test_install_script_never_asks_for_proxy_config() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # 1.7.7: the interactive GitHub-proxy prompt is removed entirely;
  # routes come from the built-in candidate speed selection.
  if declare -F ask_proxy_config >/dev/null; then
    fail "install.sh should not define ask_proxy_config anymore"
  fi
  if declare -F maybe_proxy_url >/dev/null; then
    fail "install.sh should not define maybe_proxy_url anymore"
  fi

  PROXY_ENABLED=""
  PROXY_URL=""

  assert_equals "builtin" "$(route_mode)" "install.sh should default to built-in route selection"
)

test_install_script_builds_canonical_download_url() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_URL=""

  ensure_download_url_initialized

  local expected
  expected=$(build_download_url)

  assert_equals \
    "$expected" \
    "$DOWNLOAD_URL" \
    "install.sh should keep the canonical download URL (route prefixes apply at download time)"
)

test_update_flux_agent_downloads_without_prompt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"

  local cleanup_called="0"

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

  assert_equals "1" "$cleanup_called" "update_flux_agent should clean up legacy gost before restarting the agent"
  assert_equals "$(build_download_url)" "$DOWNLOAD_URL" "update_flux_agent should build the canonical download URL"
)

test_update_flux_agent_skips_proxy_prompt_when_not_installed() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  INSTALL_DIR=$(mktemp -u)

  local rc="0"
  update_flux_agent >/dev/null || rc="$?"

  assert_equals "1" "$rc" "update_flux_agent should fail when the agent is not installed"
)

# R8-J-B: 更新成功后必须打印与安装路径同口径的上线确认指引
# （回面板确认节点在线 + journalctl 日志指针）——服务重启只证明
# 进程已启动，不代表已连上面板。旧实现仅一行「更新完成」断言，
# 本测试对旧代码为红。
test_update_flux_agent_prints_online_confirmation_guidance() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"

  ask_proxy_config() {
    PROXY_ENABLED="false"
    DOWNLOAD_URL=""
  }
  cleanup_legacy_gost_installation() { :; }
  check_and_install_tcpkill() { :; }
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

  local out_log
  out_log=$(mktemp)
  update_flux_agent >"$out_log" 2>&1

  grep -q "更新完成" "$out_log" || fail "R8-J-B: update should still print its completion line"
  grep -q "节点管理" "$out_log" && grep -q "在线" "$out_log" || \
    fail "R8-J-B: update completion must tell the operator to confirm the node is online in the panel"
  grep -q "journalctl -u flux_agent" "$out_log" || \
    fail "R8-J-B: update completion must include the journalctl log pointer"
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

# X-P2-12 (J-02): extract_leading_semver 与 Go 侧 extractLeadingSemver
# 同规则：真实 -V 形态（gost 前缀）、fullVersion 形态、v 前缀、dev
# 透传；粘连后缀与垃圾串提取失败（调用方按不可比放行）。
test_extract_leading_semver_matches_go_rules() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  assert_equals "1.3.4" "$(extract_leading_semver 'gost 1.3.4 (go1.25 linux/amd64)')" \
    "extract_leading_semver should read the version from real -V output"
  assert_equals "1.3.0" "$(extract_leading_semver '1.3.0 (debian/amd64)')" \
    "extract_leading_semver should read fullVersion form"
  assert_equals "1.3.2" "$(extract_leading_semver 'v1.3.2')" \
    "extract_leading_semver should strip a leading v"
  assert_equals "dev" "$(extract_leading_semver 'dev')" \
    "extract_leading_semver should pass dev through"
  if extract_leading_semver 'old version' >/dev/null; then
    fail "extract_leading_semver should fail on unparseable output"
  fi
  if extract_leading_semver '1.3.2-beta1' >/dev/null; then
    fail "extract_leading_semver should fail on a glued suffix"
  fi
  if extract_leading_semver '' >/dev/null; then
    fail "extract_leading_semver should fail on empty input"
  fi
)

# X-P2-12 (J-02, BC-09): 现装 1.3.4、目标 1.3.2 的脚本更新必须在
# 下载前被拒，现装二进制不变、curl 零调用。
test_update_flux_agent_rejects_downgrade() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi
echo "old good"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"

  local state_dir
  state_dir=$(mktemp -d)
  STATE_DIR="$state_dir"

  ask_proxy_config() { PROXY_ENABLED="false"; DOWNLOAD_URL=""; }
  ensure_download_url_initialized() {
    RESOLVED_VERSION="1.3.2"
    DOWNLOAD_URL="https://example.com/gost"
  }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() { :; }
  systemctl() { return 0; }
  curl() { echo called >> "$STATE_DIR/curl-called"; return 0; }

  local rc="0"
  update_flux_agent >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P2-12: downgrade update should be rejected"
  [[ ! -f "$STATE_DIR/curl-called" ]] || \
    fail "X-P2-12: downgrade must be rejected before any download"
  grep -q "1.3.4" "$INSTALL_DIR/flux_agent" || \
    fail "X-P2-12: installed binary must be unchanged after a rejected downgrade"
)

# X-P2-12 防过修：目标更高时正常更新；现装版本不可比时按
# panel_version_cmp 既有口径放行（V-19）。
test_update_flux_agent_allows_higher_and_incomparable() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"

  run_update_case() {
    local installed_body="$1" target="$2"
    INSTALL_DIR=$(mktemp -d)
    FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
    : > "$FLUX_AGENT_SYSTEMD_SERVICE_FILE"
    printf '%s\n' "$installed_body" > "$INSTALL_DIR/flux_agent"
    chmod +x "$INSTALL_DIR/flux_agent"
    TARGET_VERSION="$target"

    ask_proxy_config() { PROXY_ENABLED="false"; DOWNLOAD_URL=""; }
    ensure_download_url_initialized() {
      RESOLVED_VERSION="$TARGET_VERSION"
      DOWNLOAD_URL="https://example.com/gost"
    }
    check_and_install_tcpkill() { :; }
    cleanup_legacy_gost_installation() { :; }
    sleep() { :; }
    systemctl() {
      if [[ "$*" == *MainPID* ]]; then echo 4242; fi
      return 0
    }
    curl() {
      local output="" url="" prev=""
      for a in "$@"; do
        if [[ "$prev" == "-o" ]]; then output="$a"; fi
        case "$a" in http*) url="$a";; esac
        prev="$a"
      done
      local body="#!/bin/bash
if [[ \"\${1:-}\" == \"-V\" ]]; then echo \"gost $TARGET_VERSION (go1.25 linux/amd64)\"; exit 0; fi
echo new"
      if [[ "$url" == *.sha256 ]]; then
        printf '%s\n' "$body" | sha256sum | awk '{print $1"  gost"}' > "$output"
        return 0
      fi
      printf '%s\n' "$body" > "$output"; chmod +x "$output"
    }

    local rc="0"
    update_flux_agent >/dev/null 2>&1 || rc="$?"
    assert_equals "0" "$rc" "X-P2-12: update to $target should succeed"
    grep -q "$target" "$INSTALL_DIR/flux_agent" || \
      fail "X-P2-12: binary should be at $target after update"
    [[ ! -f "$INSTALL_DIR/flux_agent.bak" ]] || \
      fail "X-P2-12: .bak should be removed after a successful update"
  }

  # 现装 1.3.4 → 目标 1.3.5（更高）
  run_update_case '#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi
echo old' "1.3.5"
  # 现装不可比（旧桩形态）→ 目标 1.3.2 仍放行
  run_update_case '#!/bin/bash
echo "old version"' "1.3.2"
)

# X-P3-6 (J-03, BC-12): main 各分支必须传播操作函数返回码。
# 夹具：截断 install.sh 的主调用、覆写操作函数与 delete_self 后
# 以子进程实跑 main——失败分支退出码非 0、成功分支仍为 0，
# 直装（-a/-s）与菜单两条路径都覆盖。
test_main_propagates_operation_exit_codes() (
  set -euo pipefail

  local work
  work=$(mktemp -d)

  build_case() {
    local name="$1" stub="$2"
    local file="$work/$name.sh"
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$file"
    cat >> "$file" <<STUBEOF
$stub
delete_self() { :; }
require_root_privileges() { return 0; }
main
STUBEOF
    printf '%s' "$file"
  }

  local rc="0" file

  # 菜单 2 更新失败 → 非 0
  file=$(build_case menu-update-fail 'update_flux_agent() { return 1; }')
  rc="0"; printf '2\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: menu update failure should exit non-zero"

  # 菜单 2 更新成功 → 0
  file=$(build_case menu-update-ok 'update_flux_agent() { return 0; }')
  rc="0"; printf '2\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "X-P3-6: menu update success should exit 0"

  # 菜单 1 安装失败 → 非 0
  file=$(build_case menu-install-fail 'install_flux_agent() { return 1; }')
  rc="0"; printf '1\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: menu install failure should exit non-zero"

  # 菜单 3 卸载失败 → 非 0
  file=$(build_case menu-uninstall-fail 'uninstall_flux_agent() { return 1; }')
  rc="0"; printf '3\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: menu uninstall failure should exit non-zero"

  # 直装（-a/-s）失败 → 非 0
  file=$(build_case direct-fail 'install_flux_agent() { return 1; }')
  rc="0"; bash "$file" -a "panel.example.com:443" -s "test-secret" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: direct install failure should exit non-zero"

  # 直装（-a/-s）成功 → 0
  file=$(build_case direct-ok 'install_flux_agent() { return 0; }')
  rc="0"; bash "$file" -a "panel.example.com:443" -s "test-secret" >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "X-P3-6: direct install success should exit 0"
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
  # R4B11-003: 安装路径判活改走 flux_agent_health_ok 双采样后，
  # systemctl 桩须能答 MainPID（恒定非 0），sleep 桩空转避免真实等待。
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
  # R4B11-003: 安装判活改双采样后，OpenRC 侧主 PID 取自 pidfile，
  # 须预置恒定 PID 供两次采样比对。
  FLUX_AGENT_OPENRC_PIDFILE="$temp_root/flux_agent.pid"
  echo 4242 > "$FLUX_AGENT_OPENRC_PIDFILE"
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
  sleep() { :; }

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

  assert_equals "override" "$(route_mode)" "install.sh should treat PROXY_URL as a manual route override"
  assert_equals "mirror.example.com" "$PROXY_URL" "install.sh should preserve PROXY_URL when provided via env"

  ensure_download_routes

  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "override mode should produce exactly one route"
  assert_equals "https://mirror.example.com" "${DOWNLOAD_ROUTES[0]}" "install.sh should normalize the override prefix"
  assert_equals \
    "https://mirror.example.com/https://github.com/example/release" \
    "$(route_url "${DOWNLOAD_ROUTES[0]}" "https://github.com/example/release")" \
    "install.sh should apply the override prefix at request time"
)

test_panel_install_script_can_disable_proxy() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED="false"
  PROXY_URL=""

  assert_equals "direct" "$(route_mode)" "panel_install.sh should use direct mode when the proxy is disabled"

  ensure_download_routes

  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "direct mode should produce exactly one route"
  assert_equals \
    "https://github.com/example/release" \
    "$(route_url "${DOWNLOAD_ROUTES[0]}" "https://github.com/example/release")" \
    "panel_install.sh should bypass the proxy after disabling it"
)

test_panel_install_script_builds_canonical_compose_urls() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOCKER_COMPOSEV4_URL=""
  DOCKER_COMPOSEV6_URL=""

  ensure_compose_urls_initialized

  assert_equals \
    "https://github.com/${REPO}/releases/download/${RESOLVED_VERSION}/docker-compose-v4.yml" \
    "$DOCKER_COMPOSEV4_URL" \
    "panel_install.sh should build the canonical compose URL (route prefixes apply at download time)"
  assert_equals \
    "https://github.com/${REPO}/releases/download/${RESOLVED_VERSION}/docker-compose-v6.yml" \
    "$DOCKER_COMPOSEV6_URL" \
    "panel_install.sh should build the canonical v6 compose URL"
)

test_update_panel_downloads_without_prompt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local calls=""

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

  assert_equals \
    "https://github.com/${REPO}/releases/download/v-test/docker-compose-v4.yml" \
    "$DOCKER_COMPOSEV4_URL" \
    "update_panel should build the canonical compose URL"
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

  assert_equals "override" "$(route_mode)" "panel_install.sh should treat PROXY_URL as a manual route override"
  assert_equals "mirror.example.com" "$PROXY_URL" "panel_install.sh should preserve PROXY_URL when provided via env"

  ensure_download_routes

  assert_equals "https://mirror.example.com" "${DOWNLOAD_ROUTES[0]}" "panel_install.sh should normalize the override prefix"
)

test_panel_install_script_ignores_eof_without_prompt() {
  local rc="0"
  local output
  local temp_script

  temp_script=$(mktemp)
  sed '/^# 执行主函数$/,$d' "$ROOT_DIR/panel_install.sh" > "$temp_script"
  cat >> "$temp_script" <<'EOF'
PROXY_ENABLED=""
PROXY_URL=""
printf '%s\n' "$(route_mode)"
EOF

  output=$(bash "$temp_script" < /dev/null) || rc="$?"
  rm -f "$temp_script"

  assert_equals "0" "$rc" "panel_install.sh should not fail on EOF without any prompt"
  assert_equals "builtin" "$output" "panel_install.sh should default to built-in route selection on EOF (no prompt, no implicit default proxy)"
}

test_panel_install_script_uses_builtin_routes_by_default() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  if declare -F ask_proxy_config >/dev/null; then
    fail "panel_install.sh should not define ask_proxy_config anymore"
  fi
  if declare -F maybe_proxy_url >/dev/null; then
    fail "panel_install.sh should not define maybe_proxy_url anymore"
  fi

  PROXY_ENABLED="true"
  PROXY_URL=""

  assert_equals "builtin" "$(route_mode)" "panel_install.sh should use built-in route selection when no override is given (no implicit default proxy)"
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
  # R8-J-C: install_panel 完成块前新增 wait_for_backend_healthy 把关，
  # 夹具以恒成功桩承接（本例验的是 F-049 守卫，不验健康等待本身）。
  wait_for_backend_healthy() { return 0; }
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

# R8-J-C: 全新安装的成功断言必须受后端健康等待把关——健康等待
# 失败时 install_panel 返回 1、不印「部署完成」与默认凭据、改印
# logs 指引；通过时才印成功块。旧实现 up -d 后直达成功块，
# 本测试对旧代码为红。
test_install_panel_waits_for_backend_health() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)

  ask_proxy_config() { :; }
  ensure_compose_urls_initialized() {
    DOCKER_COMPOSEV4_URL="https://example.com/docker-compose-v4.yml"
    DOCKER_COMPOSEV6_URL="https://example.com/docker-compose-v6.yml"
    return 0
  }
  check_docker() { DOCKER_CMD="docker compose"; return 0; }
  get_config_params() {
    FRONTEND_PORT=6366; BACKEND_PORT=6365; RESOLVED_VERSION="9.9.9"
    DB_TYPE="sqlite"; DATABASE_URL="file:/data/panel.db"
    POSTGRES_DB="flux_panel"; POSTGRES_USER="flux_panel"
    POSTGRES_PASSWORD="pw"; JWT_SECRET="secret"
  }
  get_docker_compose_url() { echo "https://example.com/docker-compose-v4.yml"; }
  download_verified() { return 0; }
  check_ipv6_support() { return 1; }
  configure_docker_ipv6() { return 0; }
  docker_ipv6_usable() { return 1; }
  get_public_ip() { echo "203.0.113.1"; }
  docker() { return 0; }

  # 健康等待失败：不得宣称部署完成
  wait_for_backend_healthy() { return 1; }
  local rc="0" out_log
  out_log="$work/fail.log"
  ( cd "$work" && install_panel >"$out_log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R8-J-C: install_panel must fail when the backend never becomes healthy"
  if grep -q "部署完成" "$out_log"; then
    fail "R8-J-C: success block must not be printed when the backend health gate fails"
  fi
  if grep -q "默认管理员账号" "$out_log"; then
    fail "R8-J-C: default credentials must not be printed when the backend health gate fails"
  fi
  grep -q "logs backend" "$out_log" || \
    fail "R8-J-C: health-gate failure must print a docker compose logs pointer"

  # 健康等待通过：正常印成功块
  wait_for_backend_healthy() { return 0; }
  rc="0"
  out_log="$work/ok.log"
  ( cd "$work" && install_panel >"$out_log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R8-J-C: install_panel should succeed once the backend is healthy"
  grep -q "部署完成" "$out_log" || \
    fail "R8-J-C: success block should be printed after the health gate passes"
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

# R8-J-A: .sha256 下载加有界重试。桩 curl 按 argv 中的 --retry N
# 模拟真实 curl 的传输层重试语义并计数：首次传输失败、第二次成功
# 时校验必须通过。基线代码无 --retry，同桩只有一次传输机会、必然
# 失败——本测试对旧代码为红。
test_verify_downloaded_binary_retries_transient_checksum_failure() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local state payload
  state=$(mktemp -d)
  payload="$state/bin.sh"
  printf '#!/bin/bash\necho payload\n' > "$payload"
  echo 0 > "$state/attempts"
  STATE_DIR="$state"
  PAYLOAD_FILE="$payload"

  curl() {
    local output="" url="" retries=0
    local args=("$@") i
    for ((i=0; i<${#args[@]}; i++)); do
      if [[ "${args[i]}" == "--retry" ]]; then retries="${args[i+1]:-0}"; fi
      if [[ "${args[i]}" == "-o" ]]; then output="${args[i+1]:-}"; fi
      case "${args[i]}" in http*) url="${args[i]}";; esac
    done
    if [[ "$url" == *.sha256 ]]; then
      local attempt=0 n
      while (( attempt <= retries )); do
        attempt=$((attempt+1))
        n=$(cat "$STATE_DIR/attempts"); n=$((n+1)); echo "$n" > "$STATE_DIR/attempts"
        if (( n < 2 )); then
          if (( attempt <= retries )); then continue; fi
          echo "curl: (22) The requested URL returned error: 503" >&2
          return 22
        fi
        sha256sum "$PAYLOAD_FILE" | awk '{print $1"  gost"}' > "$output"
        return 0
      done
    fi
    return 0
  }

  verify_downloaded_binary "$payload" "https://example.com/gost-amd64" >/dev/null 2>&1 || \
    fail "R8-J-A: verify_downloaded_binary should survive one transient checksum fetch failure"
  assert_equals "2" "$(cat "$STATE_DIR/attempts")" \
    "R8-J-A: checksum fetch should be retried after a transient failure"
)

# R8-J-A: 校验下载持续失败时仍 fail-closed（返回 1），但传输尝试
# 须达 1+3 次，且 curl 的错误信息必须露给用户（旧实现 2>/dev/null
# 全吞，用户只看到一句无诊断的拒绝）。
test_verify_downloaded_binary_surfaces_curl_error_after_retries() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local state payload
  state=$(mktemp -d)
  payload="$state/bin.sh"
  printf '#!/bin/bash\necho payload\n' > "$payload"
  echo 0 > "$state/attempts"
  STATE_DIR="$state"

  curl() {
    local url="" retries=0
    local args=("$@") i
    for ((i=0; i<${#args[@]}; i++)); do
      if [[ "${args[i]}" == "--retry" ]]; then retries="${args[i+1]:-0}"; fi
      case "${args[i]}" in http*) url="${args[i]}";; esac
    done
    if [[ "$url" == *.sha256 ]]; then
      local attempt=0 n
      while (( attempt <= retries )); do
        attempt=$((attempt+1))
        n=$(cat "$STATE_DIR/attempts"); n=$((n+1)); echo "$n" > "$STATE_DIR/attempts"
      done
      echo "curl: (7) Failed to connect R8JA_MARKER" >&2
      return 7
    fi
    return 0
  }

  local rc="0" out
  out=$(verify_downloaded_binary "$payload" "https://example.com/gost-amd64" 2>&1) || rc="$?"
  assert_equals "1" "$rc" "R8-J-A: persistent checksum fetch failure must stay fail-closed"
  assert_equals "4" "$(cat "$STATE_DIR/attempts")" \
    "R8-J-A: checksum fetch should attempt 1 + 3 retries before giving up"
  [[ "$out" == *"R8JA_MARKER"* ]] || \
    fail "R8-J-A: curl error output must be surfaced to the user, not swallowed"
)

# R8-J-A: panel 侧 download_verified 的 .sha256 腿同口径——首次
# 传输失败经重试恢复后校验通过、本体保留（旧实现单次尝试失败即
# 删除已下好的本体并终止整次操作）。
test_download_verified_retries_transient_checksum_failure() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local state dest
  state=$(mktemp -d)
  dest="$state/docker-compose.yml"
  echo 0 > "$state/attempts"
  STATE_DIR="$state"

  curl() {
    local output="" url="" retries=0
    local args=("$@") i
    for ((i=0; i<${#args[@]}; i++)); do
      if [[ "${args[i]}" == "--retry" ]]; then retries="${args[i+1]:-0}"; fi
      if [[ "${args[i]}" == "-o" ]]; then output="${args[i+1]:-}"; fi
      case "${args[i]}" in http*) url="${args[i]}";; esac
    done
    if [[ "$url" == *.sha256 ]]; then
      local attempt=0 n
      while (( attempt <= retries )); do
        attempt=$((attempt+1))
        n=$(cat "$STATE_DIR/attempts"); n=$((n+1)); echo "$n" > "$STATE_DIR/attempts"
        if (( n < 2 )); then
          if (( attempt <= retries )); then continue; fi
          echo "curl: (22) The requested URL returned error: 503" >&2
          return 22
        fi
        printf 'payload' | sha256sum | awk '{print $1"  docker-compose.yml"}' > "$output"
        return 0
      done
    fi
    printf 'payload' > "$output"
    return 0
  }

  FLMX_VERIFY_DOWNLOADS=1 download_verified "https://example.com/docker-compose-v4.yml" "$dest" >/dev/null 2>&1 || \
    fail "R8-J-A: download_verified should survive one transient checksum fetch failure"
  assert_equals "2" "$(cat "$STATE_DIR/attempts")" \
    "R8-J-A: panel checksum fetch should be retried after a transient failure"
  [[ -f "$dest" ]] || fail "R8-J-A: verified body must be kept after a retried checksum fetch"
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

# R2X-019 (R2-J-01): 真实函数级回归——install_flux_agent 本体实跑
# （下载/校验/写服务/启服务全走真实代码，curl/systemctl 为命令级桩），
# 仅桩 flux_agent_service_is_active 决定判活结果：为假时函数必须
# return 1 且屏显失败；为真时必须 return 0。禁止再以整函数桩代替
# 被测对象（J 报告 §3：X-P3-6 的整函数桩正是本缺陷的漏网原因）。
test_install_flux_agent_returns_nonzero_when_service_not_active() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
  SERVER_ADDR="panel.example.com:443"
  SECRET="secret"
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

  # 判活失败：下载与启动命令均成功、服务就是没活起来
  flux_agent_service_is_active() { return 1; }

  local rc="0"
  ( install_flux_agent >"$INSTALL_DIR/fail.log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R2X-019: install must return 1 when the service fails to start"
  grep -q "服务启动失败" "$INSTALL_DIR/fail.log" || \
    fail "R2X-019: install should still print the startup failure hint"

  # 判活成功（防过修）：同路径必须 return 0。
  # R4B11-003: 判活改双采样后还须主 PID 恒定非 0，一并覆写采样源
  # （本例 systemctl 桩不答 MainPID）。
  flux_agent_service_is_active() { return 0; }
  flux_agent_main_pid() { echo 4242; }

  rc="0"
  ( install_flux_agent >"$INSTALL_DIR/ok.log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R2X-019: install must return 0 when the service is active"
)

# R2X-020 (R2-J-02): systemd 分支的 start 前必须先 reset-failed 清
# 失败态（与 agent 侧 scriptStartCommand / gate_resetfailed_test 同
# 口径）；OpenRC 无启动限速、分支不得出现 reset-failed/systemctl。
test_systemd_starts_reset_failed_before_start() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local work
  work=$(mktemp -d)
  CALLS_LOG="$work/calls.log"
  RC_CALLS_LOG="$work/rc-calls.log"
  : > "$CALLS_LOG"
  : > "$RC_CALLS_LOG"

  SERVICE_MANAGER="systemd"
  systemctl() { echo "$*" >> "$CALLS_LOG"; return 0; }

  start_flux_agent_service
  assert_equals "reset-failed flux_agent" "$(sed -n '1p' "$CALLS_LOG")" \
    "R2X-020: start_flux_agent_service should reset-failed before start"
  assert_equals "start flux_agent" "$(sed -n '2p' "$CALLS_LOG")" \
    "R2X-020: start_flux_agent_service should start after reset-failed"

  : > "$CALLS_LOG"
  enable_and_start_flux_agent_service
  local reset_line start_line
  reset_line=$(awk '$0=="reset-failed flux_agent"{print NR; exit}' "$CALLS_LOG")
  start_line=$(awk '$0=="start flux_agent"{print NR; exit}' "$CALLS_LOG")
  [[ -n "$reset_line" && -n "$start_line" && "$reset_line" -lt "$start_line" ]] || \
    fail "R2X-020: enable_and_start should reset-failed before start (calls: $(cat "$CALLS_LOG"))"

  # OpenRC 分支不动：不得调用 systemctl，rc-service 照常 start
  SERVICE_MANAGER="openrc"
  : > "$CALLS_LOG"
  rc-service() { echo "$*" >> "$RC_CALLS_LOG"; return 0; }
  rc-update() { echo "$*" >> "$RC_CALLS_LOG"; return 0; }
  start_flux_agent_service
  enable_and_start_flux_agent_service
  [[ ! -s "$CALLS_LOG" ]] || \
    fail "R2X-020: OpenRC starts must not call systemctl/reset-failed"
  grep -q "flux_agent start" "$RC_CALLS_LOG" || \
    fail "R2X-020: OpenRC starts should still go through rc-service"
)

# R2X-020 (R2-J-02) + 观察 O-1: systemctl 桩模拟 failed 态——裸 start
# 报启动限速错、reset-failed 后 start 成功。回滚恢复旧二进制后必须
# 终态 active（双采样判活），而不是停在限速拒绝上待人工处理。
test_rollback_restarts_service_from_failed_state() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  printf '#!/bin/bash\necho "bad flap"\n' > "$INSTALL_DIR/flux_agent"
  printf '#!/bin/bash\necho "old good"\n' > "$INSTALL_DIR/flux_agent.bak"
  chmod +x "$INSTALL_DIR/flux_agent" "$INSTALL_DIR/flux_agent.bak"

  local work
  work=$(mktemp -d)
  RB_STATE="$work"
  # flap 坏二进制已把 unit 烧进 failed 态且服务未活
  : > "$RB_STATE/failed"

  sleep() { :; }
  systemctl() {
    case "$1" in
      reset-failed)
        rm -f "$RB_STATE/failed"
        return 0 ;;
      start)
        if [[ -f "$RB_STATE/failed" ]]; then
          echo "Start request repeated too quickly" >&2
          return 1
        fi
        : > "$RB_STATE/active"
        return 0 ;;
      is-active)
        [[ -f "$RB_STATE/active" ]] ;;
      show)
        if [[ -f "$RB_STATE/active" ]]; then echo 4242; else echo 0; fi
        return 0 ;;
      *) return 0 ;;
    esac
  }

  # || true：install.sh 本体无 set -e，start 失败应由后续判活断言
  # 定性，不得让套件子 shell 的 set -e 在回滚函数内静默中止。
  rollback_flux_agent >"$work/rollback.log" 2>&1 || true

  flux_agent_service_is_active || \
    fail "R2X-020: rollback should bring the service back active from the failed state"
  grep -q "已回滚到旧版本并重启成功" "$work/rollback.log" || \
    fail "R2X-020: rollback should report a successful restart (log: $(cat "$work/rollback.log"))"
  grep -q "old good" "$INSTALL_DIR/flux_agent" || \
    fail "R2X-020: rollback should restore the old binary"
)

# 1.7.7 路线机制（install.sh）：直连探测通过 → 路线表只有直连，
# 不得再测任何代理（curl 桩对带代理前缀的探测一律失败也不影响）。
test_install_script_routes_direct_first() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *ghfast.top*|*gh-proxy.com*|*ghproxy*|*gitmirror*|*ghps.cc*|*gh.llkk.cc*|*ghp.ci*) return 22 ;;
          *) printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "direct-first: a healthy direct route must short-circuit proxy probing"
  assert_equals "" "${DOWNLOAD_ROUTES[0]}" "direct-first: the only route should be direct"
  assert_equals "1" "$ROUTES_STAGE" "direct-first: stage should stay 1 until a download fails"
)

# 1.7.7 路线机制（install.sh）：直连判死 → 并行探测代理按延迟排名
# （ghps 0.05 < ghfast 0.30，其余判死按清单原序殿后），直连排最末。
test_install_script_routes_rank_proxies_when_direct_dead() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *ghps.cc*) printf '0.050 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *ghfast.top*) printf '0.300 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "10" "${#DOWNLOAD_ROUTES[@]}" "direct-dead: routes should be 9 proxies + direct fallback"
  assert_equals "https://ghps.cc" "${DOWNLOAD_ROUTES[0]}" "direct-dead: fastest probed proxy should rank first"
  assert_equals "https://ghfast.top" "${DOWNLOAD_ROUTES[1]}" "direct-dead: second fastest probed proxy should rank second"
  assert_equals "https://gcode.hostcentral.cc" "${DOWNLOAD_ROUTES[2]}" "direct-dead: unprobed-dead proxies should follow in canonical order"
  assert_equals "" "${DOWNLOAD_ROUTES[9]}" "direct-dead: direct should be the last-resort route"
)

# 1.7.7 路线机制（install.sh）：直连已定（阶段 1）后二进制下载失败
# → 补测代理排名续切（extend），经 ghfast 下载成功且校验通过。
test_install_script_download_failover_extends_routes() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/v-test/gost-amd64"

  curl() {
    local all_args="$*"
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
    # 探测请求带 -w 输出格式串（url 变量可能被格式串污染）：以整串判别
    case "$all_args" in
      *url_effective*)
        case "$all_args" in
          *ghfast.top*) printf '0.200 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *" https://github.com/"*) printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        return 0
        ;;
    esac
    if [[ "$url" == *.sha256 ]]; then
      printf '#!/bin/bash\necho "new version"\n' | sha256sum | awk '{print $1"  gost-amd64.sha256"}' > "$output"
      return 0
    fi
    # 二进制本体：直连下载失败、ghfast 路线成功
    case "$url" in
      https://ghfast.top/*)
        cat > "$output" <<'EOF'
#!/bin/bash
echo "new version"
EOF
        chmod +x "$output"
        return 0
        ;;
      *) return 22 ;;
    esac
  }

  local dest
  dest=$(mktemp)
  rm -f "$dest"

  download_agent_binary "$dest"

  grep -q "new version" "$dest" || fail "failover: binary should arrive via the ghfast route after direct fails"
  [[ ${#DOWNLOAD_ROUTES[@]} -gt 1 ]] || fail "failover: routes should have been extended with probed proxies"
  assert_equals "https://ghfast.top" "${DOWNLOAD_ROUTES[1]}" "failover: first extension route should be the fastest probed proxy"
)

# 1.7.7 路线机制（panel_install.sh）：download_verified_routed 按
# 路线序续切——直连判死、ghps 探测最快且下载成功，其余路线不得
# 被尝试（download_verified 桩记录调用并按前缀决定成败）。
test_panel_download_verified_routed_failover() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  local calls_log
  calls_log=$(mktemp)

  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *ghps.cc*) printf '0.050 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  download_verified() {
    echo "$1" >> "$calls_log"
    case "$1" in
      https://ghps.cc/*) return 0 ;;
      *) return 1 ;;
    esac
  }

  local dest
  dest=$(mktemp)
  download_verified_routed "https://github.com/example/asset.yml" "$dest"

  assert_equals \
    "https://ghps.cc/https://github.com/example/asset.yml" \
    "$(head -n1 "$calls_log")" \
    "routed download should try the fastest probed proxy first when direct is dead"
  assert_equals "1" "$(wc -l < "$calls_log")" "routed download should stop at the first successful route"
)

test_preserve_compose_networks_keeps_existing_subnet
test_preserve_compose_networks_no_subnet_existing
test_activate_panel_update_files_preserves_subnet
test_get_deployed_panel_version_parses_image_tag
test_update_panel_crosschecks_deployed_image_version
test_install_flux_agent_keeps_service_when_download_fails
test_install_panel_refuses_existing_deployment
test_install_panel_waits_for_backend_health
test_install_docker_rejects_unverified_script
test_download_verified_fail_closed_edges
test_verify_downloaded_binary_retries_transient_checksum_failure
test_verify_downloaded_binary_surfaces_curl_error_after_retries
test_download_verified_retries_transient_checksum_failure
# R4B11-001/002: 菜单 5/6 的 Caddyfile 行为干跑（CADDYFILE_PATH 钩子，
# 此前 shipped 夹具对该钩子零覆盖）。桩口径：caddy validate 与
# systemctl restart 各由标志文件控制成败，is-active 恒真，getent 恒
# 解析成功，sleep 空转；Caddyfile 全程落在临时目录，不碰真实 /etc。
panel_caddyfile_fixture_env() {
  FIXTURE_DIR=$(mktemp -d)
  export CADDYFILE_PATH="$FIXTURE_DIR/Caddyfile"
  FRONTEND_PORT=6366
  VALIDATE_FAIL_FLAG="$FIXTURE_DIR/validate-fail"
  RESTART_FAIL_FLAG="$FIXTURE_DIR/restart-fail"
  ISACTIVE_FAIL_FLAG="$FIXTURE_DIR/isactive-fail"
  sleep() { :; }
  getent() { printf '203.0.113.10 %s\n' "$2"; }
  caddy() {
    if [[ -f "$VALIDATE_FAIL_FLAG" ]]; then
      return 1
    fi
    return 0
  }
  systemctl() {
    if [[ "$1" == "restart" && -f "$RESTART_FAIL_FLAG" ]]; then
      return 1
    fi
    if [[ "$1" == "is-active" && -f "$ISACTIVE_FAIL_FLAG" ]]; then
      return 1
    fi
    return 0
  }
}

# R4B11-001: 先菜单 6 追加订阅前置块、再重跑菜单 5 时，前置块必须被
# 保留（旧实现整文件覆盖、无声抹除）；无关站点块同样保留，面板块在
# 同域名重配后恰一份且有保留提示。旧代码对本用例必红（sub 块消失）。
test_panel_menu5_preserves_front_block_after_menu6() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "R4B11-001: initial menu 5 run should succeed"
  grep -q "^panel.example.com {" "$CADDYFILE_PATH" || \
    fail "R4B11-001: panel block missing after menu 5"

  setup_subscription_front_domain <<< "sub.example.com" >/dev/null || \
    fail "R4B11-001: menu 6 run should succeed"
  grep -q "^sub.example.com {" "$CADDYFILE_PATH" || \
    fail "R4B11-001: front block missing after menu 6"

  cat >> "$CADDYFILE_PATH" <<'EOF'
other.example.com {
    respond "hi"
}
EOF

  local out_log="$FIXTURE_DIR/menu5-rerun.log"
  setup_domain_proxy <<< "panel.example.com" >"$out_log" 2>&1 || \
    fail "R4B11-001: menu 5 re-run should succeed"
  grep -q "^sub.example.com {" "$CADDYFILE_PATH" || \
    fail "R4B11-001: menu 5 re-run must preserve the subscription front block"
  grep -q "^other.example.com {" "$CADDYFILE_PATH" || \
    fail "R4B11-001: menu 5 re-run must preserve unrelated site blocks"
  assert_equals "1" "$(grep -c '^panel.example.com {' "$CADDYFILE_PATH")" \
    "R4B11-001: panel block must appear exactly once after re-run"
  grep -q "已保留" "$out_log" || \
    fail "R4B11-001: menu 5 re-run should report the preserved blocks"
)

# R4B11-002（菜单 5 validate 腿）：菜单 5 写文件后 caddy validate 失败
# 必须回滚备份、返回 1，文件与写前逐字一致（旧实现无 validate、直达
# restart，本用例对旧代码必红）。
test_panel_menu5_rolls_back_on_validate_failure() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "R4B11-002: initial menu 5 run should succeed"
  setup_subscription_front_domain <<< "sub.example.com" >/dev/null || \
    fail "R4B11-002: menu 6 run should succeed"
  cp "$CADDYFILE_PATH" "$FIXTURE_DIR/expected-good"

  : > "$VALIDATE_FAIL_FLAG"
  local rc="0"
  setup_domain_proxy <<< "panel.example.com" >"$FIXTURE_DIR/fail.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R4B11-002: menu 5 must fail when caddy validate fails"
  diff -q "$FIXTURE_DIR/expected-good" "$CADDYFILE_PATH" >/dev/null || \
    fail "R4B11-002: menu 5 must roll back the Caddyfile when validate fails"
  grep -q "校验失败" "$FIXTURE_DIR/fail.log" || \
    fail "R4B11-002: validate failure must be reported"
)

# R4B11-002（菜单 5 restart 腿）：restart 返回非 0 时不得被 set -e 在
# is-active 检查前吞掉——必须回滚并落到 journalctl 日志指引分支。
test_panel_menu5_restart_failure_rolls_back_with_guidance() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "R4B11-002: initial menu 5 run should succeed"
  cp "$CADDYFILE_PATH" "$FIXTURE_DIR/expected-good"

  : > "$RESTART_FAIL_FLAG"
  local rc="0" log="$FIXTURE_DIR/restart-fail.log"
  setup_domain_proxy <<< "panel.example.com" >"$log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R4B11-002: menu 5 must fail when caddy restart fails"
  grep -q "journalctl -u caddy" "$log" || \
    fail "R4B11-002: menu 5 restart failure must reach the journalctl guidance branch"
  diff -q "$FIXTURE_DIR/expected-good" "$CADDYFILE_PATH" >/dev/null || \
    fail "R4B11-002: menu 5 must roll back the Caddyfile when restart fails"
)

# R4B11-002（菜单 6 restart 腿）：菜单 6 的 restart 同样加守卫——失败
# 时回滚追加并给出日志指引（旧实现该指引在 set -e 下不可达）。
test_panel_menu6_restart_failure_rolls_back_with_guidance() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "R4B11-002: initial menu 5 run should succeed"
  cp "$CADDYFILE_PATH" "$FIXTURE_DIR/expected-good"

  : > "$RESTART_FAIL_FLAG"
  local rc="0" log="$FIXTURE_DIR/restart-fail.log"
  setup_subscription_front_domain <<< "sub.example.com" >"$log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R4B11-002: menu 6 must fail when caddy restart fails"
  grep -q "journalctl -u caddy" "$log" || \
    fail "R4B11-002: menu 6 restart failure must reach the journalctl guidance branch"
  diff -q "$FIXTURE_DIR/expected-good" "$CADDYFILE_PATH" >/dev/null || \
    fail "R4B11-002: menu 6 must roll back the appended block when restart fails"
)

# R4B11-003: 安装路径判活改双采样后，flap 服务（第一次采样活、第二
# 次已死）必须判安装失败且不印「安装完成」（旧实现单次采样必红）；
# 恒活对照仍成功。
test_install_flux_agent_rejects_flapping_service() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
  SERVER_ADDR="panel.example.com:443"
  SECRET="secret"
  DOWNLOAD_URL="https://example.com/gost"

  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() { :; }
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

  local state_dir
  state_dir=$(mktemp -d)
  echo 0 > "$state_dir/active_cnt"
  STATE_DIR="$state_dir"
  flux_agent_service_is_active() {
    local n
    n=$(cat "$STATE_DIR/active_cnt"); n=$((n+1)); echo "$n" > "$STATE_DIR/active_cnt"
    [[ "$n" -le 1 ]]
  }

  local rc="0" log="$INSTALL_DIR/install.log"
  ( install_flux_agent >"$log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R4B11-003: install must fail when the service flaps after start"
  if grep -q "安装完成" "$log"; then
    fail "R4B11-003: install must not print completion for a flapping service"
  fi

  flux_agent_service_is_active() { return 0; }
  rc="0"
  ( install_flux_agent >"$log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R4B11-003: install should succeed when the service stays healthy"
  grep -q "安装完成" "$log" || \
    fail "R4B11-003: healthy install should print completion"
)

# R4B11-005: delete_self 只在成功路径执行且文案与成败一致。夹具以
# 子进程实跑 main（保留真实 delete_self）：失败时退出码非 0、脚本
# 文件保留、无「操作已完成」；成功时脚本自删且有完成文案。
test_main_delete_self_only_on_success() (
  set -euo pipefail

  local work
  work=$(mktemp -d)

  build_case() {
    local name="$1" stub="$2"
    local file="$work/$name.sh"
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$file"
    cat >> "$file" <<STUBEOF
$stub
require_root_privileges() { return 0; }
main
STUBEOF
    printf '%s' "$file"
  }

  local rc="0" file out

  file=$(build_case del-fail 'install_flux_agent() { return 1; }')
  rc="0"; out=$(printf '1\n' | bash "$file" 2>&1) || rc="$?"
  assert_equals "1" "$rc" "R4B11-005: failed install should exit non-zero"
  [[ -f "$file" ]] || fail "R4B11-005: script must be kept after a failed operation"
  if [[ "$out" == *"操作已完成"* ]]; then
    fail "R4B11-005: failure path must not print 操作已完成"
  fi
  [[ "$out" == *"已保留"* ]] || \
    fail "R4B11-005: failure path should say the script was kept for retry"

  file=$(build_case del-ok 'install_flux_agent() { return 0; }')
  rc="0"; out=$(printf '1\n' | bash "$file" 2>&1) || rc="$?"
  assert_equals "0" "$rc" "R4B11-005: successful install should exit 0"
  [[ ! -f "$file" ]] || fail "R4B11-005: script should delete itself after success"
  [[ "$out" == *"操作已完成"* ]] || \
    fail "R4B11-005: success path should print the completion line"
)

# R4B11-004: main 的 root 前置检查。EUID 只读、夹具内无法伪造，
# root 环境（本地沙箱）下跳过；非 root 环境（CI）实测 fail-fast。
test_main_requires_root() (
  set -euo pipefail
  if [[ $EUID -eq 0 ]]; then
    echo "skip: test_main_requires_root needs a non-root environment"
    return 0
  fi
  local file rc="0"
  file=$(mktemp)
  sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$file"
  printf 'main\n' >> "$file"
  bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R4B11-004: main must fail fast when not run as root"
)

# R4B11-006: 交互式密钥读取必须关回显；迁移 PG 口令不得再作为
# docker run 的 pgloader 进程参数出现（改走只读挂载的命令文件）。
test_script_secret_surfaces_closed() (
  set -euo pipefail
  grep -Fq 'read -s -p "密钥: "' "$ROOT_DIR/install.sh" || \
    fail "R4B11-006: interactive secret prompt must use read -s"
  if grep -Fq 'read -p "密钥: "' "$ROOT_DIR/install.sh"; then
    fail "R4B11-006: the echoing secret prompt must not remain"
  fi
  if grep -F 'pgloader "$pgloader_source" "postgresql://' "$ROOT_DIR/panel_install.sh" >/dev/null; then
    fail "R4B11-006: pgloader target URL carrying the password must not be a docker run argument"
  fi
  grep -Fq 'flmx-migrate.load' "$ROOT_DIR/panel_install.sh" || \
    fail "R4B11-006: pgloader should receive its target via the mounted command file"
  grep -Fq 'chmod 600 "$pgloader_load_file"' "$ROOT_DIR/panel_install.sh" || \
    fail "R4B11-006: the pgloader command file must be created with 0600 permissions"
)

# F-R4-01: pgloader 命令文件的 LOAD DATABASE 语句必须以分号终止——
# 缺失时现行 dimitri/pgloader:latest（3.6.7~devel）解析 KABOOM
# （ESRAP-PARSE-ERROR）、SQLite→PostgreSQL 迁移整体不可用。本用例
# 从出厂脚本抽出命令文件生成段原文实际执行，钉住生成文件的确切
# 形态与 0600 权限（旧形态无分号，本用例对旧代码必红）。
test_pgloader_command_file_terminated() (
  set -euo pipefail
  local work snippet expected actual
  work=$(mktemp -d)
  snippet="$work/generate.sh"
  awk '/pgloader_load_file=\$\(mktemp\)/,/^EOF$/' "$ROOT_DIR/panel_install.sh" > "$snippet"
  grep -q 'INTO postgresql://' "$snippet" || \
    fail "F-R4-01: could not extract the pgloader command-file generation block from panel_install.sh"

  pgloader_source="/sqlite/gost.db"
  postgres_user="fixture_user"
  postgres_password="fixture_pass"
  postgres_db="fixture_db"
  source "$snippet"

  [[ -f "$pgloader_load_file" ]] || \
    fail "F-R4-01: the generation block must produce the pgloader command file"
  assert_equals "600" "$(stat -c %a "$pgloader_load_file")" \
    "F-R4-01: the pgloader command file must keep 0600 permissions"
  expected=$(printf 'LOAD DATABASE\n     FROM sqlite:///sqlite/gost.db\n     INTO postgresql://fixture_user:fixture_pass@postgres:5432/fixture_db;')
  actual=$(cat "$pgloader_load_file")
  assert_equals "$expected" "$actual" \
    "F-R4-01: the pgloader command file must be one LOAD DATABASE statement terminated by a semicolon"
  rm -f "$pgloader_load_file"
  rm -rf "$work"
)

# R4B11-007: tcpkill 安装失败不得静默——check_and_install_tcpkill 在
# 尝试后复检，缺失时必须给出点名受影响能力的告警（旧实现全部输出
# 吞 /dev/null 且恒 return 0、无任何复检，本用例对旧代码必红）。
test_tcpkill_missing_warns_instead_of_silent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  attempt_install_tcpkill() { :; }
  local out rc="0"
  out=$(check_and_install_tcpkill 2>&1)
  if command -v tcpkill >/dev/null 2>&1; then
    [[ -z "$out" ]] || fail "R4B11-007: no warning expected when tcpkill is present"
  else
    [[ "$out" == *"tcpkill"* && "$out" == *"强制断开"* ]] || \
      fail "R4B11-007: missing tcpkill must produce an explicit warning naming the affected capability"
  fi

  rc="0"
  verify_tcpkill_available >/dev/null 2>&1 || rc="$?"
  if command -v tcpkill >/dev/null 2>&1; then
    assert_equals "0" "$rc" "R4B11-007: verify should pass when tcpkill exists"
  else
    assert_equals "1" "$rc" "R4B11-007: verify should fail when tcpkill is missing"
  fi
)

# 面板源分发（install.sh）：derive_panel_prefix 映射表与 Go 侧
# derivePanelOrigin / 后端 panelMirrorPrefixFromAgentAddr 对表：
# wss→https、ws→http、裸地址→https、路径丢弃、端口保留、空地址
# 与未知 scheme 派生失败。
test_derive_panel_prefix_table() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVER_ADDR="wss://panel.example.com"
  assert_equals "https://panel.example.com/api/v1/open_api/gh" "$(derive_panel_prefix)" "wss should derive https panel prefix"
  SERVER_ADDR="ws://panel.example.com:6365"
  assert_equals "http://panel.example.com:6365/api/v1/open_api/gh" "$(derive_panel_prefix)" "ws should derive http panel prefix with port"
  SERVER_ADDR="panel.example.com:8443"
  assert_equals "https://panel.example.com:8443/api/v1/open_api/gh" "$(derive_panel_prefix)" "bare address should default to https"
  SERVER_ADDR="https://panel.example.com/sub/path?x=1"
  assert_equals "https://panel.example.com/api/v1/open_api/gh" "$(derive_panel_prefix)" "path/query in addr should be dropped (origin only)"
  SERVER_ADDR=""
  if derive_panel_prefix >/dev/null; then
    fail "empty SERVER_ADDR must not derive a panel prefix"
  fi
  SERVER_ADDR="ftp://panel.example.com"
  if derive_panel_prefix >/dev/null; then
    fail "unknown scheme must not derive a panel prefix"
  fi
)

# 面板源分发（install.sh）：面板线路探活 -> 独占路线表（阶段 3），
# 直连与代理根本不被探测（桩对非面板探测一律判死也不影响）。
test_install_script_routes_panel_first_when_panel_alive() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  SERVER_ADDR="wss://panel.example.com:8443"
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *panel.example.com*) printf '0.050 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "panel-alive: routes should be exactly the panel route"
  assert_equals "https://panel.example.com:8443/api/v1/open_api/gh" "${DOWNLOAD_ROUTES[0]}" "panel-alive: the only route should be the derived panel prefix"
  assert_equals "3" "$ROUTES_STAGE" "panel-alive: stage should be 3 (panel-exclusive, extendable)"
)

# 面板源分发（install.sh）：面板线路探不通 -> 原样回退既有流程：
# 直连活则直连短路（阶段 1）；直连也死则代理排名（阶段 2）且路线
# 表里没有面板前缀。
test_install_script_routes_panel_dead_falls_back() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  SERVER_ADDR="wss://panel.example.com:8443"
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  # 第一幕：面板死、直连活
  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *panel.example.com*) return 22 ;;
          *) printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "panel-dead/direct-alive: routes should be direct only"
  assert_equals "" "${DOWNLOAD_ROUTES[0]}" "panel-dead/direct-alive: the only route should be direct"
  assert_equals "1" "$ROUTES_STAGE" "panel-dead/direct-alive: stage should be 1"

  # 第二幕：面板死、直连死、ghps 活
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *ghps.cc*) printf '0.050 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "10" "${#DOWNLOAD_ROUTES[@]}" "panel-dead/direct-dead: routes should be 9 proxies + direct fallback"
  assert_equals "https://ghps.cc" "${DOWNLOAD_ROUTES[0]}" "panel-dead/direct-dead: fastest probed proxy should rank first"
  case " ${DOWNLOAD_ROUTES[*]} " in
    *panel.example.com*) fail "panel-dead: a dead panel route must not enter the route table" ;;
  esac
  assert_equals "2" "$ROUTES_STAGE" "panel-dead/direct-dead: stage should be 2"
)

# 面板源分发（install.sh）：面板独占（阶段 3）后二进制下载失败 ->
# extend 补测代理排名 + 直连殿后，经 ghfast 下载成功且校验通过。
test_install_script_panel_route_extends_on_download_failure() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  SERVER_ADDR="wss://panel.example.com:8443"
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/v-test/gost-amd64"

  curl() {
    local all_args="$*"
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
    case "$all_args" in
      *url_effective*)
        case "$all_args" in
          *panel.example.com*) printf '0.050 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *ghfast.top*) printf '0.200 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        return 0
        ;;
    esac
    if [[ "$url" == *.sha256 ]]; then
      printf '#!/bin/bash\necho "new version"\n' | sha256sum | awk '{print $1"  gost-amd64.sha256"}' > "$output"
      return 0
    fi
    case "$url" in
      *panel.example.com*) return 22 ;;
      https://ghfast.top/*)
        cat > "$output" <<'EOF'
#!/bin/bash
echo "new version"
EOF
        chmod +x "$output"
        ;;
      *) return 22 ;;
    esac
  }

  local dest
  dest=$(mktemp)
  download_agent_binary "$dest" || fail "panel-extend: download should succeed via ghfast after the panel route fails"
  grep -q "new version" "$dest" || fail "panel-extend: downloaded body should come from the ghfast route"
  assert_equals "" "${DOWNLOAD_ROUTES[-1]}" "panel-extend: direct should be appended as the last-resort route"
)

# L2-33（R5-FD）：PROXY_ENABLED 大小写不敏感——False/FALSE/0 必须与
# false 同义走直连（旧实现仅精确匹配小写 false，运维逃生口误写大小
# 写时静默落入内置选路且无告警）。两脚本同口径，对旧代码必红。
test_proxy_enabled_case_insensitive() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  PROXY_URL=""
  local v
  for v in false False FALSE 0; do
    PROXY_ENABLED="$v"
    assert_equals "direct" "$(route_mode)" "L2-33: install.sh PROXY_ENABLED=$v should select direct mode"
  done
  PROXY_ENABLED="true"
  assert_equals "builtin" "$(route_mode)" "L2-33: install.sh PROXY_ENABLED=true should stay builtin"

  load_script_without_main "$ROOT_DIR/panel_install.sh"
  for v in false False FALSE 0; do
    PROXY_ENABLED="$v"
    assert_equals "direct" "$(route_mode)" "L2-33: panel_install.sh PROXY_ENABLED=$v should select direct mode"
  done
)

# L2-06①（R5-FD）：install.sh 慢直连门——直连探针 2.5s（通但慢）不
# 得短路独占，须按实测延迟并入代理排名：ghps 0.100、ghfast 0.200
# 在前，直连按 2.5s 排第三，其后为未判活代理（清单原序），阶段为
# 2 而非 1。对旧代码必红（旧实现只判成败、慢直连恒阶段 1 独占）。
test_install_script_slow_direct_joins_ranking() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  SERVER_ADDR=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *ghps.cc*) printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *ghfast.top*) printf '0.200 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *" https://github.com/"*) printf '2.500 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "2" "$ROUTES_STAGE" "L2-06(1): slow direct must not short-circuit (stage 2, not 1)"
  assert_equals "10" "${#DOWNLOAD_ROUTES[@]}" "L2-06(1): routes should be 9 proxies + direct"
  assert_equals "https://ghps.cc" "${DOWNLOAD_ROUTES[0]}" "L2-06(1): fastest proxy first"
  assert_equals "https://ghfast.top" "${DOWNLOAD_ROUTES[1]}" "L2-06(1): second fastest proxy second"
  assert_equals "" "${DOWNLOAD_ROUTES[2]}" "L2-06(1): slow direct inserted by measured latency (third)"
)

# L2-35（R5-FD）：panel_install.sh 同族第三面——慢直连同样不得短路
# 独占，口径与 install.sh 一致。
test_panel_slow_direct_joins_ranking() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  curl() {
    case "$*" in
      *url_effective*)
        case "$*" in
          *ghps.cc*) printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *ghfast.top*) printf '0.200 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *" https://github.com/"*) printf '2.500 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) return 22 ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "2" "$ROUTES_STAGE" "L2-35: slow direct must not short-circuit on the panel side either"
  assert_equals "https://ghps.cc" "${DOWNLOAD_ROUTES[0]}" "L2-35: fastest proxy first"
  assert_equals "" "${DOWNLOAD_ROUTES[2]}" "L2-35: slow direct inserted by measured latency (third)"
)

# L2-06②（R5-FD）：download_agent_binary 整轮总预算——预算耗尽时必
# 须整轮失败（桩 curl 恒成功也救不了已过期的预算），且正式文件与
# .part 均不得落地/残留。
test_install_script_download_budget_enforced() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED="false"
  PROXY_URL=""
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/v-test/gost-amd64"
  FLMX_DOWNLOAD_BUDGET_SECONDS=0

  curl() { return 0; }

  local dest rc="0"
  dest=$(mktemp -u)
  download_agent_binary "$dest" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-06(2): an exhausted total budget must fail the whole download"
  [[ ! -e "$dest" ]] || fail "L2-06(2): no binary may be installed when the budget is exhausted"
  [[ ! -e "$dest.part" ]] || fail "L2-06(2): the partial file must be cleaned up when the budget is exhausted"
)

# L2-06②（R5-FD）：跨线路续传——第一条路线写下前 9 字节后失败，
# 第二条路线必须带 -C - 从断点续传（桩按已有文件大小补齐剩余字节
# 模拟 Range），最终文件为完整内容且校验通过、.part 被移交为正式
# 文件。旧实现每条路线 rm 从零重下，本用例必红。
test_install_script_download_resumes_across_routes() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  SERVER_ADDR=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/v-test/gost-amd64"

  local full_body="FULLBODY-0123456789"
  local args_log
  args_log=$(mktemp)

  curl() {
    echo "$*" >> "$args_log"
    local output="" url=""
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
    case "$*" in
      *url_effective*)
        printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO"
        return 0
        ;;
    esac
    if [[ "$url" == *.sha256 ]]; then
      printf '%s' "$full_body" | sha256sum | awk '{print $1"  gost-amd64.sha256"}' > "$output"
      return 0
    fi
    case "$url" in
      https://ghfast.top/*)
        # 模拟支持 Range 的服务端：按已有字节数补齐剩余部分
        local have=0
        if [[ -f "$output" ]]; then
          have=$(stat -c %s "$output")
        fi
        printf '%s' "${full_body:$have}" >> "$output"
        return 0
        ;;
      *)
        # 直连：只写前 9 字节后失败，留下可续传的断点
        printf '%s' "${full_body:0:9}" > "$output"
        return 1
        ;;
    esac
  }

  local dest
  dest=$(mktemp -u)
  download_agent_binary "$dest" >/dev/null || fail "L2-06(2): download should succeed via the second route"
  assert_equals "$full_body" "$(cat "$dest")" "L2-06(2): final binary must be the full body assembled across routes"
  grep -q -- '-C' "$args_log" || fail "L2-06(2): later routes must request a Range resume (-C -)"
  [[ ! -e "$dest.part" ]] || fail "L2-06(2): the partial file must be moved into place on success"
)

# L2-06③（R5-FD）：resolve_latest_release_tag 整轮总预算——预算耗尽
# 时必须整轮失败，而不是逐路线 30s+30s 无上限串行下去。
test_install_script_resolve_budget_enforced() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED="false"
  PROXY_URL=""
  FLMX_RESOLVE_BUDGET_SECONDS=0

  curl() { return 0; }

  local rc="0"
  resolve_latest_release_tag >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-06(3): an exhausted resolve budget must fail tag resolution"
)

# L2-32（R5-FD）：.sha256 给出大写哈希时须先归一再比对——旧实现直
# 接以 ^[0-9a-f]{64}$ 校验，合法资产被误判「格式无效」拒绝安装。
test_install_script_uppercase_sha256_accepted() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  curl() {
    local output="" url=""
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
    if [[ "$url" == *.sha256 ]]; then
      printf 'fixture-body' | sha256sum | awk '{print toupper($1)"  gost-amd64.sha256"}' > "$output"
      return 0
    fi
    return 1
  }

  local bin
  bin=$(mktemp)
  printf 'fixture-body' > "$bin"
  verify_downloaded_binary "$bin" "https://example.com/gost-amd64" >/dev/null || \
    fail "L2-32: a valid uppercase checksum file must be accepted after normalization"
)

# L2-02（R5-FD）：PG 迁移失败回滚四幕——down 失败 / pgloader 失败 /
# 最终 up 失败（.env 已切 PG）都必须把 .env 恢复为迁移前快照、删除
# 快照并返回 1；旧实现 down/up 裸调用在 set -e 下先于回滚退出、快
# 照（含密钥）失败路径长留。成功幕 .env 切 postgres、快照删除。
test_panel_migrate_postgres_rollbacks() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cd "$work"
  printf 'JWT_SECRET=test-secret\nDB_TYPE=sqlite\nPOSTGRES_DB=flux_panel\nPOSTGRES_USER=flux_panel\nPOSTGRES_PASSWORD=test-pg-pass\n' > .env
  printf 'services: {}\n' > docker-compose.yml
  cp .env env-original

  local flags="$work/flags"
  mkdir -p "$flags"
  FIXTURE_FLAGS="$flags"

  check_docker() { DOCKER_CMD="fixture_compose"; return 0; }
  fixture_compose() {
    case "$*" in
      "down") [[ ! -f "$FIXTURE_FLAGS/down-fail" ]] ;;
      "up -d postgres backend frontend") [[ ! -f "$FIXTURE_FLAGS/final-up-fail" ]] ;;
      *) return 0 ;;
    esac
  }
  docker() {
    case "$*" in
      *pgloader*) [[ ! -f "$FIXTURE_FLAGS/pgloader-fail" ]] ;;
      *) return 0 ;;
    esac
  }
  wait_for_postgres_healthy() { return 0; }
  wait_for_backend_healthy() { return 0; }
  sleep() { :; }

  local rc

  : > "$flags/down-fail"
  rc="0"
  migrate_to_postgres >"$work/act1.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-02: down failure must fail the migration"
  diff -q env-original .env >/dev/null || fail "L2-02: down failure must restore .env from the snapshot"
  [[ ! -e .env.pre-postgres-migration ]] || fail "L2-02: down failure must delete the env snapshot after rollback"
  grep -q "恢复迁移前" "$work/act1.log" || fail "L2-02: down failure rollback must be reported"
  rm -f "$flags/down-fail"

  : > "$flags/pgloader-fail"
  rc="0"
  migrate_to_postgres >"$work/act2.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-02: pgloader failure must fail the migration"
  diff -q env-original .env >/dev/null || fail "L2-02: pgloader failure must restore .env from the snapshot"
  [[ ! -e .env.pre-postgres-migration ]] || fail "L2-02: pgloader failure must delete the env snapshot after rollback"
  rm -f "$flags/pgloader-fail"

  : > "$flags/final-up-fail"
  rc="0"
  migrate_to_postgres >"$work/act3.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-02: final up failure must fail the migration"
  diff -q env-original .env >/dev/null || fail "L2-02: final up failure must roll .env back from the PG switch"
  [[ ! -e .env.pre-postgres-migration ]] || fail "L2-02: final up failure must delete the env snapshot after rollback"
  grep -q "已回滚" "$work/act3.log" || fail "L2-02: final up failure must report the completed rollback"
  rm -f "$flags/final-up-fail"

  rc="0"
  migrate_to_postgres >"$work/act4.log" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "L2-02: migration should succeed when every step succeeds"
  grep -q '^DB_TYPE=postgres' .env || fail "L2-02: successful migration must switch .env to postgres"
  [[ ! -e .env.pre-postgres-migration ]] || fail "L2-02: successful migration must delete the env snapshot"
)

# L2-12（R5-FD）：update_panel 必须走 VERSION > FLUX_VERSION >
# PINNED_VERSION > latest 优先级链——旧实现只认 VERSION，FLUX_VERSION
# /PINNED 定版被静默吞掉直升 latest（桩 latest=9.9.9 不得被选中），
# 且定版来源须在输出中点名。
test_panel_update_honors_version_priority_chain() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  check_docker() { DOCKER_CMD="true"; }
  resolve_panel_deployment() { PANEL_DEPLOY_DIR=$(mktemp -d); }
  validate_panel_update_environment() { :; }
  get_current_db_type() { echo "sqlite"; }
  get_deployed_panel_version() { return 1; }
  resolve_latest_release_tag() { echo "9.9.9"; }
  download_panel_update_compose() { UPDATE_COMPOSE_CANDIDATE="candidate"; }
  validate_panel_update_compose() { return 0; }
  create_panel_update_backup() { UPDATE_BACKUP_DIR="backup"; return 0; }
  pull_panel_update_images() { return 0; }
  activate_panel_update_files() { return 0; }
  start_panel_after_update() { return 0; }
  check_ipv6_support() { return 1; }

  local log
  log=$(mktemp)

  VERSION=""
  FLUX_VERSION="1.5.0"
  PINNED_VERSION=""
  update_panel >"$log" 2>&1 || fail "L2-12: update should succeed with FLUX_VERSION pinned"
  assert_equals "1.5.0" "$LATEST_VERSION" "L2-12: FLUX_VERSION must win over latest"
  grep -q "FLUX_VERSION" "$log" || fail "L2-12: the pinned version source must be named in the output"

  VERSION=""
  FLUX_VERSION=""
  PINNED_VERSION="1.5.1"
  update_panel >"$log" 2>&1 || fail "L2-12: update should succeed with PINNED_VERSION pinned"
  assert_equals "1.5.1" "$LATEST_VERSION" "L2-12: PINNED_VERSION must win over latest"
  grep -q "PINNED_VERSION" "$log" || fail "L2-12: the pinned version source must be named in the output"

  VERSION=""
  FLUX_VERSION=""
  PINNED_VERSION=""
  update_panel >"$log" 2>&1 || fail "L2-12: update should succeed via latest"
  assert_equals "9.9.9" "$LATEST_VERSION" "L2-12: latest is the fallback when nothing is pinned"
)

# L2-14（R5-FD）：卸载必须先定位部署目录（不以 cwd 为准）、down 在
# 部署目录内执行、含密钥的备份产物（backups/、.env.bak.*、迁移快
# 照、gost.db.bak）一并清除；定位失败时不删任何东西、不报完成。
test_panel_uninstall_locates_deployment_and_cleans_artifacts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local deploy_dir other_dir
  deploy_dir=$(mktemp -d)
  other_dir=$(mktemp -d)
  printf 'services: {}\n' > "$deploy_dir/docker-compose.yml"
  printf 'JWT_SECRET=test\n' > "$deploy_dir/.env"
  mkdir -p "$deploy_dir/backups"
  printf 'JWT_SECRET=test\n' > "$deploy_dir/backups/panel-update-x.env"
  printf 'JWT_SECRET=test\n' > "$deploy_dir/.env.bak.123"
  printf 'db' > "$deploy_dir/gost.db.bak"
  printf 'JWT_SECRET=test\n' > "$deploy_dir/.env.pre-postgres-migration"

  DEPLOY_DIR_FIXTURE="$deploy_dir"
  DOWN_CWD_LOG=$(mktemp)

  check_docker() { DOCKER_CMD="fixture_compose_down"; return 0; }
  fixture_compose_down() { pwd -P > "$DOWN_CWD_LOG"; return 0; }
  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$DEPLOY_DIR_FIXTURE"
    cd "$PANEL_DEPLOY_DIR"
  }

  cd "$other_dir"
  local rc="0"
  uninstall_panel <<< "y" >"$other_dir/uninstall.log" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "L2-14: uninstall should succeed against the located deployment"
  assert_equals "$deploy_dir" "$(cat "$DOWN_CWD_LOG")" "L2-14: compose down must run inside the deployment directory, not the caller's cwd"
  [[ ! -e "$deploy_dir/docker-compose.yml" ]] || fail "L2-14: docker-compose.yml must be removed"
  [[ ! -e "$deploy_dir/.env" ]] || fail "L2-14: .env must be removed"
  [[ ! -e "$deploy_dir/backups" ]] || fail "L2-14: backups/ (env copies + DB backups) must be removed"
  [[ ! -e "$deploy_dir/.env.bak.123" ]] || fail "L2-14: .env.bak.* must be removed"
  [[ ! -e "$deploy_dir/gost.db.bak" ]] || fail "L2-14: gost.db.bak must be removed"
  [[ ! -e "$deploy_dir/.env.pre-postgres-migration" ]] || fail "L2-14: the PG migration snapshot must be removed"
  grep -q "卸载完成" "$other_dir/uninstall.log" || fail "L2-14: completion must be reported after real removal"

  printf 'services: {}\n' > "$deploy_dir/docker-compose.yml"
  printf 'JWT_SECRET=test\n' > "$deploy_dir/.env"
  resolve_panel_deployment() { return 1; }
  rc="0"
  uninstall_panel <<< "y" >"$other_dir/uninstall2.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-14: an unlocatable deployment must fail the uninstall"
  [[ -e "$deploy_dir/docker-compose.yml" && -e "$deploy_dir/.env" ]] || fail "L2-14: nothing may be deleted when the deployment cannot be located"
  if grep -q "卸载完成" "$other_dir/uninstall2.log"; then
    fail "L2-14: completion must not be reported when nothing was uninstalled"
  fi
)

# L2-11（R5-FD）：panel 资产下载两腿必须带超时看门狗参数——桩 curl
# 记录 argv，本体腿与 .sha256 腿都必须出现 --max-time 与
# --speed-limit（旧实现两腿皆无，假活慢死路线可无限挂死）。
test_panel_download_verified_uses_timeouts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local log dest
  log=$(mktemp)
  dest=$(mktemp -u)
  CURL_ARGS_LOG="$log"

  curl() {
    echo "$*" >> "$CURL_ARGS_LOG"
    local output="" url=""
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
    if [[ "$url" == *.sha256 ]]; then
      printf 'fixture-body' | sha256sum | awk '{print $1"  asset.sha256"}' > "$output"
    else
      printf 'fixture-body' > "$output"
    fi
    return 0
  }

  download_verified "https://example.com/asset.yml" "$dest" >/dev/null || \
    fail "L2-11: download_verified should succeed with the stub"
  assert_equals "2" "$(grep -c -- '--max-time' "$log")" "L2-11: both download legs must carry --max-time"
  assert_equals "2" "$(grep -c -- '--speed-limit' "$log")" "L2-11: both download legs must carry the stall watchdog (--speed-limit)"
)

# L2-13/L2-34（R5-FD）：菜单 6 专属覆盖补齐——重复域名幂等拒绝、
# validate 失败回滚（菜单 6 自己的那份校验/回滚代码）、缺
# Caddyfile 前置拒绝、完成输出四步文案断言（第 2 步 Cache Rules 字
# 段级文案、第 3 步必须含「添加前置地址」＋「保存配置」两个持久化
# 动作、第 4 步 cf-cache-status/BYPASS 自检）。
test_panel_menu6_dedicated_coverage() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "L2-34: initial menu 5 run should succeed"

  local ok_log="$FIXTURE_DIR/menu6-ok.log"
  setup_subscription_front_domain <<< "sub.example.com" >"$ok_log" 2>&1 || \
    fail "L2-34: menu 6 run should succeed"
  grep -q "URI 路径" "$ok_log" || fail "L2-34: step 2 must name the Cache Rules field (URI 路径)"
  grep -q "绕过缓存" "$ok_log" || fail "L2-34: step 2 must name the bypass-cache action"
  grep -q "URI 完整" "$ok_log" || fail "L2-34: step 2 must carry the URI 完整 anti-mistake warning"
  grep -q "添加前置地址" "$ok_log" || fail "L2-13: step 3 must include the 添加前置地址 action"
  grep -q "保存配置" "$ok_log" || fail "L2-13: step 3 must include the 保存配置 action"
  grep -q "cf-cache-status" "$ok_log" || fail "L2-34: step 4 self-check must use cf-cache-status"
  grep -q "BYPASS" "$ok_log" || fail "L2-34: step 4 self-check must expect BYPASS"

  cp "$CADDYFILE_PATH" "$FIXTURE_DIR/expected-good"

  local rc="0" dup_log="$FIXTURE_DIR/menu6-dup.log"
  setup_subscription_front_domain <<< "sub.example.com" >"$dup_log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-34: a duplicate front domain must be rejected"
  grep -q "已存在" "$dup_log" || fail "L2-34: duplicate rejection must be reported"
  diff -q "$FIXTURE_DIR/expected-good" "$CADDYFILE_PATH" >/dev/null || \
    fail "L2-34: duplicate rejection must not modify the Caddyfile"

  : > "$VALIDATE_FAIL_FLAG"
  rc="0"
  setup_subscription_front_domain <<< "other.example.com" >"$FIXTURE_DIR/menu6-vfail.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-34: menu 6 must fail when caddy validate fails"
  diff -q "$FIXTURE_DIR/expected-good" "$CADDYFILE_PATH" >/dev/null || \
    fail "L2-34: menu 6 must roll back the appended block when validate fails"
  rm -f "$VALIDATE_FAIL_FLAG"

  CADDYFILE_PATH="$FIXTURE_DIR/no-such-Caddyfile"
  rc="0"
  setup_subscription_front_domain <<< "sub2.example.com" >"$FIXTURE_DIR/menu6-nofile.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-34: menu 6 must refuse when the Caddyfile is missing"
  grep -q "请先运行菜单 5" "$FIXTURE_DIR/menu6-nofile.log" || \
    fail "L2-34: missing-Caddyfile refusal must point at menu 5"
)

# L2-28（R5-FD）：菜单 5/6 在 restart 成功但 is-active 失败分支必须
# 回滚 Caddyfile——旧实现该分支只有日志指引，坏配置留盘、下次重启
# 仍加载且无人提示文件已被改动（本用例对旧代码必红）。
test_panel_menu_is_active_failure_rolls_back() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "L2-28: initial menu 5 run should succeed"
  cp "$CADDYFILE_PATH" "$FIXTURE_DIR/expected-good"

  : > "$ISACTIVE_FAIL_FLAG"
  local rc="0" log="$FIXTURE_DIR/menu5-isactive.log"
  setup_domain_proxy <<< "panel.example.com" >"$log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-28: menu 5 must fail when caddy is not active after restart"
  diff -q "$FIXTURE_DIR/expected-good" "$CADDYFILE_PATH" >/dev/null || \
    fail "L2-28: menu 5 must roll back the Caddyfile when is-active fails"
  grep -q "回滚" "$log" || fail "L2-28: menu 5 rollback must be reported"
  rm -f "$ISACTIVE_FAIL_FLAG"

  setup_subscription_front_domain <<< "sub.example.com" >/dev/null || \
    fail "L2-28: menu 6 run should succeed"
  cp "$CADDYFILE_PATH" "$FIXTURE_DIR/expected-good"

  : > "$ISACTIVE_FAIL_FLAG"
  rc="0"
  log="$FIXTURE_DIR/menu6-isactive.log"
  setup_subscription_front_domain <<< "other.example.com" >"$log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-28: menu 6 must fail when caddy is not active after restart"
  diff -q "$FIXTURE_DIR/expected-good" "$CADDYFILE_PATH" >/dev/null || \
    fail "L2-28: menu 6 must roll back the appended block when is-active fails"
  grep -q "回滚" "$log" || fail "L2-28: menu 6 rollback must be reported"
)

# L2-31（R5-FD）：域名幂等/剔除比较大小写不敏感——已存小写站点块
# 时以大写输入跑菜单 6 必须被幂等拒绝；以不同大小写重跑菜单 5 必
# 须恰留一份站点块（旧实现字节比较，两幕都会写出重复块）。
test_panel_domain_compare_case_insensitive() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "L2-31: initial menu 5 run should succeed"

  local rc="0"
  setup_subscription_front_domain <<< "PANEL.example.com" >"$FIXTURE_DIR/case-dup.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "L2-31: menu 6 must reject a case-variant duplicate of an existing site block"

  setup_domain_proxy <<< "Panel.Example.COM" >/dev/null || \
    fail "L2-31: menu 5 re-run with different case should succeed"
  assert_equals "1" "$(grep -ci '^panel\.example\.com {' "$CADDYFILE_PATH")" \
    "L2-31: exactly one panel site block may remain after a case-variant re-run"
)

test_proxy_enabled_case_insensitive
test_install_script_slow_direct_joins_ranking
test_panel_slow_direct_joins_ranking
test_install_script_download_budget_enforced
test_install_script_download_resumes_across_routes
test_install_script_resolve_budget_enforced
test_install_script_uppercase_sha256_accepted
test_panel_migrate_postgres_rollbacks
test_panel_update_honors_version_priority_chain
test_panel_uninstall_locates_deployment_and_cleans_artifacts
test_panel_download_verified_uses_timeouts
test_panel_menu6_dedicated_coverage
test_panel_menu_is_active_failure_rolls_back
test_panel_domain_compare_case_insensitive
test_install_script_respects_disabled_proxy
test_install_script_never_asks_for_proxy_config
test_install_script_builds_canonical_download_url
test_install_script_routes_direct_first
test_install_script_routes_rank_proxies_when_direct_dead
test_install_script_download_failover_extends_routes
test_derive_panel_prefix_table
test_install_script_routes_panel_first_when_panel_alive
test_install_script_routes_panel_dead_falls_back
test_install_script_panel_route_extends_on_download_failure
test_panel_download_verified_routed_failover
test_update_flux_agent_downloads_without_prompt
test_update_flux_agent_skips_proxy_prompt_when_not_installed
test_update_flux_agent_prints_online_confirmation_guidance
test_flux_agent_health_ok_requires_stable_pid
test_update_flux_agent_rolls_back_flapping_binary
test_extract_leading_semver_matches_go_rules
test_update_flux_agent_rejects_downgrade
test_update_flux_agent_allows_higher_and_incomparable
test_main_propagates_operation_exit_codes
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
test_panel_install_script_builds_canonical_compose_urls
test_update_panel_downloads_without_prompt
test_resolve_panel_deployment_uses_container_labels
test_validate_panel_update_environment_rejects_empty_required_values
test_backup_sqlite_for_update_pauses_copies_and_unpauses
test_panel_install_script_uses_builtin_routes_by_default
test_panel_install_script_accepts_proxy_url_env_without_prompt
test_panel_install_script_ignores_eof_without_prompt
test_install_flux_agent_returns_nonzero_when_service_not_active
test_systemd_starts_reset_failed_before_start
test_rollback_restarts_service_from_failed_state
test_panel_menu5_preserves_front_block_after_menu6
test_panel_menu5_rolls_back_on_validate_failure
test_panel_menu5_restart_failure_rolls_back_with_guidance
test_panel_menu6_restart_failure_rolls_back_with_guidance
test_install_flux_agent_rejects_flapping_service
test_main_delete_self_only_on_success
test_main_requires_root
test_script_secret_surfaces_closed
test_pgloader_command_file_terminated
test_tcpkill_missing_warns_instead_of_silent

echo "install script tests passed"
