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

    # apk 失败时 bash 仍不在位：不得带着误导性的 exec 报错收场，
    # 先验在位、点名缺失的包与人工装法（回执与结果一致）。
    if ! command -v bash >/dev/null 2>&1; then
      echo "❌ Alpine 安装 bash 失败，请先手动执行 apk add --no-cache bash 后重跑" >&2
      exit 1
    fi

    exec bash "$0" "$@"
  fi

  echo "❌ 此安装脚本需要 Bash。" >&2
  exit 1
fi

# H1-05: ≤512MB 小内存机无 swap 时装机自动建 1G swap 的常量（组网
# 定式⑤的代码面落地）。三个路径/阈值变量做成可覆盖仅为测试夹具改
# 指临时目录与伪造内存面，生产缺省值即注释所列；退出开关沿用脚本
# 既有 FLMX_* 命名族。
FLMX_SWAP_FILE="${FLMX_SWAP_FILE:-/swapfile}"
FLMX_FSTAB_FILE="${FLMX_FSTAB_FILE:-/etc/fstab}"
FLMX_MEMINFO_FILE="${FLMX_MEMINFO_FILE:-/proc/meminfo}"

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
# OpenRC 形态的日志轮转配置落点（GARBAGE-AUDIT A2）：output_log/
# error_log 由 OpenRC 直接 append、无封顶，随安装落一份 logrotate
# 配置封顶；卸载时一并删除（remove_flmx_agent_service）。
FLMX_AGENT_LOGROTATE_FILE="${FLMX_AGENT_LOGROTATE_FILE:-/etc/logrotate.d/flmx_agent}"
# 定时重启 timer 对落点（DEV-RESTART）：单元由 agent 运行时按面板
# 下发设置生成（go-gost/x/socket/restart_schedule.go 的单元名常量
# 与 restartScheduleUnitDir），install.sh 本身不写，但卸载必须回
# 收——timer 带 Persistent=true 且处于 enabled 时，卸载后仍会每
# 日触发已删的 agent 服务（R2-SCR-F05）。做成变量以便测试夹具
# 改指临时目录（与 FLMX_AGENT_LOGROTATE_FILE 同钩子）。
FLMX_AGENT_RESTART_TIMER_FILE="${FLMX_AGENT_RESTART_TIMER_FILE:-/etc/systemd/system/flmx-agent-restart.timer}"
FLMX_AGENT_RESTART_SERVICE_FILE="${FLMX_AGENT_RESTART_SERVICE_FILE:-/etc/systemd/system/flmx-agent-restart.service}"
FLMX_AGENT_RESTART_TIMER_UNIT="flmx-agent-restart.timer"
FLMX_AGENT_RESTART_SERVICE_UNIT="flmx-agent-restart.service"
# OpenRC 日志面落点目录：日志本体由 OpenRC 服务定义的
# output_log/error_log 长期 append 写入，两代布局名（flmx_agent/
# flux_agent）各一对（见现行与存量 OpenRC 模板），轮转件为
# logrotate 的 rotate 产物。做成变量以便测试夹具改指临时目录。
FLMX_AGENT_LOG_DIR="${FLMX_AGENT_LOG_DIR:-/var/log}"
# OpenRC pidfile 单一来源（A3-08）：服务文件里写的 pidfile 与脚本
# 判活（flmx_agent_main_pid）读的 pidfile 是同一个变量值——模板生
# 成时把变量现值写进服务文件，不再写死 /run/${RC_SVCNAME}.pid。
# 旧实现三处分家（模板写死、脚本读覆盖值、存量侧读第三个从未赋
# 值的名字），覆盖一旦设置判活恒失败。改名后以 FLMX_ 为准，旧
# FLUX_ 变量仅回退读；存量服务同理走 LEGACY_ 变量（回退同名
# FLUX_ 链），两个模板与两个读面各自同源。
FLMX_AGENT_OPENRC_PIDFILE="${FLMX_AGENT_OPENRC_PIDFILE:-${FLUX_AGENT_OPENRC_PIDFILE:-/run/flmx_agent.pid}}"
LEGACY_FLUX_AGENT_OPENRC_PIDFILE="${LEGACY_FLUX_AGENT_OPENRC_PIDFILE:-${FLUX_AGENT_OPENRC_PIDFILE:-/run/flux_agent.pid}}"
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
# 侧 go-gost/x/socket/tuning.go 的路径常量一一对应——第二代四面
# 模型：tuneKernelConfPath / tuneLimitsConfPath / tuneBBRConfPath /
# tuneGaiConfPath / tuneGaiBackupPath / tuneRPSUnitPath /
# tuneRPSScriptPath / tuneKernelSnapshotPath；第一代残留：oldTuneConfPath /
# oldTuneBBRModulePath / oldTuneShapeUnitPath / legacyTuneConfPath
# （全集见 agent 侧 uninstall.go 的 hostTuningFileSet）。脚本卸载
# 必须与面板自卸载路径（runAgentUninstall →
# cleanupHostTuningFiles）对齐清理这些文件，见
# uninstall_flmx_agent_host_cleanup。做成变量以便测试夹具改指临时
# 目录。
# 第二代（四面模型）：
FLMX_TUNING_KERNEL_CONF="/etc/sysctl.d/99-network-performance.conf"
FLMX_TUNING_LIMITS_CONF="/etc/security/limits.d/99-network-performance.conf"
FLMX_TUNING_BBR_CONF="/etc/sysctl.d/10-bbr.conf"
FLMX_TUNING_GAI_CONF="/etc/gai.conf"
FLMX_TUNING_GAI_BACKUP="/etc/gai.conf.bak"
FLMX_TUNING_GAI_PRECEDENCE_LINE="precedence ::ffff:0:0/96  100"
FLMX_TUNING_RPS_UNIT_FILE="/etc/systemd/system/flmx-tuning-rps.service"
FLMX_TUNING_RPS_UNIT="flmx-tuning-rps.service"
FLMX_TUNING_RPS_SCRIPT="/usr/local/bin/flmx-tuning-rps.sh"
# 第二代面③ 的 TL-02 调优前 sysctl 快照（agent 侧
# tuneKernelSnapshotPath）：卸载时按快照写回原值后消费删除，
# 见 uninstall_flmx_agent_host_cleanup 第 2) 步二代部分。
FLMX_TUNING_KERNEL_SNAPSHOT="/etc/security/flmx-tuning-kernel-sysctls.json"
# 无 TL-02 快照时不可恢复的面③键：agent 侧 kernelTuneSettings
# 全集去掉两项可回退标题默认值的键（net.core.default_qdisc、
# net.ipv4.tcp_congestion_control）；net.core.rps_sock_flow_entries
# 由标题默认值回退覆盖、不在此列。与 agent 侧
# restoreKernelSysctls 的 unrestored 清单逐字对齐。
FLMX_TUNING_KERNEL_UNRESTORED_KEYS="net.core.somaxconn net.core.netdev_max_backlog net.ipv4.tcp_max_syn_backlog net.ipv4.ip_local_port_range net.core.rmem_max net.core.wmem_max net.ipv4.tcp_rmem net.ipv4.tcp_wmem net.core.rmem_default net.core.wmem_default net.ipv4.tcp_notsent_lowat net.ipv4.tcp_mtu_probing net.ipv4.udp_rmem_min net.ipv4.udp_wmem_min net.ipv4.tcp_ecn net.ipv4.tcp_max_orphans net.ipv4.tcp_slow_start_after_idle net.ipv4.tcp_tw_reuse net.ipv4.tcp_fin_timeout net.ipv4.tcp_retries2 net.ipv4.tcp_fastopen"
# 第一代残留（兼容清理）：
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
  local apk_rc=0
  if [[ -n "$privileged_command" ]]; then
    "$privileged_command" apk add --no-cache "${missing_packages[@]}" || apk_rc=$?
  else
    apk add --no-cache "${missing_packages[@]}" || apk_rc=$?
  fi
  if [[ "$apk_rc" -ne 0 ]]; then
    echo "❌ Alpine 运行依赖安装失败（apk 退出码 $apk_rc），缺少的软件包: ${missing_packages[*]}；请先手动执行 apk add --no-cache ${missing_packages[*]} 后重跑" >&2
    return 1
  fi
}

