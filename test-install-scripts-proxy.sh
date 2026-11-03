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

# 在伪终端下运行给定的 harness 脚本并喂入输入，返回其退出码。
# 归属守卫的交互确认分支以 [[ -t 0 ]] 为判据，管道 stdin 覆盖不到，
# 必须借 util-linux 的 script 分配 pty 才能真实覆盖（AI-07）。
pty_run() {
  local input="$1"
  local harness="$2"

  command -v script >/dev/null 2>&1 || \
    fail "pty_run requires util-linux 'script' for interactive guard coverage"
  printf '%s' "$input" | script -qec "bash \"$harness\"" /dev/null >/dev/null 2>&1
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

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

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

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

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

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

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

# R9-02: 更新链的备份步注入 ENOSPC——cp 桩仅对 .bak 目标失败。
# 旧实现 cp 无返回码检查：无陈旧 .bak 时替换照常、回滚无源、坏二
# 进制留活（P2a）；有陈旧 .bak 时静默跨代回滚且回执报成功（P2b）。
# 新实现必须在替换前中止：当前二进制逐字未动、回执点名备份失败、
# 不得出现回滚成功字样。夹具主体与 X-P2-11 flap 夹具同构（新二进
# 制为 flap 形态，旧实现下必达回滚分支）。
agent_update_backup_fail_fixture() {
  local mode="$1"
  load_script_without_main "$ROOT_DIR/install.sh"

  FLMX_AGENT_TAKEOVER="1"
  SERVICE_MANAGER="systemd"
  FIXTURE_DIR=$(mktemp -d)
  INSTALL_DIR="$FIXTURE_DIR/agent"
  mkdir -p "$INSTALL_DIR"
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
  : > "$FLUX_AGENT_SYSTEMD_SERVICE_FILE"
  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi
echo "old good"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"
  if [[ "$mode" == "stale" ]]; then
    cat > "$INSTALL_DIR/flux_agent.bak" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 0.9.0 (go1.25 linux/amd64)"; exit 0; fi
echo "stale good"
EOF
    chmod +x "$INSTALL_DIR/flux_agent.bak"
  fi
  STATE_DIR="$FIXTURE_DIR/state"
  mkdir -p "$STATE_DIR"
  echo good > "$STATE_DIR/mode"
  echo 0 > "$STATE_DIR/active_cnt"

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
  cp() {
    local last="${@: -1}"
    if [[ "$last" == *.bak ]]; then
      echo "cp: cannot create regular file '$last': No space left on device" >&2
      return 1
    fi
    command cp "$@"
  }

  local rc="0"
  update_flux_agent > "$FIXTURE_DIR/out.log" 2>&1 || rc="$?"
  echo "$rc" > "$FIXTURE_DIR/rc"
  echo "$FIXTURE_DIR"
}

test_update_flux_agent_aborts_when_backup_fails() (
  set -euo pipefail
  local dir rc
  dir=$(agent_update_backup_fail_fixture nostale)
  rc=$(cat "$dir/rc")
  assert_equals "1" "$rc" "R9-02: update must abort when the backup copy fails"
  grep -q "old good" "$dir/agent/flux_agent" || \
    fail "R9-02: current binary must remain the pre-update version when the backup fails (no stale .bak existed)"
  grep -q "备份当前版本失败" "$dir/out.log" || \
    fail "R9-02: receipt must name the backup failure"
  grep -q "已中止更新" "$dir/out.log" || \
    fail "R9-02: receipt must state the update was aborted before replacement"
  if grep -q "已回滚到旧版本并重启成功" "$dir/out.log"; then
    fail "R9-02: receipt must not claim a rollback success that has no source"
  fi
  if grep -q "更新完成" "$dir/out.log"; then
    fail "R9-02: receipt must not claim update success after an aborted backup"
  fi
  [[ ! -f "$dir/agent/flux_agent.bak" ]] || \
    fail "R9-02: no .bak may remain after an aborted backup"
)

test_update_flux_agent_aborts_when_backup_fails_with_stale_bak() (
  set -euo pipefail
  local dir rc
  dir=$(agent_update_backup_fail_fixture stale)
  rc=$(cat "$dir/rc")
  assert_equals "1" "$rc" "R9-02: update must abort when the backup copy fails even with a stale .bak present"
  grep -q "old good" "$dir/agent/flux_agent" || \
    fail "R9-02: current binary must remain the pre-update version; the stale .bak must not be rolled back across generations"
  if grep -q "stale good" "$dir/agent/flux_agent"; then
    fail "R9-02: stale .bak content must never become the running binary after a failed backup"
  fi
  grep -q "备份当前版本失败" "$dir/out.log" || \
    fail "R9-02: receipt must name the backup failure"
  if grep -q "已回滚到旧版本并重启成功" "$dir/out.log"; then
    fail "R9-02: receipt must not report a cross-generation rollback as success"
  fi
  [[ ! -f "$dir/agent/flux_agent.bak" ]] || \
    fail "R9-02: the untrusted .bak must be removed after an aborted backup"
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

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

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

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

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

  # BD-01＋B2-07：本用例自基线起被双层遮蔽、对所命名的机制零判
  # 别力——① 旧夹具只有二进制、无 config，归属守卫先行拒绝，名
  # 义路径（下载失败保 legacy gost）从未进入，三断言由拒绝路径
  # 同形满足；② cleanup 桩把标记赋给变量、而调用在子 shell 内，
  # 赋值必然丢失，断言结构性不可红。现夹具补自家 config.json
  # （addr 与 SERVER_ADDR 相同，守卫正当放行——与 update 孪生用
  # 例的 TAKEOVER 声明同为「操作者已确认归属」口径），桩一律写
  # 标记文件、断言读文件。install_flux_agent 的失败路径用 exit
  # 而非 return，调用仍须留在子 shell 内，标记文件是唯一能穿出
  # 子 shell 的观测面；curl 标记兼作「名义路径确已到达下载步」
  # 的正向证据（守卫拒绝时 curl 调用恒为 0 次）。
  cat > "$INSTALL_DIR/config.json" <<'EOF'
{"addr": "panel.example.com:443", "secret": "secret"}
EOF

  local rc="0"

  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() {
    touch "$INSTALL_DIR/.cleanup_called"
  }
  systemctl() { return 0; }
  curl() { echo call >> "$INSTALL_DIR/.curl_calls"; return 0; }

  ( install_flux_agent >/dev/null ) || rc="$?"

  assert_equals "1" "$rc" "install_flux_agent should fail when the download artifact is missing"
  [[ -f "$INSTALL_DIR/.curl_calls" ]] || fail "install_flux_agent should reach the download step with its own config (guard pass), not exit at the ownership guard"
  [[ ! -e "$INSTALL_DIR/.cleanup_called" ]] || fail "install_flux_agent should preserve legacy gost when download fails"
  [[ -f "$INSTALL_DIR/flux_agent" ]] || fail "install_flux_agent should keep the existing flux_agent binary when download fails"
)

test_update_flux_agent_preserves_legacy_gost_when_download_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

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

# AI-10：agent_config_addr 必须把 JSON 转义写法归一化为原始地址，
# 否则异风格生成器写出的自家 config 会被归属守卫误判为他人。
test_agent_config_addr_normalizes_json_escapes() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local dir
  dir=$(mktemp -d)

  assert_equals "" "$(agent_config_addr "$dir")" \
    "AI-10: agent_config_addr should be empty without config.json"

  printf '%s' '{"addr": "https://panel.example.com:443", "secret": "x"}' > "$dir/config.json"
  assert_equals "https://panel.example.com:443" "$(agent_config_addr "$dir")" \
    "AI-10: agent_config_addr should return a plain addr verbatim"

  printf '%s' '{"addr": "https:\/\/panel.example.com:443", "secret": "x"}' > "$dir/config.json"
  assert_equals "https://panel.example.com:443" "$(agent_config_addr "$dir")" \
    "AI-10: agent_config_addr should decode escaped slashes"

  printf '%s' '{"addr": "https://panel.example.com\u003a443", "secret": "x"}' > "$dir/config.json"
  assert_equals "https://panel.example.com:443" "$(agent_config_addr "$dir")" \
    "AI-10: agent_config_addr should decode unicode escapes"

  printf '%s' '{"secret": "x"}' > "$dir/config.json"
  assert_equals "" "$(agent_config_addr "$dir")" \
    "AI-10: agent_config_addr should be empty when addr is absent"
)

# AI-07（节点侧）：install 认领守卫的形态矩阵随仓落地——自家三种
# 写法放行、他人两种写法拒绝、不可判定拒绝、空目录放行、TAKEOVER
# 放行留痕、交互 y 放行 / n 拒绝，全部走真实守卫函数，拒绝时既有
# 文件逐字不动。
test_guard_agent_dir_ownership_matrix() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  INSTALL_DIR=$(mktemp -d)
  SERVER_ADDR="https://mine.example.com:443"
  SECRET="test-secret"

  local rc="0" out="" before="" harness=""
  run_guard() {
    rc="0"
    out=$(guard_agent_dir_ownership </dev/null 2>&1) || rc="$?"
  }
  set_config() { printf '%s' "$1" > "$INSTALL_DIR/config.json"; }
  set_binary() { printf 'binary-bytes' > "$INSTALL_DIR/flux_agent"; }

  # 空目录 → 放行
  run_guard
  assert_equals "0" "$rc" "AI-07: guard should pass on an empty dir"

  # 自家·原文 → 放行
  set_config '{"addr": "https://mine.example.com:443", "secret": "x"}'
  set_binary
  run_guard
  assert_equals "0" "$rc" "AI-07: guard should pass when addr matches verbatim"

  # 自家·斜杠转义写法 → 放行（AI-10）
  set_config '{"addr": "https:\/\/mine.example.com:443", "secret": "x"}'
  run_guard
  assert_equals "0" "$rc" "AI-10: guard should pass when addr uses escaped slashes"

  # 自家·unicode 转义写法 → 放行（AI-10）
  set_config '{"addr": "https://mine.example.com\u003a443", "secret": "x"}'
  run_guard
  assert_equals "0" "$rc" "AI-10: guard should pass when addr uses unicode escapes"

  # 他人·原文 → 拒绝且文件逐字不动
  set_config '{"addr": "https://other.example.com:443", "secret": "x"}'
  before=$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flux_agent")
  run_guard
  assert_equals "1" "$rc" "AI-07: guard should refuse a foreign addr"
  assert_equals "$before" "$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flux_agent")" \
    "AI-07: guard refusal must not touch existing files"

  # 他人·转义写法 → 归一化后仍是他人，拒绝
  set_config '{"addr": "https:\/\/other.example.com:443", "secret": "x"}'
  run_guard
  assert_equals "1" "$rc" "AI-10: guard should still refuse a foreign addr in escaped spelling"

  # 不可判定（config 无 addr）→ 拒绝
  set_config '{"secret": "x"}'
  run_guard
  assert_equals "1" "$rc" "AI-07: guard should refuse when addr is unreadable"

  # 不可判定（二进制痕迹、无 config）→ 拒绝
  rm -f "$INSTALL_DIR/config.json"
  run_guard
  assert_equals "1" "$rc" "AI-07: guard should refuse a binary-only trace"

  # TAKEOVER → 放行且留痕
  set_config '{"addr": "https://other.example.com:443", "secret": "x"}'
  rc="0"
  out=$(FLMX_AGENT_TAKEOVER="1" guard_agent_dir_ownership </dev/null 2>&1) || rc="$?"
  assert_equals "0" "$rc" "AI-07: guard should pass with FLMX_AGENT_TAKEOVER=1"
  [[ "$out" == *"FLMX_AGENT_TAKEOVER=1"* ]] || \
    fail "AI-07: takeover pass should leave a trace line"

  # 交互确认（pty）：y 放行 / n 拒绝
  export GUARD_TEST_DIR="$INSTALL_DIR"
  export GUARD_TEST_ADDR="$SERVER_ADDR"
  harness="$INSTALL_DIR/guard-harness.sh"
  {
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh"
    cat <<'STUBEOF'
INSTALL_DIR="$GUARD_TEST_DIR"
SERVER_ADDR="$GUARD_TEST_ADDR"
SECRET="x"
guard_agent_dir_ownership
STUBEOF
  } > "$harness"
  rc="0"; pty_run $'y\n' "$harness" || rc="$?"
  assert_equals "0" "$rc" "AI-07: guard should pass on interactive y"
  rc="0"; pty_run $'n\n' "$harness" || rc="$?"
  assert_equals "1" "$rc" "AI-07: guard should refuse on interactive n"
)

# AI-06：菜单更新入口归属守卫——他人 agent 非交互拒绝（守卫在
# 下载之前：下载标记不得出现、既有文件逐字不动、输出明示当前连
# 接地址）；不可判定同样拒绝。
test_update_flux_agent_refuses_unowned_agent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  SERVER_ADDR=""

  local state_dir
  state_dir=$(mktemp -d)

  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"

  curl() { echo called >> "$state_dir/curl-called"; return 0; }
  check_and_install_tcpkill() { :; }
  systemctl() { return 0; }

  local rc="0" out
  out=$(mktemp)

  # 他人 agent（config addr 为其他面板）
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  local before
  before=$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flux_agent")
  rc="0"
  update_flux_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu update should refuse a foreign agent"
  [[ ! -f "$state_dir/curl-called" ]] || \
    fail "AI-06: menu update refusal must happen before any download"
  assert_equals "$before" "$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flux_agent")" \
    "AI-06: menu update refusal must not touch existing files"
  grep -q "other.example.com:443" "$out" || \
    fail "AI-06: menu update refusal should disclose the current panel address"

  # 不可判定（config 无 addr）
  printf '%s' '{"secret": "x"}' > "$INSTALL_DIR/config.json"
  rc="0"
  update_flux_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu update should refuse when ownership is undecidable"
  [[ ! -f "$state_dir/curl-called" ]] || \
    fail "AI-06: menu update refusal must happen before any download"
)

# AI-06：菜单更新守卫的放行形态——TAKEOVER 显式确认放行、调用方
# 带有与现存 addr 相同的面板地址时静默放行，两者都走完整更新。
test_update_flux_agent_menu_guard_pass_forms() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"

  run_confirmed_update() {
    INSTALL_DIR=$(mktemp -d)
    FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
    : > "$FLUX_AGENT_SYSTEMD_SERVICE_FILE"
    cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
    chmod +x "$INSTALL_DIR/flux_agent"
    printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"

    ask_proxy_config() { PROXY_ENABLED="false"; DOWNLOAD_URL=""; }
    ensure_download_url_initialized() {
      RESOLVED_VERSION="1.3.5"
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
      local body='#!/bin/bash
echo "new version"'
      if [[ "$url" == *.sha256 ]]; then
        printf '%s\n' "$body" | sha256sum | awk '{print $1"  gost"}' > "$output"
        return 0
      fi
      printf '%s\n' "$body" > "$output"; chmod +x "$output"
    }

    local rc="0"
    update_flux_agent </dev/null >/dev/null 2>&1 || rc="$?"
    assert_equals "0" "$rc" "AI-06: update should proceed in an ownership-confirmed form"
    grep -q "new version" "$INSTALL_DIR/flux_agent" || \
      fail "AI-06: binary should be replaced after a confirmed update"
  }

  # TAKEOVER 放行（现存 addr 与本面板无关亦可，由操作者断言归属）
  SERVER_ADDR=""
  FLMX_AGENT_TAKEOVER="1" run_confirmed_update

  # 调用方带有相同面板地址 → 静默放行（无需 TAKEOVER）
  SERVER_ADDR="https://other.example.com:443"
  run_confirmed_update
)

# AI-06：菜单更新守卫的交互形态（pty）——明示地址后 y 放行走完
# 更新、n 拒绝且下载不得发生、二进制逐字不动。
test_update_flux_agent_menu_guard_interactive() (
  set -euo pipefail

  local work
  work=$(mktemp -d)
  local agent_dir="$work/agent"
  mkdir -p "$agent_dir"
  cat > "$agent_dir/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$agent_dir/flux_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$agent_dir/config.json"

  export GUARD_TEST_DIR="$agent_dir"
  export GUARD_TEST_WORK="$work"
  local harness="$work/update-harness.sh"
  {
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh"
    cat <<'STUBEOF'
INSTALL_DIR="$GUARD_TEST_DIR"
SERVICE_MANAGER="systemd"
SERVER_ADDR=""
ensure_download_url_initialized() { RESOLVED_VERSION="1.3.5"; DOWNLOAD_URL="https://example.com/gost"; }
download_agent_binary() {
  printf '%s\n' '#!/bin/bash' 'echo "new version"' > "$1"
  chmod +x "$1"
  echo called >> "$GUARD_TEST_WORK/download-called"
}
check_and_install_tcpkill() { :; }
cleanup_legacy_gost_installation() { :; }
flux_agent_service_exists() { return 1; }
start_flux_agent_service() { :; }
flux_agent_health_ok() { return 0; }
sleep() { :; }
update_flux_agent
STUBEOF
  } > "$harness"

  local rc="0"
  pty_run $'y\n' "$harness" || rc="$?"
  assert_equals "0" "$rc" "AI-06: interactive y should let menu update proceed"
  [[ -f "$work/download-called" ]] || \
    fail "AI-06: update should reach the download step after interactive y"
  grep -q "new version" "$agent_dir/flux_agent" || \
    fail "AI-06: binary should be replaced after interactive y"

  # 复位为他人 agent 初始状态，n 必须拒绝且零改动
  cat > "$agent_dir/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$agent_dir/flux_agent"
  rm -f "$work/download-called"
  local before
  before=$(sha256sum "$agent_dir/config.json" "$agent_dir/flux_agent")
  rc="0"
  pty_run $'n\n' "$harness" || rc="$?"
  assert_equals "1" "$rc" "AI-06: interactive n should refuse menu update"
  [[ ! -f "$work/download-called" ]] || \
    fail "AI-06: update must not download after interactive n"
  assert_equals "$before" "$(sha256sum "$agent_dir/config.json" "$agent_dir/flux_agent")" \
    "AI-06: interactive n must not touch existing files"
)

# AI-06：菜单卸载入口归属守卫——他人/不可判定 agent 非交互拒绝
# （目录与文件逐字不动、输出明示当前连接地址）。
test_uninstall_flux_agent_refuses_unowned_agent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
  SERVER_ADDR=""

  cat > "$INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flux_agent"

  systemctl() { return 0; }

  local rc="0" out
  out=$(mktemp)

  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  local before
  before=$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flux_agent")
  rc="0"
  uninstall_flux_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu uninstall should refuse a foreign agent"
  assert_equals "$before" "$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flux_agent")" \
    "AI-06: menu uninstall refusal must not touch existing files"
  grep -q "other.example.com:443" "$out" || \
    fail "AI-06: menu uninstall refusal should disclose the current panel address"

  printf '%s' '{"secret": "x"}' > "$INSTALL_DIR/config.json"
  rc="0"
  uninstall_flux_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu uninstall should refuse when ownership is undecidable"
  [[ -f "$INSTALL_DIR/config.json" ]] || \
    fail "AI-06: menu uninstall refusal must keep the install dir intact"
)

