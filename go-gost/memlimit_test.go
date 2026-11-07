package main

import (
	"os"
	"path/filepath"
	runtimedebug "runtime/debug"
	"strconv"
	"testing"
)

// TestMemoryTierFor pins the shared tier table (memlimit.go). The
// systemd unit layer renders its MemoryHigh/MemoryMax from a copy of
// this table in install.sh (agent_memory_limits), pinned to the same
// numbers by test-install-scripts-proxy.sh — the two tables must
// never drift apart.
func TestMemoryTierFor(t *testing.T) {
	cases := []struct {
		totalMiB int64
		want     memoryTier
	}{
		{256, memoryTier{179, 223, 256}},     // small tier, max capped at total
		{453, memoryTier{317, 396, 453}},     // ECS1686's 464448 kB machine
		{512, memoryTier{358, 447, 512}},     // top of the small tier
		{1024, memoryTier{512, 640, 768}},    // proportional tier
		{8192, memoryTier{2048, 2560, 3072}}, // absolute soft-cap ceiling
		{16384, memoryTier{2048, 2560, 3072}},
	}
	for _, c := range cases {
		if got := memoryTierFor(c.totalMiB); got != c.want {
			t.Errorf("memoryTierFor(%d) = %+v, want %+v", c.totalMiB, got, c.want)
		}
		if c.want.softMiB >= c.want.highMiB || c.want.highMiB > c.want.maxMiB {
			t.Errorf("tier %d: rungs out of order: %+v", c.totalMiB, c.want)
		}
	}
}

// TestSoftLimitBytes pins the decision rules: an explicit GOMEMLIMIT
// in the environment always wins, an unknown machine size installs
// nothing, and a known size maps to the tier's soft cap in bytes.
func TestSoftLimitBytes(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if _, ok := softLimitBytes(env(map[string]string{"GOMEMLIMIT": "1GiB"}), 8192<<20); ok {
		t.Error("explicit GOMEMLIMIT must suppress the in-process cap")
	}
	if _, ok := softLimitBytes(env(nil), 0); ok {
		t.Error("unknown total memory must install no cap")
	}
	limit, ok := softLimitBytes(env(nil), int64(464448)*1024)
	if !ok {
		t.Fatal("known total memory must install a cap")
	}
	if want := int64(317) << 20; limit != want {
		t.Errorf("soft limit for 464448 kB = %d bytes, want %d (317MiB tier)", limit, want)
	}
}

// writeMeminfoFixture points meminfoPath at a temp file with the
// given MemTotal (kB) for the duration of the test.
func writeMeminfoFixture(t *testing.T, totalKB int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	content := "MemTotal:       " + strconv.FormatInt(totalKB, 10) + " kB\nMemFree:         1024 kB\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := meminfoPath
	meminfoPath = path
	t.Cleanup(func() { meminfoPath = old })
}

func TestReadMemTotalBytes(t *testing.T) {
	writeMeminfoFixture(t, 464448)
	got, err := readMemTotalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(464448) * 1024; got != want {
		t.Errorf("readMemTotalBytes = %d, want %d", got, want)
	}
}

// TestApplyMemoryLimit is the construct-level end-to-end check: with
// a small-machine meminfo fixture, applyMemoryLimit must actually
// move the runtime's memory limit to the tier value; with an
// explicit GOMEMLIMIT in the environment it must leave the runtime
// untouched. (The behavioral check — GC tightening under the cap
// instead of an OOM kill on a real small node — is a live-machine
// case owned by the release verification matrix.)
func TestApplyMemoryLimit(t *testing.T) {
	prev := runtimedebug.SetMemoryLimit(-1) // negative input reads without changing
	t.Cleanup(func() { runtimedebug.SetMemoryLimit(prev) })

	writeMeminfoFixture(t, 464448)
	t.Setenv("GOMEMLIMIT", "")
	applyMemoryLimit()
	if got, want := runtimedebug.SetMemoryLimit(-1), int64(317)<<20; got != want {
		t.Errorf("after applyMemoryLimit, runtime limit = %d, want %d", got, want)
	}

	runtimedebug.SetMemoryLimit(prev)
	t.Setenv("GOMEMLIMIT", "900MiB")
	applyMemoryLimit()
	if got := runtimedebug.SetMemoryLimit(-1); got != prev {
		t.Errorf("explicit GOMEMLIMIT: runtime limit changed to %d, want untouched %d", got, prev)
	}
}