# H1-05: 小内存机 swap 装机防线（组网定式⑤「内存 ≤512MB 装机即加
# 1G swap 并 fstab 持久化」的代码面落地）。背景：ECS1686（464MB＋
# Swap=0）上 agent 长活驻留会话累积、夜间被 oom-killer 反复杀死
# （见 audit-out/release-2.1.0-audit/R1-H1-history-regression.md
# §9.2/§10 H1-05）；此前装机流程对 swap 零动作、零提示，同型新机
# 按现行脚本装机仍可复现。本步口径（裁定：自动建＋幂等＋可退出）：
#   - 判定只认 /proc/meminfo 实读：MemTotal ≤512MB 且 SwapTotal=0
#     才动手；内存 >512MB 跳过并说明；已有 swap（任意量，含
#     zram/分区）不动并输出现状。
#   - 动作：fallocate 建 1G（不可用时退 dd）→ chmod 600 → mkswap →
#     swapon → 按文件路径判重写 /etc/fstab 持久化；全序任一步失败
#     即回滚半成品（已启用先 swapoff、半成品文件删除、未生效不写
#     fstab），点名「swap 未建、OOM 风险在」并 return 1——全部调用
#     方（新装 install_flmx_agent / 更新 update_flmx_agent 两函数
#     头，非交互参数路径与菜单 1/2 均经这两函数汇入）一律只告警
#     续行：失败只经 return 1 回传，main 以 `|| rc=$?` 收下且不据
#     此中止后续流程。调用点全集＝本函数定义＋上述两函数头各一
#     处，不在别处散挂。
#   - 幂等：swapfile 已在 fstab 且已启用＝已建形态，二跑零改动；
#     仅缺 fstab 行时只补行、不重建。
#   - 退出开关：FLMX_SKIP_SWAP=1 跳过并明示（大小写不敏感，与
#     PROXY_ENABLED 的 L2-33 同口径）。
# 每次运行都输出实读内存值、判定结果与自检法（free -h /
# swapon --show）。
ensure_small_host_swap() {
  local meminfo_file="$FLMX_MEMINFO_FILE"
  local swap_file="$FLMX_SWAP_FILE"
  local fstab_file="$FLMX_FSTAB_FILE"

  # 实读先行：退出开关分支也要带实读值与自检法输出（读失败时后续
  # 统一判定分支同样会跳过，不在此重复处理）。
  local mem_kb="" swap_kb=""
  mem_kb=$(awk '/^MemTotal:/ {print $2}' "$meminfo_file" 2>/dev/null || true)
  swap_kb=$(awk '/^SwapTotal:/ {print $2}' "$meminfo_file" 2>/dev/null || true)

  case "${FLMX_SKIP_SWAP:-}" in
    1|[Tt][Rr][Uu][Ee]|[Yy][Ee][Ss])
      if [[ -n "$mem_kb" ]]; then
        echo "⏭️ FLMX_SKIP_SWAP 已设置，跳过小内存机 swap 检查（本机内存实读 $(( mem_kb / 1024 ))MiB、当前 SwapTotal $(( ${swap_kb:-0} / 1024 ))MiB；本机若 ≤512MB 且无 swap，OOM 风险自担；自检: free -h; swapon --show）"
      else
        echo "⏭️ FLMX_SKIP_SWAP 已设置，跳过小内存机 swap 检查（本机若 ≤512MB 且无 swap，OOM 风险自担；自检: free -h; swapon --show）"
      fi
      return 0
      ;;
  esac
  if [[ -z "$mem_kb" ]]; then
    echo "⚠️ 无法实读 $meminfo_file 的 MemTotal，跳过小内存机 swap 检查（请人工自检: free -h; swapon --show）"
    return 0
  fi
  swap_kb="${swap_kb:-0}"
  local mem_mib=$(( mem_kb / 1024 ))
  local swap_mib=$(( swap_kb / 1024 ))

  if [[ "$mem_kb" -gt 524288 ]]; then
    echo "ℹ️ 本机内存实读 ${mem_mib}MiB（>512MiB），无需装机自动建 swap（当前 SwapTotal ${swap_mib}MiB；自检: swapon --show）"
    return 0
  fi
  if [[ "$swap_kb" -gt 0 ]]; then
    echo "ℹ️ 本机内存实读 ${mem_mib}MiB（≤512MiB），已有 swap ${swap_mib}MiB，保持现状不新建（自检: swapon --show）"
    if [[ -f "$swap_file" ]] && ! swapon --show 2>/dev/null | grep -q "^${swap_file} "; then
      echo "⚠️ 注意: $swap_file 文件在位但当前未启用（swapon --show 未见），重启后是否自动启用取决于 $fstab_file 是否有对应行，请人工核对"
    fi
    return 0
  fi

  echo "🧩 本机内存实读 ${mem_mib}MiB（≤512MiB）且无 swap，按组网定式⑤自动建 1G swap: $swap_file"

  # fstab 在位判定（按文件路径判重，幂等二跑的依据）：fstab 中以
  # 本路径为第一字段、类型为 swap 的行。
  local in_fstab=""
  if [[ -f "$fstab_file" ]] && awk -v f="$swap_file" '$1 == f && $3 == "swap" {found=1} END {exit !found}' "$fstab_file"; then
    in_fstab="1"
  fi

  # 已启用判定用 /proc/swaps 实读（swapon --show 的数据源），不
  # 依赖 util-linux 的输出格式。
  local active=""
  if grep -q "^${swap_file} " /proc/swaps 2>/dev/null; then
    active="1"
  fi

  if [[ -n "$active" && -n "$in_fstab" ]]; then
    echo "✅ 1G swap 已在位且已持久化（$swap_file，先前装机已建），本次不重复建（自检: swapon --show）"
    return 0
  fi

  # 回滚闭包：只有本步刚 swapon 成功的才 swapoff（active 判定在进
  # 入时已存量在位的形态不进建流程、不会走到这里）。文件删除只对
  # 本步新建的：进入时已在位且大小恰为 1G 的文件是复用形态（上次
  # 中断的半成品或人工预建），回滚不删它。
  local swapped_on="" created_here=""
  swap_step_abort() {
    echo "❌ swap 未建、OOM 风险在: $1" >&2
    echo "   本步已回滚半成品；请处理后（查 df -h / 的可用空间与文件系统支持）重跑装机，或人工建: fallocate -l 1G $swap_file && chmod 600 $swap_file && mkswap $swap_file && swapon $swap_file，并把「$swap_file none swap sw 0 0」写入 $fstab_file" >&2
    echo "   自检: free -h; swapon --show" >&2
    if [[ -n "$swapped_on" ]]; then
      swapoff "$swap_file" 2>/dev/null || true
    fi
    if [[ -n "$created_here" ]]; then
      rm -f "$swap_file" 2>/dev/null || true
    fi
    return 1
  }

  if [[ -z "$active" ]]; then
    # 建文件：已存在且大小恰为 1G 的文件可复用（上次中断的半成品
    # 或人工预建），其余形态一律先删后建，不在未知内容上 mkswap。
    # 大小判定用 stat 表观大小：稀疏文件 du 读 0、会把可复用文
    # 件误判成残缺；fallocate/dd 建的都是表观 1G。
    local existing_bytes=""
    if [[ -f "$swap_file" ]]; then
      existing_bytes=$(stat -c %s "$swap_file" 2>/dev/null || true)
    fi
    if [[ "$existing_bytes" != "1073741824" ]]; then
      rm -f "$swap_file" || { swap_step_abort "旧 swapfile 残留删除失败: $swap_file"; return 1; }
      created_here="1"
      if ! fallocate -l 1G "$swap_file" 2>/dev/null; then
        # fallocate 不可用或文件系统不支持时退 dd（慢但通用）。
        rm -f "$swap_file"
        dd if=/dev/zero of="$swap_file" bs=1M count=1024 2>/dev/null || { swap_step_abort "创建 1G swapfile 失败（磁盘空间不足或文件系统不支持）: $swap_file"; return 1; }
      fi
    fi
    chmod 600 "$swap_file" || { swap_step_abort "设置 swapfile 权限 600 失败: $swap_file"; return 1; }
    # 权限回读复核：chmod 成功不等于形态正确（如被 ACL/挂载选项改
    # 写），以 stat 实读回断言 600，不符按失败回滚。
    local perm_now=""
    perm_now=$(stat -c %a "$swap_file" 2>/dev/null || true)
    if [[ "$perm_now" != "600" ]]; then
      swap_step_abort "swapfile 权限回读为 ${perm_now:-未知}、不是 600: $swap_file"
      return 1
    fi
    mkswap "$swap_file" >/dev/null 2>&1 || { swap_step_abort "mkswap 失败: $swap_file"; return 1; }
    swapon "$swap_file" 2>/dev/null || { swap_step_abort "swapon 启用失败: $swap_file"; return 1; }
    swapped_on="1"
  fi

  if [[ -z "$in_fstab" ]]; then
    printf '%s none swap sw 0 0\n' "$swap_file" >> "$fstab_file" || { swap_step_abort "写入 fstab 持久化失败: $fstab_file"; return 1; }
  fi

  # 落账复核：刚写完必须回读在位——fstab 行回读断言（按路径＋类
  # 型双字段，比写入时判重同口径），不在位即本步未完成、点名报
  # 错（不回滚已启用的 swap：运行时保护已生效，只是持久化缺失、
  # 重启后会丢，人工补行即可，不必拆已生效面）。
  if [[ -f "$fstab_file" ]] && awk -v f="$swap_file" '$1 == f && $3 == "swap" {found=1} END {exit !found}' "$fstab_file"; then
    echo "   持久化复核: $fstab_file 已含 $swap_file 的 swap 行，重启后自动启用"
  else
    echo "❌ swap 已启用，但持久化复核失败: $fstab_file 未见 $swap_file 的 swap 行——重启后 swap 不会自动启用，请人工补行「$swap_file none swap sw 0 0」" >&2
    echo "   自检: grep swap $fstab_file; swapon --show" >&2
    return 1
  fi

  echo "✅ 已建 1G swap 并启用: $swap_file（权限 600，已写入 $fstab_file 持久化）"
  echo "   自检: free -h 应见 Swap 约 1.0Gi; swapon --show 应见 $swap_file"
  return 0
}

# ---- 内置 GitHub 下载候选与测速直选（1.7.7）----
# 候选清单全产品统一（面板后端 github_routes.go、agent 侧、
# panel_install.sh 同表同序，改一处须同步其余）：9 个代理前缀 +
# 直连（""）。路线选择口径（与面板侧一致）：
#   0) 面板源分发：先单探由 -a 面板地址派生的面板线路（见
#      derive_panel_prefix）——判活后须再过两道门才可独占：重定
#      向延迟 ≤800ms（与直连同门）＋ 256KB 短测速 ≥256KB/s（吞吐
#      门，见 probe_panel_throughput）；任一不过 → 不独占，把面板
#      源按实测延迟并入代理排名（与慢直连同法），下载全败时经
#      extend_download_routes 补测代理排名 + 直连兜底；
#   1) 先单独探测直连（真实目标、~3-5s）：通且延迟 ≤800ms → 只用
#      直连，不测代理（与 agent 侧 go-gost/x/internal/util/download/routes.go 的短路门同口径）；通但
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
# 渠道 tag 文法的唯一出处（J-02）：稳定渠道只接受纯数字点分
# tag；FLMX_RELEASE_CHANNEL=dev|beta 时接受任意数字开头 tag。
# resolve_latest_release_tag 与 probe_route 共用——FIX2/D-05 前探
# 测侧写死宽文法，稳定渠道下会把重定向落到预发布 tag 的路线判
# 活，与解析器随后按渠道文法拒绝该 tag 的口径漂移（A1-04 已在
# panel_install.sh 以 release_tag_pattern 收敛为同一形态）；对齐
# 后两处同松同紧、永不各自演化。
release_tag_pattern() {
  case "${FLMX_RELEASE_CHANNEL:-stable}" in
    dev|beta) printf '%s' '^[0-9][0-9A-Za-z.\-]*$' ;;
    *) printf '%s' '^[0-9]+(\.[0-9]+)+$' ;;
  esac
}