# AI-06：菜单卸载守卫的放行形态——TAKEOVER 留痕放行（其后仍有常
# 规确认）；交互（pty）先过守卫确认、再过常规确认才执行卸载，
# 守卫处答 n 即终止且目录不动。
test_uninstall_flux_agent_menu_guard_pass_forms() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  systemctl() { return 0; }

  # TAKEOVER + 常规确认 y → 卸载执行、目录删除、输出含留痕
  INSTALL_DIR=$(mktemp -d)
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flux_agent.service"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  printf 'binary' > "$INSTALL_DIR/flux_agent"

  local rc="0" out
  out=$(mktemp)
  rc="0"
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flux_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "AI-06: uninstall should proceed with TAKEOVER plus confirm"
  [[ ! -d "$INSTALL_DIR" ]] || \
    fail "AI-06: install dir should be removed after a confirmed uninstall"
  grep -q "FLMX_AGENT_TAKEOVER=1" "$out" || \
    fail "AI-06: takeover uninstall should leave a trace line"

  # pty：守卫确认 y + 常规确认 y → 执行卸载
  local work
  work=$(mktemp -d)
  local agent_dir="$work/agent"
  mkdir -p "$agent_dir"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$agent_dir/config.json"
  printf 'binary' > "$agent_dir/flux_agent"

  export GUARD_TEST_DIR="$agent_dir"
  local harness="$work/uninstall-harness.sh"
  {
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh"
    cat <<'STUBEOF'
INSTALL_DIR="$GUARD_TEST_DIR"
FLUX_AGENT_SYSTEMD_SERVICE_FILE="$GUARD_TEST_DIR/flux_agent.service"
SERVICE_MANAGER="systemd"
SERVER_ADDR=""
flux_agent_service_exists() { return 1; }
uninstall_flux_agent
STUBEOF
  } > "$harness"

  rc="0"
  pty_run $'y\ny\n' "$harness" || rc="$?"
  assert_equals "0" "$rc" "AI-06: interactive y/y should let menu uninstall proceed"
  [[ ! -d "$agent_dir" ]] || \
    fail "AI-06: install dir should be removed after interactive y/y"

  # pty：守卫确认 n → 终止，目录不动（常规确认不会被问到）
  mkdir -p "$agent_dir"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$agent_dir/config.json"
  printf 'binary' > "$agent_dir/flux_agent"
  rc="0"
  pty_run $'n\n' "$harness" || rc="$?"
  assert_equals "1" "$rc" "AI-06: interactive n should refuse menu uninstall"
  [[ -f "$agent_dir/config.json" ]] || \
    fail "AI-06: install dir must stay intact after interactive n"
)

# R6-02：脚本菜单卸载须与面板自卸载路径对齐清理宿主残留——按
# tuning-backup.json 恢复 sysctl 原值、回放整形前队列、删调优宿
# 主三件套与改名前 legacy conf、disable 并删队列整形 unit、删
# agent 自建 nft 三表，且全部赶在删安装目录之前（备份在目录里）。
test_uninstall_flux_agent_cleans_host_tuning_residue() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)
  INSTALL_DIR="$work/agent"
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$work/flux_agent.service"
  FLMX_TUNING_SYSCTL_CONF="$work/sysctl.d/99-flmx-tuning.conf"
  FLMX_TUNING_SYSCTL_CONF_LEGACY="$work/sysctl.d/99-flvx-tuning.conf"
  FLMX_TUNING_BBR_MODULE_FILE="$work/modules-load.d/flmx-bbr.conf"
  FLMX_TUNING_QDISC_UNIT_FILE="$work/systemd/flmx-tuning-qdisc.service"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  mkdir -p "$INSTALL_DIR/tcpfit" "$(dirname "$FLMX_TUNING_SYSCTL_CONF")" \
    "$(dirname "$FLMX_TUNING_BBR_MODULE_FILE")" "$(dirname "$FLMX_TUNING_QDISC_UNIT_FILE")" \
    "$FLMX_PROC_SYS_DIR/net/ipv4" "$FLMX_PROC_SYS_DIR/net/core"

  printf 'bbr\n' > "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control"
  printf '999999\n' > "$FLMX_PROC_SYS_DIR/net/core/rmem_max"
  cat > "$INSTALL_DIR/tuning-backup.json" <<'JSON'
{
  "net.core.rmem_max": "212992",
  "net.ipv4.tcp_congestion_control": "cubic"
}
JSON
  cat > "$INSTALL_DIR/tcpfit/original-qdisc.sh" <<EOF
#!/bin/bash
echo restored >> "$work/qdisc-replayed"
EOF
  chmod +x "$INSTALL_DIR/tcpfit/original-qdisc.sh"
  echo '#!/bin/bash' > "$INSTALL_DIR/tcpfit/shape.sh"
  printf 'binary' > "$INSTALL_DIR/flux_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  echo 'net.core.rmem_max = 999999' > "$FLMX_TUNING_SYSCTL_CONF"
  echo 'legacy' > "$FLMX_TUNING_SYSCTL_CONF_LEGACY"
  echo 'tcp_bbr' > "$FLMX_TUNING_BBR_MODULE_FILE"
  echo '[Unit]' > "$FLMX_TUNING_QDISC_UNIT_FILE"
  echo '[Unit]' > "$FLUX_AGENT_SYSTEMD_SERVICE_FILE"

  local systemctl_calls="$work/systemctl-calls" nft_input="$work/nft-input"
  : > "$systemctl_calls"
  systemctl() { echo "$*" >> "$systemctl_calls"; return 0; }
  nft() { cat > "$nft_input"; return 0; }

  local rc="0" out
  out=$(mktemp)
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flux_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R6-02: uninstall with host residue should succeed"
  [[ ! -d "$INSTALL_DIR" ]] || fail "R6-02: install dir should be removed"
  assert_equals "cubic" "$(cat "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control")" \
    "R6-02: sysctl tcp_congestion_control should be restored from tuning backup"
  assert_equals "212992" "$(cat "$FLMX_PROC_SYS_DIR/net/core/rmem_max")" \
    "R6-02: sysctl rmem_max should be restored from tuning backup"
  [[ -f "$work/qdisc-replayed" ]] || fail "R6-02: original-qdisc.sh should be replayed before dir removal"
  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF" ]] || fail "R6-02: 99-flmx-tuning.conf should be removed"
  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF_LEGACY" ]] || fail "R6-02: legacy 99-flvx-tuning.conf should be removed"
  [[ ! -e "$FLMX_TUNING_BBR_MODULE_FILE" ]] || fail "R6-02: flmx-bbr.conf should be removed"
  [[ ! -e "$FLMX_TUNING_QDISC_UNIT_FILE" ]] || fail "R6-02: flmx-tuning-qdisc.service should be removed"
  grep -q 'disable --now flmx-tuning-qdisc.service' "$systemctl_calls" || \
    fail "R6-02: qdisc unit should be disabled before removal"
  local table
  for table in flmx_kernel flvx_kernel flmx_hop_probe; do
    grep -q "delete table inet $table" "$nft_input" || \
      fail "R6-02: nft delete should be issued for table $table"
  done
)

# R6-02：宿主清理逐项尽力而为——nft 删除失败只告警并逐项列明，
# 不中断其余卸载步骤；清理函数对已清状态重复执行幂等无失败。
test_uninstall_flux_agent_host_cleanup_partial_failure_and_idempotent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)
  INSTALL_DIR="$work/agent"
  FLUX_AGENT_SYSTEMD_SERVICE_FILE="$work/flux_agent.service"
  FLMX_TUNING_SYSCTL_CONF="$work/sysctl.d/99-flmx-tuning.conf"
  FLMX_TUNING_SYSCTL_CONF_LEGACY="$work/sysctl.d/99-flvx-tuning.conf"
  FLMX_TUNING_BBR_MODULE_FILE="$work/modules-load.d/flmx-bbr.conf"
  FLMX_TUNING_QDISC_UNIT_FILE="$work/systemd/flmx-tuning-qdisc.service"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  mkdir -p "$INSTALL_DIR" "$(dirname "$FLMX_TUNING_SYSCTL_CONF")"
  printf 'binary' > "$INSTALL_DIR/flux_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  echo 'tuned' > "$FLMX_TUNING_SYSCTL_CONF"
  echo '[Unit]' > "$FLUX_AGENT_SYSTEMD_SERVICE_FILE"

  systemctl() { return 0; }
  nft() { cat >/dev/null; return 1; }

  local rc="0" out
  out=$(mktemp)
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flux_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R6-02: nft failure must not fail the whole uninstall"
  [[ ! -d "$INSTALL_DIR" ]] || fail "R6-02: install dir should still be removed when nft cleanup fails"
  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF" ]] || fail "R6-02: other cleanup items should still run when nft fails"
  grep -q 'nft 表删除失败' "$out" || fail "R6-02: nft failure should be disclosed per item in the receipt"

  # 幂等：对已清状态再次执行清理函数，无失败行、返回 0。
  nft() { cat >/dev/null; return 0; }
  rc="0"
  uninstall_flux_agent_host_cleanup >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R6-02: repeated host cleanup should succeed"
  if grep -q '⚠️' "$out"; then
    fail "R6-02: repeated host cleanup on a clean state should report no failures"
  fi
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
  # C4-F01: 清理现须先过归属判定；本夹具的旧装连接地址与本次面板
  # 地址一致（本面板改名前的旧装），属有归属放行路径。
  SERVER_ADDR="https://mine.example.com:443"
  printf '%s' '{"addr": "https://mine.example.com:443", "secret": "x"}' > "$LEGACY_GOST_CONFIG_DIR/config.json"
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
  # C4-F01: 同 removes 夹具口径——旧装地址与本面板一致才放行清理。
  SERVER_ADDR="https://mine.example.com:443"
  printf '%s' '{"addr": "https://mine.example.com:443", "secret": "x"}' > "$LEGACY_GOST_CONFIG_DIR/config.json"
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

# C4-F01：旧式 gost 清理的归属门——连接地址属于其他面板（或无法
# 判定）时非交互跳过清理：服务文件/二进制逐字保留、不停服、输出
# 含跳过披露与现存地址；TAKEOVER 逃生口与「与现装 agent 地址一
# 致」的更新路径口径则照常放行清理。
test_cleanup_legacy_gost_installation_skips_foreign_agent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  LEGACY_GOST_BINARY=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_ETC=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_LIB=$(mktemp -u)
  LEGACY_GOST_SERVICE_FILE_USR_LIB=$(mktemp -u)
  LEGACY_GOST_CONFIG_DIR=$(mktemp -d)
  SERVICE_MANAGER="systemd"
  SERVER_ADDR="https://mine.example.com:443"
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<EOF
[Unit]
Description=Gost Proxy Service

[Service]
WorkingDirectory=$LEGACY_GOST_CONFIG_DIR
ExecStart=$LEGACY_GOST_CONFIG_DIR/gost
EOF
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$LEGACY_GOST_CONFIG_DIR/config.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost"

  local systemctl_calls=""
  systemctl() {
    systemctl_calls+=$'\n'"$*"
    if [[ "$1" == "list-units" ]]; then
      printf 'gost.service loaded active running\n'
    fi
    return 0
  }

  local out
  out=$(cleanup_legacy_gost_installation </dev/null 2>&1)

  [[ -e "$LEGACY_GOST_BINARY" ]] || fail "C4-F01: foreign legacy gost binary must be preserved"
  [[ -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "C4-F01: foreign legacy gost service file must be preserved"
  [[ -e "$LEGACY_GOST_CONFIG_DIR/gost" ]] || fail "C4-F01: foreign legacy gost dir binary must be preserved"
  [[ "$systemctl_calls" != *"stop gost"* ]] || fail "C4-F01: foreign legacy gost service must not be stopped"
  [[ "$out" == *"已跳过旧式 gost 安装的清理"* ]] || fail "C4-F01: skip must be disclosed in the output"
  [[ "$out" == *"https://other.example.com:443"* ]] || fail "C4-F01: skip disclosure must name the existing addr"
)

test_cleanup_legacy_gost_installation_skips_unreadable_addr() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  LEGACY_GOST_BINARY=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_ETC=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_LIB=$(mktemp -u)
  LEGACY_GOST_SERVICE_FILE_USR_LIB=$(mktemp -u)
  LEGACY_GOST_CONFIG_DIR=$(mktemp -d)
  SERVICE_MANAGER="systemd"
  SERVER_ADDR="https://mine.example.com:443"
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<EOF
[Unit]
WorkingDirectory=$LEGACY_GOST_CONFIG_DIR
ExecStart=$LEGACY_GOST_CONFIG_DIR/gost
EOF
  : > "$LEGACY_GOST_CONFIG_DIR/config.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost.json"

  systemctl() { return 0; }

  local out
  out=$(cleanup_legacy_gost_installation </dev/null 2>&1)

  [[ -e "$LEGACY_GOST_BINARY" ]] || fail "C4-F01: legacy gost with unreadable addr must be preserved (fail-closed)"
  [[ -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "C4-F01: legacy gost service file must be preserved when addr is unreadable"
  [[ "$out" == *"已跳过旧式 gost 安装的清理"* ]] || fail "C4-F01: undecidable ownership skip must be disclosed"
)

test_cleanup_legacy_gost_installation_takeover_cleans_foreign_agent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  LEGACY_GOST_BINARY=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_ETC=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_LIB=$(mktemp -u)
  LEGACY_GOST_SERVICE_FILE_USR_LIB=$(mktemp -u)
  LEGACY_GOST_CONFIG_DIR=$(mktemp -d)
  SERVICE_MANAGER="systemd"
  SERVER_ADDR="https://mine.example.com:443"
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<EOF
[Unit]
WorkingDirectory=$LEGACY_GOST_CONFIG_DIR
ExecStart=$LEGACY_GOST_CONFIG_DIR/gost
EOF
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$LEGACY_GOST_CONFIG_DIR/config.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost.json"

  systemctl() {
    if [[ "$1" == "list-units" ]]; then
      printf 'gost.service loaded active running\n'
    fi
    return 0
  }

  local out
  out=$(FLMX_AGENT_TAKEOVER="1" cleanup_legacy_gost_installation </dev/null 2>&1)

  [[ ! -e "$LEGACY_GOST_BINARY" ]] || fail "C4-F01: takeover should let cleanup remove the legacy binary"
  [[ ! -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "C4-F01: takeover should let cleanup remove the legacy service file"
  [[ "$out" == *"FLMX_AGENT_TAKEOVER=1"* ]] || fail "C4-F01: takeover cleanup must leave a trace line"
)

test_cleanup_legacy_gost_installation_matches_installed_agent_addr() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  LEGACY_GOST_BINARY=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_ETC=$(mktemp)
  LEGACY_GOST_SERVICE_FILE_LIB=$(mktemp -u)
  LEGACY_GOST_SERVICE_FILE_USR_LIB=$(mktemp -u)
  LEGACY_GOST_CONFIG_DIR=$(mktemp -d)
  SERVICE_MANAGER="systemd"
  # 菜单更新上下文：SERVER_ADDR 为空，归属证据取自现装 agent 的
  # 连接地址——与旧式安装一致即同面板旧装，放行清理。
  SERVER_ADDR=""
  INSTALL_DIR=$(mktemp -d)
  printf '%s' '{"addr": "https://mine.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<EOF
[Unit]
WorkingDirectory=$LEGACY_GOST_CONFIG_DIR
ExecStart=$LEGACY_GOST_CONFIG_DIR/gost
EOF
  printf '%s' '{"addr": "https://mine.example.com:443", "secret": "x"}' > "$LEGACY_GOST_CONFIG_DIR/config.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost.json"

  systemctl() {
    if [[ "$1" == "list-units" ]]; then
      printf 'gost.service loaded active running\n'
    fi
    return 0
  }

  cleanup_legacy_gost_installation </dev/null >/dev/null

  [[ ! -e "$LEGACY_GOST_BINARY" ]] || fail "C4-F01: legacy install matching the installed agent addr should be cleaned"
  [[ ! -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "C4-F01: legacy service file should be cleaned when it matches the installed agent addr"
)

test_cleanup_legacy_gost_installation_interactive_confirm() (
  set -euo pipefail

  local work
  work=$(mktemp -d)

  build_fixture() {
    CLEANUP_TEST_BINARY=$(mktemp)
    CLEANUP_TEST_SERVICE=$(mktemp)
    CLEANUP_TEST_CONFIG_DIR=$(mktemp -d)
    cat > "$CLEANUP_TEST_SERVICE" <<EOF
[Unit]
WorkingDirectory=$CLEANUP_TEST_CONFIG_DIR
ExecStart=$CLEANUP_TEST_CONFIG_DIR/gost
EOF
    printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$CLEANUP_TEST_CONFIG_DIR/config.json"
    : > "$CLEANUP_TEST_CONFIG_DIR/gost.json"
    export CLEANUP_TEST_BINARY CLEANUP_TEST_SERVICE CLEANUP_TEST_CONFIG_DIR
  }

  build_fixture
  local harness="$work/cleanup-harness.sh"
  {
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh"
    cat <<'STUBEOF'
LEGACY_GOST_BINARY="$CLEANUP_TEST_BINARY"
LEGACY_GOST_SERVICE_FILE_ETC="$CLEANUP_TEST_SERVICE"
LEGACY_GOST_SERVICE_FILE_LIB="$CLEANUP_TEST_CONFIG_DIR/no-lib-service"
LEGACY_GOST_SERVICE_FILE_USR_LIB="$CLEANUP_TEST_CONFIG_DIR/no-usr-lib-service"
LEGACY_GOST_CONFIG_DIR="$CLEANUP_TEST_CONFIG_DIR"
SERVICE_MANAGER="systemd"
SERVER_ADDR="https://mine.example.com:443"
systemctl() { return 0; }
cleanup_legacy_gost_installation
STUBEOF
  } > "$harness"

  # 交互答 y：放行清理，服务文件被删。
  pty_run $'y\n' "$harness"
  [[ ! -e "$CLEANUP_TEST_SERVICE" ]] || fail "C4-F01: interactive y should let cleanup remove the legacy service file"

  # 交互答 n：跳过清理，服务文件保留。
  build_fixture
  pty_run $'n\n' "$harness"
  [[ -e "$CLEANUP_TEST_SERVICE" ]] || fail "C4-F01: interactive n must preserve the legacy service file"
)

# C4-F02：卸载宿主清理的 legacy 同名共享资源（99-flvx-tuning.conf、
# nft 表 flvx_kernel）在机器上仍有旧式 gost agent 足迹时跳过并披
# 露；足迹消失后按本产品旧装遗留照常清理。flmx 专名资源不受影响。
test_uninstall_flux_agent_host_cleanup_preserves_foreign_legacy_resources() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)
  INSTALL_DIR="$work/agent"
  FLMX_TUNING_SYSCTL_CONF="$work/sysctl.d/99-flmx-tuning.conf"
  FLMX_TUNING_SYSCTL_CONF_LEGACY="$work/sysctl.d/99-flvx-tuning.conf"
  FLMX_TUNING_BBR_MODULE_FILE="$work/modules-load.d/flmx-bbr.conf"
  FLMX_TUNING_QDISC_UNIT_FILE="$work/systemd/flmx-tuning-qdisc.service"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  LEGACY_GOST_CONFIG_DIR="$work/gost"
  LEGACY_GOST_BINARY="$work/bin/gost"
  LEGACY_GOST_SERVICE_FILE_ETC="$work/systemd/gost.service"
  LEGACY_GOST_SERVICE_FILE_LIB="$work/no-lib-service"
  LEGACY_GOST_SERVICE_FILE_USR_LIB="$work/no-usr-lib-service"
  mkdir -p "$INSTALL_DIR" "$(dirname "$FLMX_TUNING_SYSCTL_CONF")" "$LEGACY_GOST_CONFIG_DIR" "$(dirname "$LEGACY_GOST_SERVICE_FILE_ETC")"
  printf 'binary' > "$INSTALL_DIR/flux_agent"
  printf '%s' '{"addr": "https://mine.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  echo 'tuned' > "$FLMX_TUNING_SYSCTL_CONF"
  echo 'tuned-legacy' > "$FLMX_TUNING_SYSCTL_CONF_LEGACY"
  # 旧式 gost 足迹（他人面板现役 agent）：指纹服务文件＋连接配置。
  cat > "$LEGACY_GOST_SERVICE_FILE_ETC" <<EOF
[Unit]
WorkingDirectory=$LEGACY_GOST_CONFIG_DIR
ExecStart=$LEGACY_GOST_CONFIG_DIR/gost
EOF
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$LEGACY_GOST_CONFIG_DIR/config.json"
  : > "$LEGACY_GOST_CONFIG_DIR/gost"

  systemctl() { return 0; }
  nft() { cat > "$work/nft-input"; return 0; }

  local out
  out=$(uninstall_flux_agent_host_cleanup 2>&1)

  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF" ]] || fail "C4-F02: own tuning conf should still be removed"
  [[ -e "$FLMX_TUNING_SYSCTL_CONF_LEGACY" ]] || fail "C4-F02: legacy tuning conf must be preserved while a foreign legacy agent footprint exists"
  [[ "$out" == *"跳过删除可能由其持有的调优文件"* ]] || fail "C4-F02: legacy tuning conf skip must be disclosed"
  grep -q 'flmx_kernel' "$work/nft-input" || fail "C4-F02: own nft tables should still be deleted"
  if grep -q 'flvx_kernel' "$work/nft-input"; then
    fail "C4-F02: flvx_kernel must not be deleted while a foreign legacy agent footprint exists"
  fi
  [[ "$out" == *"跳过删除与其同名的 nft 表"* ]] || fail "C4-F02: flvx_kernel skip must be disclosed"

  # 足迹消失（旧式 agent 已被其所属面板卸载）后重跑：legacy 资源
  # 按本产品旧装遗留照常清理。
  rm -f "$LEGACY_GOST_SERVICE_FILE_ETC" "$LEGACY_GOST_CONFIG_DIR/config.json" "$LEGACY_GOST_CONFIG_DIR/gost"
  out=$(uninstall_flux_agent_host_cleanup 2>&1)

  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF_LEGACY" ]] || fail "C4-F02: legacy tuning conf should be removed once no legacy footprint remains"
  grep -q 'flvx_kernel' "$work/nft-input" || fail "C4-F02: flvx_kernel should be deleted once no legacy footprint remains"
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

# R9-03（菜单 2 面）: 更新前备份失败时回执必须点名中止；且仅当后
# 端容器不存在（首装未完成、容器从未创建的半成品状态）时才点名
# 唯一出口 PANEL_FORCE_REINSTALL=1 并写明用法——容器存在的常规
# 部署备份失败（磁盘/权限）不得被误导去强制重装。旧实现备份失败
# 静默 return 1，两相均无点名（本用例对旧代码必红）。
test_update_panel_backup_failure_names_force_reinstall_only_when_container_missing() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  check_docker() { DOCKER_CMD="true"; }
  resolve_panel_deployment() { PANEL_DEPLOY_DIR=$(mktemp -d); }
  validate_panel_update_environment() { :; }
  get_current_db_type() { echo "sqlite"; }
  resolve_latest_release_tag() { echo "v-test"; }
  download_panel_update_compose() { UPDATE_COMPOSE_CANDIDATE="candidate"; }
  validate_panel_update_compose() { :; }
  create_panel_update_backup() { return 1; }
  check_ipv6_support() { return 1; }

  # 相一：后端容器不存在（半成品状态）→ 点名唯一出口与用法
  docker() { return 1; }
  local rc="0" log1 log2
  log1=$(mktemp)
  update_panel >"$log1" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R9-03: update must abort when the pre-update backup fails"
  grep -q "更新前备份失败，更新已中止" "$log1" || \
    fail "R9-03: backup failure receipt must state the update was aborted"
  grep -q "后端容器不存在" "$log1" || \
    fail "R9-03: receipt must name the missing-container state when the backend container does not exist"
  grep -q "PANEL_FORCE_REINSTALL=1 bash panel_install.sh" "$log1" || \
    fail "R9-03: receipt must name the force-reinstall exit with its invocation form"

  # 相二：后端容器存在 → 只报中止、不得点名强制重装
  docker() { return 0; }
  rc="0"
  log2=$(mktemp)
  update_panel >"$log2" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R9-03: update must abort when the pre-update backup fails (container present)"
  grep -q "更新前备份失败，更新已中止" "$log2" || \
    fail "R9-03: backup failure receipt must state the update was aborted (container present)"
  if grep -q "PANEL_FORCE_REINSTALL" "$log2"; then
    fail "R9-03: force-reinstall must not be named when the backend container exists (not the remedy for ordinary backup failures)"
  fi
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
        *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flvx-flux-panel-backend:latest" ;;
        *) printf '%s\n' "flvx-panel" ;;
      esac
      return 0
    fi
    # 只有存量后端容器存在；新名容器不存在（否则会判为两代同现）。
    if [[ "$1" == "inspect" && "$2" == "flmx-panel-backend" ]]; then
      return 1
    fi
    return 0
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

# I-F1-05: FRONTEND_PORT 引号值两面统一判读。同一份写了
# FRONTEND_PORT="7399" 的 .env：菜单面（resolve_caddy_frontend_port）
# 不得再剥引号静默正常化采纳（须点名无效并回退默认），更新面
# （validate_panel_update_environment）维持拒绝；无引号合法值两面
# 同采。旧实现菜单面剥引号采纳 7399（本用例对旧代码必红）。
test_frontend_port_quoted_value_consistent_across_faces() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local deploy rc="0"
  deploy=$(mktemp -d)
  printf 'JWT_SECRET=x\nBACKEND_PORT=6365\nFRONTEND_PORT="7399"\nDB_TYPE=sqlite\n' > "$deploy/.env"
  printf 'services: {}\n' > "$deploy/docker-compose.yml"
  cd "$deploy"
  DOCKER_CMD="true"

  # 菜单面：不采纳引号值、点名无效、回退默认 6366
  PANEL_DEPLOY_DIR="$deploy"
  FRONTEND_PORT=""
  resolve_caddy_frontend_port > menu.log 2>&1
  assert_equals "6366" "$FRONTEND_PORT" "I-F1-05: menu face must not silently normalize a quoted FRONTEND_PORT"
  grep -q "不是有效端口" menu.log || \
    fail "I-F1-05: menu face must name the invalid quoted value instead of skipping it silently"

  # 更新面：同一文件拒绝终止
  validate_panel_update_environment > validate.log 2>&1 || rc="$?"
  assert_equals "1" "$rc" "I-F1-05: update face must reject the quoted FRONTEND_PORT"
  grep -q "FRONTEND_PORT 不是有效端口" validate.log || \
    fail "I-F1-05: update face rejection must name FRONTEND_PORT"

  # 对照：无引号合法值两面同采
  printf 'JWT_SECRET=x\nBACKEND_PORT=6365\nFRONTEND_PORT=7399\nDB_TYPE=sqlite\n' > "$deploy/.env"
  FRONTEND_PORT=""
  resolve_caddy_frontend_port > menu2.log 2>&1
  assert_equals "7399" "$FRONTEND_PORT" "I-F1-05: menu face must adopt an unquoted valid port"
  rc="0"
  validate_panel_update_environment >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "I-F1-05: update face must accept an unquoted valid port"
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

# R9-11: SQLite 备份必须排除可弃的 github-cache（单份最高约 2GB
# 纯缓存），DB 本体与 jwt_secret 照常入备。旧实现整目录复制、
# github-cache 在档（本用例对旧代码必红）。
test_backup_sqlite_for_update_excludes_github_cache() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local destination
  destination=$(mktemp -d)/sqlite
  PANEL_BACKEND_CONTAINER="flux-panel-backend"
  docker() {
    case "$1" in
      inspect) printf 'true\n' ;;
      cp)
        local target="${@: -1}"
        mkdir -p "$target/github-cache"
        printf 'db-bytes' > "$target/gost.db"
        printf 'secret' > "$target/jwt_secret"
        printf 'cache-blob' > "$target/github-cache/blob"
        ;;
      *) return 0 ;;
    esac
  }

  backup_sqlite_for_update "$destination" >/dev/null
  [[ -f "$destination/gost.db" ]] || \
    fail "R9-11: SQLite backup must contain the database file"
  [[ -f "$destination/jwt_secret" ]] || \
    fail "R9-11: SQLite backup must contain jwt_secret"
  [[ ! -e "$destination/github-cache" ]] || \
    fail "R9-11: SQLite backup must exclude the disposable github-cache directory"
)

