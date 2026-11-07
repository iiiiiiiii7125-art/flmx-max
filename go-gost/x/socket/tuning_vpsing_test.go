package socket

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeProcSys builds a /proc/sys stand-in and points every tuning
// path at a temporary tree, so tuning tests never touch the host.
// It serves both the retained line-probe tests (which call it for
// the sysctl tree) and the second-generation tuning tests. The
// first-generation helper of the same name seeded BDP group keys;
// this one seeds the keys the new model reads and applies.
func fakeProcSys(t *testing.T, available string, skip ...string) string {
	t.Helper()
	dir := t.TempDir()
	old := struct {
		procRoot, meminfo, gai, gaiBak, bbr, kernel, limits string
		rpsUnit, rpsScript, netDir, snapshot                string
		oldConf, legacyConf, bbrMod, shapeUnit              string
		numCPU                                              func() int
	}{
		tuneProcRoot, tuneMeminfoPath, tuneGaiConfPath, tuneGaiBackupPath, tuneBBRConfPath,
		tuneKernelConfPath, tuneLimitsConfPath, tuneRPSUnitPath, tuneRPSScriptPath, tuneNetDir,
		tuneKernelSnapshotPath,
		oldTuneConfPath, legacyTuneConfPath, oldTuneBBRModulePath, oldTuneShapeUnitPath, tuneNumCPU,
	}
	tuneProcRoot = filepath.Join(dir, "proc")
	tuneMeminfoPath = filepath.Join(dir, "meminfo")
	tuneGaiConfPath = filepath.Join(dir, "gai.conf")
	tuneGaiBackupPath = filepath.Join(dir, "gai.conf.bak")
	tuneBBRConfPath = filepath.Join(dir, "sysctl.d", "10-bbr.conf")
	tuneKernelConfPath = filepath.Join(dir, "sysctl.d", "99-network-performance.conf")
	tuneLimitsConfPath = filepath.Join(dir, "limits.d", "99-network-performance.conf")
	tuneRPSUnitPath = filepath.Join(dir, "systemd", "flmx-tuning-rps.service")
	tuneRPSScriptPath = filepath.Join(dir, "bin", "flmx-tuning-rps.sh")
	tuneNetDir = filepath.Join(dir, "net")
	tuneKernelSnapshotPath = filepath.Join(dir, "security", "flmx-tuning-kernel-sysctls.json")
	oldTuneConfPath = filepath.Join(dir, "sysctl.d", "99-flmx-tuning.conf")
	legacyTuneConfPath = filepath.Join(dir, "sysctl.d", "99-flvx-tuning.conf")
	oldTuneBBRModulePath = filepath.Join(dir, "modules-load.d", "flmx-bbr.conf")
	oldTuneShapeUnitPath = filepath.Join(dir, "systemd", "flmx-tuning-qdisc.service")
	tuneNumCPU = func() int { return 4 }
	t.Cleanup(func() {
		tuneProcRoot, tuneMeminfoPath, tuneGaiConfPath, tuneGaiBackupPath, tuneBBRConfPath = old.procRoot, old.meminfo, old.gai, old.gaiBak, old.bbr
		tuneKernelConfPath, tuneLimitsConfPath, tuneRPSUnitPath, tuneRPSScriptPath, tuneNetDir = old.kernel, old.limits, old.rpsUnit, old.rpsScript, old.netDir
		tuneKernelSnapshotPath = old.snapshot
		oldTuneConfPath, legacyTuneConfPath, oldTuneBBRModulePath, oldTuneShapeUnitPath, tuneNumCPU = old.oldConf, old.legacyConf, old.bbrMod, old.shapeUnit, old.numCPU
	})
	// 2 GiB of memory.
	if err := os.WriteFile(tuneMeminfoPath, []byte("MemTotal:        2097152 kB\nMemFree:  1000 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tuneNetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skipped := map[string]bool{}
	for _, k := range skip {
		skipped[k] = true
	}
	write := func(key, value string) {
		if skipped[key] {
			return
		}
		p := sysctlPath(key)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range kernelTuneSettings(64 << 20) {
		write(s.key, "0")
	}
	write("net.ipv4.tcp_congestion_control", "cubic")
	write("net.core.default_qdisc", "fq_codel")
	write("net.core.rps_sock_flow_entries", "0")
	write("net.ipv4.tcp_available_congestion_control", available)
	write("net.ipv4.tcp_rmem", "4096\t131072\t6291456")
	write("net.ipv4.tcp_wmem", "4096\t16384\t4194304")
	write("net.ipv4.ip_local_port_range", "32768\t60999")
	write("kernel.osrelease", "6.1.0-test")
	return dir
}

// stubTuneCommands replaces the subprocess seam with a recorder and
// the lookup seam with a fixed answer set (names present in the map
// "exist"). Returns the recorded argv list.
func stubTuneCommands(t *testing.T, present map[string]bool) *[][]string {
	t.Helper()
	var calls [][]string
	oldCmd, oldLook := tuneCommand, tuneLookPath
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		return "", nil
	}
	tuneLookPath = func(name string) (string, error) {
		if present[name] {
			return "/usr/bin/" + name, nil
		}
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { tuneCommand, tuneLookPath = oldCmd, oldLook })
	return &calls
}

func TestKernelTuneSettingsExactSet(t *testing.T) {
	settings := kernelTuneSettings(107373568)
	want := map[string]string{
		"net.core.default_qdisc":             "fq",
		"net.ipv4.tcp_congestion_control":    "bbr",
		"net.core.somaxconn":                 "65535",
		"net.core.netdev_max_backlog":        "65535",
		"net.ipv4.tcp_max_syn_backlog":       "16384",
		"net.ipv4.ip_local_port_range":       "1024 65535",
		"net.core.rmem_max":                  "107373568",
		"net.core.wmem_max":                  "107373568",
		"net.ipv4.tcp_rmem":                  "4096 87380 107373568",
		"net.ipv4.tcp_wmem":                  "4096 65536 107373568",
		"net.core.rmem_default":              "2097152",
		"net.core.wmem_default":              "2097152",
		"net.ipv4.tcp_notsent_lowat":         "16384",
		"net.ipv4.tcp_mtu_probing":           "1",
		"net.ipv4.udp_rmem_min":              "16384",
		"net.ipv4.udp_wmem_min":              "16384",
		"net.ipv4.tcp_ecn":                   "1",
		"net.ipv4.tcp_max_orphans":           "32768",
		"net.ipv4.tcp_slow_start_after_idle": "0",
		"net.ipv4.tcp_tw_reuse":              "1",
		"net.ipv4.tcp_fin_timeout":           "15",
		"net.ipv4.tcp_retries2":              "8",
		"net.ipv4.tcp_fastopen":              "3",
	}
	if len(settings) != len(want) {
		t.Fatalf("settings count = %d, want %d", len(settings), len(want))
	}
	for _, s := range settings {
		w, ok := want[s.key]
		if !ok {
			t.Fatalf("unexpected key %s", s.key)
		}
		if s.value != w {
			t.Fatalf("%s = %q, want %q", s.key, s.value, w)
		}
		if s.key == "net.ipv4.tcp_congestion_control_version" {
			t.Fatal("tcp_congestion_control_version must not be written (non-mainline sysctl)")
		}
	}
}

func TestTuneBufferBytes(t *testing.T) {
	// Script formula: MemTotalKB * 5 / 100 * 1024, integer steps.
	if got := tuneBufferBytes(2097152); got != 107373568 {
		t.Fatalf("2 GiB: got %d, want 107373568", got)
	}
	if got := tuneBufferBytes(475136); got != 24326144 { // 464 MiB
		t.Fatalf("464 MiB: got %d, want 24326144", got)
	}
}

func TestApplyIPv4PriorityLifecycle(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	orig := "label ::/0          1\nprecedence  ::/0          40\n"
	if err := os.WriteFile(tuneGaiConfPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyIPv4Priority(); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(tuneGaiBackupPath)
	if err != nil || string(bak) != orig {
		t.Fatalf("backup = %q, %v", bak, err)
	}
	content, _ := os.ReadFile(tuneGaiConfPath)
	if !gaiPrecedenceActive(string(content)) {
		t.Fatalf("gai.conf not activated:\n%s", content)
	}
	if st := readTuneStatus(); !st.IPv4Priority {
		t.Fatal("status must report ipv4Priority")
	}
	// Second apply is a no-op: the backup keeps the ORIGINAL content.
	if err := applyIPv4Priority(); err != nil {
		t.Fatal(err)
	}
	bak, _ = os.ReadFile(tuneGaiBackupPath)
	if string(bak) != orig {
		t.Fatalf("backup overwritten by second apply: %q", bak)
	}
	// Rollback restores the backup verbatim and consumes it.
	rollbackTuning(context.Background())
	content, _ = os.ReadFile(tuneGaiConfPath)
	if string(content) != orig {
		t.Fatalf("gai.conf after rollback = %q, want original", content)
	}
	if _, err := os.Stat(tuneGaiBackupPath); !os.IsNotExist(err) {
		t.Fatal("backup must be consumed by rollback")
	}
}

func TestApplyIPv4PriorityUncommentAndRollbackComments(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	orig := "label ::/0          1\n#" + gaiPrecedenceLine + "\n"
	if err := os.WriteFile(tuneGaiConfPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyIPv4Priority(); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(tuneGaiConfPath)
	if !gaiPrecedenceActive(string(content)) || strings.Contains(string(content), "#"+gaiPrecedenceLine) {
		t.Fatalf("commented form not enabled:\n%s", content)
	}
	// Rollback with the backup restores the commented original.
	rollbackTuning(context.Background())
	content, _ = os.ReadFile(tuneGaiConfPath)
	if string(content) != orig {
		t.Fatalf("rollback = %q, want %q", content, orig)
	}

	// No backup: rollback comments the active line back out.
	if err := os.WriteFile(tuneGaiConfPath, []byte(gaiPrecedenceLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreGaiConf()
	content, _ = os.ReadFile(tuneGaiConfPath)
	if gaiPrecedenceActive(string(content)) || !strings.Contains(string(content), "#"+gaiPrecedenceLine) {
		t.Fatalf("line not commented back:\n%s", content)
	}
}

func TestApplyIPv4PriorityMissingFile(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	if err := applyIPv4Priority(); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(tuneGaiConfPath)
	if !strings.Contains(string(content), "label ::/0") || !gaiPrecedenceActive(string(content)) {
		t.Fatalf("missing gai.conf must be created from the template plus the line:\n%s", content)
	}
	if _, err := os.Stat(tuneGaiBackupPath); !os.IsNotExist(err) {
		t.Fatal("no backup may be taken of a file that did not exist")
	}
}

func TestApplyBBRAndKernel(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	calls := stubTuneCommands(t, map[string]bool{}) // no iptables anywhere

	warnings, err := applyBBR()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("bbr warnings: %v", warnings)
	}
	b, _ := os.ReadFile(tuneBBRConfPath)
	if string(b) != tuneBBRConfBody {
		t.Fatalf("bbr conf = %q", b)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "bbr" {
		t.Fatalf("live cc = %q", v)
	}
	if st := readTuneStatus(); !st.BBR || st.CongestionControl != "bbr" || st.DefaultQdisc != "fq" {
		t.Fatalf("status after bbr: %+v", st)
	}

	warnings, err = applyKernelTuning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warnings, "；")
	if !strings.Contains(joined, "iptables 不存在") {
		t.Fatalf("missing-iptables note expected in warnings: %v", warnings)
	}
	conf, _ := os.ReadFile(tuneKernelConfPath)
	for _, want := range []string{
		"net.core.rmem_max = 107373568",
		"net.ipv4.tcp_rmem = 4096 87380 107373568",
		"net.ipv4.ip_local_port_range = 1024 65535",
		"net.ipv4.tcp_fastopen = 3",
	} {
		if !strings.Contains(string(conf), want) {
			t.Fatalf("kernel conf missing %q:\n%s", want, conf)
		}
	}
	if strings.Contains(string(conf), "tcp_congestion_control_version") {
		t.Fatal("conf must not carry tcp_congestion_control_version")
	}
	limits, _ := os.ReadFile(tuneLimitsConfPath)
	if string(limits) != tuneLimitsConfBody {
		t.Fatalf("limits conf = %q", limits)
	}
	if v, _ := readSysctl("net.core.somaxconn"); v != "65535" {
		t.Fatalf("live somaxconn = %q", v)
	}
	if st := readTuneStatus(); !st.Kernel || !st.NoFile.LimitsFile {
		t.Fatalf("status after kernel: %+v", st)
	}
	_ = calls
}

// TestApplyKernelMSSClampOrder is the TL-01 regression gate: every
// iptables argv for the MSS clamp rule must be exactly
// [-t mangle <-A|-D> POSTROUTING <match…>] — the verb immediately
// after "-t mangle", before the chain name. The pre-fix shape
// ([-A -t mangle POSTROUTING …]) is rejected by iptables with
// exit 2 on every machine. The rollback delete path is gated too.
func TestApplyKernelMSSClampOrder(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	calls := stubTuneCommands(t, map[string]bool{"iptables": true})
	if _, err := applyKernelTuning(context.Background()); err != nil {
		t.Fatal(err)
	}
	tuneMSSClampRemove(context.Background())
	var got [][]string
	for _, c := range *calls {
		if c[0] == "iptables" {
			got = append(got, c[1:])
		}
	}
	match := []string{"-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"}
	want := [][]string{
		append([]string{"-t", "mangle", "-D", "POSTROUTING"}, match...),
		append([]string{"-t", "mangle", "-A", "POSTROUTING"}, match...),
		append([]string{"-t", "mangle", "-D", "POSTROUTING"}, match...),
	}
	if len(got) != len(want) {
		t.Fatalf("MSS calls = %v, want %v", got, want)
	}
	for i := range want {
		if strings.Join(got[i], " ") != strings.Join(want[i], " ") {
			t.Fatalf("MSS call %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestApplyNICSteering(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{}) // no ethtool, no systemctl
	for _, iface := range []string{"eth0", "docker0", "wg0"} {
		q := filepath.Join(tuneNetDir, iface, "queues", "rx-0")
		if err := os.MkdirAll(q, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(q, "rps_cpus"), []byte("0\n"), 0o644)
		_ = os.WriteFile(filepath.Join(q, "rps_flow_cnt"), []byte("0\n"), 0o644)
	}
	warnings, err := applyNICTuning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(tuneNetDir, "eth0", "queues", "rx-0", "rps_cpus")); strings.TrimSpace(string(b)) != "f" {
		t.Fatalf("eth0 rps_cpus = %q, want f (4 cores)", b)
	}
	if b, _ := os.ReadFile(filepath.Join(tuneNetDir, "eth0", "queues", "rx-0", "rps_flow_cnt")); strings.TrimSpace(string(b)) != "4096" {
		t.Fatalf("eth0 rps_flow_cnt = %q", b)
	}
	for _, excluded := range []string{"docker0", "wg0"} {
		if b, _ := os.ReadFile(filepath.Join(tuneNetDir, excluded, "queues", "rx-0", "rps_cpus")); strings.TrimSpace(string(b)) != "0" {
			t.Fatalf("%s must be excluded, rps_cpus = %q", excluded, b)
		}
	}
	if v, _ := readSysctl("net.core.rps_sock_flow_entries"); v != "32768" {
		t.Fatalf("rps_sock_flow_entries = %q", v)
	}
	if st := readTuneStatus(); !st.NIC || st.RPSSockFlowEntries != 32768 {
		t.Fatalf("status after nic: %+v", st)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "未持久化") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no-systemd persistence note expected: %v", warnings)
	}
}

func TestRPSUnitAndScriptBodies(t *testing.T) {
	script := tuneRPSScript([]string{"eth0", "ens3"}, "f")
	for _, want := range []string{"for eth in eth0 ens3", "rps_cpus", "4096", "rps_sock_flow_entries=32768"} {
		if !strings.Contains(script, want) {
			t.Fatalf("rps script missing %q:\n%s", want, script)
		}
	}
	unit := tuneRPSUnitBody()
	for _, want := range []string{"Type=oneshot", "ExecStart=/bin/sh " + tuneRPSScriptPath, "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("rps unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRollbackRemovesWholeFootprint(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{"sysctl": true, "systemctl": true})
	files := []string{tuneKernelConfPath, tuneLimitsConfPath, tuneBBRConfPath,
		tuneRPSUnitPath, tuneRPSScriptPath, oldTuneConfPath, legacyTuneConfPath,
		oldTuneBBRModulePath, oldTuneShapeUnitPath}
	for _, f := range files {
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		_ = os.WriteFile(f, []byte("x"), 0o644)
	}
	q := filepath.Join(tuneNetDir, "eth0", "queues", "rx-0")
	_ = os.MkdirAll(q, 0o755)
	_ = os.WriteFile(filepath.Join(q, "rps_cpus"), []byte("f\n"), 0o644)
	_ = os.WriteFile(filepath.Join(q, "rps_flow_cnt"), []byte("4096\n"), 0o644)
	// Live tuned values.
	_ = writeSysctl("net.ipv4.tcp_congestion_control", "bbr")
	_ = writeSysctl("net.core.default_qdisc", "fq")
	_ = writeSysctl("net.core.rps_sock_flow_entries", "32768")

	warnings := rollbackTuning(context.Background())
	// No snapshot was captured in this scenario (footprint planted
	// by hand), so exactly one fail-safe warning must name the
	// unrestored keys (TL-02) — silence here would be the defect.
	if len(warnings) != 1 || !strings.Contains(warnings[0], "未能回退") ||
		!strings.Contains(warnings[0], "net.core.somaxconn") ||
		!strings.Contains(warnings[0], "net.ipv4.tcp_fastopen") {
		t.Fatalf("rollback warnings = %v, want one fail-safe naming unrestored keys", warnings)
	}
	for _, f := range files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("footprint %s survived rollback", f)
		}
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "cubic" {
		t.Fatalf("cc after rollback = %q", v)
	}
	if v, _ := readSysctl("net.core.default_qdisc"); v != "pfifo_fast" {
		t.Fatalf("qdisc after rollback = %q, want the scripted default when no snapshot exists", v)
	}
	if v, _ := readSysctl("net.core.rps_sock_flow_entries"); v != "0" {
		t.Fatalf("rps entries after rollback = %q", v)
	}
	if b, _ := os.ReadFile(filepath.Join(q, "rps_cpus")); strings.TrimSpace(string(b)) != "0" {
		t.Fatalf("rps_cpus after rollback = %q", b)
	}
	// TL-03: the flow count is cleared together with the cpu mask.
	if b, _ := os.ReadFile(filepath.Join(q, "rps_flow_cnt")); strings.TrimSpace(string(b)) != "0" {
		t.Fatalf("rps_flow_cnt after rollback = %q, want 0 (TL-03)", b)
	}
}

// TestKernelSnapshotRollbackRestoresExact is the TL-02 regression
// gate: apply-kernel snapshots the pre-apply values, and rollback
// restores every face-③ key to exactly the snapshot (an operator
// value set before tuning survives the round trip — the scripted
// factory defaults would clobber it). Presence of the conf file is
// NOT the oracle; the live values are.
func TestKernelSnapshotRollbackRestoresExact(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{})
	// Operator state that differs from both the tuned values and
	// the scripted defaults rollback used to write.
	operator := map[string]string{
		"net.core.somaxconn":                        "8192",
		"net.ipv4.tcp_fin_timeout":                  "30",
		"net.ipv4.tcp_ecn":                          "2",
		"net.core.rmem_max":                         "4194304",
		"net.ipv4.ip_local_port_range":              "10000 60000",
		"net.ipv4.tcp_congestion_control":           "cubic",
		"net.core.default_qdisc":                    "fq_codel",
		"net.ipv4.tcp_rmem":                         "4096 131072 6291456",
		"net.ipv4.tcp_available_congestion_control": "reno cubic bbr",
	}
	for k, v := range operator {
		if err := writeSysctl(k, v); err != nil {
			t.Fatal(err)
		}
	}
	warnings, err := applyKernelTuning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings {
		if !strings.Contains(w, "iptables 不存在") {
			t.Fatalf("apply warnings = %v, want only the known no-iptables note", warnings)
		}
	}
	// The snapshot carries exactly the operator state for the
	// kernelTuneSettings key set — no more, no less.
	raw, err := os.ReadFile(tuneKernelSnapshotPath)
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	var snap tuneKernelSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Values) != len(kernelTuneSettings(0)) {
		t.Fatalf("snapshot keys = %d, want %d (the kernelTuneSettings set)", len(snap.Values), len(kernelTuneSettings(0)))
	}
	for k, v := range operator {
		if k == "net.ipv4.tcp_available_congestion_control" {
			continue // not a tuned key; must not be captured
		}
		if snap.Values[k] != v {
			t.Fatalf("snapshot[%s] = %q, want operator value %q", k, snap.Values[k], v)
		}
	}
	// Tuned values are live now.
	if v, _ := readSysctl("net.core.somaxconn"); v != "65535" {
		t.Fatalf("somaxconn after apply = %q", v)
	}
	// A repeat apply must not overwrite the snapshot with tuned
	// values (first snapshot wins).
	if _, err := applyKernelTuning(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(tuneKernelSnapshotPath)
	if string(raw2) != string(raw) {
		t.Fatal("second apply overwrote the pre-apply snapshot")
	}
	// Rollback restores every key to the snapshot, byte for byte.
	if warnings := rollbackTuning(context.Background()); len(warnings) != 0 {
		t.Fatalf("rollback warnings = %v, want none", warnings)
	}
	for _, s := range kernelTuneSettings(0) {
		want, ok := snap.Values[s.key]
		if !ok {
			continue
		}
		if got, _ := readSysctl(s.key); got != want {
			t.Fatalf("%s after rollback = %q, want snapshot %q", s.key, got, want)
		}
	}
	if _, err := os.Stat(tuneKernelSnapshotPath); !os.IsNotExist(err) {
		t.Fatal("snapshot must be consumed by rollback")
	}
}

// TestKernelSnapshotFailSafe covers the TL-02 fail-safe contract:
// a damaged or missing snapshot never fails silently — rollback
// restores the scripted headline defaults, names every unrestored
// key, and still consumes the footprint.
func TestKernelSnapshotFailSafe(t *testing.T) {
	plant := func(t *testing.T) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(tuneKernelConfPath), 0o755)
		_ = os.WriteFile(tuneKernelConfPath, []byte("x"), 0o644)
		_ = writeSysctl("net.ipv4.tcp_congestion_control", "bbr")
		_ = writeSysctl("net.core.default_qdisc", "fq")
		_ = writeSysctl("net.core.somaxconn", "65535")
	}
	assertFailSafe := func(t *testing.T, marker string) {
		t.Helper()
		warnings := rollbackTuning(context.Background())
		joined := strings.Join(warnings, "\n")
		if !strings.Contains(joined, marker) || !strings.Contains(joined, "未能回退") {
			t.Fatalf("warnings = %v, want a fail-safe naming unrestored keys (%s)", warnings, marker)
		}
		for _, key := range []string{"net.core.somaxconn", "net.ipv4.tcp_fastopen", "net.ipv4.tcp_fin_timeout"} {
			if !strings.Contains(joined, key) {
				t.Fatalf("fail-safe must name %s: %v", key, warnings)
			}
		}
		// The headline defaults are still restored.
		if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "cubic" {
			t.Fatalf("cc = %q, want scripted default cubic", v)
		}
		if _, err := os.Stat(tuneKernelSnapshotPath); !os.IsNotExist(err) {
			t.Fatal("snapshot footprint must be consumed even when unusable")
		}
		if _, err := os.Stat(tuneKernelConfPath); !os.IsNotExist(err) {
			t.Fatal("kernel conf must be removed")
		}
	}

	t.Run("corrupt", func(t *testing.T) {
		fakeProcSys(t, "reno cubic bbr")
		stubTuneCommands(t, map[string]bool{})
		plant(t)
		_ = os.MkdirAll(filepath.Dir(tuneKernelSnapshotPath), 0o755)
		_ = os.WriteFile(tuneKernelSnapshotPath, []byte("{not json"), 0o644)
		assertFailSafe(t, "损坏")
	})

	t.Run("missing", func(t *testing.T) {
		fakeProcSys(t, "reno cubic bbr")
		stubTuneCommands(t, map[string]bool{})
		plant(t)
		assertFailSafe(t, "缺失")
	})

	t.Run("clean no-op", func(t *testing.T) {
		fakeProcSys(t, "reno cubic bbr")
		stubTuneCommands(t, map[string]bool{})
		// Nothing applied, nothing planted: rollback stays silent.
		if warnings := rollbackTuning(context.Background()); len(warnings) != 0 {
			t.Fatalf("clean rollback warnings = %v, want none", warnings)
		}
	})
}

// TestKernelSnapshotCaptureAbort: a key that exists but cannot be
// read at apply time (its node stat fails with a real error, not
// ENOENT) aborts the capture with a loud warning and leaves no
// snapshot behind — a partial snapshot would strand the unread key
// at rollback. A key that does not exist at all is not an abort: it
// lands in the snapshot's Absent list.
func TestKernelSnapshotCaptureAbort(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	oldRead, oldStat := tuneRead, tuneStat
	tuneRead = func(key string) (string, bool) {
		if key == "net.ipv4.tcp_fastopen" {
			return "", false
		}
		return oldRead(key)
	}
	tuneStat = func(name string) (os.FileInfo, error) {
		if strings.HasSuffix(name, "tcp_fastopen") {
			return nil, errors.New("permission denied")
		}
		return oldStat(name)
	}
	t.Cleanup(func() { tuneRead, tuneStat = oldRead, oldStat })
	warnings := captureKernelSysctlSnapshot(kernelTuneSettings(0))
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tcp_fastopen") {
		t.Fatalf("warnings = %v, want one naming the unreadable key", warnings)
	}
	if _, err := os.Stat(tuneKernelSnapshotPath); !os.IsNotExist(err) {
		t.Fatal("aborted capture must leave no snapshot file")
	}
}

// TestKernelSnapshotWriteFailure: when the snapshot cannot be
// persisted, apply reports it loudly (the apply itself proceeds —
// tuning is not blocked by its own bookkeeping).
func TestKernelSnapshotWriteFailure(t *testing.T) {
	dir := fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{})
	// Point the snapshot at a path whose parent is a regular file.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tuneKernelSnapshotPath = filepath.Join(blocker, "snap.json")
	warnings, err := applyKernelTuning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "快照失败") {
		t.Fatalf("warnings = %v, want the snapshot failure named", warnings)
	}
}

