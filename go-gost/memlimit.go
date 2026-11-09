package main

import (
	"bufio"
	"fmt"
	"os"
	runtimedebug "runtime/debug"
	"strconv"
	"strings"
)

// Runtime memory ceiling for the agent (P1-06). Until now the agent
// had no memory limit of its own: no GOMEMLIMIT/SetMemoryLimit in
// code and no MemoryHigh/MemoryMax in the systemd unit, so when any
// accumulation surface grew (the ECS1686 incident: resident half-open
// sessions), the only rungs above Go's default GC target were the
// kernel OOM killer — which picked the agent itself (365MB of a
// 464MB machine). Two independent layers now close that gap:
//
//   - In-process (primary, this file): at startup the agent sets a
//     GOMEMLIMIT soft cap sized from the machine's total memory via
//     debug.SetMemoryLimit. Approaching the cap tightens GC (more
//     frequent collection, heap held near the cap) instead of an
//     abrupt kill. An explicit GOMEMLIMIT in the environment always
//     wins and is never overridden.
//   - Unit layer (fallback): install.sh writes MemoryHigh/MemoryMax
//     into the systemd unit from the same tier table, so cgroup
//     throttling / a hard stop sits above the soft cap even if the
//     process-level limit is somehow not in effect. The OpenRC unit
//     has no equivalent cgroup knobs and stays uncapped at the unit
//     layer; the in-process cap still applies there.
//
// Note the tiers key off MemTotal — the host-visible memory. In the
// container form a cgroup limit below the tier would bind first;
// that form's entrypoint is out of this fix's scope.

// memTierSmallMaxMiB is the largest machine size that takes the
// small-machine share of the tier table.
const memTierSmallMaxMiB = 512

// memSoftCapMiB is the absolute ceiling of the soft cap on large
// machines: past a few GiB of working set the agent gains nothing
// from a bigger heap allowance, and an unbounded share of a large
// host would defeat the ceiling's purpose.
const memSoftCapMiB = 2048

// memoryTier is one row of the shared tier table: softMiB is the
// in-process GOMEMLIMIT target; highMiB/maxMiB are the systemd
// cgroup rungs install.sh renders from the same table.
type memoryTier struct {
	softMiB int64
	highMiB int64
	maxMiB  int64
}

// memoryTierFor maps a machine's total memory (MiB) to its tier.
//
// Small machines (≤512MiB) are the incident shape: the soft cap is
// 70% of total — high enough for the agent's natural peak (a healthy
// agent idles at tens of MiB; the cap sits ~8x above that), low
// enough to leave the OS, sshd and the supervised kernels the
// remaining ~30% before GC pressure starts. ECS1686's 453MiB maps to
// a 317MiB soft cap: the 365MB the runaway agent reached would have
// hit hard GC pressure well before the OOM killer's threshold.
//
// Larger machines take half of total — proportional headroom for
// busier nodes — capped at memSoftCapMiB so the ceiling stays a
// ceiling. The cgroup rungs step above the soft cap (×5/4, ×3/2) so
// the soft cap always binds first; maxMiB never exceeds the
// machine's total memory.
//
// This table is duplicated in install.sh (agent_memory_limits) for
// the unit layer. Both copies are pinned to identical numbers by
// their tests (memlimit_test.go, test-install-scripts-proxy.sh);
// change them together. A third copy of the soft rung lives in the
// panel backend (go-backend/cmd/paneld/memlimit.go, R2-P1-F12),
// pinned to the same numbers by that package's memlimit_test.go.
func memoryTierFor(totalMiB int64) memoryTier {
	var soft int64
	if totalMiB <= memTierSmallMaxMiB {
		soft = totalMiB * 7 / 10
	} else {
		soft = totalMiB / 2
		if soft > memSoftCapMiB {
			soft = memSoftCapMiB
		}
	}
	high := soft * 5 / 4
	maxB := soft * 3 / 2
	if maxB > totalMiB {
		maxB = totalMiB
	}
	if high > maxB {
		high = maxB
	}
	return memoryTier{softMiB: soft, highMiB: high, maxMiB: maxB}
}

// softLimitBytes decides the in-process soft limit (in bytes) to
// install. ok=false means "leave the runtime exactly as configured":
// either an explicit GOMEMLIMIT is already in the environment (the
// operator's value wins), or the machine size is unknown/degenerate.
func softLimitBytes(getenv func(string) string, totalBytes int64) (limit int64, ok bool) {
	if getenv("GOMEMLIMIT") != "" {
		return 0, false
	}
	if totalBytes <= 0 {
		return 0, false
	}
	return memoryTierFor(totalBytes>>20).softMiB << 20, true
}

// meminfoPath is a var so tests can point the reader at a fixture.
var meminfoPath = "/proc/meminfo"

// readMemTotalBytes returns MemTotal in bytes from /proc/meminfo.
func readMemTotalBytes() (int64, error) {
	f, err := os.Open(meminfoPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse MemTotal: %w", err)
			}
			return kb * 1024, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%s 中找不到 MemTotal", meminfoPath)
}

// applyMemoryLimit installs the in-process soft cap. It runs first
// in main, before any business allocation, and never fails startup:
// any problem (explicit env value, unreadable meminfo) just leaves
// the runtime default in place and says so in the startup log.
func applyMemoryLimit() {
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		fmt.Printf("[flmx] GOMEMLIMIT 已由环境显式设置（%s），进程内内存软帽不覆盖\n", v)
		return
	}
	total, err := readMemTotalBytes()
	if err != nil {
		fmt.Printf("[flmx] 读取机器内存失败，跳过内存软帽: %v\n", err)
		return
	}
	limit, ok := softLimitBytes(os.Getenv, total)
	if !ok {
		return
	}
	runtimedebug.SetMemoryLimit(limit)
	fmt.Printf("[flmx] 内存软帽已设: GOMEMLIMIT=%dMiB（机器总内存 %dMiB）\n", limit>>20, total>>20)
}