# R9-11: 更新备份保留轮换——连打 K+2 次（K=3）后 backups/ 恰剩
# 最近 3 份、最旧两份被删、最新一份在位。旧实现无轮换、留 5 份
# （本用例对旧代码必红）。
test_create_panel_update_backup_rotates_to_keep() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local deploy i
  deploy=$(mktemp -d)
  PANEL_DEPLOY_DIR="$deploy"
  printf 'JWT_SECRET=x\n' > "$deploy/.env"
  printf 'services: {}\n' > "$deploy/docker-compose.yml"
  cd "$deploy"
  backup_sqlite_for_update() { mkdir -p "$1"; printf 'db' > "$1/gost.db"; }
  backup_postgres_for_update() { printf 'sql' > "$1"; }

  local -a names=()
  for i in 1 2 3 4 5; do
    create_panel_update_backup sqlite >/dev/null
    names+=("$(basename "$UPDATE_BACKUP_DIR")")
    sleep 1
  done

  assert_equals "3" "$(ls -d "$deploy"/backups/panel-update-* | wc -l)" \
    "R9-11: update backups must rotate down to the retention keep count after K+2 updates"
  [[ ! -e "$deploy/backups/${names[0]}" && ! -e "$deploy/backups/${names[1]}" ]] || \
    fail "R9-11: the two oldest update backups must be pruned"
  [[ -d "$deploy/backups/${names[2]}" && -d "$deploy/backups/${names[3]}" && -d "$deploy/backups/${names[4]}" ]] || \
    fail "R9-11: the three newest update backups (including the just-created one) must survive rotation"
)

# R9-11: 强制重装的带时间戳备份家族（.env.bak.<ts>）与更新备份同
# 轮换口径——prune_timestamped_backups 按名（时间戳序）只保留最
# 近 keep 份。旧实现无此函数（本用例对旧代码必红）。
test_prune_timestamped_backups_keeps_newest() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work ts
  work=$(mktemp -d)
  cd "$work"
  for ts in 20260101000001 20260101000002 20260101000003 20260101000004 20260101000005; do
    printf 'x' > ".env.bak.$ts"
  done
  prune_timestamped_backups 3 .env.bak.*
  assert_equals "3" "$(ls .env.bak.* | wc -l)" \
    "R9-11: timestamped backups must rotate down to the keep count"
  [[ -f .env.bak.20260101000003 && -f .env.bak.20260101000004 && -f .env.bak.20260101000005 ]] || \
    fail "R9-11: the newest timestamped backups must survive rotation"
  [[ ! -e .env.bak.20260101000001 && ! -e .env.bak.20260101000002 ]] || \
    fail "R9-11: the oldest timestamped backups must be pruned"
)

# I-C4-06 钉格（fix10 S8）：轮换排序键是 mtime 而非名字典序。名字
# 携带未来时间戳的备份（墙钟曾被拨快期间生成、其后又被以旧 mtime
# 拷回/还原入位等形态）不得凭名字永久占住保留槽：构造未来戳条目
# mtime 最旧、3 份真实备份 mtime 递增，keep=3 时未来戳被清、真实
# 3 份全在。旧实现按名排序时本用例必红（2099 名最大必留、真实最
# 旧者反被删）。文件族（.env.bak.*）与目录族（panel-update-*）同
# 函数同口径，两形都钉。
test_prune_timestamped_backups_future_name_old_mtime_pruned() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work ts
  work=$(mktemp -d)
  cd "$work"
  printf 'x' > ".env.bak.20990101000000"
  touch -d '2026-01-01 00:00:00' ".env.bak.20990101000000"
  for ts in 20260101000001 20260101000002 20260101000003; do
    printf 'x' > ".env.bak.$ts"
  done
  touch -d '2026-01-02 00:00:00' ".env.bak.20260101000001"
  touch -d '2026-01-03 00:00:00' ".env.bak.20260101000002"
  touch -d '2026-01-04 00:00:00' ".env.bak.20260101000003"
  prune_timestamped_backups 3 .env.bak.*
  assert_equals "3" "$(ls .env.bak.* | wc -l)" \
    "I-C4-06: timestamped backups must rotate down to the keep count"
  [[ ! -e .env.bak.20990101000000 ]] || \
    fail "I-C4-06: a future-named backup with the oldest mtime must not occupy a retention slot"
  [[ -f .env.bak.20260101000001 && -f .env.bak.20260101000002 && -f .env.bak.20260101000003 ]] || \
    fail "I-C4-06: the three real backups must survive rotation"

  mkdir -p backups
  cd backups
  mkdir "panel-update-20990101-000000.skewed"
  touch -d '2026-01-01 00:00:00' "panel-update-20990101-000000.skewed"
  for ts in 20260102-000000 20260103-000000 20260104-000000; do
    mkdir "panel-update-$ts"
  done
  touch -d '2026-01-02 00:00:00' "panel-update-20260102-000000"
  touch -d '2026-01-03 00:00:00' "panel-update-20260103-000000"
  touch -d '2026-01-04 00:00:00' "panel-update-20260104-000000"
  prune_timestamped_backups 3 panel-update-*
  assert_equals "3" "$(ls -d panel-update-* | wc -l)" \
    "I-C4-06: update backup dirs must rotate down to the keep count"
  [[ ! -e "panel-update-20990101-000000.skewed" ]] || \
    fail "I-C4-06: a future-named backup dir with the oldest mtime must not occupy a retention slot"
  [[ -d "panel-update-20260102-000000" && -d "panel-update-20260103-000000" && -d "panel-update-20260104-000000" ]] || \
    fail "I-C4-06: the three real backup dirs must survive rotation"
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

# R9-04: 自更新覆写失败时 $0 必须逐字不动。cat 桩仅对覆写形态注
# 入 ENOSPC（参数是已下载的新脚本副本：内容含哨兵标记、且不是夹
# 具源文件本身）；shell 重定向在 cat 运行前已执行——旧实现
# cat > $0 会先把 $0 截断为 0 字节（本用例对旧代码必红），新实
# 现写同目录临时文件、比对后 rename，$0 不动且回执成立。
panel_self_update_overwrite_fail_fixture() {
  local work stubbin good_script
  work=$(mktemp -d)
  stubbin=$(mktemp -d)
  good_script="$work/good_panel.sh"
  cat > "$good_script" <<EOF
#!/bin/bash
SCRIPT_VERSION="99999999.99"
PINNED_VERSION="9.9.9"
echo "updated" > "\$SELF_UPDATE_SENTINEL"
EOF
  STUB_SHA256=$(sha256sum "$good_script" | awk '{print $1}')
  export STUB_SHA256
  export STUB_GOOD_SCRIPT="$good_script"
  REAL_CAT=$(command -v cat)
  export REAL_CAT
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
  cat > "$stubbin/cat" <<'EOF'
#!/bin/bash
for a in "$@"; do
  if [[ -f "$a" && "$a" != "$STUB_GOOD_SCRIPT" ]] && grep -q 'SELF_UPDATE_SENTINEL' "$a" 2>/dev/null; then
    echo "cat: write error: No space left on device" >&2
    exit 1
  fi
done
exec "$REAL_CAT" "$@"
EOF
  chmod +x "$stubbin/curl" "$stubbin/cat"
  cp "$ROOT_DIR/panel_install.sh" "$work/panel_install.sh"
  export SELF_UPDATE_SENTINEL="$work/sentinel"
  rm -f "$SELF_UPDATE_SENTINEL"
  ( cd "$work" && printf '6\n' | PATH="$stubbin:$PATH" PROXY_ENABLED=false bash ./panel_install.sh > "$work/run.log" 2>&1 ) || true
  echo "$work"
}

test_panel_install_self_update_overwrite_failure_keeps_script() {
  local work
  work=$(panel_self_update_overwrite_fail_fixture)
  [[ ! -f "$work/sentinel" ]] || fail "R9-04: a failed overwrite must not execute the new script (sentinel present)"
  cmp -s "$ROOT_DIR/panel_install.sh" "$work/panel_install.sh" || \
    fail "R9-04: \$0 must remain byte-identical when the self-update overwrite fails"
  grep -q "自动更新失败，继续使用当前版本" "$work/run.log" || \
    fail "R9-04: the failure receipt must be printed (and is now true: the current script is intact)"
  echo "ok: self-update overwrite failure keeps current script intact"
}

test_panel_install_self_update_overwrite_failure_keeps_script

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
  # R8-011: install 落盘前新增 compose 内容校验——夹具须写出含
  # backend/frontend 的最小 compose，docker 桩须应答 --services。
  download_verified() {
    printf 'services:\n  backend:\n    image: example/backend:9.9.9\n  frontend:\n    image: example/frontend:9.9.9\n' > "$2"
    return 0
  }
  # R8-J-C: install_panel 完成块前新增 wait_for_backend_healthy 把关，
  # 夹具以恒成功桩承接（本例验的是 F-049 守卫，不验健康等待本身）。
  wait_for_backend_healthy() { return 0; }
  check_ipv6_support() { return 1; }
  configure_docker_ipv6() { return 0; }
  docker_ipv6_usable() { return 1; }
  get_public_ip() { echo "203.0.113.1"; }
  docker() {
    case " $* " in
      *" --services "*) printf 'backend\nfrontend\n' ;;
    esac
    return 0
  }

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
  # R8-011: 同 F-049 夹具——内容校验需要真实落盘的最小 compose 与
  # docker 桩的 --services 应答，否则健康等待把关轮不到执行。
  download_verified() {
    printf 'services:\n  backend:\n    image: example/backend:9.9.9\n  frontend:\n    image: example/frontend:9.9.9\n' > "$2"
    return 0
  }
  check_ipv6_support() { return 1; }
  configure_docker_ipv6() { return 0; }
  docker_ipv6_usable() { return 1; }
  get_public_ip() { echo "203.0.113.1"; }
  docker() {
    case " $* " in
      *" --services "*) printf 'backend\nfrontend\n' ;;
    esac
    return 0
  }

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

  # 健康等待通过：正常印成功块。R8-011 后 compose 会真实落盘，第一
  # 相跑完目录内已有完整部署，第二相须换新目录，否则会先被 F-049
  # 已有部署守卫拦截（与健康把关无关）。
  wait_for_backend_healthy() { return 0; }
  rc="0"
  out_log="$work/ok.log"
  local work_ok
  work_ok=$(mktemp -d)
  ( cd "$work_ok" && install_panel >"$out_log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R8-J-C: install_panel should succeed once the backend is healthy"
  grep -q "部署完成" "$out_log" || \
    fail "R8-J-C: success block should be printed after the health gate passes"
)

# R9-03（菜单 1 面）: F-049 守卫的拒绝回执除提及变量外，必须写明
# 强制重装的具体用法（终端命令形态＋选菜单 1），且点明首装半成
# 品状态只能走此出口。旧回执只有「设置 … 后重试」的泛述、无命
# 令形态（本用例对旧代码必红）。
test_install_panel_refusal_names_force_reinstall_usage() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work rc="0"
  work=$(mktemp -d)
  printf 'JWT_SECRET=keepme\nPOSTGRES_PASSWORD=keeppw\n' > "$work/.env"
  printf 'services: {}\n' > "$work/docker-compose.yml"

  ( cd "$work" && install_panel > refuse.log 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "R9-03: install_panel must still refuse when a deployment exists"
  grep -q "PANEL_FORCE_REINSTALL=1 bash panel_install.sh" "$work/refuse.log" || \
    fail "R9-03: refusal receipt must name the force-reinstall invocation form"
  grep -q "安装面板" "$work/refuse.log" || \
    fail "R9-03: refusal receipt must say to re-run and choose the install menu entry"
  grep -q "keepme" "$work/.env" || fail "R9-03: refusal must not modify the existing .env"
)