// TestKernelSnapshotRestoreWriteFailure: individual keys that
// refuse the restore write are named; the rest still restore.
func TestKernelSnapshotRestoreWriteFailure(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{})
	if _, err := applyKernelTuning(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldWrite := tuneWrite
	tuneWrite = func(key, value string) error {
		if key == "net.ipv4.tcp_ecn" {
			return errors.New("boom")
		}
		return oldWrite(key, value)
	}
	t.Cleanup(func() { tuneWrite = oldWrite })
	warnings := rollbackTuning(context.Background())
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tcp_ecn") {
		t.Fatalf("warnings = %v, want one naming the failed key", warnings)
	}
	if v, _ := readSysctl("net.core.somaxconn"); v == "65535" {
		t.Fatal("other keys must still restore when one write fails")
	}
}

// TestKernelSnapshotAbsentKeys: a key recorded as absent at
// capture (it did not exist then) is reported by name at rollback
// via the snapshot's Absent list instead of being silently dropped.
func TestKernelSnapshotAbsentKeys(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{})
	snap := tuneKernelSnapshot{
		Values: map[string]string{"net.core.somaxconn": "4096"},
		Absent: []string{"net.ipv4.tcp_mtu_probing"},
	}
	raw, _ := json.Marshal(snap)
	_ = os.MkdirAll(filepath.Dir(tuneKernelSnapshotPath), 0o755)
	_ = os.WriteFile(tuneKernelSnapshotPath, raw, 0o644)
	_ = os.MkdirAll(filepath.Dir(tuneKernelConfPath), 0o755)
	_ = os.WriteFile(tuneKernelConfPath, []byte("x"), 0o644)
	warnings := rollbackTuning(context.Background())
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tcp_mtu_probing") {
		t.Fatalf("warnings = %v, want the absent key named", warnings)
	}
	if v, _ := readSysctl("net.core.somaxconn"); v != "4096" {
		t.Fatalf("somaxconn = %q, want snapshot value 4096", v)
	}
}