probe_route() {
  local prefix="$1"
  local out effective tag tag_pattern
  out=$(curl -fsSL -o /dev/null --connect-timeout 3 --max-time 5 -w '%{time_total} %{url_effective}' "$(route_url "$prefix" "https://github.com/${REPO}/releases/latest")" 2>/dev/null) || return 1
  effective="${out#* }"
  tag="${effective##*/}"
  tag_pattern=$(release_tag_pattern)
  [[ "$tag" =~ $tag_pattern ]] || return 1
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

# direct_latency_within_gate <秒>：与 agent 侧 go-gost/x/internal/util/download/routes.go 同口径——直连
# 探测延迟 ≤800ms 才配短路独占；shell 浮点比较走 awk。面板源独占
# 的延迟门与本门同值同口径（FIX2/D-03：旧实现面板分支丢弃
# probe_route 耗时、延迟再高也独占）。
direct_latency_within_gate() {
  awk -v t="$1" 'BEGIN { exit !(t + 0 <= 0.8) }'
}

# probe_panel_throughput <面板源前缀>：面板源独占前的吞吐短测速
# （FIX2/D-01）。经面板源对 probe_route 刚判活的 releases/latest
# 重定向 URL 拉前 256KB（Range bytes=0-262143），--max-time 10
# 硬顶、计时求实际字节速率（KB/s）；达门槛 256KB/s 返回 0，
# 否则（测速失败 / 样本不足 / 速率不达标）返回 1，调用方不独
# 占、并入排名——fail-safe 方向：测不出来就不许独占。样本取
# latest URL 而非已决议本体：路线表构建恒发生在版本决议之前
# （resolve_version 内部即先建表后决议），门不能依赖决议结果；
# latest 样本经一次重定向落到与本体同一 tag 的资产前段，与
# 面板镜像端点的实际转发路径一致。
# 门槛依据：9.2MB gost 本体在整轮 240s 总预算内下完须 ≥约
# 39KB/s 均速；256KB/s 留约 6 倍余量覆盖慢启动与波动，且 256KB
# 样本在门槛速率下约 1s 拉满；10s 硬顶内拉不满 256KB 即速率
# <25.6KB/s、连可完工线都不达，直接不合格。
# 端点忽略 Range 回 200 全体时以实际落盘字节为准（>256KB 时
# curl 达样本量即止）；回 416（本体小于样本量）按失败处理——
# release 本体恒为 MB 级，416 视为异常、不独占。
probe_panel_throughput() {
  local prefix="$1"
  local sample_file metrics code size_download time_total rate
  sample_file=$(mktemp) || return 1
  metrics=$(curl -fsL -o "$sample_file" -w '%{http_code} %{size_download} %{time_total}' \
    --connect-timeout 5 --max-time 10 \
    -H 'Range: bytes=0-262143' \
    "$(route_url "$prefix" "https://github.com/${REPO}/releases/latest")" 2>/dev/null) || { rm -f "$sample_file"; return 1; }
  rm -f "$sample_file"
  code="${metrics%% *}"
  metrics="${metrics#* }"
  size_download="${metrics%% *}"
  time_total="${metrics##* }"
  [[ "$code" == "200" || "$code" == "206" ]] || return 1
  rate=$(awk -v s="$size_download" -v t="$time_total" 'BEGIN { if (t + 0 <= 0) exit 1; printf "%.1f", s / t / 1024 }') || return 1
  awk -v r="$rate" 'BEGIN { exit !(r + 0 >= 256) }'
}

# routes_insert_ranked_member <成员前缀> <成员实测延迟秒>：须在
# rank_proxy_routes 之后调用。把一个「已判活但不配独占」的成员
# （慢直连 = 前缀 ""，或吞吐/延迟门未过的面板源 = 派生前缀）按实
# 测延迟插入已排名路线表（与 Go 侧口径一致：慢者不丢弃、按延迟
# 参与排序；未判活代理无延迟值，成员排在已判活段末尾、未判活段
# 之前）。成员不是直连时，直连 "" 追加为末位兜底——并表后的表
# 已是排名全集、ROUTES_STAGE=2 使 extend_download_routes 不再生
# 效（其阶段 3 分支只对独占表追加「排名＋直连」），不预置直连则
# 整轮全败时无直连可试、丢失旧独占形态的兜底语义。结果写入
# DOWNLOAD_ROUTES，ROUTES_STAGE 置 2。
routes_insert_ranked_member() {
  local member="$1" member_latency="$2"
  DOWNLOAD_ROUTES=()
  local i lat inserted=0
  for i in "${!PROXY_RANKED[@]}"; do
    lat="${PROXY_RANKED_LATENCY[$i]:-}"
    if [[ "$inserted" == "0" ]] && { [[ -z "$lat" ]] || awk -v a="$member_latency" -v b="$lat" 'BEGIN { exit !(a + 0 <= b + 0) }'; }; then
      DOWNLOAD_ROUTES+=("$member")
      inserted=1
    fi
    DOWNLOAD_ROUTES+=("${PROXY_RANKED[$i]}")
  done
  if [[ "$inserted" == "0" ]]; then
    DOWNLOAD_ROUTES+=("$member")
  fi
  if [[ -n "$member" ]]; then
    DOWNLOAD_ROUTES+=("")
  fi
  ROUTES_STAGE=2
}

# routes_insert_slow_direct <直连实测延迟秒>：慢直连是
# routes_insert_ranked_member 的成员="" 特例（既有调用面不变）。
routes_insert_slow_direct() {
  routes_insert_ranked_member "" "$1"
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
      # 就是面板本身。FIX2 收紧独占条件（D-01/D-03）：判活之后
      # 须再过两道门——① 延迟门：重定向往返延迟 ≤800ms，与直连
      # 独占同门（旧实现 >/dev/null 丢弃耗时、4.9s 也独占）；
      # ② 吞吐门：probe_panel_throughput 的 256KB 短测速
      # ≥256KB/s（重定向快、本体 10KB/s 的线路旧实现照样独占、
      # 240s 预算耗尽才改道，D-01/D-02 实证）。两门全过才独占
      # （阶段 3）；任一不过 → 面板源按实测延迟并入代理排名
      # （routes_insert_ranked_member，与慢直连同法、不另造排
      # 名），并设 ROUTES_STAGE=2——extend 的阶段 3 分支只对独
      # 占表生效，并表后的表已含排名全集，整轮失败自然收口。
      # 不通或派生不出，则原样走下方直连短路 → 代理排名的既有
      # 流程。PROXY_URL 显式覆盖与 PROXY_ENABLED=false 在
      # route_mode 层已先行处理，不受本步影响（运维逃生口语义
      # 不变）。
      local panel_prefix=""
      panel_prefix=$(derive_panel_prefix 2>/dev/null || true)
      local direct_latency="" panel_latency=""
      if [[ -n "$panel_prefix" ]] && panel_latency=$(probe_route "$panel_prefix"); then
        if direct_latency_within_gate "$panel_latency" && probe_panel_throughput "$panel_prefix"; then
          DOWNLOAD_ROUTES=("$panel_prefix")
          ROUTES_STAGE=3
          echo "🌐 GitHub 下载路线: 面板源（经本机面板镜像，延迟 ${panel_latency}s、短测速 ≥256KB/s）" >&2
          return 0
        fi
        rank_proxy_routes
        routes_insert_ranked_member "$panel_prefix" "$panel_latency"
        echo "🌐 面板源延迟或吞吐未过独占门（延迟 ${panel_latency}s），已按实测延迟并入测速排名（首选: ${DOWNLOAD_ROUTES[0]:-直连}）" >&2
        return 0
      fi
      if direct_latency=$(probe_route ""); then
        if direct_latency_within_gate "$direct_latency"; then
          DOWNLOAD_ROUTES=("")
          ROUTES_STAGE=1
          echo "🌐 GitHub 下载路线: 直连" >&2
        else
          # L2-06①（R5-FD）：慢直连门——旧实现 probe_route 回读了
          # time_total 却只用成败，探针耗时近 5s 的直连也短路独占。
          # 与 agent 侧 go-gost/x/internal/util/download/routes.go 同口径：>800ms 不短路，按实测延迟
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

# latest_tag_pattern：渠道 tag 形态（J-02，与 Go 侧
# releaseChannelFromTag / stableVersionPattern 同口径）。稳定渠道只
# 接受纯数字 tag；FLMX_RELEASE_CHANNEL=dev|beta 时接受任意数字开
# 头 tag（beta 渠道）。默认稳定渠道，永不解析出 beta。主解析与
# A3-01 异路复核取数共用此定义，避免两处形态漂移。
latest_tag_pattern() {
  case "${FLMX_RELEASE_CHANNEL:-stable}" in
    dev|beta) echo '^[0-9][0-9A-Za-z.\-]*$' ;;
    *) echo '^[0-9]+(\.[0-9]+)+$' ;;
  esac
}

# fetch_latest_tag_from_route <前缀> <max-time>：从单条路线取
# latest tag（先重定向法、后 API 法，与主解析同法），取到合格
# tag 回显并返回 0，否则返回 1。供 corroborate_latest_tag 异路
# 复核使用——复核必须走与主解析相同的取数方法。
fetch_latest_tag_from_route() {
  local prefix="$1" max_time="$2"
  local tag_pattern effective_url tag api_tag
  tag_pattern=$(latest_tag_pattern)
  effective_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' -L --connect-timeout 5 --max-time "$max_time" "$(route_url "$prefix" "https://github.com/${REPO}/releases/latest")" 2>/dev/null || true)
  tag="${effective_url##*/}"
  if [[ "$tag" =~ $tag_pattern ]]; then
    echo "$tag"
    return 0
  fi
  api_tag=$(curl -fsSL --connect-timeout 5 --max-time "$max_time" "$(route_url "$prefix" "https://api.github.com/repos/${REPO}/releases/latest")" 2>/dev/null | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' || true)
  if [[ "$api_tag" =~ $tag_pattern ]]; then
    echo "$api_tag"
    return 0
  fi
  return 1
}

# latest_source_is_trusted <获胜前缀>：A3-01/A3-03 共用的下载源
# 信任分级。直连（前缀为空）取的是 github.com 本体、面板源前缀
# 是运维自有面板——这两类是规范/自有源，与第三方代理的风险模
# 型不同（见 corroborate_latest_tag）。第三方代理前缀不在此列。
latest_source_is_trusted() {
  local winner="$1"
  [[ -z "$winner" ]] && return 0
  local panel_prefix=""
  panel_prefix=$(derive_panel_prefix 2>/dev/null || true)
  [[ -n "$panel_prefix" && "$winner" == "$panel_prefix" ]]
}

# corroborate_latest_tag <获胜前缀> <tag>：A3-01 版本面第二锚。
# 旧实现逐路线串行、首个解析出合格 tag 的路线即被采信：任一第
# 三方代理只要回一个形态合法的旧 tag，新装节点就静默装上旧版
# 正版 agent（本体与其 .sha256 都从该 tag 取、校验自洽），无任
# 何下限与告警（LIVE-A S3 实证）。现口径：
#   - 任一异路给出不同 tag → 硬失败（有线路在说谎，不采信）；
#   - 异路给出相同 tag → 复核通过；
#   - 异路全部无应答：获胜源是第三方代理 → fail-closed（拒绝
#     单源采信，并提示可显式 VERSION= 兜底）；获胜源是直连/
#     面板源（规范/自有源）或运维显式指定的单路（PROXY_URL 覆
#     盖、PROXY_ENABLED=false）→ 点名后单源采信——这两类形态
#     里不存在「被陌生代理喂值」的威胁，fail-closed 只会打死
#     面板源分发与直连独占两种既定部署形态。
# 整轮复核共用 20s 总界（与二进制第二锚同值），逐路 --max-time
# 取剩余量与 10s 的较小者。路线表只有一条时先 extend 补测一次
# （与 corroborate_binary_anchor 同机制）。
corroborate_latest_tag() {
  local winner="$1" tag="$2"
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
  if [[ ${#candidates[@]} -eq 0 ]]; then
    if latest_source_is_trusted "$winner" || [[ "$(route_mode)" != "builtin" ]]; then
      echo "⚠️ 最新版本 $tag 由单条路线解析、无异路可复核，按该源采信（A3-01）" >&2
      return 0
    fi
    echo "❌ 最新版本解析失败：路线表无第二条路线可复核，拒绝单源采信（A3-01）。可显式指定 VERSION=<版本号> 后重试" >&2
    return 1
  fi
  local deadline=$(( $(date +%s) + 20 ))
  local other remain max_time
  for prefix in "${candidates[@]}"; do
    remain=$(( deadline - $(date +%s) ))
    if (( remain <= 0 )); then
      break
    fi
    max_time=10
    if (( remain < max_time )); then max_time=$remain; fi
    other=$(fetch_latest_tag_from_route "$prefix" "$max_time") || continue
    if [[ "$other" != "$tag" ]]; then
      echo "❌ 最新版本解析跨线路复核不一致，拒绝采信：线路（${winner:-直连}）给出 $tag、线路（${prefix:-直连}）给出 $other（A3-01）" >&2
      return 1
    fi
    # 通过文案走 stderr：本函数的调用方以 $(...) 捕获解析结果，
    # stdout 只许回显 tag 本身。
    echo "✅ 最新版本 $tag 已由第二线路（${prefix:-直连}）复核一致" >&2
    return 0
  done
  if latest_source_is_trusted "$winner"; then
    echo "⚠️ 最新版本 $tag 的异路复核无应答；获胜源为直连/面板源，按单源采信（A3-01）" >&2
    return 0
  fi
  echo "❌ 最新版本解析无法复核：获胜线路（${winner:-直连}）为第三方代理、其他线路均未给出版本号，拒绝单源采信 $tag（A3-01）。可显式指定 VERSION=<版本号> 后重试" >&2
  return 1
}

resolve_latest_release_tag() {
  local effective_url tag api_tag latest_url api_url tag_pattern

  # J-02: 渠道分流口径见 latest_tag_pattern（与主解析外复核共用）。
  tag_pattern=$(latest_tag_pattern)

  latest_url="https://github.com/${REPO}/releases/latest"
  api_url="https://api.github.com/repos/${REPO}/releases/latest"

  # 1.7.7：逐路线尝试（每条路线内先重定向法、后 API 法），首个解析
  # 出合格 tag 的路线成为候选，须经 A3-01 异路复核才采信（见
  # corroborate_latest_tag）；路线全败时按直选口径补测/续切一次。
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
      # A3-01: 合格也只是候选——须经 corroborate_latest_tag 异路复核
      # 才可采信，复核不过整轮失败（不换线重试：有线路在给出不一
      # 致的版本事实，继续采信任何单源都不安全）。
      if [[ "$tag" =~ $tag_pattern ]]; then
        if ! corroborate_latest_tag "$prefix" "$tag"; then
          return 1
        fi
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
        # A3-01: 同重定向法，采信前须过异路复核。
        if ! corroborate_latest_tag "$prefix" "$api_tag"; then
          return 1
        fi
        echo "$api_tag"
        return 0
      fi
    done
    extend_download_routes || break
  done

  return 1
}

# A3-02: 显式版本值的格式校验（与 S-02 在 agent 升级面确立的口径
# 同族）。VERSION/FLMX_VERSION/FLUX_VERSION/PINNED_VERSION 此前原样
# 回显、直接内插进下载 URL：含 ../ 的值经 curl 点段压缩可逃出钉死
# 的 ${REPO} 指向任意 GitHub 仓库的 Release，且 .sha256 由同一拼出
# URL 派生、校验自洽不构成防线（LIVE-A S5 实证）。现统一校验：
# 稳定渠道只接受可选 v 前缀的纯点分数字（集合不超出 latest 稳定
# 渠道正则）；dev/beta 渠道接受与 latest 同集的数字开头 tag；两渠
# 道共同硬拒 /、?、#、%、反斜杠、空白与 ".." 子串——凡能改变 URL
# 指向或逃出版本路径段的字符一律不许进入拼接。
version_value_format_ok() {
  local value="$1"
  [[ -n "$value" ]] || return 1
  # R2-P2-11：总长 ≤40，与规范文法（后端 kernelVersionPattern）
  # 同界；".." 与穿越字符已由下方 case 拒绝、字符集已由渠道正则
  # 把关。panel_install.sh 的 panel_validate_explicit_version 同律。
  (( ${#value} <= 40 )) || return 1
  case "$value" in
    */*|*\?*|*\#*|*%*|*\\*|*[[:space:]]*|*..*) return 1 ;;
  esac
  case "${FLMX_RELEASE_CHANNEL:-stable}" in
    dev|beta) [[ "$value" =~ ^v?[0-9][0-9A-Za-z.\-]*$ ]] ;;
    *) [[ "$value" =~ ^v?[0-9]+(\.[0-9]+)+$ ]] ;;
  esac
}

resolve_version() {
  # 四个显式入口统一过 version_value_format_ok，不合即点名来源中止
  # （A3-02）。优先级顺序不变（RN-1）。
  local explicit="" explicit_source=""
  if [[ -n "${VERSION:-}" ]]; then
    explicit="$VERSION"; explicit_source="VERSION"
  elif [[ -n "${FLMX_VERSION:-}" ]]; then
    explicit="$FLMX_VERSION"; explicit_source="FLMX_VERSION"
  elif [[ -n "${FLUX_VERSION:-}" ]]; then
    explicit="$FLUX_VERSION"; explicit_source="FLUX_VERSION"
  elif [[ -n "${PINNED_VERSION:-}" ]]; then
    explicit="$PINNED_VERSION"; explicit_source="PINNED_VERSION"
  fi
  if [[ -n "$explicit_source" ]]; then
    if ! version_value_format_ok "$explicit"; then
      echo "❌ 版本号格式不合法（来自 $explicit_source）：\"$explicit\"。只接受形如 1.9.9 / v1.9.9 的纯点分数字版本（dev/beta 渠道可带 -dev 等后缀）；含 /、..、?、# 等能改变下载地址的字符一律拒绝（A3-02）" >&2
      return 1
    fi
    echo "$explicit"
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
    # A3-02 双点收口：内插前对 RESOLVED_VERSION 再断言一次格式，
    # 防止未来新调用方绕过 resolve_version 的校验直接赋值。
    if ! version_value_format_ok "${RESOLVED_VERSION:-}"; then
        echo "❌ 版本号格式不合法，拒绝拼接下载地址：\"${RESOLVED_VERSION:-}\"（A3-02）" >&2
        return 1
    fi
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
#   - 全部异路都给不出合法校验值 → A3-03 起 fail-closed（返回 1，
#     S-02「禁止降级跳过」口径）：获胜线路是第三方代理时，单个作
#     恶代理给自洽（假二进制，假 .sha256）对正是第二锚要防的形
#     态，降级等于没防。仅两类形态保留点名降级（返回 0）：路线
#     表只有一条运维显式指定路线（无候选）、或获胜源是直连/面
#     板源（latest_source_is_trusted）——规范/自有源在异路无应
#     答时点名单锚继续，避免打死面板源分发与直连独占。
# 边界（如实写明）：① 路线表只有一条时先按下载主流程同款机制
# extend 补测一次（面板源独占/直连短路形态扩得动；PROXY_URL 覆盖与
# PROXY_ENABLED=false 是运维显式单路，extend 不动、无候选可扩，
# 走点名降级）；② 整轮复核共用 20s 总界（与 Go 两侧及
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
  # A3-03: 异路全部给不出合法校验值时的收口（口径见函数头注）。
  if [[ ${#candidates[@]} -eq 0 ]]; then
    echo "⚠️ 跨线路锚复核已降级：路线表只有一条运维指定路线、无异路可复核，按单锚继续（R7-002 第二锚）" >&2
    return 0
  fi
  if latest_source_is_trusted "$winner"; then
    echo "⚠️ 跨线路锚复核已降级：其他线路均未给出校验值；获胜源为直连/面板源，按单锚继续（R7-002 第二锚）" >&2
    return 0
  fi
  echo "❌ 跨线路锚复核失败：获胜线路（${winner:-直连}）为第三方代理、其他线路均未给出合法校验值，拒绝安装（A3-03：第二锚 fail-closed，不降级单锚）" >&2
  return 1
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
    # 无异路应答按 A3-03 口径 fail-closed（仅直连/面板源与运维显
    # 式单路点名降级，见 corroborate_binary_anchor 头注）。
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
# FIX2/D-02：低速看门狗与 Go 侧对齐为 --speed-limit 40960
# --speed-time 15（40KB/s 连续 15s）——40KB/s 是 9.2MB÷240s 的
# 可完工线取整、15s 窗与 Go 侧 TrickleGrace 同值；10–16KB/s 的
# 塌方线路在首个 15s 窗内判死切线，不再白耗满 240s。总预算维持
# 240s 不动。
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
      if curl -fL -C - --connect-timeout 10 --max-time "$remain" --speed-limit 40960 --speed-time 15 "$url" -o "$part" && [[ -s "$part" ]]; then
        if verify_downloaded_binary "$part" "$url" "$prefix"; then
          # 落盘 mv 必须守卫：mv 失败（目标目录不可写/跨设备等）
          # 时旧实现照印 ✅ 并 return 0，.part 残留、dest 仍是旧
          # 二进制甚至不存在，调用方按「下载成功」继续装机。
          if ! mv -f "$part" "$dest" || [[ ! -s "$dest" ]]; then
            echo "❌ 下载已完成但落盘失败：无法把 $part 移为 $dest" >&2
            rm -f "$part"
            return 1
          fi
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
# delete_self 的本体校验（多因子，两脚本同口径）：常规文件、首
# 行是 sh/bash shebang、含本仓 REPO 钉死行、含本脚本标志性函数
# 定义、且自身定义了 delete_self——全过才认本体。旧实现只查
# REPO 行一因子，校验串与被校验内容同源（把 REPO 值改成本仓地
# 址的异物文件即可骗过自删校验），现以结构因子叠加抬高伪造门
# 槛；校验不过即保留并点名，永不误删。
flmx_script_file_is_self() {
  local path="$1"
  [[ -f "$path" ]] || return 1
  head -n 1 "$path" 2>/dev/null | grep -qE '^#!/[^ ]*/(ba)?sh' || return 1
  grep -qF 'REPO="iiiiiiiii7125-art/flmx-max"' "$path" 2>/dev/null || return 1
  grep -q '^install_flmx_agent()' "$path" 2>/dev/null || return 1
  grep -q '^delete_self()' "$path" 2>/dev/null || return 1
  return 0
}

delete_self() {
  local rc="${1:-0}"
  if [[ "$rc" != "0" ]]; then
    echo ""
    echo "⚠️ 操作未成功（退出码 $rc），脚本文件已保留，可直接重新运行重试"
    return 0
  fi
  # A3-05: 删除目标必须是脚本本体。管道执行（curl … | bash）时
  # $0 是裸命令名 bash/sh，readlink -f 会按当前目录把它解析成
  # $CWD/bash——与脚本无关的同名文件，删之是数据事故、印「已
  # 删除」是假文案（管道形态本无文件可删）。判定两层：① $0 不
  # 含 / 时，只认 CWD 下同名文件且其内容经本体校验（多因子，见
  # flmx_script_file_is_self）才继续（bash install.sh 的相对调用
  # $0 无斜杠但确是本体，不受影响）；② 解析出的目标须真实存在
  # 且同样过本体校验才删。任一层不过 → 保留并点名。
  case "$0" in
    */*) ;;
    *)
      if ! flmx_script_file_is_self "$0"; then
        echo "⚠️ 无法确认脚本本体文件（管道执行或同名异物），脚本文件已保留"
        return 0
      fi
      ;;
  esac
  SCRIPT_PATH="$(readlink -f "$0" 2>/dev/null || realpath "$0" 2>/dev/null || echo "$0")"
  if [[ "$SCRIPT_PATH" == /dev/fd/* ]]; then
    return 0
  fi
  if ! flmx_script_file_is_self "$SCRIPT_PATH"; then
    echo "⚠️ 待删除的脚本文件校验未通过（$SCRIPT_PATH 无法确认为本脚本本体），已保留未删"
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
  local value="$1" out="" ch code
  local i
  # 逐字转义：除 \" \\ 与 \n \r \t \b \f 的具名转义外，其余控制
  # 字符（<0x20）一律 \u00XX——旧实现只处理五种，\b \f 与其他控
  # 制字符原样透传会产出非法 JSON（值域为地址/密钥/代理 URL，
  # 逐字循环的开销可忽略）。多字节字符逐字透传（UTF-8 在 JSON
  # 字符串内合法）。
  for (( i = 0; i < ${#value}; i++ )); do
    ch=${value:i:1}
    case "$ch" in
      '\') out+='\\' ;;
      '"') out+='\"' ;;
      $'\n') out+='\n' ;;
      $'\r') out+='\r' ;;
      $'\t') out+='\t' ;;
      $'\b') out+='\b' ;;
      $'\f') out+='\f' ;;
      *)
        code=$(printf '%d' "'$ch" 2>/dev/null || printf '256')
        if (( code < 32 )); then
          printf -v ch '\\u%04x' "$code"
        fi
        out+="$ch"
        ;;
    esac
  done
  printf '%s' "$out"
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
  # 值形态按 JSON 字符串文法提取（转义对优先于裸字符）：旧实现
  # 的 "[^"]*" 在值含转义引号 \" 时于转义处截断，归属判定会把
  # 自家 agent 误判为他人。
  addr=$(grep -oE '"addr"[[:space:]]*:[[:space:]]*"(\\.|[^"\\])*"' "$config" | head -n 1 | sed -E 's/^"addr"[[:space:]]*:[[:space:]]*"((\\.|[^"\\])*)"$/\1/' || true)
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

# 足迹判定：某目录是否住着一个 agent——连接配置、任一世代的二
# 进制（现行 flmx_agent／存量 flux_agent）或 gost.json（本脚本安
# 装/更新必落的伴生配置，只剩它时同样是自家 agent 的残骸）在位
# 即认。归属守卫、布局迁移与卸载的存量识别共用这一口径，不另
# 造第二套。
agent_dir_has_agent() {
  local dir="$1"
  [[ -e "$dir/config.json" || -e "$dir/flmx_agent" || -e "$dir/flux_agent" || -e "$dir/gost.json" ]]
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

# agent_memory_limits echoes "<MemoryHigh MiB> <MemoryMax MiB>" for
# the agent's systemd unit, computed from this machine's MemTotal via
# the shared memory tier table (P1-06): the soft cap itself is the
# in-process GOMEMLIMIT the agent sets at startup (go-gost/memlimit.go
# memoryTierFor — ≤512MiB machines: 70% of total; larger: half of
# total, capped at 2048MiB); the unit's cgroup rungs step above it
# (High = soft×5/4, Max = soft×3/2, Max never above total memory) so
# the soft cap always binds first and the unit is only the hard
# fallback. Both copies of the table are pinned to identical numbers
# by their tests (memlimit_test.go and test-install-scripts-proxy.sh);
# change them together. When MemTotal cannot be read, fall back to
# the 512MiB tier rather than writing no limits at all.
# AGENT_MEMINFO_PATH exists so the script test suite can feed
# fixtures; production always reads /proc/meminfo.
agent_memory_limits() {
  local meminfo_path="${AGENT_MEMINFO_PATH:-/proc/meminfo}"
  local total_kb total_mib soft high max
  total_kb=$(awk '/^MemTotal:/ {print $2; exit}' "$meminfo_path" 2>/dev/null || true)
  case "$total_kb" in
    ''|*[!0-9]*) total_kb=524288 ;;
  esac
  total_mib=$((total_kb / 1024))
  if [ "$total_mib" -le 512 ]; then
    soft=$((total_mib * 7 / 10))
  else
    soft=$((total_mib / 2))
    if [ "$soft" -gt 2048 ]; then soft=2048; fi
  fi
  high=$((soft * 5 / 4))
  max=$((soft * 3 / 2))
  if [ "$max" -gt "$total_mib" ]; then max=$total_mib; fi
  if [ "$high" -gt "$max" ]; then high=$max; fi
  printf '%s %s\n' "$high" "$max"
}

write_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      local agent_mem_limits agent_mem_high agent_mem_max
      agent_mem_limits=$(agent_memory_limits)
      agent_mem_high=${agent_mem_limits%% *}
      agent_mem_max=${agent_mem_limits##* }
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
# OBS-A5-01：无 RestartSec 时快速失败（配置损坏等）以默认 100ms
# 间隔连撞，1 秒内烧完 systemd 默认启动限速（10 秒内 5 次）进入
# failed，配置修好后仍须人工 reset-failed 才能拉起。3 秒间隔使
# 任意 10 秒窗口内至多 4 次启动、限速不被瞬时烧完，瞬态故障可自
# 愈重试；存量 unit 不随面板内二进制升级刷新，下次脚本安装/更新
# 时随模板自然带入。
RestartSec=3s
# Core dumps of the agent or its helpers must never pile up in the
# data directory (the binary also zeroes RLIMIT_CORE itself at
# startup and sweeps stockpiled core files, see go-gost/coredump.go).
LimitCORE=0
# 调优面③ 写的 /etc/security/limits.d（nofile 1048576）只作用于
# PAM 登录会话、对已注册的 systemd 服务无效；agent 的连接负载同
# 样需要句柄上限，直接写进 unit 本体。limits.d 与 unit 两路并
# 存、互不替代（TuneStatus 的 nofile 概要报进程真实 rlimit）。
LimitNOFILE=1048576
# 内存天花板（P1-06，部署层兜底）：数值由 agent_memory_limits 按
# 本机 MemTotal 从共享档位表算出，与 agent 进程内 GOMEMLIMIT 软帽
# （go-gost/memlimit.go）同表——High＝软帽×5/4、Max＝软帽×3/2，
# 软帽先行收紧 GC，cgroup 两档只作硬兜底。OpenRC 无 cgroup 对应
# 面，只靠进程内软帽。
MemoryHigh=${agent_mem_high}M
MemoryMax=${agent_mem_max}M
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
pidfile="$FLMX_AGENT_OPENRC_PIDFILE"
# R17-C (R17-X-01): same rationale as the systemd unit — /dev/null
# discarded startup logs; keep them in files for post-mortem diagnosis.
output_log="/var/log/flmx_agent.log"
error_log="/var/log/flmx_agent.err.log"

depend() {
  need net
}
EOF
      chmod +x "$FLMX_AGENT_OPENRC_SERVICE_FILE"
      write_flmx_agent_logrotate_config
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

# GARBAGE-AUDIT A2：给 OpenRC 的两份 append 日志落 logrotate 封顶
# 配置（size 1M、保留 3 份；copytruncate 因 OpenRC 长期持有日志
# fd）。写入失败只告警不阻断装机：logrotate 本体缺席的系统上配
# 置躺着无害，装上即生效；无轮转时日志增速极低（KB 级/年）。
write_flmx_agent_logrotate_config() {
  mkdir -p "$(dirname "$FLMX_AGENT_LOGROTATE_FILE")" || {
    echo "⚠️ 创建 logrotate 配置目录失败，OpenRC 日志轮转未配置（不影响安装）" >&2
    return 0
  }
  cat > "$FLMX_AGENT_LOGROTATE_FILE" <<'LOGROTATE_EOF' || {
# flmx_agent OpenRC logs: output_log/error_log are appended by
# OpenRC without any cap; bound them at 1MB x 3 rotations.
/var/log/flmx_agent.log /var/log/flmx_agent.err.log {
    size 1M
    rotate 3
    missingok
    notifempty
    copytruncate
}
LOGROTATE_EOF
    echo "⚠️ 写入 logrotate 配置失败，OpenRC 日志轮转未配置（不影响安装）" >&2
  }
  return 0
}

remove_flmx_agent_service() {
  ensure_service_manager || return 1

  case "$SERVICE_MANAGER" in
    systemd)
      rm -f "$FLMX_AGENT_SYSTEMD_SERVICE_FILE"
      # drop-in 目录（内核目录覆盖等运行时下发项）随 unit 本体一
      # 并删除：旧实现只删 unit 文件，.d 残留会在同名 unit 日后
      # 被重建时复活其中的覆盖项。
      rm -rf "${FLMX_AGENT_SYSTEMD_SERVICE_FILE}.d"
      systemctl daemon-reload 2>/dev/null || true
      ;;
    openrc)
      rm -f "$FLMX_AGENT_OPENRC_SERVICE_FILE"
      rm -f "$FLMX_AGENT_LOGROTATE_FILE"
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
      rm -rf "${LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE}.d"
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
# 保证回滚拉起的旧服务与迁移前等价运行。三处有意例外（现行模
# 板的新增项均不带入——回滚的目标是复原迁移前 unit 的原貌，旧
# unit 本无这些项）：① LimitNOFILE=1048576；② RestartSec=3s
# （OBS-A5-01 的防限速烧穿项）；③ MemoryHigh/MemoryMax 内存天
# 花板（P1-06，按本机内存算出的档位值）。三项以逐字 diff 两模
# 板核对（本轮复核：差异全集即此三处与名/路径/描述/SyslogIden-
# tifier 的世代字面差）。
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
pidfile="$LEGACY_FLUX_AGENT_OPENRC_PIDFILE"
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
      # A3-08: 与存量模板同一变量（见文件头常量区），不再读第三
      # 个从未赋值的名字。
      cat "${LEGACY_FLUX_AGENT_OPENRC_PIDFILE}" 2>/dev/null
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

  # 二进制删除面不得宽于指纹（判定材料须在删服务文件之前采集）：
  # 仅当命中的服务文件确实以该路径为 ExecStart 时才删对应二进
  # 制——旧实现任一分支命中即删 /usr/local/bin/gost，分支 1
  # （ExecStart=/etc/gost/gost）与该路径无关，同机另装的 gost
  # 二进制会被无干系地删掉。在位但未被指纹引用的二进制保留并
  # 点名。
  local legacy_gost_refs=""
  legacy_gost_refs=$(cat "${matched_service_files[@]}" 2>/dev/null || true)

  for service_file in "${matched_service_files[@]}"; do
    if [[ -f "$service_file" ]]; then
      if rm -f "$service_file"; then
        removed_service_file="1"
      else
        echo "⚠️ 删除旧式 gost 服务文件失败: $service_file"
      fi
    fi
  done

  if [[ -f "$LEGACY_GOST_BINARY" ]]; then
    if grep -Fq "ExecStart=$LEGACY_GOST_BINARY" <<< "$legacy_gost_refs"; then
      rm -f "$LEGACY_GOST_BINARY" || echo "⚠️ 删除旧式 gost 二进制失败: $LEGACY_GOST_BINARY"
    else
      echo "ℹ️ $LEGACY_GOST_BINARY 在位但未被旧式 gost 服务文件引用（非本次指纹形态），已保留未删；如确认无用请人工删除"
    fi
  fi
  if [[ -f "$LEGACY_GOST_CONFIG_DIR/gost" ]]; then
    if grep -Fq "ExecStart=$LEGACY_GOST_CONFIG_DIR/gost" <<< "$legacy_gost_refs"; then
      rm -f "$LEGACY_GOST_CONFIG_DIR/gost" || echo "⚠️ 删除旧式 gost 二进制失败: $LEGACY_GOST_CONFIG_DIR/gost"
    else
      echo "ℹ️ $LEGACY_GOST_CONFIG_DIR/gost 在位但未被旧式 gost 服务文件引用（非本次指纹形态），已保留未删；如确认无用请人工删除"
    fi
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
      return 1
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

# A3-07: 节点密钥的非 argv 入口。-s 让密钥在整个安装期间驻留进
# 程 argv（版本解析+下载+判活可达数分钟，ps 与 /proc/<pid>/cmdline
# 对本机所有本地用户可读），与交互路径 read -s 的防窥口径分家。
# 自动化/面板一键场景应改用 FLMX_NODE_SECRET 环境变量传入（进
# 程环境仅属主与 root 可读）；-s 保留为兼容回退且优先。env 值
# 读入后立即 unset，不随后续子进程（curl 等）的环境扩散。
if [[ -z "${SECRET:-}" && -n "${FLMX_NODE_SECRET:-}" ]]; then
  SECRET="$FLMX_NODE_SECRET"
fi
unset FLMX_NODE_SECRET

# A3-04: install 覆盖存量后失败的回滚体——与 update 的
# rollback_flmx_agent 同形态（恢复 .bak、重启、双采样判活），
# 差别只在 install 路径替换前执行过 disable，回滚须连自启一并
# 恢复（enable_and_start_flmx_agent_service）；服务文件此时可能
# 已按新版重写，但模板同内容、不影响旧二进制运行。
rollback_install_replaced_agent() {
  # 配置与二进制同为回滚面：config.json.bak 在位时一并还原（覆
  # 盖安装整覆重写了 config.json，只还原二进制时旧二进制要配
  # 新/坏配置，回滚不完整）。
  if [[ -f "$INSTALL_DIR/config.json.bak" ]]; then
    if mv -f "$INSTALL_DIR/config.json.bak" "$INSTALL_DIR/config.json"; then
      echo "↩️ 已还原安装前的 config.json"
    else
      echo "⚠️ 还原 config.json 失败，请人工检查：$INSTALL_DIR/config.json.bak"
    fi
  fi
  if [[ -f "$INSTALL_DIR/flmx_agent.bak" ]]; then
    mv -f "$INSTALL_DIR/flmx_agent.bak" "$INSTALL_DIR/flmx_agent"
    chmod +x "$INSTALL_DIR/flmx_agent"
    enable_and_start_flmx_agent_service
    # 与更新/回滚路径同口径：先沉降 3 秒让 flap 越过存活窗口，
    # 再走 flmx_agent_health_ok 双采样判活。
    sleep 3
    if flmx_agent_health_ok; then
      echo "✅ 已自动回滚到安装前的版本并重新启动。"
    else
      echo "❌ 回滚后服务仍未启动，请手动检查：$INSTALL_DIR"
    fi
  else
    echo "⚠️ 无备份可回滚，请手动检查：$INSTALL_DIR"
  fi
}

# 安装功能
install_flmx_agent() {
  echo "🚀 开始安装 flmx_agent..."

  # H1-05: 小内存机 swap 防线在装机最前面跑（下载/落盘之前，低内
  # 存机先有缓冲再承压）。失败只告警续装：本步是风险防线不是前置
  # 条件，续装与否不改变 OOM 风险的存在——函数内已点名「swap 未
  # 建、OOM 风险在」并给出人工建法与自检法。
  ensure_small_host_swap || echo "⚠️ swap 装机步未完成（见上 ❌ 行），装机继续；该机 OOM 风险未解除，请按上方人工建法处理后以 free -h; swapon --show 自检"

  # 失败一律 return 而非 exit：main 以 rc 分流 delete_self（失败
  # 保留脚本并点名），函数内 exit 会绕过该分流、用户得不到「脚本
  # 已保留可重试」的回执（下同）。
  ensure_download_url_initialized || return 1

  get_config_params || return 1

  # 共存守卫（C2-02）：在下载、停服、写文件之前先判定既有 agent 的
  # 归属；不属于本面板时终止，不留任何改动。
  guard_agent_dir_ownership || return 1

  # 存量布局迁移：新目录无 agent、存量目录有 → 先对存量目录同口
  # 径过一遍归属守卫，再整体迁到新布局，之后按新装流程继续（配
  # 置以本次 -a/-s 参数重写、二进制换新版）。守卫不过则迁移不发
  # 生、存量原样不动。
  if ! agent_dir_has_agent "$INSTALL_DIR" && agent_dir_has_agent "$LEGACY_INSTALL_DIR"; then
    guard_agent_dir_ownership "$LEGACY_INSTALL_DIR" || return 1
    migrate_legacy_agent_install || return 1
  fi

  # 检查并安装 tcpkill
  check_and_install_tcpkill
  ensure_service_manager || return 1

  mkdir -p "$INSTALL_DIR"

  local tmp_binary="$INSTALL_DIR/flmx_agent.new"

  # 下载 flmx_agent（内置路线逐个尝试、首成即用；同源校验在
  # download_agent_binary 内完成，失败自动切下一条路线）
  echo "⬇️ 下载 flmx_agent 中..."
  download_agent_binary "$tmp_binary" || { rm -f "$tmp_binary"; echo "❌ 下载失败，请检查网络或下载链接。"; return 1; }
  # legacy gost 清理不在此处：它含不可逆删除，须等防降级门的全
  # 部「未被改动」回执点与二进制/配置备份（回滚源）都过去后才执
  # 行（见下方落盘前调用点）——旧位置在门与备份之前，后续失败
  # 时「现装版本未被改动」回执与已删的 legacy gost 不符。

  # A3-04 ①: 覆盖存量 agent 时先过防降级门（与 update 的 X-P2-12
  # 同口径）。此刻下载已完成、服务未停、现装二进制未动，拒绝不
  # 留任何改动。update 侧对降级是无条件拒绝；install 是一键重装
  # 的主用路径，只保留一个逃生口：交互终端点名确认（非交互直
  # 接拒绝）。全新安装（无存量二进制）不进此门。
  local replacing_existing=""
  if [[ -x "$INSTALL_DIR/flmx_agent" ]]; then
    replacing_existing="1"
    local current_version cmp_result
    current_version=$(flmx_agent_installed_version || true)
    cmp_result=$(panel_version_gate_cmp "${RESOLVED_VERSION:-}" "$current_version" || true)
    if [[ -z "$cmp_result" && -n "$current_version" && -n "${RESOLVED_VERSION:-}" ]]; then
      echo "❌ 目标版本 ${RESOLVED_VERSION:-} 与现装版本 $current_version 至少一侧格式不可比（版本段须为纯数字，带后缀的版本如 2.1.1-rc1 不可作安装目标），拒绝安装以防静默降级。现装版本未被改动。"
      rm -f "$tmp_binary"
      return 1
    fi
    if [[ "$cmp_result" == "-1" ]]; then
      local confirm_downgrade=""
      if [[ -t 0 ]]; then
        read -p "⚠️ 目标版本 ${RESOLVED_VERSION:-} 低于现装版本 $current_version，确认要降级安装吗？(y/N): " confirm_downgrade || confirm_downgrade=""
      fi
      if [[ "$confirm_downgrade" != "y" && "$confirm_downgrade" != "Y" ]]; then
        echo "❌ 拒绝降级安装：目标版本 ${RESOLVED_VERSION:-} 低于现装版本 $current_version，现装版本未被改动（A3-04）。如确需降级，请在交互终端重跑并点名确认，或先卸载后重装。"
        rm -f "$tmp_binary"
        return 1
      fi
      echo "⚠️ 已按交互确认继续降级安装（$current_version → ${RESOLVED_VERSION:-}）"
    fi
  fi

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

  # A3-04 ②: 覆盖前备份现装二进制（与 update 的 R9-02 同口径：
  # 备份失败即中止——无回滚源时绝不替换）。此点服务已停，备份
  # 失败须把服务恢复到安装前状态（重新启用并启动）。
  if [[ -n "$replacing_existing" ]]; then
    echo "💾 备份当前版本..."
    if ! cp -f "$INSTALL_DIR/flmx_agent" "$INSTALL_DIR/flmx_agent.bak"; then
      echo "❌ 备份当前版本失败（磁盘空间不足或拷贝出错），已中止安装：现装版本未被替换。"
      rm -f "$INSTALL_DIR/flmx_agent.bak" "$tmp_binary"
      if flmx_agent_service_exists; then
        echo "🔄 正在恢复安装前已停止的服务..."
        enable_and_start_flmx_agent_service || true
      fi
      return 1
    fi
    # 配置同为回滚面：覆盖安装会整覆重写 config.json，只备份二
    # 进制时新配置写坏/写一半无回滚源（回滚后旧二进制配坏配置
    # 同样起不来）。config.json 在位时同步备份，失败同口径中止。
    if [[ -f "$INSTALL_DIR/config.json" ]] && ! cp -f "$INSTALL_DIR/config.json" "$INSTALL_DIR/config.json.bak"; then
      echo "❌ 备份当前配置失败（磁盘空间不足或拷贝出错），已中止安装：现装版本与配置均未被替换。"
      rm -f "$INSTALL_DIR/flmx_agent.bak" "$INSTALL_DIR/config.json.bak" "$tmp_binary"
      if flmx_agent_service_exists; then
        echo "🔄 正在恢复安装前已停止的服务..."
        enable_and_start_flmx_agent_service || true
      fi
      return 1
    fi
  fi

  # legacy gost 清理的执行点：防降级门已过、二进制与配置备份
  # （回滚源）已建立，此后失败回执不再声称「未被改动」且回滚源
  # 在手。清理本身仍在新二进制落盘与新服务启动之前，端口与服
  # 务名冲突面不变。
  cleanup_legacy_gost_installation

  if ! mv "$tmp_binary" "$INSTALL_DIR/flmx_agent" || ! chmod +x "$INSTALL_DIR/flmx_agent"; then
    echo "❌ 新版本二进制落盘失败（mv/chmod 出错），已中止安装。"
    rm -f "$tmp_binary"
    if [[ -n "$replacing_existing" ]]; then
      rollback_install_replaced_agent
    fi
    return 1
  fi

  # A3-04 ③: 覆盖后先烟雾测试（与 update 同口径：-V 必须可执行），
  # 失败立即回滚——不等服务文件与配置按新版重写后才发现二进制
  # 根本跑不起来。
  if [[ -n "$replacing_existing" ]] && ! "$INSTALL_DIR/flmx_agent" -V >/dev/null 2>&1; then
    echo "❌ 新版本二进制无法执行，正在回滚到安装前的版本..."
    rollback_install_replaced_agent
    return 1
  fi
  echo "✅ 下载完成"

  # 打印版本
  echo "🔎 flmx_agent 版本：$($INSTALL_DIR/flmx_agent -V)"

  # 写入 config.json (安装时总是创建新的)
  CONFIG_FILE="$INSTALL_DIR/config.json"
  echo "📄 创建新配置: config.json"
  if ! write_flmx_agent_config "$CONFIG_FILE"; then
    echo "❌ 写入 config.json 失败，已中止安装。"
    if [[ -n "$replacing_existing" ]]; then
      rollback_install_replaced_agent
    fi
    return 1
  fi

  # 写入 gost.json
  GOST_CONFIG="$INSTALL_DIR/gost.json"
  if [[ -f "$GOST_CONFIG" ]]; then
    echo "⏭️ 跳过配置文件: gost.json (已存在)"
  else
    echo "📄 创建新配置: gost.json"
    if ! cat > "$GOST_CONFIG" <<EOF
{}
EOF
    then
      echo "❌ 写入 gost.json 失败，已中止安装。"
      if [[ -n "$replacing_existing" ]]; then
        rollback_install_replaced_agent
      fi
      return 1
    fi
  fi

  # 加强权限
  chmod 600 "$INSTALL_DIR"/*.json

  # 创建 systemd 或 OpenRC 服务
  if ! write_flmx_agent_service; then
    echo "❌ 写入服务文件失败，已中止安装。"
    if [[ -n "$replacing_existing" ]]; then
      rollback_install_replaced_agent
    fi
    return 1
  fi

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
    # A3-04: 覆盖安装成功后清备份（与 update 同口径）。
    if [[ -n "$replacing_existing" ]]; then
      rm -f "$INSTALL_DIR/flmx_agent.bak" || echo "⚠️ 清理二进制备份失败：$INSTALL_DIR/flmx_agent.bak 已保留，可人工删除"
      rm -f "$INSTALL_DIR/config.json.bak" || echo "⚠️ 清理配置备份失败：$INSTALL_DIR/config.json.bak 已保留，可人工删除"
    fi
    return 0
  else
    # A3-04 ④: 覆盖存量的判活失败自动回滚（与 update 同口径）；
    # 全新安装无回滚源，维持原提示路径。
    if [[ -n "$replacing_existing" && -f "$INSTALL_DIR/flmx_agent.bak" ]]; then
      echo "❌ 新版本服务启动失败，正在回滚到安装前的版本..."
      rollback_install_replaced_agent
    else
      echo "❌ flmx_agent服务启动失败，请执行以下命令查看状态："
      flmx_agent_service_status_hint
    fi
    return 1
  fi
}

# X-P2-12 (J-02): 与 panel_install.sh 同实现的点分数字版本比较。
# 回显 -1/0/1 表示 a 小于/等于/大于 b；任一侧为空或含非数字段时
# 不可比，回显空并返回 1。防降级门不得直接据「不可比」放行（带
# 粘连后缀的低版本如 2.1.1-rc1 即由此绕过），一律经
# panel_version_gate_cmp 判定（见下）。
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

# 防降级门判定（两脚本同实现，与 Go 面 compareVersions 对齐，
# A11-132-06）：在 panel_version_cmp 之外补 "dev" 定序——dev 为
# 最低版本（两侧同为 dev 判等、目标为 dev 判低、现装为 dev 判
# 高）；其余情形回显 panel_version_cmp 的结果，不可比时回显空。
# 调用方口径：两侧皆非空而回显空＝不可比，必须 fail-closed 拒
# 绝（旧 V-19 放行口径使带后缀的低版本可绕过防降级、与 Go 面
# 的严格口径分家，已废止）；现装版本为空（-V 不可读、无从判
# 定）时维持放行。
panel_version_gate_cmp() {
  local target="$1" current="$2"
  if [[ "$target" == "dev" && "$current" == "dev" ]]; then
    echo 0
    return 0
  fi
  if [[ "$target" == "dev" && -n "$current" ]]; then
    echo -1
    return 0
  fi
  if [[ "$current" == "dev" && -n "$target" ]]; then
    echo 1
    return 0
  fi
  panel_version_cmp "$target" "$current"
}

# X-P2-12 (J-02): 按 extractLeadingSemver（system_upgrade.go）同规则
# 从版本串中取语义版本：逐个空白分隔段做严格点分数字校验，首个
# 合格段即为版本。真实 -V 输出形如 "gost 1.3.4 (go1.25
# linux/amd64)"（首段是程序名 gost），握手上报的 fullVersion 形如
# "1.3.4 (debian/amd64)"，两种形态都由此归一；粘连后缀
# （如 1.3.2-beta1）、无数字段、空串一律提取失败，调用方得到空
# 版本（无从判定降级，防降级门维持放行；目标侧不可比则由
# panel_version_gate_cmp 口径拒绝）。"dev" 原样透传（防降级门
# 按 Go 规则为其定序，见 panel_version_gate_cmp）。
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

  # H1-05: 更新路径同样先过小内存机 swap 防线：存量小内存机可能
  # 只走更新路径、从未经过新装，swap 供给缺口只有这里能补上（全
  # 集口径见 ensure_small_host_swap 头注）。失败只告警续行，更新
  # 主流程不因本步半途死。
  ensure_small_host_swap || echo "⚠️ swap 检查步未完成（见上 ❌ 行），更新继续；该机 OOM 风险未解除，请按上方人工建法处理后以 free -h; swapon --show 自检"

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
  # panel_install 脚本更新）均已有此门，脚本路径此前独缺。判定
  # 经 panel_version_gate_cmp：两侧皆非空而不可比（带粘连后缀的
  # 低版本如 2.1.1-rc1）fail-closed 拒绝，与 Go 面严格口径对
  # 齐；现装版本不可读（为空）时无从判定、维持放行。
  local current_version cmp_result
  current_version=$(flmx_agent_installed_version || true)
  cmp_result=$(panel_version_gate_cmp "${RESOLVED_VERSION:-}" "$current_version" || true)
  if [[ -z "$cmp_result" && -n "$current_version" && -n "${RESOLVED_VERSION:-}" ]]; then
    echo "❌ 目标版本 ${RESOLVED_VERSION:-} 与当前版本 $current_version 至少一侧格式不可比（版本段须为纯数字，带后缀的版本如 2.1.1-rc1 不可作更新目标），拒绝更新以防静默降级。如确需更新请手动处理。"
    return 1
  fi
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
  # legacy gost 清理不在此处：含不可逆删除，须等下方备份（回滚
  # 源）建立后再执行，与 install 流同口径（见落盘前调用点）。

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

  # legacy gost 清理的执行点：备份（回滚源）已建立、落盘替换尚
  # 未发生，与 install 流同口径。
  cleanup_legacy_gost_installation

  # 替换文件（落盘 mv/chmod 必须守卫：失败时旧实现照常往下走、
  # 烟雾测试测的是未被替换的旧二进制、最终照印「✅ 更新完成」）
  if ! mv "$INSTALL_DIR/flmx_agent.new" "$INSTALL_DIR/flmx_agent" || ! chmod +x "$INSTALL_DIR/flmx_agent"; then
    echo "❌ 新版本二进制落盘失败（mv/chmod 出错），已中止更新。"
    rm -f "$INSTALL_DIR/flmx_agent.new"
    if [[ -f "$INSTALL_DIR/flmx_agent.bak" ]]; then
      rollback_flmx_agent
    elif flmx_agent_service_exists; then
      echo "🔄 正在重新启动更新前已停止的服务..."
      start_flmx_agent_service || true
    fi
    return 1
  fi

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

# 写单个 sysctl 运行时值：只写 $FLMX_PROC_SYS_DIR 下已存在的叶
# 子（本脚本恢复路径的统一纪律），写失败忽略（与 agent 侧
# tuneWrite 的 `_ =` 同口径）。
flmx_agent_uninstall_write_sysctl_value() {
  local target="$FLMX_PROC_SYS_DIR/${1//.//}"
  if [[ -f "$target" ]]; then
    { printf '%s\n' "$2" > "$target"; } 2>/dev/null || true
  fi
}

# 三项标题默认值回退，与 agent 侧 restoreKernelSysctls 的
# writeDefaults 逐字对齐（无快照/快照损坏时用）。
flmx_agent_uninstall_write_tuning_defaults() {
  flmx_agent_uninstall_write_sysctl_value "net.ipv4.tcp_congestion_control" "cubic"
  flmx_agent_uninstall_write_sysctl_value "net.core.default_qdisc" "pfifo_fast"
  flmx_agent_uninstall_write_sysctl_value "net.core.rps_sock_flow_entries" "0"
  # 假定值披露：此三项是「快照缺失/损坏时」的假定回退值、不是读
  # 回的调优前原值——调优前若为 bbr/fq 等非缺省值，无快照即无从
  # 恢复，两调用点统一在此点名，不再静默以假定值冒充已恢复。
  echo "ℹ️ 其中三项 sysctl 已按假定默认值回退（net.ipv4.tcp_congestion_control=cubic、net.core.default_qdisc=pfifo_fast、net.core.rps_sock_flow_entries=0）；若调优前原值不同（如 bbr/fq），因快照缺失/损坏无法自动恢复，请人工核对"
}

# R6-02：脚本卸载与面板自卸载路径（go-gost/x/socket/uninstall.go
# 的 runAgentUninstall）对齐的宿主清理。面板路径在删安装目录之前
# 依次：回退调优（restoreTuningForUninstall 走第二代 rollback：
# 删四面与第一代全部足迹文件、gai.conf 按备份还原、删 MSS 钳制
# 规则、内存值复位，再由 cleanupHostTuningFiles 兜底删漏）→ 停
# 内核并删 agent 自建 nft 三表 → 才停服务删目录。本函数第 3)～
# 7) 步与 cleanupHostTuningFiles 的对应清理面逐项对齐（精确边
# 界见下方「对齐范围」段）；第 1) 步与第 2) 步的一代部分是第
# 一代兼容恢复（整形前队列回放、tuning-backup.json 逐键写回
# sysctl 原值）——该部分第二代面板路径已不再执行，但备份与整
# 形脚本都在即将删除的安装目录里，脚本卸载是它们最后的恢复
# 机会，故保留（产物不存在时两步都是空转）；第 2) 步的二代部
# 分（面③ TL-02 快照逐键写回原值后消费删除、无快照时回退标
# 题默认值并点名其余不可恢复键）与面板路径
# restoreKernelSysctls 同口径（J9-01）。旧脚本卸载只有停服
# 删目录，残留后果：调优 sysctl conf 与 qdisc unit 开机自动重放、
# nft 白名单 DROP 规则比所有者长寿（R2-001 危害形态）、
# tuning-backup.json 随目录删除后原值永久不可恢复、同机重装还会
# 把已调优值当作原值再备份。本函数在服务已停、安装目录未删时执
# 行同一清理全集。口径与面板路径一致：逐项尽力而为，单项失败只
# 告警并逐项列明、不中断后续项；全部以「存在才处理」为前提，重
# 复执行只剩跳过、无副作用（幂等）。
# 对齐范围的精确边界（防「逐项对齐」宣称漂移）：第 3) 步的 unit
# 集、第 4) 步的 MSS 规则、第 5)～7) 步的文件集与
# cleanupHostTuningFiles 的对应面逐项一致；第 8) 步 nft 三表对
# 应 agent 侧 kernel.Uninstall 的 removeTables（同一全集，
# flvx_kernel 的足迹守卫差异见下段）；面③ sysctl 快照的处置形态是唯一例外（agent 侧
# tuneKernelSnapshotPath，/etc/security/flmx-tuning-kernel-sysctls.json）
# ——脚本侧由第 2) 步二代部分恢复式处置（逐键写回原值后消费
# 删除，J9-01／R2-SCR-F02），不裸删，避免重演「原值永久不可
# 恢复」；agent 侧则由调优回退步消费后兜底删除，两路终态一致。
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

  # 2) 之二代部分：面③ TL-02 快照恢复（J9-01，与面板路径
  # restoreKernelSysctls 同口径）。快照是 agent 首次 apply-kernel
  # 前留的 {values:{键:原值}, absent:[调优前不存在的键]} JSON，
  # 写在宿主机 /etc/security、不在安装目录里——旧脚本卸载从不处
  # 理它：运行时值不恢复、文件成孤儿残留，同机重装时 capture
  # 「首份胜出」还会让陈旧快照继续压住运营者的现值。处置分三
  # 路（与 agent 侧逐项对应）：
  #   快照可解析：逐键写回原值（同第 2) 步的键字符集与「只写
  #   已存在叶子」纪律）；absent 键无法回退删除、逐项点名；
  #   末尾把 rps_sock_flow_entries 复位为 0（agent 侧恢复路径
  #   同样无条件做）；快照文件随后消费删除（agent 侧 defer 删
  #   除，任何分支都不留）。
  #   快照损坏（解析不出键值对且无 absent 清单）：按 agent 侧
  #   同向处理——回退三项标题默认值并点名其余不可恢复键，快
  #   照同样删除。
  #   无快照但面③ 内核 conf 在位（旧 agent 部署、无 TL-02）：
  #   回退三项标题默认值并点名其余不可恢复键（保留至重启）。
  # 三项标题默认值与 agent 侧 writeDefaults 逐字对齐：
  # tcp_congestion_control=cubic、default_qdisc=pfifo_fast、
  # rps_sock_flow_entries=0（写失败按 agent 侧同为忽略）。
  local snap_pairs=0 snap_restored=0 snap_unsupported=0
  local snap_absent=""
  if [[ -f "$FLMX_TUNING_KERNEL_SNAPSHOT" ]]; then
    snap_absent=$(sed -n 's/.*"absent"[[:space:]]*:[[:space:]]*\[\(.*\)\].*/\1/p' "$FLMX_TUNING_KERNEL_SNAPSHOT" 2>/dev/null | grep -oE '"[A-Za-z0-9_.]+"' | tr -d '"' | paste -sd, -) || snap_absent=""
    local skey="" svalue="" starget=""
    while IFS=$'\t' read -r skey svalue; do
      [[ -n "$skey" ]] || continue
      snap_pairs=$((snap_pairs + 1))
      starget="$FLMX_PROC_SYS_DIR/${skey//.//}"
      if [[ ! -f "$starget" ]]; then
        snap_unsupported=$((snap_unsupported + 1))
        continue
      fi
      if { printf '%s\n' "$svalue" > "$starget"; } 2>/dev/null; then
        snap_restored=$((snap_restored + 1))
      else
        failures=$((failures + 1))
        echo "⚠️ sysctl 参数按快照恢复失败: $skey"
      fi
    done < <(grep -oE '"[A-Za-z0-9_.]+": *"[^"]*"' "$FLMX_TUNING_KERNEL_SNAPSHOT" 2>/dev/null | sed -e 's/^"//' -e 's/"$//' -e 's/": *"/\t/')
    if [[ "$snap_pairs" -eq 0 && -z "$snap_absent" ]]; then
      failures=$((failures + 1))
      echo "⚠️ 调优前快照损坏、无法解析，以下 sysctl 键未能回退（仍为调优值）: ${FLMX_TUNING_KERNEL_UNRESTORED_KEYS// /, }"
      flmx_agent_uninstall_write_tuning_defaults
    else
      if [[ "$snap_restored" -gt 0 || "$snap_unsupported" -gt 0 ]]; then
        echo "🧹 已按调优前快照恢复 sysctl 原值（${snap_restored} 项生效，${snap_unsupported} 项本机内核不支持已跳过）"
      fi
      if [[ -n "$snap_absent" ]]; then
        echo "⚠️ 以下 sysctl 键在调优前不存在、无法回退删除: $snap_absent"
      fi
      flmx_agent_uninstall_write_sysctl_value "net.core.rps_sock_flow_entries" "0"
    fi
    if rm -f "$FLMX_TUNING_KERNEL_SNAPSHOT"; then
      echo "🧹 删除调优宿主文件: $FLMX_TUNING_KERNEL_SNAPSHOT"
    else
      failures=$((failures + 1))
      echo "⚠️ 删除调优宿主文件失败: $FLMX_TUNING_KERNEL_SNAPSHOT"
    fi
  elif [[ -e "$FLMX_TUNING_KERNEL_CONF" ]]; then
    flmx_agent_uninstall_write_tuning_defaults
    echo "⚠️ 调优前快照缺失，以下 sysctl 键未能回退（仍为调优值、保留至重启）: ${FLMX_TUNING_KERNEL_UNRESTORED_KEYS// /, }"
  fi

  # 3) systemd unit（第二代 RPS、第一代 qdisc 与定时重启 timer
  # 对四件）：先 disable --now 再删文件、都处理完再统一
  # daemon-reload 一次（与 agent 侧 cleanupHostTuningFiles 的
  # unit 段同集同序），否则悬空启用项在下次开机仍会去执行已被
  # 删除的脚本（RPS unit 执行 /usr/local/bin 下的助手脚本、
  # qdisc unit 执行安装目录内的整形脚本）；定时重启 timer 对由
  # agent 运行时按面板设置生成（R2-SCR-F05：脚本侧此前漏收，
  # timer 带 Persistent=true、enabled 状态下卸载后仍会每日触
  # 发已删的 agent 服务）。OpenRC 形态没有这些 unit，文件不存
  # 在即跳过。
  local unit_removed=0
  local unit_pair="" unit_file="" unit_name=""
  for unit_pair in "$FLMX_TUNING_RPS_UNIT_FILE|$FLMX_TUNING_RPS_UNIT" "$FLMX_TUNING_QDISC_UNIT_FILE|$FLMX_TUNING_QDISC_UNIT" "$FLMX_AGENT_RESTART_TIMER_FILE|$FLMX_AGENT_RESTART_TIMER_UNIT" "$FLMX_AGENT_RESTART_SERVICE_FILE|$FLMX_AGENT_RESTART_SERVICE_UNIT"; do
    unit_file="${unit_pair%%|*}"
    unit_name="${unit_pair##*|}"
    if [[ -e "$unit_file" ]]; then
      if [[ "${SERVICE_MANAGER:-}" == "systemd" ]] && command -v systemctl >/dev/null 2>&1; then
        systemctl disable --now "$unit_name" 2>/dev/null || true
      fi
      if rm -f "$unit_file"; then
        echo "🧹 删除 systemd 单元: $unit_file"
        unit_removed=1
      else
        failures=$((failures + 1))
        echo "⚠️ 删除 systemd 单元失败: $unit_file"
      fi
    fi
  done
  if [[ "$unit_removed" -eq 1 && "${SERVICE_MANAGER:-}" == "systemd" ]] && command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload 2>/dev/null || true
  fi

  # 4) MSS 钳制规则（第二代面③ 部署）：不是文件、单独删除。先
  # -C 探在位才 -D 并回执；iptables 不在或规则不在都无动作，与
  # agent 侧 tuneMSSClampRemove 同为尽力而为——删除失败只点名、
  # 不计 failures（规则会残留至宿主机重启或人工清理）。
  if command -v iptables >/dev/null 2>&1; then
    if iptables -t mangle -C POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null; then
      if iptables -t mangle -D POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null; then
        echo "🧹 已删除 MSS 钳制规则（iptables mangle POSTROUTING TCPMSS）"
      else
        echo "⚠️ MSS 钳制规则删除失败，请人工执行: iptables -t mangle -D POSTROUTING -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu"
      fi
    fi
  fi

  # 5) gai.conf 兜底还原（第二代面①）：备份还在且 IPv4 优先行仍
  # 在位时，先按备份逐字还原 gai.conf 本体、再删备份——否则卸载
  # 后 IPv4 优先残留且原文永久丢失。备份无论还原与否都在本步删
  # 除（与 agent 侧 cleanupHostTuningFiles 同口径：备份是调优产
  # 物、不留给系统）。gai.conf 本体是系统文件，只还原不删除。
  if [[ -e "$FLMX_TUNING_GAI_BACKUP" ]]; then
    if [[ -f "$FLMX_TUNING_GAI_CONF" ]] && grep -qE "^${FLMX_TUNING_GAI_PRECEDENCE_LINE}[[:space:]]*$" "$FLMX_TUNING_GAI_CONF"; then
      if cp -f "$FLMX_TUNING_GAI_BACKUP" "$FLMX_TUNING_GAI_CONF" 2>/dev/null; then
        echo "🧹 已按调优备份还原 gai.conf: $FLMX_TUNING_GAI_CONF"
      else
        failures=$((failures + 1))
        echo "⚠️ 按调优备份还原 gai.conf 失败: $FLMX_TUNING_GAI_CONF"
      fi
    fi
    if rm -f "$FLMX_TUNING_GAI_BACKUP"; then
      echo "🧹 删除调优宿主文件: $FLMX_TUNING_GAI_BACKUP"
    else
      failures=$((failures + 1))
      echo "⚠️ 删除调优宿主文件失败: $FLMX_TUNING_GAI_BACKUP"
    fi
  fi

  # 6) 宿主调优文件：第二代三件 conf（内核调优、limits、BBR）与
  # RPS 助手脚本、第一代两件（sysctl conf、BBR 开机加载 conf）。
  # 删掉后调优值不再开机重放；运行时值已在第 2 步按一代备份与
  # 二代 TL-02 快照尽力恢复（快照文件已在第 2 步消费删除，不在
  # 本步清单内）；无快照的二代存量已按面板路径同口径回退标题
  # 默认值并点名其余键，未恢复的运行时值保留至重启。
  local host_file=""
  for host_file in "$FLMX_TUNING_KERNEL_CONF" "$FLMX_TUNING_LIMITS_CONF" "$FLMX_TUNING_BBR_CONF" "$FLMX_TUNING_RPS_SCRIPT" "$FLMX_TUNING_SYSCTL_CONF" "$FLMX_TUNING_BBR_MODULE_FILE"; do
    if [[ -e "$host_file" ]]; then
      if rm -f "$host_file"; then
        echo "🧹 删除调优宿主文件: $host_file"
      else
        failures=$((failures + 1))
        echo "⚠️ 删除调优宿主文件失败: $host_file"
      fi
    fi
  done

  # 7) 改名前 legacy 名的 sysctl conf（99-flvx-tuning.conf）：与
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

  # 8) agent 自建 nft 三表：沿用 agent 侧 removeTables 的「先建空
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
# 2.0.0 一并退场，本函数只认新名、同名以最后赋值为准）。来源只限本服务自己的
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

# OpenRC 日志面清理（R2-P2-04）：logrotate 配置与两代日志本体及
# 其轮转件。日志本体由 OpenRC 服务定义的 output_log/error_log
# 长期 append 写入、只有 OpenRC 形态才产生，但删除按「存在才处
# 理」不分当前形态执行——形态判定靠当前 init 系统、与装机时形
# 态未必一致，按在位文件收口才不漏；轮转件为 logrotate 配置的
# rotate 3 产物（.1–.3，含可能的 .gz 形态）。逐项点名、单项失
# 败只告警不中断（与宿主清理同口径），重复执行为空转（幂等）。
# 面板自卸载路径的对应处置在 agent 侧 buildAgentUninstallScript
# 生成的脚本里（同名派生集）。
remove_flmx_agent_log_files() {
  local failures=0
  local target=""
  if [[ -e "$FLMX_AGENT_LOGROTATE_FILE" ]]; then
    if rm -f "$FLMX_AGENT_LOGROTATE_FILE"; then
      echo "🧹 删除日志轮转配置: $FLMX_AGENT_LOGROTATE_FILE"
    else
      failures=$((failures + 1))
      echo "⚠️ 删除日志轮转配置失败: $FLMX_AGENT_LOGROTATE_FILE"
    fi
  fi
  local base="" suffix=""
  for base in "flmx_agent.log" "flmx_agent.err.log" "flux_agent.log" "flux_agent.err.log"; do
    for suffix in "" ".1" ".2" ".3" ".gz" ".1.gz" ".2.gz" ".3.gz"; do
      target="$FLMX_AGENT_LOG_DIR/$base$suffix"
      if [[ -e "$target" ]]; then
        if rm -f "$target"; then
          echo "🧹 删除日志文件: $target"
        else
          failures=$((failures + 1))
          echo "⚠️ 删除日志文件失败: $target"
        fi
      fi
    done
  done
  if [[ "$failures" -gt 0 ]]; then
    echo "⚠️ 日志面清理有 ${failures} 项未完成（见上 ⚠️ 行），其余卸载步骤继续执行"
    return 1
  fi
  return 0
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

  local confirm_prompt="确认卸载 flmx_agent 吗？将停止服务，并删除安装目录、agent 服务文件、定时重启单元、日志文件、调优宿主文件与自建 nft 表，同时恢复调优前的系统参数"
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
  # 局逐目录各跑一遍（清理本身幂等，宿主文件/nft 表在第一遍后即
  # 不存在，第二遍为空转核对）。调用不以目录存在为前提：宿主面
  # 产物多在安装目录之外（/etc/security 的 TL-02 快照、sysctl
  # conf、unit、nft 表），安装目录已被先前一次部分卸载删掉时恰
  # 是最需要补跑的形态——旧实现以 -d 门控、该形态整集跳过却仍
  # 报「卸载完成」；函数内各步均以存在才处理，目录缺失时目录内
  # 步骤自然空转。
  uninstall_flmx_agent_host_cleanup "$INSTALL_DIR"
  uninstall_flmx_agent_host_cleanup "$LEGACY_INSTALL_DIR"

  # 删除服务文件
  if [[ -f "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" || -f "$FLMX_AGENT_OPENRC_SERVICE_FILE" || -d "${FLMX_AGENT_SYSTEMD_SERVICE_FILE}.d" ]]; then
    remove_flmx_agent_service
    echo "🧹 删除服务文件"
  fi
  if [[ -f "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" || -f "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE" || -d "${LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE}.d" ]]; then
    remove_legacy_flux_agent_service
    echo "🧹 删除改名前的存量服务文件"
  fi

  # R2-P2-04：OpenRC 日志面（logrotate 配置＋两代日志本体与轮
  # 转件）——在安装目录之外，单独清理（逐项点名见函数内）；其
  # 失败计数并入下方完成判决，不再被恒回 0 吞掉。
  local uninstall_failures=0
  remove_flmx_agent_log_files || uninstall_failures=$((uninstall_failures + 1))

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

  # 完成回执必须有后置实证：逐项核对本函数声称删除的对象确已
  # 不在位，任一残留即点名并以非 0 收尾——旧实现 rm 集裸调用、
  # 函数恒回 0 并无条件印「✅ 卸载完成」，删失败（权限/占用）
  # 时用户无从知晓。
  local leftover="" check_path=""
  for check_path in "$FLMX_AGENT_SYSTEMD_SERVICE_FILE" "${FLMX_AGENT_SYSTEMD_SERVICE_FILE}.d" \
    "$FLMX_AGENT_OPENRC_SERVICE_FILE" \
    "$LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE" "${LEGACY_FLUX_AGENT_SYSTEMD_SERVICE_FILE}.d" \
    "$LEGACY_FLUX_AGENT_OPENRC_SERVICE_FILE" \
    "$INSTALL_DIR" "$LEGACY_INSTALL_DIR"; do
    if [[ -e "$check_path" ]]; then
      leftover="$leftover $check_path"
      uninstall_failures=$((uninstall_failures + 1))
    fi
  done
  if [[ -n "$CUSTOM_KERNEL_DIR" && -e "$CUSTOM_KERNEL_DIR" ]]; then
    leftover="$leftover $CUSTOM_KERNEL_DIR"
    uninstall_failures=$((uninstall_failures + 1))
  fi
  if [[ "$uninstall_failures" -gt 0 ]]; then
    echo "❌ 卸载未完全完成：以下对象未能删除：${leftover:-（见上日志面 ⚠️ 行）}，请人工处理后重跑卸载"
    return 1
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

# A3-06: 进程级互斥锁。install/update/uninstall 共用一组固定路
# 径工件（$INSTALL_DIR/flmx_agent.new、.part、.bak、config.json
# 与服务文件），旧实现全程无锁：两实例并发时互相删/续对方的下
# 载半成品、config 后写覆盖先写、二进制与配置可来自不同进程。
# main 入口非阻塞占锁，已有实例运行时点名退出；锁随进程退出
# 自动释放（flock fd 关闭 / mkdir 锁由 EXIT trap 清），不留陈旧
# 锁。锁路径可用 FLMX_INSTALL_LOCK_FILE 覆盖（测试夹具用），缺
# 省 /run（不可写时退 ${TMPDIR:-/tmp}）。无 flock 的系统（极简
# Alpine 镜像）退化为原子 mkdir 锁：锁目录内记 PID，持有者已
# 死时下任可接管。
INSTALL_LOCK_DIR=""
acquire_install_lock() {
  local lock_file="${FLMX_INSTALL_LOCK_FILE:-}"
  if [[ -z "$lock_file" ]]; then
    if [[ -w /run ]]; then
      lock_file="/run/flmx-install.lock"
    else
      lock_file="${TMPDIR:-/tmp}/flmx-install.lock"
    fi
  fi
  if command -v flock >/dev/null 2>&1; then
    exec 9>>"$lock_file" || return 1
    if ! flock -n 9; then
      echo "❌ 已有另一个 install.sh 实例正在运行（锁: $lock_file），请等它结束后再运行；并发实例会共用下载与配置工件、互相覆盖（A3-06）" >&2
      return 1
    fi
    return 0
  fi
  local lock_dir="${lock_file}.d"
  if mkdir "$lock_dir" 2>/dev/null; then
    echo "$$" > "$lock_dir/pid"
    INSTALL_LOCK_DIR="$lock_dir"
    return 0
  fi
  local holder=""
  holder=$(cat "$lock_dir/pid" 2>/dev/null || true)
  if [[ -n "$holder" ]] && ! kill -0 "$holder" 2>/dev/null; then
    rm -rf "$lock_dir"
    if mkdir "$lock_dir" 2>/dev/null; then
      echo "$$" > "$lock_dir/pid"
      INSTALL_LOCK_DIR="$lock_dir"
      return 0
    fi
  fi
  echo "❌ 已有另一个 install.sh 实例正在运行（锁: $lock_dir），请等它结束后再运行；并发实例会共用下载与配置工件、互相覆盖（A3-06）" >&2
  return 1
}

release_install_lock() {
  if [[ -n "${INSTALL_LOCK_DIR:-}" ]]; then
    rm -rf "$INSTALL_LOCK_DIR"
    INSTALL_LOCK_DIR=""
  fi
}

# 主逻辑
main() {
  # A3-06: 先占锁再做任何落盘/联网动作（占锁本身不写业务工件）。
  acquire_install_lock || exit 1
  if [[ -n "${INSTALL_LOCK_DIR:-}" ]]; then
    trap release_install_lock EXIT
  fi
  require_root_privileges || exit 1

  # X-P3-6 (J-03, BC-12): 各分支捕获操作函数返回码并以其退出。
  # 旧实现无条件 exit 0，菜单更新/卸载失败（函数以 return 1 报告）
  # 也被以退出码判断成败的自动化消费者误判为成功；成功路径仍
  # exit 0。delete_self 接收同一返回码分流（R4B11-005：失败保留
  # 脚本、成功才自删）。install_flmx_agent 的失败路径已统一为
  # return 1（早年有函数内 exit 1 直接退出、绕过本分流且无保留
  # 回执，现已收口）。
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
        # A3-05: 未执行任何操作，不走 delete_self——「退出」不是
        # 「操作已完成」，自删只属于成功的操作路径（R4B11-005）。
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