# R9-03（首装失败面）: 首装在 up 步失败（容器从未创建）必须落回
# 执：宣告部署未完成、点名唯一出口与用法，不得靠 set -e 静默退
# 出。旧实现裸 up 调用、失败零指引（本用例对旧代码必红）。
test_install_panel_up_failure_names_force_reinstall() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work rc="0"
  work=$(mktemp -d)
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
  download_verified() {
    printf 'services:\n  backend:\n    image: example/backend:9.9.9\n  frontend:\n    image: example/frontend:9.9.9\n' > "$2"
    return 0
  }
  check_ipv6_support() { return 1; }
  configure_docker_ipv6() { return 0; }
  docker_ipv6_usable() { return 1; }
  get_public_ip() { echo "203.0.113.1"; }
  docker() {
    case " $* " in
      *" --services "*) printf 'backend\nfrontend\n' ;;
      *" up "*) return 1 ;;
    esac
    return 0
  }
  wait_for_backend_healthy() { return 1; }

  ( cd "$work" && install_panel > up-fail.log 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R9-03: install_panel must fail when compose up fails"
  grep -q "部署未完成" "$work/up-fail.log" || \
    fail "R9-03: up failure must print a deployment-incomplete receipt instead of dying silently under set -e"
  grep -q "PANEL_FORCE_REINSTALL=1 bash panel_install.sh" "$work/up-fail.log" || \
    fail "R9-03: up failure receipt must name the force-reinstall exit with its invocation form"
  if grep -q "🎉" "$work/up-fail.log"; then
    fail "R9-03: success block must not be printed when compose up fails"
  fi
)

# I-F1-06: install_panel 的 PG 健康等待失败必须与同函数后端支同
# 形：宣告部署未完成＋logs 指引＋后续路径，且中止在后端启动之
# 前。旧实现裸调用、失败只有等待函数自身的超时两行（本用例对旧
# 代码必红）。
test_install_panel_postgres_health_failure_has_guidance() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work rc="0"
  work=$(mktemp -d)
  ensure_compose_urls_initialized() {
    DOCKER_COMPOSEV4_URL="https://example.com/docker-compose-v4.yml"
    DOCKER_COMPOSEV6_URL="https://example.com/docker-compose-v6.yml"
    return 0
  }
  check_docker() { DOCKER_CMD="docker compose"; return 0; }
  get_config_params() {
    FRONTEND_PORT=6366; BACKEND_PORT=6365; RESOLVED_VERSION="9.9.9"
    DB_TYPE="postgres"; DATABASE_URL="postgresql://flux_panel:pw@postgres:5432/flux_panel?sslmode=disable"
    POSTGRES_DB="flux_panel"; POSTGRES_USER="flux_panel"
    POSTGRES_PASSWORD="pw"; JWT_SECRET="secret"
  }
  download_verified() {
    printf 'services:\n  backend:\n    image: example/backend:9.9.9\n  frontend:\n    image: example/frontend:9.9.9\n' > "$2"
    return 0
  }
  check_ipv6_support() { return 1; }
  configure_docker_ipv6() { return 0; }
  docker_ipv6_usable() { return 1; }
  get_public_ip() { echo "203.0.113.1"; }
  docker() {
    case " $* " in
      *" --services "*) printf 'backend\nfrontend\n' ;;
    esac
    return 0
  }
  wait_for_postgres_healthy() { return 1; }
  wait_for_backend_healthy() { return 0; }

  ( cd "$work" && install_panel > pg-fail.log 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "I-F1-06: install_panel must fail when PostgreSQL never becomes healthy"
  grep -q "部署未完成：PostgreSQL 未通过健康检查" "$work/pg-fail.log" || \
    fail "I-F1-06: PG health failure must print a deployment-incomplete receipt like the backend branch"
  grep -q "logs postgres" "$work/pg-fail.log" || \
    fail "I-F1-06: PG health failure must print a logs pointer like the backend branch"
  if grep -q "🎉" "$work/pg-fail.log"; then
    fail "I-F1-06: success block must not be printed when the PG health gate fails"
  fi
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

# R6-01（行为半）：兜底成功路径——官方脚本 pin 校验不过、包管理器
# 桩成成功应答时，install_docker 必须经包管理器兜底返回 0 并走到
# 装成判定。F-050 只桩了全失败形态，成功路径此前从未被验过。断言
# 按本机发行版分支取（CI/沙箱为 Debian 支）：Debian 支必须装
# docker.io、且请求清单中不得出现发行源不存在的
# docker-compose-plugin；RHEL 支必须先落官方源再装 docker-ce。
test_install_docker_fallback_succeeds_with_working_package_managers() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local sentinel pm_log
  sentinel=$(mktemp -u)
  pm_log=$(mktemp)
  export DOCKER_INSTALL_SENTINEL="$sentinel"
  export DOCKER_PM_LOG="$pm_log"
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
  apt-get() { printf 'apt-get %s\n' "$*" >> "$DOCKER_PM_LOG"; return 0; }
  dnf() { printf 'dnf %s\n' "$*" >> "$DOCKER_PM_LOG"; return 0; }
  yum() { printf 'yum %s\n' "$*" >> "$DOCKER_PM_LOG"; return 0; }
  apk() { printf 'apk %s\n' "$*" >> "$DOCKER_PM_LOG"; return 0; }

  local rc="0"
  ( install_docker >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R6-01: install_docker must succeed via the package-manager fallback when the package manager succeeds"
  [[ ! -f "$sentinel" ]] || fail "R6-01: unverified Docker installer script must not be executed on the fallback path either"
  [[ -s "$pm_log" ]] || fail "R6-01: fallback must actually invoke a package manager"
  if [[ -f /etc/debian_version ]]; then
    grep -q 'docker\.io' "$pm_log" || \
      fail "R6-01: Debian fallback must install docker.io"
    if grep -q 'docker-compose-plugin' "$pm_log"; then
      fail "R6-01: Debian fallback must not request docker-compose-plugin (absent from distro repos, fails the whole apt command)"
    fi
    grep -Eq 'docker-compose-v2|docker-compose' "$pm_log" || \
      fail "R6-01: Debian fallback must install a compose package that exists in distro repos"
  elif [[ -f /etc/redhat-release ]]; then
    grep -q 'docker-ce\.repo' "$pm_log" || \
      fail "R6-01: RHEL fallback must add the Docker official repo before installing"
    grep -q 'docker-ce' "$pm_log" || \
      fail "R6-01: RHEL fallback must install docker-ce from the official repo"
  elif [[ -f /etc/alpine-release ]]; then
    grep -q 'docker' "$pm_log" || \
      fail "R6-01: Alpine fallback must install docker"
  fi
)

# R6-01（静态半）：以真实包名清单为断言钉住 install_docker 函数体
# ——apt 的 install 行不得再含发行源不存在的 docker-compose-plugin；
# 旧 RHEL 坏支签名「install -y docker 」不得在函数内复现；RHEL 支
# 必须出现官方源地址（先落源、再装官方包名）。
test_install_docker_fallback_package_names_exist_in_repos() (
  set -euo pipefail
  local body
  body=$(awk '/^install_docker\(\) \{/,/^\}/' "$ROOT_DIR/panel_install.sh")
  [[ -n "$body" ]] || fail "R6-01: install_docker body must be extractable"
  if grep 'apt-get install' <<< "$body" | grep -q 'docker-compose-plugin'; then
    fail "R6-01: the apt fallback line must not contain docker-compose-plugin (not in Debian/Ubuntu distro repos)"
  fi
  grep -q 'docker\.io' <<< "$body" || \
    fail "R6-01: Debian fallback must install docker.io"
  grep -Eq 'docker-compose-v2|docker-compose' <<< "$body" || \
    fail "R6-01: Debian fallback must name a distro-repo compose package"
  if grep -q 'install -y docker ' <<< "$body"; then
    fail "R6-01: the old RHEL fallback form (install -y docker ...) must not reappear"
  fi
  grep -q 'download\.docker\.com/linux/centos/docker-ce\.repo' <<< "$body" || \
    fail "R6-01: RHEL fallback must add the Docker official repo before using official package names"
)

# R7-02（行为半）：兜底各支装后必须启动守护进程——包管理器与服务
# 管理器桩共用一份保序 argv 日志，逐发行版断言：装包成功后、返回
# 0 前出现本发行版的启动动作（systemd 支 systemctl enable --now；
# Alpine 支 rc-update 入默认运行级＋rc-service 启动、且不得出现
# systemctl）。发行版分支按 /etc 标记文件选择，这里以 `[` 函数覆
# 盖只拦截这三个探针在沙箱内逐支驱动（其余 `[` 调用委托 builtin
# test 并剥掉尾随 ]），结论与宿主发行版无关。RHEL 的 yum 子支
# （无 dnf 时）同口径、同断言。
test_install_docker_fallback_starts_daemon_all_distros() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work sentinel argv_log
  work=$(mktemp -d)
  sentinel="$work/SENTINEL"
  argv_log="$work/argv.log"
  export DOCKER_INSTALL_SENTINEL="$sentinel"
  export DOCKER_ARGV_LOG="$argv_log"
  curl() {
    local out="" prev=""
    for a in "$@"; do
      if [[ "$prev" == "-o" ]]; then out="$a"; fi
      prev="$a"
    done
    if [[ -n "$out" ]]; then
      printf '#!/bin/sh\ntouch "%s"\n' "$DOCKER_INSTALL_SENTINEL" > "$out"
    fi
    return 0
  }
  apt-get()   { printf 'apt-get %s\n'   "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  dnf()       { printf 'dnf %s\n'       "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  yum()       { printf 'yum %s\n'       "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  yum-config-manager() { printf 'yum-config-manager %s\n' "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  apk()       { printf 'apk %s\n'       "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  systemctl() { printf 'systemctl %s\n' "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  service()   { printf 'service %s\n'   "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  rc-update() { printf 'rc-update %s\n' "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  rc-service(){ printf 'rc-service %s\n' "$*" >> "$DOCKER_ARGV_LOG"; return 0; }
  [() {
    if [[ "${1:-}" == "-f" ]]; then
      case "${2:-}" in
        /etc/debian_version) [[ "${SIM_DISTRO:-}" == "debian" ]]; return ;;
        /etc/redhat-release) [[ "${SIM_DISTRO:-}" == "rhel" ]]; return ;;
        /etc/alpine-release) [[ "${SIM_DISTRO:-}" == "alpine" ]]; return ;;
      esac
    fi
    builtin test "${@:1:$#-1}"
  }

  local distro rc install_line start_line
  for distro in debian rhel alpine; do
    : > "$argv_log"
    SIM_DISTRO="$distro"
    rc="0"
    ( install_docker >/dev/null 2>&1 ) || rc="$?"
    assert_equals "0" "$rc" "R7-02: $distro fallback install_docker should succeed"
    [[ ! -f "$sentinel" ]] || fail "R7-02: unverified installer script must not be executed ($distro)"
    case "$distro" in
      debian)
        install_line=$(grep -n 'apt-get install' "$argv_log" | head -1 | cut -d: -f1 || true)
        start_line=$(grep -n '^systemctl enable --now docker$' "$argv_log" | head -1 | cut -d: -f1 || true)
        [[ -n "$install_line" ]] || fail "R7-02: Debian fallback must install packages"
        [[ -n "$start_line" ]] || fail "R7-02: Debian fallback must start the daemon (systemctl enable --now docker)"
        ;;
      rhel)
        install_line=$(grep -n 'dnf install -y docker-ce' "$argv_log" | head -1 | cut -d: -f1 || true)
        start_line=$(grep -n '^systemctl enable --now docker$' "$argv_log" | head -1 | cut -d: -f1 || true)
        [[ -n "$install_line" ]] || fail "R7-02: RHEL fallback must install docker-ce"
        [[ -n "$start_line" ]] || fail "R7-02: RHEL fallback must start the daemon (systemctl enable --now docker)"
        ;;
      alpine)
        install_line=$(grep -n 'apk add' "$argv_log" | head -1 | cut -d: -f1 || true)
        start_line=$(grep -n '^rc-service docker start$' "$argv_log" | head -1 | cut -d: -f1 || true)
        [[ -n "$install_line" ]] || fail "R7-02: Alpine fallback must install packages"
        grep -q '^rc-update add docker default$' "$argv_log" || \
          fail "R7-02: Alpine fallback must add docker to the OpenRC default runlevel"
        [[ -n "$start_line" ]] || fail "R7-02: Alpine fallback must start the daemon (rc-service docker start)"
        if grep -q '^systemctl ' "$argv_log"; then
          fail "R7-02: Alpine fallback must use OpenRC, not systemctl"
        fi
        ;;
    esac
    [[ "$start_line" -gt "$install_line" ]] || \
      fail "R7-02: $distro daemon start must come after the package install"
  done

  # RHEL 的 yum 子支（PATH/函数面上无 dnf 时）同口径：装后同样启动。
  unset -f dnf
  : > "$argv_log"
  SIM_DISTRO="rhel"
  rc="0"
  ( install_docker >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R7-02: rhel/yum fallback install_docker should succeed"
  install_line=$(grep -n 'yum install -y docker-ce' "$argv_log" | head -1 | cut -d: -f1 || true)
  start_line=$(grep -n '^systemctl enable --now docker$' "$argv_log" | head -1 | cut -d: -f1 || true)
  [[ -n "$install_line" && -n "$start_line" && "$start_line" -gt "$install_line" ]] || \
    fail "R7-02: RHEL/yum fallback must install docker-ce and then start the daemon"
  unset -f '['
)

# R7-02（复检半）：安装路径的 check_docker 必须验 daemon 可达——
# CLI 与 compose 在场、daemon 不可达时必须以点名 daemon 的文案失
# 败退出，不许以「检测到命令」放行到后面的健康等待；daemon 可达
# 时才放行。两个入口形态都验：已装检出（docker 桩预置在 PATH
# 上）与装后复检（入口 PATH 无 docker、apt 桩在 install 时把
# docker 桩落进 PATH 目录、模拟装包产物落盘）。docker 桩的 info
# 子命令以标志文件控制 daemon 在/不在。
test_check_docker_requires_reachable_daemon_on_install_path() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work stubbin sysbin sealed_path cli_log daemon_flag
  work=$(mktemp -d)
  stubbin="$work/bin"
  sysbin="$work/sysbin"
  mkdir -p "$stubbin" "$sysbin"
  # 密封 PATH（CI 定谳后立）：本格全部 check_docker 调用只走
  # stubbin＋sysbin，不继承宿主 $PATH。GitHub runner 与一般宿主
  # 机自带在跑的 docker——形态二删桩后若把宿主 PATH 后缀带进
  # 来，check_docker 会检出宿主 docker、对活 daemon 探活成功，
  # 以 rc=0 假绿（CI 实测：expected 1, actual 0；同一形态在无
  # docker 的容器内则全过，红只取决于宿主 PATH 里有没有活
  # docker，与产品复检逻辑无关）。sysbin 是符号链接农场：只放
  # 被测路径真正执行的宿主工具（官方脚本校验的 sha256sum/
  # mktemp/awk/rm 与 apt 桩落桩用的 cp/chmod），缺一即点名失
  # 败，不许静默退回未密封形态。
  local tool tool_path
  for tool in sha256sum mktemp awk rm cp chmod; do
    tool_path=$(command -v "$tool" || true)
    [[ -n "$tool_path" ]] || \
      fail "R7-02: cannot seal PATH, core tool missing: $tool"
    ln -s "$tool_path" "$sysbin/$tool"
  done
  sealed_path="$stubbin:$sysbin"
  cli_log="$work/docker-cli.log"
  daemon_flag="$work/daemon-up"
  export DOCKER_CLI_LOG="$cli_log"
  export DAEMON_UP_FLAG="$daemon_flag"
  export DOCKER_STUB_SRC="$work/docker-stub"
  export DOCKER_STUBBIN="$stubbin"
  cat > "$DOCKER_STUB_SRC" <<'EOF'
#!/bin/bash
printf 'docker %s\n' "$*" >> "$DOCKER_CLI_LOG"
case "${1:-}" in
  compose) exit 0 ;;
  info) [[ -f "$DAEMON_UP_FLAG" ]] && exit 0 || exit 1 ;;
  *) exit 0 ;;
esac
EOF
  # 官方脚本校验不过（同 F-050 形态）使 install_docker 落兜底。
  curl() {
    local out="" prev=""
    for a in "$@"; do
      if [[ "$prev" == "-o" ]]; then out="$a"; fi
      prev="$a"
    done
    if [[ -n "$out" ]]; then printf '#!/bin/sh\nexit 0\n' > "$out"; fi
    return 0
  }
  systemctl() { return 0; }
  service() { return 0; }
  # apt 桩：install 时把 docker 桩落进 stubbin，模拟装包产物落盘。
  apt-get() {
    if [[ "$*" == *install* ]]; then
      cp "$DOCKER_STUB_SRC" "$DOCKER_STUBBIN/docker"
      chmod +x "$DOCKER_STUBBIN/docker"
    fi
    return 0
  }

  local rc out
  # 形态一：已装检出。daemon 不可达 → 退出 1 且点名守护进程。
  cp "$DOCKER_STUB_SRC" "$stubbin/docker"
  chmod +x "$stubbin/docker"
  rm -f "$daemon_flag"
  hash -r
  rc="0"; out=""
  out=$( PATH="$sealed_path" check_docker 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R7-02: check_docker must fail when the daemon is unreachable"
  grep -q '守护进程（daemon）不可达' <<< "$out" || \
    fail "R7-02: daemon-unreachable failure must name the daemon"
  grep -q '^docker info' "$cli_log" || \
    fail "R7-02: check_docker must probe the daemon via docker info"
  # daemon 可达 → 放行并报出命令。
  touch "$daemon_flag"
  hash -r
  rc="0"; out=""
  out=$( PATH="$sealed_path" check_docker 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R7-02: check_docker must pass when the daemon is reachable"
  grep -q '检测到 Docker 命令' <<< "$out" || \
    fail "R7-02: check_docker should report the detected command once the daemon is verified"

  # 形态二：装后复检。入口 PATH 无 docker（删桩），兜底经 apt 桩
  # 装出 docker 桩；daemon 仍不可达 → 复检退出 1、点名 daemon。
  # 「入口无 docker」由密封 PATH 构造保证（删桩＋不继承宿主
  # PATH），不依赖运行机恰好没装 docker。
  [() {
    if [[ "${1:-}" == "-f" ]]; then
      case "${2:-}" in
        /etc/debian_version) return 0 ;;
        /etc/redhat-release) return 1 ;;
        /etc/alpine-release) return 1 ;;
      esac
    fi
    builtin test "${@:1:$#-1}"
  }
  rm -f "$stubbin/docker" "$daemon_flag"
  hash -r
  # 密封自检：删桩后密封 PATH 内不得再解析出任何 docker /
  # docker-compose——解析得出说明密封构造被环境击穿、本格结论
  # 不可信，按构造失败点名退出，绝不拿宿主 docker 假绿/假红。
  if ( export PATH="$sealed_path"; hash -r; command -v docker ) >/dev/null 2>&1 || \
     ( export PATH="$sealed_path"; hash -r; command -v docker-compose ) >/dev/null 2>&1; then
    fail "R7-02: sealed PATH still resolves a host docker/docker-compose; the hermetic construction is broken"
  fi
  rc="0"; out=""
  out=$( PATH="$sealed_path" check_docker 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R7-02: post-install recheck must fail when the daemon stays unreachable"
  grep -q '守护进程（daemon）不可达' <<< "$out" || \
    fail "R7-02: post-install recheck failure must name the daemon"
  # 同形态 daemon 可达 → 装后复检放行。
  rm -f "$stubbin/docker"
  touch "$daemon_flag"
  hash -r
  rc="0"; out=""
  out=$( PATH="$sealed_path" check_docker 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R7-02: post-install recheck must pass once the daemon is reachable"
  grep -q '检测到 Docker 命令' <<< "$out" || \
    fail "R7-02: post-install recheck should report the detected command once the daemon is verified"
  unset -f '['
)

# R7-02（静态半）：钉住启动接线与复检探活，防文本回退——helper
# 本体必须含 systemd 与 OpenRC 两种启动形态；install_docker 本
# 体必须在官方路径与兜底各支共 5 处以上调用 start_docker_
# daemon、Alpine 支传 openrc；check_docker 本体必须含 docker
# info 探活与点名 daemon 的失败文案。
test_install_docker_daemon_start_wiring_static() (
  set -euo pipefail
  local helper_body install_body check_body calls
  helper_body=$(awk '/^start_docker_daemon\(\) \{/,/^\}/' "$ROOT_DIR/panel_install.sh")
  install_body=$(awk '/^install_docker\(\) \{/,/^\}/' "$ROOT_DIR/panel_install.sh")
  check_body=$(awk '/^check_docker\(\) \{/,/^\}/' "$ROOT_DIR/panel_install.sh")
  [[ -n "$helper_body" && -n "$install_body" && -n "$check_body" ]] || \
    fail "R7-02: start_docker_daemon/install_docker/check_docker bodies must be extractable"
  grep -q 'systemctl enable --now docker' <<< "$helper_body" || \
    fail "R7-02: start_docker_daemon must enable+start docker via systemd"
  grep -q 'rc-update add docker default' <<< "$helper_body" || \
    fail "R7-02: start_docker_daemon openrc form must add docker to the default runlevel"
  grep -q 'rc-service docker start' <<< "$helper_body" || \
    fail "R7-02: start_docker_daemon openrc form must start docker via rc-service"
  calls=$(grep -c 'start_docker_daemon' <<< "$install_body" || true)
  [[ "$calls" -ge 5 ]] || \
    fail "R7-02: install_docker must call start_docker_daemon on the official path and every fallback branch (found $calls)"
  grep -q 'start_docker_daemon openrc' <<< "$install_body" || \
    fail "R7-02: the Alpine fallback must call start_docker_daemon with openrc"
  grep -q 'docker info' <<< "$check_body" || \
    fail "R7-02: check_docker must probe daemon reachability via docker info"
  grep -q '守护进程（daemon）不可达' <<< "$check_body" || \
    fail "R7-02: check_docker daemon failure must name the daemon"
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

# R7-002 第二锚（install.sh 段 B）：download_agent_binary 的第一锚
# 校验值由下载地址派生、恒同源——定版后须经异路重取 .sha256 复核：
# 异路同值放行、异路异值整轮拒绝、异路无应答降级点名但不拦截。
# 桩 curl 按 URL 前缀供数：a.example 为获胜线路，b.example 为复核
# 线路（其 .sha256 模式由 BIN_ANCHOR_B_MODE 控制）。
bin_anchor_fixture_setup() {
  load_script_without_main "$ROOT_DIR/install.sh"
  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_URL="https://github.com/example/gost-amd64"
  DOWNLOAD_ROUTES=("https://a.example" "https://b.example")
  ROUTES_STAGE=2
  BIN_ANCHOR_BODY="agent-binary-body"
  BIN_ANCHOR_SHA_A=$(printf '%s' "$BIN_ANCHOR_BODY" | sha256sum | awk '{print $1}')
  BIN_ANCHOR_SHA_B=$(printf '%s' "different-body" | sha256sum | awk '{print $1}')
  BIN_ANCHOR_B_MODE="same"
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
    case "$url" in
      *.sha256)
        case "$url" in
          *b.example*)
            case "$BIN_ANCHOR_B_MODE" in
              different) printf '%s' "$BIN_ANCHOR_SHA_B" > "$output" ;;
              fail) return 22 ;;
              *) printf '%s' "$BIN_ANCHOR_SHA_A" > "$output" ;;
            esac
            ;;
          *) printf '%s' "$BIN_ANCHOR_SHA_A" > "$output" ;;
        esac
        ;;
      *) printf '%s' "$BIN_ANCHOR_BODY" > "$output" ;;
    esac
  }
}

