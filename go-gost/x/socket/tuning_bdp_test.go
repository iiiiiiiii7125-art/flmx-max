package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeProcSys builds a /proc/sys stand-in holding every tunable key at a
// stock value, and points the tuning paths at it. Adapted from upstream
// 3.3.42's tuning_test.go, with FLMX paths (99-flmx, flmx-bbr) and the
// legacy 99-flvx conf path kept.
func fakeProcSys(t *testing.T, available string, skip ...string) string {
	t.Helper()
	dir := t.TempDir()
	oldProc, oldConf, oldBackup, oldModprobe := tuneProcRoot, tuneConfPath, tuneBackupPath, tuneModprobe
	oldParams, oldMem, oldBBR, oldLegacy := tuneParamsPath, tuneMeminfo, tuneBBRModule, legacyTuneConfPath
	tuneProcRoot = filepath.Join(dir, "proc")
	tuneConfPath = filepath.Join(dir, "sysctl.d", "99-flmx-tuning.conf")
	tuneBackupPath = filepath.Join(dir, "agent", "tuning-backup.json")
	tuneParamsPath = filepath.Join(dir, "agent", "tuning-params.json")
	tuneMeminfo = filepath.Join(dir, "meminfo")
	tuneBBRModule = filepath.Join(dir, "modules-load.d", "flmx-bbr.conf")
	legacyTuneConfPath = filepath.Join(dir, "sysctl.d", "99-flvx-tuning.conf")
	tuneModprobe = func(string) error { return nil }
	t.Cleanup(func() {
		tuneProcRoot, tuneConfPath, tuneBackupPath, tuneModprobe = oldProc, oldConf, oldBackup, oldModprobe
		tuneParamsPath, tuneMeminfo, tuneBBRModule, legacyTuneConfPath = oldParams, oldMem, oldBBR, oldLegacy
	})
	// 2 GiB of memory.
	if err := os.WriteFile(tuneMeminfo, []byte("MemTotal:        2097152 kB\nMemFree:  1000 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(tuneConfPath), 0o755)

	skipped := map[string]bool{}
	for _, k := range skip {
		skipped[k] = true
	}
	write := func(key, value string) {
		p := sysctlPath(key)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range tuneGroupsFor(tuneSizesFor(defaultTuneParams, 2048)) {
		for _, s := range g.Settings {
			if !skipped[s.Key] {
				write(s.Key, "0")
			}
		}
	}
	write("net.ipv4.tcp_congestion_control", "cubic")
	write("net.ipv4.tcp_available_congestion_control", available)
	write("net.ipv4.tcp_rmem", "4096\t131072\t6291456")
	write("kernel.osrelease", "6.1.0-test")
	return dir
}

func TestTuneBdpApplyAndRestore(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr", "net.netfilter.nf_conntrack_max")

	st, err := applyTuning([]string{"bbr", "buffer", "backlog"}, defaultTuneParams)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "bbr" {
		t.Fatalf("congestion: %q", v)
	}
	// 1000 Mbps × 150 ms: 2 × 18.75 MB + 2 MiB, under the 64 MiB memory cap.
	if v, _ := readSysctl("net.ipv4.tcp_rmem"); v != "4096 1048576 39597152" {
		t.Fatalf("tcp_rmem: %q", v)
	}
	if v, _ := readSysctl("net.ipv4.tcp_mem"); v != "32768 65536 131072" {
		t.Fatalf("tcp_mem: %q", v)
	}
	if v, _ := readSysctl("net.ipv4.ip_local_port_range"); v != "10000 65535" {
		t.Fatalf("port range: %q", v)
	}
	if b, _ := os.ReadFile(tuneBBRModule); string(b) != "tcp_bbr\n" {
		t.Fatalf("bbr module not loaded at boot: %q", b)
	}
	conf, _ := os.ReadFile(tuneConfPath)
	if !strings.Contains(string(conf), "net.ipv4.tcp_congestion_control = bbr") || strings.Contains(string(conf), "nf_conntrack_max") {
		t.Fatalf("conf:\n%s", conf)
	}
	applied := map[string]bool{}
	for _, g := range st.Groups {
		applied[g.ID] = g.Applied
	}
	if !applied["bbr"] || !applied["buffer"] || !applied["backlog"] || applied["tcp"] || !st.HasBackup {
		t.Fatalf("status: %+v", st)
	}

	// Unchecking a group puts its keys back; the rest stay tuned.
	if _, err := applyTuning([]string{"buffer"}, defaultTuneParams); err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "cubic" {
		t.Fatalf("bbr not restored: %q", v)
	}
	if _, err := os.Stat(tuneBBRModule); !os.IsNotExist(err) {
		t.Fatal("bbr module file left behind")
	}
	if v, _ := readSysctl("net.core.rmem_max"); v != "39597152" {
		t.Fatalf("buffer lost: %q", v)
	}

	// Another line resizes the tuned buffers, even downwards.
	if _, err := applyTuning([]string{"buffer"}, tuneParams{BandwidthMbps: 100, RTTMs: 50}); err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.core.rmem_max"); v != "4194304" {
		t.Fatalf("small line: %q", v)
	}
	if savedTuneParams() != (tuneParams{BandwidthMbps: 100, RTTMs: 50}) {
		t.Fatalf("params not kept: %+v", savedTuneParams())
	}

	// A groups-only request (old panel) reuses the saved line, not the
	// defaults: rmem stays at the 4 MiB floor instead of jumping back
	// to the 1000 Mbps / 150 ms value.
	if _, err := handleTuneCommand("TuneApply", map[string]interface{}{"groups": []string{"buffer"}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.core.rmem_max"); v != "4194304" {
		t.Fatalf("groups-only apply lost saved line: %q", v)
	}

	// Restore puts back the values found before the first apply.
	st, err = applyTuning(nil, defaultTuneParams)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.ipv4.tcp_rmem"); v != "4096 131072 6291456" {
		t.Fatalf("tcp_rmem not restored: %q", v)
	}
	if _, err := os.Stat(tuneConfPath); !os.IsNotExist(err) {
		t.Fatal("conf file left behind")
	}
	if _, err := os.Stat(tuneBackupPath); !os.IsNotExist(err) || st.HasBackup {
		t.Fatal("backup left behind")
	}
	if _, err := os.Stat(tuneParamsPath); !os.IsNotExist(err) {
		t.Fatal("params left behind")
	}
}

func TestTuneBdpSkipsBBRWithoutKernelSupport(t *testing.T) {
	fakeProcSys(t, "reno cubic")
	st, err := applyTuning([]string{"bbr", "forward"}, defaultTuneParams)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "cubic" {
		t.Fatalf("bbr set anyway: %q", v)
	}
	if v, _ := readSysctl("net.ipv4.ip_forward"); v != "1" {
		t.Fatalf("forward: %q", v)
	}
	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "BBR") {
		t.Fatalf("warnings: %v", st.Warnings)
	}
}

func TestTuneBdpCommandValidation(t *testing.T) {
	fakeProcSys(t, "cubic bbr")
	if _, err := handleTuneCommand("TuneApply", map[string]interface{}{"groups": []string{"rm-rf"}}); err == nil {
		t.Fatal("expected an error")
	}
	out, err := handleTuneCommand("TuneStatus", nil)
	st := out.(tuneStatus)
	if err != nil || st.Kernel != "6.1.0-test" || len(st.Groups) != 6 || st.Params != defaultTuneParams || st.Sizes.RAMMB != 2048 {
		t.Fatalf("status: %+v %v", out, err)
	}
	// The targets follow the line given.
	out, _ = handleTuneCommand("TuneStatus", map[string]interface{}{"bandwidthMbps": 100, "rttMs": 50})
	if s := out.(tuneStatus).Sizes; s.BufMax != 4*mib || s.Reason != "最低 4MB" {
		t.Fatalf("sizes: %+v", s)
	}
	// Partial and out-of-range lines are rejected (zero bandwidth,
	// zero RTT, over-range RTT).
	for _, data := range []map[string]interface{}{
		{"bandwidthMbps": 0, "rttMs": 5000},
		{"bandwidthMbps": 1000, "rttMs": 0},
		{"bandwidthMbps": 0, "rttMs": 150},
		{"bandwidthMbps": 100001, "rttMs": 150},
	} {
		if _, err := handleTuneCommand("TuneStatus", data); err == nil {
			t.Fatalf("line %+v must be rejected", data)
		}
	}
	// Unknown fields are ignored by encoding/json — the same mechanism
	// by which an old agent ignores bandwidthMbps/rttMs from a new panel.
	if _, err := handleTuneCommand("TuneStatus", map[string]interface{}{"futureField": 123}); err != nil {
		t.Fatalf("unknown field must be ignored: %v", err)
	}
}

func TestTuneBdpNeverLowersLimits(t *testing.T) {
	fakeProcSys(t, "cubic bbr")
	_ = writeSysctl("fs.file-max", "9999999")
	_ = writeSysctl("net.ipv4.tcp_rmem", "4096 131072 134217728")

	st, err := applyTuning([]string{"files", "buffer"}, defaultTuneParams)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("fs.file-max"); v != "9999999" {
		t.Fatalf("file-max lowered to %q", v)
	}
	if v, _ := readSysctl("net.ipv4.tcp_rmem"); v != "4096 131072 134217728" {
		t.Fatalf("tcp_rmem lowered to %q", v)
	}
	if v, _ := readSysctl("fs.nr_open"); v != "1048576" {
		t.Fatalf("nr_open not raised: %q", v)
	}
	conf, _ := os.ReadFile(tuneConfPath)
	if strings.Contains(string(conf), "file-max") {
		t.Fatalf("conf pins a value the node already beat:\n%s", conf)
	}
	for _, g := range st.Groups {
		if g.ID == "files" && !g.Applied {
			t.Fatalf("files should count as applied: %+v", g)
		}
	}
	// Restoring leaves the untouched key alone.
	if _, err := applyTuning(nil, defaultTuneParams); err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("fs.file-max"); v != "9999999" {
		t.Fatalf("file-max changed on restore: %q", v)
	}
}

func TestTuneBdpFailedSettingKeepsBackup(t *testing.T) {
	fakeProcSys(t, "cubic bbr")
	// A kernel without the fq qdisc answers ENOENT when it is written.
	tuneWrite = func(key, value string) error {
		if key == "net.core.default_qdisc" {
			return &os.PathError{Op: "write", Path: key, Err: syscall.ENOENT}
		}
		return writeSysctl(key, value)
	}
	t.Cleanup(func() { tuneWrite = writeSysctl })

	st, err := applyTuning([]string{"bbr"}, defaultTuneParams)
	if err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(tuneConfPath)
	if strings.Contains(string(conf), "default_qdisc") || !strings.Contains(string(conf), "tcp_congestion_control = bbr") {
		t.Fatalf("conf:\n%s", conf)
	}
	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "default_qdisc") {
		t.Fatalf("warnings: %v", st.Warnings)
	}
	// O1-001 (fork fix, NOT upstream 3.3.42's delete-on-failure): the
	// failed key's pre-tune value must stay backed up, or it can never
	// be restored.
	b, err := os.ReadFile(tuneBackupPath)
	if err != nil {
		t.Fatal(err)
	}
	var backup map[string]string
	if err := json.Unmarshal(b, &backup); err != nil {
		t.Fatal(err)
	}
	if backup["net.core.default_qdisc"] != "0" {
		t.Fatalf("failed key lost from backup: %v", backup)
	}
}