func TestTuneDispatchRetiredAndActions(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{})
	for _, old := range []string{"TuneApply", "TuneStart", "TuneCancel"} {
		if _, err := handleTuneCommand(old, nil); err == nil || !strings.Contains(err.Error(), "已退役") {
			t.Fatalf("%s: got %v, want retired error", old, err)
		}
	}
	if _, err := handleTuneCommand("Tune", map[string]interface{}{"action": "deep"}); err == nil ||
		!strings.Contains(err.Error(), "未知调优动作") {
		t.Fatalf("unknown action: got %v", err)
	}
	out, err := handleTuneCommand("Tune", map[string]interface{}{"action": "apply-bbr"})
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := out.(tuneStatus); !ok || !st.BBR {
		t.Fatalf("apply-bbr via dispatch: %+v", out)
	}
	out, err = handleTuneCommand("TuneStatus", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.(tuneStatus); !ok {
		t.Fatalf("TuneStatus type: %T", out)
	}
	if _, err := handleTuneCommand("Tune", map[string]interface{}{"action": "rollback"}); err != nil {
		t.Fatal(err)
	}
}

func TestRPSCPUMaskAndEthtoolParse(t *testing.T) {
	for cpus, want := range map[int]string{1: "1", 2: "3", 4: "f", 8: "ff", 0: "1"} {
		if got := rpsCPUMask(cpus); got != want {
			t.Fatalf("rpsCPUMask(%d) = %q, want %q", cpus, got, want)
		}
	}
	out := "Ring parameters for eth0:\nPre-set maximums:\nRX:\t\t4096\nRX Mini:\t0\nTX:\t\t4096\nCurrent hardware settings:\nRX:\t\t256\n"
	if n, ok := parseEthtoolMaxRing(out); !ok || n != 4096 {
		t.Fatalf("parseEthtoolMaxRing = %d, %v", n, ok)
	}
	if _, ok := parseEthtoolMaxRing("no rings here"); ok {
		t.Fatal("garbage must not parse")
	}
	// Mask rendering must agree with strconv for the tested range.
	if got := rpsCPUMask(16); got != strconv.FormatUint((1<<16)-1, 16) {
		t.Fatalf("mask 16 = %q", got)
	}
}

func TestNICExcludeFilter(t *testing.T) {
	for _, name := range []string{"lo", "docker0", "veth12ab", "br-3f2a", "tun0", "wg0", "sit0", "tung3", "any0"} {
		if !tuneNICExcludePattern.MatchString(name) {
			t.Fatalf("%s must be excluded", name)
		}
	}
	for _, name := range []string{"eth0", "ens3", "enp1s0", "bond0"} {
		if tuneNICExcludePattern.MatchString(name) {
			t.Fatalf("%s must be steered", name)
		}
	}
}