test_install_script_binary_anchor_passes() (
  set -euo pipefail
  bin_anchor_fixture_setup
  local dest
  dest=$(mktemp)
  download_agent_binary "$dest" 2>/dev/null
  assert_equals "$BIN_ANCHOR_BODY" "$(cat "$dest")" "binary anchor pass: binary should land when the second route corroborates"
)

test_install_script_binary_anchor_mismatch_rejects() (
  set -euo pipefail
  bin_anchor_fixture_setup
  BIN_ANCHOR_B_MODE="different"
  local dest rc=0
  dest=$(mktemp -u)
  download_agent_binary "$dest" 2>/dev/null || rc=$?
  assert_equals "1" "$rc" "binary anchor mismatch: download must fail when the second route disagrees"
  [[ ! -f "$dest" ]] || fail "binary anchor mismatch: binary must not land on anchor disagreement"
)

test_install_script_binary_anchor_degrades() (
  set -euo pipefail
  bin_anchor_fixture_setup
  BIN_ANCHOR_B_MODE="fail"
  local dest errlog
  dest=$(mktemp)
  errlog=$(mktemp)
  download_agent_binary "$dest" 2>"$errlog"
  assert_equals "$BIN_ANCHOR_BODY" "$(cat "$dest")" "binary anchor degrade: binary should still land when no second route answers"
  grep -q "跨线路锚复核已降级" "$errlog" || fail "binary anchor degrade: degradation must be named on stderr"
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

test_ensure_compose_trusted_proxy_hops_injects_mapping_form() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/docker-compose.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment:
      DB_TYPE: sqlite
      SERVER_ADDR: :6365
  frontend:
    image: example/frontend:1.9.2
YAML

  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml"

  grep -qF 'FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}' "$work/docker-compose.yml" \
    || fail "R6-L8-F-01: generated compose must carry the FLMX_TRUSTED_PROXY_HOPS passthrough line"
  assert_equals "1" "$(grep -c 'FLMX_TRUSTED_PROXY_HOPS' "$work/docker-compose.yml")" \
    "R6-L8-F-01: hops line must be injected exactly once"

  # 幂等：再跑一次不得重复注入。
  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml"
  assert_equals "1" "$(grep -c 'FLMX_TRUSTED_PROXY_HOPS' "$work/docker-compose.yml")" \
    "R6-L8-F-01: ensure must be idempotent"
)

test_ensure_compose_trusted_proxy_hops_keeps_existing_line() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/docker-compose.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.3
    environment:
      DB_TYPE: sqlite
      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}
YAML
  cp "$work/docker-compose.yml" "$work/before.yml"

  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml"

  cmp -s "$work/before.yml" "$work/docker-compose.yml" \
    || fail "R6-L8-F-01: a compose already carrying the hops line must pass through unchanged"
)

test_ensure_compose_trusted_proxy_hops_list_form() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/docker-compose.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment:
      - DB_TYPE=sqlite
      - SERVER_ADDR=:6365
YAML

  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml"

  grep -qF -- '- FLMX_TRUSTED_PROXY_HOPS=${FLMX_TRUSTED_PROXY_HOPS:-2}' "$work/docker-compose.yml" \
    || fail "R6-L8-F-01: list-form environment must gain a list-form hops entry"
)

test_ensure_compose_trusted_proxy_hops_list_form_comment_first() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/docker-compose.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment:
      # 运行时开关
      - DB_TYPE=sqlite
      - SERVER_ADDR=:6365
YAML

  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml"

  # RB3-01：定形须跳过 environment 后的注释行——注入列表形而非
  # 映射形（映射行混入序列表会把 YAML 写坏）。
  grep -qF -- '      - FLMX_TRUSTED_PROXY_HOPS=${FLMX_TRUSTED_PROXY_HOPS:-2}' "$work/docker-compose.yml" \
    || fail "RB3-01: list-form environment with a leading comment must gain a list-form hops entry"
  if grep -qF 'FLMX_TRUSTED_PROXY_HOPS: ' "$work/docker-compose.yml"; then
    fail "RB3-01: mapping-form hops line must not be injected into a list-form environment"
  fi
  assert_equals "1" "$(grep -c 'FLMX_TRUSTED_PROXY_HOPS' "$work/docker-compose.yml")" \
    "RB3-01: hops line must be injected exactly once"
)

test_ensure_compose_trusted_proxy_hops_env_header_trailing_comment() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/docker-compose.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment: # 后端环境变量
      DB_TYPE: sqlite
YAML

  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml"

  # RB3-02：environment 头行带行尾注释时旧正则不匹配、静默不注入。
  grep -qF 'FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}' "$work/docker-compose.yml" \
    || fail "RB3-02: environment header with a trailing comment must still receive the hops line"
)

test_ensure_compose_trusted_proxy_hops_decoy_string_elsewhere() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/docker-compose.yml" <<'YAML'
# FLMX_TRUSTED_PROXY_HOPS 由 backend 段管理，此处仅注释提及
services:
  backend:
    image: example/backend:1.9.2
    environment:
      DB_TYPE: sqlite
  frontend:
    image: example/frontend:1.9.2
    # frontend 段注释同样提到 FLMX_TRUSTED_PROXY_HOPS
YAML

  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml"

  # RB3-02：已有判定收窄在 backend 的 environment 块内，文件他处
  # 的同名注释不得抑制注入（旧实现是文件全局 grep）。
  grep -qF 'FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}' "$work/docker-compose.yml" \
    || fail "RB3-02: decoy key strings elsewhere in the file must not suppress injection into backend environment"
  assert_equals "3" "$(grep -c 'FLMX_TRUSTED_PROXY_HOPS' "$work/docker-compose.yml")" \
    "RB3-02: exactly the two decoy mentions plus the injected line may carry the key string"
)

test_ensure_compose_trusted_proxy_hops_unrecognized_env_form_keeps_original() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cat > "$work/docker-compose.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment: ["DB_TYPE=sqlite"]
YAML
  cp "$work/docker-compose.yml" "$work/before.yml"

  # RB3-01：无法识别的 environment 形态不得静默放行或写坏文件——
  # 函数须返回非零、向 stderr 点名、原文件逐字保留。
  local rc=0
  ensure_compose_trusted_proxy_hops "$work/docker-compose.yml" 2>"$work/stderr.txt" || rc=$?
  if [[ "$rc" -eq 0 ]]; then
    fail "RB3-01: an unrecognized environment form must make ensure return nonzero"
  fi
  grep -q 'FLMX_TRUSTED_PROXY_HOPS' "$work/stderr.txt" \
    || fail "RB3-01: the degradation must be announced on stderr"
  cmp -s "$work/before.yml" "$work/docker-compose.yml" \
    || fail "RB3-01: a failed validation must leave the original compose byte-identical"
)

test_compose_hops_injection_problem_detects_form_mismatch() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  # 映射形段内混入列表形传递行——模拟注入定形出错的坏产物。
  cat > "$work/bad.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment:
      DB_TYPE: sqlite
      - FLMX_TRUSTED_PROXY_HOPS=${FLMX_TRUSTED_PROXY_HOPS:-2}
YAML
  cat > "$work/good.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment:
      DB_TYPE: sqlite
      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}
YAML

  # RB3-01：覆写前的结构校验须拦下形态不一致的产物并给出理由。
  local problem
  if problem=$(compose_hops_injection_problem "$work/bad.yml"); then
    fail "RB3-01: validation must reject a list-form hops line inside a mapping-form environment"
  fi
  [[ -n "$problem" ]] \
    || fail "RB3-01: validation must name the problem it found"
  compose_hops_injection_problem "$work/good.yml" >/dev/null \
    || fail "RB3-01: validation must accept a well-formed injected compose"
)

test_shipped_compose_templates_carry_hops_line() (
  set -euo pipefail
  local tpl
  for tpl in "$ROOT_DIR/docker-compose-v4.yml" "$ROOT_DIR/docker-compose-v6.yml"; do
    grep -qF 'FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}' "$tpl" \
      || fail "R6-L8-F-01: shipped template $tpl must carry the FLMX_TRUSTED_PROXY_HOPS passthrough line with default 2"
  done
)

# R8-011 的 run_panel_compose 桩：config -q 以「含 services: 且无
# INVALID-YAML 标记」模拟解析成功，config --services 按两空格缩进的
# 服务键抽取（与 shipped 模板的实际排版一致）。
r8_011_stub_compose() {
  local file="" mode=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -f) file="$2"; shift 2 ;;
      -q) mode="quiet"; shift ;;
      --services) mode="services"; shift ;;
      *) shift ;;
    esac
  done
  case "$mode" in
    quiet)
      ! grep -q 'INVALID-YAML' "$file" && grep -q '^services:' "$file"
      ;;
    services)
      awk '/^services:/{in_s=1; next} /^[^ ]/{in_s=0} in_s && /^  [A-Za-z0-9_-]+:/{name=$1; sub(/:$/, "", name); print name}' "$file"
      ;;
  esac
}

test_validate_panel_compose_content_accepts_shipped_templates() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  run_panel_compose() { r8_011_stub_compose "$@"; }

  local tpl
  for tpl in "$ROOT_DIR/docker-compose-v4.yml" "$ROOT_DIR/docker-compose-v6.yml"; do
    validate_panel_compose_content "$tpl" "1.9.4" \
      || fail "R8-011: shipped template $tpl must pass compose content validation"
  done
)

test_validate_panel_compose_content_rejects_bad_files() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  run_panel_compose() { r8_011_stub_compose "$@"; }

  local work
  work=$(mktemp -d)

  cat > "$work/only-backend.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.4
YAML
  if validate_panel_compose_content "$work/only-backend.yml" "1.9.4"; then
    fail "R8-011: compose without a frontend service must be rejected"
  fi

  cat > "$work/invalid.yml" <<'YAML'
services:
  backend: [INVALID-YAML
YAML
  if validate_panel_compose_content "$work/invalid.yml" "1.9.4"; then
    fail "R8-011: unparsable compose must be rejected"
  fi

  cat > "$work/good.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.4
  frontend:
    image: example/frontend:1.9.4
YAML
  validate_panel_compose_content "$work/good.yml" "1.9.4" \
    || fail "R8-011: compose with backend+frontend must pass"
)

test_validate_panel_update_compose_uses_content_validation() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  run_panel_compose() { r8_011_stub_compose "$@"; }

  local work
  work=$(mktemp -d)
  cat > "$work/candidate.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.4
YAML

  UPDATE_COMPOSE_CANDIDATE="$work/candidate.yml"
  LATEST_VERSION="1.9.4"
  if validate_panel_update_compose; then
    fail "R8-011: update validation must reject a candidate missing the frontend service"
  fi

  cat > "$work/candidate.yml" <<'YAML'
services:
  backend:
    image: example/backend:1.9.4
  frontend:
    image: example/frontend:1.9.4
YAML
  validate_panel_update_compose \
    || fail "R8-011: update validation must accept a candidate with backend+frontend"
)

test_activate_panel_update_files_keeps_hops_line() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  cd "$work"
  printf 'FLUX_VERSION=1.9.2\nJWT_SECRET=x\n' > .env
  # 存量 compose 带显式跳数行；候选是无此行的旧版资产——覆写后该行
  # 必须仍在（标准行再生），不得被整份覆写冲掉。
  cat > docker-compose.yml <<'YAML'
services:
  backend:
    image: example/backend:1.9.2
    environment:
      DB_TYPE: sqlite
      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}
YAML
  cat > candidate.yml <<'YAML'
services:
  backend:
    image: example/backend:1.9.3
    environment:
      DB_TYPE: sqlite
      SERVER_ADDR: :6365
YAML

  UPDATE_COMPOSE_CANDIDATE="$work/candidate.yml"
  LATEST_VERSION="1.9.3"
  activate_panel_update_files

  grep -qF 'FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}' docker-compose.yml \
    || fail "R6-L8-F-01: self-upgrade overwrite must keep the FLMX_TRUSTED_PROXY_HOPS line"
  grep -q "example/backend:1.9.3" docker-compose.yml \
    || fail "R6-L8-F-01: update candidate content must otherwise be written"
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

# L8-F-02 第二锚（panel_install.sh）：download_verified_routed 获胜
# 后须经异路重取 .sha256 复核——异路同值放行、异路异值删档整单
# 中止、异路全灭降级点名但不拦截。桩 curl 按 URL 前缀供数：
# a.example 为获胜线路，b.example 为复核线路。
anchor_fixture_setup() {
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_ROUTES=("https://a.example" "https://b.example")
  ROUTES_STAGE=2
  ANCHOR_BODY="anchor-body-content"
  ANCHOR_SHA_A=$(printf '%s' "$ANCHOR_BODY" | sha256sum | awk '{print $1}')
  ANCHOR_SHA_B=$(printf '%s' "different-body" | sha256sum | awk '{print $1}')
  ANCHOR_SHA_B_MODE="same"
  ANCHOR_SHA_B_FAIL="0"
  curl() {
    local out="" url="" prev="" a
    for a in "$@"; do
      if [[ "$prev" == "-o" ]]; then out="$a"; fi
      case "$a" in http*://*) url="$a" ;; esac
      prev="$a"
    done
    case "$url" in
      *.sha256)
        case "$url" in
          *b.example*)
            if [[ "$ANCHOR_SHA_B_FAIL" == "1" ]]; then return 22; fi
            if [[ "$ANCHOR_SHA_B_MODE" == "different" ]]; then
              printf '%s' "$ANCHOR_SHA_B" > "$out"
            else
              printf '%s' "$ANCHOR_SHA_A" > "$out"
            fi
            ;;
          *) printf '%s' "$ANCHOR_SHA_A" > "$out" ;;
        esac
        ;;
      *) printf '%s' "$ANCHOR_BODY" > "$out" ;;
    esac
  }
}

test_panel_anchor_corroboration_passes() (
  set -euo pipefail
  anchor_fixture_setup
  local dest
  dest=$(mktemp)
  download_verified_routed "https://github.com/example/asset.yml" "$dest" 2>/dev/null
  assert_equals "$ANCHOR_BODY" "$(cat "$dest")" "anchor pass: body should land when the second route corroborates"
)

test_panel_anchor_corroboration_mismatch_aborts() (
  set -euo pipefail
  anchor_fixture_setup
  ANCHOR_SHA_B_MODE="different"
  local dest rc=0
  dest=$(mktemp)
  download_verified_routed "https://github.com/example/asset.yml" "$dest" 2>/dev/null || rc=$?
  assert_equals "1" "$rc" "anchor mismatch: routed download must abort when the second route disagrees"
  [[ ! -f "$dest" ]] || fail "anchor mismatch: staged file must be removed on abort"
)

test_panel_anchor_corroboration_degrades() (
  set -euo pipefail
  anchor_fixture_setup
  ANCHOR_SHA_B_FAIL="1"
  local dest errlog
  dest=$(mktemp)
  errlog=$(mktemp)
  download_verified_routed "https://github.com/example/asset.yml" "$dest" 2>"$errlog"
  assert_equals "$ANCHOR_BODY" "$(cat "$dest")" "anchor degrade: body should still land when no second route answers"
  grep -q "跨线路锚复核已降级" "$errlog" || fail "anchor degrade: degradation must be named on stderr"
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
test_install_docker_fallback_succeeds_with_working_package_managers
test_install_docker_fallback_package_names_exist_in_repos
test_install_docker_fallback_starts_daemon_all_distros
test_check_docker_requires_reachable_daemon_on_install_path
test_install_docker_daemon_start_wiring_static
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
  # R6-04 后菜单 5 会把解析结果与本机公网 IP 比对：夹具的解析恒指
  # 203.0.113.10，本机公网 IP 桩同值，既有菜单 5 用例才仍走「已核
  # 对指向本机」分支（不额外消耗确认输入）。
  get_public_ip() { printf '203.0.113.10'; }
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

# R9-05 夹具：菜单 5 的 Caddy 自动安装路径（caddy 不在 PATH、
# apt-get/gpg/curl 全桩）。官方源 GPG 指纹恒验过（走官方源安装
# 支）；源列表内容由 CADDY_LIST_MODE 切换（truncated=截断体、
# valid=完整 deb 行）；装 caddy 本体恒失败以落到失败路径。
# CADDY_LIST_PATH 钩子把落盘点重定向进临时目录（与 CADDYFILE_PATH
# 同钩子口径，不碰真实 /etc）。
panel_caddy_install_fixture_env() {
  FIXTURE_DIR=$(mktemp -d)
  export CADDYFILE_PATH="$FIXTURE_DIR/Caddyfile"
  export CADDY_LIST_PATH="$FIXTURE_DIR/caddy-stable.list"
  sleep() { :; }
  getent() { printf '203.0.113.10 %s\n' "$2"; }
  get_public_ip() { printf '203.0.113.10'; }
  systemctl() { return 0; }
  gpg() {
    case " $* " in
      *" --show-keys "*) printf 'fpr:::::::::65760C51EDEA2017CEA2CA15155B6D79CA56EA34:\n' ;;
    esac
    return 0
  }
  curl() {
    local out="" url="" prev=""
    for a in "$@"; do
      if [[ "$prev" == "-o" ]]; then out="$a"; fi
      case "$a" in http*) url="$a";; esac
      prev="$a"
    done
    case "$url" in
      *gpg.key)
        printf 'dummy-key-material' > "$out" ;;
      *debian.deb.txt)
        if [[ "${CADDY_LIST_MODE:-truncated}" == "valid" ]]; then
          printf 'deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian bookworm main\n' > "$out"
        else
          printf 'deb https://dl.cloudsmith.io/public/caddy/stable/deb/debi' > "$out"
        fi ;;
      *) return 22 ;;
    esac
    return 0
  }
  apt-get() {
    if [[ "$1" == "install" && " $* " == *" caddy "* ]]; then
      return 1
    fi
    return 0
  }
}

