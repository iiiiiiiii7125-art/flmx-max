#!/bin/bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

# T-INC-01 宿主机安全门：本套件会 source install.sh/panel_install.sh
# 并以 root 形态调用其服务/文件操作（桩接管绝大多数路径，但
# INC-01 事故证明桩层有漏：基线套件在装有真实旧布局 agent 的测
# 试机宿主机上裸跑时，旧布局 stop/disable/删 unit/rm -rf 序列突
# 破隔离、销毁了在跑 agent）。开跑前先判宿主：真实 agent 布局
# （任一布局的安装目录或服务文件）在位且未声明隔离环境
# （FLMX_SUITE_ISOLATED=1，只许在专用容器/VM 内设置）→
# fail-closed 中止并点名，一条用例都不许跑。FLMX_SUITE_HOST_ROOT
# 仅供本门的单测夹具改指假宿主根，正常运行恒为空（查真实 /etc）。
suite_host_safety_gate() {
  if [[ "${FLMX_SUITE_ISOLATED:-}" == "1" ]]; then
    return 0
  fi
  local root="${FLMX_SUITE_HOST_ROOT:-}"
  local hits=""
  [[ -d "$root/etc/flux_agent" ]] && hits="$hits $root/etc/flux_agent"
  [[ -d "$root/etc/flmx_agent" ]] && hits="$hits $root/etc/flmx_agent"
  [[ -f "$root/etc/systemd/system/flux_agent.service" ]] && hits="$hits $root/etc/systemd/system/flux_agent.service"
  [[ -f "$root/etc/systemd/system/flmx_agent.service" ]] && hits="$hits $root/etc/systemd/system/flmx_agent.service"
  [[ -f "$root/etc/init.d/flux_agent" ]] && hits="$hits $root/etc/init.d/flux_agent"
  [[ -f "$root/etc/init.d/flmx_agent" ]] && hits="$hits $root/etc/init.d/flmx_agent"
  if [[ -n "$hits" ]]; then
    echo "FAIL: T-INC-01 host safety gate: real agent layout detected on this host:$hits" >&2
    echo "  本套件曾在装有真实 agent 的宿主机上裸跑时销毁在跑 agent（INC-01）。只许在专用容器/VM 内运行；确认环境隔离后设置 FLMX_SUITE_ISOLATED=1 再跑。" >&2
    return 1
  fi
  return 0
}

suite_host_safety_gate || exit 1

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

# A2-01 残面（audit-max）：事故态门的旧卷内容探测须三态——挂载点
# 不可读是「未知」而非「无数据」。旧实现 panel_volume_file_exists
# 二值 fail-open：inspect 给不出挂载点即按卷内无文件处理，无键＋
# 实挂新缺省卷＋旧卷存在（内容未知）时两格门都被越过、钉键把可
# 能的事故态正式化。三幕：① SQLite 格未知 → 中止、不写卷键、点
# 名未知；② PG 格未知（SQLite 侧非事故形态）→ 中止、不写 PG 卷
# 键；③ 挂载点可读且旧卷确定无数据文件 → 门放行、照常钉实读值
# （既有口径回归）。
test_panel_pin_accident_gate_unknown_mount_aborts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  # docker 桩：卷存在性恒真；挂载点仅 FIXTURE_MOUNT_OK=1 时可读
  # （回显 $FIXTURE_VOLROOT/<卷名>），否则 inspect -f 取不到挂载点。
  # 容器挂载实读由 FIXTURE_SQLITE_VOLUME/FIXTURE_POSTGRES_VOLUME 给定。
  pin_unknown_docker() {
    docker() {
      if [[ "$1" == "volume" && "$2" == "inspect" ]]; then
        if [[ "$3" == "-f" ]]; then
          [[ "${FIXTURE_MOUNT_OK:-}" == "1" ]] || return 1
          printf '%s' "$FIXTURE_VOLROOT/$5"
          return 0
        fi
        return 0
      fi
      if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
        case "$3" in
          *"/app/data"*) printf '%s' "${FIXTURE_SQLITE_VOLUME:-}" ;;
          *"/var/lib/postgresql/data"*) printf '%s' "${FIXTURE_POSTGRES_VOLUME:-}" ;;
          *) printf '' ;;
        esac
        return 0
      fi
      return 0
    }
  }

  # ① SQLite 格未知
  local dir1
  dir1=$(mktemp -d)
  cat > "$dir1/.env" <<'EOF'
DB_TYPE=sqlite
COMPOSE_PROJECT_NAME=kept_project
EOF
  pin_unknown_docker
  FIXTURE_MOUNT_OK=""
  FIXTURE_SQLITE_VOLUME="flmx-sqlite-data"
  FIXTURE_POSTGRES_VOLUME="flmx-postgres-data"
  local rc="0"
  ( cd "$dir1" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "A2-01R: pinning must abort when the legacy sqlite volume exists but its mountpoint is unreadable (unknown must not read as empty)"
  grep -q '资源键钉值中止' "$dir1/pin.log" || fail "A2-01R: the unknown-state abort must be named in the pin output"
  grep -q '无法确认' "$dir1/pin.log" || fail "A2-01R: the abort must name that the legacy volume content could not be determined"
  if grep -q '^FLMX_SQLITE_VOLUME=' "$dir1/.env"; then
    fail "A2-01R: no sqlite volume key may be written when the accident gate aborts on unknown state"
  fi

  # ② PG 格未知（SQLite 侧答自定义卷名、不构成事故形态）
  local dir2
  dir2=$(mktemp -d)
  cat > "$dir2/.env" <<'EOF'
DB_TYPE=postgres
COMPOSE_PROJECT_NAME=kept_project
EOF
  pin_unknown_docker
  FIXTURE_MOUNT_OK=""
  FIXTURE_SQLITE_VOLUME="custom_sqlite"
  FIXTURE_POSTGRES_VOLUME="flmx-postgres-data"
  rc="0"
  ( cd "$dir2" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "A2-01R: pinning must abort when the legacy postgres volume exists but its mountpoint is unreadable"
  grep -q 'postgres_data' "$dir2/pin.log" || fail "A2-01R: the PG unknown-state abort must name the legacy postgres volume"
  if grep -q '^FLMX_POSTGRES_VOLUME=' "$dir2/.env"; then
    fail "A2-01R: no postgres volume key may be written when the PG accident gate aborts on unknown state"
  fi

  # ③ 挂载点可读、旧卷确定无数据文件 → 放行并钉实读值
  local dir3
  dir3=$(mktemp -d)
  cat > "$dir3/.env" <<'EOF'
DB_TYPE=sqlite
COMPOSE_PROJECT_NAME=kept_project
EOF
  FIXTURE_VOLROOT="$dir3/volroot"
  mkdir -p "$FIXTURE_VOLROOT/sqlite_data" "$FIXTURE_VOLROOT/postgres_data"
  pin_unknown_docker
  FIXTURE_MOUNT_OK="1"
  FIXTURE_SQLITE_VOLUME="flmx-sqlite-data"
  FIXTURE_POSTGRES_VOLUME="flmx-postgres-data"
  ( cd "$dir3" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || \
    fail "A2-01R: pinning must proceed when both legacy volumes are readable and provably hold no data files"
  grep -q '^FLMX_SQLITE_VOLUME=flmx-sqlite-data$' "$dir3/.env" || fail "A2-01R: the live-read sqlite volume must be pinned once the gate clears"
  grep -q '^FLMX_POSTGRES_VOLUME=flmx-postgres-data$' "$dir3/.env" || fail "A2-01R: the live-read postgres volume must be pinned once the gate clears"
)

# A2-02（audit-max）：升级后卷自检补 PG 面——旧实现只有
# verify_panel_update_sqlite_volume，PG 部署重建后挂到模板缺省空
# 卷（flmx-postgres-data）时健康检查与 SQLite 自检双过、照报更新
# 完成。两幕（均 PG 模式、卷键预钉 real_pg）：① 重建后 PG 实挂
# flmx-postgres-data → PG 自检判失败、更新回滚、.env 逐字恢复；
# ② 重建后仍挂 real_pg 且卷内有 PG_VERSION → 自检点名通过、更
# 新成功。
test_panel_update_pg_volume_check_rolls_back_on_mismatch() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  wrap_pg_volume_docker() {
    eval "$(declare -f docker | sed '1s/^docker /fixture_docker_base /')"
    docker() {
      if [[ "$1" == "volume" && "$2" == "inspect" ]]; then
        if [[ "$3" == "-f" ]]; then
          printf '%s' "$FIXTURE_VOLROOT/$5"
          return 0
        fi
        return 0
      fi
      if [[ "$1" == "inspect" && "$2" == "-f" && "$3" == *"/var/lib/postgresql/data"* ]]; then
        if [[ -n "${FIXTURE_REBUILT_POSTGRES_VOLUME:-}" && -e "$FIXTURE_DIR/.rebuilt" ]]; then
          printf '%s' "$FIXTURE_REBUILT_POSTGRES_VOLUME"
          return 0
        fi
        printf '%s' "${FIXTURE_POSTGRES_VOLUME:-postgres_data}"
        return 0
      fi
      fixture_docker_base "$@"
    }
  }

  # ① 不符必回滚
  local dir1
  dir1=$(mktemp -d)
  setup_rename_update_fixture "$dir1" current
  printf 'FLMX_POSTGRES_VOLUME=real_pg\n' >> "$dir1/.env"
  cp -p "$dir1/.env" "$dir1/env.pristine"
  FIXTURE_VOLROOT="$dir1/volroot"
  mkdir -p "$FIXTURE_VOLROOT/real_pg"
  : > "$FIXTURE_VOLROOT/real_pg/PG_VERSION"
  wrap_pg_volume_docker
  get_current_db_type() { echo "postgres"; }
  FIXTURE_POSTGRES_VOLUME="real_pg"
  FIXTURE_REBUILT_POSTGRES_VOLUME="flmx-postgres-data"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  local rc="0"
  update_panel >"$dir1/update.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "A2-02: update must fail when the rebuilt postgres container mounts a different data volume than recorded"
  grep -q '升级后 PG 卷名自检失败' "$dir1/update.log" || fail "A2-02: the PG volume self-check failure must be named in the update output"
  grep -q 'flmx-postgres-data' "$dir1/update.log" || fail "A2-02: the PG self-check failure must name the actually mounted volume"
  grep -q 'real_pg' "$dir1/update.log" || fail "A2-02: the PG self-check failure must name the expected recorded volume"
  grep -q '已恢复更新前版本' "$dir1/update.log" || fail "A2-02: a PG self-check failure must roll back to the pre-update version"
  diff -q "$dir1/env.pristine" "$dir1/.env" >/dev/null || fail "A2-02: rollback after a PG self-check failure must restore the pre-update .env verbatim"

  # ② 相符通过
  local dir2
  dir2=$(mktemp -d)
  setup_rename_update_fixture "$dir2" current
  printf 'FLMX_POSTGRES_VOLUME=real_pg\n' >> "$dir2/.env"
  FIXTURE_VOLROOT="$dir2/volroot"
  mkdir -p "$FIXTURE_VOLROOT/real_pg"
  : > "$FIXTURE_VOLROOT/real_pg/PG_VERSION"
  wrap_pg_volume_docker
  get_current_db_type() { echo "postgres"; }
  FIXTURE_POSTGRES_VOLUME="real_pg"
  FIXTURE_REBUILT_POSTGRES_VOLUME=""
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir2/update.log" 2>&1 || fail "A2-02: update must succeed when the rebuilt postgres container still mounts the recorded volume"
  grep -q '升级后 PG 卷名自检通过' "$dir2/update.log" || fail "A2-02: the PG volume self-check pass must be named in the update output"
)

# A2-03（audit-max）：缺省 scope 钉键与 live scope 同口径——无实
# 读证据不写卷键。旧实现两处反着来：SQLite 实读为空（bind 挂载
# 等）照样写死 sqlite_data；PG 实读为空时只要 postgres_data 卷
# 存在就认领为本部署 PG 卷（SQLite 部署主机上一个无关的
# postgres_data 卷即被写进 .env）。三幕：① SQLite 实读为空 →
# 不写 FLMX_SQLITE_VOLUME＋点名；② SQLite 部署＋无关
# postgres_data 卷存在、PG 实读为空 → 不写 FLMX_POSTGRES_VOLUME
# ＋点名不认领；③ 实读有值 → 照常钉实读值（既有口径回归）。
test_panel_pin_no_live_read_no_volume_key() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  pin_noread_docker() {
    docker() {
      if [[ "$1" == "volume" && "$2" == "inspect" ]]; then
        return 0
      fi
      if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
        case "$3" in
          *"/app/data"*) printf '%s' "${FIXTURE_SQLITE_VOLUME:-}" ;;
          *"/var/lib/postgresql/data"*) printf '%s' "${FIXTURE_POSTGRES_VOLUME:-}" ;;
          *) printf '' ;;
        esac
        return 0
      fi
      return 0
    }
  }

  # ① SQLite 实读为空
  local dir1
  dir1=$(mktemp -d)
  cat > "$dir1/.env" <<'EOF'
DB_TYPE=sqlite
COMPOSE_PROJECT_NAME=kept_project
EOF
  pin_noread_docker
  FIXTURE_SQLITE_VOLUME=""
  FIXTURE_POSTGRES_VOLUME=""
  ( cd "$dir1" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || \
    fail "A2-03: pinning must not fail when the sqlite mount cannot be read"
  if grep -q '^FLMX_SQLITE_VOLUME=' "$dir1/.env"; then
    fail "A2-03: no sqlite volume key may be written without a live-read value (bind mounts must not be pinned to the template fallback)"
  fi
  grep -q '不钉 FLMX_SQLITE_VOLUME' "$dir1/pin.log" || fail "A2-03: the skipped sqlite pin must be named in the pin output"

  # ② 无关 postgres_data 卷不得被认领
  local dir2
  dir2=$(mktemp -d)
  cat > "$dir2/.env" <<'EOF'
DB_TYPE=sqlite
COMPOSE_PROJECT_NAME=kept_project
EOF
  pin_noread_docker
  FIXTURE_SQLITE_VOLUME="my_sqlite"
  FIXTURE_POSTGRES_VOLUME=""
  ( cd "$dir2" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || \
    fail "A2-03: pinning must not fail in the foreign-postgres-volume scenario"
  grep -q '^FLMX_SQLITE_VOLUME=my_sqlite$' "$dir2/.env" || fail "A2-03: the live-read sqlite volume must still be pinned"
  if grep -q '^FLMX_POSTGRES_VOLUME=' "$dir2/.env"; then
    fail "A2-03: an unrelated postgres_data volume must not be claimed as this deployment's PG volume by existence alone"
  fi
  grep -q '不钉 FLMX_POSTGRES_VOLUME' "$dir2/pin.log" || fail "A2-03: the skipped postgres pin must be named in the pin output"

  # ③ 实读有值照常钉
  local dir3
  dir3=$(mktemp -d)
  cat > "$dir3/.env" <<'EOF'
DB_TYPE=postgres
COMPOSE_PROJECT_NAME=kept_project
EOF
  pin_noread_docker
  FIXTURE_SQLITE_VOLUME="my_sqlite"
  FIXTURE_POSTGRES_VOLUME="my_pg"
  ( cd "$dir3" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || \
    fail "A2-03: pinning must succeed when both mounts are readable"
  grep -q '^FLMX_SQLITE_VOLUME=my_sqlite$' "$dir3/.env" || fail "A2-03: the live-read sqlite volume must be pinned"
  grep -q '^FLMX_POSTGRES_VOLUME=my_pg$' "$dir3/.env" || fail "A2-03: the live-read postgres volume must be pinned"
)

# A1-01（audit-max）：路由下载加跨腿总预算——旧实现只有单腿
# 600s×重试×双腿×约 10 路线，涓流路线（高于 1KB/s 地板看门狗）
# 可把一次下载拖到小时级；install.sh 同面已有 240s 总预算
# （FLMX_DOWNLOAD_BUDGET_SECONDS），面板侧缺失。三幕：① 预算=0
# → routed 在发出任何下载请求前即以总预算点名失败；② 给
# download_verified 传 30s 后截止 → 两腿 --max-time 均收紧到
# 30（不再是 600）且带 --retry-max-time；③ 预算充足时 routed
# 照常成功（既有口径回归）。
test_panel_download_routed_total_budget() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local log dest out
  log=$(mktemp)
  dest=$(mktemp -u)
  out=$(mktemp)
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

  # ① 预算耗尽：不发任何请求、点名失败
  PROXY_ENABLED="false"
  PROXY_URL=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  FLMX_DOWNLOAD_BUDGET_SECONDS=0
  local rc="0"
  download_verified_routed "https://example.com/asset.yml" "$dest" >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "A1-01: an exhausted download budget must fail the routed download"
  grep -q '下载总预算' "$out" || fail "A1-01: the budget failure must be named in the output"
  assert_equals "0" "$(grep -c . "$log" || true)" "A1-01: no download request may be issued once the budget is exhausted"

  # ② 截止收紧两腿（两腿各自在调用时点算剩余量，跨秒后可差 1-2s，
  # 故断言区间：两腿帽都落在 [25,30]、无一为 600）
  : > "$log"
  local deadline=$(( $(date +%s) + 30 ))
  download_verified "https://example.com/asset.yml" "$dest" "$deadline" >/dev/null || \
    fail "A1-01: download_verified with a live deadline should succeed with the stub"
  local leg_caps leg_cap_value
  leg_caps=$(grep -o -- '--max-time [0-9]*' "$log" | awk '{print $2}')
  assert_equals "2" "$(printf '%s' "$leg_caps" | grep -c .)" "A1-01: both legs must carry a budget-derived --max-time"
  while IFS= read -r leg_cap_value; do
    if (( leg_cap_value < 25 || leg_cap_value > 30 )); then
      fail "A1-01: each leg cap must be the remaining budget (~30s), got $leg_cap_value"
    fi
  done <<< "$leg_caps"
  if grep -q -- '--max-time 600' "$log"; then
    fail "A1-01: no leg may keep the 600s cap while a tighter budget deadline is in force"
  fi
  assert_equals "2" "$(grep -c -- '--retry-max-time' "$log")" "A1-01: both legs must bound total retry time by the remaining budget"

  # ③ 预算充足：routed 照常成功
  : > "$log"
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  FLMX_DOWNLOAD_BUDGET_SECONDS=240
  download_verified_routed "https://example.com/asset.yml" "$dest" >/dev/null || \
    fail "A1-01: routed download must succeed when the budget is ample"
  assert_equals "fixture-body" "$(cat "$dest")" "A1-01: the downloaded body must land verbatim"
)

# A1-02（audit-max）：run_verified_installer_script 的 curl 此前是
# 裸 -fsSL——无连接超时、无总界、无地板看门狗、无重试，
# get.docker.com 连通后停滞（TCP 开、零字节）时 install_docker
# 永不返回、包管理器兜底永不触发。与 L2-11 对 download_verified
# 的钉格同型：断言该函数的 curl 参数集带齐看门狗，且校验通过
# 后脚本被执行（夹具脚本仅 exit 0）。
test_panel_installer_script_curl_uses_timeouts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local log
  log=$(mktemp)
  CURL_ARGS_LOG="$log"

  curl() {
    echo "$*" >> "$CURL_ARGS_LOG"
    local output=""
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == "-o" ]]; then
        output="$2"
        shift 2
        continue
      fi
      shift
    done
    printf 'exit 0\n' > "$output"
    return 0
  }

  local expected
  expected=$(printf 'exit 0\n' | sha256sum | awk '{print $1}')
  run_verified_installer_script "https://get.docker.com" "$expected" >/dev/null || \
    fail "A1-02: the verified installer script should run with the stub"
  grep -q -- '--connect-timeout' "$log" || fail "A1-02: the installer-script curl must carry --connect-timeout"
  grep -q -- '--max-time' "$log" || fail "A1-02: the installer-script curl must carry --max-time"
  grep -q -- '--speed-limit' "$log" || fail "A1-02: the installer-script curl must carry the stall watchdog (--speed-limit/--speed-time)"
  grep -q -- '--retry' "$log" || fail "A1-02: the installer-script curl must carry --retry"
)

# A2-05（audit-max）：菜单 5 保留式写入不得整块删除共享站点——
# 旧实现 caddyfile_without_site 任一地址 token 命中目标域名即丢
# 整块，`panel.example.com, sub.example.com { ... }` 共享块里的
# sub 站点被静默删掉、回执却称已保留。现改为命中共享块时只剔
# 目标地址、其余地址与块体整块保留并点名。对照幕：仅含目标地
# 址的块仍整块剔除（既有口径）。
test_panel_caddyfile_without_site_keeps_shared_addresses() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir cf out err
  dir=$(mktemp -d)
  cf="$dir/Caddyfile"
  cat > "$cf" <<'EOF'
panel.example.com, sub.example.com {
    reverse_proxy 127.0.0.1:6366
}
other.example.com {
    respond "hi"
}
panel.example.com {
    reverse_proxy 127.0.0.1:6366
}
EOF
  out=$(mktemp)
  err=$(mktemp)
  caddyfile_without_site "$cf" "panel.example.com" >"$out" 2>"$err" || \
    fail "A2-05: caddyfile_without_site should succeed on the shared-block fixture"
  grep -q '^sub.example.com {' "$out" || fail "A2-05: the shared block must survive with the remaining address as its header"
  grep -q 'reverse_proxy 127.0.0.1:6366' "$out" || fail "A2-05: the shared block body must be preserved"
  if grep -q 'panel.example.com' "$out"; then
    fail "A2-05: the target address must be gone from the output (both its solo block and its share of the shared block)"
  fi
  grep -q '^other.example.com {' "$out" || fail "A2-05: unrelated blocks must be preserved verbatim"
  grep -q 'sub.example.com' "$err" || fail "A2-05: the shared-block rewrite must be named on stderr"
)

# A2-06（audit-max）：无 Docker 卸载的清理集与 Docker 分支对齐
# ——旧实现只删 docker-compose.yml/.env 两文件，backups/（含完整
# DB 备份与 .env 密钥副本）、.env 历史备份、PG 迁移快照、
# gost.db.bak 与更新临时文件全部残留，而 Docker 分支回执口径
# 是「删除所有数据」（L2-14 已在该分支闭合、本分支漏补）。两
# 幕：① 自家部署＋全量残留集 → 逐项点名且全部删除；② 仅剩残
# 留产物（无 compose/.env）时不再谎称「无需清理」——走归属确
# 认，确认后删除、拒绝则保留。
test_panel_uninstall_without_docker_cleans_full_residual_set() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  check_docker() { return 1; }

  make_residuals() {
    local d="$1"
    mkdir -p "$d/backups"
    printf 'db-backup\n' > "$d/backups/backup.tar"
    printf 'x\n' > "$d/.env.bak.1"
    printf 'x\n' > "$d/docker-compose.yml.bak.1"
    printf 'x\n' > "$d/.env.pre-postgres-migration"
    printf 'x\n' > "$d/gost.db.bak"
    printf 'x\n' > "$d/.docker-compose.yml.update.1"
    printf 'x\n' > "$d/.docker-compose.yml.install.1"
  }
  assert_residuals_gone() {
    local d="$1" item
    for item in backups .env.bak.1 docker-compose.yml.bak.1 .env.pre-postgres-migration gost.db.bak .docker-compose.yml.update.1 .docker-compose.yml.install.1; do
      [[ ! -e "$d/$item" ]] || fail "A2-06: residual $item must be deleted by the no-docker uninstall"
    done
  }

  # ① 自家部署＋全量残留（归属已证时与 Docker 分支同口径须显式
  # 确认——backups/ 含完整 DB 备份与 .env 密钥副本、删除不可恢
  # 复；旧实现无 Docker 分支零确认直删）
  local d out rc="0"
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  make_residuals "$d"
  out=$( ( cd "$d" && uninstall_panel <<< "y" 2>&1 ) ) || rc="$?"
  assert_equals "0" "$rc" "A2-06: own no-docker uninstall should succeed"
  [[ ! -e "$d/.env" && ! -e "$d/docker-compose.yml" ]] || fail "A2-06: compose/.env must be removed"
  assert_residuals_gone "$d"
  grep -q 'backups/' <<< "$out" || fail "A2-06: the residual listing must name backups/ before deletion"

  # ①b 自家部署但未确认（EOF/答 n）→ 取消、整集保留
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  make_residuals "$d"
  rc="0"
  out=$( ( cd "$d" && uninstall_panel </dev/null 2>&1 ) ) || rc="$?"
  assert_equals "0" "$rc" "A2-06: unconfirmed no-docker uninstall of a proven deployment should be a clean cancel"
  [[ -f "$d/.env" && -d "$d/backups" ]] || fail "A2-06: nothing may be deleted without confirmation"
  grep -q '不可恢复' <<< "$out" || fail "A2-06: the confirmation must warn that deletion is unrecoverable"

  # ② 仅残留产物：拒绝保留、确认删除
  d=$(mktemp -d)
  make_residuals "$d"
  rc="0"
  ( cd "$d" && uninstall_panel <<< "n" >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "A2-06: declining the confirmation should be a clean no-op"
  [[ -d "$d/backups" ]] || fail "A2-06: residuals must stay when confirmation is declined"
  rc="0"
  ( cd "$d" && uninstall_panel <<< "y" >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "A2-06: confirming should clean residuals-only directories"
  assert_residuals_gone "$d"
)

# R2-SCR-F04（FIX-UNINSTALL-RESIDUE）：面板内升级工件五件两卸
# 载分支同集回收——.env.upgrade.bak 是含 JWT_SECRET 与数据库口
# 令的完整 .env 副本（后端 helper 仅成功路径自删、失败/回滚终
# 态刻意保留＝卸载时最可能在场），此前两分支皆漏。Docker 分支
# 断言五件随卸载删除；无 Docker 分支断言五件进残留清单被点名
# 且删除（与 A2-06 同归属夹具）。
test_panel_uninstall_removes_inpanel_upgrade_artifacts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  make_upgrade_artifacts() {
    local d="$1"
    printf 'JWT_SECRET=x\nPOSTGRES_PASSWORD=y\n' > "$d/.env.upgrade.bak"
    printf 'services:\n' > "$d/docker-compose.yml.upgrade.bak"
    printf 'upgrade log\n' > "$d/upgrade.log"
    printf '{"status":"failed"}\n' > "$d/.flmx_upgrade_status.json"
    printf 'hb\n' > "$d/.flmx_upgrade_heartbeat"
  }
  assert_artifacts_gone() {
    local d="$1" item
    for item in .env.upgrade.bak docker-compose.yml.upgrade.bak upgrade.log .flmx_upgrade_status.json .flmx_upgrade_heartbeat; do
      [[ ! -e "$d/$item" ]] || fail "R2-SCR-F04: upgrade artifact $item must be deleted"
    done
  }

  # ① Docker 分支
  check_docker() { DOCKER_CMD="true"; }
  local d out rc="0"
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\nFRONTEND_PORT=6400\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  make_upgrade_artifacts "$d"
  FIXTURE_DIR="$d"
  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$FIXTURE_DIR"
    PANEL_BACKEND_CONTAINER="flmx-panel-backend"
    cd "$PANEL_DEPLOY_DIR" || return 1
  }
  systemctl() { return 0; }
  caddy() { return 0; }
  out=$( ( cd "$d" && PANEL_DEPLOY_DIR="$d" PANEL_BACKEND_CONTAINER="flmx-panel-backend" uninstall_panel <<< "y" 2>&1 ) ) || rc="$?"
  assert_equals "0" "$rc" "R2-SCR-F04: docker-branch uninstall should succeed"
  assert_artifacts_gone "$d"
  [[ ! -e "$d/.env" && ! -e "$d/docker-compose.yml" ]] || fail "R2-SCR-F04: compose/.env must be removed (docker branch)"

  # ② 无 Docker 分支（归属已证亦须显式确认，C4-中·无Docker卸载
  # 本轮改判：旧实现零确认直删；无输入即取消、工件保留）
  check_docker() { return 1; }
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  make_upgrade_artifacts "$d"
  rc="0"
  out=$( ( cd "$d" && uninstall_panel <<< "y" 2>&1 ) ) || rc="$?"
  assert_equals "0" "$rc" "R2-SCR-F04: no-docker uninstall should succeed"
  assert_artifacts_gone "$d"
  grep -q '.env.upgrade.bak' <<< "$out" || fail "R2-SCR-F04: the residual listing must name .env.upgrade.bak before deletion"
)

# R2-P2-05（FIX-UNINSTALL-RESIDUE）：卸载回收 Caddy 站点块——菜
# 单 5/6 此前只追加无移除。归属判据＝块体反代 127.0.0.1:<本部
# 署前端端口>：自家两块（面板＋订阅前置）整块摘除并逐个点名，
# 他人端口块、无关块、全局块逐字保留；摘除前留 .bak 备份、
# caddy validate 不过则原文件不动只告警；二跑无命中为干净
# no-op。CADDYFILE_PATH 改指临时文件、caddy/systemctl 全桩。
test_panel_uninstall_removes_own_caddy_blocks() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work cf calls
  work=$(mktemp -d)
  cf="$work/Caddyfile"
  calls="$work/systemctl-calls"
  : > "$calls"
  cat > "$cf" <<'CF'
{
	email ops@example.com
}

z.example.com {
	reverse_proxy 127.0.0.1:6400
}

sub.example.com {
	reverse_proxy 127.0.0.1:6400
}

other.example.com {
	reverse_proxy 127.0.0.1:7777
}

plain.example.com {
	root * /srv/www
	file_server
}
CF
  systemctl() { echo "$*" >> "$calls"; return 0; }
  caddy() { return 0; }

  local rc="0" out
  out=$(CADDYFILE_PATH="$cf" uninstall_caddy_panel_blocks 6400 2>&1) || rc="$?"
  assert_equals "0" "$rc" "R2-P2-05: caddy block removal should succeed"
  ! grep -q 'z.example.com' "$cf" || fail "R2-P2-05: own panel block must be removed"
  ! grep -q 'sub.example.com' "$cf" || fail "R2-P2-05: own subscription-front block must be removed"
  grep -q 'other.example.com' "$cf" || fail "R2-P2-05: another deployment's block must be kept"
  grep -q 'plain.example.com' "$cf" || fail "R2-P2-05: unrelated blocks must be kept"
  grep -q 'ops@example.com' "$cf" || fail "R2-P2-05: the global block must be kept"
  grep -q 'z.example.com' <<< "$out" || fail "R2-P2-05: removed blocks must be named in output"
  compgen -G "$cf.bak.*" >/dev/null || fail "R2-P2-05: a timestamped backup of the original Caddyfile must be kept"
  grep -q 'restart caddy' "$calls" || fail "R2-P2-05: caddy must be restarted after block removal"

  # 二跑：无命中 → 文件不动、点名未发现、仍成功。
  local before
  before=$(cat "$cf")
  rc="0"
  out=$(CADDYFILE_PATH="$cf" uninstall_caddy_panel_blocks 6400 2>&1) || rc="$?"
  assert_equals "0" "$rc" "R2-P2-05: second pass should succeed"
  assert_equals "$before" "$(cat "$cf")" "R2-P2-05: second pass must not modify the Caddyfile"
  grep -q '未发现' <<< "$out" || fail "R2-P2-05: second pass must report no matching blocks"

  # validate 失败：原文件逐字不动、只告警、卸载不失败。
  cat > "$cf" <<'CF'
z.example.com {
	reverse_proxy 127.0.0.1:6400
}
CF
  caddy() { return 1; }
  rc="0"
  out=$(CADDYFILE_PATH="$cf" uninstall_caddy_panel_blocks 6400 2>&1) || rc="$?"
  assert_equals "0" "$rc" "R2-P2-05: validate failure must not fail the uninstall"
  grep -q 'z.example.com' "$cf" || fail "R2-P2-05: validate failure must leave the Caddyfile untouched"
  grep -q '校验失败' <<< "$out" || fail "R2-P2-05: validate failure must be named in output"
)

# A1-03（audit-max）：显式版本 env 在拼 URL/镜像 tag 前须过格式
# 校验——旧实现 resolve_version 与 update_panel 的版本链对
# VERSION/FLMX_VERSION/PINNED_VERSION 零校验原样
# 透传，路径穿越/空白/命令片段可进入全部拼接面。文法取并集
# （数字开头、仅含数字/字母/点/连字符）：稳定版与显式预发布
# （J-02 既定支持面）放行，穿越/注入形态全拒；update 链为第二
# 入口、同律把守（全打桩下坏版本必须在拉镜像前终止）。
test_panel_explicit_version_format_validated() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local out rc bad
  for bad in '../../etc/passwd' '1.9.9;id' '1.9.9 rc1' ' 1.9.9' '-1.0' '1.9.9$(id)' '.9.9' '1.9.9/x' "1.9.9'x"; do
    VERSION="$bad" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
    rc="0"
    out=$(resolve_version 2>&1) || rc="$?"
    [[ "$rc" != "0" ]] || fail "A1-03: resolve_version must reject the malformed explicit version: $bad"
    grep -q '格式非法' <<< "$out" || fail "A1-03: the rejection must name the format problem for: $bad"
  done

  VERSION="" FLMX_VERSION='1.9.9-rc.1' FLUX_VERSION="" PINNED_VERSION=""
  out=$(resolve_version) || fail "A1-03: an explicit prerelease version must stay installable (J-02)"
  assert_equals "1.9.9-rc.1" "$out" "A1-03: the explicit prerelease version must pass through verbatim"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION='2.0.0-beta.3'
  out=$(resolve_version) || fail "A1-03: an explicit beta PINNED_VERSION must stay installable"
  assert_equals "2.0.0-beta.3" "$out" "A1-03: the explicit beta version must pass through verbatim"
  VERSION='1.9.9' FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  out=$(resolve_version) || fail "A1-03: a plain stable version must pass"
  assert_equals "1.9.9" "$out" "A1-03: the stable version must pass through verbatim"
  # v 前缀与开发哨兵值放行（panel_version_cmp 剥 v 比较；套件
  # 定版夹具以 v-test 为既定输入、多个既有更新/装机用例依赖其
  # 流过版本链）：逐字透传，不在此改写。本校验守注入面（穿
  # 越/空白/元字符），不守版本语义。
  VERSION='v1.9.9' FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  out=$(resolve_version) || fail "A1-03: a v-prefixed version must pass (the prefix is cosmetic to the version grammar)"
  assert_equals "v1.9.9" "$out" "A1-03: the v-prefixed version must pass through verbatim"
  VERSION='v-test' FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  out=$(resolve_version) || fail "A1-03: the suite's pinned-version sentinel v-test must pass (standing fixture contract)"
  assert_equals "v-test" "$out" "A1-03: the sentinel version must pass through verbatim"

  # update 链同守（与 L2-12 同款全打桩）
  check_docker() { DOCKER_CMD="true"; }
  resolve_panel_deployment() { PANEL_DEPLOY_DIR=$(mktemp -d); }
  validate_panel_update_environment() { :; }
  get_current_db_type() { echo "sqlite"; }
  get_deployed_panel_version() { return 1; }
  resolve_latest_release_tag() { echo "9.9.9"; }
  download_panel_update_compose() { UPDATE_COMPOSE_CANDIDATE="candidate"; }
  validate_panel_update_compose() { return 0; }
  create_panel_update_backup() { UPDATE_BACKUP_DIR="backup"; return 0; }
  pull_panel_update_images() { echo pulled >> "$PULL_MARKER"; return 0; }
  panel_pin_missing_resource_keys() { return 0; }
  activate_panel_update_files() { return 0; }
  start_panel_after_update() { return 0; }
  verify_panel_update_sqlite_volume() { return 0; }
  check_ipv6_support() { return 1; }

  PULL_MARKER=$(mktemp)
  local log
  log=$(mktemp)
  VERSION='1.9.9;id' FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  rc="0"
  update_panel >"$log" 2>&1 || rc="$?"
  [[ "$rc" != "0" ]] || fail "A1-03: update_panel must reject a malformed explicit version"
  grep -q '格式非法' "$log" || fail "A1-03: the update rejection must name the format problem"
  [[ ! -s "$PULL_MARKER" ]] || fail "A1-03: no image pull may happen with a malformed version"
)

# A1-04（audit-max）：路线探测的 tag 文法与版本解析器对齐——旧
# 实现 probe_route 用宽文法（任意数字开头 tag），而
# resolve_latest_release_tag 是渠道文法（稳定渠道只认纯数字点
# 分）：稳定渠道下探测会把「重定向落到预发布 tag」的路线判活，
# 与解析器随后拒绝该 tag 的口径漂移。两处现共用
# release_tag_pattern 一个出处：稳定渠道探测拒绝预发布 tag、
# beta 渠道两处同放行、垃圾 tag 两渠道同拒。
test_panel_probe_route_uses_channel_tag_grammar() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  curl() {
    printf '0.123 https://github.com/some-owner/some-repo/releases/tag/%s' "$FAKE_TAG"
    return 0
  }

  local rc
  # 稳定渠道（缺省）：纯数字 tag 判活、预发布 tag 判死
  unset FLMX_RELEASE_CHANNEL
  FAKE_TAG='1.9.10'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  assert_equals "0" "$rc" "A1-04: a stable tag must pass the probe on the stable channel"
  FAKE_TAG='1.9.10-beta.1'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  [[ "$rc" != "0" ]] || fail "A1-04: on the stable channel the probe must reject a prerelease tag, matching the parser grammar"

  # beta 渠道：预发布与稳定 tag 同放行
  FLMX_RELEASE_CHANNEL='beta'
  FAKE_TAG='1.9.10-beta.1'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  assert_equals "0" "$rc" "A1-04: a prerelease tag must pass the probe on the beta channel"
  FAKE_TAG='1.9.10'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  assert_equals "0" "$rc" "A1-04: a stable tag must pass the probe on the beta channel"

  # 垃圾 tag 两渠道同拒（H-24 的 /releases 索引页形态）
  FAKE_TAG='releases'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  [[ "$rc" != "0" ]] || fail "A1-04: a junk tag must fail the probe on the beta channel"
  unset FLMX_RELEASE_CHANNEL
  rc="0"; probe_route "" >/dev/null || rc="$?"
  [[ "$rc" != "0" ]] || fail "A1-04: a junk tag must fail the probe on the stable channel"
)

# A1-05（audit-max）：normalize_proxy_prefix 须去掉全部尾斜杠
# ——旧实现 ${proxy%/} 只去一个，https://host// 归一后仍带尾斜
# 杠，后续 route_url 拼接成双斜杠路径。含 scheme 与裸主机两形
# 态、路径段多斜杠、无尾斜杠不变四类断言。
test_panel_normalize_proxy_prefix_strips_all_trailing_slashes() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  assert_equals "https://proxy.example.com" "$(normalize_proxy_prefix 'https://proxy.example.com//')" "A1-05: all trailing slashes must be stripped (scheme form)"
  assert_equals "https://proxy.example.com" "$(normalize_proxy_prefix 'proxy.example.com///')" "A1-05: all trailing slashes must be stripped (bare host form)"
  assert_equals "http://proxy.example.com" "$(normalize_proxy_prefix 'http://proxy.example.com/')" "A1-05: a single trailing slash must still be stripped"
  assert_equals "https://proxy.example.com/path" "$(normalize_proxy_prefix 'https://proxy.example.com/path//')" "A1-05: trailing slashes after a path segment must be stripped"
  assert_equals "https://proxy.example.com" "$(normalize_proxy_prefix 'https://proxy.example.com')" "A1-05: a prefix without trailing slash must pass through unchanged"
)

# A1-06（audit-max）：configure_docker_ipv6 的 jq 分支不得再写
# 可预测的 /tmp/daemon.json——须以 mktemp 在目标同目录
# /etc/docker 落不可预测临时名（兼原子 rename）。静态钉格（与
# test_install_docker_daemon_start_wiring_static 同型）：该函数
# 的目标路径写死 /etc/docker，行为级驱动会真写运行宿主机的
# Docker 配置，套件内不可行，故以形态断言把守。
test_panel_docker_ipv6_uses_mktemp_not_predictable_tmp() (
  set -euo pipefail
  local body
  body=$(sed -n '/^configure_docker_ipv6()/,/^}/p' "$ROOT_DIR/panel_install.sh")
  if grep -Eq '(>|[[:space:]]mv[[:space:]]+)["]?/tmp/daemon\.json' <<< "$body"; then
    fail "A1-06: configure_docker_ipv6 must not write or rename over the predictable /tmp/daemon.json path"
  fi
  grep -q 'mktemp /etc/docker/.daemon.json.XXXXXX' <<< "$body" || fail "A1-06: the daemon.json rewrite must stage through mktemp in /etc/docker"
  grep -q 'jq -e' <<< "$body" || fail "A1-06: the staged file must be re-parsed as valid JSON before it replaces daemon.json"
)

# A2-07（audit-max）：入口进程锁＋备份名唯一后缀——① 锁被另一
# 进程持有时 panel_acquire_run_lock 必须点名拒绝（返回非 0）；
# 锁空闲时取得成功；main() 必须接线调用（静态断言）。② 菜单
# 5/6 与卸载回收（R2-P2-05）的 Caddyfile 备份名此前只有秒级时
# 间戳，同秒再跑即覆盖前一份回滚源：三处命名都必须带进程号唯
# 一后缀（静态钉格，与 A1-06 同因：菜单全流程夹具不产出同秒
# 双跑场景）。
test_panel_run_lock_and_unique_caddy_backup_names() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local lock_file holder_rc lock_out
  lock_file=$(mktemp -u)
  lock_out=$(mktemp)
  PANEL_INSTALL_LOCK_FILE="$lock_file"
  ( exec 9>>"$lock_file"; flock 9; sleep 5 ) &
  local holder_pid=$!
  sleep 0.5
  holder_rc="0"
  panel_acquire_run_lock > "$lock_out" 2>&1 || holder_rc="$?"
  [[ "$holder_rc" != "0" ]] || fail "A2-07: acquiring the run lock must fail while another process holds it"
  grep -q 'panel_install.sh 进程正在运行' "$lock_out" || fail "A2-07: the lock refusal must be named in the output"
  kill "$holder_pid" 2>/dev/null || true
  wait "$holder_pid" 2>/dev/null || true
  panel_acquire_run_lock >/dev/null 2>&1 || fail "A2-07: acquiring the run lock must succeed once it is free"

  local main_body
  main_body=$(sed -n '/^main()/,/^}/p' "$ROOT_DIR/panel_install.sh")
  grep -q 'panel_acquire_run_lock' <<< "$main_body" || fail "A2-07: main() must acquire the run lock before any operation"

  local bak_count
  bak_count=$(grep -Fc 'caddyfile}.bak.$(date +%s).$$' "$ROOT_DIR/panel_install.sh" || true)
  assert_equals "3" "$bak_count" "A2-07: all Caddy backup names (menus 5/6 and uninstall) must carry the PID suffix"
)

# A2-08（audit-max）：非法 FLMX_INSTANCE 在管理路径须与装机路径
# 同律硬拒——旧实现只有 install_panel 对环境变量形态硬拒；读路
# 径 panel_instance_id 对非法值（env/.env 两形态）静默回落默认
# 实例（B2-03 口径，对纯读成立），于是 update/uninstall/migrate
# 带着 .env 里的非法实例 id 一路操作到默认实例的资源上（或派
# 生名错位）。现各变更性入口先过 panel_assert_instance_id_valid
# （读路径回退口径本身不改，B2-03 用例继续把守）。四幕：
# helper 单元、update 链、uninstall 无 Docker 分支、install 的
# .env 形态（旧实现只查环境变量、.env 非法值不拒）。
test_panel_invalid_instance_id_hard_fails_on_manage_paths() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local d rc out
  # helper 单元：env 非法拒、.env 非法拒、合法/缺省放行
  d=$(mktemp -d)
  rc="0"; out=$( ( FLMX_INSTANCE='Bad Id!' panel_assert_instance_id_valid "$d" ) 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "A2-08: an invalid FLMX_INSTANCE from the environment must hard-fail"
  grep -q 'FLMX_INSTANCE 不合法' <<< "$out" || fail "A2-08: the hard-fail must name the invalid instance id"
  printf 'FLMX_INSTANCE=Bad Id!\n' > "$d/.env"
  rc="0"; ( unset FLMX_INSTANCE; panel_assert_instance_id_valid "$d" >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "A2-08: an invalid FLMX_INSTANCE from .env must hard-fail"
  printf 'FLMX_INSTANCE=east\n' > "$d/.env"
  ( unset FLMX_INSTANCE; panel_assert_instance_id_valid "$d" >/dev/null 2>&1 ) || fail "A2-08: a valid instance id must pass"
  rm -f "$d/.env"
  ( unset FLMX_INSTANCE; panel_assert_instance_id_valid "$d" >/dev/null 2>&1 ) || fail "A2-08: an absent instance id must pass (default instance)"

  # update 链：resolve 到的部署 .env 含非法实例 id → 拉镜像前终止
  check_docker() { DOCKER_CMD="true"; }
  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$INSTANCE_FIXTURE_DIR"
    PANEL_BACKEND_CONTAINER="flmx-panel-backend"
  }
  validate_panel_update_environment() { :; }
  get_current_db_type() { echo "sqlite"; }
  get_deployed_panel_version() { return 1; }
  resolve_latest_release_tag() { echo "9.9.9"; }
  download_panel_update_compose() { UPDATE_COMPOSE_CANDIDATE="candidate"; }
  validate_panel_update_compose() { return 0; }
  create_panel_update_backup() { UPDATE_BACKUP_DIR="backup"; return 0; }
  pull_panel_update_images() { echo pulled >> "$PULL_MARKER"; return 0; }
  panel_pin_missing_resource_keys() { return 0; }
  activate_panel_update_files() { return 0; }
  start_panel_after_update() { return 0; }
  verify_panel_update_sqlite_volume() { return 0; }
  check_ipv6_support() { return 1; }

  INSTANCE_FIXTURE_DIR=$(mktemp -d)
  printf 'FLMX_INSTANCE=Bad Id!\nDB_TYPE=sqlite\n' > "$INSTANCE_FIXTURE_DIR/.env"
  PULL_MARKER=$(mktemp)
  local log
  log=$(mktemp)
  rc="0"
  ( unset FLMX_INSTANCE; VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION="" update_panel >"$log" 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "A2-08: update_panel must hard-fail on an invalid instance id in the deployment .env"
  grep -q 'FLMX_INSTANCE 不合法' "$log" || fail "A2-08: the update hard-fail must name the invalid instance id"
  [[ ! -s "$PULL_MARKER" ]] || fail "A2-08: no image pull may happen with an invalid instance id"

  # uninstall 无 Docker 分支：cwd .env 非法实例 id → 不删任何文件
  check_docker() { return 1; }
  d=$(mktemp -d)
  printf 'FLMX_INSTANCE=Bad Id!\nJWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && unset FLMX_INSTANCE && uninstall_panel </dev/null >/dev/null 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "A2-08: the no-docker uninstall must hard-fail on an invalid instance id"
  [[ -e "$d/.env" && -e "$d/docker-compose.yml" ]] || fail "A2-08: no file may be deleted with an invalid instance id"

  # install 的 .env 形态：环境变量未设、cwd .env 非法 → 拒绝
  d=$(mktemp -d)
  printf 'FLMX_INSTANCE=Bad Id!\n' > "$d/.env"
  rc="0"
  out=$( ( cd "$d" && unset FLMX_INSTANCE && install_panel 2>&1 ) ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "A2-08: install_panel must hard-fail on an invalid instance id from .env"
  grep -q 'FLMX_INSTANCE 不合法' <<< "$out" || fail "A2-08: the install hard-fail must name the invalid instance id"
)

# §9.6 缓解（audit-max 裁定）：WS token 面的非协议缓解——菜单 5
# 生成的面板站点块须带访问日志过滤，把 ?secret= 查询参数从
# Caddy 访问日志里删掉（WS 握手把 token 放在 secret 查询串，
# 落日志即长效凭据落盘）。过滤器写在站点块内（菜单 5 保留式
# 重写时随块再生、不累积）；菜单 6 的订阅前置块不在此列（订
# 阅 token 走路径段而非 secret 查询串，口径不同，单独断言其
# 不被误加）。
test_panel_menu5_site_block_filters_secret_query_from_access_log() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  panel_caddyfile_fixture_env

  setup_domain_proxy <<< "panel.example.com" >/dev/null || \
    fail "§9.6: the menu 5 run should succeed"
  grep -q 'format filter' "$CADDYFILE_PATH" || fail "§9.6: the generated panel site block must carry the log filter encoder"
  grep -q 'request>uri query' "$CADDYFILE_PATH" || fail "§9.6: the filter must target the request URI query string"
  grep -q 'delete secret' "$CADDYFILE_PATH" || fail "§9.6: the filter must delete the secret query parameter from the access log"

  # 菜单 6 随后追加订阅前置块：不得被误加（其 token 在路径段），
  # 全文件 delete secret 仍只出现一次（面板块内）。
  setup_subscription_front_domain <<< "sub.example.com" >/dev/null || \
    fail "§9.6: the menu 6 run should succeed"
  assert_equals "1" "$(grep -c 'delete secret' "$CADDYFILE_PATH")" "§9.6: only the panel block may carry the secret filter; the menu 6 block must not gain one"
)

# A2-04 附带钉格（XC-1 指出）：A2-04 本体（.env 读取与 Compose
# 语义一致）已在他路修好，但 CRLF 形态此前无套件断言——Windows
# 编辑过的 .env 若把 \r 带进取值，实例 id/DB 类型等比较会整体
# 错位。钉死：CRLF 文件里普通值、引号值、空值的读数都不得带
# \r，引号语义与空值语义与 LF 形态一致。
test_env_var_reading_crlf_shape() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local d
  d=$(mktemp -d)
  printf 'FLMX_INSTANCE=east\r\nDB_TYPE=postgres\r\nQUOTED="va l"\r\nEMPTY=\r\n' > "$d/.env"
  assert_equals "east" "$(get_env_var FLMX_INSTANCE "$d/.env")" "A2-04/CRLF: a CRLF value must be read without the carriage return"
  assert_equals "postgres" "$(get_env_var DB_TYPE "$d/.env")" "A2-04/CRLF: the DB type must be read without the carriage return"
  assert_equals "va l" "$(get_env_var QUOTED "$d/.env")" "A2-04/CRLF: a quoted CRLF value must be unquoted and CR-free"
  assert_equals "" "$(get_env_var EMPTY "$d/.env")" "A2-04/CRLF: an empty CRLF value must read as empty"
)

# C4-01 跟进（FIX-B7 已落码）：clientip 信任面改为「回环默认可
# 信＋FLMX_TRUSTED_PROXIES 显式 CIDR 名单」后，新装生成的 .env
# 必须带该条目（默认空）与字段级指引注释——标准拓扑下后端经前
# 端容器转发，模板不给这个旋钮，运维无从知道要把前端容器子网
# 填进去，限流键会一直坍缩到前端容器 IP 的共享桶。与 FX-D 装
# 机用例同款全打桩驱动 install_panel，断言真实落盘的 .env。
test_panel_install_env_carries_trusted_proxies_entry() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

  local work volmount
  work=$(mktemp -d)
  volmount=$(mktemp -d)

  ask_proxy_config() { :; }
  ensure_compose_urls_initialized() {
    DOCKER_COMPOSEV4_URL="https://example.com/docker-compose-v4.yml"
    DOCKER_COMPOSEV6_URL="https://example.com/docker-compose-v6.yml"
    return 0
  }
  check_docker() { DOCKER_CMD="docker compose"; return 0; }
  get_config_params() {
    FRONTEND_PORT=6366; BACKEND_PORT=6365; RESOLVED_VERSION="9.9.9"
    DB_TYPE="sqlite"; DATABASE_URL=""; POSTGRES_DB="flux_panel"; POSTGRES_USER="flux_panel"
    POSTGRES_PASSWORD="test-password"
    JWT_SECRET="test-secret"
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
      *" volume inspect "*) printf '%s' "$FXD_TEST_VOLMOUNT" ;;
      *" inspect "*) printf 'flmx-sqlite-data' ;;
    esac
    return 0
  }

  export FXD_TEST_VOLMOUNT="$volmount"
  local rc="0"
  ( cd "$work" && install_panel >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "C4-01 follow-up: install_panel should succeed in the fixture"
  [[ -f "$work/.env" ]] || fail "C4-01 follow-up: install must generate .env"
  grep -q '^FLMX_TRUSTED_PROXIES=$' "$work/.env" || fail "C4-01 follow-up: the generated .env must carry FLMX_TRUSTED_PROXIES, empty by default (loopback-only trust)"
  grep -q '转发者所在子网' "$work/.env" || fail "C4-01 follow-up: the guidance must name what to fill (the frontend container subnet CIDR)"
  grep -q '172.30.0.0/16' "$work/.env" || fail "C4-01 follow-up: the guidance must give the shipped template subnet as the concrete example"
  grep -q '共享' "$work/.env" || fail "C4-01 follow-up: the guidance must state the shared-bucket consequence of leaving it empty"
  grep -q '0.0.0.0/0' "$work/.env" || fail "C4-01 follow-up: the guidance must warn against an over-broad trust range"
  grep -q '自检' "$work/.env" || fail "C4-01 follow-up: the guidance must include a self-check method"
)

test_shipped_compose_templates_carry_trusted_proxies_passthrough() (
  set -euo pipefail

  # C4-01/PV-11: 随版 compose 模板的 backend environment 段必须带
  # FLMX_TRUSTED_PROXIES 传递行——.env 写了该键而模板不透传时，后端
  # 容器读不到它，clientip 的显式 CIDR 可信名单在标准部署下形同虚设。
  local tpl
  for tpl in docker-compose-v4.yml docker-compose-v6.yml; do
    grep -qF 'FLMX_TRUSTED_PROXIES: ${FLMX_TRUSTED_PROXIES:-}' "$ROOT_DIR/$tpl" \
      || fail "C4-01: $tpl backend environment must carry the FLMX_TRUSTED_PROXIES passthrough line"
    assert_equals "1" "$(grep -cF 'FLMX_TRUSTED_PROXIES: ${FLMX_TRUSTED_PROXIES:-}' "$ROOT_DIR/$tpl")" \
      "C4-01: $tpl must carry the passthrough line exactly once"
  done
)

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
  # A3-02 后显式版本须过格式校验：夹具预设的 v-test 不再合法，
  # 本例改用合法版本（只验 URL 规范化本身，不验版本解析）。
  VERSION="1.2.3"

  ensure_download_url_initialized

  local expected
  expected=$(build_download_url)

  assert_equals \
    "$expected" \
    "$DOWNLOAD_URL" \
    "install.sh should keep the canonical download URL (route prefixes apply at download time)"
)

test_update_flmx_agent_downloads_without_prompt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # A3-02 后显式版本须过格式校验：夹具预设的 v-test 不再合法，
  # 本例走真实 ensure 路径，须先给合法版本（只测更新机制本身，
  # 不测版本解析）。
  VERSION="1.3.5"

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"

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

  update_flmx_agent >/dev/null

  assert_equals "1" "$cleanup_called" "update_flmx_agent should clean up legacy gost before restarting the agent"
  assert_equals "$(build_download_url)" "$DOWNLOAD_URL" "update_flmx_agent should build the canonical download URL"
)

test_update_flmx_agent_skips_proxy_prompt_when_not_installed() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  INSTALL_DIR=$(mktemp -u)

  local rc="0"
  update_flmx_agent >/dev/null || rc="$?"

  assert_equals "1" "$rc" "update_flmx_agent should fail when the agent is not installed"
)

# R8-J-B: 更新成功后必须打印与安装路径同口径的上线确认指引
# （回面板确认节点在线 + journalctl 日志指针）——服务重启只证明
# 进程已启动，不代表已连上面板。旧实现仅一行「更新完成」断言，
# 本测试对旧代码为红。
test_update_flmx_agent_prints_online_confirmation_guidance() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # A3-02 后显式版本须过格式校验：夹具预设的 v-test 不再合法，
  # 本例走真实 ensure 路径，须先给合法版本（只测更新机制本身，
  # 不测版本解析；SCRIPT-RED-A）。
  VERSION="v1.9.9"

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"

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
  update_flmx_agent >"$out_log" 2>&1

  grep -q "更新完成" "$out_log" || fail "R8-J-B: update should still print its completion line"
  grep -q "节点管理" "$out_log" && grep -q "在线" "$out_log" || \
    fail "R8-J-B: update completion must tell the operator to confirm the node is online in the panel"
  grep -q "journalctl -u flmx_agent" "$out_log" || \
    fail "R8-J-B: update completion must include the journalctl log pointer"
)

# X-P2-11 (J-01): 双采样判活的四个关键场景（与 agent 侧
# fix3_health_test 同口径）：PID 稳定才活、第二次采样已死不活、
# 两次 PID 不一致不活、PID 为 0/空不活。采样间隔 sleep 桩为空转，
# 结果由桩的调用计数决定。
test_flmx_agent_health_ok_requires_stable_pid() (
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
  flmx_agent_main_pid() {
    local n
    n=$(cat "$STATE_DIR/pid_idx"); n=$((n+1)); echo "$n" > "$STATE_DIR/pid_idx"
    sed -n "${n}p" "$STATE_DIR/pids"
  }
  flmx_agent_service_is_active() {
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
  flmx_agent_health_ok || fail "X-P2-11: stable PID + always active should be healthy"

  # 第二次采样时已死（flap 形态）
  reset_health_state; echo 1 > "$MAXOK_FILE"
  if flmx_agent_health_ok; then
    fail "X-P2-11: service dead at second sample must not be healthy"
  fi

  # 两次 PID 不一致（期间已重启）
  reset_health_state; echo 99 > "$MAXOK_FILE"
  printf '1111\n2222\n' > "$STATE_DIR/pids"
  if flmx_agent_health_ok; then
    fail "X-P2-11: PID changing between samples must not be healthy"
  fi

  # PID 为 0 / 空
  printf '0\n0\n' > "$STATE_DIR/pids"
  reset_health_state
  if flmx_agent_health_ok; then
    fail "X-P2-11: PID 0 must not be healthy"
  fi
  : > "$STATE_DIR/pids"
  reset_health_state
  if flmx_agent_health_ok; then
    fail "X-P2-11: empty PID must not be healthy"
  fi
)

# X-P2-11 (J-01): flap 二进制（-V 通过、启动后短时存活即倒下，
# 此处以第二次 is-active 采样转失败模拟）走更新路径：必须判死、
# 回滚到旧二进制且回滚后服务 active；.bak 已被回滚消费。
# 正常二进制更新成功为防过修对照（见
# test_update_flmx_agent_asks_for_proxy_config 的成功路径）。
test_update_flmx_agent_rolls_back_flapping_binary() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
  : > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi
echo "old good"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"

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
        if grep -q FLAP_MARKER "$INSTALL_DIR/flmx_agent"; then
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
        echo "flmx_agent.service loaded active running"; return 0 ;;
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
  update_flmx_agent >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P2-11: flapping binary update should fail and roll back"
  grep -q "old good" "$INSTALL_DIR/flmx_agent" || \
    fail "X-P2-11: rollback should restore the old binary"
  [[ ! -f "$INSTALL_DIR/flmx_agent.bak" ]] || \
    fail "X-P2-11: rollback should consume the .bak backup"
  flmx_agent_service_is_active || \
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
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
  : > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi
echo "old good"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"
  if [[ "$mode" == "stale" ]]; then
    cat > "$INSTALL_DIR/flmx_agent.bak" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 0.9.0 (go1.25 linux/amd64)"; exit 0; fi
echo "stale good"
EOF
    chmod +x "$INSTALL_DIR/flmx_agent.bak"
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
        if grep -q FLAP_MARKER "$INSTALL_DIR/flmx_agent"; then
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
        echo "flmx_agent.service loaded active running"; return 0 ;;
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
  update_flmx_agent > "$FIXTURE_DIR/out.log" 2>&1 || rc="$?"
  echo "$rc" > "$FIXTURE_DIR/rc"
  echo "$FIXTURE_DIR"
}

test_update_flmx_agent_aborts_when_backup_fails() (
  set -euo pipefail
  local dir rc
  dir=$(agent_update_backup_fail_fixture nostale)
  rc=$(cat "$dir/rc")
  assert_equals "1" "$rc" "R9-02: update must abort when the backup copy fails"
  grep -q "old good" "$dir/agent/flmx_agent" || \
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
  [[ ! -f "$dir/agent/flmx_agent.bak" ]] || \
    fail "R9-02: no .bak may remain after an aborted backup"
)

test_update_flmx_agent_aborts_when_backup_fails_with_stale_bak() (
  set -euo pipefail
  local dir rc
  dir=$(agent_update_backup_fail_fixture stale)
  rc=$(cat "$dir/rc")
  assert_equals "1" "$rc" "R9-02: update must abort when the backup copy fails even with a stale .bak present"
  grep -q "old good" "$dir/agent/flmx_agent" || \
    fail "R9-02: current binary must remain the pre-update version; the stale .bak must not be rolled back across generations"
  if grep -q "stale good" "$dir/agent/flmx_agent"; then
    fail "R9-02: stale .bak content must never become the running binary after a failed backup"
  fi
  grep -q "备份当前版本失败" "$dir/out.log" || \
    fail "R9-02: receipt must name the backup failure"
  if grep -q "已回滚到旧版本并重启成功" "$dir/out.log"; then
    fail "R9-02: receipt must not report a cross-generation rollback as success"
  fi
  [[ ! -f "$dir/agent/flmx_agent.bak" ]] || \
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
test_update_flmx_agent_rejects_downgrade() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi
echo "old good"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"

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
  update_flmx_agent >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P2-12: downgrade update should be rejected"
  [[ ! -f "$STATE_DIR/curl-called" ]] || \
    fail "X-P2-12: downgrade must be rejected before any download"
  grep -q "1.3.4" "$INSTALL_DIR/flmx_agent" || \
    fail "X-P2-12: installed binary must be unchanged after a rejected downgrade"
)

# X-P2-12 防过修：目标更高时正常更新；现装版本不可比时按
# panel_version_cmp 既有口径放行（V-19）。
test_update_flmx_agent_allows_higher_and_incomparable() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

  SERVICE_MANAGER="systemd"

  run_update_case() {
    local installed_body="$1" target="$2"
    INSTALL_DIR=$(mktemp -d)
    FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
    : > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
    printf '%s\n' "$installed_body" > "$INSTALL_DIR/flmx_agent"
    chmod +x "$INSTALL_DIR/flmx_agent"
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
    update_flmx_agent >/dev/null 2>&1 || rc="$?"
    assert_equals "0" "$rc" "X-P2-12: update to $target should succeed"
    grep -q "$target" "$INSTALL_DIR/flmx_agent" || \
      fail "X-P2-12: binary should be at $target after update"
    [[ ! -f "$INSTALL_DIR/flmx_agent.bak" ]] || \
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
  file=$(build_case menu-update-fail 'update_flmx_agent() { return 1; }')
  rc="0"; printf '2\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: menu update failure should exit non-zero"

  # 菜单 2 更新成功 → 0
  file=$(build_case menu-update-ok 'update_flmx_agent() { return 0; }')
  rc="0"; printf '2\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "X-P3-6: menu update success should exit 0"

  # 菜单 1 安装失败 → 非 0
  file=$(build_case menu-install-fail 'install_flmx_agent() { return 1; }')
  rc="0"; printf '1\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: menu install failure should exit non-zero"

  # 菜单 3 卸载失败 → 非 0
  file=$(build_case menu-uninstall-fail 'uninstall_flmx_agent() { return 1; }')
  rc="0"; printf '3\n' | bash "$file" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: menu uninstall failure should exit non-zero"

  # 直装（-a/-s）失败 → 非 0
  file=$(build_case direct-fail 'install_flmx_agent() { return 1; }')
  rc="0"; bash "$file" -a "panel.example.com:443" -s "test-secret" >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "X-P3-6: direct install failure should exit non-zero"

  # 直装（-a/-s）成功 → 0
  file=$(build_case direct-ok 'install_flmx_agent() { return 0; }')
  rc="0"; bash "$file" -a "panel.example.com:443" -s "test-secret" >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "X-P3-6: direct install success should exit 0"
)

test_install_flmx_agent_preserves_legacy_gost_when_download_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"
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
  # 标记文件、断言读文件。install_flmx_agent 的失败路径用 exit
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

  ( install_flmx_agent >/dev/null ) || rc="$?"

  assert_equals "1" "$rc" "install_flmx_agent should fail when the download artifact is missing"
  [[ -f "$INSTALL_DIR/.curl_calls" ]] || fail "install_flmx_agent should reach the download step with its own config (guard pass), not exit at the ownership guard"
  [[ ! -e "$INSTALL_DIR/.cleanup_called" ]] || fail "install_flmx_agent should preserve legacy gost when download fails"
  [[ -f "$INSTALL_DIR/flmx_agent" ]] || fail "install_flmx_agent should keep the existing flmx_agent binary when download fails"
)

test_update_flmx_agent_preserves_legacy_gost_when_download_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # A3-02 后显式版本须过格式校验：夹具预设的 v-test 不再合法，
  # 本例走真实 ensure 路径，须先给合法版本（只测更新机制本身，
  # 不测版本解析；SCRIPT-RED-A）。
  VERSION="v1.9.9"

  # AI-06：菜单更新已加归属守卫；本夹具的操作者以 TAKEOVER 显式
  # 确认归属，专测更新机制本身。
  FLMX_AGENT_TAKEOVER="1"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"
  cat > "$INSTALL_DIR/flmx_agent.new" <<'EOF'
#!/bin/bash
echo "stale version"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent.new"

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

  update_flmx_agent >/dev/null || rc="$?"

  assert_equals "1" "$rc" "update_flmx_agent should fail when the download artifact is missing"
  assert_equals "0" "$cleanup_called" "update_flmx_agent should preserve legacy gost when download fails"
  [[ ! -f "$INSTALL_DIR/flmx_agent.new" ]] || fail "update_flmx_agent should remove stale download artifacts before retrying"
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
  set_binary() { printf 'binary-bytes' > "$INSTALL_DIR/flmx_agent"; }

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
  before=$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flmx_agent")
  run_guard
  assert_equals "1" "$rc" "AI-07: guard should refuse a foreign addr"
  assert_equals "$before" "$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flmx_agent")" \
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
test_update_flmx_agent_refuses_unowned_agent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  SERVER_ADDR=""

  local state_dir
  state_dir=$(mktemp -d)

  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"

  curl() { echo called >> "$state_dir/curl-called"; return 0; }
  check_and_install_tcpkill() { :; }
  systemctl() { return 0; }

  local rc="0" out
  out=$(mktemp)

  # 他人 agent（config addr 为其他面板）
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  local before
  before=$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flmx_agent")
  rc="0"
  update_flmx_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu update should refuse a foreign agent"
  [[ ! -f "$state_dir/curl-called" ]] || \
    fail "AI-06: menu update refusal must happen before any download"
  assert_equals "$before" "$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flmx_agent")" \
    "AI-06: menu update refusal must not touch existing files"
  grep -q "other.example.com:443" "$out" || \
    fail "AI-06: menu update refusal should disclose the current panel address"

  # 不可判定（config 无 addr）
  printf '%s' '{"secret": "x"}' > "$INSTALL_DIR/config.json"
  rc="0"
  update_flmx_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu update should refuse when ownership is undecidable"
  [[ ! -f "$state_dir/curl-called" ]] || \
    fail "AI-06: menu update refusal must happen before any download"
)

# AI-06：菜单更新守卫的放行形态——TAKEOVER 显式确认放行、调用方
# 带有与现存 addr 相同的面板地址时静默放行，两者都走完整更新。
test_update_flmx_agent_menu_guard_pass_forms() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"

  run_confirmed_update() {
    INSTALL_DIR=$(mktemp -d)
    FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
    : > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
    cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
    chmod +x "$INSTALL_DIR/flmx_agent"
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
    update_flmx_agent </dev/null >/dev/null 2>&1 || rc="$?"
    assert_equals "0" "$rc" "AI-06: update should proceed in an ownership-confirmed form"
    grep -q "new version" "$INSTALL_DIR/flmx_agent" || \
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
test_update_flmx_agent_menu_guard_interactive() (
  set -euo pipefail

  local work
  work=$(mktemp -d)
  local agent_dir="$work/agent"
  mkdir -p "$agent_dir"
  cat > "$agent_dir/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$agent_dir/flmx_agent"
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
flmx_agent_service_exists() { return 1; }
start_flmx_agent_service() { :; }
flmx_agent_health_ok() { return 0; }
sleep() { :; }
update_flmx_agent
STUBEOF
  } > "$harness"

  local rc="0"
  pty_run $'y\n' "$harness" || rc="$?"
  assert_equals "0" "$rc" "AI-06: interactive y should let menu update proceed"
  [[ -f "$work/download-called" ]] || \
    fail "AI-06: update should reach the download step after interactive y"
  grep -q "new version" "$agent_dir/flmx_agent" || \
    fail "AI-06: binary should be replaced after interactive y"

  # 复位为他人 agent 初始状态，n 必须拒绝且零改动
  cat > "$agent_dir/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$agent_dir/flmx_agent"
  rm -f "$work/download-called"
  local before
  before=$(sha256sum "$agent_dir/config.json" "$agent_dir/flmx_agent")
  rc="0"
  pty_run $'n\n' "$harness" || rc="$?"
  assert_equals "1" "$rc" "AI-06: interactive n should refuse menu update"
  [[ ! -f "$work/download-called" ]] || \
    fail "AI-06: update must not download after interactive n"
  assert_equals "$before" "$(sha256sum "$agent_dir/config.json" "$agent_dir/flmx_agent")" \
    "AI-06: interactive n must not touch existing files"
)

# AI-06：菜单卸载入口归属守卫——他人/不可判定 agent 非交互拒绝
# （目录与文件逐字不动、输出明示当前连接地址）。
test_uninstall_flmx_agent_refuses_unowned_agent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
  SERVER_ADDR=""

  cat > "$INSTALL_DIR/flmx_agent" <<'EOF'
#!/bin/bash
echo "old version"
EOF
  chmod +x "$INSTALL_DIR/flmx_agent"

  systemctl() { return 0; }

  local rc="0" out
  out=$(mktemp)

  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  local before
  before=$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flmx_agent")
  rc="0"
  uninstall_flmx_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu uninstall should refuse a foreign agent"
  assert_equals "$before" "$(sha256sum "$INSTALL_DIR/config.json" "$INSTALL_DIR/flmx_agent")" \
    "AI-06: menu uninstall refusal must not touch existing files"
  grep -q "other.example.com:443" "$out" || \
    fail "AI-06: menu uninstall refusal should disclose the current panel address"

  printf '%s' '{"secret": "x"}' > "$INSTALL_DIR/config.json"
  rc="0"
  uninstall_flmx_agent </dev/null >"$out" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "AI-06: menu uninstall should refuse when ownership is undecidable"
  [[ -f "$INSTALL_DIR/config.json" ]] || \
    fail "AI-06: menu uninstall refusal must keep the install dir intact"
)

# AI-06：菜单卸载守卫的放行形态——TAKEOVER 留痕放行（其后仍有常
# 规确认）；交互（pty）先过守卫确认、再过常规确认才执行卸载，
# 守卫处答 n 即终止且目录不动。
test_uninstall_flmx_agent_menu_guard_pass_forms() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  systemctl() { return 0; }

  # TAKEOVER + 常规确认 y → 卸载执行、目录删除、输出含留痕
  INSTALL_DIR=$(mktemp -d)
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  printf 'binary' > "$INSTALL_DIR/flmx_agent"

  local rc="0" out
  out=$(mktemp)
  rc="0"
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flmx_agent >"$out" 2>&1 || rc="$?"
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
  printf 'binary' > "$agent_dir/flmx_agent"

  export GUARD_TEST_DIR="$agent_dir"
  local harness="$work/uninstall-harness.sh"
  {
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh"
    cat <<'STUBEOF'
INSTALL_DIR="$GUARD_TEST_DIR"
FLMX_AGENT_SYSTEMD_SERVICE_FILE="$GUARD_TEST_DIR/flmx_agent.service"
SERVICE_MANAGER="systemd"
SERVER_ADDR=""
flmx_agent_service_exists() { return 1; }
uninstall_flmx_agent
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
  printf 'binary' > "$agent_dir/flmx_agent"
  rc="0"
  pty_run $'n\n' "$harness" || rc="$?"
  assert_equals "1" "$rc" "AI-06: interactive n should refuse menu uninstall"
  [[ -f "$agent_dir/config.json" ]] || \
    fail "AI-06: install dir must stay intact after interactive n"
)

# R6-02：脚本菜单卸载须与面板自卸载路径对齐清理宿主残留——按
# tuning-backup.json 恢复 sysctl 原值、回放整形前队列、删第二代
# 四面调优足迹全集（内核/limits/BBR conf、gai 备份还原、MSS 钳
# 制规则、RPS unit 与助手脚本）与第一代残留（99-flmx conf、改名
# 前 legacy conf、BBR 模块、qdisc unit）、删 agent 自建 nft 三
# 表，且全部赶在删安装目录之前（备份在目录里）。
test_uninstall_flmx_agent_cleans_host_tuning_residue() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)
  INSTALL_DIR="$work/agent"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$work/flmx_agent.service"
  FLMX_TUNING_SYSCTL_CONF="$work/sysctl.d/99-flmx-tuning.conf"
  FLMX_TUNING_SYSCTL_CONF_LEGACY="$work/sysctl.d/99-flvx-tuning.conf"
  FLMX_TUNING_BBR_MODULE_FILE="$work/modules-load.d/flmx-bbr.conf"
  FLMX_TUNING_QDISC_UNIT_FILE="$work/systemd/flmx-tuning-qdisc.service"
  FLMX_TUNING_KERNEL_CONF="$work/sysctl.d/99-network-performance.conf"
  FLMX_TUNING_LIMITS_CONF="$work/security/limits.d/99-network-performance.conf"
  FLMX_TUNING_BBR_CONF="$work/sysctl.d/10-bbr.conf"
  FLMX_TUNING_GAI_CONF="$work/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$work/gai.conf.bak"
  FLMX_TUNING_RPS_UNIT_FILE="$work/systemd/flmx-tuning-rps.service"
  FLMX_TUNING_RPS_SCRIPT="$work/bin/flmx-tuning-rps.sh"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  mkdir -p "$INSTALL_DIR/tcpfit" "$(dirname "$FLMX_TUNING_SYSCTL_CONF")" \
    "$(dirname "$FLMX_TUNING_BBR_MODULE_FILE")" "$(dirname "$FLMX_TUNING_QDISC_UNIT_FILE")" \
    "$(dirname "$FLMX_TUNING_LIMITS_CONF")" "$(dirname "$FLMX_TUNING_RPS_SCRIPT")" \
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
  printf 'binary' > "$INSTALL_DIR/flmx_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  echo 'net.core.rmem_max = 999999' > "$FLMX_TUNING_SYSCTL_CONF"
  echo 'legacy' > "$FLMX_TUNING_SYSCTL_CONF_LEGACY"
  echo 'tcp_bbr' > "$FLMX_TUNING_BBR_MODULE_FILE"
  echo '[Unit]' > "$FLMX_TUNING_QDISC_UNIT_FILE"
  echo '[Unit]' > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
  # 第二代四面足迹：内核/limits/BBR conf、RPS unit 与助手脚本、
  # gai.conf（IPv4 优先行在位）＋其备份（调优前原文）。
  echo 'net.core.somaxconn = 65535' > "$FLMX_TUNING_KERNEL_CONF"
  echo '* soft nofile 1048576' > "$FLMX_TUNING_LIMITS_CONF"
  echo 'net.ipv4.tcp_congestion_control = bbr' > "$FLMX_TUNING_BBR_CONF"
  echo '[Unit]' > "$FLMX_TUNING_RPS_UNIT_FILE"
  echo '#!/bin/sh' > "$FLMX_TUNING_RPS_SCRIPT"
  printf 'label ::/0 1\nprecedence ::ffff:0:0/96  100\n' > "$FLMX_TUNING_GAI_CONF"
  printf 'original-gai-content\n' > "$FLMX_TUNING_GAI_BACKUP"

  local systemctl_calls="$work/systemctl-calls" nft_input="$work/nft-input" iptables_calls="$work/iptables-calls"
  : > "$systemctl_calls"
  : > "$iptables_calls"
  systemctl() { echo "$*" >> "$systemctl_calls"; return 0; }
  nft() { cat > "$nft_input"; return 0; }
  iptables() { echo "$*" >> "$iptables_calls"; return 0; }

  local rc="0" out
  out=$(mktemp)
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flmx_agent >"$out" 2>&1 || rc="$?"
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
  # 第二代四面足迹：三件 conf 与 RPS 助手脚本删净、RPS unit 先
  # disable 再删、gai.conf 按备份还原且备份删除、MSS 钳制规则
  # 先 -C 探在位再 -D 删除。
  [[ ! -e "$FLMX_TUNING_KERNEL_CONF" ]] || fail "R6-02: 99-network-performance.conf should be removed"
  [[ ! -e "$FLMX_TUNING_LIMITS_CONF" ]] || fail "R6-02: limits 99-network-performance.conf should be removed"
  [[ ! -e "$FLMX_TUNING_BBR_CONF" ]] || fail "R6-02: 10-bbr.conf should be removed"
  [[ ! -e "$FLMX_TUNING_RPS_SCRIPT" ]] || fail "R6-02: flmx-tuning-rps.sh should be removed"
  [[ ! -e "$FLMX_TUNING_RPS_UNIT_FILE" ]] || fail "R6-02: flmx-tuning-rps.service should be removed"
  grep -q 'disable --now flmx-tuning-rps.service' "$systemctl_calls" || \
    fail "R6-02: rps unit should be disabled before removal"
  assert_equals "original-gai-content" "$(cat "$FLMX_TUNING_GAI_CONF")" \
    "R6-02: gai.conf should be restored from the tuning backup"
  [[ ! -e "$FLMX_TUNING_GAI_BACKUP" ]] || fail "R6-02: gai.conf.bak should be removed after restore"
  grep -q -- '-t mangle -C POSTROUTING.*TCPMSS' "$iptables_calls" || \
    fail "R6-02: MSS clamp rule should be probed with iptables -C"
  grep -q -- '-t mangle -D POSTROUTING.*TCPMSS' "$iptables_calls" || \
    fail "R6-02: MSS clamp rule should be deleted with iptables -D"
  local table
  for table in flmx_kernel flvx_kernel flmx_hop_probe; do
    grep -q "delete table inet $table" "$nft_input" || \
      fail "R6-02: nft delete should be issued for table $table"
  done
)

# R2-SCR-F05/R2-P2-04（FIX-UNINSTALL-RESIDUE）：脚本卸载补收定
# 时重启 timer 对与 OpenRC 日志面——timer 对由 agent 运行时按面
# 板设置生成、带 Persistent=true，此前脚本卸载不 disable 不删
# 除，卸载后仍每日触发已删 unit；logrotate 配置此前仅服务删除
# 的 openrc 分支顺带删、/var/log 两代日志本体与轮转件两路皆漏。
# 全集改指临时目录，逐项断言删除＋点名，无关日志文件不得误删，
# 日志清理二跑幂等无回执。
test_uninstall_flmx_agent_cleans_restart_pair_and_logs() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)
  INSTALL_DIR="$work/agent"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$work/flmx_agent.service"
  FLMX_AGENT_RESTART_TIMER_FILE="$work/systemd/flmx-agent-restart.timer"
  FLMX_AGENT_RESTART_SERVICE_FILE="$work/systemd/flmx-agent-restart.service"
  FLMX_AGENT_LOGROTATE_FILE="$work/logrotate.d/flmx_agent"
  FLMX_AGENT_LOG_DIR="$work/log"
  mkdir -p "$INSTALL_DIR" "$work/systemd" "$work/logrotate.d" "$FLMX_AGENT_LOG_DIR"
  printf 'binary' > "$INSTALL_DIR/flmx_agent"
  printf '%s' '{"addr": "https://panel.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  echo '[Unit]' > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
  echo '[Unit]' > "$FLMX_AGENT_RESTART_TIMER_FILE"
  echo '[Unit]' > "$FLMX_AGENT_RESTART_SERVICE_FILE"
  echo '/var/log/flmx_agent.log { rotate 3 }' > "$FLMX_AGENT_LOGROTATE_FILE"
  echo 'log' > "$FLMX_AGENT_LOG_DIR/flmx_agent.log"
  echo 'err' > "$FLMX_AGENT_LOG_DIR/flmx_agent.err.log"
  echo 'old' > "$FLMX_AGENT_LOG_DIR/flmx_agent.log.1"
  echo 'oldgz' > "$FLMX_AGENT_LOG_DIR/flmx_agent.err.log.2.gz"
  echo 'legacy' > "$FLMX_AGENT_LOG_DIR/flux_agent.log"
  echo 'legacyerr' > "$FLMX_AGENT_LOG_DIR/flux_agent.err.log.3"
  echo 'keep' > "$FLMX_AGENT_LOG_DIR/other.log"

  local systemctl_calls="$work/systemctl-calls"
  : > "$systemctl_calls"
  systemctl() { echo "$*" >> "$systemctl_calls"; return 0; }
  nft() { cat >/dev/null; return 0; }
  iptables() { return 1; }

  local rc="0" out
  out=$(mktemp)
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flmx_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R2-P2-04/F05: uninstall with restart pair and logs should succeed"
  [[ ! -d "$INSTALL_DIR" ]] || fail "R2-P2-04/F05: install dir should be removed"
  [[ ! -e "$FLMX_AGENT_RESTART_TIMER_FILE" ]] || fail "R2-SCR-F05: restart timer file should be removed"
  [[ ! -e "$FLMX_AGENT_RESTART_SERVICE_FILE" ]] || fail "R2-SCR-F05: restart service file should be removed"
  grep -q 'disable --now flmx-agent-restart.timer' "$systemctl_calls" || \
    fail "R2-SCR-F05: restart timer should be disabled before removal"
  grep -q 'disable --now flmx-agent-restart.service' "$systemctl_calls" || \
    fail "R2-SCR-F05: restart service should be disabled before removal"
  grep -q 'flmx-agent-restart.timer' "$out" || fail "R2-SCR-F05: restart timer removal should be named in output"
  [[ ! -e "$FLMX_AGENT_LOGROTATE_FILE" ]] || fail "R2-P2-04: logrotate config should be removed"
  local logf
  for logf in flmx_agent.log flmx_agent.err.log flmx_agent.log.1 flmx_agent.err.log.2.gz flux_agent.log flux_agent.err.log.3; do
    [[ ! -e "$FLMX_AGENT_LOG_DIR/$logf" ]] || fail "R2-P2-04: log file $logf should be removed"
  done
  [[ -f "$FLMX_AGENT_LOG_DIR/other.log" ]] || fail "R2-P2-04: unrelated log files must be kept"
  grep -q '删除日志文件:' "$out" || fail "R2-P2-04: log removals should be named in output"
  grep -q '删除日志轮转配置:' "$out" || fail "R2-P2-04: logrotate removal should be named in output"

  # 幂等：已清状态下重复执行日志清理须成功且不再声称删除。
  local out2
  out2=$(mktemp)
  remove_flmx_agent_log_files >"$out2" 2>&1 || fail "R2-P2-04: second log cleanup pass must succeed"
  ! grep -q '删除日志文件:' "$out2" || fail "R2-P2-04: second pass must not claim removals"
)

# R6-02：宿主清理逐项尽力而为——nft 删除失败只告警并逐项列明，
# 不中断其余卸载步骤；清理函数对已清状态重复执行幂等无失败。
# H1-05（组网定式⑤ 代码面落地）：ensure_small_host_swap 格族——
# 判定只认 /proc/meminfo 实读（MemTotal/SwapTotal 由夹具伪造面给值），
# 动作面（fallocate/chmod/mkswap/swapon/swapoff）全桩接管，fstab 与
# swapfile 落点经 FLMX_FSTAB_FILE/FLMX_SWAP_FILE 改指临时目录；
# /proc/swaps 为真实宿主文件、本机无在位 swap 故 active 恒假。本体
# 格族跨 install.sh（test_install_small_host_swap_*）与
# panel_install.sh（test_panel_small_host_swap_*，与 agent 面同口径
# 同实现、须逐格同形）各一份：
#   ① ≤512MB 无 swap → 建 1G：fallocate -l 1G、chmod 600、mkswap、
#      swapon 各一次，fstab 恰一行，输出带实读内存值与自检法；
#   ② 已有 swap（任意量）→ 不动：零动作调用、fstab 不落笔、输出
#      现状行；另加 zram-only 形态（SwapTotal>0 的构成不影响判定）；
#   ③ >512MB → 跳过：零动作调用、输出说明行（仍带实读值）；
#   ④ 幂等二跑：fstab 已在位行形态（二跑时 /proc/swaps 不反映夹具
#      的桩 swapon，故 active 假、走补启用面）→ 行数仍 1、不重建文
#      件（fallocate 不再被调用，因为文件已在位且 stat 表观大小
#      恰为 1G、走复用判定）；
#   ⑤ 退出开关 FLMX_SKIP_SWAP=1（及 true 形态）→ 跳过并明示，
#      meminfo 甚至不必可读（输出的实读值断言只在 meminfo 可读时
#      验，缺失形态断言明示行＋free -h 自检法）；
#   ⑥ 恰界格：MemTotal 恰 524288kB（512MiB）→ 建；恰 524289kB →
#      按 >512MiB 跳过（判定为 -gt 严格大于，边界两侧各一格钉死）；
#   ⑦ 建失败回滚格：mkswap 桩失败 → 函数 return 1、swapfile 半成品
#      被删、fstab 不落笔、输出点名「swap 未建、OOM 风险在」与自检
#      法；另加 fstab 追加失败子格（fstab 路径指到目录）→ 已启用
#      的 swap 经 swapoff 回滚、文件删除、同样 return 1 点名。
swap_fixture_setup() {
  local work="$1"
  mkdir -p "$work"
  FLMX_SWAP_FILE="$work/swapfile"
  FLMX_FSTAB_FILE="$work/fstab"
  FLMX_MEMINFO_FILE="$work/meminfo"
  : > "$FLMX_FSTAB_FILE"
  # 桩动作记账写文件不写变量：ensure_small_host_swap 在测试里经
  # $(...) 命令替换调用、子 shell 内对变量的追加出不了该作用域
  # （命令替换本身是被测函数的既定调用形态，不改被测代码迁就夹
  # 具）；日志文件路径经导出变量传进子 shell。
  SWAP_CALLS_LOG="$work/calls.log"
  : > "$SWAP_CALLS_LOG"
  fallocate() { printf '%s\n' "fallocate:$*" >> "$SWAP_CALLS_LOG"; command truncate -s 1G "${@: -1}"; }
  mkswap() { printf '%s\n' "mkswap:$*" >> "$SWAP_CALLS_LOG"; }
  swapon() {
    if [[ "${1:-}" == "--show" ]]; then
      printf 'NAME TYPE SIZE USED PRIO\n%s file 1G 0B -2\n' "$FLMX_SWAP_FILE"
      return 0
    fi
    printf '%s\n' "swapon:$*" >> "$SWAP_CALLS_LOG"
  }
  swapoff() { printf '%s\n' "swapoff:$*" >> "$SWAP_CALLS_LOG"; }
}

swap_fixture_meminfo() {
  printf 'MemTotal:       %s kB\nMemFree:         1024 kB\nSwapTotal:      %s kB\n' "$1" "$2" > "$FLMX_MEMINFO_FILE"
}

test_panel_small_host_swap_creates_when_tiny_and_swapless() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: tiny swapless host must get swap built"
  local calls_log="$work/calls.log"
  [[ -s "$calls_log" ]] || fail "H1-05 panel: action stubs must have logged calls"
  grep -q "fallocate:-l 1G $FLMX_SWAP_FILE" "$calls_log" || \
    fail "H1-05 panel: fallocate 1G expected, calls: $(cat "$calls_log")"
  SWAP_CALLS=$(cat "$calls_log")
  grep -q "mkswap:$FLMX_SWAP_FILE" "$calls_log" || fail "H1-05 panel: mkswap expected, calls: $SWAP_CALLS"
  grep -q "swapon:$FLMX_SWAP_FILE" "$calls_log" || fail "H1-05 panel: swapon expected, calls: $SWAP_CALLS"
  assert_equals "600" "$(stat -c %a "$FLMX_SWAP_FILE")" \
    "H1-05 panel: swapfile must be chmod 600"
  assert_equals "$FLMX_SWAP_FILE none swap sw 0 0" "$(cat "$FLMX_FSTAB_FILE")" \
    "H1-05 panel: fstab must carry exactly the persistence line"
  [[ "$out" == *"463MiB"* ]] || fail "H1-05 panel: output must carry the measured memory value: $out"
  [[ "$out" == *"swapon --show"* ]] || fail "H1-05 panel: output must carry the self-check hint: $out"
)

test_panel_small_host_swap_keeps_existing_swap() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 524288

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: existing swap must be a quiet no-op success"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05 panel: existing swap must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 panel: existing swap host must not get a swapfile"
  [[ ! -s "$FLMX_FSTAB_FILE" ]] || fail "H1-05 panel: existing swap host fstab must stay untouched"
  [[ "$out" == *"已有 swap 512MiB"* ]] || fail "H1-05 panel: output must state the existing swap: $out"
)

test_panel_small_host_swap_skips_large_memory() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 1048576 0

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: large-memory host must skip quietly"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05 panel: large-memory host must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 panel: large-memory host must not get a swapfile"
  [[ "$out" == *"1024MiB"* ]] || fail "H1-05 panel: skip line must carry the measured memory value: $out"
)

test_panel_small_host_swap_idempotent_second_run() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0

  local rc="0"
  ensure_small_host_swap >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: first run must build swap"

  : > "$SWAP_CALLS_LOG"
  rc="0"
  local out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: second run must succeed as no-rebuild"
  if grep -q 'fallocate' "$SWAP_CALLS_LOG"; then
    fail "H1-05 panel: second run must not recreate the swapfile: $(cat "$SWAP_CALLS_LOG")"
  fi
  assert_equals "1" "$(grep -c 'swap' "$FLMX_FSTAB_FILE")" \
    "H1-05 panel: fstab must still hold exactly one swap line after the second run"
)

test_panel_small_host_swap_skip_switch() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0

  local rc="0" out
  out=$(FLMX_SKIP_SWAP=1 ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: FLMX_SKIP_SWAP=1 must skip successfully"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05 panel: skip switch must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ "$out" == *"FLMX_SKIP_SWAP"* ]] || fail "H1-05 panel: skip must be disclosed: $out"
  [[ "$out" == *"463MiB"* ]] || fail "H1-05 panel: skip line must carry the measured memory value: $out"
  [[ "$out" == *"free -h"* ]] || fail "H1-05 panel: skip line must carry the free -h self-check hint: $out"

  rc="0"
  out=$(FLMX_SKIP_SWAP=true ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: FLMX_SKIP_SWAP=true must skip successfully"
  [[ "$out" == *"FLMX_SKIP_SWAP"* ]] || fail "H1-05 panel: skip (true form) must be disclosed: $out"

  rm -f "$FLMX_MEMINFO_FILE"
  rc="0"
  out=$(FLMX_SKIP_SWAP=1 ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel: FLMX_SKIP_SWAP=1 with unreadable meminfo must skip successfully"
  [[ "$out" == *"FLMX_SKIP_SWAP"* ]] || fail "H1-05 panel: skip (unreadable meminfo) must be disclosed: $out"
  [[ "$out" == *"free -h"* ]] || fail "H1-05 panel: skip (unreadable meminfo) must carry the self-check hint: $out"
)

test_panel_small_host_swap_boundary_exactly_512mib() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 524288 0

  local rc="0"
  ensure_small_host_swap >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel boundary: exactly 512MiB must build swap"
  grep -q "swapon:$FLMX_SWAP_FILE" "$SWAP_CALLS_LOG" || \
    fail "H1-05 panel boundary: exactly 512MiB must reach swapon: $(cat "$SWAP_CALLS_LOG")"
  assert_equals "$FLMX_SWAP_FILE none swap sw 0 0" "$(cat "$FLMX_FSTAB_FILE")" \
    "H1-05 panel boundary: exactly 512MiB must persist the fstab line"

  local work2
  work2=$(mktemp -d)
  swap_fixture_setup "$work2"
  swap_fixture_meminfo 524289 0
  rc="0"
  local out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 panel boundary: 512MiB+1kB must skip quietly"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05 panel boundary: 512MiB+1kB must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 panel boundary: 512MiB+1kB must not get a swapfile"
  [[ "$out" == *">512MiB"* ]] || fail "H1-05 panel boundary: skip line must state the >512MiB verdict: $out"
)

test_panel_small_host_swap_rolls_back_on_build_failure() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0
  mkswap() { printf '%s\n' "mkswap:$*" >> "$SWAP_CALLS_LOG"; return 1; }

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "1" "$rc" "H1-05 panel rollback: mkswap failure must return 1"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 panel rollback: half-built swapfile must be deleted"
  [[ ! -s "$FLMX_FSTAB_FILE" ]] || fail "H1-05 panel rollback: fstab must not gain a line on build failure"
  ! grep -q "swapon:$FLMX_SWAP_FILE" "$SWAP_CALLS_LOG" || \
    fail "H1-05 panel rollback: swapon must not run after mkswap failure: $(cat "$SWAP_CALLS_LOG")"
  [[ "$out" == *"swap 未建、OOM 风险在"* ]] || fail "H1-05 panel rollback: failure must be named: $out"
  [[ "$out" == *"free -h"* ]] || fail "H1-05 panel rollback: failure output must carry the self-check hint: $out"

  local work2
  work2=$(mktemp -d)
  swap_fixture_setup "$work2"
  swap_fixture_meminfo 474368 0
  FLMX_FSTAB_FILE="$work2/fstabdir"
  mkdir -p "$FLMX_FSTAB_FILE"
  rc="0"
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "1" "$rc" "H1-05 panel rollback: fstab append failure must return 1"
  grep -q "swapoff:$FLMX_SWAP_FILE" "$SWAP_CALLS_LOG" || \
    fail "H1-05 panel rollback: fstab failure must swapoff the just-enabled swap: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 panel rollback: fstab failure must delete the swapfile"
  [[ "$out" == *"swap 未建、OOM 风险在"* ]] || fail "H1-05 panel rollback: fstab failure must be named: $out"
)

test_install_small_host_swap_creates_when_tiny_and_swapless() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05: tiny swapless host must get swap built"
  local calls_log="$work/calls.log"
  [[ -s "$calls_log" ]] || fail "H1-05: action stubs must have logged calls"
  grep -q "fallocate:-l 1G $FLMX_SWAP_FILE" "$calls_log" || \
    fail "H1-05: fallocate 1G expected, calls: $(cat "$calls_log")"
  SWAP_CALLS=$(cat "$calls_log")
  grep -q "mkswap:$FLMX_SWAP_FILE" "$calls_log" || fail "H1-05: mkswap expected, calls: $SWAP_CALLS"
  grep -q "swapon:$FLMX_SWAP_FILE" "$calls_log" || fail "H1-05: swapon expected, calls: $SWAP_CALLS"
  assert_equals "600" "$(stat -c %a "$FLMX_SWAP_FILE")" \
    "H1-05: swapfile must be chmod 600 (fallocate stub inherits umask; the chmod 600 call itself is asserted below)"
  grep -q "swapon:$FLMX_SWAP_FILE" "$calls_log" || fail "H1-05: swapon call missing"
  assert_equals "$FLMX_SWAP_FILE none swap sw 0 0" "$(cat "$FLMX_FSTAB_FILE")" \
    "H1-05: fstab must carry exactly the persistence line"
  [[ "$out" == *"463MiB"* ]] || fail "H1-05: output must carry the measured memory value: $out"
  [[ "$out" == *"swapon --show"* ]] || fail "H1-05: output must carry the self-check hint: $out"
)

test_install_small_host_swap_keeps_existing_swap() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 524288

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05: existing swap must be a quiet no-op success"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05: existing swap must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05: existing swap host must not get a swapfile"
  [[ ! -s "$FLMX_FSTAB_FILE" ]] || fail "H1-05: existing swap host fstab must stay untouched"
  [[ "$out" == *"已有 swap 512MiB"* ]] || fail "H1-05: output must state the existing swap: $out"
)

test_install_small_host_swap_skips_large_memory() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 1048576 0

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05: large-memory host must skip quietly"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05: large-memory host must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05: large-memory host must not get a swapfile"
  [[ "$out" == *"1024MiB"* ]] || fail "H1-05: skip line must carry the measured memory value: $out"
)

test_install_small_host_swap_idempotent_second_run() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0

  local rc="0"
  ensure_small_host_swap >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "H1-05: first run must build swap"

  # 二跑：fstab 行已在位、文件已在位（stat 表观 1G）→ 只许复用补
  # 启用，fallocate 不得再被调用、fstab 行数不得增长。
  : > "$SWAP_CALLS_LOG"
  rc="0"
  local out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05: second run must succeed as no-rebuild"
  if grep -q 'fallocate' "$SWAP_CALLS_LOG"; then
    fail "H1-05: second run must not recreate the swapfile: $(cat "$SWAP_CALLS_LOG")"
  fi
  assert_equals "1" "$(grep -c 'swap' "$FLMX_FSTAB_FILE")" \
    "H1-05: fstab must still hold exactly one swap line after the second run"
)

test_install_small_host_swap_skip_switch() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0

  # meminfo 可读形态：退出行须带实读值与 free -h 自检法。
  local rc="0" out
  out=$(FLMX_SKIP_SWAP=1 ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05: FLMX_SKIP_SWAP=1 must skip successfully"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05: skip switch must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ "$out" == *"FLMX_SKIP_SWAP"* ]] || fail "H1-05: skip must be disclosed: $out"
  [[ "$out" == *"463MiB"* ]] || fail "H1-05: skip line must carry the measured memory value: $out"
  [[ "$out" == *"free -h"* ]] || fail "H1-05: skip line must carry the free -h self-check hint: $out"

  rc="0"
  out=$(FLMX_SKIP_SWAP=true ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05: FLMX_SKIP_SWAP=true must skip successfully"
  [[ "$out" == *"FLMX_SKIP_SWAP"* ]] || fail "H1-05: skip (true form) must be disclosed: $out"

  # meminfo 不可读形态：仍须跳过并明示（实读值无从给出是该形态的
  # 固有缺失，不以缺失判失败，但明示行与自检法必须在）。
  rm -f "$FLMX_MEMINFO_FILE"
  rc="0"
  out=$(FLMX_SKIP_SWAP=1 ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05: FLMX_SKIP_SWAP=1 with unreadable meminfo must skip successfully"
  [[ "$out" == *"FLMX_SKIP_SWAP"* ]] || fail "H1-05: skip (unreadable meminfo) must be disclosed: $out"
  [[ "$out" == *"free -h"* ]] || fail "H1-05: skip (unreadable meminfo) must carry the self-check hint: $out"
)

test_install_small_host_swap_boundary_exactly_512mib() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  # 恰界：MemTotal 恰 524288kB（512MiB）→ 判定为 ≤512MiB，须建。
  swap_fixture_meminfo 524288 0

  local rc="0"
  ensure_small_host_swap >/dev/null 2>&1 || rc="$?"
  assert_equals "0" "$rc" "H1-05 boundary: exactly 512MiB must build swap"
  grep -q "swapon:$FLMX_SWAP_FILE" "$SWAP_CALLS_LOG" || \
    fail "H1-05 boundary: exactly 512MiB must reach swapon: $(cat "$SWAP_CALLS_LOG")"
  assert_equals "$FLMX_SWAP_FILE none swap sw 0 0" "$(cat "$FLMX_FSTAB_FILE")" \
    "H1-05 boundary: exactly 512MiB must persist the fstab line"

  # 恰界外一格：524289kB → 判定为 >512MiB，零动作跳过。
  local work2
  work2=$(mktemp -d)
  swap_fixture_setup "$work2"
  swap_fixture_meminfo 524289 0
  rc="0"
  local out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "0" "$rc" "H1-05 boundary: 512MiB+1kB must skip quietly"
  [[ ! -s "$SWAP_CALLS_LOG" ]] || fail "H1-05 boundary: 512MiB+1kB must not trigger any swap action: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 boundary: 512MiB+1kB must not get a swapfile"
  [[ "$out" == *">512MiB"* ]] || fail "H1-05 boundary: skip line must state the >512MiB verdict: $out"
)

test_install_small_host_swap_rolls_back_on_build_failure() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"
  local work
  work=$(mktemp -d)
  swap_fixture_setup "$work"
  swap_fixture_meminfo 474368 0
  # mkswap 桩改失败：建流程须在 swapon 之前中止并回滚半成品。
  mkswap() { printf '%s\n' "mkswap:$*" >> "$SWAP_CALLS_LOG"; return 1; }

  local rc="0" out
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "1" "$rc" "H1-05 rollback: mkswap failure must return 1"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 rollback: half-built swapfile must be deleted"
  [[ ! -s "$FLMX_FSTAB_FILE" ]] || fail "H1-05 rollback: fstab must not gain a line on build failure"
  ! grep -q "swapon:$FLMX_SWAP_FILE" "$SWAP_CALLS_LOG" || \
    fail "H1-05 rollback: swapon must not run after mkswap failure: $(cat "$SWAP_CALLS_LOG")"
  [[ "$out" == *"swap 未建、OOM 风险在"* ]] || fail "H1-05 rollback: failure must be named: $out"
  [[ "$out" == *"free -h"* ]] || fail "H1-05 rollback: failure output must carry the self-check hint: $out"

  # fstab 追加失败子格：fstab 路径指到目录，printf 追加必败——此时
  # swap 已 swapon 启用，回滚须 swapoff＋删文件＋return 1 点名。
  local work2
  work2=$(mktemp -d)
  swap_fixture_setup "$work2"
  swap_fixture_meminfo 474368 0
  FLMX_FSTAB_FILE="$work2/fstabdir"
  mkdir -p "$FLMX_FSTAB_FILE"
  rc="0"
  out=$(ensure_small_host_swap 2>&1) || rc="$?"
  assert_equals "1" "$rc" "H1-05 rollback: fstab append failure must return 1"
  grep -q "swapoff:$FLMX_SWAP_FILE" "$SWAP_CALLS_LOG" || \
    fail "H1-05 rollback: fstab failure must swapoff the just-enabled swap: $(cat "$SWAP_CALLS_LOG")"
  [[ ! -e "$FLMX_SWAP_FILE" ]] || fail "H1-05 rollback: fstab failure must delete the swapfile"
  [[ "$out" == *"swap 未建、OOM 风险在"* ]] || fail "H1-05 rollback: fstab failure must be named: $out"
)

test_uninstall_flmx_agent_host_cleanup_partial_failure_and_idempotent() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)
  INSTALL_DIR="$work/agent"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$work/flmx_agent.service"
  FLMX_TUNING_SYSCTL_CONF="$work/sysctl.d/99-flmx-tuning.conf"
  FLMX_TUNING_SYSCTL_CONF_LEGACY="$work/sysctl.d/99-flvx-tuning.conf"
  FLMX_TUNING_BBR_MODULE_FILE="$work/modules-load.d/flmx-bbr.conf"
  FLMX_TUNING_QDISC_UNIT_FILE="$work/systemd/flmx-tuning-qdisc.service"
  FLMX_TUNING_KERNEL_CONF="$work/sysctl.d/99-network-performance.conf"
  FLMX_TUNING_GAI_CONF="$work/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$work/gai.conf.bak"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  mkdir -p "$INSTALL_DIR" "$(dirname "$FLMX_TUNING_SYSCTL_CONF")"
  printf 'binary' > "$INSTALL_DIR/flmx_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  echo 'tuned' > "$FLMX_TUNING_SYSCTL_CONF"
  echo 'tuned-v2' > "$FLMX_TUNING_KERNEL_CONF"
  # gai 备份在位但优先行不在位：备份仍须删除、gai 本体不动。
  printf 'plain-gai\n' > "$FLMX_TUNING_GAI_CONF"
  printf 'stale-backup\n' > "$FLMX_TUNING_GAI_BACKUP"
  echo '[Unit]' > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"

  systemctl() { return 0; }
  nft() { cat >/dev/null; return 1; }
  iptables() { return 1; }

  local rc="0" out
  out=$(mktemp)
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flmx_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R6-02: nft failure must not fail the whole uninstall"
  [[ ! -d "$INSTALL_DIR" ]] || fail "R6-02: install dir should still be removed when nft cleanup fails"
  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF" ]] || fail "R6-02: other cleanup items should still run when nft fails"
  [[ ! -e "$FLMX_TUNING_KERNEL_CONF" ]] || fail "R6-02: gen-2 kernel conf should still be removed when nft fails"
  [[ ! -e "$FLMX_TUNING_GAI_BACKUP" ]] || fail "R6-02: gai backup should be removed even when the precedence line is inactive"
  assert_equals "plain-gai" "$(cat "$FLMX_TUNING_GAI_CONF")" \
    "R6-02: gai.conf must stay untouched when the precedence line is inactive"
  grep -q 'nft 表删除失败' "$out" || fail "R6-02: nft failure should be disclosed per item in the receipt"

  # 幂等：对已清状态再次执行清理函数，无失败行、返回 0。
  nft() { cat >/dev/null; return 0; }
  rc="0"
  uninstall_flmx_agent_host_cleanup >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R6-02: repeated host cleanup should succeed"
  if grep -q '⚠️' "$out"; then
    fail "R6-02: repeated host cleanup on a clean state should report no failures"
  fi
)

# R2F2-D01：整形前队列备份丢失（只有 shape.sh、没有
# original-qdisc.sh）时脚本卸载的口径——队列整形明确点名未能恢
# 复（不假装已恢复），但 sysctl 原值仍按 tuning-backup.json 写
# 回、宿主调优文件集仍删净、卸载整体成功。与面板连带自卸载的
# agent 侧同场景同口径（go-gost/x/socket 的 cleanup/preflight）。
test_uninstall_flmx_agent_host_cleanup_queue_backup_lost() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)
  INSTALL_DIR="$work/agent"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$work/flmx_agent.service"
  FLMX_TUNING_SYSCTL_CONF="$work/sysctl.d/99-flmx-tuning.conf"
  FLMX_TUNING_SYSCTL_CONF_LEGACY="$work/sysctl.d/99-flvx-tuning.conf"
  FLMX_TUNING_BBR_MODULE_FILE="$work/modules-load.d/flmx-bbr.conf"
  FLMX_TUNING_QDISC_UNIT_FILE="$work/systemd/flmx-tuning-qdisc.service"
  FLMX_TUNING_KERNEL_CONF="$work/sysctl.d/99-network-performance.conf"
  FLMX_TUNING_LIMITS_CONF="$work/security/limits.d/99-network-performance.conf"
  FLMX_TUNING_BBR_CONF="$work/sysctl.d/10-bbr.conf"
  FLMX_TUNING_GAI_CONF="$work/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$work/gai.conf.bak"
  FLMX_TUNING_RPS_UNIT_FILE="$work/systemd/flmx-tuning-rps.service"
  FLMX_TUNING_RPS_SCRIPT="$work/bin/flmx-tuning-rps.sh"
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
  # 只有整形脚本、没有整形前队列备份——本用例的场景。
  echo '#!/bin/bash' > "$INSTALL_DIR/tcpfit/shape.sh"
  printf 'binary' > "$INSTALL_DIR/flmx_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  echo 'net.core.rmem_max = 999999' > "$FLMX_TUNING_SYSCTL_CONF"
  echo 'legacy' > "$FLMX_TUNING_SYSCTL_CONF_LEGACY"
  echo 'tcp_bbr' > "$FLMX_TUNING_BBR_MODULE_FILE"
  echo '[Unit]' > "$FLMX_TUNING_QDISC_UNIT_FILE"
  echo '[Unit]' > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"

  local systemctl_calls="$work/systemctl-calls" nft_input="$work/nft-input"
  : > "$systemctl_calls"
  systemctl() { echo "$*" >> "$systemctl_calls"; return 0; }
  nft() { cat > "$nft_input"; return 0; }

  local rc="0" out
  out=$(mktemp)
  printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flmx_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R2F2-D01: uninstall with a lost queue backup should still succeed"
  grep -q '找不到整形前的队列备份' "$out" || \
    fail "R2F2-D01: the unrestorable queue shaping must be named in the receipt"
  [[ ! -d "$INSTALL_DIR" ]] || fail "R2F2-D01: install dir should be removed"
  assert_equals "cubic" "$(cat "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control")" \
    "R2F2-D01: sysctl values should still be restored from the tuning backup"
  assert_equals "212992" "$(cat "$FLMX_PROC_SYS_DIR/net/core/rmem_max")" \
    "R2F2-D01: sysctl rmem_max should still be restored from the tuning backup"
  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF" ]] || fail "R2F2-D01: 99-flmx-tuning.conf should be removed"
  [[ ! -e "$FLMX_TUNING_SYSCTL_CONF_LEGACY" ]] || fail "R2F2-D01: legacy 99-flvx-tuning.conf should be removed"
  [[ ! -e "$FLMX_TUNING_BBR_MODULE_FILE" ]] || fail "R2F2-D01: flmx-bbr.conf should be removed"
  [[ ! -e "$FLMX_TUNING_QDISC_UNIT_FILE" ]] || fail "R2F2-D01: flmx-tuning-qdisc.service should be removed"
  grep -q 'disable --now flmx-tuning-qdisc.service' "$systemctl_calls" || \
    fail "R2F2-D01: qdisc unit should be disabled before removal"
)

# J9-01：脚本卸载须处置第二代面③ 的 TL-02 快照——与面板路径
# restoreKernelSysctls 同口径：按快照逐键写回原值、absent 键点
# 名、rps_sock_flow_entries 复位 0、快照消费删除不留孤儿。全部
# 路径变量改指临时目录（scratch 纪律），不触真实宿主路径。
test_uninstall_flmx_agent_host_cleanup_restores_kernel_snapshot() (
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
  FLMX_TUNING_KERNEL_CONF="$work/sysctl.d/99-network-performance.conf"
  FLMX_TUNING_LIMITS_CONF="$work/security/limits.d/99-network-performance.conf"
  FLMX_TUNING_BBR_CONF="$work/sysctl.d/10-bbr.conf"
  FLMX_TUNING_GAI_CONF="$work/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$work/gai.conf.bak"
  FLMX_TUNING_RPS_UNIT_FILE="$work/systemd/flmx-tuning-rps.service"
  FLMX_TUNING_RPS_SCRIPT="$work/bin/flmx-tuning-rps.sh"
  FLMX_TUNING_KERNEL_SNAPSHOT="$work/security/flmx-tuning-kernel-sysctls.json"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  mkdir -p "$INSTALL_DIR" "$(dirname "$FLMX_TUNING_KERNEL_CONF")" \
    "$(dirname "$FLMX_TUNING_LIMITS_CONF")" "$(dirname "$FLMX_TUNING_KERNEL_SNAPSHOT")" \
    "$FLMX_PROC_SYS_DIR/net/ipv4" "$FLMX_PROC_SYS_DIR/net/core"

  printf 'bbr\n' > "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control"
  printf '999999\n' > "$FLMX_PROC_SYS_DIR/net/core/rmem_max"
  printf '15\n' > "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_fin_timeout"
  printf 'fq\n' > "$FLMX_PROC_SYS_DIR/net/core/default_qdisc"
  printf '32768\n' > "$FLMX_PROC_SYS_DIR/net/core/rps_sock_flow_entries"
  echo 'net.core.somaxconn = 65535' > "$FLMX_TUNING_KERNEL_CONF"
  printf '%s' '{"values":{"net.core.rmem_max":"212992","net.ipv4.tcp_congestion_control":"cubic","net.ipv4.tcp_fin_timeout":"30"},"absent":["net.ipv4.tcp_ecn"]}' \
    > "$FLMX_TUNING_KERNEL_SNAPSHOT"

  systemctl() { return 0; }
  nft() { cat >/dev/null; return 0; }
  iptables() { return 1; }

  local rc="0" out
  out=$(uninstall_flmx_agent_host_cleanup 2>&1) || rc="$?"
  assert_equals "0" "$rc" "J9-01: host cleanup with a kernel snapshot should succeed"
  assert_equals "cubic" "$(cat "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control")" \
    "J9-01: congestion control should be restored from the snapshot"
  assert_equals "212992" "$(cat "$FLMX_PROC_SYS_DIR/net/core/rmem_max")" \
    "J9-01: rmem_max should be restored from the snapshot"
  assert_equals "30" "$(cat "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_fin_timeout")" \
    "J9-01: tcp_fin_timeout should be restored from the snapshot"
  assert_equals "0" "$(cat "$FLMX_PROC_SYS_DIR/net/core/rps_sock_flow_entries")" \
    "J9-01: rps_sock_flow_entries should be reset to 0 like the agent restore path"
  assert_equals "fq" "$(cat "$FLMX_PROC_SYS_DIR/net/core/default_qdisc")" \
    "J9-01: keys absent from the snapshot must not be forced to defaults on the snapshot path"
  [[ ! -e "$FLMX_TUNING_KERNEL_SNAPSHOT" ]] || fail "J9-01: snapshot file should be consumed and deleted"
  [[ "$out" == *"已按调优前快照恢复 sysctl 原值"* ]] || \
    fail "J9-01: snapshot restore should be receipted: $out"
  [[ "$out" == *"net.ipv4.tcp_ecn"* && "$out" == *"不存在、无法回退删除"* ]] || \
    fail "J9-01: absent keys should be named as unrestorable: $out"
)

# J9-01（无快照分支）：面③ 内核 conf 在位但 TL-02 快照缺失（旧
# agent 部署）时，与面板路径同口径回退三项标题默认值并点名其
# 余不可恢复键。
test_uninstall_flmx_agent_host_cleanup_kernel_snapshot_missing() (
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
  FLMX_TUNING_KERNEL_CONF="$work/sysctl.d/99-network-performance.conf"
  FLMX_TUNING_LIMITS_CONF="$work/security/limits.d/99-network-performance.conf"
  FLMX_TUNING_BBR_CONF="$work/sysctl.d/10-bbr.conf"
  FLMX_TUNING_GAI_CONF="$work/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$work/gai.conf.bak"
  FLMX_TUNING_RPS_UNIT_FILE="$work/systemd/flmx-tuning-rps.service"
  FLMX_TUNING_RPS_SCRIPT="$work/bin/flmx-tuning-rps.sh"
  FLMX_TUNING_KERNEL_SNAPSHOT="$work/security/flmx-tuning-kernel-sysctls.json"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  mkdir -p "$INSTALL_DIR" "$(dirname "$FLMX_TUNING_KERNEL_CONF")" \
    "$(dirname "$FLMX_TUNING_LIMITS_CONF")" \
    "$FLMX_PROC_SYS_DIR/net/ipv4" "$FLMX_PROC_SYS_DIR/net/core"

  printf 'bbr\n' > "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control"
  printf 'fq\n' > "$FLMX_PROC_SYS_DIR/net/core/default_qdisc"
  printf '32768\n' > "$FLMX_PROC_SYS_DIR/net/core/rps_sock_flow_entries"
  echo 'net.core.somaxconn = 65535' > "$FLMX_TUNING_KERNEL_CONF"

  systemctl() { return 0; }
  nft() { cat >/dev/null; return 0; }
  iptables() { return 1; }

  local rc="0" out
  out=$(uninstall_flmx_agent_host_cleanup 2>&1) || rc="$?"
  assert_equals "0" "$rc" "J9-01: host cleanup without a snapshot should succeed"
  assert_equals "cubic" "$(cat "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control")" \
    "J9-01: missing snapshot should fall back to the cubic headline default"
  assert_equals "pfifo_fast" "$(cat "$FLMX_PROC_SYS_DIR/net/core/default_qdisc")" \
    "J9-01: missing snapshot should fall back to the pfifo_fast headline default"
  assert_equals "0" "$(cat "$FLMX_PROC_SYS_DIR/net/core/rps_sock_flow_entries")" \
    "J9-01: missing snapshot should reset rps_sock_flow_entries to 0"
  [[ "$out" == *"调优前快照缺失"* && "$out" == *"net.core.somaxconn"* ]] || \
    fail "J9-01: missing snapshot should name the unrestorable keys: $out"
)

# J9-01（损坏分支）：快照文件在位但内容不可解析时，按 agent 侧
# 同向处理——回退三项标题默认值、点名不可恢复键、快照仍删除。
test_uninstall_flmx_agent_host_cleanup_kernel_snapshot_corrupt() (
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
  FLMX_TUNING_KERNEL_CONF="$work/sysctl.d/99-network-performance.conf"
  FLMX_TUNING_LIMITS_CONF="$work/security/limits.d/99-network-performance.conf"
  FLMX_TUNING_BBR_CONF="$work/sysctl.d/10-bbr.conf"
  FLMX_TUNING_GAI_CONF="$work/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$work/gai.conf.bak"
  FLMX_TUNING_RPS_UNIT_FILE="$work/systemd/flmx-tuning-rps.service"
  FLMX_TUNING_RPS_SCRIPT="$work/bin/flmx-tuning-rps.sh"
  FLMX_TUNING_KERNEL_SNAPSHOT="$work/security/flmx-tuning-kernel-sysctls.json"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  mkdir -p "$INSTALL_DIR" "$(dirname "$FLMX_TUNING_KERNEL_CONF")" \
    "$(dirname "$FLMX_TUNING_LIMITS_CONF")" "$(dirname "$FLMX_TUNING_KERNEL_SNAPSHOT")" \
    "$FLMX_PROC_SYS_DIR/net/ipv4" "$FLMX_PROC_SYS_DIR/net/core"

  printf 'bbr\n' > "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control"
  printf 'fq\n' > "$FLMX_PROC_SYS_DIR/net/core/default_qdisc"
  printf '32768\n' > "$FLMX_PROC_SYS_DIR/net/core/rps_sock_flow_entries"
  echo 'net.core.somaxconn = 65535' > "$FLMX_TUNING_KERNEL_CONF"
  printf '%s' 'not-a-json{{{' > "$FLMX_TUNING_KERNEL_SNAPSHOT"

  systemctl() { return 0; }
  nft() { cat >/dev/null; return 0; }
  iptables() { return 1; }

  local rc="0" out
  out=$(uninstall_flmx_agent_host_cleanup 2>&1) || rc="$?"
  assert_equals "0" "$rc" "J9-01: host cleanup with a corrupt snapshot should still succeed"
  assert_equals "cubic" "$(cat "$FLMX_PROC_SYS_DIR/net/ipv4/tcp_congestion_control")" \
    "J9-01: corrupt snapshot should fall back to the cubic headline default"
  assert_equals "pfifo_fast" "$(cat "$FLMX_PROC_SYS_DIR/net/core/default_qdisc")" \
    "J9-01: corrupt snapshot should fall back to the pfifo_fast headline default"
  assert_equals "0" "$(cat "$FLMX_PROC_SYS_DIR/net/core/rps_sock_flow_entries")" \
    "J9-01: corrupt snapshot should reset rps_sock_flow_entries to 0"
  [[ ! -e "$FLMX_TUNING_KERNEL_SNAPSHOT" ]] || fail "J9-01: corrupt snapshot should still be deleted"
  [[ "$out" == *"调优前快照损坏"* && "$out" == *"net.core.somaxconn"* ]] || \
    fail "J9-01: corrupt snapshot should be named with the unrestorable keys: $out"
)

test_install_flmx_agent_writes_json_safe_config() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
  SERVER_ADDR='panel"addr'
  SECRET='sec\ret"1'
  DOWNLOAD_URL="https://example.com/gost"

  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() { :; }
  # R4B11-003: 安装路径判活改走 flmx_agent_health_ok 双采样后，
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

  ( install_flmx_agent >/dev/null 2>/dev/null ) || true

  local actual
  actual=$(<"$INSTALL_DIR/config.json")
  local expected=$'{\n  "addr": "panel\\"addr",\n  "secret": "sec\\\\ret\\"1"\n}'

  assert_equals "$expected" "$actual" "install_flmx_agent should JSON-escape config values"
  grep -Fqx 'Environment=GODEBUG=disablethp=1' "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" || \
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

test_install_flmx_agent_uses_openrc() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local temp_root
  temp_root=$(mktemp -d)
  INSTALL_DIR="$temp_root/flmx_agent"
  FLMX_AGENT_OPENRC_SERVICE_FILE="$temp_root/init.d/flmx_agent"
  # R4B11-003: 安装判活改双采样后，OpenRC 侧主 PID 取自 pidfile，
  # 须预置恒定 PID 供两次采样比对。
  FLMX_AGENT_OPENRC_PIDFILE="$temp_root/flmx_agent.pid"
  echo 4242 > "$FLMX_AGENT_OPENRC_PIDFILE"
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

  install_flmx_agent >/dev/null

  [[ -x "$FLMX_AGENT_OPENRC_SERVICE_FILE" ]] || fail "OpenRC service file should be executable"
  grep -Fq '#!/sbin/openrc-run' "$FLMX_AGENT_OPENRC_SERVICE_FILE" || \
    fail "OpenRC service should use openrc-run"
  grep -Fq "command=\"$INSTALL_DIR/flmx_agent\"" "$FLMX_AGENT_OPENRC_SERVICE_FILE" || \
    fail "OpenRC service should launch the installed flmx_agent binary"
  grep -Fq 'command_background="yes"' "$FLMX_AGENT_OPENRC_SERVICE_FILE" || \
    fail "OpenRC service should run flmx_agent in the background"
  if command -v openrc-run >/dev/null 2>&1; then
    "$FLMX_AGENT_OPENRC_SERVICE_FILE" describe >/dev/null 2>&1
  fi
  [[ "$rc_update_calls" == *"add flmx_agent default"* ]] || \
    fail "OpenRC install should enable flmx_agent in the default runlevel"
  [[ "$rc_service_calls" == *"start"* ]] || fail "OpenRC install should start flmx_agent"
  [[ "$rc_service_calls" == *"status"* ]] || fail "OpenRC install should verify flmx_agent status"
)

test_remove_flmx_agent_service_uses_openrc() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local temp_root
  temp_root=$(mktemp -d)
  SERVICE_MANAGER="openrc"
  FLMX_AGENT_OPENRC_SERVICE_FILE="$temp_root/init.d/flmx_agent"
  mkdir -p "$(dirname "$FLMX_AGENT_OPENRC_SERVICE_FILE")"
  : > "$FLMX_AGENT_OPENRC_SERVICE_FILE"

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

  stop_flmx_agent_service
  disable_flmx_agent_service
  remove_flmx_agent_service

  [[ "$rc_service_calls" == *"stop"* ]] || fail "OpenRC uninstall should stop flmx_agent"
  [[ "$rc_update_calls" == *"del flmx_agent default"* ]] || \
    fail "OpenRC uninstall should remove flmx_agent from the default runlevel"
  [[ ! -e "$FLMX_AGENT_OPENRC_SERVICE_FILE" ]] || fail "OpenRC uninstall should remove its service file"
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

  # 本夹具的 ExecStart 指向配置目录下的 gost 二进制（指纹命中
  # 形态）；LEGACY_GOST_BINARY（真实部署中即 /usr/local/bin/
  # gost）未被任何被删 unit 引用——旧实现按分支整集连带删除、
  # 宽于指纹命中形态（C4 族六立案），现只删被引用者、未引用
  # 者保留并在回执点名。
  if [[ ! -e "$LEGACY_GOST_BINARY" ]]; then
    fail "cleanup_legacy_gost_installation must preserve the unreferenced binary (only referenced binaries are removed)"
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
  printf 'x' > "$LEGACY_GOST_CONFIG_DIR/gost"

  systemctl() {
    if [[ "$1" == "list-units" ]]; then
      printf 'gost.service loaded active running\n'
    fi
    return 0
  }

  local out
  out=$(FLMX_AGENT_TAKEOVER="1" cleanup_legacy_gost_installation </dev/null 2>&1)

  # 接管只放行归属门，不放宽指纹面：本夹具 ExecStart 指向配置目录
  # 下的 gost 二进制，/usr/local/bin/gost 未被任何被删 unit 引用
  # ——族六 legacy过宽已定按指纹删，未引用者保留并点名（与
  # removes_service_and_binary 夹具同口径；旧断言按分支整集删，
  # 正是该条立案的旧行为）。
  [[ -e "$LEGACY_GOST_BINARY" ]] || fail "C4-F01: takeover cleanup must preserve the unreferenced binary (only referenced binaries are removed)"
  [[ ! -e "$LEGACY_GOST_CONFIG_DIR/gost" ]] || fail "C4-F01: takeover should let cleanup remove the referenced config-dir binary"
  [[ ! -e "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "C4-F01: takeover should let cleanup remove the legacy service file"
  [[ "$out" == *"FLMX_AGENT_TAKEOVER=1"* ]] || fail "C4-F01: takeover cleanup must leave a trace line"
  [[ "$out" == *"已保留未删"* ]] || fail "C4-F01: preserved unreferenced binary must be named in the output"
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
  printf 'x' > "$LEGACY_GOST_CONFIG_DIR/gost"

  systemctl() {
    if [[ "$1" == "list-units" ]]; then
      printf 'gost.service loaded active running\n'
    fi
    return 0
  }

  cleanup_legacy_gost_installation </dev/null >/dev/null

  # 与 takeover 夹具同口径：ExecStart 指向配置目录下的 gost，
  # /usr/local/bin/gost 未被引用，按族六 legacy过宽指纹面保留
  # （旧断言按分支整集删，正是该条立案的旧行为）。
  [[ -e "$LEGACY_GOST_BINARY" ]] || fail "C4-F01: unreferenced binary must be preserved even when the legacy install matches the installed agent addr"
  [[ ! -e "$LEGACY_GOST_CONFIG_DIR/gost" ]] || fail "C4-F01: referenced config-dir binary should be cleaned when it matches the installed agent addr"
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
test_uninstall_flmx_agent_host_cleanup_preserves_foreign_legacy_resources() (
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
  FLMX_TUNING_KERNEL_CONF="$work/sysctl.d/99-network-performance.conf"
  FLMX_TUNING_LIMITS_CONF="$work/security/limits.d/99-network-performance.conf"
  FLMX_TUNING_BBR_CONF="$work/sysctl.d/10-bbr.conf"
  FLMX_TUNING_GAI_CONF="$work/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$work/gai.conf.bak"
  FLMX_TUNING_RPS_UNIT_FILE="$work/systemd/flmx-tuning-rps.service"
  FLMX_TUNING_RPS_SCRIPT="$work/bin/flmx-tuning-rps.sh"
  FLMX_PROC_SYS_DIR="$work/proc/sys"
  LEGACY_GOST_CONFIG_DIR="$work/gost"
  LEGACY_GOST_BINARY="$work/bin/gost"
  LEGACY_GOST_SERVICE_FILE_ETC="$work/systemd/gost.service"
  LEGACY_GOST_SERVICE_FILE_LIB="$work/no-lib-service"
  LEGACY_GOST_SERVICE_FILE_USR_LIB="$work/no-usr-lib-service"
  mkdir -p "$INSTALL_DIR" "$(dirname "$FLMX_TUNING_SYSCTL_CONF")" "$LEGACY_GOST_CONFIG_DIR" "$(dirname "$LEGACY_GOST_SERVICE_FILE_ETC")"
  printf 'binary' > "$INSTALL_DIR/flmx_agent"
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
  out=$(uninstall_flmx_agent_host_cleanup 2>&1)

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
  out=$(uninstall_flmx_agent_host_cleanup 2>&1)

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

  panel_pin_missing_resource_keys() {
    calls+=" pin"
  }

  activate_panel_update_files() {
    calls+=" activate"
  }

  start_panel_after_update() {
    calls+=" start"
  }

  verify_panel_update_sqlite_volume() {
    calls+=" verify"
  }

  check_ipv6_support() { return 1; }

  update_panel >/dev/null

  assert_equals \
    "https://github.com/${REPO}/releases/download/v-test/docker-compose-v4.yml" \
    "$DOCKER_COMPOSEV4_URL" \
    "update_panel should build the canonical compose URL"
  assert_equals \
    " resolve validate-current download validate-new backup pull pin activate start verify" \
    "$calls" \
    "update_panel should validate, back up, pull, and pin resource keys before activating the update, then verify the mounted volume after starting"
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
test_install_flmx_agent_keeps_service_when_download_fails() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  printf '#!/bin/bash\necho "old version"\n' > "$INSTALL_DIR/flmx_agent"
  chmod +x "$INSTALL_DIR/flmx_agent"
  SERVER_ADDR="panel.example.com:443"
  SECRET="<redacted>"
  DOWNLOAD_URL="https://example.com/gost"

  # 调用记录走文件：install_flmx_agent 在子 shell 里跑，变量赋值传不回来
  local calls_log
  calls_log=$(mktemp)
  ask_proxy_config() { :; }
  ensure_download_url_initialized() { :; }
  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  ensure_service_manager() { return 0; }
  cleanup_legacy_gost_installation() { :; }
  flmx_agent_service_exists() { return 0; }
  stop_flmx_agent_service() { echo stop >> "$calls_log"; }
  disable_flmx_agent_service() { echo disable >> "$calls_log"; }
  curl() { return 0; }

  local rc="0"
  ( install_flmx_agent >/dev/null ) || rc="$?"

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
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

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
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

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

# FX-D（R1-12-01）: 新装完成输出必须打印后端播种的一次性初始
# 管理员口令（经后端容器实读数据卷→卷挂载点读
# initial-admin-password.txt），且绝不再打印旧公开字面量口令；
# 文件读不到时只给文件位置与 FLMX_ADMIN_PASSWORD 查法。
test_install_panel_prints_generated_initial_admin_password() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

  local work volmount
  work=$(mktemp -d)
  volmount=$(mktemp -d)
  printf 'Xk9TestInitialPw123456\n' > "$volmount/initial-admin-password.txt"

  ask_proxy_config() { :; }
  ensure_compose_urls_initialized() {
    DOCKER_COMPOSEV4_URL="https://example.com/docker-compose-v4.yml"
    DOCKER_COMPOSEV6_URL="https://example.com/docker-compose-v6.yml"
    return 0
  }
  check_docker() { DOCKER_CMD="docker compose"; return 0; }
  get_config_params() {
    FRONTEND_PORT=6366; BACKEND_PORT=6365; RESOLVED_VERSION="9.9.9"
    DB_TYPE="sqlite"; DATABASE_URL=""; POSTGRES_DB="flux_panel"; POSTGRES_USER="flux_panel"
    POSTGRES_PASSWORD="test-postgres-password"
    # 真 get_config_params 恒无条件赋 JWT_SECRET，桩须同契约：缺此赋值时 .env heredoc 在 set -u 下展开失败——旧无守卫实现静默跳过写盘仍报成功（heredoc 无守卫本条），守卫化后如实失败，故补齐桩而非放宽守卫。
    JWT_SECRET="test-jwt-secret"
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
      *" volume inspect "*) printf '%s' "$FXD_TEST_VOLMOUNT" ;;
      *" inspect "*) printf 'flmx-sqlite-data' ;;
    esac
    return 0
  }

  # 第一相：一次性文件可读——完成输出必须含随机口令本体、点名
  # 文件位置，且不得出现旧字面量口令对。
  export FXD_TEST_VOLMOUNT="$volmount"
  local rc="0" out_log
  out_log="$work/with-file.log"
  ( cd "$work" && install_panel >"$out_log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "FX-D: install_panel should succeed with a readable initial password file"
  grep -q "Xk9TestInitialPw123456" "$out_log" || \
    fail "FX-D: completion output must print the generated initial admin password read from the data volume"
  grep -q "initial-admin-password.txt" "$out_log" || \
    fail "FX-D: completion output must name the one-time initial password file location"
  if grep -q "admin@user / admin@user" "$out_log"; then
    fail "FX-D: completion output must never print the old public literal password"
  fi

  # 第二相：一次性文件缺失（环境指定路径）——只给查法指引，
  # 仍不得出现旧字面量口令对。
  local work2 volmount2
  work2=$(mktemp -d)
  volmount2=$(mktemp -d)
  export FXD_TEST_VOLMOUNT="$volmount2"
  rc="0"
  out_log="$work2/no-file.log"
  ( cd "$work2" && install_panel >"$out_log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "FX-D: install_panel should succeed when no initial password file exists"
  grep -q "FLMX_ADMIN_PASSWORD" "$out_log" || \
    fail "FX-D: without the one-time file the completion output must point at the FLMX_ADMIN_PASSWORD route"
  grep -q "initial-admin-password.txt" "$out_log" || \
    fail "FX-D: without the one-time file the completion output must still name the file location"
  if grep -q "admin@user / admin@user" "$out_log"; then
    fail "FX-D: fallback guidance must never print the old public literal password"
  fi
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
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

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
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

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
  local dest errlog rc=0
  dest=$(mktemp -u)
  errlog=$(mktemp)
  # A3-03: 第二锚改 fail-closed（S-02 口径：无降级跳过）。获胜线
  # 路是第三方代理、异路全部给不出合法校验值时必须拒绝安装且二
  # 进制不得落地——旧实现在此降级单锚放行，本用例旧预期（仍落
  # 地）随修复翻转为拒绝。
  download_agent_binary "$dest" 2>"$errlog" || rc=$?
  assert_equals "1" "$rc" "A3-03: download must fail when a proxy-served binary cannot be corroborated"
  [[ ! -f "$dest" ]] || fail "A3-03: binary must not land when the second anchor cannot corroborate"
  grep -q "第二锚 fail-closed" "$errlog" || fail "A3-03: the fail-closed refusal must be named on stderr"

  # 保留点名降级的两类形态：① 直连（规范源）当选、代理无应答 →
  # 点名单锚落地；② 运维显式单路（PROXY_URL 覆盖）无异路可复核
  # → 点名单锚落地。
  bin_anchor_fixture_setup
  BIN_ANCHOR_B_MODE="fail"
  DOWNLOAD_ROUTES=("" "https://b.example")
  dest=$(mktemp)
  errlog=$(mktemp)
  download_agent_binary "$dest" 2>"$errlog"
  assert_equals "$BIN_ANCHOR_BODY" "$(cat "$dest")" "A3-03: direct-sourced binary should still land with a named single-anchor degradation"
  grep -q "跨线路锚复核已降级" "$errlog" || fail "A3-03: direct-source degradation must be named on stderr"

  bin_anchor_fixture_setup
  BIN_ANCHOR_B_MODE="fail"
  PROXY_URL="https://a.example"
  DOWNLOAD_ROUTES=("https://a.example")
  dest=$(mktemp)
  errlog=$(mktemp)
  download_agent_binary "$dest" 2>"$errlog"
  assert_equals "$BIN_ANCHOR_BODY" "$(cat "$dest")" "A3-03: operator-pinned single route should still land with a named single-anchor degradation"
  grep -q "跨线路锚复核已降级" "$errlog" || fail "A3-03: single-route degradation must be named on stderr"
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
  grep -q "FLMX_VERSION=1.3.3" .env \
    || fail "activate_panel_update_files must bump FLMX_VERSION"
  grep -q "FLUX_VERSION=1.3.2" .env \
    || fail "activate_panel_update_files must leave the retired FLUX_VERSION key untouched (S1-F02)"
  if grep -q "FLUX_VERSION=1.3.3" .env; then
    fail "activate_panel_update_files must not bump the retired FLUX_VERSION key"
  fi
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
      # HF-200-01：模拟真实 compose 对无默认值变量的插值——文件中引用
      # ${BACKEND_PORT}/${FRONTEND_PORT} 且环境值为空时 config 必失败
      #（真实 compose 会把端口映射展开为空主机端口并判类型非法）。
      if grep -q '\${BACKEND_PORT}' "$file" && [[ -z "${BACKEND_PORT:-}" ]]; then
        return 1
      fi
      if grep -q '\${FRONTEND_PORT}' "$file" && [[ -z "${FRONTEND_PORT:-}" ]]; then
        return 1
      fi
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

  # HF-200-01：模拟新装现场——端口/密钥变量未导出、目录内尚无 .env，
  # 随版模板仍必须通过校验（2.0.0 曾在此形状下误判 ports 类型非法）。
  unset BACKEND_PORT FRONTEND_PORT JWT_SECRET || true
  local work
  work=$(mktemp -d)
  cd "$work"

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
  printf 'FLMX_VERSION=1.0.0\nJWT_SECRET=x\n' > "$deploy_dir/.env"
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

# R2X-019 (R2-J-01): 真实函数级回归——install_flmx_agent 本体实跑
# （下载/校验/写服务/启服务全走真实代码，curl/systemctl 为命令级桩），
# 仅桩 flmx_agent_service_is_active 决定判活结果：为假时函数必须
# return 1 且屏显失败；为真时必须 return 0。禁止再以整函数桩代替
# 被测对象（J 报告 §3：X-P3-6 的整函数桩正是本缺陷的漏网原因）。
test_install_flmx_agent_returns_nonzero_when_service_not_active() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
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
  flmx_agent_service_is_active() { return 1; }

  local rc="0"
  ( install_flmx_agent >"$INSTALL_DIR/fail.log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R2X-019: install must return 1 when the service fails to start"
  grep -q "服务启动失败" "$INSTALL_DIR/fail.log" || \
    fail "R2X-019: install should still print the startup failure hint"

  # 判活成功（防过修）：同路径必须 return 0。
  # R4B11-003: 判活改双采样后还须主 PID 恒定非 0，一并覆写采样源
  # （本例 systemctl 桩不答 MainPID）。
  flmx_agent_service_is_active() { return 0; }
  flmx_agent_main_pid() { echo 4242; }

  rc="0"
  ( install_flmx_agent >"$INSTALL_DIR/ok.log" 2>&1 ) || rc="$?"
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

  start_flmx_agent_service
  assert_equals "reset-failed flmx_agent" "$(sed -n '1p' "$CALLS_LOG")" \
    "R2X-020: start_flmx_agent_service should reset-failed before start"
  assert_equals "start flmx_agent" "$(sed -n '2p' "$CALLS_LOG")" \
    "R2X-020: start_flmx_agent_service should start after reset-failed"

  : > "$CALLS_LOG"
  enable_and_start_flmx_agent_service
  local reset_line start_line
  reset_line=$(awk '$0=="reset-failed flmx_agent"{print NR; exit}' "$CALLS_LOG")
  start_line=$(awk '$0=="start flmx_agent"{print NR; exit}' "$CALLS_LOG")
  [[ -n "$reset_line" && -n "$start_line" && "$reset_line" -lt "$start_line" ]] || \
    fail "R2X-020: enable_and_start should reset-failed before start (calls: $(cat "$CALLS_LOG"))"

  # OpenRC 分支不动：不得调用 systemctl，rc-service 照常 start
  SERVICE_MANAGER="openrc"
  : > "$CALLS_LOG"
  rc-service() { echo "$*" >> "$RC_CALLS_LOG"; return 0; }
  rc-update() { echo "$*" >> "$RC_CALLS_LOG"; return 0; }
  start_flmx_agent_service
  enable_and_start_flmx_agent_service
  [[ ! -s "$CALLS_LOG" ]] || \
    fail "R2X-020: OpenRC starts must not call systemctl/reset-failed"
  grep -q "flmx_agent start" "$RC_CALLS_LOG" || \
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
  printf '#!/bin/bash\necho "bad flap"\n' > "$INSTALL_DIR/flmx_agent"
  printf '#!/bin/bash\necho "old good"\n' > "$INSTALL_DIR/flmx_agent.bak"
  chmod +x "$INSTALL_DIR/flmx_agent" "$INSTALL_DIR/flmx_agent.bak"

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
  rollback_flmx_agent >"$work/rollback.log" 2>&1 || true

  flmx_agent_service_is_active || \
    fail "R2X-020: rollback should bring the service back active from the failed state"
  grep -q "已回滚到旧版本并重启成功" "$work/rollback.log" || \
    fail "R2X-020: rollback should report a successful restart (log: $(cat "$work/rollback.log"))"
  grep -q "old good" "$INSTALL_DIR/flmx_agent" || \
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
      *.sha256*)
        # R2-01-01：第二锚已 fail-closed——获胜线路是代理时异路
        # 必须真给出合法校验值，直连候选在此供出空本体（桩
        # download_verified 不写盘，dest 为空文件）的哈希供复核
        # 通过；否则复核按新口径拒绝、本用例的 failover 断言无
        # 从谈起。-o 目标从参数中提取。
        local _out="" _prev=""
        for _a in "$@"; do
          [[ "$_prev" == "-o" ]] && _out="$_a"
          _prev="$_a"
        done
        printf '%s  asset.yml.sha256\n' "$(printf '' | sha256sum | awk '{print $1}')" > "$_out"
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
  local dest errlog rc=0
  dest=$(mktemp)
  errlog=$(mktemp)
  # R2-01-01: 第二锚与 install.sh 的 A3-03 同口径分层收口——获胜
  # 线路是第三方代理、异路全部给不出合法校验值时必须拒绝下载且
  # 文件不得落地：单个代理喂自洽（资产，.sha256）对正是第二锚
  # 要防的形态，旧实现在此无条件降级单锚放行，本用例旧预期
  # （仍落地）随修复翻转为拒绝。
  download_verified_routed "https://github.com/example/asset.yml" "$dest" 2>"$errlog" || rc=$?
  assert_equals "1" "$rc" "R2-01-01: routed download must fail when a proxy-served asset cannot be corroborated"
  [[ ! -f "$dest" ]] || fail "R2-01-01: file must not land when the second anchor cannot corroborate"
  grep -q "第二锚 fail-closed" "$errlog" || fail "R2-01-01: the fail-closed refusal must be named on stderr"

  # 保留点名降级的两类形态：① 直连（规范源）当选、异路无应答
  # → 点名单锚落地；② 路线表只有一条运维指定路线、无异路可复
  # 核 → 点名单锚落地。
  anchor_fixture_setup
  ANCHOR_SHA_B_FAIL="1"
  DOWNLOAD_ROUTES=("" "https://b.example")
  dest=$(mktemp)
  errlog=$(mktemp)
  download_verified_routed "https://github.com/example/asset.yml" "$dest" 2>"$errlog"
  assert_equals "$ANCHOR_BODY" "$(cat "$dest")" "R2-01-01: direct-sourced asset should still land with a named single-anchor degradation"
  grep -q "跨线路锚复核已降级" "$errlog" || fail "R2-01-01: direct-source degradation must be named on stderr"

  anchor_fixture_setup
  DOWNLOAD_ROUTES=("https://a.example")
  dest=$(mktemp)
  errlog=$(mktemp)
  download_verified_routed "https://github.com/example/asset.yml" "$dest" 2>"$errlog"
  assert_equals "$ANCHOR_BODY" "$(cat "$dest")" "R2-01-01: operator-pinned single route should still land with a named single-anchor degradation"
  grep -q "跨线路锚复核已降级" "$errlog" || fail "R2-01-01: single-route degradation must be named on stderr"
)

# GARBAGE-AUDIT A1：升级收尾按升级前版本 tag 定点删除旧
# backend/frontend 镜像。删除面只许本面板前缀下、现行名＋旧仓
# 名形态、tag 恰为旧版本；现行版本/latest/空/非法格式一律不
# 动；在场的删、不在场的不调 rmi；单条 rmi 失败只告警、函数
# 仍返回 0 且不中断其余删除（不回滚升级的口径在脚本层先钉住
# 「失败不放大」）。
test_cleanup_old_panel_images_after_update_targets_only_old_tag() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local rmi_calls=""
  local fail_rmi_for=""
  docker() {
    if [[ "$1" == "image" && "$2" == "inspect" ]]; then
      # flvx 旧仓名形态假定不在场，其余三名在场。
      [[ "$3" != *"flvx-flux-panel-backend"* ]]
      return
    fi
    if [[ "$1" == "rmi" ]]; then
      rmi_calls+=" $2"
      [[ -z "$fail_rmi_for" || "$2" != *"$fail_rmi_for"* ]]
      return
    fi
    return 1
  }

  local prefix
  prefix=$(panel_image_prefix)

  cleanup_old_panel_images_after_update "1.9.9" "2.0.0"
  case "$rmi_calls" in
    *"${prefix}flmx-panel-backend:1.9.9"*) ;;
    *) fail "GARBAGE-A1: current-name backend old tag must be removed (got:$rmi_calls)" ;;
  esac
  case "$rmi_calls" in
    *"${prefix}flmx-flux-panel-backend:1.9.9"*) ;;
    *) fail "GARBAGE-A1: legacy-repo backend old tag must be removed (got:$rmi_calls)" ;;
  esac
  case "$rmi_calls" in
    *"${prefix}flmx-vite-frontend:1.9.9"*) ;;
    *) fail "GARBAGE-A1: frontend old tag must be removed (got:$rmi_calls)" ;;
  esac
  case "$rmi_calls" in
    *":2.0.0"*|*":latest"*|*"flvx-flux-panel-backend"*)
      fail "GARBAGE-A1: cleanup must never target the new version, latest, or absent images (got:$rmi_calls)" ;;
  esac

  # rmi 失败只告警：函数返回 0、其余镜像照常尝试删除。
  rmi_calls=""
  fail_rmi_for="flmx-vite-frontend"
  cleanup_old_panel_images_after_update "1.9.9" "2.0.0"
  case "$rmi_calls" in
    *"${prefix}flmx-panel-backend:1.9.9"*"${prefix}flmx-vite-frontend:1.9.9"*) ;;
    *) fail "GARBAGE-A1: one failed rmi must not stop the remaining targeted removals (got:$rmi_calls)" ;;
  esac

  # 同版本重装、latest、空值与非法格式：整步跳过，零 rmi。
  rmi_calls=""
  fail_rmi_for=""
  cleanup_old_panel_images_after_update "2.0.0" "2.0.0"
  cleanup_old_panel_images_after_update "latest" "2.0.0"
  cleanup_old_panel_images_after_update "" "2.0.0"
  cleanup_old_panel_images_after_update '1.9.9";evil' "2.0.0"
  assert_equals "" "$rmi_calls" \
    "GARBAGE-A1: same-version/latest/empty/malformed old tags must trigger no removal"
)

# GARBAGE-AUDIT A2：OpenRC 安装随服务文件落一份 logrotate 配置
# 给两份 append 日志封顶（1M×3、copytruncate），卸载一并删除。
test_openrc_install_writes_logrotate_config() (
  set -euo pipefail
  local work
  work=$(mktemp -d)
  SERVICE_MANAGER="openrc"
  load_script_without_main "$ROOT_DIR/install.sh"
  FLMX_AGENT_OPENRC_SERVICE_FILE="$work/flmx.initd"
  FLMX_AGENT_LOGROTATE_FILE="$work/logrotate.d/flmx_agent"
  write_flmx_agent_service
  [[ -f "$work/flmx.initd" ]] || fail "GARBAGE-A2: OpenRC service file must be written"
  grep -Fq '/var/log/flmx_agent.log /var/log/flmx_agent.err.log' "$FLMX_AGENT_LOGROTATE_FILE" || \
    fail "GARBAGE-A2: OpenRC install must drop a logrotate config covering both agent logs"
  grep -Fq 'size 1M' "$FLMX_AGENT_LOGROTATE_FILE" || \
    fail "GARBAGE-A2: logrotate config must cap the logs at 1M"
  grep -Fq 'copytruncate' "$FLMX_AGENT_LOGROTATE_FILE" || \
    fail "GARBAGE-A2: logrotate config must use copytruncate (OpenRC holds the log fds)"
  remove_flmx_agent_service
  [[ ! -e "$FLMX_AGENT_LOGROTATE_FILE" ]] || \
    fail "GARBAGE-A2: uninstall must remove the logrotate config"
  [[ ! -e "$work/flmx.initd" ]] || \
    fail "GARBAGE-A2: uninstall must remove the OpenRC service file"
)

test_preserve_compose_networks_keeps_existing_subnet
test_preserve_compose_networks_no_subnet_existing
test_activate_panel_update_files_preserves_subnet
test_cleanup_old_panel_images_after_update_targets_only_old_tag
test_openrc_install_writes_logrotate_config
test_get_deployed_panel_version_parses_image_tag
test_update_panel_crosschecks_deployed_image_version
test_install_flmx_agent_keeps_service_when_download_fails
test_install_panel_refuses_existing_deployment
test_install_panel_waits_for_backend_health
test_install_panel_prints_generated_initial_admin_password
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
# R2-01-02（audit-max 复审）：setup_domain_proxy 的 Caddy 官方源两
# 抓取（gpg.key / debian.deb.txt）此前是裸 curl -1sLf——无连接超
# 时、无总界、无速度看门狗：dl.cloudsmith.io 连接被黑洞（TCP 已
# 开、零字节）时菜单 5/6 无限挂死，函数内设计好的 Debian 仓库回
# 退只在「失败」时触发、「停滞」永不达（A1-02 同族漏网——A1-02
# 的行为钉只守 run_verified_installer_script 一条腿）。静态钉格
# （与 A1-06 同因：菜单全流程夹具须驱动 apt/gpg/systemd 全面，
# 不可行）：① 全仓 panel_install.sh 内 cloudsmith 抓取行（恰两
# 条）各自带齐看门狗四件；② 字面 URL 抓取普查——两脚本中非注
# 释、非回显的 curl 字面 URL 行必须同行带 --max-time，防同族再
# 从别的抓取点漏网。
test_panel_caddy_source_fetches_use_timeouts() (
  set -euo pipefail
  local line bad script
  local fetch_lines
  fetch_lines=$(grep 'curl' "$ROOT_DIR/panel_install.sh" | grep -c 'dl\.cloudsmith\.io' || true)
  [[ "$fetch_lines" -eq 2 ]] || fail "R2-01-02: panel_install.sh must contain exactly the two cloudsmith fetches (found $fetch_lines)"
  while IFS= read -r line; do
    grep -q -- '--connect-timeout' <<< "$line" || fail "R2-01-02: cloudsmith fetch lacks --connect-timeout: $line"
    grep -q -- '--max-time' <<< "$line" || fail "R2-01-02: cloudsmith fetch lacks --max-time: $line"
    grep -q -- '--speed-limit' <<< "$line" || fail "R2-01-02: cloudsmith fetch lacks the stall watchdog (--speed-limit): $line"
    grep -q -- '--speed-time' <<< "$line" || fail "R2-01-02: cloudsmith fetch lacks the stall watchdog (--speed-time): $line"
  done < <(grep 'curl' "$ROOT_DIR/panel_install.sh" | grep 'dl\.cloudsmith\.io')

  for script in panel_install.sh install.sh; do
    bad=$(grep -nE '(^|[[:space:]])curl[[:space:]]' "$ROOT_DIR/$script" \
      | grep -E 'https?://' \
      | grep -vE '^[0-9]+:[[:space:]]*#' \
      | grep -vE '^[0-9]+:[[:space:]]*echo[[:space:]]' \
      | grep -v -- '--max-time' || true)
    [[ -z "$bad" ]] || fail "R2-01-02: literal-URL curl fetch without --max-time in $script: $bad"
  done
)

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
test_install_flmx_agent_rejects_flapping_service() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
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
  flmx_agent_service_is_active() {
    local n
    n=$(cat "$STATE_DIR/active_cnt"); n=$((n+1)); echo "$n" > "$STATE_DIR/active_cnt"
    [[ "$n" -le 1 ]]
  }

  local rc="0" log="$INSTALL_DIR/install.log"
  ( install_flmx_agent >"$log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R4B11-003: install must fail when the service flaps after start"
  if grep -q "安装完成" "$log"; then
    fail "R4B11-003: install must not print completion for a flapping service"
  fi

  flmx_agent_service_is_active() { return 0; }
  rc="0"
  ( install_flmx_agent >"$log" 2>&1 ) || rc="$?"
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

  file=$(build_case del-fail 'install_flmx_agent() { return 1; }')
  rc="0"; out=$(printf '1\n' | bash "$file" 2>&1) || rc="$?"
  assert_equals "1" "$rc" "R4B11-005: failed install should exit non-zero"
  [[ -f "$file" ]] || fail "R4B11-005: script must be kept after a failed operation"
  if [[ "$out" == *"操作已完成"* ]]; then
    fail "R4B11-005: failure path must not print 操作已完成"
  fi
  [[ "$out" == *"已保留"* ]] || \
    fail "R4B11-005: failure path should say the script was kept for retry"

  file=$(build_case del-ok 'install_flmx_agent() { return 0; }')
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

# R2-F7-01（R2-F7-B01）: install.sh 卸载取消路径以真实
# uninstall_flmx_agent 钉格——不覆写被测函数本体，只桩其前置
# （root 检查、归属守卫、服务管理器探测）。菜单选 3、确认答 N：
# 退出 0、脚本文件保留、无「操作已完成」、输出含取消说明，与
# panel 侧 C4-F04 取消口径一致。此前 install.sh 侧只有覆写操作函
# 数的 rc 传播用例（test_main_delete_self_only_on_success），真实
# 取消路径（return 0 且无标志）无覆盖：main 把取消当成功自删并
# 谎报完成（本用例对旧代码必红：脚本被删且出现完成文案）。
test_install_uninstall_cancel_keeps_script() (
  set -euo pipefail

  local work
  work=$(mktemp -d)

  local file="$work/cancel.sh"
  sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$file"
  cat >> "$file" <<'STUBEOF'
require_root_privileges() { return 0; }
guard_agent_dir_ownership_menu() { return 0; }
ensure_service_manager() { return 0; }
main
STUBEOF

  local rc="0" out
  out=$(printf '3\nN\n' | bash "$file" 2>&1) || rc="$?"
  assert_equals "0" "$rc" "R2-F7-01: cancelled uninstall should exit 0"
  [[ -f "$file" ]] || fail "R2-F7-01: script must be kept when the user cancels uninstall"
  [[ "$out" == *"取消卸载"* ]] || \
    fail "R2-F7-01: cancel path should print the cancellation line"
  if [[ "$out" == *"操作已完成"* ]]; then
    fail "R2-F7-01: cancel path must not print 操作已完成"
  fi
)

# R2-F7-I02: 卸载清理 FLMX_KERNEL_DIR 自定义内核目录。值只认 agent
# 自己的 systemd 单元/drop-in（卸载入口已过归属守卫）；正例（drop-in
# 配置、内含 state.json）随卸载删除且确认提示点名；拒绝形态（指向
# 系统目录）不删、点名披露、卸载本身仍成功。
test_uninstall_removes_custom_kernel_dir() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  systemctl() { return 0; }
  guard_agent_dir_ownership_menu() { return 0; }
  ensure_service_manager() { return 0; }
  flmx_agent_service_exists() { return 1; }
  uninstall_flmx_agent_host_cleanup() { :; }
  SERVICE_MANAGER="systemd"
  SERVER_ADDR=""

  local work
  work=$(mktemp -d)

  # 正例：drop-in 配置自定义目录且内含 state.json → 随卸载删除
  INSTALL_DIR="$work/agent"
  mkdir -p "$INSTALL_DIR"
  printf 'binary' > "$INSTALL_DIR/flmx_agent"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$work/flmx_agent.service"
  printf '[Service]\nExecStart=%s/flmx_agent\n' "$INSTALL_DIR" > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
  mkdir -p "${FLMX_AGENT_SYSTEMD_SERVICE_FILE}.d"
  local kdir="$work/custom-kernels"
  mkdir -p "$kdir"
  printf '{}' > "$kdir/state.json"
  printf '[Service]\nEnvironment=FLMX_KERNEL_DIR=%s\n' "$kdir" > "${FLMX_AGENT_SYSTEMD_SERVICE_FILE}.d/override.conf"

  local rc="0" out
  out=$(mktemp)
  rc="0"
  printf 'y\n' | uninstall_flmx_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R2-F7-I02: uninstall with a custom kernel dir should succeed"
  [[ ! -d "$INSTALL_DIR" ]] || fail "R2-F7-I02: install dir should be removed"
  [[ ! -d "$kdir" ]] || fail "R2-F7-I02: custom kernel dir should be removed on uninstall"
  grep -q "custom-kernels" "$out" || \
    fail "R2-F7-I02: uninstall output should name the custom kernel dir"

  # 拒绝形态：单元内指向系统目录 → 不删、点名披露、卸载仍成功
  INSTALL_DIR="$work/agent2"
  mkdir -p "$INSTALL_DIR"
  printf 'binary' > "$INSTALL_DIR/flmx_agent"
  rm -rf "${FLMX_AGENT_SYSTEMD_SERVICE_FILE}.d"
  printf '[Service]\nEnvironment=FLMX_KERNEL_DIR=/etc\n' > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
  rc="0"
  printf 'y\n' | uninstall_flmx_agent >"$out" 2>&1 || rc="$?"
  assert_equals "0" "$rc" "R2-F7-I02: uninstall should still succeed when the kernel dir value is refused"
  grep -q "不自动清理" "$out" || \
    fail "R2-F7-I02: refused kernel dir value should be disclosed, not silently skipped"
)

# R2-F7-02: 首装 get_config_params 校验失败（非法端口）必须在下载与
# 写 .env 之前终止 install_panel。夹具保留真实 get_config_params、
# 其余前置/后续全桩：download_verified 落标记、wait 健康恒过——旧
# 实现裸调用校验函数，失败后继续下载并写出 .env、最终报成功（本
# 用例对旧代码必红：标记与 .env 都会出现且 rc=0）。
test_install_panel_invalid_port_aborts_before_download() (
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
  download_verified() {
    touch "$work/download-called"
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
  wait_for_backend_healthy() { return 0; }

  ( cd "$work" && install_panel >"$work/fail.log" 2>&1 ) <<< "abc" || rc="$?"
  assert_equals "1" "$rc" "R2-F7-02: install_panel must abort when the port validation fails"
  grep -q "FRONTEND_PORT 不是有效端口" "$work/fail.log" || \
    fail "R2-F7-02: the port validation error should be printed at the input point"
  [[ ! -f "$work/download-called" ]] || \
    fail "R2-F7-02: install must not proceed to downloads after a failed validation"
  [[ ! -f "$work/.env" ]] || \
    fail "R2-F7-02: install must not write .env after a failed validation"
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

# RR-LIVE-01: 迁移预处理副本必须删掉毒死 pgloader 的 user 表函数唯
# 一索引 idx_user_username_fold_unique（表达式列 name 为 NULL、
# pgloader 解析得 NIL 即 KABOOM），且连带删除 schema_migration 里
# 的 v5 版本记录 k10_v5_username_casefold_unique——否则 PG 首启时
# 后端 migrateV5UsernameCasefoldUnique 命中记录短路、被删的函数索
# 引无人重建。本用例从出厂 panel_install.sh 抽出预处理 docker 载荷
# 原文（路径改写到夹具目录、apk 行以宿主 sqlite3 代替）在样本库上
# 实际执行，断言副本双删生效且其余内容无损、源库未动。摘掉脚本里
# 删索引行或删记录行任一行，本用例必红。
test_panel_migrate_preprocess_drops_fold_index_and_v5_record() (
  set -euo pipefail
  command -v sqlite3 >/dev/null 2>&1 || \
    fail "RR-LIVE-01: sqlite3 is required to execute the migration preprocessing payload"

  local work
  work=$(mktemp -d)
  mkdir -p "$work/sqlite"

  sqlite3 "$work/sqlite/gost.db" <<'EOF'
CREATE TABLE "user" (id INTEGER PRIMARY KEY, "user" TEXT, pwd TEXT);
CREATE UNIQUE INDEX idx_user_username_unique ON "user"("user");
CREATE UNIQUE INDEX idx_user_username_fold_unique ON "user"(LOWER("user"));
INSERT INTO "user" VALUES (1, 'Admin', 'x'), (2, 'bob', 'y');
CREATE TABLE schema_migration (version VARCHAR(64) PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_migration VALUES ('r5b4_v1_port_user_unique', 111);
INSERT INTO schema_migration VALUES ('k10_v5_username_casefold_unique', 222);
INSERT INTO schema_migration VALUES ('c12_v4_forward_lookup', 333);
CREATE TABLE peer_share_resource (id INTEGER PRIMARY KEY, release_legacy_family numeric DEFAULT false);
EOF

  local payload="$work/payload.sh" runner="$work/runner.sh"
  awk '/alpine sh -c '"'"'$/{f=1; next} /^  '"'"'; then$/{f=0} f' \
    "$ROOT_DIR/panel_install.sh" > "$payload"
  grep -q 'DROP INDEX IF EXISTS idx_user_username_fold_unique' "$payload" || \
    fail "RR-LIVE-01: could not extract the preprocessing payload (fold index drop) from panel_install.sh"
  grep -q 'DELETE FROM schema_migration' "$payload" || \
    fail "RR-LIVE-01: could not extract the preprocessing payload (v5 record delete) from panel_install.sh"
  grep -v 'apk add' "$payload" | \
    sed -e "s|/sqlite/|$work/sqlite/|g" -e "s|/tmp/dump|$work/dump|g" > "$work/payload-local.sh"
  # 载荷按出厂形态仍以 sh -c 单引号包裹执行：载荷内的 '"'"' 转义只
  # 有在单引号上下文里才还原成 SQL 的引号，拆开直跑会拼错语句。
  { printf '%s\n' "sh -c '"; cat "$work/payload-local.sh"; printf '%s\n' "'"; } > "$runner"
  bash "$runner" || fail "RR-LIVE-01: preprocessing payload must succeed on the fixture database"

  local copy="$work/sqlite/gost-migrate.db"
  [[ -f "$copy" ]] || fail "RR-LIVE-01: preprocessing must produce the migration copy"

  assert_equals "" \
    "$(sqlite3 "$copy" "SELECT name FROM sqlite_master WHERE type='index' AND name='idx_user_username_fold_unique';")" \
    "RR-LIVE-01: the migration copy must not carry the fold index that crashes pgloader"
  assert_equals "0" \
    "$(sqlite3 "$copy" "SELECT COUNT(*) FROM schema_migration WHERE version='k10_v5_username_casefold_unique';")" \
    "RR-LIVE-01: the migration copy must not carry the v5 migration record (PG first start must re-run v5 and rebuild the fold index)"
  assert_equals "2" \
    "$(sqlite3 "$copy" "SELECT COUNT(*) FROM schema_migration;")" \
    "RR-LIVE-01: other schema_migration records must survive preprocessing"
  assert_equals "1" \
    "$(sqlite3 "$copy" "SELECT COUNT(*) FROM schema_migration WHERE version='r5b4_v1_port_user_unique';")" \
    "RR-LIVE-01: the v1 migration record must survive preprocessing"
  assert_equals "2" \
    "$(sqlite3 "$copy" "SELECT COUNT(*) FROM \"user\";")" \
    "RR-LIVE-01: user rows must survive preprocessing"
  assert_equals "Admin" \
    "$(sqlite3 "$copy" "SELECT \"user\" FROM \"user\" WHERE id=1;")" \
    "RR-LIVE-01: user row contents must survive preprocessing"
  assert_equals "idx_user_username_unique" \
    "$(sqlite3 "$copy" "SELECT name FROM sqlite_master WHERE type='index' AND name='idx_user_username_unique';")" \
    "RR-LIVE-01: the plain unique username index must survive preprocessing"
  case "$(sqlite3 "$copy" ".schema peer_share_resource")" in
    *"DEFAULT false"*|*"default false"*) fail "RR-LIVE-01: the legacy release_legacy_family default rewrite must still apply" ;;
  esac

  assert_equals "idx_user_username_fold_unique" \
    "$(sqlite3 "$work/sqlite/gost.db" "SELECT name FROM sqlite_master WHERE type='index' AND name='idx_user_username_fold_unique';")" \
    "RR-LIVE-01: preprocessing must not touch the source database (fold index)"
  assert_equals "1" \
    "$(sqlite3 "$work/sqlite/gost.db" "SELECT COUNT(*) FROM schema_migration WHERE version='k10_v5_username_casefold_unique';")" \
    "RR-LIVE-01: preprocessing must not touch the source database (v5 record)"
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

# 面板源分发（install.sh）：面板线路判活后须再过 FIX2 两道门
# （延迟 ≤800ms + 256KB 短测速 ≥256KB/s）才独占路线表（阶段 3）；
# 本用例桩把两门都备成快面，且按真实契约预置 DOWNLOAD_URL 已
# 决议（install/update 恒先决议后下载）。直连与代理不被探测
# （桩对非面板探测一律判死也不影响）。
test_install_script_routes_panel_first_when_panel_alive() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  SERVER_ADDR="wss://panel.example.com:8443"
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/9.9.9/gost-amd64"

  curl() {
    case "$*" in
      *'%{http_code} %{size_download} %{time_total}'*)
        case "$*" in
          *panel.example.com*) printf '206 262144 0.500000' ;;
          *) return 22 ;;
        esac
        ;;
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

# F1-01（panel_install.sh）：直连独占须延迟门与吞吐门双过——与
# install.sh 面板源独占同口径（256KB Range 样本、≥256KB/s）。
# 第一幕：延迟 0.100s＋吞吐样本 262144B/0.5s（512KB/s）→ 直连
# 短路独占（阶段 1、路线仅直连、代理不被探测）。
# 第二幕：延迟达标但吞吐样本 262144B/30s（约 8.5KB/s）→ 不独
# 占，按实测延迟并入代理排名（ghps 0.050 在前、直连次之、阶段
# 2），慢直连不丢弃。
test_panel_install_routes_direct_needs_throughput_gate() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0

  # 第一幕：双门全过 → 直连独占。
  curl() {
    case "$*" in
      *'%{http_code} %{size_download} %{time_total}'*)
        printf '206 262144 0.500000' ;;
      *url_effective*)
        printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "F1-01 fast direct: routes should be direct only"
  assert_equals "" "${DOWNLOAD_ROUTES[0]}" "F1-01 fast direct: the only route should be direct"
  assert_equals "1" "$ROUTES_STAGE" "F1-01 fast direct: stage should be 1"

  # 第二幕：吞吐不过门 → 并入排名。
  DOWNLOAD_ROUTES=()
  ROUTES_STAGE=0
  curl() {
    case "$*" in
      *'%{http_code} %{size_download} %{time_total}'*)
        printf '206 262144 30.000000' ;;
      *url_effective*)
        case "$*" in
          *ghps.cc*) printf '0.050 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
          *) printf '0.100 https://github.com/%s/releases/tag/9.9.9' "$REPO" ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  ensure_download_routes

  assert_equals "2" "$ROUTES_STAGE" "F1-01 slow-throughput direct: stage should be 2 (ranked)"
  assert_equals "https://ghps.cc" "${DOWNLOAD_ROUTES[0]}" "F1-01 slow-throughput direct: fastest proxy should rank first"
  assert_equals "" "${DOWNLOAD_ROUTES[1]}" "F1-01 slow-throughput direct: direct should be inserted by its measured latency, not dropped"
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
    case "$1" in
      ps)
        # 真等待函数按名查运行中容器：本夹具部署为旧缺省名三件套。
        printf '%s\n' "flux-panel-postgres" "flux-panel-backend" "vite-frontend"
        return 0
        ;;
      inspect)
        case "$*" in
          *Health.Status*) printf 'healthy\n' ;;
        esac
        return 0
        ;;
    esac
    case "$*" in
      *pgloader*) [[ ! -f "$FIXTURE_FLAGS/pgloader-fail" ]] ;;
      *) return 0 ;;
    esac
  }
  # R3-F7-01：两个健康等待函数不再整体打桩——等待内的容器名推导
  # 须被真实执行。本夹具 .env 无身份键，真等待应按缺省旧名命中上
  # 方 docker 桩的清单（存量部署口径）；flmx 身份名的推导由
  # test_panel_migrate_waits_use_deployment_container_names 专测。
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

# R3-F7-01: 迁移的健康等待必须按部署 .env 的身份名查容器——旧实
# 现只把后端/前端名读进 local、PG 名从未自 .env 读过，两等待函数
# 从全局重导恒落 DEFAULT 旧名（flux-panel-*），flmx 命名部署在 PG
# 等待处 90 秒超时回滚。本例不打桩等待函数：docker 桩记录每次
# inspect 的被查容器名、ps 只报部署的真实容器名，断言两等待命中
# 的是 .env 身份名（默认实例与实例派生名各一幕）、旧名从未被查
# 询、迁移走通。同幕兼验 R3-F7-02：已存在 compose 缺可信代理跳
# 数传递行时，迁移后必须补齐（旧实现只在下载分支补行）。
test_panel_migrate_waits_use_deployment_container_names() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_DEPLOY_DIR FLMX_INSTANCE

  check_docker() { DOCKER_CMD="fixture_compose"; return 0; }
  fixture_compose() { return 0; }
  sleep() { :; }

  docker() {
    case "$1" in
      ps)
        cat "$FIXTURE_PS_NAMES"
        return 0
        ;;
      inspect)
        # 记录被查容器名（恒为最后一个参数）；健康形态答 healthy，
        # 镜像形态答本面板镜像（归属实物证据）。
        local last="${!#}"
        printf '%s\n' "$last" >> "$FIXTURE_INSPECT_LOG"
        case "$*" in
          *Health.Status*) printf 'healthy\n' ;;
          *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test" ;;
        esac
        return 0
        ;;
    esac
    return 0
  }

  # $1=后端名 $2=前端名 $3=PG名：构造该身份的部署并跑一次迁移，
  # 断言等待查询名、补行与迁移结果。
  run_migrate_name_scenario() {
    local backend="$1" frontend="$2" postgres="$3"
    local work
    work=$(mktemp -d)
    FIXTURE_PS_NAMES="$work/ps-names"
    FIXTURE_INSPECT_LOG="$work/inspect-log"
    printf '%s\n' "$postgres" "$backend" "$frontend" > "$FIXTURE_PS_NAMES"
    : > "$FIXTURE_INSPECT_LOG"
    cat > "$work/.env" <<EOF
JWT_SECRET=test-secret
DB_TYPE=sqlite
POSTGRES_DB=flux_panel
POSTGRES_USER=flux_panel
POSTGRES_PASSWORD=test-pg-pass
PANEL_BACKEND_CONTAINER=$backend
PANEL_FRONTEND_CONTAINER=$frontend
PANEL_POSTGRES_CONTAINER=$postgres
EOF
    # compose 带 backend environment 段但缺可信代理跳数行（F7-02 面）。
    cat > "$work/docker-compose.yml" <<'EOF'
services:
  backend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test
    environment:
      DB_TYPE: sqlite
  frontend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test
EOF
    local rc="0"
    ( cd "$work" && migrate_to_postgres >"$work/migrate.log" 2>&1 ) || rc="$?"
    assert_equals "0" "$rc" "R3-F7-01: migration must succeed for deployment identity $backend"
    grep -Fxq "$postgres" "$FIXTURE_INSPECT_LOG" || \
      fail "R3-F7-01: postgres wait must inspect the deployment container $postgres"
    grep -Fxq "$backend" "$FIXTURE_INSPECT_LOG" || \
      fail "R3-F7-01: backend wait must inspect the deployment container $backend"
    if grep -Fxq "flux-panel-postgres" "$FIXTURE_INSPECT_LOG"; then
      fail "R3-F7-01: postgres wait must never fall back to the legacy default name"
    fi
    if grep -Fxq "flux-panel-backend" "$FIXTURE_INSPECT_LOG"; then
      fail "R3-F7-01: backend wait must never fall back to the legacy default name"
    fi
    grep -q 'FLMX_TRUSTED_PROXY_HOPS' "$work/docker-compose.yml" || \
      fail "R3-F7-02: migration with an existing compose must add the trusted proxy hops line"
    grep -q '^DB_TYPE=postgres' "$work/.env" || \
      fail "R3-F7-01: scenario migration must switch .env to postgres"
  }

  run_migrate_name_scenario "flmx-panel-backend" "flmx-panel-frontend" "flmx-panel-postgres"
  run_migrate_name_scenario "flmx-panel-backend-b2" "flmx-panel-frontend-b2" "flmx-panel-postgres-b2"
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
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

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
# 人的残留文件不删并返回 1；已证属自家同样须显式确认（删除集含
# 完整 DB 备份与 .env 密钥副本、不可恢复，与 Docker 分支确认同
# 口径——旧「已证即零确认直删」即 C4 族九立案点，本轮改判）；
# 归属未证须显式确认，拒绝确认时保留、无输入时不删。
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

  # 自家残留（Compose 引用本面板镜像）：无输入不删、确认后清理
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  rc="0"
  ( cd "$d" && uninstall_panel </dev/null >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-09: unconfirmed no-docker uninstall should be a clean cancel"
  [[ -e "$d/.env" && -e "$d/docker-compose.yml" ]] || fail "AI-09: own files must stay without confirmation"
  rc="0"
  ( cd "$d" && uninstall_panel <<< "y" >/dev/null 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "AI-09: no-docker uninstall should clean an own deployment after confirmation"
  [[ ! -e "$d/.env" && ! -e "$d/docker-compose.yml" ]] || fail "AI-09: own leftover files must be removed after confirmation"

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

# R1-01-L2：脚本面 .env 读值与 Go 面 parseEnvFileValues 同语义
# （Compose 口径）——重复键后现胜出、剥引号、剥行尾注释（# 前为
# 空白才算注释）、键名两侧空白裁剪、注释行不命中。旧实现 grep
# -m1 首现＋原文：带引号卷键被读成含引号真值（自检恒不等）、
# PANEL_IDENTITY_RENAME="0" 与 Go 面判定分家。raw 读面则相反：
# 只剥空白、引号保留，供 I-F1-05 的端口严格面判读原始形态。
# 末幕钉值级复核：带引号卷键与实挂同名时不得触发冲突改写、
# .env 逐字不动（旧实现必触发改写、本幕必红）。
test_env_var_reading_matches_compose_semantics() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)
  cd "$dir"
  printf '%s\n' \
    'FLMX_SQLITE_VOLUME="quoted_vol"' \
    'DUP=first' \
    'DUP=second' \
    'TRAIL=value # comment' \
    'HASHY=abc#def' \
    "SINGLE='single quoted'" \
    'SPACED = spaced_v ' \
    '#COMMENTED=nope' \
    'EMPTYQ=""' > .env
  assert_equals "quoted_vol" "$(get_env_var FLMX_SQLITE_VOLUME)" "R1-01-L2: a double-quoted value must be read without the quotes"
  assert_equals "second" "$(get_env_var DUP)" "R1-01-L2: a duplicate key must resolve to the last occurrence"
  assert_equals "value" "$(get_env_var TRAIL)" "R1-01-L2: a trailing comment after whitespace must be stripped"
  assert_equals "abc#def" "$(get_env_var HASHY)" "R1-01-L2: a # not preceded by whitespace is part of the value"
  assert_equals "single quoted" "$(get_env_var SINGLE)" "R1-01-L2: a single-quoted value must be read without the quotes"
  assert_equals "spaced_v" "$(get_env_var SPACED)" "R1-01-L2: key and value whitespace must be trimmed"
  assert_equals "" "$(get_env_var COMMENTED)" "R1-01-L2: a commented-out key must not be read"
  assert_equals "" "$(get_env_var EMPTYQ)" "R1-01-L2: an empty quoted value must read as empty"
  assert_equals '"quoted_vol"' "$(get_env_var_raw FLMX_SQLITE_VOLUME)" "R1-01-L2: the raw reader must keep the quotes for the strict faces"
  printf 'PANEL_IDENTITY_RENAME="0"\n' > .env
  assert_equals "0" "$(get_env_var PANEL_IDENTITY_RENAME)" "R1-01-L2: a quoted rename switch must parse like the Go face"

  local pdir
  pdir=$(mktemp -d)
  cat > "$pdir/.env" <<'EOF'
FLMX_SQLITE_VOLUME="real_vol"
FLMX_POSTGRES_VOLUME=kept_pg
FLMX_NETWORK=kept_net
COMPOSE_PROJECT_NAME=kept_project
DB_TYPE=sqlite
EOF
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *"/app/data"*) printf 'real_vol' ;;
        *"/var/lib/postgresql/data"*) printf 'kept_pg' ;;
        *) printf '' ;;
      esac
      return 0
    fi
    return 0
  }
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || \
    fail "R1-01-L2: pinning must succeed when the quoted key parses to the live mount"
  if grep -q '资源键冲突' "$pdir/pin.log"; then
    fail "R1-01-L2: a quoted key equal to the live mount after parsing must not be treated as a conflict"
  fi
  grep -q '^FLMX_SQLITE_VOLUME="real_vol"$' "$pdir/.env" || fail "R1-01-L2: a parsed-equal volume key must stay verbatim (no rewrite)"
)

# L2-12（R5-FD）：update_panel 必须走 VERSION > FLMX_VERSION >
# PINNED_VERSION > latest 优先级链——旧实现只认 VERSION，FLMX_VERSION
# /PINNED 定版被静默吞掉直升 latest（桩 latest=9.9.9 不得被选中），
# 且定版来源须在输出中点名。S1-F02：旧键 FLUX_VERSION 已退场，
# 不再参与定版链，设值也不得被选中、不得在输出中被点名为版本来源。
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
  panel_pin_missing_resource_keys() { return 0; }
  activate_panel_update_files() { return 0; }
  start_panel_after_update() { return 0; }
  verify_panel_update_sqlite_volume() { return 0; }
  check_ipv6_support() { return 1; }

  local log
  log=$(mktemp)

  VERSION=""
  FLUX_VERSION="1.5.0"
  PINNED_VERSION=""
  update_panel >"$log" 2>&1 || fail "L2-12: update should succeed with FLUX_VERSION set (ignored)"
  grep -q "本次更新目标" "$log" || fail "B2-04: update must name its target deployment before mutating anything"
  assert_equals "9.9.9" "$LATEST_VERSION" "L2-12: FLUX_VERSION must be ignored after the transition window (falls through to latest)"
  if grep -q "FLUX_VERSION" "$log"; then
    fail "L2-12: FLUX_VERSION must not be named as the version source"
  fi

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

# RN-1: 改名后的新装缺省全集——脚本常量（现行缺省为 flmx 名、旧名
# 只留在识别集）、两份 compose 模板的回退默认（容器/PG 库用户/卷/
# 网络/后端镜像/默认口令哨兵）、版本键优先级（FLMX_VERSION 优先、
# FLUX_VERSION 已退场）与新装 PG 库/用户缺省。
test_panel_rename_new_install_defaults_are_flmx() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  assert_equals "flmx-panel-backend" "$DEFAULT_PANEL_BACKEND_CONTAINER" "RN-1: script default backend container must be the flmx name"
  assert_equals "flmx-panel-postgres" "$DEFAULT_PANEL_POSTGRES_CONTAINER" "RN-1: script default postgres container must be the flmx name"
  assert_equals "flux-panel-backend" "$LEGACY_PANEL_BACKEND_CONTAINER" "RN-1: the legacy recognition set must keep the old backend name"
  assert_equals "flux-panel-postgres" "$LEGACY_PANEL_POSTGRES_CONTAINER" "RN-1: the legacy recognition set must keep the old postgres name"
  assert_equals "vite-frontend" "$LEGACY_PANEL_FRONTEND_CONTAINER" "RN-1: the legacy recognition set must keep the old frontend name"

  local f lit
  for f in docker-compose-v4.yml docker-compose-v6.yml; do
    for lit in \
      'flmx-panel-backend:${FLMX_VERSION:-latest}' \
      'container_name: ${PANEL_BACKEND_CONTAINER:-flmx-panel-backend}' \
      'container_name: ${PANEL_POSTGRES_CONTAINER:-flmx-panel-postgres}' \
      'container_name: ${PANEL_FRONTEND_CONTAINER:-flmx-panel-frontend}' \
      'POSTGRES_DB: ${POSTGRES_DB:-flmx_panel}' \
      'POSTGRES_USER: ${POSTGRES_USER:-flmx_panel}' \
      'POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-flmx_panel_change_me}' \
      'flmx-vite-frontend:${FLMX_VERSION:-latest}' \
      'name: ${FLMX_SQLITE_VOLUME:-flmx-sqlite-data}' \
      'name: ${FLMX_POSTGRES_VOLUME:-flmx-postgres-data}' \
      'name: ${FLMX_NETWORK:-flmx-network}'; do
      grep -Fq "$lit" "$ROOT_DIR/$f" || fail "RN-1: $f must carry the new-install default: $lit"
    done
    if grep -Fq ':-flux-panel-' "$ROOT_DIR/$f"; then
      fail "RN-1: $f must not keep a legacy container-name fallback"
    fi
    if grep -Fq 'flmx-flux-panel-backend' "$ROOT_DIR/$f"; then
      fail "RN-1: $f must not keep the old backend image name"
    fi
    if grep -Fq ':-flux_panel}' "$ROOT_DIR/$f"; then
      fail "RN-1: $f must not keep the legacy PG db/user fallback"
    fi
    if grep -Fq 'FLUX_VERSION' "$ROOT_DIR/$f"; then
      fail "RN-1: $f must not keep a retired FLUX_VERSION fallback (S1-F02)"
    fi
    if grep -Fq ':-vite-frontend}' "$ROOT_DIR/$f"; then
      fail "RN-1: $f must not keep the legacy frontend fallback"
    fi
  done

  # 版本键环境链：FLMX_VERSION 优先；旧键 FLUX_VERSION 已退场，
  # 设值也不得被选中（S1-F02）。
  resolve_latest_release_tag() { echo "9.9.9"; }
  VERSION="" FLMX_VERSION="2.0.0" FLUX_VERSION="1.0.0" PINNED_VERSION=""
  assert_equals "2.0.0" "$(resolve_version)" "RN-1: FLMX_VERSION must win over FLUX_VERSION"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="1.0.0" PINNED_VERSION=""
  assert_equals "9.9.9" "$(resolve_version)" "RN-1: FLUX_VERSION must be ignored after the transition window (falls through to latest)"

  # 新装选 PostgreSQL 时库/用户缺省为 flmx_panel。
  get_config_params <<< $'\n\n2' >/dev/null
  assert_equals "flmx_panel" "$POSTGRES_DB" "RN-1: fresh installs must default POSTGRES_DB to flmx_panel"
  assert_equals "flmx_panel" "$POSTGRES_USER" "RN-1: fresh installs must default POSTGRES_USER to flmx_panel"
)

# RN-1: 旧名识别集不回归——已知名全集仍含旧缺省名、旧名不作专属
# 证据、detect 在只有旧名/只有新名/两代同现三态下分别沿用旧名/
# 取新名/拒绝判定。
test_panel_legacy_identity_recognition_intact() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  if ! panel_backend_name_is_known_ours "flux-panel-backend"; then
    fail "RN-1: the legacy backend name must stay in the known-ours set"
  fi
  if ! panel_backend_name_is_known_ours "flmx-panel-backend"; then
    fail "RN-1: the flmx backend name must be in the known-ours set"
  fi
  if panel_backend_name_is_known_ours "other-panel-backend"; then
    fail "RN-1: a foreign backend name must not enter the known-ours set"
  fi
  if panel_backend_name_is_exclusive_ours "flux-panel-backend"; then
    fail "RN-1: the legacy backend name must not count as exclusive ownership evidence"
  fi
  if ! panel_backend_name_is_exclusive_ours "flmx-panel-backend"; then
    fail "RN-1: the flmx backend name must count as exclusive ownership evidence"
  fi

  unset PANEL_BACKEND_CONTAINER FLMX_INSTANCE
  local dir
  dir=$(mktemp -d)
  PANEL_DEPLOY_DIR="$dir"
  EXIST_CASE=""
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:v-test"
      return 0
    fi
    if [[ "$1" == "inspect" ]]; then
      case "$EXIST_CASE" in
        legacy) [[ "$2" == "flux-panel-backend" ]] ;;
        new) [[ "$2" == "flmx-panel-backend" ]] ;;
        both) [[ "$2" == "flux-panel-backend" || "$2" == "flmx-panel-backend" ]] ;;
      esac
      return
    fi
    if [[ "$1" == "ps" ]]; then
      case "$EXIST_CASE" in
        legacy) printf '%s\n' "flux-panel-backend" ;;
        new) printf '%s\n' "flmx-panel-backend" ;;
        both) printf '%s\n' "flux-panel-backend" "flmx-panel-backend" ;;
      esac
      return 0
    fi
    return 0
  }

  EXIST_CASE="legacy"
  assert_equals "flux-panel-backend" "$(detect_panel_backend_container)" "RN-1: detect must still resolve a legacy-only deployment to the legacy name"
  EXIST_CASE="new"
  assert_equals "flmx-panel-backend" "$(detect_panel_backend_container)" "RN-1: detect must resolve a new-only deployment to the flmx name"
  EXIST_CASE="both"
  local rc="0"
  detect_panel_backend_container >/dev/null 2>&1 || rc="$?"
  assert_equals "1" "$rc" "RN-1: detect must still refuse when both generations are present"
)

# RN-1 升级改名夹具：在 $1 构造存量部署（$2=legacy 带旧名键 /
# keyless 无身份键 / current 身份键已是现行名），docker/compose 以存在性文件与日志桩承接；
# resolve 桩按真实口径给出旧名全局（keyless 经探测亦落旧名）。
setup_rename_update_fixture() {
  FIXTURE_DIR="$1"
  FIXTURE_EXIST="$1/exist"
  FIXTURE_RMLOG="$1/rm.log"
  mkdir -p "$FIXTURE_EXIST"
  : > "$FIXTURE_RMLOG"
  # 第三种部署形态 current：身份键已是现行名（不触发改名），供升
  # 级后卷名自检的「已有键路径」用例使用；resolve 桩按形态给名。
  FIXTURE_RESOLVE_BACKEND="flux-panel-backend"
  FIXTURE_RESOLVE_FRONTEND="vite-frontend"
  FIXTURE_RESOLVE_POSTGRES="flux-panel-postgres"
  if [[ "$2" == "current" ]]; then
    FIXTURE_RESOLVE_BACKEND="flmx-panel-backend"
    FIXTURE_RESOLVE_FRONTEND="flmx-panel-frontend"
    FIXTURE_RESOLVE_POSTGRES="flmx-panel-postgres"
    touch "$FIXTURE_EXIST/flmx-panel-backend" "$FIXTURE_EXIST/flmx-panel-frontend" "$FIXTURE_EXIST/flmx-panel-postgres"
  else
    touch "$FIXTURE_EXIST/flux-panel-backend" "$FIXTURE_EXIST/vite-frontend" "$FIXTURE_EXIST/flux-panel-postgres"
  fi
  cat > "$1/.env" <<'EOF'
JWT_SECRET=<redacted>
FRONTEND_PORT=6366
BACKEND_PORT=6365
FLUX_VERSION=1.9.7
DB_TYPE=sqlite
EOF
  if [[ "$2" == "legacy" ]]; then
    cat >> "$1/.env" <<'EOF'
PANEL_BACKEND_CONTAINER=flux-panel-backend
PANEL_FRONTEND_CONTAINER=vite-frontend
PANEL_POSTGRES_CONTAINER=flux-panel-postgres
COMPOSE_PROJECT_NAME=flvx-panel
EOF
  fi
  if [[ "$2" == "current" ]]; then
    cat >> "$1/.env" <<'EOF'
PANEL_BACKEND_CONTAINER=flmx-panel-backend
PANEL_FRONTEND_CONTAINER=flmx-panel-frontend
PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
COMPOSE_PROJECT_NAME=flvx-panel
EOF
    # 卷键仅在用例显式给定 FIXTURE_ENV_SQLITE_VOLUME 时才写入，
    # 缺省即「容器键齐全非旧名但缺卷键」的待钉形状。
    if [[ -n "${FIXTURE_ENV_SQLITE_VOLUME:-}" ]]; then
      echo "FLMX_SQLITE_VOLUME=$FIXTURE_ENV_SQLITE_VOLUME" >> "$1/.env"
    fi
  fi
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:1.9.7\n' > "$1/docker-compose.yml"

  check_docker() { DOCKER_CMD="fixture_rename_compose"; }
  fixture_rename_compose() {
    if [[ "$1" == "up" ]]; then
      # 重建状态标记：新模板 compose 下的 up 视作容器已被新版重
      # 建，此后 inspect 的 /app/data 改答 FIXTURE_REBUILT_SQLITE_
      # VOLUME（见下方 Mounts 分支）。旧实现无条件改答——同名容
      # 器在重建前（钉键实读时点）也被答成重建后挂载，与真实
      # docker 语义不符（旧容器在被重建前恒答自身真实挂载），
      # FIX199-D1 的钉键交叉核对落地后该失真会把自检用例的形
      # 态顶掉，故按状态建模。
      if ! grep -q 'flmx-flux-panel-backend' "$FIXTURE_DIR/docker-compose.yml" 2>/dev/null; then
        touch "$FIXTURE_DIR/.rebuilt"
      fi
      local k v def_backend def_frontend def_postgres
      # 键缺失时的容器名按当前 compose 文件的代际缺省（与真实模
      # 板同口径：旧 compose 缺省旧名、新 compose 缺省现行名）；
      # 否则无键存量的回滚重建在夹具里造不出旧名容器、健康等待
      # 恒失败，与真实 compose 行为不符。
      if grep -q 'flmx-flux-panel-backend' "$FIXTURE_DIR/docker-compose.yml" 2>/dev/null; then
        def_backend="flux-panel-backend" def_frontend="vite-frontend" def_postgres="flux-panel-postgres"
      else
        def_backend="flmx-panel-backend" def_frontend="flmx-panel-frontend" def_postgres="flmx-panel-postgres"
      fi
      for k in PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER; do
        v=$(get_env_var "$k" "$FIXTURE_DIR/.env" || true)
        if [[ -z "$v" ]]; then
          case "$k" in
            PANEL_BACKEND_CONTAINER) v="$def_backend" ;;
            PANEL_FRONTEND_CONTAINER) v="$def_frontend" ;;
            PANEL_POSTGRES_CONTAINER) v="$def_postgres" ;;
          esac
        fi
        [[ -n "$v" ]] && touch "$FIXTURE_EXIST/$v"
      done
    fi
    return 0
  }
  docker() {
    case "$1" in
      ps)
        ls "$FIXTURE_EXIST" 2>/dev/null || true
        return 0
        ;;
      stop)
        return 0
        ;;
      rm)
        printf '%s\n' "$2" >> "$FIXTURE_RMLOG"
        rm -f "$FIXTURE_EXIST/$2"
        return 0
        ;;
      volume)
        return 1
        ;;
      inspect)
        if [[ "$2" == "-f" ]]; then
          local fmt="$3" name="$4"
          # RR4-F7-01 夹具硬化：真实 docker inspect 对已删/不存在容
          # 器必失败——旧夹具不校验存在性、删后仍返回 Mounts/
          # Networks，正好掩盖改名流「先删后钉」的顺序缺陷。现与
          # 无 -f 分支同口径：容器不在册即失败。资源名经 FIXTURE_*
          # 参数化（缺省＝旧缺省名），供自定义资源名用例改值。
          [[ -e "$FIXTURE_EXIST/$name" ]] || return 1
          case "$fmt" in
            *Health.Status*)
              if [[ "$name" == "flmx-panel-backend" && -n "${FIXTURE_BACKEND_UNHEALTHY:-}" ]]; then
                printf 'unhealthy\n'
              else
                printf 'healthy\n'
              fi
              ;;
            *Mounts*)
              case "$fmt" in
                *"/app/data"*)
                  # FIX199-B：重建后的新名后端可经 FIXTURE_REBUILT_
                  # SQLITE_VOLUME 单独改答挂载卷，模拟「漏钉后按模
                  # 板回退名挂空卷」的事故形态，供升级后卷名自检
                  # 用例制造挂载不符；未设时与其它容器同答。
                  # FIX199-D1 状态化：改答只在容器已被新模板 compose
                  # 的 up 重建后（.rebuilt 标记）生效——重建前的实读
                  # （钉键交叉核对）必须答旧容器真实挂载，否则同名
                  # 新旧容器在夹具里不可分辨、钉键会把重建后形态
                  # 误当现行真值改写键。
                  if [[ "$name" == "flmx-panel-backend" && -n "${FIXTURE_REBUILT_SQLITE_VOLUME:-}" && -e "$FIXTURE_DIR/.rebuilt" ]]; then
                    printf '%s' "$FIXTURE_REBUILT_SQLITE_VOLUME"
                  else
                    printf '%s' "${FIXTURE_SQLITE_VOLUME:-sqlite_data}"
                  fi
                  ;;
                *"/var/lib/postgresql/data"*) printf '%s' "${FIXTURE_POSTGRES_VOLUME:-postgres_data}" ;;
              esac
              ;;
            *NetworkSettings.Networks*)
              printf '%s' "${FIXTURE_NETWORK:-gost-network}"
              ;;
            *)
              printf '\n'
              ;;
          esac
          return 0
        fi
        [[ -e "$FIXTURE_EXIST/$2" ]]
        return
        ;;
    esac
    return 0
  }
  sleep() { :; }

  resolve_panel_deployment() {
    PANEL_DEPLOY_DIR="$FIXTURE_DIR"
    PANEL_BACKEND_CONTAINER="${FIXTURE_RESOLVE_BACKEND:-flux-panel-backend}"
    PANEL_FRONTEND_CONTAINER="${FIXTURE_RESOLVE_FRONTEND:-vite-frontend}"
    PANEL_POSTGRES_CONTAINER="${FIXTURE_RESOLVE_POSTGRES:-flux-panel-postgres}"
    PANEL_COMPOSE_PROJECT="flvx-panel"
    cd "$FIXTURE_DIR"
  }
  validate_panel_update_environment() { :; }
  get_current_db_type() { echo "sqlite"; }
  get_deployed_panel_version() { return 1; }
  resolve_latest_release_tag() { echo "1.9.8"; }
  download_panel_update_compose() {
    UPDATE_COMPOSE_CANDIDATE="$FIXTURE_DIR/candidate.yml"
    cat > "$UPDATE_COMPOSE_CANDIDATE" <<'YAML'
services:
  backend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:1.9.8
    environment:
      FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2}
YAML
  }
  validate_panel_update_compose() { return 0; }
  create_panel_update_backup() {
    UPDATE_BACKUP_DIR="$FIXTURE_DIR/backup"
    mkdir -p "$UPDATE_BACKUP_DIR"
    cp -p "$FIXTURE_DIR/.env" "$FIXTURE_DIR/docker-compose.yml" "$UPDATE_BACKUP_DIR/"
  }
  pull_panel_update_images() { return 0; }
}

# RN-1: 存量升级改名成功路径——.env 带旧名键与完全无身份键两幕：
# 更新走通、三身份键写为现行名、旧容器被删、卷/网络缺失键钉为现
# 行有效值（旧名、不随改名翻新）、既有项目名逐字保留、版本键双写
# 到目标版本、进程全局同步新名。
test_panel_update_renames_legacy_identity() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  assert_renamed_env() {
    local dir="$1"
    grep -q '^PANEL_BACKEND_CONTAINER=flmx-panel-backend$' "$dir/.env" || fail "RN-1: backend identity key must be renamed in .env"
    grep -q '^PANEL_FRONTEND_CONTAINER=flmx-panel-frontend$' "$dir/.env" || fail "RN-1: frontend identity key must be renamed in .env"
    grep -q '^PANEL_POSTGRES_CONTAINER=flmx-panel-postgres$' "$dir/.env" || fail "RN-1: postgres identity key must be renamed in .env"
    grep -q '^FLMX_VERSION=1.9.8$' "$dir/.env" || fail "RN-1: update must write FLMX_VERSION"
    grep -q '^FLUX_VERSION=1.9.7$' "$dir/.env" || fail "RN-1: update must leave the retired FLUX_VERSION key untouched (no longer bumped, S1-F02)"
    if grep -q '^FLUX_VERSION=1.9.8$' "$dir/.env"; then
      fail "RN-1: update must not bump the retired FLUX_VERSION key"
    fi
    grep -q '^FLMX_SQLITE_VOLUME=sqlite_data$' "$dir/.env" || fail "RN-1: a missing sqlite volume key must be pinned to the effective volume, not renamed"
    grep -q '^FLMX_POSTGRES_VOLUME=postgres_data$' "$dir/.env" || fail "RN-1: a missing postgres volume key must be pinned to the effective volume, not renamed"
    grep -q '^FLMX_NETWORK=gost-network$' "$dir/.env" || fail "RN-1: a missing network key must be pinned to the effective network, not renamed"
    grep -q '^COMPOSE_PROJECT_NAME=flvx-panel$' "$dir/.env" || fail "RN-1: the effective compose project must be preserved/pinned verbatim"
    grep -Fxq 'flux-panel-backend' "$dir/rm.log" || fail "RN-1: the legacy backend container must be removed during the rename"
    grep -Fxq 'vite-frontend' "$dir/rm.log" || fail "RN-1: the legacy frontend container must be removed during the rename"
    grep -Fxq 'flux-panel-postgres' "$dir/rm.log" || fail "RN-1: the legacy postgres container must be removed during the rename"
  }

  local dir1 dir2
  dir1=$(mktemp -d)
  setup_rename_update_fixture "$dir1" legacy
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir1/update.log" 2>&1 || fail "RN-1: update of a legacy-named deployment must succeed"
  grep -q '身份改名' "$dir1/update.log" || fail "RN-1: the rename must be named in the update output"
  assert_renamed_env "$dir1"
  assert_equals "flmx-panel-backend" "$PANEL_BACKEND_CONTAINER" "RN-1: process globals must switch to the new backend name after the rename"
  assert_equals "flmx-panel-postgres" "$PANEL_POSTGRES_CONTAINER" "RN-1: process globals must switch to the new postgres name after the rename"

  dir2=$(mktemp -d)
  setup_rename_update_fixture "$dir2" keyless
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir2/update.log" 2>&1 || fail "RN-1: update of a keyless legacy deployment must succeed"
  assert_renamed_env "$dir2"
)

# RR4-F7-01: 自定义卷/网络名的无键存量改名——缺失的卷/网络键必须
# 钉为从尚存旧容器实读到的真实名（mydata_vol/mypg_vol/mynet），而
# 不是模板/回退名。缺陷形态（钉键排在停删之后＋夹具 inspect 不校
# 验存在性）下 inspect 恒失败：SQLite 卷与网络被钉回退名、PG 卷键
# 根本不钉，本例必红；顺序修复＋夹具硬化后全绿。
test_panel_update_rename_pins_custom_resource_names() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)
  setup_rename_update_fixture "$dir" keyless
  FIXTURE_SQLITE_VOLUME="mydata_vol"
  FIXTURE_POSTGRES_VOLUME="mypg_vol"
  FIXTURE_NETWORK="mynet"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir/update.log" 2>&1 || fail "RR4-F7-01: update of a keyless deployment with custom resource names must succeed"
  grep -q '^PANEL_BACKEND_CONTAINER=flmx-panel-backend$' "$dir/.env" || fail "RR4-F7-01: backend identity key must still be renamed in .env"
  grep -q '^FLMX_SQLITE_VOLUME=mydata_vol$' "$dir/.env" || fail "RR4-F7-01: a missing sqlite volume key must be pinned to the volume read from the live legacy container, not the fallback"
  grep -q '^FLMX_POSTGRES_VOLUME=mypg_vol$' "$dir/.env" || fail "RR4-F7-01: a missing postgres volume key must be pinned to the volume read from the live legacy container, not left unpinned"
  grep -q '^FLMX_NETWORK=mynet$' "$dir/.env" || fail "RR4-F7-01: a missing network key must be pinned to the network read from the live legacy container, not the fallback"
  grep -Fxq 'flux-panel-backend' "$dir/rm.log" || fail "RR4-F7-01: the legacy backend container must still be removed during the rename"
)

# RN-1: 改名后健康检查失败的回滚路径——新名后端恒 unhealthy 时更
# 新必须失败并走既有回滚：先删新名容器、.env 逐字恢复为更新前
# （旧名与旧版本键）、进程全局复位旧名、回执点名已恢复。
test_panel_update_rename_health_failure_rolls_back() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)
  setup_rename_update_fixture "$dir" legacy
  cp -p "$dir/.env" "$dir/env.pristine"
  FIXTURE_BACKEND_UNHEALTHY="1"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  local rc="0"
  update_panel >"$dir/update.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "RN-1: update must fail when the renamed backend never becomes healthy"
  grep -q '已恢复更新前版本' "$dir/update.log" || fail "RN-1: the rollback completion must be reported"
  grep -q '身份改名回滚' "$dir/update.log" || fail "RN-1: the rollback must name the identity-rename rollback step"
  grep -Fxq 'flmx-panel-backend' "$dir/rm.log" || fail "RN-1: rollback must remove the new-name backend container before relaunching under the old name"
  grep -Fxq 'flux-panel-backend' "$dir/rm.log" || fail "RN-1: the rename step must have removed the legacy backend container"
  diff -q "$dir/env.pristine" "$dir/.env" >/dev/null || fail "RN-1: rollback must restore the pre-update .env verbatim (legacy names and version)"
  assert_equals "flux-panel-backend" "$PANEL_BACKEND_CONTAINER" "RN-1: process globals must be restored to the old backend name after rollback"
  assert_equals "flux-panel-postgres" "$PANEL_POSTGRES_CONTAINER" "RN-1: process globals must be restored to the old postgres name after rollback"
)

# FIX199-B: 升级后卷名自检·改名路径不符必回滚——无键存量改名钉
# 键无误（期望 sqlite_data），但重建后的新后端被夹具改答挂
# flmx-sqlite-data（模拟漏钉/模板回退挂空卷的 1.9.8 事故形态）：
# 健康检查照样通过，自检必须把关——更新判失败、回执点名期望卷
# 与实际卷、走既有回滚恢复更新前 .env 并报已恢复。
test_panel_update_volume_check_rolls_back_on_mismatch() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)
  setup_rename_update_fixture "$dir" keyless
  cp -p "$dir/.env" "$dir/env.pristine"
  FIXTURE_REBUILT_SQLITE_VOLUME="flmx-sqlite-data"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  local rc="0"
  update_panel >"$dir/update.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "FIX199-B: update must fail when the rebuilt backend mounts a different data volume than recorded"
  grep -q '升级后卷名自检失败' "$dir/update.log" || fail "FIX199-B: the volume self-check failure must be named in the update output"
  grep -q 'flmx-sqlite-data' "$dir/update.log" || fail "FIX199-B: the self-check failure must name the actually mounted volume"
  grep -q 'sqlite_data' "$dir/update.log" || fail "FIX199-B: the self-check failure must name the expected recorded volume"
  grep -q '已恢复更新前版本' "$dir/update.log" || fail "FIX199-B: a self-check failure must roll back to the pre-update version"
  diff -q "$dir/env.pristine" "$dir/.env" >/dev/null || fail "FIX199-B: rollback after a self-check failure must restore the pre-update .env verbatim"
)

# FIX199-B: 升级后卷名自检·已有键路径两幕——身份键已是现行名、
# 不触发改名时自检同样生效：第一幕重建挂载与 .env 既有卷名
# （kept_vol）相符，更新成功、自检通过被点名、卷键逐字不动；
# 第二幕重建被改答挂 flmx-sqlite-data，更新判失败并回滚、.env
# 逐字恢复。
test_panel_update_volume_check_existing_key_path() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir1 dir2
  dir1=$(mktemp -d)
  FIXTURE_ENV_SQLITE_VOLUME="kept_vol"
  setup_rename_update_fixture "$dir1" current
  FIXTURE_SQLITE_VOLUME="kept_vol"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir1/update.log" 2>&1 || fail "FIX199-B: update with pre-existing identity/volume keys and a matching mount must succeed"
  grep -q '升级后卷名自检通过' "$dir1/update.log" || fail "FIX199-B: the volume self-check pass must be named in the update output"
  if grep -q '身份改名' "$dir1/update.log"; then
    fail "FIX199-B: a deployment already on current names must not enter the rename path"
  fi
  grep -q '^FLMX_SQLITE_VOLUME=kept_vol$' "$dir1/.env" || fail "FIX199-B: a pre-existing sqlite volume key must stay verbatim"

  dir2=$(mktemp -d)
  FIXTURE_ENV_SQLITE_VOLUME="kept_vol"
  setup_rename_update_fixture "$dir2" current
  cp -p "$dir2/.env" "$dir2/env.pristine"
  FIXTURE_SQLITE_VOLUME="kept_vol"
  FIXTURE_REBUILT_SQLITE_VOLUME="flmx-sqlite-data"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  local rc="0"
  update_panel >"$dir2/update.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "FIX199-B: the existing-key path must also fail the update when the rebuilt mount differs"
  grep -q '升级后卷名自检失败' "$dir2/update.log" || fail "FIX199-B: the existing-key path self-check failure must be named"
  grep -q '已恢复更新前版本' "$dir2/update.log" || fail "FIX199-B: the existing-key path self-check failure must roll back"
  diff -q "$dir2/env.pristine" "$dir2/.env" >/dev/null || fail "FIX199-B: rollback on the existing-key path must restore the pre-update .env verbatim"
)

# FIX199-B: 钉键与改名解耦——容器键齐全非旧名（不触发改名）但
# 缺卷键的存量，走 update_panel 必须经无条件前置钉键把卷键钉为
# fake inspect 实读值，而不是落模板缺省挂空卷。两幕：常态与
# PANEL_IDENTITY_RENAME=0（改名被关）同验。旧实现下钉键只挂在
# 改名函数早退之后，两幕的卷键都不会被写入，本例必红。
test_panel_update_pins_keys_without_rename() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  assert_pinned_without_rename() {
    local dir="$1"
    grep -q '^FLMX_SQLITE_VOLUME=real_vol$' "$dir/.env" || fail "FIX199-B: a missing sqlite volume key must be pinned to the live container's mounted volume even when no rename triggers"
    grep -q '^PANEL_BACKEND_CONTAINER=flmx-panel-backend$' "$dir/.env" || fail "FIX199-B: existing current container keys must stay verbatim"
    if grep -q '身份改名' "$dir/update.log"; then
      fail "FIX199-B: pinning without a rename trigger must not enter the rename path"
    fi
  }

  local dir1 dir2
  dir1=$(mktemp -d)
  setup_rename_update_fixture "$dir1" current
  FIXTURE_SQLITE_VOLUME="real_vol"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir1/update.log" 2>&1 || fail "FIX199-B: update of a current-named keyless-volume deployment must succeed"
  assert_pinned_without_rename "$dir1"

  dir2=$(mktemp -d)
  setup_rename_update_fixture "$dir2" current
  FIXTURE_SQLITE_VOLUME="real_vol"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  PANEL_IDENTITY_RENAME=0 update_panel >"$dir2/update.log" 2>&1 || fail "FIX199-B: update with the rename disabled must still pin missing resource keys"
  assert_pinned_without_rename "$dir2"
)

# RN-1: 默认口令哨兵改名后的迁移判定——现行字面量
# flmx_panel_change_me 与 legacy 字面量 flux_panel_change_me 都必
# 须继续触发强制重生成（存量 .env 的旧值保护不丢），真实口令不
# 得被动。
test_panel_migrate_placeholder_password_sentinels() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_DEPLOY_DIR FLMX_INSTANCE
  check_docker() { DOCKER_CMD="fixture_compose"; return 0; }
  fixture_compose() { return 0; }
  sleep() { :; }
  docker() {
    case "$1" in
      ps)
        printf '%s\n' "flmx-panel-postgres" "flmx-panel-backend" "flmx-panel-frontend"
        return 0
        ;;
      inspect)
        case "$*" in
          *Health.Status*) printf 'healthy\n' ;;
          *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:v-test" ;;
        esac
        return 0
        ;;
    esac
    return 0
  }

  run_password_scenario() {
    local password="$1" expect_regen="$2"
    local work
    work=$(mktemp -d)
    cat > "$work/.env" <<EOF
JWT_SECRET=<redacted>
DB_TYPE=sqlite
POSTGRES_DB=flmx_panel
POSTGRES_USER=flmx_panel
POSTGRES_PASSWORD=$password
PANEL_BACKEND_CONTAINER=flmx-panel-backend
PANEL_FRONTEND_CONTAINER=flmx-panel-frontend
PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
EOF
    printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$work/docker-compose.yml"
    local rc="0"
    ( cd "$work" && migrate_to_postgres >"$work/migrate.log" 2>&1 ) || rc="$?"
    assert_equals "0" "$rc" "RN-1: migration must succeed in the password scenario"
    local final_password
    final_password=$(get_env_var POSTGRES_PASSWORD "$work/.env" || true)
    if [[ "$expect_regen" == "1" ]]; then
      if [[ -z "$final_password" || "$final_password" == "$password" ]]; then
        fail "RN-1: placeholder password ($password) must be regenerated during migration"
      fi
      grep -q '已生成随机口令' "$work/migrate.log" || fail "RN-1: password regeneration must be named in the migration output"
    else
      assert_equals "$password" "$final_password" "RN-1: a real password must survive migration unchanged"
    fi
  }

  run_password_scenario "flmx_panel_change_me" 1
  run_password_scenario "flux_panel_change_me" 1
  run_password_scenario "a-real-password-123" 0
)

# RR4-F7-03/F7-04: 迁移缺键命名分流＋失败回滚清理。有状态 docker
# 桩以存在性目录承接容器/卷的生灭（compose up 建容器与 PG 卷、
# down 只删容器、volume inspect/rm 按在册与否作答）：
# 第一幕（全新迁移）：.env 缺 POSTGRES_DB/USER 键、无任何 PG 卷、
# 哨兵口令触发重生成；pgloader 失败回滚必须清掉本次新建的 PG 容
# 器与卷、.env 逐字恢复——不清则卷里固化本次随机口令、.env 回到
# 哨兵，重试必吃 28P01。清旗重试必须成功且新库名走 flmx_panel
# （F7-03 全新分流，不把新库生为旧名）。
# 第二幕（半迁移存量）：PG 卷运行前已存在（已按旧名初始化），回
# 滚只清本次新建的 PG 容器、卷必须保留且不得过 docker volume rm；
# 重试成功且库名保持 flux_panel 与既有卷对齐（F7-03 存量分流）。
test_panel_migrate_volume_split_and_rollback_cleanup() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_DEPLOY_DIR FLMX_INSTANCE
  check_docker() { DOCKER_CMD="fixture_migrate_compose"; return 0; }
  sleep() { :; }

  fixture_migrate_compose() {
    local svc name
    case "$1" in
      down)
        rm -f "$FIXTURE_STATE/containers"/* 2>/dev/null || true
        return 0
        ;;
      up)
        shift
        for svc in "$@"; do
          case "$svc" in
            -d) ;;
            postgres)
              name=$(get_env_var PANEL_POSTGRES_CONTAINER || true)
              touch "$FIXTURE_STATE/containers/${name:-flmx-panel-postgres}"
              name=$(get_env_var FLMX_POSTGRES_VOLUME || true)
              touch "$FIXTURE_STATE/volumes/${name:-flmx-postgres-data}"
              ;;
            backend)
              name=$(get_env_var PANEL_BACKEND_CONTAINER || true)
              touch "$FIXTURE_STATE/containers/${name:-flmx-panel-backend}"
              ;;
            frontend)
              name=$(get_env_var PANEL_FRONTEND_CONTAINER || true)
              touch "$FIXTURE_STATE/containers/${name:-flmx-panel-frontend}"
              ;;
          esac
        done
        return 0
        ;;
    esac
    return 0
  }
  docker() {
    case "$1" in
      ps)
        ls "$FIXTURE_STATE/containers" 2>/dev/null || true
        return 0
        ;;
      inspect)
        if [[ "$2" == "-f" ]]; then
          case "$3" in
            *Health.Status*) printf 'healthy\n' ;;
            *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:v-test" ;;
          esac
          return 0
        fi
        [[ -e "$FIXTURE_STATE/containers/$2" ]]
        return
        ;;
      stop)
        return 0
        ;;
      rm)
        printf '%s\n' "$2" >> "$FIXTURE_STATE/rm.log"
        rm -f "$FIXTURE_STATE/containers/$2"
        return 0
        ;;
      volume)
        case "$2" in
          inspect) [[ -e "$FIXTURE_STATE/volumes/$3" ]]; return ;;
          rm)
            printf '%s\n' "$3" >> "$FIXTURE_STATE/volrm.log"
            rm -f "$FIXTURE_STATE/volumes/$3"
            return 0
            ;;
        esac
        return 0
        ;;
      run)
        case "$*" in
          *pgloader*)
            if [[ -f "$FIXTURE_STATE/pgloader-fail" ]]; then
              return 1
            fi
            return 0
            ;;
          *) return 0 ;;
        esac
        ;;
    esac
    return 0
  }

  setup_migrate_scenario() {
    local work="$1" password="$2" prevolume="$3"
    FIXTURE_STATE="$work/state"
    mkdir -p "$FIXTURE_STATE/containers" "$FIXTURE_STATE/volumes"
    : > "$FIXTURE_STATE/rm.log"
    : > "$FIXTURE_STATE/volrm.log"
    touch "$FIXTURE_STATE/containers/flmx-panel-backend" "$FIXTURE_STATE/containers/flmx-panel-frontend"
    if [[ "$prevolume" == "1" ]]; then
      touch "$FIXTURE_STATE/volumes/flmx-postgres-data"
    fi
    cat > "$work/.env" <<EOF
JWT_SECRET=<redacted>
DB_TYPE=sqlite
POSTGRES_PASSWORD=$password
FLMX_POSTGRES_VOLUME=flmx-postgres-data
PANEL_BACKEND_CONTAINER=flmx-panel-backend
PANEL_FRONTEND_CONTAINER=flmx-panel-frontend
PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
EOF
    printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n  frontend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test\n' > "$work/docker-compose.yml"
    cp "$work/.env" "$work/env-original"
  }

  local work1 work2 rc
  work1=$(mktemp -d)
  setup_migrate_scenario "$work1" "flmx_panel_change_me" 0
  : > "$work1/state/pgloader-fail"
  rc="0"
  ( cd "$work1" && migrate_to_postgres >"$work1/act1.log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "RR4-F7-04: a failed first migration must still fail"
  diff -q "$work1/env-original" "$work1/.env" >/dev/null || fail "RR4-F7-04: rollback must restore the pre-migration .env verbatim (sentinel password included)"
  [[ ! -e "$work1/state/containers/flmx-panel-postgres" ]] || fail "RR4-F7-04: rollback must remove the postgres container created by this run"
  [[ ! -e "$work1/state/volumes/flmx-postgres-data" ]] || fail "RR4-F7-04: rollback must remove the postgres volume created by this run (it holds this run's regenerated password while .env is back to the sentinel)"
  grep -Fxq 'flmx-postgres-data' "$work1/state/volrm.log" || fail "RR4-F7-04: the created volume must be removed through docker volume rm"
  grep -q '已清理本次迁移新建的 PostgreSQL 数据卷' "$work1/act1.log" || fail "RR4-F7-04: the rollback cleanup must be named in the migration output"
  [[ ! -e "$work1/.env.pre-postgres-migration" ]] || fail "RR4-F7-04: rollback must still delete the env snapshot"

  rm -f "$work1/state/pgloader-fail"
  rc="0"
  ( cd "$work1" && migrate_to_postgres >"$work1/act2.log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "RR4-F7-04: retry after rollback cleanup must succeed (no 28P01 trap state left behind)"
  grep -q '^POSTGRES_DB=flmx_panel$' "$work1/.env" || fail "RR4-F7-03: a fresh migration without POSTGRES_DB must create the database under the flmx name, not the legacy name"
  grep -q '^POSTGRES_USER=flmx_panel$' "$work1/.env" || fail "RR4-F7-03: a fresh migration without POSTGRES_USER must create the user under the flmx name, not the legacy name"
  grep -q '^DB_TYPE=postgres' "$work1/.env" || fail "RR4-F7-03: the retried migration must switch .env to postgres"
  [[ -e "$work1/state/volumes/flmx-postgres-data" ]] || fail "RR4-F7-04: a successful migration must keep its postgres volume"

  work2=$(mktemp -d)
  setup_migrate_scenario "$work2" "a-real-password-123" 1
  : > "$work2/state/pgloader-fail"
  rc="0"
  ( cd "$work2" && migrate_to_postgres >"$work2/act1.log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "RR4-F7-04: a failed migration on a half-migrated deployment must still fail"
  [[ -e "$work2/state/volumes/flmx-postgres-data" ]] || fail "RR4-F7-04: rollback must never remove a postgres volume that existed before this run"
  if grep -Fxq 'flmx-postgres-data' "$work2/state/volrm.log"; then
    fail "RR4-F7-04: a pre-existing postgres volume must not be passed to docker volume rm"
  fi
  [[ ! -e "$work2/state/containers/flmx-panel-postgres" ]] || fail "RR4-F7-04: rollback must remove the postgres container created by this run even when the volume pre-exists"

  rm -f "$work2/state/pgloader-fail"
  rc="0"
  ( cd "$work2" && migrate_to_postgres >"$work2/act2.log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "RR4-F7-03: retry on a half-migrated deployment must succeed"
  grep -q '^POSTGRES_DB=flux_panel$' "$work2/.env" || fail "RR4-F7-03: with a pre-existing postgres volume, a missing POSTGRES_DB must fall back to the legacy name the volume was initialized with"
  grep -q '^POSTGRES_USER=flux_panel$' "$work2/.env" || fail "RR4-F7-03: with a pre-existing postgres volume, a missing POSTGRES_USER must fall back to the legacy name the volume was initialized with"
)

# RN-2 接缝：存量旧布局（LEGACY_INSTALL_DIR＋旧二进制名＋旧服务
# 文件）在位时，菜单更新入口必须先整体迁移到新布局再走完更新：
# 目录搬移、二进制改新名、配置逐字不变、新 unit 只写新名、旧
# unit 摘除。归属以 TAKEOVER 显式确认（专测迁移机制本身）。
test_update_flmx_agent_migrates_legacy_layout() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  # A3-02 后显式版本须过格式校验：夹具预设的 v-test 不再合法，
  # 本例走真实 ensure 路径，须先给合法版本（只测更新机制本身，
  # 不测版本解析；SCRIPT-RED-A）。
  VERSION="v1.9.9"

  FLMX_AGENT_TAKEOVER="1"
  SERVICE_MANAGER="systemd"

  local root
  root=$(mktemp -d)
  INSTALL_DIR="$root/flmx_agent"
  LEGACY_INSTALL_DIR="$root/flux_agent"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$root/flmx_agent.service"
  LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="$root/flux_agent.service"

  mkdir -p "$LEGACY_INSTALL_DIR"
  cat > "$LEGACY_INSTALL_DIR/flux_agent" <<'EOF'
#!/bin/bash
if [[ "$1" == "-V" ]]; then echo "1.9.0"; else echo "legacy agent"; fi
EOF
  chmod +x "$LEGACY_INSTALL_DIR/flux_agent"
  printf '{\n  "addr": "wss://panel.example.com",\n  "secret": "s3cret"\n}\n' > "$LEGACY_INSTALL_DIR/config.json"
  printf 'node-state' > "$LEGACY_INSTALL_DIR/node-state.txt"
  printf '[Unit]\nDescription=legacy agent\n[Service]\nExecStart=%s/flux_agent\n' "$LEGACY_INSTALL_DIR" > "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE"
  cp -p "$LEGACY_INSTALL_DIR/config.json" "$root/config.before"

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

  update_flmx_agent >"$root/update.log" 2>&1 || \
    fail "RN-2 migration: the update entry must migrate the legacy layout and complete the update"

  grep -q '迁移到新布局' "$root/update.log" || \
    fail "RN-2 migration: the migration must be named in the update output"
  [[ -d "$INSTALL_DIR" ]] || fail "RN-2 migration: the new install dir must exist after migration"
  [[ ! -e "$LEGACY_INSTALL_DIR" ]] || fail "RN-2 migration: the legacy install dir must be gone after migration"
  [[ -f "$INSTALL_DIR/flmx_agent" ]] || fail "RN-2 migration: the binary must carry the new name in the new layout"
  [[ ! -e "$INSTALL_DIR/flux_agent" ]] || fail "RN-2 migration: the old binary name must not remain in the new layout"
  grep -q "new version" "$INSTALL_DIR/flmx_agent" || \
    fail "RN-2 migration: the update must still replace the binary after migrating the layout"
  diff -q "$root/config.before" "$INSTALL_DIR/config.json" >/dev/null || \
    fail "RN-2 migration: config.json must survive the migration byte-identical"
  [[ -f "$INSTALL_DIR/node-state.txt" ]] || fail "RN-2 migration: other state files must move with the directory"
  [[ -f "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" ]] || fail "RN-2 migration: the new service file must be written"
  if grep -q 'flux_agent' "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"; then
    fail "RN-2 migration: the new service file must not reference the legacy name"
  fi
  grep -q 'flmx_agent' "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" || \
    fail "RN-2 migration: the new service file must reference flmx_agent"
  [[ ! -e "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" ]] || fail "RN-2 migration: the legacy service file must be removed"
)

# INV-9（事故专项审计路 3）：菜单 4 迁移的成败判据不再只有
# pgloader 退出码——切换 .env 前逐表对账源（pgloader 实际读入的
# 预处理副本）↔目标行数。夹具以真实 SQLite 文件冒充数据卷：预
# 处理载荷与计数载荷都经 fake docker 在本机真实执行（与
# RR-LIVE-01 同手法），pgloader 桩按副本计数生成目标计数表
# （可经旗标令 node 表少一行模拟部分搬运），psql 计数经
# docker exec 桩查表。第一幕计数全等（含 schema_migration 副本
# 删行后的 2 行口径）→ 对账通过、迁移成功；第二幕 node 表目标
# 少一行 → 必须点名差异表与两侧计数、经快照回滚判失败、.env
# 逐字恢复。
test_panel_migrate_reconciles_row_counts() (
  set -euo pipefail
  command -v sqlite3 >/dev/null 2>&1 || \
    fail "INV-9: sqlite3 is required to execute the migration reconcile fixture"
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_DEPLOY_DIR FLMX_INSTANCE
  check_docker() { DOCKER_CMD="fixture_reconcile_compose"; return 0; }
  sleep() { :; }

  fixture_reconcile_compose() {
    local svc
    case "$1" in
      down)
        rm -f "$FIXTURE_STATE/containers"/* 2>/dev/null || true
        return 0
        ;;
      up)
        shift
        for svc in "$@"; do
          case "$svc" in
            -d) ;;
            postgres)
              touch "$FIXTURE_STATE/containers/flmx-panel-postgres"
              touch "$FIXTURE_STATE/volumes/flmx-postgres-data"
              ;;
            backend) touch "$FIXTURE_STATE/containers/flmx-panel-backend" ;;
            frontend) touch "$FIXTURE_STATE/containers/flmx-panel-frontend" ;;
          esac
        done
        return 0
        ;;
    esac
    return 0
  }

  sh_payload_of() {
    local prev="" arg
    for arg in "$@"; do
      if [[ "$prev" == "-c" ]]; then
        printf '%s' "$arg"
        return 0
      fi
      prev="$arg"
    done
    return 1
  }

  run_payload_locally() {
    # 以 bash -c 语义执行（$0=名、$1=库路径），与真实
    # docker run … sh -c '<payload>' <name> <db> 的位置参数口径一致；
    # bash <file> 形态会把名字错位进 $1（预处理载荷用字面路径未暴露此差）。
    local payload="$1" dbpath="$2" script_file rc
    script_file=$(mktemp)
    printf '%s' "$payload" | grep -v 'apk add' > "$script_file"
    bash -c "$(cat "$script_file")" flmx-fixture "$FIXTURE_VOL/${dbpath##*/}"
    rc=$?
    rm -f "$script_file"
    return $rc
  }

  run_preprocess_locally() {
    local payload="$1" script_file rc
    script_file=$(mktemp)
    printf '%s' "$payload" | grep -v 'apk add' | \
      sed -e "s|/sqlite/|$FIXTURE_VOL/|g" -e "s|/tmp/dump|$FIXTURE_WORK/dump|g" > "$script_file"
    bash "$script_file"
    rc=$?
    rm -f "$script_file"
    return $rc
  }

  fixture_pgloader_apply() {
    local src="$FIXTURE_VOL/gost.db" t c
    if [[ -f "$FIXTURE_VOL/gost-migrate.db" ]]; then
      src="$FIXTURE_VOL/gost-migrate.db"
    fi
    : > "$FIXTURE_STATE/pg-counts.txt"
    for t in $(sqlite3 "$src" "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name;"); do
      c=$(sqlite3 "$src" "SELECT COUNT(*) FROM \"$t\";")
      if [[ -f "$FIXTURE_STATE/pg-drop-node" && "$t" == "node" ]]; then
        c=$((c - 1))
      fi
      printf '%s|%s\n' "$t" "$c" >> "$FIXTURE_STATE/pg-counts.txt"
    done
    return 0
  }

  docker() {
    case "$1" in
      ps)
        ls "$FIXTURE_STATE/containers" 2>/dev/null || true
        return 0
        ;;
      inspect)
        if [[ "$2" == "-f" ]]; then
          case "$3" in
            *Health.Status*) printf 'healthy\n' ;;
            *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:v-test" ;;
          esac
          return 0
        fi
        [[ -e "$FIXTURE_STATE/containers/$2" ]]
        return
        ;;
      stop)
        return 0
        ;;
      exec)
        if [[ "$*" =~ FROM\ \"([^\"]+)\" ]]; then
          local tbl="${BASH_REMATCH[1]}" line=""
          line=$(grep "^${tbl}|" "$FIXTURE_STATE/pg-counts.txt" 2>/dev/null | head -n 1 || true)
          if [[ -n "$line" ]]; then
            printf '%s\n' "${line##*|}"
            return 0
          fi
        fi
        return 1
        ;;
      volume)
        case "$2" in
          inspect) [[ -e "$FIXTURE_STATE/volumes/$3" ]]; return ;;
          rm)
            rm -f "$FIXTURE_STATE/volumes/$3"
            return 0
            ;;
        esac
        return 0
        ;;
      run)
        local payload=""
        payload=$(sh_payload_of "$@") || payload=""
        case "$*" in
          *flmx-migrate-reconcile*)
            run_payload_locally "$payload" "${!#}"
            return
            ;;
          *pgloader*)
            fixture_pgloader_apply
            return
            ;;
          *"DROP INDEX IF EXISTS"*)
            run_preprocess_locally "$payload"
            return
            ;;
          *) return 0 ;;
        esac
        ;;
    esac
    return 0
  }

  setup_reconcile_scenario() {
    local work="$1"
    FIXTURE_STATE="$work/state"
    FIXTURE_VOL="$work/vol"
    FIXTURE_WORK="$work"
    mkdir -p "$FIXTURE_STATE/containers" "$FIXTURE_STATE/volumes" "$FIXTURE_VOL"
    touch "$FIXTURE_STATE/containers/flmx-panel-backend" "$FIXTURE_STATE/containers/flmx-panel-frontend"
    sqlite3 "$FIXTURE_VOL/gost.db" <<'EOF'
CREATE TABLE "user" (id INTEGER PRIMARY KEY, "user" TEXT);
INSERT INTO "user" VALUES (1, 'admin@user'), (2, 'alice'), (3, 'bob');
CREATE UNIQUE INDEX idx_user_username_fold_unique ON "user"(LOWER("user"));
CREATE TABLE node (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO node VALUES (1, 'n1'), (2, 'n2');
CREATE TABLE tunnel (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO tunnel VALUES (1, 't1');
CREATE TABLE user_subscription (id INTEGER PRIMARY KEY);
INSERT INTO user_subscription VALUES (1), (2), (3), (4);
CREATE TABLE peer_share_resource (id INTEGER PRIMARY KEY, release_legacy_family numeric DEFAULT 0);
CREATE TABLE schema_migration (version VARCHAR(64) PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_migration VALUES ('r5b4_v1_port_user_unique', 111);
INSERT INTO schema_migration VALUES ('k10_v5_username_casefold_unique', 222);
INSERT INTO schema_migration VALUES ('c12_v4_forward_lookup', 333);
EOF
    cat > "$work/.env" <<'EOF'
JWT_SECRET=<redacted>
DB_TYPE=sqlite
POSTGRES_DB=flmx_panel
POSTGRES_USER=flmx_panel
POSTGRES_PASSWORD=<redacted>
PANEL_BACKEND_CONTAINER=flmx-panel-backend
PANEL_FRONTEND_CONTAINER=flmx-panel-frontend
PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
FLMX_SQLITE_VOLUME=sqlite_data
EOF
    printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n  frontend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test\n' > "$work/docker-compose.yml"
    cp "$work/.env" "$work/env-original"
  }

  local work1 work2 rc
  work1=$(mktemp -d)
  setup_reconcile_scenario "$work1"
  rc="0"
  ( cd "$work1" && migrate_to_postgres >"$work1/migrate.log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "INV-9: migration with matching row counts must succeed"
  grep -q '迁移数据对账通过' "$work1/migrate.log" || fail "INV-9: a passed reconciliation must be named in the migration output"
  grep -q '^DB_TYPE=postgres' "$work1/.env" || fail "INV-9: a reconciled migration must switch .env to postgres"

  work2=$(mktemp -d)
  setup_reconcile_scenario "$work2"
  : > "$work2/state/pg-drop-node"
  rc="0"
  ( cd "$work2" && migrate_to_postgres >"$work2/migrate.log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "INV-9: a row-count mismatch must fail the migration even when pgloader exits 0"
  grep -q '表 node：源 2 行，目标 1 行' "$work2/migrate.log" || fail "INV-9: the mismatch must name the differing table with both counts"
  grep -q '数据对账未过' "$work2/migrate.log" || fail "INV-9: the reconciliation failure must go through the snapshot rollback path"
  diff -q "$work2/env-original" "$work2/.env" >/dev/null || fail "INV-9: rollback must restore the pre-migration .env verbatim"
  [[ ! -e "$work2/.env.pre-postgres-migration" ]] || fail "INV-9: rollback must delete the env snapshot"
)

# E4 窄子形：迁移源卷名此前只有「.env 键 → 字面旧名」两级解析、
# 不实读——键值漂移（键里是陈旧名、真库在另一卷）时按错卷读数
# 据。本例 .env 的 FLMX_SQLITE_VOLUME 故意写陈旧名，在跑后端的
# inspect 实读给出真实卷名：迁移必须点名冲突、按实读卷名挂载
# 全部数据面 docker run（备份/预处理/pgloader），陈旧名不得出
# 现在任何挂载里。
test_panel_migrate_source_volume_live_read_wins() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_DEPLOY_DIR FLMX_INSTANCE
  check_docker() { DOCKER_CMD="fixture_live_compose"; return 0; }
  fixture_live_compose() { return 0; }
  sleep() { :; }
  docker() {
    case "$1" in
      ps)
        printf '%s\n' "flmx-panel-postgres" "flmx-panel-backend" "flmx-panel-frontend"
        return 0
        ;;
      inspect)
        if [[ "$2" == "-f" ]]; then
          case "$3" in
            *Health.Status*) printf 'healthy\n' ;;
            *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:v-test" ;;
            *Mounts*) printf 'real_sqlite_vol' ;;
          esac
          return 0
        fi
        return 0
        ;;
      run)
        local arg
        for arg in "$@"; do
          case "$arg" in
            *:/sqlite|*:/sqlite:*|*:/data|*:/data:*) printf '%s\n' "$arg" >> "$FIXTURE_STATE/run-mounts.log" ;;
          esac
        done
        return 0
        ;;
      volume)
        return 1
        ;;
    esac
    return 0
  }

  local work rc
  work=$(mktemp -d)
  FIXTURE_STATE="$work/state"
  mkdir -p "$FIXTURE_STATE"
  : > "$FIXTURE_STATE/run-mounts.log"
  cat > "$work/.env" <<'EOF'
JWT_SECRET=<redacted>
DB_TYPE=sqlite
POSTGRES_DB=flmx_panel
POSTGRES_USER=flmx_panel
POSTGRES_PASSWORD=<redacted>
PANEL_BACKEND_CONTAINER=flmx-panel-backend
PANEL_FRONTEND_CONTAINER=flmx-panel-frontend
PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
FLMX_SQLITE_VOLUME=stale_sqlite_vol
EOF
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n  frontend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test\n' > "$work/docker-compose.yml"

  rc="0"
  ( cd "$work" && migrate_to_postgres >"$work/migrate.log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "E4: migration must succeed on the live-read volume"
  grep -q '迁移源卷名冲突' "$work/migrate.log" || fail "E4: the key/live-read volume conflict must be named in the migration output"
  grep -q 'stale_sqlite_vol' "$work/migrate.log" || fail "E4: the conflict notice must name the key-resolved (stale) volume"
  grep -q 'real_sqlite_vol' "$work/migrate.log" || fail "E4: the conflict notice must name the live-read volume"
  grep -Fxq 'real_sqlite_vol:/data' "$FIXTURE_STATE/run-mounts.log" || fail "E4: the backup run must mount the live-read volume"
  grep -Fxq 'real_sqlite_vol:/sqlite' "$FIXTURE_STATE/run-mounts.log" || fail "E4: the pgloader run must mount the live-read volume"
  if grep -q 'stale_sqlite_vol' "$FIXTURE_STATE/run-mounts.log"; then
    fail "E4: the stale key-resolved volume must not be mounted by any data run"
  fi
)

# R-1（事故专项审计路 5）：布局迁移中途失败必须自动回滚——旧
# 实现摘旧 unit 后 mv 失败、或迁后判活失败都只点名人工处理，
# 节点停在离线态。本例两幕共用 systemd 桩（新服务恒判死、旧服
# 务恒判活）：第一幕迁后判活失败 → 目录搬回、文件名改回、旧
# unit 逐字还原、新 unit 摘除、回执点名已自动回滚；第二幕经旗
# 标令整目录 mv 失败 → 目录未动、旧 unit 同样逐字还原并拉起。
test_migrate_legacy_agent_rolls_back_on_failure() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  sleep() { :; }
  systemctl() {
    case "$*" in
      *MainPID*flmx_agent*) echo 0 ;;
      *MainPID*flux_agent*) echo 4242 ;;
      *"is-active --quiet flmx_agent") return 1 ;;
      *"is-active --quiet flux_agent") return 0 ;;
    esac
    return 0
  }
  mv() {
    if [[ -n "${MV_FAIL:-}" && "$1" == "$LEGACY_INSTALL_DIR" && "$2" == "$INSTALL_DIR" ]]; then
      return 1
    fi
    command mv "$@"
  }

  setup_legacy_layout() {
    local root="$1"
    INSTALL_DIR="$root/flmx_agent"
    LEGACY_INSTALL_DIR="$root/flux_agent"
    FLMX_AGENT_SYSTEMD_SERVICE_FILE="$root/flmx_agent.service"
    LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="$root/flux_agent.service"
    mkdir -p "$LEGACY_INSTALL_DIR"
    printf '#!/bin/bash\necho legacy agent\n' > "$LEGACY_INSTALL_DIR/flux_agent"
    chmod +x "$LEGACY_INSTALL_DIR/flux_agent"
    printf '{\n  "addr": "wss://panel.example.com",\n  "secret": "s3cret"\n}\n' > "$LEGACY_INSTALL_DIR/config.json"
    printf 'node-state' > "$LEGACY_INSTALL_DIR/node-state.txt"
    printf '[Unit]\nDescription=legacy agent\n[Service]\nExecStart=%s/flux_agent\n' "$LEGACY_INSTALL_DIR" > "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE"
    cp -p "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" "$root/unit.before"
    cp -p "$LEGACY_INSTALL_DIR/config.json" "$root/config.before"
  }

  local root1 rc
  root1=$(mktemp -d)
  setup_legacy_layout "$root1"
  MV_FAIL=""
  rc="0"
  migrate_legacy_agent_install >"$root1/migrate.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R-1: a migration whose new service never becomes healthy must fail"
  grep -q '已自动回滚' "$root1/migrate.log" || fail "R-1: the health-failure path must report the automatic rollback"
  [[ -d "$LEGACY_INSTALL_DIR" ]] || fail "R-1: rollback must move the install dir back to the legacy layout"
  [[ ! -e "$INSTALL_DIR" ]] || fail "R-1: rollback must not leave the new layout dir behind"
  [[ -f "$LEGACY_INSTALL_DIR/flux_agent" ]] || fail "R-1: rollback must rename the binary back to the legacy name"
  [[ ! -e "$LEGACY_INSTALL_DIR/flmx_agent" ]] || fail "R-1: rollback must not leave the new binary name in the legacy layout"
  diff -q "$root1/config.before" "$LEGACY_INSTALL_DIR/config.json" >/dev/null || fail "R-1: rollback must keep config.json byte-identical"
  [[ -f "$LEGACY_INSTALL_DIR/node-state.txt" ]] || fail "R-1: rollback must keep the other state files"
  diff -q "$root1/unit.before" "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" >/dev/null || fail "R-1: rollback must restore the legacy unit file byte-identical from the snapshot"
  [[ ! -e "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" ]] || fail "R-1: rollback must remove the new service file"

  local root2
  root2=$(mktemp -d)
  setup_legacy_layout "$root2"
  MV_FAIL="1"
  rc="0"
  migrate_legacy_agent_install >"$root2/migrate.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R-1: a failed directory move must fail the migration"
  grep -q '迁移安装目录失败' "$root2/migrate.log" || fail "R-1: the move failure must be named in the migration output"
  grep -q '已自动回滚' "$root2/migrate.log" || fail "R-1: the move-failure path must report the automatic rollback"
  [[ -d "$LEGACY_INSTALL_DIR" ]] || fail "R-1: after a failed move the legacy dir must still be in place"
  diff -q "$root2/config.before" "$LEGACY_INSTALL_DIR/config.json" >/dev/null || fail "R-1: after a failed move config.json must be untouched"
  diff -q "$root2/unit.before" "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" >/dev/null || fail "R-1: after a failed move the legacy unit file must be restored byte-identical"
  MV_FAIL=""
)

# R-3（事故专项审计路 5）：install 整覆 config.json 必须保全存
# 量协议计数 http/tls/socks（与 agent 侧 map 合并保全对齐）——
# 身份（addr/secret）仍由本次参数决定，计数逐字带回；无存量时
# 输出与旧实现逐字一致（不发明字段）；非数字的存量值不保全。
test_install_flmx_agent_config_preserves_protocol_counts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVER_ADDR="wss://panel.example.com"
  SECRET="s3cret"
  PROXY_URL=""
  PROXY_ENABLED=""

  local work cfg actual expected
  work=$(mktemp -d)
  cfg="$work/config.json"

  printf '{\n  "addr": "wss://old.example.com",\n  "secret": "oldsecret",\n  "http": 2,\n  "tls": 0,\n  "socks": 5\n}\n' > "$cfg"
  write_flmx_agent_config "$cfg"
  actual=$(<"$cfg")
  expected=$'{\n  "addr": "wss://panel.example.com",\n  "secret": "s3cret",\n  "http": 2,\n  "tls": 0,\n  "socks": 5\n}'
  assert_equals "$expected" "$actual" "R-3: rewriting config.json must preserve the stored protocol counts"

  rm -f "$cfg"
  write_flmx_agent_config "$cfg"
  actual=$(<"$cfg")
  expected=$'{\n  "addr": "wss://panel.example.com",\n  "secret": "s3cret"\n}'
  assert_equals "$expected" "$actual" "R-3: a fresh config.json must keep the exact legacy two-field shape"

  printf '{\n  "addr": "wss://old.example.com",\n  "secret": "oldsecret",\n  "http": "many",\n  "tls": 7\n}\n' > "$cfg"
  write_flmx_agent_config "$cfg"
  actual=$(<"$cfg")
  expected=$'{\n  "addr": "wss://panel.example.com",\n  "secret": "s3cret",\n  "tls": 7\n}'
  assert_equals "$expected" "$actual" "R-3: non-numeric stored counts must not be preserved"
)


# FIX199-D1（RA2-S-01／RA-1 L-2）：update 路径的卷键交叉核对——
# .env 已有卷键但与在跑容器实读挂载不一致时，旧实现逐字信任错
# 键（钉键只补缺、自检期望同源于键），挂错卷还报成功。本例两
# 幕：① update 全程——.env 写陈旧卷名 stale_vol、在跑后端实挂
# real_vol，更新必须点名冲突、以实读为准改写键并成功（旧实现
# 下自检期望 stale_vol 而重建挂 real_vol，自检必失败回滚）；
# ② pin 函数直测——PG 卷键同形冲突同样改写、实读不到时既有键
# 逐字保留、无冲突的网络键不被触碰。
test_panel_update_volume_key_conflict_live_read_wins() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local dir
  dir=$(mktemp -d)
  FIXTURE_ENV_SQLITE_VOLUME="stale_vol"
  setup_rename_update_fixture "$dir" current
  FIXTURE_SQLITE_VOLUME="real_vol"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir/update.log" 2>&1 || fail "FIX199-D1: update with a stale volume key contradicted by the live mount must succeed after live-read correction"
  grep -q '资源键冲突' "$dir/update.log" || fail "FIX199-D1: the key/live conflict must be named in the update output"
  grep -q 'stale_vol' "$dir/update.log" || fail "FIX199-D1: the conflict notice must name the stale key value"
  grep -q 'real_vol' "$dir/update.log" || fail "FIX199-D1: the conflict notice must name the live-read volume"
  grep -q '^FLMX_SQLITE_VOLUME=real_vol$' "$dir/.env" || fail "FIX199-D1: the volume key must be rewritten to the live-read value"
  grep -q '升级后卷名自检通过' "$dir/update.log" || fail "FIX199-D1: the post-update volume self-check must pass against the corrected key"

  local pdir
  pdir=$(mktemp -d)
  cat > "$pdir/.env" <<'EOF'
FLMX_SQLITE_VOLUME=kept_sqlite
FLMX_POSTGRES_VOLUME=stale_pg
FLMX_NETWORK=kept_net
COMPOSE_PROJECT_NAME=kept_project
EOF
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *"/app/data"*) printf 'kept_sqlite' ;;
        *"/var/lib/postgresql/data"*) printf 'live_pg' ;;
        *) printf '' ;;
      esac
      return 0
    fi
    return 0
  }
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || \
    fail "FIX199-D1: pinning must succeed in the PG-conflict scenario"
  grep -q '^FLMX_POSTGRES_VOLUME=live_pg$' "$pdir/.env" || fail "FIX199-D1: a stale postgres volume key must be rewritten to the live-read value"
  grep -q 'stale_pg' "$pdir/pin.log" || fail "FIX199-D1: the PG conflict notice must name the stale key value"
  grep -q 'live_pg' "$pdir/pin.log" || fail "FIX199-D1: the PG conflict notice must name the live-read volume"
  grep -q '^FLMX_SQLITE_VOLUME=kept_sqlite$' "$pdir/.env" || fail "FIX199-D1: a volume key matching the live mount must stay verbatim"
  grep -q '^FLMX_NETWORK=kept_net$' "$pdir/.env" || fail "FIX199-D1: the network key has no live-read-wins semantics and must stay verbatim"

  # 实读不到（inspect 全失败）→ 既有键逐字保留，不臆断改写
  printf 'FLMX_SQLITE_VOLUME=stale2\n' > "$pdir/.env"
  docker() { return 1; }
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c >> pin.log 2>&1 ) || \
    fail "FIX199-D1: pinning must not fail when the live mount is unreadable"
  grep -q '^FLMX_SQLITE_VOLUME=stale2$' "$pdir/.env" || fail "FIX199-D1: an existing key must be kept verbatim when the live mount cannot be read"
)

# FIX199-D1 事故态门（RA2-S-01 变体）：无键存量、后端却坐在新模
# 板缺省卷 flmx-sqlite-data 上、旧名卷 sqlite_data 存在且含
# gost.db——正是 1.9.8 事故后未救援的形态。钉键若照常把
# flmx-sqlite-data 钉入，事故态即被正式化、自检也以错卷为期望。
# 必须 fail-closed：更新中止、点名两卷与人工钉键方法、容器未
# 动、.env 未写入卷键。反面一幕：sqlite_data 无 gost.db（无真库
# 证据）时门不触发，更新照常走通并钉现行卷名。
test_panel_update_aborts_on_accident_state_volume() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  wrap_volume_docker() {
    eval "$(declare -f docker | sed '1s/^docker /fixture_docker_base /')"
    docker() {
      if [[ "$1" == "volume" && "$2" == "inspect" ]]; then
        if [[ "$3" == "-f" ]]; then
          [[ -e "$FIXTURE_VOLROOT/$5" ]] || return 1
          printf '%s' "$FIXTURE_VOLROOT/$5"
          return 0
        fi
        [[ -e "$FIXTURE_VOLROOT/$3" ]]
        return
      fi
      fixture_docker_base "$@"
    }
  }

  local dir1
  dir1=$(mktemp -d)
  setup_rename_update_fixture "$dir1" keyless
  FIXTURE_VOLROOT="$dir1/volroot"
  mkdir -p "$FIXTURE_VOLROOT/sqlite_data"
  : > "$FIXTURE_VOLROOT/sqlite_data/gost.db"
  wrap_volume_docker
  FIXTURE_SQLITE_VOLUME="flmx-sqlite-data"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  local rc="0"
  update_panel >"$dir1/update.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "FIX199-D1: update must abort when a keyless backend sits on the template-default volume while the legacy volume holds the database"
  grep -q '资源键钉值中止' "$dir1/update.log" || fail "FIX199-D1: the accident-state gate must be named in the update output"
  grep -q 'FLMX_SQLITE_VOLUME=sqlite_data' "$dir1/update.log" || fail "FIX199-D1: the gate notice must name the manual pin remedy for the legacy volume"
  grep -q 'flmx-sqlite-data' "$dir1/update.log" || fail "FIX199-D1: the gate notice must name the template-default volume"
  if grep -q '^FLMX_SQLITE_VOLUME=' "$dir1/.env"; then
    fail "FIX199-D1: the gate must abort before any volume key is written"
  fi
  [[ ! -s "$dir1/rm.log" ]] || fail "FIX199-D1: the gate must abort before any container is removed"

  local dir2
  dir2=$(mktemp -d)
  setup_rename_update_fixture "$dir2" keyless
  FIXTURE_VOLROOT="$dir2/volroot"
  mkdir -p "$FIXTURE_VOLROOT/sqlite_data"
  wrap_volume_docker
  FIXTURE_SQLITE_VOLUME="flmx-sqlite-data"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir2/update.log" 2>&1 || fail "FIX199-D1: update must proceed when the legacy volume carries no database file"
  grep -q '^FLMX_SQLITE_VOLUME=flmx-sqlite-data$' "$dir2/.env" || fail "FIX199-D1: without database evidence the live volume must be pinned as usual"
)

# R1-01-M1 事故态门 PG 格（批 D 只关了脚本面×SQLite 一格，本例
# 补 PG 格）：无键存量、PG 容器却坐在新模板缺省卷 flmx-
# postgres-data 上、旧名卷 postgres_data 存在且含 PG_VERSION
# （真实 PG 数据目录）——钉键照常会把 flmx-postgres-data 钉入、
# 或按卷存在性回退把 postgres_data 钉入冒充现行卷，两者都把事
# 故态正式化。必须 fail-closed：更新中止、点名两卷与人工钉键
# 方法、.env 未写入 PG 卷键。反面一幕：postgres_data 无
# PG_VERSION（无真库证据）时门不触发，实读卷名照常钉入。
test_panel_update_aborts_on_pg_accident_state_volume() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  wrap_volume_docker() {
    eval "$(declare -f docker | sed '1s/^docker /fixture_docker_base /')"
    docker() {
      if [[ "$1" == "volume" && "$2" == "inspect" ]]; then
        if [[ "$3" == "-f" ]]; then
          [[ -e "$FIXTURE_VOLROOT/$5" ]] || return 1
          printf '%s' "$FIXTURE_VOLROOT/$5"
          return 0
        fi
        [[ -e "$FIXTURE_VOLROOT/$3" ]]
        return
      fi
      fixture_docker_base "$@"
    }
  }

  local dir1
  dir1=$(mktemp -d)
  setup_rename_update_fixture "$dir1" keyless
  FIXTURE_VOLROOT="$dir1/volroot"
  mkdir -p "$FIXTURE_VOLROOT/postgres_data"
  : > "$FIXTURE_VOLROOT/postgres_data/PG_VERSION"
  wrap_volume_docker
  FIXTURE_POSTGRES_VOLUME="flmx-postgres-data"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  local rc="0"
  update_panel >"$dir1/update.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "R1-01-M1: update must abort when a keyless PG container sits on the template-default volume while the legacy volume holds a real PG data directory"
  grep -q '资源键钉值中止' "$dir1/update.log" || fail "R1-01-M1: the PG accident-state gate must be named in the update output"
  grep -q 'FLMX_POSTGRES_VOLUME=postgres_data' "$dir1/update.log" || fail "R1-01-M1: the gate notice must name the manual pin remedy for the legacy PG volume"
  grep -q 'flmx-postgres-data' "$dir1/update.log" || fail "R1-01-M1: the gate notice must name the template-default PG volume"
  if grep -q '^FLMX_POSTGRES_VOLUME=' "$dir1/.env"; then
    fail "R1-01-M1: the gate must abort before any PG volume key is written"
  fi

  local dir2
  dir2=$(mktemp -d)
  setup_rename_update_fixture "$dir2" keyless
  FIXTURE_VOLROOT="$dir2/volroot"
  mkdir -p "$FIXTURE_VOLROOT/postgres_data"
  wrap_volume_docker
  FIXTURE_POSTGRES_VOLUME="flmx-postgres-data"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  update_panel >"$dir2/update.log" 2>&1 || fail "R1-01-M1: update must proceed when the legacy PG volume carries no PG_VERSION evidence"
  grep -q '^FLMX_POSTGRES_VOLUME=flmx-postgres-data$' "$dir2/.env" || fail "R1-01-M1: without PG data evidence the live PG volume must be pinned as usual"
)

# R1-01-L5 PG bind 收敛（钉值级）：PG 模式＋PG 卷键已有值＋PG
# 容器在场但数据挂载实读为空（bind）——旧行为逐字信键、重建落
# 错键空卷还报成功；现与 Go 面统一为钉键前中止点名。反面两幕：
# SQLite 模式下 PG 容器惰性不拦、键保留；PG 模式但挂载实读与
# 键一致时照常通过。
test_panel_pin_pg_bind_declared_key_aborts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local pdir
  pdir=$(mktemp -d)
  cat > "$pdir/.env" <<'EOF'
DB_TYPE=postgres
FLMX_SQLITE_VOLUME=kept_sqlite
FLMX_POSTGRES_VOLUME=declared_pg
FLMX_NETWORK=kept_net
COMPOSE_PROJECT_NAME=kept_project
EOF
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *"/app/data"*) printf 'kept_sqlite' ;;
        *) printf '' ;;
      esac
      return 0
    fi
    return 0
  }
  local rc="0"
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "R1-01-L5: pinning must abort when a declared PG volume key cannot be verified against a bind-mounted PG container"
  grep -q '资源键钉值中止' "$pdir/pin.log" || fail "R1-01-L5: the bind abort must be named in the pin output"
  grep -q 'declared_pg' "$pdir/pin.log" || fail "R1-01-L5: the bind abort must name the declared key value"
  grep -q '^FLMX_POSTGRES_VOLUME=declared_pg$' "$pdir/.env" || fail "R1-01-L5: the abort must leave the declared key untouched"

  # SQLite 模式：PG 容器惰性，同形态不拦、键逐字保留
  printf 'DB_TYPE=sqlite\n' | cat - <(grep -v '^DB_TYPE=' "$pdir/.env") > "$pdir/.env.new" && mv "$pdir/.env.new" "$pdir/.env"
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c >> pin.log 2>&1 ) || \
    fail "R1-01-L5: pinning must not gate on the PG container in sqlite mode"
  grep -q '^FLMX_POSTGRES_VOLUME=declared_pg$' "$pdir/.env" || fail "R1-01-L5: the declared PG key must stay verbatim in sqlite mode"

  # PG 模式但实读与键一致：照常通过（docker 桩改答实挂 declared_pg）
  printf 'DB_TYPE=postgres\n' | cat - <(grep -v '^DB_TYPE=' "$pdir/.env") > "$pdir/.env.new" && mv "$pdir/.env.new" "$pdir/.env"
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *"/app/data"*) printf 'kept_sqlite' ;;
        *"/var/lib/postgresql/data"*) printf 'declared_pg' ;;
        *) printf '' ;;
      esac
      return 0
    fi
    return 0
  }
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c >> pin.log 2>&1 ) || \
    fail "R1-01-L5: pinning must pass when the live PG mount matches the declared key"
)

# R1-01-L1 网络钉键多网络废名：后端挂 ≥2 个网络时，旧模板无分
# 隔拼接全部网络名钉入 FLMX_NETWORK（废串）。现逐行计数、恰一
# 个才钉实读值。A2-09 残面（audit-max）续收口：多个时不再静默
# 钉旧缺省 gost-network——与 Go 面 inspectLegacyResourceNames
# 同口径钉无名并点名告警（真实网络为自定义名时静默改钉会与旧
# 网络上的外部组件失联）；零个（实读为空）保留旧缺省回退但同
# 样点名、不静默。三幕在钉值级验：多网络 → 不写 FLMX_NETWORK
# 键＋告警点名各网络；单网络 → 钉实读名；零网络 → 钉旧缺省＋
# 告警。
test_panel_pin_network_multi_network_not_concatenated() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local pdir
  pdir=$(mktemp -d)
  cat > "$pdir/.env" <<'EOF'
FLMX_SQLITE_VOLUME=kept_sqlite
FLMX_POSTGRES_VOLUME=kept_pg
COMPOSE_PROJECT_NAME=kept_project
DB_TYPE=sqlite
EOF
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *"/app/data"*) printf 'kept_sqlite' ;;
        *"/var/lib/postgresql/data"*) printf 'kept_pg' ;;
        *"NetworkSettings.Networks"*) printf 'net_a\nnet_b\n' ;;
        *) printf '' ;;
      esac
      return 0
    fi
    return 0
  }
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c > pin.log 2>&1 ) || \
    fail "R1-01-L1: pinning must succeed in the multi-network scenario"
  if grep -q '^FLMX_NETWORK=' "$pdir/.env"; then
    fail "A2-09R: a multi-network read must pin no network key at all (matching the Go face), never a fallback or concatenated name"
  fi
  grep -q '不钉 FLMX_NETWORK' "$pdir/pin.log" || fail "A2-09R: the multi-network skip must be named in the pin output, not silent"
  grep -q 'net_a' "$pdir/pin.log" || fail "A2-09R: the multi-network warning must name the networks that were read"
  if grep -q 'net_anet_b' "$pdir/.env"; then
    fail "R1-01-L1: the pinned network must never be a concatenation of network names"
  fi

  # 零网络（实读为空）：保留缺省回退，但必须点名、不静默；回退值
  # 按在盘 compose 世代取模板实际网络名——旧实现无条件钉
  # gost-network，正是族七 C4-中·网络名缺省立案的错（现行模板实
  # 名为 flmx-network，gost-network 只是键名）。现行模板钉
  # flmx-network、旧模板钉 gost-network，两幕分验。
  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *"/app/data"*) printf 'kept_sqlite' ;;
        *"/var/lib/postgresql/data"*) printf 'kept_pg' ;;
        *) printf '' ;;
      esac
      return 0
    fi
    return 0
  }
  printf 'networks:\n  gost-network:\n    name: ${FLMX_NETWORK:-flmx-network}\n# FLMX_SQLITE_VOLUME\n' > "$pdir/docker-compose.yml"
  printf 'FLMX_SQLITE_VOLUME=kept_sqlite\nFLMX_POSTGRES_VOLUME=kept_pg\nCOMPOSE_PROJECT_NAME=kept_project\nDB_TYPE=sqlite\n' > "$pdir/.env"
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c >> pin.log 2>&1 ) || \
    fail "A2-09R: pinning must succeed in the zero-network scenario"
  grep -q '^FLMX_NETWORK=flmx-network$' "$pdir/.env" || fail "A2-09R: an unreadable network read on a current template must fall back to flmx-network, not the legacy default"
  grep -q '未能实读' "$pdir/pin.log" || fail "A2-09R: the zero-network fallback must be named in the pin output, not silent"
  printf 'networks:\n  gost-network:\n    name: gost-network\n' > "$pdir/docker-compose.yml"
  printf 'FLMX_SQLITE_VOLUME=kept_sqlite\nFLMX_POSTGRES_VOLUME=kept_pg\nCOMPOSE_PROJECT_NAME=kept_project\nDB_TYPE=sqlite\n' > "$pdir/.env"
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c >> pin.log 2>&1 ) || \
    fail "A2-09R: pinning must succeed in the zero-network legacy-template scenario"
  grep -q '^FLMX_NETWORK=gost-network$' "$pdir/.env" || fail "A2-09R: an unreadable network read on a legacy template must fall back to gost-network"

  docker() {
    if [[ "$1" == "inspect" && "$2" == "-f" ]]; then
      case "$3" in
        *"/app/data"*) printf 'kept_sqlite' ;;
        *"/var/lib/postgresql/data"*) printf 'kept_pg' ;;
        *"NetworkSettings.Networks"*) printf 'mynet\n' ;;
        *) printf '' ;;
      esac
      return 0
    fi
    return 0
  }
  printf 'FLMX_SQLITE_VOLUME=kept_sqlite\nFLMX_POSTGRES_VOLUME=kept_pg\nCOMPOSE_PROJECT_NAME=kept_project\nDB_TYPE=sqlite\n' > "$pdir/.env"
  ( cd "$pdir" && panel_pin_missing_resource_keys backend-c pg-c >> pin.log 2>&1 ) || \
    fail "R1-01-L1: pinning must succeed in the single-network scenario"
  grep -q '^FLMX_NETWORK=mynet$' "$pdir/.env" || fail "R1-01-L1: exactly one network must be pinned by its live-read name"
)

# FIX199-D5（RA-1 L-3）：改名开关双面口径——脚本只认进程环境时
# .env 里的 PANEL_IDENTITY_RENAME=0 形同虚设，同一部署在脚本面
# 改名、面板内面不改。现与 Go 面同序（.env 键在即赢、缺键回退
# 进程环境）。三幕：① .env 写 0、进程未设 → 不改名（旧实现必
# 改、本幕必红）；② .env 无键、进程设 0 → 不改名（既有口径回
# 归）；③ .env 写 1、进程设 0 → 以 .env 为准照常改名（钉死优
# 先序，与 Go 面 env 映射优先逐字一致）。
test_panel_identity_rename_switch_reads_env_file() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  assert_not_renamed() {
    local dir="$1"
    grep -q '^PANEL_BACKEND_CONTAINER=flux-panel-backend$' "$dir/.env" || fail "FIX199-D5: the backend key must stay legacy when the rename switch is off"
    if grep -q '身份改名' "$dir/update.log"; then
      fail "FIX199-D5: the rename path must not be entered when the switch is off"
    fi
    if grep -Fxq 'flux-panel-backend' "$dir/rm.log"; then
      fail "FIX199-D5: the legacy backend must not be removed when the switch is off"
    fi
  }

  local dir1 dir2 dir3
  dir1=$(mktemp -d)
  setup_rename_update_fixture "$dir1" legacy
  printf 'PANEL_IDENTITY_RENAME=0\n' >> "$dir1/.env"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  ( unset PANEL_IDENTITY_RENAME; update_panel ) >"$dir1/update.log" 2>&1 || fail "FIX199-D5: update must succeed with the rename disabled via .env"
  assert_not_renamed "$dir1"

  dir2=$(mktemp -d)
  setup_rename_update_fixture "$dir2" legacy
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  PANEL_IDENTITY_RENAME=0 update_panel >"$dir2/update.log" 2>&1 || fail "FIX199-D5: update must succeed with the rename disabled via process env"
  assert_not_renamed "$dir2"

  dir3=$(mktemp -d)
  setup_rename_update_fixture "$dir3" legacy
  printf 'PANEL_IDENTITY_RENAME=1\n' >> "$dir3/.env"
  VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  PANEL_IDENTITY_RENAME=0 update_panel >"$dir3/update.log" 2>&1 || fail "FIX199-D5: update must succeed when .env re-enables the rename"
  grep -q '^PANEL_BACKEND_CONTAINER=flmx-panel-backend$' "$dir3/.env" || fail "FIX199-D5: a .env value of 1 must win over a process-env 0, matching the Go face"
  grep -q '身份改名' "$dir3/update.log" || fail "FIX199-D5: the rename must be named when .env re-enables it"
)

# FIX199-D2（RA2-S-02）：迁移起步钉键——无键存量＋后端实挂自定
# 义名卷 legacy_sqlite_vol 时，迁移在任何 down/up 之前以实读把
# 卷键钉入 .env（随迁移快照走），使迁移失败回滚的 up 按实读卷
# 名解析、不落新模板缺省空卷。夹具 compose 记录每次 up 时 .env
# 的卷键值；pgloader 恒失败以强制回滚。旧实现迁移全程不钉键：
# 快照与回滚后 .env 都无卷键、回滚 up 记录 key=<missing>，本
# 例必红。
test_panel_migrate_pins_keys_before_any_up() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_DEPLOY_DIR FLMX_INSTANCE
  check_docker() { DOCKER_CMD="fixture_pinmig_compose"; return 0; }
  sleep() { :; }

  fixture_pinmig_compose() {
    local svc v
    case "$1" in
      down)
        rm -f "$FIXTURE_STATE/containers"/* 2>/dev/null || true
        return 0
        ;;
      up)
        v=$(get_env_var FLMX_SQLITE_VOLUME || true)
        printf 'up key=%s\n' "${v:-<missing>}" >> "$FIXTURE_STATE/up.log"
        shift
        for svc in "$@"; do
          case "$svc" in
            -d) ;;
            postgres) touch "$FIXTURE_STATE/containers/flmx-panel-postgres" ;;
            backend) touch "$FIXTURE_STATE/containers/flmx-panel-backend" ;;
            frontend) touch "$FIXTURE_STATE/containers/flmx-panel-frontend" ;;
          esac
        done
        return 0
        ;;
    esac
    return 0
  }
  docker() {
    case "$1" in
      ps)
        ls "$FIXTURE_STATE/containers" 2>/dev/null || true
        return 0
        ;;
      inspect)
        if [[ "$2" == "-f" ]]; then
          [[ -e "$FIXTURE_STATE/containers/$4" ]] || return 1
          case "$3" in
            *Health.Status*) printf 'healthy\n' ;;
            *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:v-test" ;;
            *Mounts*)
              case "$3" in
                *"/app/data"*) printf 'legacy_sqlite_vol' ;;
                *) printf '' ;;
              esac
              ;;
          esac
          return 0
        fi
        [[ -e "$FIXTURE_STATE/containers/$2" ]]
        return
        ;;
      stop)
        return 0
        ;;
      volume)
        case "$2" in
          inspect) [[ "$3" == "legacy_sqlite_vol" ]]; return ;;
        esac
        return 0
        ;;
      run)
        case "$*" in
          *pgloader*) return 1 ;;
          *) return 0 ;;
        esac
        ;;
    esac
    return 0
  }

  local work rc
  work=$(mktemp -d)
  FIXTURE_STATE="$work/state"
  mkdir -p "$FIXTURE_STATE/containers"
  touch "$FIXTURE_STATE/containers/flmx-panel-backend" "$FIXTURE_STATE/containers/flmx-panel-frontend"
  : > "$FIXTURE_STATE/up.log"
  cat > "$work/.env" <<'EOF'
JWT_SECRET=<redacted>
DB_TYPE=sqlite
POSTGRES_DB=flmx_panel
POSTGRES_USER=flmx_panel
POSTGRES_PASSWORD=<redacted>
PANEL_BACKEND_CONTAINER=flmx-panel-backend
PANEL_FRONTEND_CONTAINER=flmx-panel-frontend
PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
EOF
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n  frontend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test\n' > "$work/docker-compose.yml"

  rc="0"
  ( cd "$work" && migrate_to_postgres >"$work/migrate.log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "FIX199-D2: the migration must fail when pgloader fails"
  grep -q 'key=legacy_sqlite_vol' "$FIXTURE_STATE/up.log" || fail "FIX199-D2: every compose up (including the rollback up) must resolve the live-read volume via the pinned key"
  if grep -q 'key=<missing>' "$FIXTURE_STATE/up.log"; then
    fail "FIX199-D2: no compose up may run with the volume key missing (template default would mount an empty volume)"
  fi
  grep -q '^FLMX_SQLITE_VOLUME=legacy_sqlite_vol$' "$work/.env" || fail "FIX199-D2: rollback must restore an .env that carries the pinned live-read volume key"
  [[ ! -e "$work/.env.pre-postgres-migration" ]] || fail "FIX199-D2: rollback must delete the env snapshot"
)

# FIX199-D3（RA2-S-05＋S-04）：迁移对账双通道——通道一（容器内
# apk 装 sqlite）失败时必须走通道二（迁移已拉取的 pgloader 镜
# 像把同一源文件重装进一次性复核库、psql 逐表比复核库/目标行
# 数），仅双通道皆失败才降级。三幕共用 INV-9 式夹具（真 SQLite
# 文件、载荷本机真执行）：① 通道一死、通道二成 → 对账通过且
# 点名通道二、复核库被清理；② 双通道皆死 → 降级告警注明双通
# 道、迁移照常切换（旧实现告警只提单一通道，本幕文案断言必
# 红）；③ 通道一死、主搬运少搬 node 一行、通道二复算得出真
# 数 → 不一致必须判失败回滚（证明通道二是真门不是橡皮章）。
test_panel_migrate_reconcile_second_channel() (
  set -euo pipefail
  command -v sqlite3 >/dev/null 2>&1 || \
    fail "FIX199-D3: sqlite3 is required to execute the reconcile fixture"
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  unset PANEL_BACKEND_CONTAINER PANEL_FRONTEND_CONTAINER PANEL_POSTGRES_CONTAINER PANEL_DEPLOY_DIR FLMX_INSTANCE
  check_docker() { DOCKER_CMD="fixture_reconcile2_compose"; return 0; }
  sleep() { :; }

  fixture_reconcile2_compose() {
    local svc
    case "$1" in
      down)
        rm -f "$FIXTURE_STATE/containers"/* 2>/dev/null || true
        return 0
        ;;
      up)
        shift
        for svc in "$@"; do
          case "$svc" in
            -d) ;;
            postgres)
              touch "$FIXTURE_STATE/containers/flmx-panel-postgres"
              touch "$FIXTURE_STATE/volumes/flmx-postgres-data"
              ;;
            backend) touch "$FIXTURE_STATE/containers/flmx-panel-backend" ;;
            frontend) touch "$FIXTURE_STATE/containers/flmx-panel-frontend" ;;
          esac
        done
        return 0
        ;;
    esac
    return 0
  }

  sh_payload_of() {
    local prev="" arg
    for arg in "$@"; do
      if [[ "$prev" == "-c" ]]; then
        printf '%s' "$arg"
        return 0
      fi
      prev="$arg"
    done
    return 1
  }

  run_payload_locally() {
    local payload="$1" dbpath="$2" script_file rc
    script_file=$(mktemp)
    printf '%s' "$payload" | grep -v 'apk add' > "$script_file"
    bash -c "$(cat "$script_file")" flmx-fixture "$FIXTURE_VOL/${dbpath##*/}"
    rc=$?
    rm -f "$script_file"
    return $rc
  }

  run_preprocess_locally() {
    local payload="$1" script_file rc
    script_file=$(mktemp)
    printf '%s' "$payload" | grep -v 'apk add' | \
      sed -e "s|/sqlite/|$FIXTURE_VOL/|g" -e "s|/tmp/dump|$FIXTURE_WORK/dump|g" > "$script_file"
    bash "$script_file"
    rc=$?
    rm -f "$script_file"
    return $rc
  }

  fixture_pgloader_apply() {
    local out_file="$1" drop_flag="$2" src="$FIXTURE_VOL/gost.db" t c
    if [[ -f "$FIXTURE_VOL/gost-migrate.db" ]]; then
      src="$FIXTURE_VOL/gost-migrate.db"
    fi
    : > "$out_file"
    for t in $(sqlite3 "$src" "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name;"); do
      c=$(sqlite3 "$src" "SELECT COUNT(*) FROM \"$t\";")
      if [[ -n "$drop_flag" && -f "$drop_flag" && "$t" == "node" ]]; then
        c=$((c - 1))
      fi
      printf '%s|%s\n' "$t" "$c" >> "$out_file"
    done
    return 0
  }

  docker() {
    case "$1" in
      ps)
        ls "$FIXTURE_STATE/containers" 2>/dev/null || true
        return 0
        ;;
      inspect)
        if [[ "$2" == "-f" ]]; then
          case "$3" in
            *Health.Status*) printf 'healthy\n' ;;
            *Config.Image*) printf '%s\n' "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:v-test" ;;
          esac
          return 0
        fi
        [[ -e "$FIXTURE_STATE/containers/$2" ]]
        return
        ;;
      stop)
        return 0
        ;;
      exec)
        printf '%s\n' "$*" >> "$FIXTURE_STATE/exec.log"
        local db="" prev="" arg sql=""
        for arg in "$@"; do
          if [[ "$prev" == "-d" ]]; then db="$arg"; fi
          if [[ "$prev" == "-c" ]]; then sql="$arg"; fi
          prev="$arg"
        done
        case "$sql" in
          *"DATABASE"*)
            return 0
            ;;
          *pg_tables*)
            local counts_file="$FIXTURE_STATE/pg-counts.txt" line
            if [[ "$db" == "flmx_reconcile_scratch" ]]; then
              counts_file="$FIXTURE_STATE/scratch-counts.txt"
            fi
            while IFS='|' read -r line _; do
              printf '%s\n' "$line"
            done < "$counts_file"
            return 0
            ;;
        esac
        if [[ "$sql" =~ FROM\ \"([^\"]+)\" ]]; then
          local tbl="${BASH_REMATCH[1]}" counts_file2="$FIXTURE_STATE/pg-counts.txt" found=""
          if [[ "$db" == "flmx_reconcile_scratch" ]]; then
            counts_file2="$FIXTURE_STATE/scratch-counts.txt"
          fi
          found=$(grep "^${tbl}|" "$counts_file2" 2>/dev/null | head -n 1 || true)
          if [[ -n "$found" ]]; then
            printf '%s\n' "${found##*|}"
            return 0
          fi
        fi
        return 1
        ;;
      volume)
        case "$2" in
          inspect) [[ -e "$FIXTURE_STATE/volumes/$3" ]]; return ;;
          rm)
            rm -f "$FIXTURE_STATE/volumes/$3"
            return 0
            ;;
        esac
        return 0
        ;;
      run)
        local payload=""
        payload=$(sh_payload_of "$@") || payload=""
        case "$*" in
          *flmx-migrate-reconcile*)
            if [[ -f "$FIXTURE_STATE/ch1-fail" ]]; then
              return 1
            fi
            run_payload_locally "$payload" "${!#}"
            return
            ;;
          *flmx-reconcile.load*)
            if [[ -f "$FIXTURE_STATE/ch2-fail" ]]; then
              return 1
            fi
            fixture_pgloader_apply "$FIXTURE_STATE/scratch-counts.txt" ""
            return
            ;;
          *pgloader*)
            fixture_pgloader_apply "$FIXTURE_STATE/pg-counts.txt" "$FIXTURE_STATE/pg-drop-node"
            return
            ;;
          *"DROP INDEX IF EXISTS"*)
            run_preprocess_locally "$payload"
            return
            ;;
          *) return 0 ;;
        esac
        ;;
    esac
    return 0
  }

  setup_reconcile2_scenario() {
    local work="$1"
    FIXTURE_STATE="$work/state"
    FIXTURE_VOL="$work/vol"
    FIXTURE_WORK="$work"
    mkdir -p "$FIXTURE_STATE/containers" "$FIXTURE_STATE/volumes" "$FIXTURE_VOL"
    touch "$FIXTURE_STATE/containers/flmx-panel-backend" "$FIXTURE_STATE/containers/flmx-panel-frontend"
    : > "$FIXTURE_STATE/exec.log"
    sqlite3 "$FIXTURE_VOL/gost.db" <<'EOF'
CREATE TABLE "user" (id INTEGER PRIMARY KEY, "user" TEXT);
INSERT INTO "user" VALUES (1, 'admin@user'), (2, 'alice'), (3, 'bob');
CREATE UNIQUE INDEX idx_user_username_fold_unique ON "user"(LOWER("user"));
CREATE TABLE node (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO node VALUES (1, 'n1'), (2, 'n2');
CREATE TABLE tunnel (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO tunnel VALUES (1, 't1');
CREATE TABLE user_subscription (id INTEGER PRIMARY KEY);
INSERT INTO user_subscription VALUES (1), (2), (3), (4);
CREATE TABLE peer_share_resource (id INTEGER PRIMARY KEY, release_legacy_family numeric DEFAULT 0);
CREATE TABLE schema_migration (version VARCHAR(64) PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_migration VALUES ('r5b4_v1_port_user_unique', 111);
INSERT INTO schema_migration VALUES ('k10_v5_username_casefold_unique', 222);
INSERT INTO schema_migration VALUES ('c12_v4_forward_lookup', 333);
EOF
    cat > "$work/.env" <<'EOF'
JWT_SECRET=<redacted>
DB_TYPE=sqlite
POSTGRES_DB=flmx_panel
POSTGRES_USER=flmx_panel
POSTGRES_PASSWORD=<redacted>
PANEL_BACKEND_CONTAINER=flmx-panel-backend
PANEL_FRONTEND_CONTAINER=flmx-panel-frontend
PANEL_POSTGRES_CONTAINER=flmx-panel-postgres
FLMX_SQLITE_VOLUME=sqlite_data
EOF
    printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n  frontend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-vite-frontend:v-test\n' > "$work/docker-compose.yml"
    cp "$work/.env" "$work/env-original"
  }

  local work1 work2 work3 rc
  work1=$(mktemp -d)
  setup_reconcile2_scenario "$work1"
  : > "$work1/state/ch1-fail"
  rc="0"
  ( cd "$work1" && migrate_to_postgres >"$work1/migrate.log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "FIX199-D3: migration must succeed when channel one is down but channel two verifies the counts"
  grep -q '对账通过（通道二' "$work1/migrate.log" || fail "FIX199-D3: a channel-two pass must be named in the migration output"
  grep -q 'DROP DATABASE IF EXISTS flmx_reconcile_scratch' "$work1/state/exec.log" || fail "FIX199-D3: the scratch database must be dropped after the recount"
  grep -q '^DB_TYPE=postgres' "$work1/.env" || fail "FIX199-D3: a channel-two verified migration must switch .env to postgres"

  work2=$(mktemp -d)
  setup_reconcile2_scenario "$work2"
  : > "$work2/state/ch1-fail"
  : > "$work2/state/ch2-fail"
  rc="0"
  ( cd "$work2" && migrate_to_postgres >"$work2/migrate.log" 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "FIX199-D3: migration must still proceed on the loud downgrade when both channels are unavailable"
  grep -q '双通道均不可用' "$work2/migrate.log" || fail "FIX199-D3: the downgrade warning must name that both channels were unavailable"
  grep -q '^DB_TYPE=postgres' "$work2/.env" || fail "FIX199-D3: the downgraded migration must still switch .env to postgres"

  work3=$(mktemp -d)
  setup_reconcile2_scenario "$work3"
  : > "$work3/state/ch1-fail"
  : > "$work3/state/pg-drop-node"
  rc="0"
  ( cd "$work3" && migrate_to_postgres >"$work3/migrate.log" 2>&1 ) || rc="$?"
  assert_equals "1" "$rc" "FIX199-D3: a channel-two mismatch must fail the migration"
  grep -q '表 node：复核库 2 行，目标 1 行' "$work3/migrate.log" || fail "FIX199-D3: the channel-two mismatch must name the differing table with both counts"
  grep -q '数据对账未过' "$work3/migrate.log" || fail "FIX199-D3: the channel-two failure must go through the snapshot rollback path"
  diff -q "$work3/env-original" "$work3/.env" >/dev/null || fail "FIX199-D3: rollback must restore the pre-migration .env verbatim"
)

# FIX199-D4（RA2-S-06）：install.sh 布局迁移的旧 unit 快照改
# fail-closed——快照失败（mktemp/cp 失败）旧实现只置空快照、迁
# 移照常继续，此后回滚只能按模板重写旧 unit，人工改过的 unit
# 无法逐字还原且无点名。本例令快照 cp 恒失败：迁移必须在动
# 手前中止（旧服务不被停、目录未动、unit 逐字未变、新目录未
# 建），回执点名快照失败与中止。
test_migrate_legacy_agent_snapshot_failure_aborts() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  sleep() { :; }
  systemctl() {
    printf '%s\n' "$*" >> "$FIXTURE_CALLS"
    case "$*" in
      *MainPID*flmx_agent*) echo 0 ;;
      *MainPID*flux_agent*) echo 4242 ;;
      *"is-active --quiet flmx_agent") return 1 ;;
      *"is-active --quiet flux_agent") return 0 ;;
    esac
    return 0
  }
  cp() {
    if [[ -n "${CP_FAIL_SNAPSHOT:-}" && "${1:-}" == "-p" && "${2:-}" == "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" ]]; then
      return 1
    fi
    command cp "$@"
  }

  local root rc
  root=$(mktemp -d)
  FIXTURE_CALLS="$root/systemctl.log"
  : > "$FIXTURE_CALLS"
  INSTALL_DIR="$root/flmx_agent"
  LEGACY_INSTALL_DIR="$root/flux_agent"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$root/flmx_agent.service"
  LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="$root/flux_agent.service"
  mkdir -p "$LEGACY_INSTALL_DIR"
  printf '#!/bin/bash\necho legacy agent\n' > "$LEGACY_INSTALL_DIR/flux_agent"
  chmod +x "$LEGACY_INSTALL_DIR/flux_agent"
  printf '{\n  "addr": "wss://panel.example.com",\n  "secret": "s3cret"\n}\n' > "$LEGACY_INSTALL_DIR/config.json"
  printf '[Unit]\nDescription=legacy agent (hand-edited)\n[Service]\nExecStart=%s/flux_agent\n' "$LEGACY_INSTALL_DIR" > "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE"
  command cp -p "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" "$root/unit.before"

  CP_FAIL_SNAPSHOT="1"
  rc="0"
  migrate_legacy_agent_install >"$root/migrate.log" 2>&1 || rc="$?"
  assert_equals "1" "$rc" "FIX199-D4: a failed unit snapshot must abort the migration"
  grep -q '快照失败' "$root/migrate.log" || fail "FIX199-D4: the snapshot failure must be named in the migration output"
  grep -q '动手前中止' "$root/migrate.log" || fail "FIX199-D4: the receipt must state the migration aborted before any change"
  [[ -d "$LEGACY_INSTALL_DIR" ]] || fail "FIX199-D4: the legacy install dir must stay in place"
  [[ ! -e "$INSTALL_DIR" ]] || fail "FIX199-D4: the new install dir must not be created"
  diff -q "$root/unit.before" "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" >/dev/null || fail "FIX199-D4: the legacy unit file must stay byte-identical"
  if grep -q 'stop' "$FIXTURE_CALLS"; then
    fail "FIX199-D4: the legacy service must not be stopped when the snapshot fails"
  fi
  CP_FAIL_SNAPSHOT=""
)

# FIX199-D6（RA-3 #5）：强制重装的数据点名＋有数据确认——旧门
# 只证明「文件意图」，覆写前不再点名数据后果，且失败指引把容
# 器不存在一律断言为半成品，能把有数据的部署漏斗进强制重装。
# 三幕：① 卷内存 gost.db＋非 TTY → 中止、点名数据卷与非交互原
# 因、.env/compose 未被覆写（备份已生成）；② 卷存在但无数据
# → 无摩擦放行走完重装（R9-03 出口不堵）；③ 卷内有数据＋
# pty 交互：输入部署目录名 → 放行；输错 → 中止。
test_install_panel_force_reinstall_data_gate() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"
  # FIX-CI-2: 本例只验安装守卫/流程，不验选路与第二锚：下载单腿
  # 以 download_verified 桩替换，但选路（真实探针排名）与异路锚
  # 复核（真实 curl）仍是活的——宿主直连在实测排名落败、代理路
  # 线胜出时，第二锚对夹具假资产（example.com）给不出合法校验
  # 值，R2-01-01 fail-closed 会把安装判死，用例结果随宿主网络
  # 漂移（CI 实测 F-049 单红）。按套件既有口径以 PROXY_ENABLED=
  # false 钉死直连单路线，使结果与宿主网络无关。
  PROXY_ENABLED="false"

  gate_docker() {
    case "$1" in
      volume)
        case "$2" in
          inspect)
            if [[ "$3" == "-f" ]]; then
              [[ -e "$FIXTURE_VOLROOT/$5" ]] || return 1
              printf '%s' "$FIXTURE_VOLROOT/$5"
              return 0
            fi
            [[ -e "$FIXTURE_VOLROOT/$3" ]]
            return
            ;;
        esac
        return 0
        ;;
    esac
    case " $* " in
      *" --services "*) printf 'backend\nfrontend\n' ;;
    esac
    return 0
  }

  setup_gate_stubs() {
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
    docker() { gate_docker "$@"; }
  }
  setup_gate_stubs

  # 第一幕：有数据＋非 TTY → 中止、点名、文件未覆写
  local work1 rc
  work1=$(mktemp -d)
  FIXTURE_VOLROOT="$work1/volroot"
  mkdir -p "$FIXTURE_VOLROOT/sqlite_data"
  : > "$FIXTURE_VOLROOT/sqlite_data/gost.db"
  printf 'JWT_SECRET=%s\n' keep-this-env > "$work1/.env"
  printf 'services: {}\n' > "$work1/docker-compose.yml"
  rc="0"
  ( cd "$work1" && PANEL_FORCE_REINSTALL=1 install_panel </dev/null > install.log 2>&1 ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "FIX199-D6: forced reinstall with data in a volume must abort on a non-TTY"
  grep -q '数据点名' "$work1/install.log" || fail "FIX199-D6: existing data volumes must be named before any overwrite"
  grep -q 'sqlite_data' "$work1/install.log" || fail "FIX199-D6: the data-bearing volume must be named"
  grep -q '非交互终端' "$work1/install.log" || fail "FIX199-D6: the non-TTY abort reason must be named"
  grep -q 'keep-this-env' "$work1/.env" || fail "FIX199-D6: .env must not be overwritten when the gate aborts"
  if grep -q 'brandnewsecret' "$work1/.env"; then
    fail "FIX199-D6: .env must not carry the new install secrets when the gate aborts"
  fi
  ls "$work1"/.env.bak.* >/dev/null 2>&1 || fail "FIX199-D6: the pre-gate backup must exist so the recovery path is real"

  # 第二幕：卷存在但无数据 → 无摩擦放行
  local work2
  work2=$(mktemp -d)
  FIXTURE_VOLROOT="$work2/volroot"
  mkdir -p "$FIXTURE_VOLROOT/sqlite_data" "$FIXTURE_VOLROOT/postgres_data"
  printf 'JWT_SECRET=%s\n' keep-this-env > "$work2/.env"
  printf 'services: {}\n' > "$work2/docker-compose.yml"
  rc="0"
  ( cd "$work2" && PANEL_FORCE_REINSTALL=1 install_panel </dev/null > install.log 2>&1 ) || rc="$?"
  assert_equals "0" "$rc" "FIX199-D6: forced reinstall must proceed without friction when volumes carry no data"
  grep -q 'brandnewsecret' "$work2/.env" || fail "FIX199-D6: a no-data forced reinstall must complete the install"
  if grep -q '非交互终端' "$work2/install.log"; then
    fail "FIX199-D6: the non-TTY abort must not fire when no volume carries data"
  fi

  # 第三幕：有数据＋pty 交互确认（对/错两输）
  local base3 work3 harness
  base3=$(mktemp -d)
  work3="$base3/deploydir"
  mkdir -p "$work3"
  FIXTURE_VOLROOT="$work3/volroot"
  mkdir -p "$FIXTURE_VOLROOT/sqlite_data"
  : > "$FIXTURE_VOLROOT/sqlite_data/gost.db"
  printf 'JWT_SECRET=%s\n' keep-this-env > "$work3/.env"
  printf 'services: {}\n' > "$work3/docker-compose.yml"
  export E3_WORK="$work3" E3_VOLROOT="$FIXTURE_VOLROOT"
  harness="$base3/gate-harness.sh"
  {
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/panel_install.sh"
    cat <<'STUBEOF'
FIXTURE_VOLROOT="$E3_VOLROOT"
# FIX-CI-2: 子进程 harness 不继承父用例的 shell 变量，选路钉死须
# 在本块内自带（理由同父用例 FIX-CI-2 注：防实测选路漂移把第二
# 锚 fail-closed 误带进数据门用例）。
PROXY_ENABLED="false"
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
  case "$1" in
    volume)
      case "$2" in
        inspect)
          if [[ "$3" == "-f" ]]; then
            [[ -e "$FIXTURE_VOLROOT/$5" ]] || return 1
            printf '%s' "$FIXTURE_VOLROOT/$5"
            return 0
          fi
          [[ -e "$FIXTURE_VOLROOT/$3" ]]
          return
          ;;
      esac
      return 0
      ;;
  esac
  case " $* " in
    *" --services "*) printf 'backend\nfrontend\n' ;;
  esac
  return 0
}
cd "$E3_WORK" || exit 99
gate_rc="0"
PANEL_FORCE_REINSTALL=1 install_panel >/dev/null 2>&1 || gate_rc="$?"
printf '%s' "$gate_rc" > "$E3_WORK/gate-rc"
STUBEOF
  } > "$harness"
  rc="0"; pty_run $'deploydir\n' "$harness" || rc="$?"
  assert_equals "deploydir" "$(basename "$work3")" "FIX199-D6: fixture sanity (deploy dir name)"
  grep -q '^0$' "$work3/gate-rc" || fail "FIX199-D6: typing the deployment directory name must let the forced reinstall proceed"
  grep -q 'brandnewsecret' "$work3/.env" || fail "FIX199-D6: a confirmed forced reinstall must complete the install"

  printf 'JWT_SECRET=%s\n' keep-this-env > "$work3/.env"
  printf 'services: {}\n' > "$work3/docker-compose.yml"
  rm -f "$work3/gate-rc"
  rc="0"; pty_run $'wrongname\n' "$harness" || rc="$?"
  grep -q '^1$' "$work3/gate-rc" || fail "FIX199-D6: a mismatched confirmation must abort the forced reinstall"
  grep -q 'keep-this-env' "$work3/.env" || fail "FIX199-D6: .env must stay untouched after a mismatched confirmation"
)

# A3-02: 显式版本三入口（VERSION/FLMX_VERSION/
# PINNED_VERSION）拼下载 URL 前必须过严格格式校验——含 /、..、?、#
# 等能改变 URL 指向的畸形值一律拒绝（旧实现原样直通，../ 可逃出
# 钉死仓库且同源派生的 .sha256 使校验失效，LIVE-A S5 已实证）。
# build_download_url 内插前再断言一次（双点收口）。
test_install_script_rejects_malformed_explicit_versions() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local var value
  for var in VERSION FLMX_VERSION PINNED_VERSION; do
    for value in '../../../evil/repo/releases/download/9.9.9' '1.9.9/extra' '1.9.9?x=1' '1.9.9#x' '1.9.9 2.0.0' '..' 'v' '1.' 'a.b.c' '1.9.9%2e'; do
      VERSION="" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
      printf -v "$var" '%s' "$value"
      if resolve_version >/dev/null 2>&1; then
        fail "A3-02: $var='$value' must be rejected by resolve_version"
      fi
    done
  done

  # S1-F02：旧键 FLUX_VERSION 已退场，不再是显式入口——恶意值不
  # 得被选中，也不得导致拒绝（注入面本身已随读面摘除而消失）；
  # 须直落 latest，不回显该值。
  resolve_latest_release_tag() { echo "9.9.9"; }
  VERSION="" FLMX_VERSION="" FLUX_VERSION='../../../evil/repo/releases/download/9.9.9' PINNED_VERSION=""
  assert_equals "9.9.9" "$(resolve_version)" "A3-02/S1-F02: a malicious FLUX_VERSION value must be ignored (falls through to latest)"

  # 合法值放行（稳定渠道：可选 v 前缀的纯点分数字）。
  VERSION="1.9.9" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  assert_equals "1.9.9" "$(resolve_version)" "A3-02: plain version must pass validation"
  VERSION="v1.9.9" FLMX_VERSION="" FLUX_VERSION="" PINNED_VERSION=""
  assert_equals "v1.9.9" "$(resolve_version)" "A3-02: v-prefixed version must pass validation"

  # build_download_url 双点断言：RESOLVED_VERSION 被污染时不得拼 URL。
  RESOLVED_VERSION='../../../evil'
  if build_download_url >/dev/null 2>&1; then
    fail "A3-02: build_download_url must refuse a traversal RESOLVED_VERSION"
  fi
  RESOLVED_VERSION="1.9.9"
  assert_equals \
    "https://github.com/${REPO}/releases/download/1.9.9/gost-$(get_architecture)" \
    "$(build_download_url)" \
    "A3-02: canonical download URL for a valid version"
)

# A3-01: latest 版本解析不得单路线采信——首个给出 tag 的线路若是
# 第三方代理，其 tag 必须经另一条不同线路复核一致才可采信：异路
# 异值硬失败、异路全部无应答 fail-closed（旧实现首个合格 tag 即
# 用，任一线路回一个形态合法的旧 tag 就静默装旧版，LIVE-A S3 实
# 证）。直连（github.com 本体）当选时属规范源：异路异值同样硬失
# 败，无应答时点名后单源采信。桩 curl 按 URL 中的线路前缀供数，
# API 法一律失败、只走重定向法。
test_install_script_latest_resolution_requires_corroboration() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  PROXY_ENABLED=""
  PROXY_URL=""
  ROUTES_STAGE=2
  LATEST_B_MODE="same"

  curl() {
    local all="$*"
    case "$all" in
      *api.github.com*) return 22 ;;
      *url_effective*)
        case "$all" in
          *b.example*)
            case "$LATEST_B_MODE" in
              different) printf 'https://github.com/%s/releases/tag/1.9.0' "$REPO" ;;
              silent) return 22 ;;
              *) printf 'https://github.com/%s/releases/tag/1.9.9' "$REPO" ;;
            esac
            ;;
          *) printf 'https://github.com/%s/releases/tag/1.9.9' "$REPO" ;;
        esac
        ;;
      *) return 22 ;;
    esac
  }

  # 代理当选 + 异路同值 → 采信。
  DOWNLOAD_ROUTES=("https://a.example" "https://b.example")
  LATEST_B_MODE="same"
  assert_equals "1.9.9" "$(resolve_latest_release_tag 2>/dev/null)" \
    "A3-01: corroborated latest tag should be accepted"

  # 代理当选 + 异路异值 → 硬失败（旧 tag 不得被采信）。
  LATEST_B_MODE="different"
  if resolve_latest_release_tag >/dev/null 2>&1; then
    fail "A3-01: latest tag from a proxy must be rejected when another route disagrees"
  fi

  # 代理当选 + 异路全无应答 → fail-closed。
  LATEST_B_MODE="silent"
  if resolve_latest_release_tag >/dev/null 2>&1; then
    fail "A3-01: latest tag from a proxy must fail closed when no other route answers"
  fi

  # 直连当选 + 异路无应答 → 规范源点名后采信。
  DOWNLOAD_ROUTES=("" "https://b.example")
  LATEST_B_MODE="silent"
  assert_equals "1.9.9" "$(resolve_latest_release_tag 2>/dev/null)" \
    "A3-01: direct-sourced latest tag should be accepted when proxies are silent"

  # 直连当选 + 异路异值 → 同样硬失败。
  LATEST_B_MODE="different"
  if resolve_latest_release_tag >/dev/null 2>&1; then
    fail "A3-01: latest tag must be rejected when another route reports a different tag"
  fi
)

# A3-04: install 覆盖存量 agent 必须与 update 同保护集——防降级、
# 替换前 .bak 备份、替换后 -V 烟雾、判活失败自动回滚。旧实现四项
# 全无：mv 直覆盖、-V 只打印不判、判活失败仅提示，旧版字节永久
# 丢失（update 侧对应格齐全，本用例对旧代码必红）。
test_install_flmx_agent_rolls_back_over_existing() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  SERVICE_MANAGER="systemd"
  INSTALL_DIR=$(mktemp -d)
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$INSTALL_DIR/flmx_agent.service"
  SERVER_ADDR="panel.example.com:443"
  SECRET="fixture-secret"
  DOWNLOAD_URL="https://example.com/gost"
  DOWNLOAD_ROUTES=("https://a.example" "https://b.example")
  ROUTES_STAGE=2

  get_config_params() { :; }
  check_and_install_tcpkill() { :; }
  cleanup_legacy_gost_installation() { :; }
  sleep() { :; }
  systemctl() { return 0; }
  flmx_agent_main_pid() { echo 4242; }

  local old_body new_body
  old_body=$(printf '#!/bin/bash\nif [[ "${1:-}" == "-V" ]]; then echo "gost 1.3.4 (go1.25 linux/amd64)"; exit 0; fi\necho old-agent\n')
  printf '%s\n' "$old_body" > "$INSTALL_DIR/flmx_agent"
  chmod +x "$INSTALL_DIR/flmx_agent"
  # 存量配置（addr 与本次 -a 一致，归属守卫放行——本用例测的是
  # 覆盖保护，不是守卫）。
  printf '{\n  "addr": "%s",\n  "secret": "%s"\n}\n' "$SERVER_ADDR" "$SECRET" > "$INSTALL_DIR/config.json"

  # 下载桩：本体与 .sha256 都按 NEW_BODY 供数（两条路线同值，
  # 第二锚可复核通过）。
  NEW_BODY=""
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
      printf '%s' "$NEW_BODY" | sha256sum | awk '{print $1"  gost-amd64.sha256"}' > "$output"
      return 0
    fi
    printf '%s' "$NEW_BODY" > "$output"
    chmod +x "$output"
  }

  local rc log
  log="$INSTALL_DIR/install.log"

  # 臂 1：新二进制 -V 即崩（烟雾失败）→ 自动回滚，旧二进制逐字恢复。
  ensure_download_url_initialized() { RESOLVED_VERSION="1.3.5"; }
  flmx_agent_service_is_active() { return 0; }
  NEW_BODY=$(printf '#!/bin/bash\nexit 1\n')
  rc="0"; ( install_flmx_agent >"$log" 2>&1 </dev/null ) || rc="$?"
  assert_equals "1" "$rc" "A3-04: install over existing must fail when the new binary cannot run"
  assert_equals "$old_body" "$(cat "$INSTALL_DIR/flmx_agent")" "A3-04: smoke failure must roll back to the previous binary"
  grep -q "回滚" "$log" || fail "A3-04: smoke-failure rollback must be named in the output"

  # 臂 2：新二进制能跑但服务判活失败 → 自动回滚。
  NEW_BODY=$(printf '#!/bin/bash\necho "gost 1.3.5 (go1.25 linux/amd64)"\n')
  flmx_agent_service_is_active() { return 1; }
  rc="0"; ( install_flmx_agent >"$log" 2>&1 </dev/null ) || rc="$?"
  assert_equals "1" "$rc" "A3-04: install over existing must fail when the new service stays down"
  assert_equals "$old_body" "$(cat "$INSTALL_DIR/flmx_agent")" "A3-04: health failure must roll back to the previous binary"

  # 臂 3：显式降级（1.3.0 < 现装 1.3.4）、非交互 → 拒绝且现装不动。
  ensure_download_url_initialized() { RESOLVED_VERSION="1.3.0"; }
  flmx_agent_service_is_active() { return 0; }
  rc="0"; ( install_flmx_agent >"$log" 2>&1 </dev/null ) || rc="$?"
  assert_equals "1" "$rc" "A3-04: non-interactive downgrade over existing must be refused"
  assert_equals "$old_body" "$(cat "$INSTALL_DIR/flmx_agent")" "A3-04: refused downgrade must leave the installed binary untouched"
  grep -q "拒绝降级" "$log" || fail "A3-04: downgrade refusal must be named in the output"

  # 臂 4：正常升级（能跑且判活通过）→ 新二进制在位、.bak 已清。
  ensure_download_url_initialized() { RESOLVED_VERSION="1.3.5"; }
  NEW_BODY=$(printf '#!/bin/bash\necho "gost 1.3.5 (go1.25 linux/amd64)"\n')
  rc="0"; ( install_flmx_agent >"$log" 2>&1 </dev/null ) || rc="$?"
  assert_equals "0" "$rc" "A3-04: healthy install over existing should succeed"
  assert_equals "$NEW_BODY" "$(cat "$INSTALL_DIR/flmx_agent")" "A3-04: healthy install must leave the new binary in place"
  [[ ! -f "$INSTALL_DIR/flmx_agent.bak" ]] || fail "A3-04: backup must be removed after a healthy install over existing"
)

# T-INC-01: 套件宿主机安全门的判定格——无真实 agent 布局放行；
# 任一布局（旧目录/新服务文件）在位拒绝；隔离标记（专用容器/VM
# 的显式声明）放行。门本体定义在套件头部、开跑前已执行一次。
test_suite_host_safety_gate_blocks_real_agent_hosts() (
  set -euo pipefail
  local root
  root=$(mktemp -d)

  FLMX_SUITE_HOST_ROOT="$root" suite_host_safety_gate

  # OBS-1: 拒绝臂必须显式清掉 FLMX_SUITE_ISOLATED——调用方环境里
  # 若恰好导出该变量，门会直接放行、拒绝臂假红。本用例验的是
  # 「未声明隔离时门按真实布局拒绝」，须与 ambient 环境无关。
  mkdir -p "$root/etc/flux_agent"
  if FLMX_SUITE_ISOLATED="" FLMX_SUITE_HOST_ROOT="$root" suite_host_safety_gate 2>/dev/null; then
    fail "T-INC-01: host gate must refuse when a legacy agent layout is present"
  fi

  rm -rf "$root/etc/flux_agent"
  mkdir -p "$root/etc/systemd/system"
  : > "$root/etc/systemd/system/flmx_agent.service"
  if FLMX_SUITE_ISOLATED="" FLMX_SUITE_HOST_ROOT="$root" suite_host_safety_gate 2>/dev/null; then
    fail "T-INC-01: host gate must refuse when a current agent unit is present"
  fi

  FLMX_SUITE_ISOLATED="1" FLMX_SUITE_HOST_ROOT="$root" suite_host_safety_gate
)

# T-INC-01: 桩层泄漏哨兵。INC-01 的泄漏形态是「某个旧布局路径没
# 被夹具接管、仍指向真实宿主位置」——本用例在夹具内复刻该形态：
# 故意把存量 systemd unit 路径指向夹具根之外的 canary（模拟真实
# 宿主文件），systemctl 与 rm 都经记录桩，rm 桩对夹具根之外的目
# 标只记录不执行（LEAK-BLOCKED）。断言：旧布局停/禁/删序列全部
# 被桩层截获、canary 存活。变异臂：关掉 rm 桩拦截后同一序列必
# 须真的删掉 canary——证明破坏调用真实存在、哨兵不是空转（桩
# 覆盖被摘即变红）。
test_suite_stub_layer_catches_legacy_leak() (
  set -euo pipefail
  local work fixture canary
  work=$(mktemp -d)
  fixture="$work/fixture"
  canary="$work/host"
  mkdir -p "$fixture" "$canary"

  build_harness() {
    local guard_mode="$1"
    local harness="$work/harness-$guard_mode.sh"
    sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$harness"
    cat >> "$harness" <<EOF
INSTALL_DIR="$fixture/flmx_agent"
LEGACY_INSTALL_DIR="$fixture/flux_agent"
FLMX_AGENT_SYSTEMD_SERVICE_FILE="$fixture/flmx_agent.service"
LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="$canary/flux_agent.service"
SERVICE_MANAGER="systemd"
CALLS_LOG="$work/calls-$guard_mode.log"
: > "\$CALLS_LOG"
systemctl() { echo "systemctl \$*" >> "\$CALLS_LOG"; return 0; }
rm() {
  echo "rm \$*" >> "\$CALLS_LOG"
  if [[ "$guard_mode" == "on" ]]; then
    local a
    for a in "\$@"; do
      case "\$a" in
        -*) ;;
        "$fixture"*) ;;
        *) echo "LEAK-BLOCKED \$a" >> "\$CALLS_LOG"; return 0 ;;
      esac
    done
  fi
  command rm "\$@"
}
stop_legacy_flux_agent_service
disable_legacy_flux_agent_service
remove_legacy_flux_agent_service
EOF
    printf '%s' "$harness"
  }

  printf 'real unit\n' > "$canary/flux_agent.service"
  local harness_on
  harness_on=$(build_harness on)
  bash "$harness_on"
  [[ -f "$canary/flux_agent.service" ]] || fail "T-INC-01: stub layer must prevent deletion of the out-of-fixture legacy unit"
  grep -q "systemctl stop flux_agent" "$work/calls-on.log" || fail "T-INC-01: legacy stop must be intercepted by the systemctl stub"
  grep -q "systemctl disable flux_agent" "$work/calls-on.log" || fail "T-INC-01: legacy disable must be intercepted by the systemctl stub"
  grep -q "LEAK-BLOCKED $canary/flux_agent.service" "$work/calls-on.log" || fail "T-INC-01: out-of-fixture rm target must be recorded as a blocked leak"

  printf 'real unit\n' > "$canary/flux_agent.service"
  local harness_off
  harness_off=$(build_harness off)
  bash "$harness_off"
  [[ ! -f "$canary/flux_agent.service" ]] || fail "T-INC-01 mutation arm: with the rm guard off, the legacy removal must actually delete the canary (the destructive call is real)"
)

# A3-05: delete_self 的删除目标必须是脚本本体。管道执行
# （curl … | bash）时 $0 是裸命令名 bash，旧实现以 readlink -f
# 按当前目录解析成 $CWD/bash 并真的删掉同名无关文件、还误报
# 「已删除」（沙箱已实证）。菜单选「4. 退出」（未做任何操作）
# 旧实现也自删并印完成文案，与 R4B11-005 口径不符。本用例三
# 臂：管道形态哨兵存活且无已删文案；任意文件名的文件形态仍可
# 自删（防过修）；菜单退出不自删。
test_delete_self_pipe_form_preserves_cwd_namesake() (
  set -euo pipefail
  local work cwd
  work=$(mktemp -d)
  cwd="$work/cwd"
  mkdir -p "$cwd"
  printf 'sentinel\n' > "$cwd/bash"

  # 管道臂：stdin 喂脚本全文，$0=bash，CWD 放同名哨兵文件。
  local pipe_script="$work/pipe.sh" out
  sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$pipe_script"
  echo 'delete_self 0' >> "$pipe_script"
  out=$(cd "$cwd" && cat "$pipe_script" | bash 2>&1) || fail "A3-05: pipe-form delete_self should exit cleanly"
  [[ -f "$cwd/bash" ]] || fail "A3-05: pipe-form execution must not delete the unrelated CWD file named bash"
  if [[ "$out" == *"脚本文件已删除"* ]]; then
    fail "A3-05: pipe-form execution must not claim the script file was deleted"
  fi

  # 文件形态臂（防过修）：改名副本以路径调用仍须自删。
  local renamed="$work/renamed-copy.sh"
  sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$renamed"
  echo 'delete_self 0' >> "$renamed"
  ( cd "$work" && bash "$renamed" >/dev/null 2>&1 ) || fail "A3-05: file-form delete_self should exit cleanly"
  [[ ! -f "$renamed" ]] || fail "A3-05: file-form execution should still delete the script itself"

  # 菜单退出臂：选 4 不自删、无完成文案、退出 0。
  local menu_file="$work/menu.sh" menu_out rc="0"
  sed '/^# 执行主函数$/,$d' "$ROOT_DIR/install.sh" > "$menu_file"
  cat >> "$menu_file" <<'EOF'
require_root_privileges() { return 0; }
main
EOF
  menu_out=$(printf '4\n' | bash "$menu_file" 2>&1) || rc="$?"
  assert_equals "0" "$rc" "A3-05: menu exit should exit 0"
  [[ -f "$menu_file" ]] || fail "A3-05: choosing exit in the menu must not delete the script"
  if [[ "$menu_out" == *"操作已完成"* ]]; then
    fail "A3-05: menu exit must not print the completion line"
  fi
)

# A3-06: install/update/uninstall 全程必须持有进程级互斥锁——旧
# 实现无锁，两个实例共用 .new/.part/.bak/config.json 固定工件可
# 互相删/续对方的半成品、config 后写覆盖先写。本用例以外部持锁
# 模拟先行实例：锁被占时 acquire 必须点名拒绝，释放后必须可占。
test_install_lock_rejects_concurrent_run() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local work lock_file errlog
  work=$(mktemp -d)
  lock_file="$work/install.lock"
  errlog="$work/acquire.err"
  export FLMX_INSTALL_LOCK_FILE="$lock_file"

  # 先行实例持锁（本 shell 的 fd 9）→ 后到者被拒且点名。
  exec 9>>"$lock_file"
  flock -n 9 || fail "A3-06: fixture could not take the lock"
  if ( acquire_install_lock ) 2>"$errlog"; then
    fail "A3-06: a second instance must be refused while the lock is held"
  fi
  grep -q "已有另一个 install.sh 实例正在运行" "$errlog" || \
    fail "A3-06: the concurrent-run refusal must be named on stderr"

  # 释放后可占（锁随进程退出自动释放，无陈旧锁残留）。
  flock -u 9
  exec 9>&-
  ( acquire_install_lock ) 2>/dev/null || \
    fail "A3-06: the lock must be acquirable after the first instance exits"
  unset FLMX_INSTALL_LOCK_FILE
)

# A3-07: 节点密钥的非 argv 入口。-s 让密钥在整个安装期间驻留进
# 程 argv（ps//proc/<pid>/cmdline 对本机所有本地用户可读），与
# 交互路径 read -s 的防窥口径分家。脚本须提供 FLMX_NODE_SECRET
# 环境入口：env 在场且未给 -s 时用 env 值、读入后立即从环境移
# 除（不随子进程扩散）；-s 在场时仍优先（兼容回退不变）。
test_install_script_accepts_secret_from_env() (
  set -euo pipefail

  # env 入口臂：在干净子 shell 里 source 截断脚本（getopts 段与
  # env 接入段都在截断保留范围内），断言 SECRET 与环境清理。
  # 期望值用变量拼（避免把密钥形态的字面值写进断言）。
  local out want_env="env-secret-value" want_argv="argv-secret-value"
  out=$(
    FLMX_NODE_SECRET="$want_env" bash -c '
      sed "/^# 执行主函数$/,\$d" "$1" > "$2"
      source "$2"
      printf "got=%s env=%s" "${SECRET:-}" "${FLMX_NODE_SECRET:-unset}"
    ' _ "$ROOT_DIR/install.sh" "$(mktemp)"
  )
  assert_equals "got=${want_env} env=unset" "$out" \
    "A3-07: FLMX_NODE_SECRET must feed SECRET and be removed from the environment"

  # -s 优先臂：argv 与 env 同时在场时 argv 胜（兼容回退口径）。
  out=$(
    FLMX_NODE_SECRET="$want_env" ARG_SECRET="$want_argv" bash -c '
      tmpf="$2"
      sed "/^# 执行主函数$/,\$d" "$1" > "$tmpf"
      set -- -a "wss://panel.example:443" -s "$ARG_SECRET"
      source "$tmpf"
      printf "got=%s" "${SECRET:-}"
    ' _ "$ROOT_DIR/install.sh" "$(mktemp)"
  )
  assert_equals "got=${want_argv}" "$out" \
    "A3-07: -s must keep precedence over FLMX_NODE_SECRET"
)

# A3-08: OpenRC pidfile 必须单一来源——服务文件里写的 pidfile 与
# 脚本判活读的 pidfile 是同一个变量值。旧实现三处分家：模板写死
# /run/${RC_SVCNAME}.pid、脚本读 FLMX_AGENT_OPENRC_PIDFILE 覆盖
# 值、存量侧又读从未赋值的第三个名字——覆盖一旦设置，守护进程
# 仍写默认路径、脚本去自定义路径读 PID，判活恒失败。
test_openrc_pidfile_uses_configured_value() (
  set -euo pipefail
  local work
  work=$(mktemp -d)
  SERVICE_MANAGER="openrc"

  # 缺省臂：模板落缺省值、判活从同一路径读。
  load_script_without_main "$ROOT_DIR/install.sh"
  FLMX_AGENT_OPENRC_SERVICE_FILE="$work/flmx.initd"
  write_flmx_agent_service
  grep -Fxq 'pidfile="/run/flmx_agent.pid"' "$work/flmx.initd" || \
    fail "A3-08: default OpenRC pidfile must be written from the single source variable"

  # 覆盖臂：FLMX_AGENT_OPENRC_PIDFILE 改指后，模板与读面同值。
  # （用普通赋值而非函数前缀赋值：前缀赋值在函数返回后会被
  # bash 恢复，测不到真实的 source 时取值。）
  FLMX_AGENT_OPENRC_PIDFILE="$work/custom.pid"
  load_script_without_main "$ROOT_DIR/install.sh"
  FLMX_AGENT_OPENRC_SERVICE_FILE="$work/flmx-custom.initd"
  write_flmx_agent_service
  grep -Fxq "pidfile=\"$work/custom.pid\"" "$work/flmx-custom.initd" || \
    fail "A3-08: configured OpenRC pidfile must be written into the service file"
  printf '777\n' > "$work/custom.pid"
  assert_equals "777" "$(flmx_agent_main_pid)" \
    "A3-08: health check must read the pid from the configured pidfile"

  # 存量臂：旧名前缀回退链（FLUX_AGENT_OPENRC_PIDFILE）同样进模板
  # 与读面，不再是第三个从未赋值的名字。
  FLUX_AGENT_OPENRC_PIDFILE="$work/legacy.pid"
  # 同壳多次 source 时派生变量已被前一轮赋过值，须先清空才测得
  # 到回退链（生产每进程只 source 一次、直接读环境，无此问题）。
  LEGACY_FLUX_AGENT_OPENRC_PIDFILE=""
  load_script_without_main "$ROOT_DIR/install.sh"
  LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE="$work/flux.initd"
  write_legacy_flux_agent_service
  grep -Fxq "pidfile=\"$work/legacy.pid\"" "$work/flux.initd" || \
    fail "A3-08: legacy OpenRC pidfile must be written from the same fallback chain"
  printf '888\n' > "$work/legacy.pid"
  assert_equals "888" "$(legacy_flux_agent_main_pid)" \
    "A3-08: legacy health check must read the pid from the legacy pidfile variable"
)

# T1b：agent systemd unit 必须自带 LimitNOFILE=1048576——调优面③
# 写的 /etc/security/limits.d 只作用于 PAM 登录会话，对已注册的
# 服务无效；没有 unit 这一行，存量与新装节点的 agent 进程句柄
# 上限都停在系统默认。存量侧的 unit 只在安装/重装（与改名迁移）
# 重写 unit 时才刷新，面板/脚本的二进制更新路径不重写 unit——
# 该生效条件由报告点名，本用例只钉模板本身。
test_agent_service_unit_carries_limit_nofile() (
  set -euo pipefail
  local work
  work=$(mktemp -d)
  SERVICE_MANAGER="systemd"

  load_script_without_main "$ROOT_DIR/install.sh"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$work/flmx_agent.service"
  write_flmx_agent_service
  grep -Fxq 'LimitNOFILE=1048576' "$work/flmx_agent.service" || \
    fail "T1b: agent systemd unit must carry LimitNOFILE=1048576"
  grep -Fxq 'LimitCORE=0' "$work/flmx_agent.service" || \
    fail "T1b: agent systemd unit must keep LimitCORE=0"
  # P1-06：unit 还须带内存天花板两档，数值按本机 MemTotal 从共享
  # 档位表算出（install.sh agent_memory_limits，与 go-gost/
  # memlimit.go 的 memoryTierFor 同表同数，两侧测试互钉）：
  # High＝进程内软帽×5/4、Max＝软帽×3/2、Max 不超过总内存。
  # 464448 kB（≈453MiB，ECS1686 同档）→ 软帽 317MiB。
  printf 'MemTotal:       464448 kB\nMemFree:         1024 kB\n' > "$work/meminfo"
  AGENT_MEMINFO_PATH="$work/meminfo"
  write_flmx_agent_service
  grep -Fxq 'MemoryHigh=396M' "$work/flmx_agent.service" || \
    fail "P1-06: agent systemd unit must carry MemoryHigh=396M on a 453MiB machine"
  grep -Fxq 'MemoryMax=453M' "$work/flmx_agent.service" || \
    fail "P1-06: agent systemd unit must carry MemoryMax=453M on a 453MiB machine"
  # 8GiB 档 → 软帽封顶 2048MiB、High 2560M、Max 3072M。
  printf 'MemTotal:       8388608 kB\n' > "$work/meminfo"
  write_flmx_agent_service
  grep -Fxq 'MemoryHigh=2560M' "$work/flmx_agent.service" || \
    fail "P1-06: agent systemd unit must carry MemoryHigh=2560M on an 8GiB machine"
  grep -Fxq 'MemoryMax=3072M' "$work/flmx_agent.service" || \
    fail "P1-06: agent systemd unit must carry MemoryMax=3072M on an 8GiB machine"
  # MemTotal 读不到时按 512MiB 档兜底（软帽 358MiB），不许无帽。
  printf 'MemFree:         1024 kB\n' > "$work/meminfo"
  write_flmx_agent_service
  grep -Fxq 'MemoryHigh=447M' "$work/flmx_agent.service" || \
    fail "P1-06: agent systemd unit must fall back to the 512MiB tier when MemTotal is unreadable"
  grep -Fxq 'MemoryMax=512M' "$work/flmx_agent.service" || \
    fail "P1-06: agent systemd unit must fall back to the 512MiB tier when MemTotal is unreadable"
)
test_proxy_enabled_case_insensitive
test_install_script_slow_direct_joins_ranking
test_panel_slow_direct_joins_ranking
test_install_script_download_budget_enforced
test_install_script_download_resumes_across_routes
test_install_script_resolve_budget_enforced
test_install_script_uppercase_sha256_accepted
test_panel_migrate_postgres_rollbacks
test_panel_migrate_waits_use_deployment_container_names
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
test_env_var_reading_matches_compose_semantics
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
test_panel_install_routes_direct_needs_throughput_gate
test_install_script_panel_route_extends_on_download_failure
test_panel_download_verified_routed_failover
test_panel_anchor_corroboration_passes
test_panel_anchor_corroboration_mismatch_aborts
test_panel_anchor_corroboration_degrades
test_install_script_binary_anchor_passes
test_install_script_binary_anchor_mismatch_rejects
test_install_script_binary_anchor_degrades
test_install_script_rejects_malformed_explicit_versions
test_install_script_latest_resolution_requires_corroboration
test_install_flmx_agent_rolls_back_over_existing
test_suite_host_safety_gate_blocks_real_agent_hosts
test_suite_stub_layer_catches_legacy_leak
test_delete_self_pipe_form_preserves_cwd_namesake
test_install_lock_rejects_concurrent_run
test_install_script_accepts_secret_from_env
test_openrc_pidfile_uses_configured_value
test_agent_service_unit_carries_limit_nofile
test_update_flmx_agent_downloads_without_prompt
test_update_flmx_agent_skips_proxy_prompt_when_not_installed
test_update_flmx_agent_prints_online_confirmation_guidance
test_flmx_agent_health_ok_requires_stable_pid
test_update_flmx_agent_rolls_back_flapping_binary
test_update_flmx_agent_aborts_when_backup_fails
test_update_flmx_agent_aborts_when_backup_fails_with_stale_bak
test_extract_leading_semver_matches_go_rules
test_update_flmx_agent_rejects_downgrade
test_update_flmx_agent_allows_higher_and_incomparable
test_main_propagates_operation_exit_codes
test_install_flmx_agent_preserves_legacy_gost_when_download_fails
test_update_flmx_agent_preserves_legacy_gost_when_download_fails
test_agent_config_addr_normalizes_json_escapes
test_guard_agent_dir_ownership_matrix
test_update_flmx_agent_refuses_unowned_agent
test_update_flmx_agent_menu_guard_pass_forms
test_update_flmx_agent_menu_guard_interactive
test_uninstall_flmx_agent_refuses_unowned_agent
test_uninstall_flmx_agent_menu_guard_pass_forms
test_uninstall_flmx_agent_cleans_host_tuning_residue
test_uninstall_flmx_agent_cleans_restart_pair_and_logs
test_install_small_host_swap_creates_when_tiny_and_swapless
test_install_small_host_swap_keeps_existing_swap
test_install_small_host_swap_skips_large_memory
test_install_small_host_swap_idempotent_second_run
test_install_small_host_swap_skip_switch
test_install_small_host_swap_boundary_exactly_512mib
test_install_small_host_swap_rolls_back_on_build_failure
test_panel_small_host_swap_creates_when_tiny_and_swapless
test_panel_small_host_swap_keeps_existing_swap
test_panel_small_host_swap_skips_large_memory
test_panel_small_host_swap_idempotent_second_run
test_panel_small_host_swap_skip_switch
test_panel_small_host_swap_boundary_exactly_512mib
test_panel_small_host_swap_rolls_back_on_build_failure
test_uninstall_flmx_agent_host_cleanup_partial_failure_and_idempotent
test_uninstall_flmx_agent_host_cleanup_queue_backup_lost
test_uninstall_flmx_agent_host_cleanup_restores_kernel_snapshot
test_uninstall_flmx_agent_host_cleanup_kernel_snapshot_missing
test_uninstall_flmx_agent_host_cleanup_kernel_snapshot_corrupt
test_install_flmx_agent_writes_json_safe_config
test_install_flmx_agent_config_preserves_protocol_counts
test_install_script_bootstraps_bash_for_alpine
test_install_flmx_agent_uses_openrc
test_remove_flmx_agent_service_uses_openrc
test_cleanup_legacy_gost_installation_removes_service_and_binary
test_cleanup_legacy_gost_installation_preserves_unrelated_gost
test_cleanup_legacy_gost_installation_skips_systemd_on_openrc
test_cleanup_legacy_gost_installation_skips_foreign_agent
test_cleanup_legacy_gost_installation_skips_unreadable_addr
test_cleanup_legacy_gost_installation_takeover_cleans_foreign_agent
test_cleanup_legacy_gost_installation_matches_installed_agent_addr
test_cleanup_legacy_gost_installation_interactive_confirm
test_uninstall_flmx_agent_host_cleanup_preserves_foreign_legacy_resources
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
test_install_flmx_agent_returns_nonzero_when_service_not_active
test_systemd_starts_reset_failed_before_start
test_rollback_restarts_service_from_failed_state
test_panel_menu5_preserves_front_block_after_menu6
test_panel_menu5_rolls_back_on_validate_failure
test_panel_menu5_restart_failure_rolls_back_with_guidance
test_panel_menu6_restart_failure_rolls_back_with_guidance
test_panel_menu5_caddy_list_truncated_not_landed
test_panel_menu5_caddy_list_removed_when_install_fails
test_install_flmx_agent_rejects_flapping_service
test_main_delete_self_only_on_success
test_panel_main_delete_self_only_on_success
test_install_uninstall_cancel_keeps_script
test_uninstall_removes_custom_kernel_dir
test_install_panel_invalid_port_aborts_before_download
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
test_panel_migrate_preprocess_drops_fold_index_and_v5_record
test_tcpkill_missing_warns_instead_of_silent

test_panel_rename_new_install_defaults_are_flmx
test_panel_legacy_identity_recognition_intact
test_panel_update_renames_legacy_identity
test_panel_update_rename_pins_custom_resource_names
test_panel_update_rename_health_failure_rolls_back
test_panel_update_volume_check_rolls_back_on_mismatch
test_panel_update_volume_check_existing_key_path
test_panel_update_pins_keys_without_rename
test_panel_migrate_placeholder_password_sentinels
test_panel_migrate_volume_split_and_rollback_cleanup
test_panel_migrate_reconciles_row_counts
test_panel_migrate_source_volume_live_read_wins
test_update_flmx_agent_migrates_legacy_layout
test_migrate_legacy_agent_rolls_back_on_failure
test_panel_update_volume_key_conflict_live_read_wins
test_panel_update_aborts_on_accident_state_volume
test_panel_update_aborts_on_pg_accident_state_volume
test_panel_pin_pg_bind_declared_key_aborts
test_panel_pin_network_multi_network_not_concatenated
test_panel_identity_rename_switch_reads_env_file
test_panel_migrate_pins_keys_before_any_up
test_panel_migrate_reconcile_second_channel
test_migrate_legacy_agent_snapshot_failure_aborts


# FIX2/D-05（节点审计 D 路）：install.sh 的 probe_route 与版本解析
# 器共用渠道 tag 文法——与 panel_install.sh 的 A1-04 同型。旧实现
# 探测侧写死宽文法，稳定渠道下会把重定向落到预发布 tag 的路线
# 判活、解析器随后按稳定文法拒绝，自相矛盾。断言：稳定渠道探
# 测拒绝预发布 tag、beta 渠道两处同放行、垃圾 tag 两渠道同拒。
test_install_probe_route_uses_channel_tag_grammar() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  curl() {
    printf '0.123 https://github.com/some-owner/some-repo/releases/tag/%s' "$FAKE_TAG"
    return 0
  }

  local rc
  unset FLMX_RELEASE_CHANNEL
  FAKE_TAG='1.9.10'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  assert_equals "0" "$rc" "FIX2/D-05: a stable tag must pass the probe on the stable channel"
  FAKE_TAG='1.9.10-beta.1'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  [[ "$rc" != "0" ]] || fail "FIX2/D-05: on the stable channel the probe must reject a prerelease tag, matching the parser grammar"

  FLMX_RELEASE_CHANNEL='beta'
  FAKE_TAG='1.9.10-beta.1'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  assert_equals "0" "$rc" "FIX2/D-05: a prerelease tag must pass the probe on the beta channel"
  FAKE_TAG='1.9.10'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  assert_equals "0" "$rc" "FIX2/D-05: a stable tag must pass the probe on the beta channel"

  FAKE_TAG='releases'
  rc="0"; probe_route "" >/dev/null || rc="$?"
  [[ "$rc" != "0" ]] || fail "FIX2/D-05: a junk tag must fail the probe on the beta channel"
  unset FLMX_RELEASE_CHANNEL
  rc="0"; probe_route "" >/dev/null || rc="$?"
  [[ "$rc" != "0" ]] || fail "FIX2/D-05: a junk tag must fail the probe on the stable channel"
)

# FIX2（节点审计 D-01/D-02/D-03）：面板源独占双门 + 下载看门狗
# 对齐。全桩 curl 按 URL 子串分流：面板源宿主 127.0.0.1:18099
# （SERVER_ADDR 派生），代理候选被桩成可控延迟，直连可控成败；
# RESOLVED_VERSION 预置免版本解析联网，⑥ 的 DOWNLOAD_URL 按真实契约在路线表就绪后经 ensure_download_url_initialized 决议。
#   ① 面板源慢速（测速样本 286720B/10s ≈ 28KB/s < 256KB/s 门
#      槛、重定向延迟快）：不得独占（ROUTES_STAGE != 3），面板
#      源须按实测延迟落入排名表，且测速只拉 256KB 样本（整包不
#      经测速腿下载）；
#   ② 面板源快速（样本 256KB/0.5s）：独占（阶段 3、表仅一条）；
#   ③ 面板源延迟 1.2s > 800ms 门（测速快）：不得独占、落入排名；
#   ④ PROXY_URL 逃生口：路线先于面板源——表单条 = 覆盖前缀，
#      且桩日志不得出现面板源主机；
#   ⑤ PROXY_ENABLED=false：直连单条、桩日志零请求；
#   ⑥ 看门狗：经真实 download_agent_binary 下载须带
#      --speed-limit 40960 --speed-time 15 与 -C -，旧值 1024
#      不得再现。
test_install_panel_route_gates_and_watchdog() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local log metrics_log
  log=$(mktemp)
  metrics_log=$(mktemp)
  CURL_LOG="$log"
  CURL_METRICS_LOG="$metrics_log"
  PANEL_MODE="slow"
  PANEL_LAT="0.100"
  DIRECT_MODE="dead"
  BODY_SHA=""

  curl() {
    echo "$*" >> "$CURL_LOG"
    local all=" $* "
    if [[ "$all" == *'%{http_code} %{size_download} %{time_total}'* ]]; then
      if [[ "$PANEL_MODE" == "slow" ]]; then
        printf '206 286720 10.000000'
      else
        printf '206 262144 0.500000'
      fi
      return 0
    fi
    if [[ "$all" == *'%{time_total}'* ]]; then
      case "$all" in
        *127.0.0.1:18099*)
          printf '%s https://github.com/%s/releases/tag/2.0.0' "$PANEL_LAT" "$REPO" ;;
        *ghfast.top*)
          printf '0.900 https://github.com/%s/releases/tag/2.0.0' "$REPO" ;;
        *gcode.hostcentral.cc*)
          printf '0.050 https://github.com/%s/releases/tag/2.0.0' "$REPO" ;;
        *github.com*)
          if [[ "$DIRECT_MODE" == "dead" ]]; then return 22; fi
          printf '0.300 https://github.com/%s/releases/tag/2.0.0' "$REPO" ;;
        *) return 22 ;;
      esac
      return 0
    fi
    if [[ "$all" == *'%{time_total}'* ]]; then
      return 22
    fi
    case "$all" in
      *api.github.com*) return 22 ;;
      *.sha256\ *)
        local out="" prev=""
        for a in "$@"; do
          [[ "$prev" == "-o" ]] && out="$a"
          prev="$a"
        done
        printf '%s  gost-test' "$BODY_SHA" > "$out"
        BODY_SHA=""
        return 0 ;;
      *releases/download*.sha256\ *)
        local out="" prev=""
        for a in "$@"; do
          [[ "$prev" == "-o" ]] && out="$a"
          prev="$a"
        done
        printf '%s  gost-test' "$BODY_SHA" > "$out"
        return 0 ;;
      *releases/download*)
        local out="" prev=""
        for a in "$@"; do
          [[ "$prev" == "-o" ]] && out="$a"
          [[ "$prev" == "--max-time" ]] && echo "max-time $a" >> "$CURL_METRICS_LOG"
          prev="$a"
        done
        printf 'fixture-gost-body' > "$out"
        return 0 ;;
      *) return 22 ;;
    esac
  }

  SERVER_ADDR="wss://127.0.0.1:18099"
  RESOLVED_VERSION="2.0.0"
  unset FLMX_RELEASE_CHANNEL
  PROXY_ENABLED=""
  PROXY_URL=""

  # ① 慢速面板源：不独占、落排名、排名首位为最快代理。
  PANEL_MODE="slow"; PANEL_LAT="0.100"; DIRECT_MODE="dead"
  DOWNLOAD_ROUTES=(); ROUTES_STAGE=0; DOWNLOAD_URL=""
  : > "$CURL_LOG"
  ensure_download_routes 2>/dev/null
  [[ "$ROUTES_STAGE" != "3" ]] || fail "FIX2/D-01: a slow panel source must not monopolize the route table"
  assert_equals "2" "$ROUTES_STAGE" "FIX2/D-01: the demoted panel source must join the stage-2 ranking"
  local found_panel="0" r
  for r in "${DOWNLOAD_ROUTES[@]}"; do
    [[ "$r" == "https://127.0.0.1:18099/api/v1/open_api/gh" ]] && found_panel="1"
  done
  assert_equals "1" "$found_panel" "FIX2/D-01: the panel source must remain in the ranked route table"
  assert_equals "https://gcode.hostcentral.cc" "${DOWNLOAD_ROUTES[0]}" "FIX2/D-01: the fastest ranked proxy must lead the table"

  # ② 快速面板源：正常独占。
  PANEL_MODE="fast"
  DOWNLOAD_ROUTES=(); ROUTES_STAGE=0; DOWNLOAD_URL=""
  ensure_download_routes 2>/dev/null
  assert_equals "3" "$ROUTES_STAGE" "FIX2/D-01: a fast panel source must monopolize (stage 3)"
  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "FIX2/D-01: stage 3 must hold exactly the panel route"
  assert_equals "https://127.0.0.1:18099/api/v1/open_api/gh" "${DOWNLOAD_ROUTES[0]}" "FIX2/D-01: stage 3 route must be the panel source"

  # ③ 延迟超门：测速再快也不独占。
  PANEL_LAT="1.200"
  DOWNLOAD_ROUTES=(); ROUTES_STAGE=0; DOWNLOAD_URL=""
  ensure_download_routes 2>/dev/null
  [[ "$ROUTES_STAGE" != "3" ]] || fail "FIX2/D-03: a panel source slower than the 800ms latency gate must not monopolize"
  assert_equals "2" "$ROUTES_STAGE" "FIX2/D-03: the latency-gated panel source must join the ranking"

  # ④ PROXY_URL 逃生口优先于面板源。
  PANEL_LAT="0.100"
  PROXY_URL="https://override.example/"
  DOWNLOAD_ROUTES=(); ROUTES_STAGE=0; DOWNLOAD_URL=""
  : > "$CURL_LOG"
  ensure_download_routes 2>/dev/null
  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "FIX2: PROXY_URL override must yield exactly one route"
  assert_equals "https://override.example" "${DOWNLOAD_ROUTES[0]}" "FIX2: PROXY_URL override route must be the override prefix"
  if grep -q '127.0.0.1:18099' "$CURL_LOG"; then
    fail "FIX2: with PROXY_URL set, the panel source must never be probed"
  fi
  PROXY_URL=""

  # ⑤ PROXY_ENABLED=false：直连单条、零请求。
  PROXY_ENABLED="false"
  DOWNLOAD_ROUTES=(); ROUTES_STAGE=0; DOWNLOAD_URL=""
  : > "$CURL_LOG"
  ensure_download_routes 2>/dev/null
  assert_equals "1" "${#DOWNLOAD_ROUTES[@]}" "FIX2: PROXY_ENABLED=false must yield exactly the direct route"
  assert_equals "" "${DOWNLOAD_ROUTES[0]}" "FIX2: PROXY_ENABLED=false route must be direct"
  assert_equals "0" "$(grep -c . "$CURL_LOG" || true)" "FIX2: PROXY_ENABLED=false must not issue any probe"
  PROXY_ENABLED=""

  # ⑥ 看门狗新阈值 + 续传保留（真实 download_agent_binary）。
  # 按真实调用契约：路线表先行（② 的 ensure 已建阶段 3 表并
  # 复用），下载前调用 ensure_download_url_initialized 决议
  # DOWNLOAD_URL（install/update 两路同序）。覆写该函数为桩决
  # 议（套件内同法多见），决议值指向桩本体。
  PANEL_MODE="fast"; PANEL_LAT="0.100"
  ensure_download_url_initialized() { DOWNLOAD_URL="https://github.com/${REPO}/releases/download/2.0.0/gost-test"; }
  ensure_download_url_initialized
  BODY_SHA=$(printf 'fixture-gost-body' | sha256sum | awk '{print $1}')
  local dest_dir dest
  dest_dir=$(mktemp -d)
  dest="$dest_dir/gost-test"
  download_agent_binary "$dest" >/dev/null 2>&1 || fail "FIX2/D-02: download via the fast panel route must succeed"
  assert_equals "fixture-gost-body" "$(cat "$dest")" "FIX2/D-02: the downloaded body must land verbatim"
  grep -q -- '--speed-limit 40960' "$CURL_LOG" || fail "FIX2/D-02: the download watchdog must use --speed-limit 40960"
  grep -q -- '--speed-time 15' "$CURL_LOG" || fail "FIX2/D-02: the download watchdog must use --speed-time 15"
  if grep -q -- '--speed-limit 1024' "$CURL_LOG"; then
    fail "FIX2/D-02: the old 1KB/s watchdog floor must be gone"
  fi
  grep -q -- '-C -' "$CURL_LOG" || fail "FIX2/D-02: cross-route resume (-C -) must be preserved"
)

test_install_probe_route_uses_channel_tag_grammar
test_install_panel_route_gates_and_watchdog
test_install_panel_force_reinstall_data_gate
test_panel_pin_accident_gate_unknown_mount_aborts
test_panel_update_pg_volume_check_rolls_back_on_mismatch
test_panel_pin_no_live_read_no_volume_key
test_panel_download_routed_total_budget
test_panel_installer_script_curl_uses_timeouts
test_panel_caddy_source_fetches_use_timeouts
test_panel_caddyfile_without_site_keeps_shared_addresses
test_panel_uninstall_without_docker_cleans_full_residual_set
test_panel_uninstall_removes_inpanel_upgrade_artifacts
test_panel_uninstall_removes_own_caddy_blocks
test_panel_explicit_version_format_validated
test_panel_probe_route_uses_channel_tag_grammar
test_panel_normalize_proxy_prefix_strips_all_trailing_slashes
test_panel_docker_ipv6_uses_mktemp_not_predictable_tmp
test_panel_run_lock_and_unique_caddy_backup_names
test_panel_invalid_instance_id_hard_fails_on_manage_paths
test_panel_menu5_site_block_filters_secret_query_from_access_log
test_env_var_reading_crlf_shape
test_panel_install_env_carries_trusted_proxies_entry
test_shipped_compose_templates_carry_trusted_proxies_passthrough


# FIX2/D-05（节点审计 D 路）：install.sh 的 probe_route 与

# W1 新增用例（并入 test-install-scripts-proxy.sh 末尾）

# W1-UPSERT: upsert_env_var 逐字保真与键口径——awk -v 转义解释曾
# 把字面 \n 展开成真换行注入新行、\t 变真 TAB；键 grep 按正则且
# 不认行首空白键，get_env_var 却认，同一 .env 读得到写不进。现
# 经 ENVIRON 逐字传递＋与读侧同口径的键匹配＋同键去重＋真换行
# 拒绝，四格钉死。
test_upsert_env_var_literal_values_and_key_parity() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local f
  f=$(mktemp)
  printf 'A=1\nSPACED=old\nK=v1\nK=v2\n' > "$f"

  upsert_env_var "$f" NEWKEY 'x\ny=INJECTED' || fail "W1-UPSERT: literal backslash value must be accepted"
  grep -Fxq 'NEWKEY=x\ny=INJECTED' "$f" || fail "W1-UPSERT: backslash sequence must be stored verbatim, not expanded"
  [[ $(wc -l < "$f") -eq 5 ]] || fail "W1-UPSERT: value must not inject extra lines"

  upsert_env_var "$f" TABKEY 'a\tb' || fail "W1-UPSERT: tab sequence value must be accepted"
  grep -Fxq 'TABKEY=a\tb' "$f" || fail "W1-UPSERT: tab sequence must be stored verbatim"

  upsert_env_var "$f" SPACED new || fail "W1-UPSERT: whitespace-padded key update must succeed"
  [[ $(grep -c 'SPACED' "$f") -eq 1 ]] || fail "W1-UPSERT: whitespace-padded key must be replaced, not duplicated"
  grep -Fxq 'SPACED=new' "$f" || fail "W1-UPSERT: replaced value must land"

  upsert_env_var "$f" K v3 || fail "W1-UPSERT: duplicate key update must succeed"
  [[ $(grep -c '^K=' "$f") -eq 1 ]] || fail "W1-UPSERT: duplicate keys must collapse to one line"
  grep -Fxq 'K=v3' "$f" || fail "W1-UPSERT: the surviving line must carry the new value"

  if upsert_env_var "$f" BAD "$(printf 'a\nb')" 2>/dev/null; then
    fail "W1-UPSERT: a value with a real newline must be rejected"
  fi
  if grep -q '^BAD=' "$f"; then
    fail "W1-UPSERT: rejected value must not be written"
  fi
)

# W1-GATE: 防降级门版本口径与 Go 面对齐——带后缀目标旧实现经
# 前导数字提取判「可比较」放行（2.1.1-rc1 冒充 2.1.1），dev 旧
# 实现一边可比一边提不出而放行。现两脚本同走 gate 助手：不可
# 比即空串（调用方拒绝）、dev 定序与 Go 面一致（dev 最低、同
# dev 相等）、纯数字升降级判定不变。
test_panel_version_gate_matches_go_semantics() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  assert_equals "" "$(panel_version_gate_cmp 2.1.1-rc1 2.1.2)" "W1-GATE: suffixed target must be incomparable"
  assert_equals "" "$(panel_version_gate_cmp 2.1.2 2.1.1-rc1)" "W1-GATE: suffixed current must be incomparable"
  assert_equals "-1" "$(panel_version_gate_cmp 1.9.9 2.1.2)" "W1-GATE: plain downgrade target stays -1"
  assert_equals "1" "$(panel_version_gate_cmp 2.1.3 2.1.2)" "W1-GATE: plain upgrade stays 1"
  assert_equals "0" "$(panel_version_gate_cmp v2.1.2 2.1.2)" "W1-GATE: v-prefixed equal stays 0"
  assert_equals "-1" "$(panel_version_gate_cmp dev 2.1.2)" "W1-GATE: dev target is lowest"
  assert_equals "1" "$(panel_version_gate_cmp 2.1.2 dev)" "W1-GATE: dev current is lowest"
  assert_equals "0" "$(panel_version_gate_cmp dev dev)" "W1-GATE: dev vs dev is equal"
)

test_install_version_gate_matches_go_semantics() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  assert_equals "" "$(panel_version_gate_cmp 2.1.1-rc1 2.1.2)" "W1-GATE: install copy must reject suffixed target as incomparable"
  assert_equals "-1" "$(panel_version_gate_cmp 1.9.9 2.1.2)" "W1-GATE: install copy plain downgrade stays -1"
  assert_equals "-1" "$(panel_version_gate_cmp dev 2.1.2)" "W1-GATE: install copy dev target is lowest"
)

# W1-CADDY1L: caddyfile_without_site 对单行块——旧 flush 只剥行尾
# 的 { ，单行块整行被当块头吞掉：独占块残留成无法解析的碎片、
# 共享块连带删掉同行其他站点。现取首个 { 前的地址段，独占整
# 行丢弃、共享以剩余地址重建单行块，多行块行为不变。
test_caddyfile_without_site_single_line_blocks() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local cf out
  cf=$(mktemp)
  printf 'panel.example.com { reverse_proxy 127.0.0.1:6366 }\n' > "$cf"
  out=$(caddyfile_without_site "$cf" panel.example.com)
  [[ -z "${out//[$'\n ']/}" ]] || fail "W1-CADDY1L: solo single-line block must be fully removed, got: $out"

  printf 'panel.example.com, other.example.com { reverse_proxy 127.0.0.1:6366 }\n' > "$cf"
  out=$(caddyfile_without_site "$cf" panel.example.com)
  assert_equals 'other.example.com { reverse_proxy 127.0.0.1:6366 }' "$out" "W1-CADDY1L: shared single-line block must keep the other site"

  printf 'a.com {\n  reverse_proxy 127.0.0.1:1\n}\nb.com {\n  reverse_proxy 127.0.0.1:2\n}\n' > "$cf"
  out=$(caddyfile_without_site "$cf" a.com)
  assert_equals $'b.com {\n  reverse_proxy 127.0.0.1:2\n}' "$out" "W1-CADDY1L: multi-line removal must be unchanged"
)

# W1-NETNAME: panel_network_name 缺省名按在盘 compose 世代推导
# （旧模板网络实名 gost-network、无 FLMX_NETWORK 展开式），不
# 再无条件套现行缺省；NETLABEL 对账：标签合无动作、标签不合且
# 网络为空时摘除待 compose 以现行键名重建、挂有外来容器时拒绝
# 并点名。
test_panel_network_default_name_and_label_reconcile() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local work
  work=$(mktemp -d)
  docker() { return 1; }

  ( cd "$work" && printf 'networks:\n  gost-network:\n    name: ${FLMX_NETWORK:-flmx-network}\n# FLMX_SQLITE_VOLUME\n' > docker-compose.yml && panel_network_name ) > "$work/nn1"
  assert_equals "flmx-network" "$(cat "$work/nn1")" "W1-NETNAME: current template default must be flmx-network"
  ( cd "$work" && printf 'networks:\n  gost-network:\n    name: gost-network\n' > docker-compose.yml && panel_network_name ) > "$work/nn2"
  assert_equals "gost-network" "$(cat "$work/nn2")" "W1-NETNAME: legacy template default must be gost-network"

  DOCKER_CALLS="$work/calls"; : > "$DOCKER_CALLS"
  # 对账段钉死现行模板（键名 gost-network、实名缺省 flmx-network），
  # 与上方 nn2 的旧模板状态解耦。
  printf 'networks:\n  gost-network:\n    name: ${FLMX_NETWORK:-flmx-network}\n# FLMX_SQLITE_VOLUME\n' > "$work/docker-compose.yml"
  docker() {
    echo "docker $*" >> "$DOCKER_CALLS"
    if [[ "$1" == "network" && "$2" == "inspect" ]]; then
      # R2-W1-NETLABEL: absent-network case — plain inspect must fail
      # so reconcile takes the no-op path.
      if [[ -n "${FAKE_NET_ABSENT:-}" && "${3:-}" != "-f" ]]; then
        return 1
      fi
      local fmt=""
      [[ "${3:-}" == "-f" ]] && fmt="$4"
      case "$fmt" in
        *com.docker.compose.network*) printf '%s' "${FAKE_NET_LABEL:-}" ;;
        *Containers*) for c in ${FAKE_NET_CONTAINERS:-}; do printf '%s\n' "$c"; done ;;
        *) echo '{}' ;;
      esac
      return 0
    fi
    return 0
  }
  PANEL_BACKEND_CONTAINER="flmx-panel-backend"
  PANEL_FRONTEND_CONTAINER="flmx-panel-frontend"
  PANEL_POSTGRES_CONTAINER="flmx-panel-postgres"

  ( cd "$work" && FAKE_NET_LABEL="flmx-network" panel_reconcile_network_label ) || fail "W1-NETNAME: mismatched empty network must reconcile"
  grep -q 'network rm flmx-network' "$DOCKER_CALLS" || fail "W1-NETNAME: mismatched empty network must be removed for recreation"

  : > "$DOCKER_CALLS"
  ( cd "$work" && FAKE_NET_LABEL="gost-network" panel_reconcile_network_label ) || fail "W1-NETNAME: matching label must be a no-op success"
  if grep -q 'network rm' "$DOCKER_CALLS"; then
    fail "W1-NETNAME: matching label must not remove the network"
  fi

  : > "$DOCKER_CALLS"
  if ( cd "$work" && FAKE_NET_LABEL="flmx-network" FAKE_NET_CONTAINERS="other-app" panel_reconcile_network_label ) 2>/dev/null; then
    fail "W1-NETNAME: foreign containers on the network must refuse reconciliation"
  fi
  if grep -q 'network rm' "$DOCKER_CALLS"; then
    fail "W1-NETNAME: refused reconciliation must not remove the network"
  fi

  # R2-W1-NETLABEL: mismatched label + only own containers attached →
  # detach them first, then remove the network for recreation.
  : > "$DOCKER_CALLS"
  ( cd "$work" && FAKE_NET_LABEL="flmx-network" FAKE_NET_CONTAINERS="flmx-panel-backend flmx-panel-frontend" panel_reconcile_network_label ) || fail "R2-W1-NETLABEL: own-only attached containers must detach and reconcile"
  grep -q 'rm -f flmx-panel-backend flmx-panel-frontend' "$DOCKER_CALLS" || fail "R2-W1-NETLABEL: own containers must be detached before network removal"
  grep -q 'network rm flmx-network' "$DOCKER_CALLS" || fail "R2-W1-NETLABEL: mismatched own-only network must be removed for recreation"

  # R2-W1-NETLABEL: target network absent → rc 0, no-op.
  : > "$DOCKER_CALLS"
  ( cd "$work" && FAKE_NET_ABSENT=1 FAKE_NET_LABEL="flmx-network" panel_reconcile_network_label ) || fail "R2-W1-NETLABEL: absent network must be a no-op success"
  if grep -q 'network rm' "$DOCKER_CALLS"; then
    fail "R2-W1-NETLABEL: absent network must not attempt removal"
  fi
)

# R2-C4-01: configure_docker_ipv6 must not claim success from the key
# name alone — "ipv6": false must be rewritten, not receipted. The
# function hardcodes /etc/docker/daemon.json; tests run as root in the
# sandbox and restore whatever was there (skip if not writable).
test_configure_docker_ipv6_false_value_is_updated_not_claimed() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local cfg="/etc/docker/daemon.json" bak had_cfg=0
  bak=$(mktemp)
  restore_cfg() {
    if [[ "$had_cfg" == "1" ]]; then cp "$bak" "$cfg" 2>/dev/null || true; else rm -f "$cfg"; fi
    rm -f "$bak"
  }
  trap restore_cfg EXIT
  if [[ -f "$cfg" ]]; then had_cfg=1; cp "$cfg" "$bak" 2>/dev/null || { echo "skip: test_configure_docker_ipv6_false_value_is_updated_not_claimed needs a readable /etc/docker/daemon.json"; return 0; }; fi
  if ! mkdir -p /etc/docker 2>/dev/null; then
    echo "skip: test_configure_docker_ipv6_false_value_is_updated_not_claimed needs a writable /etc/docker"
    return 0
  fi
  printf '{\n  "ipv6": false,\n  "fixed-cidr-v6": "fd00::/80"\n}\n' > "$cfg" 2>/dev/null || {
    echo "skip: test_configure_docker_ipv6_false_value_is_updated_not_claimed needs a writable /etc/docker"
    return 0
  }

  local work restarted=0
  work=$(mktemp -d)
  restart_docker_checked() { restarted=1; return 0; }
  sleep() { :; }

  configure_docker_ipv6 >"$work/out" 2>&1 || fail "R2-C4-01: updating a false ipv6 value must succeed"
  if grep -q "✅ Docker 已配置 IPv6 支持" "$work/out"; then
    fail "R2-C4-01: an existing \"ipv6\": false must not print the configured receipt"
  fi
  grep -q '"ipv6": true' "$cfg" || fail "R2-C4-01: the false value must be rewritten to true"
  [[ "$(grep -c '"ipv6"' "$cfg")" == "1" ]] || fail "R2-C4-01: the config must carry exactly one ipv6 key (no duplicate from sed/jq)"
  assert_equals "1" "$restarted" "R2-C4-01: docker must be restarted after rewriting the config"
)

# R2-C4-01: the jq-less (sed) path must replace a false value in place —
# inserting a second "ipv6": true would fool the write-back grep check
# (JSON duplicate keys resolve to the last value, i.e. false).
test_configure_docker_ipv6_sed_path_replaces_false_value() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local cfg="/etc/docker/daemon.json" bak had_cfg=0
  bak=$(mktemp)
  restore_cfg() {
    if [[ "$had_cfg" == "1" ]]; then cp "$bak" "$cfg" 2>/dev/null || true; else rm -f "$cfg"; fi
    rm -f "$bak"
  }
  trap restore_cfg EXIT
  if [[ -f "$cfg" ]]; then had_cfg=1; cp "$cfg" "$bak" 2>/dev/null || { echo "skip: test_configure_docker_ipv6_sed_path_replaces_false_value needs a readable /etc/docker/daemon.json"; return 0; }; fi
  if ! mkdir -p /etc/docker 2>/dev/null; then
    echo "skip: test_configure_docker_ipv6_sed_path_replaces_false_value needs a writable /etc/docker"
    return 0
  fi
  printf '{\n  "ipv6": false,\n  "fixed-cidr-v6": "fd00::/80"\n}\n' > "$cfg" 2>/dev/null || {
    echo "skip: test_configure_docker_ipv6_sed_path_replaces_false_value needs a writable /etc/docker"
    return 0
  }

  # Hide jq: a stub bin dir with every tool the sed path needs except jq.
  local work bindir old_path
  work=$(mktemp -d); bindir="$work/bin"; mkdir -p "$bindir"
  local t
  for t in uname grep cp sed sleep rm mkdir chmod mv tee; do
    ln -s "$(command -v "$t")" "$bindir/$t"
  done
  old_path="$PATH"
  PATH="$bindir"
  trap 'PATH="$old_path"; restore_cfg' EXIT

  restart_docker_checked() { return 0; }
  sleep() { :; }

  configure_docker_ipv6 >"$work/out" 2>&1 || fail "R2-C4-01: sed-path update of a false value must succeed"
  grep -q '"ipv6": true' "$cfg" || fail "R2-C4-01: sed path must rewrite the false value to true"
  [[ "$(grep -c '"ipv6"' "$cfg")" == "1" ]] || fail "R2-C4-01: sed path must not leave a duplicate ipv6 key"
)

# R2-C4-01: an already-true value is accepted without rewrite/restart.
test_configure_docker_ipv6_true_value_is_accepted_without_rewrite() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  local cfg="/etc/docker/daemon.json" bak had_cfg=0
  bak=$(mktemp)
  restore_cfg() {
    if [[ "$had_cfg" == "1" ]]; then cp "$bak" "$cfg" 2>/dev/null || true; else rm -f "$cfg"; fi
    rm -f "$bak"
  }
  trap restore_cfg EXIT
  if [[ -f "$cfg" ]]; then had_cfg=1; cp "$cfg" "$bak" 2>/dev/null || { echo "skip: test_configure_docker_ipv6_true_value_is_accepted_without_rewrite needs a readable /etc/docker/daemon.json"; return 0; }; fi
  if ! mkdir -p /etc/docker 2>/dev/null; then
    echo "skip: test_configure_docker_ipv6_true_value_is_accepted_without_rewrite needs a writable /etc/docker"
    return 0
  fi
  printf '{\n  "ipv6": true,\n  "fixed-cidr-v6": "fd00::/80"\n}\n' > "$cfg" 2>/dev/null || {
    echo "skip: test_configure_docker_ipv6_true_value_is_accepted_without_rewrite needs a writable /etc/docker"
    return 0
  }

  local work restarted=0
  work=$(mktemp -d)
  restart_docker_checked() { restarted=1; return 0; }
  sleep() { :; }

  configure_docker_ipv6 >"$work/out" 2>&1 || fail "R2-C4-01: an already-true value must succeed"
  grep -q "✅ Docker 已配置 IPv6 支持" "$work/out" || fail "R2-C4-01: an already-true value must print the configured receipt"
  assert_equals "0" "$restarted" "R2-C4-01: an already-true value must not restart docker"
  grep -q '"ipv6": true' "$cfg" || fail "R2-C4-01: the true value must be left in place"
)

# R2-C4-01: the macOS branch must verify with docker_ipv6_usable
# instead of assuming Docker Desktop enables IPv6 by default.
test_configure_docker_ipv6_darwin_does_not_claim_without_proof() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  uname() { echo "Darwin"; }
  docker_ipv6_usable() { return 1; }

  local out rc="0"
  out=$(configure_docker_ipv6 2>&1) || rc="$?"
  [[ "$rc" != "0" ]] || fail "R2-C4-01: Darwin without verified IPv6 must not return success"
  if grep -q "✅" <<< "$out"; then
    fail "R2-C4-01: Darwin must not print a success receipt without verification"
  fi
)

test_configure_docker_ipv6_darwin_verified_prints_receipt() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  uname() { echo "Darwin"; }
  docker_ipv6_usable() { return 0; }

  local out rc="0"
  out=$(configure_docker_ipv6 2>&1) || rc="$?"
  assert_equals "0" "$rc" "R2-C4-01: Darwin with verified IPv6 must succeed"
  grep -q "✅" <<< "$out" || fail "R2-C4-01: Darwin with verified IPv6 must print the verified receipt"
)

# W1-DLMV: download_agent_binary 落盘 mv 失败必须点名返回非 0，
# 不得续印「下载完成」。旧实现 mv 无守卫、失败后 dest 缺失/为空
# 仍报 ✅。
test_install_download_mv_failure_is_named() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  ensure_download_routes() { DOWNLOAD_ROUTES=("" ); }
  extend_download_routes() { return 1; }
  verify_downloaded_binary() { return 0; }
  curl() { local out="" prev=""; for a in "$@"; do [[ "$prev" == "-o" ]] && out="$a"; prev="$a"; done; printf 'binary-bytes' > "$out"; }
  mv() { return 1; }

  local d out rc="0"
  d=$(mktemp -d)
  out=$(download_agent_binary "$d/flmx_agent" 2>&1) || rc="$?"
  [[ "$rc" != "0" ]] || fail "W1-DLMV: mv failure must return non-zero"
  [[ "$out" == *"落盘失败"* ]] || fail "W1-DLMV: mv failure must be named in the receipt"
  [[ "$out" != *"✅"* ]] || fail "W1-DLMV: mv failure must not print a success receipt"
)

# W1-LEGACY-SCOPE: cleanup_legacy_gost_installation 的二进制删除
# 按删除前采集的 unit 引用集合逐个判定——被引用才删、未引用
# （如别产品 gost）保留并点名。旧实现按分支整集删，第一分支连
# /usr/local/bin/gost 一并删。
test_legacy_gost_cleanup_scoped_binary_deletion() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local d
  d=$(mktemp -d)
  LEGACY_GOST_BINARY="$d/usr-bin-gost"
  LEGACY_GOST_CONFIG_DIR="$d/etc-gost"
  mkdir -p "$LEGACY_GOST_CONFIG_DIR"
  LEGACY_GOST_SERVICE_FILE_ETC="$d/gost.service"
  LEGACY_GOST_SERVICE_FILE_LIB="$d/none1"
  LEGACY_GOST_SERVICE_FILE_USR_LIB="$d/none2"
  printf 'x' > "$LEGACY_GOST_BINARY"
  printf 'x' > "$LEGACY_GOST_CONFIG_DIR/gost"
  SERVICE_MANAGER="systemd"
  systemctl() { return 0; }
  legacy_gost_cleanup_ownership_confirmed() { return 0; }

  printf '[Service]\nWorkingDirectory=%s\nExecStart=%s/gost\n' "$LEGACY_GOST_CONFIG_DIR" "$LEGACY_GOST_CONFIG_DIR" > "$LEGACY_GOST_SERVICE_FILE_ETC"
  cleanup_legacy_gost_installation >/dev/null 2>&1
  [[ -f "$LEGACY_GOST_BINARY" ]] || fail "W1-LEGACY-SCOPE: unreferenced binary must be preserved"
  [[ ! -f "$LEGACY_GOST_CONFIG_DIR/gost" ]] || fail "W1-LEGACY-SCOPE: referenced config-dir binary must be removed"
  [[ ! -f "$LEGACY_GOST_SERVICE_FILE_ETC" ]] || fail "W1-LEGACY-SCOPE: service file must be removed"

  printf 'x' > "$LEGACY_GOST_CONFIG_DIR/gost"
  printf '{}' > "$LEGACY_GOST_CONFIG_DIR/config.json"
  printf '{}' > "$LEGACY_GOST_CONFIG_DIR/gost.json"
  printf '[Service]\nWorkingDirectory=%s\nExecStart=%s\n' "$LEGACY_GOST_CONFIG_DIR" "$LEGACY_GOST_BINARY" > "$LEGACY_GOST_SERVICE_FILE_ETC"
  cleanup_legacy_gost_installation >/dev/null 2>&1
  [[ ! -f "$LEGACY_GOST_BINARY" ]] || fail "W1-LEGACY-SCOPE: referenced binary must be removed"
)

# W1-JSONESC: json_escape 全 C0 控制字符按 JSON 文法转义（旧手写
# 表漏 \b \f 与其余控制字符、写出非法 JSON）；agent_config_addr
# 提取按 JSON 字符串文法，转义引号不截断。
test_json_escape_and_config_addr_grammar() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local esc d addr
  esc=$(json_escape "$(printf 'a\x01b\x08c\x0cd\te"f\\g')")
  assert_equals 'a\u0001b\bc\fd\te\"f\\g' "$esc" "W1-JSONESC: control characters must be escaped per JSON grammar"
  esc=$(json_escape 'https://panel.example.com:8443')
  assert_equals 'https://panel.example.com:8443' "$esc" "W1-JSONESC: plain values pass through"

  d=$(mktemp -d)
  printf '{ "addr": "https://pan\\"el.example.com", "secret": "x" }\n' > "$d/config.json"
  addr=$(agent_config_addr "$d")
  assert_equals 'https://pan"el.example.com' "$addr" "W1-JSONESC: escaped quote must not truncate addr extraction"
  printf '{ "addr": "wss://a.example.com", "secret": "x" }\n' > "$d/config.json"
  addr=$(agent_config_addr "$d")
  assert_equals 'wss://a.example.com' "$addr" "W1-JSONESC: plain addr extraction unchanged"
)

# W1-UNINSTALL-VERDICT: install.sh 卸载完成回执须有后置实证——
# 任一对象删失败即点名残留、返回非 0、不印「✅ 卸载完成」；全
# 删净才印完成。旧实现 rm 集裸调用、恒回 0 无条件报完成。夹具
# 口径同 R6-02：调优路径全集改指临时目录、异面板地址＋接管标
# 志过归属守卫。
test_install_uninstall_verdict_requires_poststate() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local root
  root=$(mktemp -d)
  SERVER_ADDR=""
  INSTALL_DIR="$root/cur"; LEGACY_INSTALL_DIR="$root/legacy"
  mkdir -p "$INSTALL_DIR"
  printf 'bin' > "$INSTALL_DIR/flmx_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  FLMX_AGENT_SYSTEMD_SERVICE_FILE="$root/flmx_agent.service"
  LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE="$root/flux_agent.service"
  FLMX_AGENT_OPENRC_SERVICE_FILE="$root/initd-cur"
  LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE="$root/initd-legacy"
  FLMX_AGENT_LOG_DIR="$root/logs"; FLMX_AGENT_LOGROTATE_FILE="$root/logrotate"
  FLMX_TUNING_SYSCTL_CONF="$root/99-flmx-tuning.conf"
  FLMX_TUNING_SYSCTL_CONF_LEGACY="$root/99-flvx-tuning.conf"
  FLMX_TUNING_BBR_MODULE_FILE="$root/flmx-bbr.conf"
  FLMX_TUNING_QDISC_UNIT_FILE="$root/flmx-tuning-qdisc.service"
  FLMX_TUNING_KERNEL_CONF="$root/99-network-performance.conf"
  FLMX_TUNING_LIMITS_CONF="$root/99-limits.conf"
  FLMX_TUNING_BBR_CONF="$root/10-bbr.conf"
  FLMX_TUNING_GAI_CONF="$root/gai.conf"
  FLMX_TUNING_GAI_BACKUP="$root/gai.conf.bak"
  FLMX_TUNING_RPS_UNIT_FILE="$root/flmx-tuning-rps.service"
  FLMX_TUNING_RPS_SCRIPT="$root/flmx-tuning-rps.sh"
  FLMX_PROC_SYS_DIR="$root/proc/sys"
  SERVICE_MANAGER="systemd"
  systemctl() { return 0; }
  nft() { cat >/dev/null; return 0; }
  iptables() { return 0; }

  local out rc="0"
  out=$(printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flmx_agent 2>&1) || rc="$?"
  assert_equals "0" "$rc" "W1-UNINSTALL-VERDICT: clean uninstall should succeed"
  [[ "$out" == *"✅ 卸载完成"* ]] || fail "W1-UNINSTALL-VERDICT: clean uninstall should print the completion receipt"
  [[ ! -e "$INSTALL_DIR" ]] || fail "W1-UNINSTALL-VERDICT: install dir must be gone"

  # 服务文件删失败（rm 桩对该路径恒失败）→ 残留点名、非 0、无完成回执
  mkdir -p "$INSTALL_DIR"
  printf 'bin' > "$INSTALL_DIR/flmx_agent"
  printf '%s' '{"addr": "https://other.example.com:443", "secret": "x"}' > "$INSTALL_DIR/config.json"
  printf 'unit' > "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
  rm() {
    local a
    for a in "$@"; do [[ "$a" == "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" ]] && return 1; done
    command rm "$@"
  }
  rc="0"
  out=$(printf 'y\n' | FLMX_AGENT_TAKEOVER="1" uninstall_flmx_agent 2>&1) || rc="$?"
  [[ "$rc" != "0" ]] || fail "W1-UNINSTALL-VERDICT: leftover service file must fail the uninstall"
  [[ "$out" == *"卸载未完全完成"* ]] || fail "W1-UNINSTALL-VERDICT: leftover must be named, got: $out"
  [[ "$out" != *"✅ 卸载完成"* ]] || fail "W1-UNINSTALL-VERDICT: leftover must not print the completion receipt"
)

# W1-PANEL-UNINSTALL-VERDICT: panel Docker 分支卸载在印「卸载完
# 成」前实查 compose/.env/backups 后置状态，残留即点名非 0。
test_panel_uninstall_docker_branch_verifies_poststate() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/panel_install.sh"

  check_docker() { DOCKER_CMD="docker"; return 0; }
  docker() { return 0; }

  local d out rc="0"
  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  out=$( ( cd "$d" && uninstall_panel <<< "y" 2>&1 ) ) || rc="$?"
  assert_equals "0" "$rc" "W1-PANEL-UNINSTALL-VERDICT: clean docker-branch uninstall should succeed"
  [[ "$out" == *"卸载完成"* ]] || fail "W1-PANEL-UNINSTALL-VERDICT: clean uninstall should print completion"
  [[ ! -e "$d/.env" && ! -e "$d/docker-compose.yml" ]] || fail "W1-PANEL-UNINSTALL-VERDICT: files must be gone"

  d=$(mktemp -d)
  printf 'JWT_SECRET=x\n' > "$d/.env"
  printf 'services:\n  backend:\n    image: ghcr.io/iiiiiiiii7125-art/flmx-flux-panel-backend:v-test\n' > "$d/docker-compose.yml"
  rm() {
    local a
    for a in "$@"; do [[ "$a" == *docker-compose.yml ]] && return 1; done
    command rm "$@"
  }
  rc="0"
  out=$( ( cd "$d" && uninstall_panel <<< "y" 2>&1 ) ) || rc="$?"
  [[ "$rc" != "0" ]] || fail "W1-PANEL-UNINSTALL-VERDICT: leftover compose must fail the uninstall"
  [[ "$out" == *"卸载未完全完成"* ]] || fail "W1-PANEL-UNINSTALL-VERDICT: leftover must be named, got: $out"
  [[ "$out" != *"✅ 卸载完成"* ]] || fail "W1-PANEL-UNINSTALL-VERDICT: leftover must not print completion"
)

# W1-SELFCHECK: delete_self 本体校验须为多因子——仅含 REPO 钉死
# 行的异物文件不得通过（旧单因子校验可被改 REPO 值的异物骗过，
# C4-低·delete_self自指/无校验）；全因子齐备才认本体。反向臂：
# 任一因子缺失即拒绝；若校验退回单因子，本用例必红。
test_delete_self_multifactor_rejects_repo_line_only() (
  set -euo pipefail
  load_script_without_main "$ROOT_DIR/install.sh"

  local f
  f=$(mktemp)
  printf 'REPO="iiiiiiiii7125-art/flmx-max"\n' > "$f"
  if flmx_script_file_is_self "$f" 2>/dev/null; then
    fail "W1-SELFCHECK: REPO-line-only file must not pass install self check"
  fi
  printf '#!/bin/bash\nREPO="iiiiiiiii7125-art/flmx-max"\n' > "$f"
  if flmx_script_file_is_self "$f" 2>/dev/null; then
    fail "W1-SELFCHECK: file without marker functions must not pass install self check"
  fi
  printf '#!/bin/bash\nREPO="iiiiiiiii7125-art/flmx-max"\ninstall_flmx_agent() { :; }\ndelete_self() { :; }\n' > "$f"
  flmx_script_file_is_self "$f" || fail "W1-SELFCHECK: full-factor file must pass install self check"
  flmx_script_file_is_self "$ROOT_DIR/install.sh" || fail "W1-SELFCHECK: install.sh itself must pass its own self check"

  load_script_without_main "$ROOT_DIR/panel_install.sh"
  printf 'REPO="iiiiiiiii7125-art/flmx-max"\n' > "$f"
  if panel_script_file_is_self "$f" 2>/dev/null; then
    fail "W1-SELFCHECK: REPO-line-only file must not pass panel self check"
  fi
  printf '#!/bin/bash\nREPO="iiiiiiiii7125-art/flmx-max"\nSCRIPT_VERSION="9.9.9"\ninstall_panel() { :; }\ndelete_self() { :; }\n' > "$f"
  panel_script_file_is_self "$f" || fail "W1-SELFCHECK: full-factor file must pass panel self check"
  panel_script_file_is_self "$ROOT_DIR/panel_install.sh" || fail "W1-SELFCHECK: panel_install.sh itself must pass its own self check"
)

test_upsert_env_var_literal_values_and_key_parity
test_panel_version_gate_matches_go_semantics
test_install_version_gate_matches_go_semantics
test_caddyfile_without_site_single_line_blocks
test_panel_network_default_name_and_label_reconcile
test_configure_docker_ipv6_false_value_is_updated_not_claimed
test_configure_docker_ipv6_sed_path_replaces_false_value
test_configure_docker_ipv6_true_value_is_accepted_without_rewrite
test_configure_docker_ipv6_darwin_does_not_claim_without_proof
test_configure_docker_ipv6_darwin_verified_prints_receipt
test_install_download_mv_failure_is_named
test_legacy_gost_cleanup_scoped_binary_deletion
test_json_escape_and_config_addr_grammar
test_install_uninstall_verdict_requires_poststate
test_panel_uninstall_docker_branch_verifies_poststate
test_delete_self_multifactor_rejects_repo_line_only

echo "install script tests passed"
