package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	runtimedebug "runtime/debug"
	"strconv"
	"strings"
)

// Runtime memory ceiling for the panel backend (R2-P1-F12). The
// agent face has had two layers since the FIX-MEMLIMIT work — an
// in-process GOMEMLIMIT soft cap plus systemd MemoryHigh/MemoryMax
// from one shared tier table (go-gost/memlimit.go; install.sh
// agent_memory_limits) — while the panel face had none: no
// SetMemoryLimit/GOMEMLIMIT in the backend, no memory limit on any
// of the three services in either compose file, and no unit layer
// to fall back on (the panel is compose-deployed, there is no
// systemd unit). Backend growth (large backup imports,
// subscription rendering, cache surfaces) therefore had no
// first-class rung below the host OOM killer, which on a small
// panel host can pick the co-located postgres instead.
//
// This file closes the asymmetry with the same primary layer the
// agent uses: at startup the backend sets a GOMEMLIMIT soft cap
// sized from the machine's total memory via debug.SetMemoryLimit.
// Approaching the cap tightens GC instead of ending in an abrupt
// kill. An explicit GOMEMLIMIT in the environment always wins and
// is never overridden.
//
// The tier table below is the agent's table, unchanged — the two
// faces share one sizing policy so a host's ceiling does not
// depend on which FLMX process runs on it. The agent copy
// (go-gost/memlimit.go) is in turn pinned against install.sh's
// unit-layer copy by their tests; this third copy is pinned to the
// same numbers by memlimit_test.go in this package. Change them
// together.
//
// Note the tiers key off MemTotal — the host-visible memory. The
// backend normally runs in a container whose /proc/meminfo shows
// the host total; a cgroup limit below the tier (if an operator
// adds one) would bind first, which is the safe direction.

// memTierSmallMaxMiB is the largest machine size that takes the
// small-machine share of the tier table.
const memTierSmallMaxMiB = 512

// memSoftCapMiB is the absolute ceiling of the soft cap on large
// machines.
const memSoftCapMiB = 2048

// memoryTierFor maps a machine's total memory (MiB) to the soft
// cap (MiB): ≤512MiB takes 70% of total (the agent incident shape:
// leave the OS and co-located services the remaining ~30% before
// GC pressure starts); larger machines take half of total, capped
// at memSoftCapMiB so the ceiling stays a ceiling. Only the soft
// rung is used on the panel face — the high/max rungs of the
// agent's table belong to its systemd unit layer, which the panel
// does not have.
func memoryTierFor(totalMiB int64) (softMiB int64) {
	if totalMiB <= memTierSmallMaxMiB {
		return totalMiB * 7 / 10
	}
	soft := totalMiB / 2
	if soft > memSoftCapMiB {
		soft = memSoftCapMiB
	}
	return soft
}

// softLimitBytes decides the in-process soft limit (in bytes) to
// install. ok=false means "leave the runtime exactly as
// configured": either an explicit GOMEMLIMIT is already in the
// environment (the operator's value wins), or the machine size is
// unknown/degenerate.
func softLimitBytes(getenv func(string) string, totalBytes int64) (limit int64, ok bool) {
	if getenv("GOMEMLIMIT") != "" {
		return 0, false
	}
	if totalBytes <= 0 {
		return 0, false
	}
	return memoryTierFor(totalBytes>>20) << 20, true
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
// in main, before any business allocation, and never fails
// startup: any problem (explicit env value, unreadable meminfo)
// just leaves the runtime default in place and says so in the
// startup log.
func applyMemoryLimit() {
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		log.Printf("GOMEMLIMIT 已由环境显式设置（%s），进程内内存软帽不覆盖", v)
		return
	}
	total, err := readMemTotalBytes()
	if err != nil {
		log.Printf("读取机器内存失败，跳过内存软帽: %v", err)
		return
	}
	limit, ok := softLimitBytes(os.Getenv, total)
	if !ok {
		return
	}
	runtimedebug.SetMemoryLimit(limit)
	log.Printf("内存软帽已设: GOMEMLIMIT=%dMiB（机器总内存 %dMiB）", limit>>20, total>>20)
}