# R9-05（截断体）: HTTP 200 的截断源列表不得落盘——落盘前验形态，
# 不完整即点名并按未写入处置。旧实现只验非空、截断体照常落盘
# （本用例对旧代码必红）。
test_panel_menu5_caddy_list_truncated_not_landed() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddy_install_fixture_env
  CADDY_LIST_MODE="truncated"

  local rc="0" log="$FIXTURE_DIR/menu5-truncated.log"
  setup_domain_proxy <<< "panel.example.com" >"$log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R9-05: menu 5 must fail when Caddy cannot be installed"
  grep -q "内容不完整" "$log" || \
    fail "R9-05: a truncated source list must be named as incomplete instead of being landed silently"
  grep -q "未写入 apt 源文件" "$log" || \
    fail "R9-05: receipt must state the malformed list was not written to the apt sources"
  [[ ! -f "$CADDY_LIST_PATH" ]] || \
    fail "R9-05: truncated source list must not be landed in the apt sources directory"
)

# R9-05（落盘后失败清理）: 形态完整的源列表落盘后若安装随即失
# 败，本次写入的列表必须被删除并点名，不得留盘污染后续全部
# apt 操作。旧实现失败路径从不清理（本用例对旧代码必红）。
test_panel_menu5_caddy_list_removed_when_install_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddy_install_fixture_env
  CADDY_LIST_MODE="valid"

  local rc="0" log="$FIXTURE_DIR/menu5-landed-fail.log"
  setup_domain_proxy <<< "panel.example.com" >"$log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R9-05: menu 5 must fail when the Caddy install fails after the list landed"
  grep -q "已删除本次写入的官方源列表" "$log" || \
    fail "R9-05: install failure must remove the source list this run landed and name the removal"
  [[ ! -f "$CADDY_LIST_PATH" ]] || \
    fail "R9-05: the source list landed by this run must not remain after a failed install"
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

# C4-F04: panel_install.sh 的 main/delete_self 与 install.sh 的
# R4B11-005 口径对齐。夹具以子进程实跑 panel main（保留真实
# delete_self）：操作失败时退出码非 0、脚本保留、无「操作已完
# 成」；成功时自删且有完成文案；操作函数置 PANEL_OP_CANCELLED
# 标志报告用户取消时退出 0、脚本保留、同样无完成文案（操作函数
# 本身的返回码口径不变，取消仍 return 0，AI-09）。panel main 有 EUID 前置
# 检查，非 root 环境跳过本用例。
test_panel_main_delete_self_only_on_success() (
  set -euo pipefail
  if [[ $EUID -ne 0 ]]; then
    echo "skip: test_panel_main_delete_self_only_on_success needs a root environment"
    return 0
  fi

  local work
  work=$(mktemp -d)

  build_case() {
    local name="$1" stub="$2"
    local file="$work/$name.sh"
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/panel_install.sh" > "$file"
    cat >> "$file" <<STUBEOF
$stub
main
STUBEOF
    printf '%s' "$file"
  }

  local rc="0" file out

  file=$(build_case panel-fail 'install_panel() { return 1; }')
  rc="0"; out=$(printf '1\n' | SELF_UPDATE_SKIP=1 bash "$file" 2>&1) || rc="$?"
  assert_equals "1" "$rc" "C4-F04: failed panel operation should exit non-zero"
  [[ -f "$file" ]] || fail "C4-F04: panel script must be kept after a failed operation"
  if [[ "$out" == *"操作已完成"* ]]; then
    fail "C4-F04: failure path must not print 操作已完成"
  fi
  [[ "$out" == *"已保留"* ]] || \
    fail "C4-F04: failure path should say the script was kept for retry"

  file=$(build_case panel-ok 'install_panel() { return 0; }')
  rc="0"; out=$(printf '1\n' | SELF_UPDATE_SKIP=1 bash "$file" 2>&1) || rc="$?"
  assert_equals "0" "$rc" "C4-F04: successful panel operation should exit 0"
  [[ ! -f "$file" ]] || fail "C4-F04: panel script should delete itself after success"
  [[ "$out" == *"操作已完成"* ]] || \
    fail "C4-F04: success path should print the completion line"

  file=$(build_case panel-cancel 'uninstall_panel() { echo "❌ 取消卸载"; PANEL_OP_CANCELLED="1"; return 0; }')
  rc="0"; out=$(printf '3\n' | SELF_UPDATE_SKIP=1 bash "$file" 2>&1) || rc="$?"
  assert_equals "0" "$rc" "C4-F04: user-cancelled operation should exit 0"
  [[ -f "$file" ]] || fail "C4-F04: panel script must be kept when the user cancels"
  if [[ "$out" == *"操作已完成"* ]]; then
    fail "C4-F04: cancel path must not print 操作已完成"
  fi
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
  # 夹具须是真实自家部署形态：迁移守卫（AI-02 后与 resolve 同口径）
  # 按 Compose 的 image 行判归属，空 compose 会被判归属未证而拒绝，
  # 与本例要验的回滚面无关。
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n  frontend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test\n' > docker-compose.yml
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

# AI-01/AI-02/AI-04/AI-05: 共存守卫归属谓词矩阵——守卫函数本体真实
# 执行（判定本体不许桩替换），docker 以桩承接容器实物证据。关键格：
# 他方新装形态（.env 写他人后端名＋他人镜像）与他方旧装形态（.env
# 后端名与本面板旧缺省名同值＋他人镜像）都必须判他人、不得判自家
# （AI-01）；注释含本面板前缀不得伪造归属（AI-04）；build: 型与单
# 文件残缺态归属未证（两谓词皆否，交调用方按未证口径处理）。
test_panel_ownership_predicates_matrix() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  FIXTURE_CONTAINER_IMAGE=""
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      printf '%s\n' "$FIXTURE_CONTAINER_IMAGE"
      return 0
    fi
    return 0
  }

  ours_rc() {
    if panel_deploy_dir_is_ours "$1" "${2:-flmx-panel-backend}" >/dev/null 2>&1; then echo 0; else echo 1; fi
  }
  foreign_rc() {
    if is_foreign_panel_deploy_dir "$1" >/dev/null 2>&1; then echo 0; else echo 1; fi
  }
  mk_fixture() {
    local dir
    dir=$(mktemp -d)
    if [[ "$1" != "-" ]]; then printf '%s\n' "$1" > "$dir/.env"; fi
    if [[ "$2" != "-" ]]; then printf '%s\n' "$2" > "$dir/docker-compose.yml"; fi
    printf '%s' "$dir"
  }

  local own_compose foreign_compose comment_compose build_compose
  own_compose='services:
  backend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test
  frontend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test'
  foreign_compose='services:
  backend:
    image: ghcr.io/other-owner/other-panel-backend:v-test
  postgres:
    image: postgres:16-alpine'
  comment_compose='services:
  backend:
    image: ghcr.io/other-owner/other-panel-backend:v-test
# previously used ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend here'
  build_compose='services:
  backend:
    build: .'

  local d
  # A 他方新装形态：判他人、不判自家
  FIXTURE_CONTAINER_IMAGE='ghcr.io/other-owner/other-panel-backend:v-test'
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=other-panel-backend' "$foreign_compose")
  assert_equals "0" "$(foreign_rc "$d")" "AI-01: foreign deployment (declared foreign name + foreign images) must be foreign"
  assert_equals "1" "$(ours_rc "$d")" "AI-01: foreign deployment must not be ours"
  # A2 他方旧装形态：.env 后端名与本面板旧缺省名同值——只验值不得判自家
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=flux-panel-backend' "$foreign_compose")
  assert_equals "0" "$(foreign_rc "$d")" "AI-01: legacy-named foreign deployment must be foreign"
  assert_equals "1" "$(ours_rc "$d")" "AI-01: legacy default name alone must not prove ownership"
  # B build: 型：归属未证（两谓词皆否）
  FIXTURE_CONTAINER_IMAGE=''
  d=$(mk_fixture 'JWT_SECRET=x' "$build_compose")
  assert_equals "1" "$(foreign_rc "$d")" "AI-02: build: compose must not be hard-judged foreign"
  assert_equals "1" "$(ours_rc "$d")" "AI-02: build: compose must not prove ownership"
  # C 注释伪造：全文出现本面板前缀不算证据
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=other-panel-backend' "$comment_compose")
  assert_equals "0" "$(foreign_rc "$d")" "AI-04: a comment mentioning our image prefix must not erase foreignness"
  assert_equals "1" "$(ours_rc "$d")" "AI-04: a comment mentioning our image prefix must not prove ownership"
  # D 仅 compose（他人镜像）：判他人（旧双文件合取口径下漏守）
  d=$(mk_fixture '-' "$foreign_compose")
  assert_equals "0" "$(foreign_rc "$d")" "AI-05: compose-only foreign deployment must be foreign"
  # E 仅 .env（声明他人后端名）：判他人
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=other-panel-backend' '-')
  assert_equals "0" "$(foreign_rc "$d")" "AI-05: env-only deployment declaring a foreign backend name must be foreign"
  # F 自家新装形态：判自家、不判他人
  FIXTURE_CONTAINER_IMAGE='ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test'
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=flmx-panel-backend' "$own_compose")
  assert_equals "1" "$(foreign_rc "$d")" "own new deployment must not be foreign"
  assert_equals "0" "$(ours_rc "$d")" "own new deployment must be ours"
  # G 自家存量形态（.env 无身份键、Compose 引用本面板镜像、容器已不在）
  FIXTURE_CONTAINER_IMAGE=''
  d=$(mk_fixture 'JWT_SECRET=x' "$own_compose")
  assert_equals "0" "$(ours_rc "$d")" "own legacy deployment must be ours via compose image evidence"
  # H Compose 缺失但 .env 声明专属名、容器已不在：判自家
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=flmx-panel-backend' '-')
  assert_equals "0" "$(ours_rc "$d")" "declared exclusive name without contradicting images must prove ownership"
  assert_equals "1" "$(foreign_rc "$d")" "declared exclusive name must not be foreign"
  # I 声明专属名但 Compose 是他人镜像：镜像证据优先，不判自家
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=flmx-panel-backend' "$foreign_compose")
  assert_equals "1" "$(ours_rc "$d")" "foreign compose images must override a declared exclusive name"
  assert_equals "0" "$(foreign_rc "$d")" "foreign compose images must be foreign even with a declared exclusive name"
  # J 文件面归属未证、但后端容器正运行本面板镜像：实物证据判自家
  FIXTURE_CONTAINER_IMAGE='ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test'
  d=$(mk_fixture 'JWT_SECRET=x' "$build_compose")
  assert_equals "0" "$(ours_rc "$d")" "a backend container running our image must prove ownership"
  # K 实例派生名声明：判自家
  FIXTURE_CONTAINER_IMAGE=''
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=flmx-panel-backend-b2
FLMX_INSTANCE=b2' '-')
  assert_equals "0" "$(ours_rc "$d")" "instance-derived backend name must prove ownership"
  # L 空目录：两谓词皆否
  d=$(mk_fixture '-' '-')
  assert_equals "1" "$(foreign_rc "$d")" "empty dir must not be foreign"
  assert_equals "1" "$(ours_rc "$d")" "empty dir must not be ours"
  # M1 他人 backend＋工具服务借用我方 agent 镜像：镜像证据只认
  # backend 段，附属服务的命中不判自家、他人 backend 判他人
  FIXTURE_CONTAINER_IMAGE=''
  d=$(mk_fixture '-' 'services:
  backend:
    image: ghcr.io/other-owner/other-panel-backend:v-test
  tools:
    image: ghcr.io/iiiiiiiii7125-art/flmx-agent:v-test')
  assert_equals "1" "$(ours_rc "$d")" "B2-02: a sidecar using our agent image must not prove ownership of a foreign backend"
  assert_equals "0" "$(foreign_rc "$d")" "B2-02: a foreign backend must be foreign even when a sidecar uses our image"
  # M2 他人 backend＋我方 frontend 镜像：同口径
  d=$(mk_fixture '-' 'services:
  backend:
    image: ghcr.io/other-owner/other-panel-backend:v-test
  frontend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test')
  assert_equals "1" "$(ours_rc "$d")" "B2-02: our frontend image must not prove ownership of a foreign backend"
  assert_equals "0" "$(foreign_rc "$d")" "B2-02: foreign backend + our frontend image must be foreign"
  # M3 自家 backend＋他人 frontend：backend 段命中即判自家
  d=$(mk_fixture '-' 'services:
  backend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test
  frontend:
    image: ghcr.io/other-owner/other-frontend:v-test')
  assert_equals "0" "$(ours_rc "$d")" "B2-02: our backend image must prove ownership regardless of other services"
  assert_equals "1" "$(foreign_rc "$d")" "B2-02: our backend image must not be foreign"
  # M4 名为 backend-tools 的服务不是 backend 段
  d=$(mk_fixture '-' 'services:
  backend:
    image: ghcr.io/other-owner/other-panel-backend:v-test
  backend-tools:
    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test')
  assert_equals "1" "$(ours_rc "$d")" "B2-02: a service named backend-tools is not the backend segment"
  assert_equals "0" "$(foreign_rc "$d")" "B2-02: backend-tools using our backend image must not erase foreignness"
  # M5 backend 段 image 带引号与行尾注释：仍按段口径正确提取
  d=$(mk_fixture '-' 'services:
  backend:
    image: "ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test" # pinned
  frontend:
    image: ghcr.io/other-owner/other-frontend:v-test')
  assert_equals "0" "$(ours_rc "$d")" "B2-02: a quoted backend image with a trailing comment must be parsed"
  # N 专属名证据（证据 3）的派生后缀必须合法：空后缀/非法字符/
  # 超 32 的名字任何安装都不可能产生，不作归属证据（B2-03）
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=flmx-panel-backend-' '-')
  assert_equals "1" "$(ours_rc "$d")" "B2-03: an empty instance suffix must not prove ownership"
  d=$(mk_fixture 'PANEL_BACKEND_CONTAINER=flmx-panel-backend-Bad_Id' '-')
  assert_equals "1" "$(ours_rc "$d")" "B2-03: an invalid-char instance suffix must not prove ownership"
  local a40n a32n
  a40n=$(printf 'a%.0s' $(seq 1 40))
  d=$(mk_fixture "PANEL_BACKEND_CONTAINER=flmx-panel-backend-$a40n" '-')
  assert_equals "1" "$(ours_rc "$d")" "B2-03: an over-32 instance suffix must not prove ownership"
  a32n=$(printf 'a%.0s' $(seq 1 32))
  d=$(mk_fixture "PANEL_BACKEND_CONTAINER=flmx-panel-backend-$a32n" '-')
  assert_equals "0" "$(ours_rc "$d")" "B2-03: a 32-char instance suffix must still prove ownership"
)

# AI-01: resolve（update/uninstall 共用入口）对他方部署必须在守卫处
# 终止——夹具为他方新装形态（.env 写他人后端名、Compose 他人镜像、
# 容器标签指向该目录、容器跑他人镜像），旧实现被「键存在」第一证
# 据放行、会按本面板更新/卸载接管他方部署。
test_panel_resolve_refuses_foreign_panel_deployment() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local foreign_dir
  foreign_dir=$(mktemp -d)
  foreign_dir=$(cd "$foreign_dir" && pwd -P)
  printf 'PANEL_BACKEND_CONTAINER=other-panel-backend\nJWT_SECRET=test\n' > "$foreign_dir/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/other-owner/other-panel-backend:v-test\n' > "$foreign_dir/docker-compose.yml"

  unset PANEL_DEPLOY_DIR PANEL_BACKEND_CONTAINER COMPOSE_PROJECT_NAME FLMX_INSTANCE
  FIXTURE_DIR="$foreign_dir"
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *working_dir*) printf '%s\n' "$FIXTURE_DIR" ;;
        *Config.Image*) printf '%s\n' "ghcr.io/other-owner/other-panel-backend:v-test" ;;
        *) printf '%s\n' "other-panel" ;;
      esac
      return 0
    fi
    if [[ "$1" == "inspect" && "$2" == "flmx-panel-backend" ]]; then
      return 1
    fi
    return 0
  }

  local rc="0" out
  out=$(resolve_panel_deployment 2>&1) || rc="$?"
  assert_equals "1" "$rc" "AI-01: resolve must refuse a foreign panel deployment"
  [[ "$out" == *"不属于本面板"* ]] || fail "AI-01: resolve refusal must name the ownership failure"
)

# AI-03: 实例部署（FLMX_INSTANCE=b2）经 resolve 定位并管理本实例的
# 后端容器，身份自 .env 读回、与契约一致。
test_panel_resolve_accepts_instance_deployment() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)
  dir=$(cd "$dir" && pwd -P)
  printf 'PANEL_BACKEND_CONTAINER=flmx-panel-backend-b2\nFLMX_INSTANCE=b2\nJWT_SECRET=test\n' > "$dir/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$dir/docker-compose.yml"
  cd "$dir"

  unset PANEL_DEPLOY_DIR PANEL_BACKEND_CONTAINER COMPOSE_PROJECT_NAME
  FIXTURE_DIR="$dir"
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *working_dir*) printf '%s\n' "$FIXTURE_DIR" ;;
        *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test" ;;
        *) printf '%s\n' "flmx-panel-b2" ;;
      esac
      return 0
    fi
    return 0
  }

  resolve_panel_deployment >/dev/null
  assert_equals "$dir" "$PANEL_DEPLOY_DIR" "AI-03: resolve must locate the instance deployment"
  assert_equals "flmx-panel-backend-b2" "$PANEL_BACKEND_CONTAINER" "AI-03: resolve must manage the instance backend container"
)

