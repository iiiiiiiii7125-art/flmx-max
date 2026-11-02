package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-gost/x/kernel"
)

// sweepCoreDumps runs the agent's core dump hygiene at startup:
// disableCoreDumps (coredump_unix.go) zeroes RLIMIT_CORE for this
// process — the limit is inherited by every supervised kernel and
// helper, so none of them can leave a core file behind — and any
// core files already stockpiled in the data directories are swept
// (kernel.RemoveCoreDumps, which deletes only files verified as ELF
// core dumps). The systemd unit's LimitCORE=0 backs this up on
// systemd hosts.
func sweepCoreDumps() {
	disableCoreDumps()
	var files int
	var bytes int64
	for _, dir := range coreSweepDirs() {
		n, freed := kernel.RemoveCoreDumps(dir)
		files += n
		bytes += freed
	}
	if files > 0 {
		fmt.Printf("[flmx] 已清理历史 core 文件 %d 个，释放 %d 字节\n", files, bytes)
	}
}

// coreSweepDirs lists the directories swept at startup: the agent's
// working directory (its data directory — the systemd unit sets
// WorkingDirectory to the install dir, and agent-cwd helpers such as
// nft dump there), the directory holding the agent binary, plus
// every supervised kernel's run directory, which is the working
// directory of the kernel processes.
//
// The binary's directory is swept explicitly (A-05): an agent that
// actually runs must sit in its data directory anyway — config.json
// is loaded by relative path, so a hand start from a foreign working
// directory exits on the missing config — but the script form keeps
// the binary inside the install directory, so naming that directory
// directly sweeps the install dir's stockpile even for such a start
// instead of depending on "." resolving there. In the container form
// the binary lives in /usr/local/bin, where no process runs with
// that working directory; the entry is a harmless extra scan.
func coreSweepDirs() []string {
	dirs := []string{"."}
	seen := map[string]bool{".": true}
	add := func(dir string) {
		if dir != "" && !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	// Mirror kernel.Default's base dir resolution.
	base := strings.TrimSpace(os.Getenv("FLMX_KERNEL_DIR"))
	if base == "" {
		base = strings.TrimSpace(os.Getenv("FLVX_KERNEL_DIR"))
	}
	if base == "" {
		base = "/etc/flux_agent/kernels"
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return dirs
	}
	for _, e := range entries {
		if e.IsDir() {
			add(filepath.Join(base, e.Name(), "run"))
		}
	}
	return dirs
}
