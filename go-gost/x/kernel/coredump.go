package kernel

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
)

// Core dump hygiene for the agent's data directories. The agent and
// the helpers it spawns (nft, the supervised kernels) inherit the
// host's core_pattern; when that is a relative value such as "core",
// every crash leaves a multi-megabyte core file in the working
// directory, and a helper crashing in a loop fills the disk. The
// agent zeroes RLIMIT_CORE at startup (see go-gost's main package),
// which every child inherits; RemoveCoreDumps is the other half — it
// sweeps core files already stockpiled before that protection
// existed. A file is deleted only when its name is a core-name
// candidate AND its ELF header says e_type == ET_CORE, so a regular
// file that merely happens to be named like a core is safe: the
// ELF check is the safety predicate, the name is only a coarse
// screen deciding which files are worth opening.

// coreNameCandidate is the coarse name screen for the sweep. The
// host's core_pattern decides the shape of the product: the plain
// "core" and "core.<pid>" of a bare pattern, but also forms with
// the executable name, hostname or timestamps around it when the
// pattern carries %e / %h / %t fields ("core.xray.1234",
// "xray.core"). The old screen only matched "core" and
// "core.<digits>", so on hosts with a decorated pattern the sweep
// silently never matched anything (R2-031). The widened screen
// accepts any name carrying "core" as a whole dot-separated
// component; everything it lets through still has to prove itself
// an ELF core dump before it is deleted, and a name like "mycore"
// or "coreutils" — "core" only as a substring — stays unscreened.
func coreNameCandidate(name string) bool {
	for _, part := range strings.Split(name, ".") {
		if part == "core" {
			return true
		}
	}
	return false
}

// isCoreDumpFile reports whether path is an ELF core dump: ELF magic
// present and e_type == ET_CORE (4), honouring the header's own byte
// order.
func isCoreDumpFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [20]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		return false
	}
	if hdr[0] != 0x7f || hdr[1] != 'E' || hdr[2] != 'L' || hdr[3] != 'F' {
		return false
	}
	const etCore = 4
	switch hdr[5] { // EI_DATA
	case 1: // little-endian
		return binary.LittleEndian.Uint16(hdr[16:18]) == etCore
	case 2: // big-endian
		return binary.BigEndian.Uint16(hdr[16:18]) == etCore
	}
	return false
}

// RemoveCoreDumps deletes verified core dump files sitting directly
// in dir and returns how many were removed and how many bytes they
// freed. Missing directories and unreadable entries are skipped.
func RemoveCoreDumps(dir string) (removed int, freed int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !coreNameCandidate(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !isCoreDumpFile(path) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
			freed += info.Size()
		}
	}
	return removed, freed
}