# AI-02: migrate 守卫必须前置于任何下载/写入——S6（只有他人 .env、
# 无 Compose）旧实现先下载本面板 Compose 制造证据再过守卫，现不得
# 发生下载、不得落 Compose；S4（build: 型）旧合取守卫整体跳过，
# 现按归属未证终止；自家存量形态守卫放行、迁移走通（正向对照）。
test_panel_migrate_guard_precedes_download() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER FLMX_INSTANCE PANEL_DEPLOY_DIR
  check_docker() { DOCKER_CMD="fixture_compose"; return 0; }
  fixture_compose() { return 0; }
  docker() { return 0; }
  wait_for_postgres_healthy() { return 0; }
  wait_for_backend_healthy() { return 0; }
  sleep() { :; }
  MIGRATE_DOWNLOADS=$(mktemp)
  download_verified_routed() { echo "routed" >> "$MIGRATE_DOWNLOADS"; return 0; }
  download_verified() { echo "plain" >> "$MIGRATE_DOWNLOADS"; return 0; }

  local s6 rc="0"
  s6=$(mktemp -d)
  printf 'PANEL_BACKEND_CONTAINER=other-panel-backend\nJWT_SECRET=x\nDB_TYPE=sqlite\n' > "$s6/.env"
  ( cd "$s6" && migrate_to_postgres >/dev/null 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "AI-02: migrate must refuse a foreign env-only deployment"
  [[ ! -e "$s6/docker-compose.yml" ]] || fail "AI-02: migrate must not download a compose file before the ownership guard"
  [[ ! -s "$MIGRATE_DOWNLOADS" ]] || fail "AI-02: migrate guard must run before any download"

  local s4
  s4=$(mktemp -d)
  printf 'JWT_SECRET=x\nDB_TYPE=sqlite\n' > "$s4/.env"
  printf 'services:\n  backend:\n    build: .\n' > "$s4/docker-compose.yml"
  rc="0"
  ( cd "$s4" && migrate_to_postgres >/dev/null 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "AI-02: migrate must refuse a build: deployment whose ownership is unproven"

  local own
  own=$(mktemp -d)
  printf 'JWT_SECRET=test-secret\nDB_TYPE=sqlite\nPOSTGRES_DB=flux_panel\nPOSTGRES_USER=flux_panel\nPOSTGRES_PASSWORD=test-pg-pass\n' > "$own/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n  frontend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test\n' > "$own/docker-compose.yml"
  rc="0"
  ( cd "$own" && migrate_to_postgres >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-02: migrate must proceed for an own deployment (compose image evidence)"
  grep -q '^DB_TYPE=postgres' "$own/.env" || fail "AI-02: own-deployment migration must complete the DB switch"
)

# AI-05: install 守卫对单文件残缺态同样设防——仅他人 Compose 时硬
# 拒（旧双文件合取口径无守卫直接覆写）；仅他人 .env（声明他人后端
# 名）时强制重装也不放行；归属未证的单文件/双文件残留默认拒绝，
# 强制放行时 .env 与 docker-compose.yml 一并备份（旧实现只备份
# .env，compose 定制在覆写时无声丢失）。
test_panel_install_guards_single_file_and_backups() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

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
  download_verified() {
    printf 'services:\n  backend:\n    image: example/backend:9.9.9\n  frontend:\n    image: example/frontend:9.9.9\n' > "$2"
    return 0
  }
  wait_for_backend_healthy() { return 0; }
  check_ipv6_support() { return 1; }
  configure_docker_ipv6() { return 0; }
  docker_ipv6_usable() { return 1; }
  get_public_ip() { echo "203.0.113.1"; }
  docker() {
    case " $* " in
      *" --services "*) printf 'backend\nfrontend\n' ;;
    esac
    return 0
  }
  unset PANEL_FORCE_REINSTALL FLMX_INSTANCE

  local rc d
  # S7: 仅他人 Compose——硬拒、文件不动
  d=$(mktemp -d)
  printf 'services:\n  backend:\n    image: ghcr.io/other-owner/other-panel-backend:v-test\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && install_panel >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "AI-05: install must refuse a compose-only foreign deployment"
  grep -q 'other-owner' "$d/docker-compose.yml" || fail "AI-05: foreign compose must stay untouched"

  # S6: 仅他人 .env（声明他人后端名）——强制重装也不得放行
  d=$(mktemp -d)
  printf 'PANEL_BACKEND_CONTAINER=other-panel-backend\nJWT_SECRET=keepme\n' > "$d/.env"
  rc="0"
  ( cd "$d" && PANEL_FORCE_REINSTALL=1 install_panel >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "AI-05: install must hard-refuse an env-only foreign deployment even with PANEL_FORCE_REINSTALL=1"
  grep -q 'keepme' "$d/.env" || fail "AI-05: foreign .env must stay untouched"

  # 单文件未证（仅 Compose、无镜像引用）：默认拒；强制放行先备份该 Compose
  d=$(mktemp -d)
  printf 'services: {}\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && install_panel >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "AI-05: install must refuse a single-file leftover deployment by default"
  rc="0"
  ( cd "$d" && PANEL_FORCE_REINSTALL=1 install_panel >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-05: forced reinstall should proceed for an unproven single-file leftover"
  ls "$d"/docker-compose.yml.bak.* >/dev/null 2>&1 || fail "AI-05: forced reinstall must back up the existing compose"
  grep -q 'services: {}' "$d"/docker-compose.yml.bak.* || fail "AI-05: compose backup must contain the original content"

  # S8: 双文件未证 + 强制：.env 与 Compose 一并备份
  d=$(mktemp -d)
  printf 'JWT_SECRET=keepme\n' > "$d/.env"
  printf 'services: {}\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && PANEL_FORCE_REINSTALL=1 install_panel >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-05: forced reinstall should proceed for an unproven deployment"
  ls "$d"/.env.bak.* >/dev/null 2>&1 || fail "AI-05: forced reinstall must back up .env"
  ls "$d"/docker-compose.yml.bak.* >/dev/null 2>&1 || fail "AI-05: forced reinstall must back up docker-compose.yml too"
)

# AI-09: 无 Docker 分支的卸载清理必须按归属判定删文件——明确属他
# 人的残留文件不删并返回 1；已证属自家的直接清理（不依赖确认输
# 入）；归属未证须显式确认，拒绝确认时保留、无输入时不删。
test_panel_uninstall_without_docker_respects_ownership() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  check_docker() { return 1; }

  local rc d
  # 他人残留：不删、返回 1
  d=$(mktemp -d)
  printf 'PANEL_BACKEND_CONTAINER=other-panel-backend\nJWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/other-owner/other-panel-backend:v-test\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && uninstall_panel </dev/null >/dev/null 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "AI-09: no-docker uninstall must refuse files of a foreign deployment"
  [[ -e "$d/.env" && -e "$d/docker-compose.yml" ]] || fail "AI-09: foreign files must stay untouched"

  # 自家残留（Compose 引用本面板镜像）：直接清理
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && uninstall_panel </dev/null >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-09: no-docker uninstall should clean an own deployment without prompting"
  [[ ! -e "$d/.env" && ! -e "$d/docker-compose.yml" ]] || fail "AI-09: own leftover files must be removed"

  # 归属未证：拒绝确认 → 保留；确认 → 删除
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services: {}\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && uninstall_panel <<< "n" >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-09: declining the confirmation should be a clean no-op"
  [[ -e "$d/.env" && -e "$d/docker-compose.yml" ]] || fail "AI-09: unproven files must stay when confirmation is declined"
  rc="0"
  ( cd "$d" && uninstall_panel <<< "y" >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-09: confirming should clean unproven leftover files"
  [[ ! -e "$d/.env" && ! -e "$d/docker-compose.yml" ]] || fail "AI-09: confirmed unproven files must be removed"

  # 空目录：无文件可清、直接返回 0
  d=$(mktemp -d)
  rc="0"
  ( cd "$d" && uninstall_panel </dev/null >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-09: no-docker uninstall on an empty dir should be a no-op success"
)

# AI-03: 实例级身份——默认实例写值与历史逐字一致（存量无感）；实
# 例 b2 的全部派生名（容器/卷/网络/项目）统一加后缀 -b2 且与默认
# 实例不相交，FLMX_INSTANCE 落盘供后续路径按同一契约读回；旧模板
# 回退分支保持旧名且对实例后缀不生效显式告警；detect 按派生名探
# 容器；非法实例 id 在安装入口前置拒绝、不落文件。
test_panel_instance_identity_naming() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local d1 d2 d3
  d1=$(mktemp -d)
  d2=$(mktemp -d)
  d3=$(mktemp -d)
  cp "$ROOT_DIR/docker-compose-v4.yml" "$d1/docker-compose.yml"
  cp "$ROOT_DIR/docker-compose-v4.yml" "$d2/docker-compose.yml"
  printf 'services: {}\n' > "$d3/docker-compose.yml"

  ( cd "$d1" && write_new_panel_identity >/dev/null )
  assert_equals "flmx-panel-backend" "$(get_env_var PANEL_BACKEND_CONTAINER "$d1/.env")" "AI-03: default backend name must stay unchanged"
  assert_equals "flmx-panel-frontend" "$(get_env_var PANEL_FRONTEND_CONTAINER "$d1/.env")" "AI-03: default frontend name must stay unchanged"
  assert_equals "flmx-panel-postgres" "$(get_env_var PANEL_POSTGRES_CONTAINER "$d1/.env")" "AI-03: default postgres name must stay unchanged"
  assert_equals "flmx-sqlite-data" "$(get_env_var FLMX_SQLITE_VOLUME "$d1/.env")" "AI-03: default sqlite volume must stay unchanged"
  assert_equals "flmx-postgres-data" "$(get_env_var FLMX_POSTGRES_VOLUME "$d1/.env")" "AI-03: default postgres volume must stay unchanged"
  assert_equals "flmx-network" "$(get_env_var FLMX_NETWORK "$d1/.env")" "AI-03: default network must stay unchanged"
  assert_equals "flmx-panel" "$(get_env_var COMPOSE_PROJECT_NAME "$d1/.env")" "AI-03: default project must stay unchanged"
  assert_equals "" "$(get_env_var FLMX_INSTANCE "$d1/.env")" "AI-03: default instance must not record an instance id"

  ( cd "$d2" && FLMX_INSTANCE=b2 write_new_panel_identity >/dev/null )
  assert_equals "flmx-panel-backend-b2" "$(get_env_var PANEL_BACKEND_CONTAINER "$d2/.env")" "AI-03: instance backend name must be suffixed"
  assert_equals "flmx-panel-frontend-b2" "$(get_env_var PANEL_FRONTEND_CONTAINER "$d2/.env")" "AI-03: instance frontend name must be suffixed"
  assert_equals "flmx-panel-postgres-b2" "$(get_env_var PANEL_POSTGRES_CONTAINER "$d2/.env")" "AI-03: instance postgres name must be suffixed"
  assert_equals "flmx-sqlite-data-b2" "$(get_env_var FLMX_SQLITE_VOLUME "$d2/.env")" "AI-03: instance sqlite volume must be suffixed"
  assert_equals "flmx-postgres-data-b2" "$(get_env_var FLMX_POSTGRES_VOLUME "$d2/.env")" "AI-03: instance postgres volume must be suffixed"
  assert_equals "flmx-network-b2" "$(get_env_var FLMX_NETWORK "$d2/.env")" "AI-03: instance network must be suffixed"
  assert_equals "flmx-panel-b2" "$(get_env_var COMPOSE_PROJECT_NAME "$d2/.env")" "AI-03: instance project must be suffixed"
  assert_equals "b2" "$(get_env_var FLMX_INSTANCE "$d2/.env")" "AI-03: instance id must be recorded in .env"

  local key v1 v2
  for key in PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER FLMX_SQLITE_VOLUME FLMX_POSTGRES_VOLUME FLMX_NETWORK COMPOSE_PROJECT_NAME; do
    v1=$(get_env_var "$key" "$d1/.env")
    v2=$(get_env_var "$key" "$d2/.env")
    [[ "$v1" != "$v2" ]] || fail "AI-03: instance naming must be disjoint from the default instance ($key=$v1)"
  done

  local legacy_out
  legacy_out=$( cd "$d3" && FLMX_INSTANCE=b2 write_new_panel_identity 2>&1 )
  assert_equals "flux-panel-backend" "$(get_env_var PANEL_BACKEND_CONTAINER "$d3/.env")" "AI-03: legacy template branch must keep legacy names"
  assert_equals "sqlite_data" "$(get_env_var FLMX_SQLITE_VOLUME "$d3/.env")" "AI-03: legacy template branch must keep legacy volumes"
  [[ "$legacy_out" == *"实例后缀不生效"* ]] || fail "AI-03: legacy template branch must warn that instance isolation does not apply"

  docker() {
    if [[ "$1" == "inspect" && "$2" == "flmx-panel-backend-b2" ]]; then
      return 0
    fi
    if [[ "$1" == "inspect" ]]; then
      return 1
    fi
    return 0
  }
  local empty_dir detected
  empty_dir=$(mktemp -d)
  detected=$( cd "$empty_dir" && FLMX_INSTANCE=b2 PANEL_DEPLOY_DIR="$empty_dir" detect_panel_backend_container )
  assert_equals "flmx-panel-backend-b2" "$detected" "AI-03: detect must probe the instance-derived backend name"

  local bad rc="0"
  bad=$(mktemp -d)
  ( cd "$bad" && FLMX_INSTANCE='Bad!' install_panel >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "AI-03: install must reject an invalid FLMX_INSTANCE"
  [[ ! -e "$bad/.env" && ! -e "$bad/docker-compose.yml" ]] || fail "AI-03: invalid instance id must not create deployment files"
)

# FLMX_INSTANCE 跨侧契约：脚本侧校验（panel_instance_id_is_valid）
# 与后端消费侧白名单（panelInstanceID：同字符集、全长 ≤32）必须逐
# 值一致——任一侧放宽而另一侧不跟，脚本派生名与后端派生名即分家。
# 金值表与后端 TestPanelInstanceID 的用例表同源；空白修剪是后端单
# 侧宽容（脚本侧安装入口对空白直接拒绝——'ea st' 已在非法集内），
# 不入本表比对。后缀形态与后端派生同形（空实例无后缀、非空 -<id>）；
# 随仓 compose 模板必须把 FLMX_INSTANCE 透传进后端容器环境，否则
# 后端进程读不到实例键、节点侧派生永不生效。
test_panel_instance_contract_matches_backend_whitelist() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local v
  for v in east panel-2 a_b 0 z; do
    panel_instance_id_is_valid "$v" || fail "instance contract: '$v' must be valid"
  done
  local a32 a33
  a32=$(printf 'a%.0s' $(seq 1 32))
  a33=$(printf 'a%.0s' $(seq 1 33))
  [[ ${#a32} -eq 32 && ${#a33} -eq 33 ]] || fail "instance contract: gold length construction broken"
  panel_instance_id_is_valid "$a32" || fail "instance contract: a 32-char id must be valid"
  if panel_instance_id_is_valid "$a33"; then
    fail "instance contract: a 33-char id must be invalid (backend consumer caps at 32)"
  fi
  for v in East -east _east 'ea st' ea/st ea.st ../x 'Bad!'; do
    if panel_instance_id_is_valid "$v"; then
      fail "instance contract: '$v' must be invalid"
    fi
  done
  if panel_instance_id_is_valid ""; then
    fail "instance contract: an empty id must be invalid as a value (empty means default instance)"
  fi

  local empty_dir
  empty_dir=$(mktemp -d)
  assert_equals "" "$(FLMX_INSTANCE="" PANEL_DEPLOY_DIR="$empty_dir" panel_instance_suffix)" \
    "instance contract: the default instance must have no suffix"
  assert_equals "-east" "$(FLMX_INSTANCE=east panel_instance_suffix)" \
    "instance contract: the suffix form must be -<id>"

  grep -q 'FLMX_INSTANCE: ${FLMX_INSTANCE:-}' "$ROOT_DIR/docker-compose-v4.yml" || \
    fail "instance contract: the v4 compose template must pass FLMX_INSTANCE to the backend"
  grep -q 'FLMX_INSTANCE: ${FLMX_INSTANCE:-}' "$ROOT_DIR/docker-compose-v6.yml" || \
    fail "instance contract: the v6 compose template must pass FLMX_INSTANCE to the backend"
)

# B2-03：实例 id 的读路径必须过契约白名单——.env/环境变量里的非
# 法值（非法字符、超 32）不得被带进派生名，按后端 validPanel-
# InstanceID 的回退口径视为空（默认实例）并告警；合法值原样读
# 回。install 入口对环境变量形态的硬拒绝不变（由
# test_panel_instance_identity_naming 钉住）。
test_panel_instance_id_read_path_validates() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)

  printf 'FLMX_INSTANCE=east\n' > "$dir/.env"
  assert_equals "east" "$( ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" panel_instance_id ) 2>/dev/null )" \
    "B2-03: a valid instance id from .env must be read back"
  assert_equals "-east" "$( ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" panel_instance_suffix ) 2>/dev/null )" \
    "B2-03: a valid instance id must derive its suffix"

  printf 'FLMX_INSTANCE=Bad Id!\n' > "$dir/.env"
  assert_equals "" "$( ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" panel_instance_id ) 2>/dev/null )" \
    "B2-03: an invalid instance id from .env must fall back to the default instance"
  assert_equals "" "$( ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" panel_instance_suffix ) 2>/dev/null )" \
    "B2-03: an invalid instance id must not derive a suffix"
  ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" panel_instance_id >/dev/null 2>"$dir/warn" )
  grep -q "不合法" "$dir/warn" || fail "B2-03: an invalid instance id must warn on the read path"

  local a40
  a40=$(printf 'a%.0s' $(seq 1 40))
  printf 'FLMX_INSTANCE=%s\n' "$a40" > "$dir/.env"
  assert_equals "" "$( ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" panel_instance_id ) 2>/dev/null )" \
    "B2-03: an over-32 instance id from .env must fall back to the default instance"

  assert_equals "" "$(FLMX_INSTANCE='East' PANEL_DEPLOY_DIR="$dir" panel_instance_id 2>/dev/null)" \
    "B2-03: an invalid instance id from the environment must fall back to the default instance on read paths"
  assert_equals "east" "$(FLMX_INSTANCE='east' PANEL_DEPLOY_DIR="$dir" panel_instance_id 2>/dev/null)" \
    "B2-03: a valid instance id from the environment must be read back"
)

