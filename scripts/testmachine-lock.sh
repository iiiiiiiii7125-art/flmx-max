#!/usr/bin/env bash
# FLMX 测试机互斥锁（R19-PROC-01 / P-01，R20 起生效）。
#
# 背景：R19 复发——L1 02:56:37Z 建锁文件 15 秒后被 L5 覆盖为
# `L5 02:56:52Z`，L5 随即在窗口 1 内 03:18–03:20 部署并三次重启 agent；
# 根因是锁文件非原子（`echo … > file` 可静默覆盖）+ 无操作前归属复核。
# 本脚本把抢占改为 mkdir 原子化，并提供每次重启/部署前的归属复核。
#
# 锁本体：$HOME/workspace/.testmachine-lock.d/ 目录（mkdir 原子创建），
# 内含 owner 文件一行：`LANE ISO8601-UTC`。锁位于本机共享工作区
# （各 lane 同机协作），非测试机磁盘；R19 旧锁文件
# `~/workspace/.r19-testmachine-lock` 已废弃，不再使用。
#
# 用法：
#   testmachine-lock.sh acquire <LANE> [--wait SECONDS]
#       抢锁。mkdir 成功即持有并写入 owner；失败时打印当前持有者并
#       退出 1（--wait 则每 5 秒轮询直至超时，超时仍退出 1）。
#       绝不覆盖他人锁。
#   testmachine-lock.sh check <LANE>
#       复核归属：锁须存在且 owner 的 lane 名 == 本 lane，否则退出 1。
#       每次重启 / 部署 / agent 操作前必跑；不通过不得操作测试机。
#   testmachine-lock.sh release <LANE>
#       放锁。仅 owner 本人可放；他人持有时拒绝并退出 1。
#   testmachine-lock.sh status
#       打印当前持有者（`LANE TIMESTAMP`），无锁打印 `free`。
#   testmachine-lock.sh break --reason "……"
#       仅协调者可用：移除疑似僵死的锁。须写明原因，原因与原持有者
#       一并打印（应抄入本轮报告）；不得用于抢占在用窗口。
#
# 环境变量：FLMX_TESTMACHINE_LOCK_DIR 覆盖默认锁目录（仅供测试）。
set -euo pipefail

DEFAULT_LOCK_DIR="$HOME/workspace/.testmachine-lock.d"
LOCK_DIR="${FLMX_TESTMACHINE_LOCK_DIR:-$DEFAULT_LOCK_DIR}"
OWNER_FILE="$LOCK_DIR/owner"

die() { printf 'testmachine-lock: %s\n' "$*" >&2; exit 1; }

current_owner() {
	if [[ -f "$OWNER_FILE" ]]; then
		cat "$OWNER_FILE"
	elif [[ -d "$LOCK_DIR" ]]; then
		echo "(unknown: lock dir exists but owner file missing)"
	else
		echo ""
	fi
}

cmd_acquire() {
	local lane="$1"; shift
	local wait_secs=0
	while [[ $# -gt 0 ]]; do
		case "$1" in
			--wait) wait_secs="${2:?--wait needs seconds}"; shift 2 ;;
			*) die "acquire: unknown option $1" ;;
		esac
	done
	[[ "$lane" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || die "acquire: bad lane name '$lane'"
	[[ -d "$HOME/workspace" ]] || die "acquire: shared workspace $HOME/workspace missing; refusing to lock elsewhere"

	local deadline=$(( $(date +%s) + wait_secs ))
	while true; do
		# 父目录可预建（非原子步骤）；原子抢占仍是下面单次 mkdir 锁目录。
		mkdir -p -- "$(dirname -- "$LOCK_DIR")"
		if mkdir "$LOCK_DIR" 2>/dev/null; then
			printf '%s %s\n' "$lane" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$OWNER_FILE"
			printf 'testmachine-lock: acquired by %s at %s\n' "$lane" "$(cut -d' ' -f2- "$OWNER_FILE")"
			return 0
		fi
		local owner
		owner="$(current_owner)"
		if [[ "$(date +%s)" -lt "$deadline" ]]; then
			printf 'testmachine-lock: held by %s, waiting…\n' "$owner" >&2
			sleep 5
			continue
		fi
		printf 'testmachine-lock: DENIED, held by %s; wait, do not overwrite\n' "$owner" >&2
		return 1
	done
}

cmd_check() {
	local lane="$1"
	[[ -d "$LOCK_DIR" ]] || die "check: no lock held (free); $lane must acquire first"
	local owner
	owner="$(current_owner)"
	local owner_lane="${owner%% *}"
	if [[ "$owner_lane" != "$lane" ]]; then
		die "check: lock held by $owner, not $lane; $lane must not operate the test machine"
	fi
	printf 'testmachine-lock: check ok, held by %s\n' "$owner"
}

cmd_release() {
	local lane="$1"
	[[ -d "$LOCK_DIR" ]] || die "release: no lock held"
	local owner
	owner="$(current_owner)"
	local owner_lane="${owner%% *}"
	if [[ "$owner_lane" != "$lane" ]]; then
		die "release: lock held by $owner, $lane cannot release another lane's lock"
	fi
	rm -rf -- "$LOCK_DIR"
	printf 'testmachine-lock: released by %s (was %s)\n' "$lane" "$owner"
}

cmd_status() {
	if [[ -d "$LOCK_DIR" ]]; then
		printf 'held by %s\n' "$(current_owner)"
	else
		printf 'free\n'
	fi
}

cmd_break() {
	local reason=""
	while [[ $# -gt 0 ]]; do
		case "$1" in
			--reason) reason="${2:-}"; shift 2 ;;
			*) die "break: unknown option $1" ;;
		esac
	done
	[[ -n "$reason" ]] || die "break: --reason is mandatory (coordinator only)"
	[[ -d "$LOCK_DIR" ]] || die "break: no lock held"
	local owner
	owner="$(current_owner)"
	rm -rf -- "$LOCK_DIR"
	printf 'testmachine-lock: BROKEN by coordinator (was %s); reason: %s\n' "$owner" "$reason"
}

usage() {
	sed -n '2,30p' "$0" >&2
	exit 2
}

[[ $# -ge 1 ]] || usage
cmd="$1"; shift
case "$cmd" in
	acquire) [[ $# -ge 1 ]] || die "acquire needs <LANE>"; cmd_acquire "$@" ;;
	check)   [[ $# -eq 1 ]] || die "check needs <LANE>";   cmd_check "$1" ;;
	release) [[ $# -eq 1 ]] || die "release needs <LANE>"; cmd_release "$1" ;;
	status)  [[ $# -eq 0 ]] || die "status takes no args";  cmd_status ;;
	break)   cmd_break "$@" ;;
	*) die "unknown command '$cmd' (acquire|check|release|status|break)" ;;
esac