func TestTuneBdpLegacyConfRemoved(t *testing.T) {
	dir := fakeProcSys(t, "cubic bbr")
	legacy := filepath.Join(dir, "sysctl.d", "99-flvx-tuning.conf")
	if err := os.WriteFile(legacy, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyTuning([]string{"forward"}, defaultTuneParams); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy conf left behind")
	}
}

func TestTuneBdpSizes(t *testing.T) {
	for _, c := range []struct {
		name   string
		p      tuneParams
		ram    int64
		max    int64
		reason string
		tcpMem string
	}{
		// 2 × 18.75 MB + 2 MiB.
		{"gigabit cross-border", tuneParams{1000, 150}, 4096, 39597152, "带宽 × 延迟 × 2 + 2MB", "65536 131072 262144"},
		// 512 MB: a socket gets at most 1/32 of the memory.
		{"small VPS", tuneParams{1000, 150}, 512, 16 * mib, "受内存限制", "8192 16384 32768"},
		{"slow line", tuneParams{10, 20}, 2048, 4 * mib, "最低 4MB", "32768 65536 131072"},
		// 2 × 250 MB + 2 MiB, over the 256 MiB ceiling.
		{"10G long-haul", tuneParams{10000, 200}, 65536, 256 * mib, "受内存限制", "1048576 2097152 4194304"},
		// The tcp_mem floors on a tiny machine.
		{"tiny", tuneParams{100, 100}, 128, 4 * mib, "受内存限制", "4096 8192 16384"},
		// Unknown memory (ram 0): D43-T5 caps the budget at 4 MiB instead
		// of leaving it unbounded (upstream
		// TestTuneMissingMemoryCannotCreateUnboundedBuffer).
		{"unknown memory", tuneParams{1000, 150}, 0, 4 * mib, "受内存限制", "4096 8192 16384"},
		// Zero bandwidth degrades to the floor, never to zero.
		{"zero bandwidth", tuneParams{0, 150}, 2048, 4 * mib, "最低 4MB", "32768 65536 131072"},
		// Max line, huge RTT: capped by the 256 MiB ceiling, no overflow.
		{"max line huge RTT", tuneParams{100000, 2000}, 65536, 256 * mib, "受内存限制", "1048576 2097152 4194304"},
		// Max line on a small VPS: capped by 1/32 of memory.
		{"max line small VPS", tuneParams{100000, 2000}, 512, 16 * mib, "受内存限制", "8192 16384 32768"},
	} {
		s := tuneSizesFor(c.p, c.ram)
		if s.BufMax != c.max || s.Reason != c.reason || s.TCPMem != c.tcpMem || s.BufDef != mib {
			t.Errorf("%s: %+v", c.name, s)
		}
	}
}

func TestTuneBdpParamsValid(t *testing.T) {
	for _, c := range []struct {
		p     tuneParams
		valid bool
	}{
		{tuneParams{1, 1}, true},
		{tuneParams{100000, 2000}, true},
		{tuneParams{0, 0}, false},
		{tuneParams{0, 150}, false},
		{tuneParams{1000, 0}, false},
		{tuneParams{-1, 150}, false},
		{tuneParams{100001, 150}, false},
		{tuneParams{1000, 2001}, false},
	} {
		if got := c.p.valid(); got != c.valid {
			t.Errorf("%+v: valid=%v", c.p, got)
		}
	}
}