# B2-04：同机多实例时，无实例上下文的 detect 探到默认实例、却另
# 有实例派生名后端存在，必须像双名冲突一样报错要求显式指定，不
# 得静默定位默认实例；默认实例单独存在、只有实例存在、显式实例
# 上下文、.env 已声明容器名、非法后缀仿冒名五景行为不变。
test_panel_detect_refuses_multi_instance_ambiguity() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER COMPOSE_PROJECT_NAME
  FIXTURE_NAMES=""
  docker() {
    case "$1" in
      inspect)
        if [[ "$2" == "-f" ]]; then
          case " $FIXTURE_NAMES " in *" $4 "*) printf 'ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n'; return 0 ;; *) return 1 ;; esac
        fi
        case " $FIXTURE_NAMES " in *" $2 "*) return 0 ;; *) return 1 ;; esac
        ;;
      ps) printf '%s\n' $FIXTURE_NAMES ;;
      *) return 0 ;;
    esac
  }

  local dir rc out
  dir=$(mktemp -d)

  FIXTURE_NAMES="flmx-panel-backend flmx-panel-backend-east"
  rc="0"; out=$( cd "$dir" && ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" detect_panel_backend_container ) 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "B2-04: detect must refuse when a default and an instance backend coexist without context"
  [[ "$out" == *"FLMX_INSTANCE"* ]] || fail "B2-04: the ambiguity refusal must name the explicit-selection escape hatch"

  FIXTURE_NAMES="flmx-panel-backend"
  out=$( cd "$dir" && ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" detect_panel_backend_container ) 2>/dev/null )
  assert_equals "flmx-panel-backend" "$out" "B2-04: a lone default instance must still be detected"

  FIXTURE_NAMES="flmx-panel-backend-east"
  out=$( cd "$dir" && ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" detect_panel_backend_container ) 2>/dev/null )
  assert_equals "flmx-panel-backend" "$out" "B2-04: an instance-only host must not be reported as ambiguous"

  FIXTURE_NAMES="flmx-panel-backend flmx-panel-backend-east"
  out=$( cd "$dir" && FLMX_INSTANCE=east PANEL_DEPLOY_DIR="$dir" detect_panel_backend_container 2>/dev/null )
  assert_equals "flmx-panel-backend-east" "$out" "B2-04: an explicit instance context must probe its own backend"

  printf 'PANEL_BACKEND_CONTAINER=flmx-panel-backend\n' > "$dir/.env"
  out=$( cd "$dir" && ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" detect_panel_backend_container ) 2>/dev/null )
  assert_equals "flmx-panel-backend" "$out" "B2-04: a declared backend container must win without ambiguity checks"

  rm -f "$dir/.env"
  FIXTURE_NAMES="flmx-panel-backend flmx-panel-backend-Bad_Id"
  out=$( cd "$dir" && ( unset FLMX_INSTANCE; PANEL_DEPLOY_DIR="$dir" detect_panel_backend_container ) 2>/dev/null )
  assert_equals "flmx-panel-backend" "$out" "B2-04: an invalid-suffix lookalike must not count as an instance"
)

# B2-05：DB_TYPE 的别名与大小写必须与后端接受集同口径——后端
# TrimSpace＋ToLower 后接受 sqlite/postgres/postgresql（空＝
# sqlite），脚本的 get_current_db_type 与更新校验曾只认原样小写
# sqlite/postgres，按 postgresql.md 填 postgresql 的手工 PG 部
# 署被脚本更新直接拒绝。不支持的值仍须拒绝。
test_panel_db_type_aliases_match_backend() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)
  cd "$dir"
  printf 'services: {}\n' > docker-compose.yml
  DOCKER_CMD="true"

  local v
  for v in postgresql Postgres POSTGRES PostgreSQL; do
    printf 'DB_TYPE=%s\n' "$v" > .env
    assert_equals "postgres" "$(get_current_db_type)" "B2-05: DB_TYPE=$v must be read as postgres like the backend does"
  done
  printf 'DB_TYPE=SQLite\n' > .env
  assert_equals "sqlite" "$(get_current_db_type)" "B2-05: DB_TYPE case must not matter for sqlite either"
  printf 'DB_TYPE=\n' > .env
  assert_equals "sqlite" "$(get_current_db_type)" "B2-05: an empty DB_TYPE must stay sqlite"

  validate_with_db_type() {
    printf 'JWT_SECRET=x\nBACKEND_PORT=6365\nFRONTEND_PORT=6366\nDB_TYPE=%s\nDATABASE_URL=postgres://u:p@h/db\nPOSTGRES_PASSWORD=pw\n' "$1" > .env
    local rc="0"
    validate_panel_update_environment >/dev/null 2>&1 || rc="$?"
    printf '%s' "$rc"
  }
  assert_equals "0" "$(validate_with_db_type postgresql)" "B2-05: update validation must accept the postgresql alias"
  assert_equals "0" "$(validate_with_db_type Postgres)" "B2-05: update validation must accept case variants"
  assert_equals "1" "$(validate_with_db_type mysql)" "B2-05: update validation must still reject unsupported DB_TYPE values"
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
  grep -q "本次更新目标" "$log" || fail "B2-04: update must name its target deployment before mutating anything"
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
  printf 'services: {}\n' > "$deploy_dir/docker-compose.yml.bak.123"
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
  [[ ! -e "$deploy_dir/docker-compose.yml.bak.123" ]] || fail "L2-14: docker-compose.yml.bak.* (forced-reinstall backups) must be removed"
  [[ ! -e "$deploy_dir/gost.db.bak" ]] || fail "L2-14: gost.db.bak must be removed"
  [[ ! -e "$deploy_dir/.env.pre-postgres-migration" ]] || fail "L2-14: the PG migration snapshot must be removed"
  grep -q "卸载完成" "$other_dir/uninstall.log" || fail "L2-14: completion must be reported after real removal"
  grep -q "部署目录：$deploy_dir" "$other_dir/uninstall.log" || fail "B2-04: the uninstall confirmation must name the deployment directory it is about to delete"

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

# R6-03（菜单 5 命中支）：部署目录外运行菜单 5——resolve 定位到部
# 署目录（.env 写 FRONTEND_PORT=7366）后，Caddyfile 必须反代到
# 7366、完成输出必须打印实际反代端口。resolve 桩只回填
# PANEL_DEPLOY_DIR、不 cd（比真实 resolve 更严：证明端口确从定位
# 到的部署 .env 读出，而非恰好 cwd 命中）。旧实现从不调用
# resolve、候选链全不中、静默按 6366 写——本用例对旧代码必红。
test_panel_menu5_resolves_deploy_dir_port_outside_cwd() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env
  unset FRONTEND_PORT

  local deploy_dir scratch_dir
  deploy_dir=$(mktemp -d)
  scratch_dir=$(mktemp -d)
  printf 'FRONTEND_PORT=7366\n' > "$deploy_dir/.env"
  export R6_DEPLOY_DIR="$deploy_dir"
  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$R6_DEPLOY_DIR"
    export PANEL_DEPLOY_DIR
    return 0
  }

  local log="$FIXTURE_DIR/menu5-resolve.log" rc="0"
  ( cd "$scratch_dir" && setup_domain_proxy <<< "panel.example.com" >"$log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R6-03: menu 5 should succeed when deployment resolution finds the deploy dir"
  grep -q 'reverse_proxy 127.0.0.1:7366' "$CADDYFILE_PATH" || \
    fail "R6-03: menu 5 must proxy to the FRONTEND_PORT of the resolved deployment .env"
  grep -q '实际反代端口: 127.0.0.1:7366' "$log" || \
    fail "R6-03: menu 5 completion output must print the actual reverse-proxy port"
)

# R6-03（菜单 6 命中支）：同菜单 5——目录外运行、resolve 定位到写
# 有 7366 的部署 .env 时，追加的订阅前置块必须反代到 7366 且完成
# 输出打印实际反代端口（旧实现静默 6366，必红）。
test_panel_menu6_resolves_deploy_dir_port_outside_cwd() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env
  unset FRONTEND_PORT

  cat > "$CADDYFILE_PATH" <<'EOF'
panel.example.com {
    reverse_proxy 127.0.0.1:7366
}
EOF

  local deploy_dir scratch_dir
  deploy_dir=$(mktemp -d)
  scratch_dir=$(mktemp -d)
  printf 'FRONTEND_PORT=7366\n' > "$deploy_dir/.env"
  export R6_DEPLOY_DIR="$deploy_dir"
  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$R6_DEPLOY_DIR"
    export PANEL_DEPLOY_DIR
    return 0
  }

  local log="$FIXTURE_DIR/menu6-resolve.log" rc="0"
  ( cd "$scratch_dir" && setup_subscription_front_domain <<< "sub.example.com" >"$log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R6-03: menu 6 should succeed when deployment resolution finds the deploy dir"
  grep -q '^sub.example.com {' "$CADDYFILE_PATH" || \
    fail "R6-03: menu 6 must append the front block"
  grep -A1 '^sub.example.com {' "$CADDYFILE_PATH" | grep -q 'reverse_proxy 127.0.0.1:7366' || \
    fail "R6-03: menu 6 must proxy the front block to the resolved deployment FRONTEND_PORT"
  grep -q '实际反代端口: 127.0.0.1:7366' "$log" || \
    fail "R6-03: menu 6 completion output must print the actual reverse-proxy port"
)

# R6-03（未命中支）：定位失败、候选链全不中时不得静默——必须显
# 式告警「未定位到部署 .env」后才回退 6366（旧实现无任何告警，
# 必红）。
test_panel_menu5_warns_when_deploy_env_not_found() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env
  unset FRONTEND_PORT
  unset PANEL_DEPLOY_DIR

  resolve_panel_deployment() { return 1; }

  local scratch_dir
  scratch_dir=$(mktemp -d)
  local log="$FIXTURE_DIR/menu5-noresolve.log" rc="0"
  ( cd "$scratch_dir" && setup_domain_proxy <<< "panel.example.com" >"$log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R6-03: menu 5 should still complete with the default port when nothing is located"
  grep -q '未定位到部署 .env' "$log" || \
    fail "R6-03: menu 5 must explicitly warn when no deployment .env is located"
  grep -q '实际反代端口: 127.0.0.1:6366' "$log" || \
    fail "R6-03: menu 5 completion output must print the actual reverse-proxy port even on the default fallback"
  grep -q 'reverse_proxy 127.0.0.1:6366' "$CADDYFILE_PATH" || \
    fail "R6-03: menu 5 should fall back to 6366 after warning"
)

# R8-16（菜单 6 端到端）：环境预置 FRONTEND_PORT 为畸形值（abc /
# 99999）时不得静默采纳——必须告警点名并忽略预置、回退到部署
# .env 候选链（全不中时 6366），写出的前置块与完成回执只许出现
# 合法端口。旧实现对预置分支零校验：abc 被逐字写成
# reverse_proxy 127.0.0.1:abc 且成功回执照印该端口（caddy 二进制
# 缺位、校验被跳过时坏块直接落盘），本用例对旧代码必红。
test_panel_menu6_rejects_malformed_preset_frontend_port() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env
  unset PANEL_DEPLOY_DIR
  resolve_panel_deployment() { return 1; }

  local scratch_dir
  scratch_dir=$(mktemp -d)

  cat > "$CADDYFILE_PATH" <<'EOF'
panel.example.com {
    reverse_proxy 127.0.0.1:6366
}
EOF

  FRONTEND_PORT=abc
  local log="$FIXTURE_DIR/menu6-badpreset.log" rc="0"
  ( cd "$scratch_dir" && setup_subscription_front_domain <<< "sub.example.com" >"$log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "R8-16: menu 6 should still complete via fallback when the preset port is malformed"
  grep -q '环境预置的 FRONTEND_PORT 不是有效端口' "$log" || \
    fail "R8-16: menu 6 must warn by name when the preset FRONTEND_PORT is malformed"
  if grep -q '127.0.0.1:abc' "$CADDYFILE_PATH"; then
    fail "R8-16: malformed preset port must never be written into the Caddyfile"
  fi
  grep -A1 '^sub.example.com {' "$CADDYFILE_PATH" | grep -q 'reverse_proxy 127.0.0.1:6366' || \
    fail "R8-16: menu 6 must fall back to a valid port for the front block"
  grep -q '实际反代端口: 127.0.0.1:6366' "$log" || \
    fail "R8-16: menu 6 completion output must print the valid fallback port"

  FRONTEND_PORT=99999
  local log2="$FIXTURE_DIR/menu6-badpreset2.log" rc2="0"
  ( cd "$scratch_dir" && setup_subscription_front_domain <<< "sub2.example.com" >"$log2" 2>&1 ) || rc2="$?"
  assert_equals "0" "$rc2" "R8-16: menu 6 should still complete via fallback when the preset port is out of range"
  grep -q '环境预置的 FRONTEND_PORT 不是有效端口' "$log2" || \
    fail "R8-16: out-of-range preset port must be warned by name too"
  if grep -q '127.0.0.1:99999' "$CADDYFILE_PATH"; then
    fail "R8-16: out-of-range preset port must never be written into the Caddyfile"
  fi
  grep -A1 '^sub2.example.com {' "$CADDYFILE_PATH" | grep -q 'reverse_proxy 127.0.0.1:6366' || \
    fail "R8-16: menu 6 must fall back to a valid port for the second front block"
)

# R8-16（resolve 单元支）：预置畸形时不是硬失败、也不是直接落到
# 默认值——告警后清掉预置并续走候选链，部署 .env 的合法值仍须被
# 采到（与 .env 分支「合法才采」同语义）。旧实现直接采纳 abc，
# FRONTEND_PORT 终值为 abc，本用例对旧代码必红。
test_panel_resolve_falls_through_chain_on_malformed_preset() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  unset PANEL_DEPLOY_DIR

  local deploy_dir scratch_dir
  deploy_dir=$(mktemp -d)
  scratch_dir=$(mktemp -d)
  printf 'FRONTEND_PORT=7366\n' > "$deploy_dir/.env"
  export R8_DEPLOY_DIR="$deploy_dir"
  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$R8_DEPLOY_DIR"
    export PANEL_DEPLOY_DIR
    return 0
  }

  # 在当前（测试子）壳内直调：resolve 对 FRONTEND_PORT 的修改须对断言可见。
  FRONTEND_PORT=abc
  local log="$scratch_dir/resolve-badpreset.log"
  resolve_caddy_frontend_port >"$log" 2>&1
  assert_equals "7366" "$FRONTEND_PORT" \
    "R8-16: malformed preset must be ignored and the deployment .env port adopted"
  grep -q '环境预置的 FRONTEND_PORT 不是有效端口' "$log" || \
    fail "R8-16: resolve must warn by name when ignoring a malformed preset"
)

# R6-04：域名解析到非本机 IP 时不得打 ✅ 假绿——必须按未解析同级
# 警告并要求显式确认：拒绝确认则终止且不写 Caddyfile，确认后才
# 继续（旧实现只判能否解析、直接 ✅ 放行，两幕对旧代码必红）。
test_panel_menu5_dns_pointing_elsewhere_warns() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env
  # 夹具 getent 恒解析到 203.0.113.10；本机公网 IP 改桩为他值即
  # 构造「解析未指向本机」。
  get_public_ip() { printf '198.51.100.99'; }

  local rc="0" log="$FIXTURE_DIR/menu5-dns-mismatch-refuse.log"
  setup_domain_proxy <<< "panel.example.com" >"$log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R6-04: menu 5 must stop when DNS points elsewhere and the operator does not confirm"
  grep -q '未指向本机' "$log" || \
    fail "R6-04: DNS pointing elsewhere must be warned as not pointing at this machine"
  if grep -q '✅ 域名解析到' "$log"; then
    fail "R6-04: DNS pointing elsewhere must not print the green check"
  fi
  [[ ! -f "$CADDYFILE_PATH" ]] || \
    fail "R6-04: no Caddyfile may be written when the operator refuses at the DNS warning"

  local ok_log="$FIXTURE_DIR/menu5-dns-mismatch-confirm.log"
  setup_domain_proxy <<< $'panel.example.com\ny' >"$ok_log" 2>&1 || \
    fail "R6-04: menu 5 should proceed after explicit confirmation despite the DNS mismatch"
  grep -q '^panel.example.com {' "$CADDYFILE_PATH" || \
    fail "R6-04: menu 5 must write the site block after explicit confirmation"
)

test_proxy_enabled_case_insensitive
test_install_script_slow_direct_joins_ranking
test_panel_slow_direct_joins_ranking
test_install_script_download_budget_enforced
test_install_script_download_resumes_across_routes
test_install_script_resolve_budget_enforced
test_install_script_uppercase_sha256_accepted
test_panel_migrate_postgres_rollbacks
test_panel_ownership_predicates_matrix
test_panel_resolve_refuses_foreign_panel_deployment
test_panel_resolve_accepts_instance_deployment
test_panel_migrate_guard_precedes_download
test_panel_install_guards_single_file_and_backups
test_panel_uninstall_without_docker_respects_ownership
test_panel_instance_identity_naming
test_panel_instance_contract_matches_backend_whitelist
test_panel_instance_id_read_path_validates
test_panel_detect_refuses_multi_instance_ambiguity
test_panel_db_type_aliases_match_backend
test_panel_update_honors_version_priority_chain
test_panel_uninstall_locates_deployment_and_cleans_artifacts
test_panel_download_verified_uses_timeouts
test_panel_menu6_dedicated_coverage
test_panel_menu_is_active_failure_rolls_back
test_panel_domain_compare_case_insensitive
test_panel_menu5_resolves_deploy_dir_port_outside_cwd
test_panel_menu6_resolves_deploy_dir_port_outside_cwd
test_panel_menu5_warns_when_deploy_env_not_found
test_panel_menu6_rejects_malformed_preset_frontend_port
test_panel_resolve_falls_through_chain_on_malformed_preset
test_panel_menu5_dns_pointing_elsewhere_warns
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
test_panel_anchor_corroboration_passes
test_panel_anchor_corroboration_mismatch_aborts
test_panel_anchor_corroboration_degrades
test_install_script_binary_anchor_passes
test_install_script_binary_anchor_mismatch_rejects
test_install_script_binary_anchor_degrades
test_update_flux_agent_downloads_without_prompt
test_update_flux_agent_skips_proxy_prompt_when_not_installed
test_update_flux_agent_prints_online_confirmation_guidance
test_flux_agent_health_ok_requires_stable_pid
test_update_flux_agent_rolls_back_flapping_binary
test_update_flux_agent_aborts_when_backup_fails
test_update_flux_agent_aborts_when_backup_fails_with_stale_bak
test_extract_leading_semver_matches_go_rules
test_update_flux_agent_rejects_downgrade
test_update_flux_agent_allows_higher_and_incomparable
test_main_propagates_operation_exit_codes
test_install_flux_agent_preserves_legacy_gost_when_download_fails
test_update_flux_agent_preserves_legacy_gost_when_download_fails
test_agent_config_addr_normalizes_json_escapes
test_guard_agent_dir_ownership_matrix
test_update_flux_agent_refuses_unowned_agent
test_update_flux_agent_menu_guard_pass_forms
test_update_flux_agent_menu_guard_interactive
test_uninstall_flux_agent_refuses_unowned_agent
test_uninstall_flux_agent_menu_guard_pass_forms
test_uninstall_flux_agent_cleans_host_tuning_residue
test_uninstall_flux_agent_host_cleanup_partial_failure_and_idempotent
test_install_flux_agent_writes_json_safe_config
test_install_script_bootstraps_bash_for_alpine
test_install_flux_agent_uses_openrc
test_remove_flux_agent_service_uses_openrc
test_cleanup_legacy_gost_installation_removes_service_and_binary
test_cleanup_legacy_gost_installation_preserves_unrelated_gost
test_cleanup_legacy_gost_installation_skips_systemd_on_openrc
test_cleanup_legacy_gost_installation_skips_foreign_agent
test_cleanup_legacy_gost_installation_skips_unreadable_addr
test_cleanup_legacy_gost_installation_takeover_cleans_foreign_agent
test_cleanup_legacy_gost_installation_matches_installed_agent_addr
test_cleanup_legacy_gost_installation_interactive_confirm
test_uninstall_flux_agent_host_cleanup_preserves_foreign_legacy_resources
test_install_script_accepts_proxy_url_env_without_prompt
test_panel_install_script_can_disable_proxy
test_panel_install_script_builds_canonical_compose_urls
test_update_panel_downloads_without_prompt
test_update_panel_backup_failure_names_force_reinstall_only_when_container_missing
test_resolve_panel_deployment_uses_container_labels
test_validate_panel_update_environment_rejects_empty_required_values
test_frontend_port_quoted_value_consistent_across_faces
test_backup_sqlite_for_update_pauses_copies_and_unpauses
test_backup_sqlite_for_update_excludes_github_cache
test_create_panel_update_backup_rotates_to_keep
test_prune_timestamped_backups_keeps_newest
test_prune_timestamped_backups_future_name_old_mtime_pruned
test_install_panel_refusal_names_force_reinstall_usage
test_install_panel_up_failure_names_force_reinstall
test_install_panel_postgres_health_failure_has_guidance
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
test_panel_menu5_caddy_list_truncated_not_landed
test_panel_menu5_caddy_list_removed_when_install_fails
test_install_flux_agent_rejects_flapping_service
test_main_delete_self_only_on_success
test_panel_main_delete_self_only_on_success
test_main_requires_root
test_script_secret_surfaces_closed
test_ensure_compose_trusted_proxy_hops_injects_mapping_form
test_ensure_compose_trusted_proxy_hops_keeps_existing_line
test_ensure_compose_trusted_proxy_hops_list_form
test_ensure_compose_trusted_proxy_hops_list_form_comment_first
test_ensure_compose_trusted_proxy_hops_env_header_trailing_comment
test_ensure_compose_trusted_proxy_hops_decoy_string_elsewhere
test_ensure_compose_trusted_proxy_hops_unrecognized_env_form_keeps_original
test_compose_hops_injection_problem_detects_form_mismatch
test_shipped_compose_templates_carry_hops_line
test_validate_panel_compose_content_accepts_shipped_templates
test_validate_panel_compose_content_rejects_bad_files
test_validate_panel_update_compose_uses_content_validation
test_activate_panel_update_files_keeps_hops_line
test_pgloader_command_file_terminated
test_tcpkill_missing_warns_instead_of_silent

echo "install script tests passed"
