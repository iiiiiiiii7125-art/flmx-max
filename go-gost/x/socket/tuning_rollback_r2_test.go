package socket

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R2-P2-01 / R2-P2-02 regression gates: the TL-02 snapshot used to
// cover only the face-③ sysctl set, captured by apply-kernel
// alone. Two write sets escaped it, so rollback could not restore
// them: face ②'s congestion control / default qdisc (an apply-bbr
// without apply-kernel left rollback writing scripted defaults,
// and an apply-bbr before apply-kernel let the snapshot record
// the tuned fq/bbr as the "originals"), and face ④'s ethtool ring
// sizes (never captured, never restored). The snapshot is now
// captured in full — every sysctl any face writes plus the ring
// sizes — before the first write of whichever face applies first.

func TestParseEthtoolCurrentRings(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		wantRX int
		wantTX int
		wantOK bool
	}{
		{
			name:   "asymmetric current below maxima",
			out:    "Ring parameters for ens5:\nPre-set maximums:\nRX:\t\t2048\nTX:\t\t1024\nCurrent hardware settings:\nRX:\t\t256\nTX:\t\t128\n",
			wantRX: 256, wantTX: 128, wantOK: true,
		},
		{
			name:   "TX line absent keeps RX",
			out:    "Pre-set maximums:\nRX:\t\t512\nCurrent hardware settings:\nRX:\t\t256\n",
			wantRX: 256, wantTX: 0, wantOK: true,
		},
		{
			name:   "maxima are not current values",
			out:    "Pre-set maximums:\nRX:\t\t4096\nTX:\t\t4096\n",
			wantOK: false,
		},
		{
			name:   "garbage",
			out:    "no rings here",
			wantOK: false,
		},
		{
			name:   "unparsable current RX",
			out:    "Current hardware settings:\nRX:\t\tabc\nTX:\t\t128\n",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur, ok := parseEthtoolCurrentRings(tc.out)
			if ok != tc.wantOK || cur.RX != tc.wantRX || cur.TX != tc.wantTX {
				t.Fatalf("parseEthtoolCurrentRings = %+v, %v; want {%d %d}, %v", cur, ok, tc.wantRX, tc.wantTX, tc.wantOK)
			}
		})
	}
}

func TestEthtoolRingRestoreArgs(t *testing.T) {
	if got := strings.Join(ethtoolRingRestoreArgs("eth0", tuneRingSnapshot{RX: 256, TX: 128}), " "); got != "-G eth0 rx 256 tx 128" {
		t.Fatalf("restore args = %q", got)
	}
	// An unrecorded direction is not named, so the driver keeps
	// its current value for it.
	if got := strings.Join(ethtoolRingRestoreArgs("eth0", tuneRingSnapshot{RX: 256}), " "); got != "-G eth0 rx 256" {
		t.Fatalf("restore args without TX = %q", got)
	}
}

// TestApplyBBROnlySnapshotRollback is the R2-P2-01 first half: a
// node that applied ONLY apply-bbr used to roll back to the
// scripted cubic/pfifo_fast defaults, losing the operator's own
// congestion control and qdisc for good. The snapshot apply-bbr
// itself captures now carries them home.
func TestApplyBBROnlySnapshotRollback(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{})
	// Operator values that are neither the tuned values nor the
	// scripted defaults.
	if err := writeSysctl("net.ipv4.tcp_congestion_control", "westwood"); err != nil {
		t.Fatal(err)
	}
	if err := writeSysctl("net.core.default_qdisc", "cake"); err != nil {
		t.Fatal(err)
	}
	warnings, err := applyBBR(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("apply-bbr warnings = %v, want none", warnings)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "bbr" {
		t.Fatalf("live cc after apply = %q", v)
	}
	raw, err := os.ReadFile(tuneKernelSnapshotPath)
	if err != nil {
		t.Fatalf("apply-bbr must leave a snapshot: %v", err)
	}
	var snap tuneKernelSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Values["net.ipv4.tcp_congestion_control"] != "westwood" || snap.Values["net.core.default_qdisc"] != "cake" {
		t.Fatalf("snapshot cc/qdisc = %q/%q, want the operator originals westwood/cake",
			snap.Values["net.ipv4.tcp_congestion_control"], snap.Values["net.core.default_qdisc"])
	}
	if warnings := rollbackTuning(context.Background()); len(warnings) != 0 {
		t.Fatalf("rollback warnings = %v, want none", warnings)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "westwood" {
		t.Fatalf("cc after rollback = %q, want operator value westwood", v)
	}
	if v, _ := readSysctl("net.core.default_qdisc"); v != "cake" {
		t.Fatalf("qdisc after rollback = %q, want operator value cake", v)
	}
}

// TestApplyBBRBeforeKernelSnapshotNotPolluted is the R2-P2-01
// second half: apply-bbr lands first and rewrites cc/qdisc; the
// snapshot apply-kernel used to take afterwards recorded the
// TUNED fq/bbr as the originals, so rollback "restored" the tuned
// state. The first face to apply now takes the snapshot, before
// any of its own writes.
func TestApplyBBRBeforeKernelSnapshotNotPolluted(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{})
	operator := map[string]string{
		"net.ipv4.tcp_congestion_control": "westwood",
		"net.core.default_qdisc":          "cake",
		"net.core.somaxconn":              "8192",
	}
	for k, v := range operator {
		if err := writeSysctl(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := applyBBR(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "bbr" {
		t.Fatalf("live cc after apply-bbr = %q", v)
	}
	if _, err := applyKernelTuning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, _ := readSysctl("net.core.somaxconn"); v != "65535" {
		t.Fatalf("live somaxconn after apply-kernel = %q", v)
	}
	raw, err := os.ReadFile(tuneKernelSnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snap tuneKernelSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	for k, v := range operator {
		if snap.Values[k] != v {
			t.Fatalf("snapshot[%s] = %q, want pristine operator value %q (polluted by the earlier apply-bbr)", k, snap.Values[k], v)
		}
	}
	if warnings := rollbackTuning(context.Background()); len(warnings) != 0 {
		t.Fatalf("rollback warnings = %v, want none", warnings)
	}
	for k, v := range operator {
		if got, _ := readSysctl(k); got != v {
			t.Fatalf("%s after rollback = %q, want %q", k, got, v)
		}
	}
}

// stubEthtoolRings installs a command seam where `ethtool -g`
// serves canned output and every invocation is recorded. Only
// ethtool "exists".
func stubEthtoolRings(t *testing.T, gOut string) *[][]string {
	t.Helper()
	var calls [][]string
	oldCmd, oldLook := tuneCommand, tuneLookPath
	tuneLookPath = func(name string) (string, error) {
		if name == "ethtool" {
			return "/usr/bin/ethtool", nil
		}
		return "", os.ErrNotExist
	}
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "ethtool" && len(args) > 0 && args[0] == "-g" {
			return gOut, nil
		}
		return "", nil
	}
	t.Cleanup(func() { tuneCommand, tuneLookPath = oldCmd, oldLook })
	return &calls
}

// TestNICRingSnapshotAndRollback is the R2-P2-02 gate: apply-nic
// pushes the rings to their preset maxima (runtime-only state),
// and rollback must write the pre-apply sizes back from the
// snapshot — before the fix they stayed at the maxima until the
// next reboot or driver reload.
func TestNICRingSnapshotAndRollback(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	gOut := "Ring parameters for eth0:\nPre-set maximums:\nRX:\t\t4096\nTX:\t\t2048\nCurrent hardware settings:\nRX:\t\t256\nTX:\t\t128\n"
	calls := stubEthtoolRings(t, gOut)
	q := filepath.Join(tuneNetDir, "eth0", "queues", "rx-0")
	if err := os.MkdirAll(q, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(q, "rps_cpus"), []byte("0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(q, "rps_flow_cnt"), []byte("0\n"), 0o644)

	if _, err := applyNICTuning(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The snapshot recorded the pre-apply ring sizes.
	raw, err := os.ReadFile(tuneKernelSnapshotPath)
	if err != nil {
		t.Fatalf("apply-nic must leave a snapshot: %v", err)
	}
	var snap tuneKernelSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if got := snap.Rings["eth0"]; got.RX != 256 || got.TX != 128 {
		t.Fatalf("snapshot rings for eth0 = %+v, want {256 128}", got)
	}
	// Apply pushed the rings to the preset maxima.
	var gCalls []string
	for _, c := range *calls {
		if len(c) > 1 && c[0] == "ethtool" && c[1] == "-G" {
			gCalls = append(gCalls, strings.Join(c, " "))
		}
	}
	if len(gCalls) != 1 || gCalls[0] != "ethtool -G eth0 rx 4096 tx 2048" {
		t.Fatalf("apply -G calls = %v, want the maxima push only", gCalls)
	}
	// Rollback writes the recorded originals back.
	if warnings := rollbackTuning(context.Background()); len(warnings) != 0 {
		t.Fatalf("rollback warnings = %v, want none", warnings)
	}
	gCalls = gCalls[:0]
	for _, c := range *calls {
		if len(c) > 1 && c[0] == "ethtool" && c[1] == "-G" {
			gCalls = append(gCalls, strings.Join(c, " "))
		}
	}
	if len(gCalls) != 2 || gCalls[1] != "ethtool -G eth0 rx 256 tx 128" {
		t.Fatalf("-G calls after rollback = %v, want apply maxima then rollback originals", gCalls)
	}
}

// TestLegacySnapshotRollbackCompat: a snapshot written by an
// older agent — sysctl values only, no rings field, and no
// rps_sock_flow_entries in the values — must still roll back
// cleanly: recorded values restore, the face-④ counter keeps the
// historical reset to 0, and no ring write is attempted from data
// the snapshot does not have.
func TestLegacySnapshotRollbackCompat(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	calls := stubEthtoolRings(t, "Ring parameters for eth0:\nPre-set maximums:\nRX:\t\t4096\nTX:\t\t2048\nCurrent hardware settings:\nRX:\t\t256\nTX:\t\t128\n")
	_ = os.MkdirAll(filepath.Dir(tuneKernelConfPath), 0o755)
	_ = os.WriteFile(tuneKernelConfPath, []byte("x"), 0o644)
	legacy := `{"values":{"net.core.somaxconn":"4096","net.ipv4.tcp_congestion_control":"cubic","net.core.default_qdisc":"fq_codel"}}`
	_ = os.MkdirAll(filepath.Dir(tuneKernelSnapshotPath), 0o755)
	if err := os.WriteFile(tuneKernelSnapshotPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	// Live tuned state.
	_ = writeSysctl("net.core.somaxconn", "65535")
	_ = writeSysctl("net.ipv4.tcp_congestion_control", "bbr")
	_ = writeSysctl("net.core.default_qdisc", "fq")
	_ = writeSysctl("net.core.rps_sock_flow_entries", "32768")

	if warnings := rollbackTuning(context.Background()); len(warnings) != 0 {
		t.Fatalf("legacy rollback warnings = %v, want none", warnings)
	}
	if v, _ := readSysctl("net.core.somaxconn"); v != "4096" {
		t.Fatalf("somaxconn = %q, want legacy snapshot value 4096", v)
	}
	if v, _ := readSysctl("net.ipv4.tcp_congestion_control"); v != "cubic" {
		t.Fatalf("cc = %q, want legacy snapshot value cubic", v)
	}
	if v, _ := readSysctl("net.core.rps_sock_flow_entries"); v != "0" {
		t.Fatalf("rps_sock_flow_entries = %q, want the historical reset to 0 for a legacy snapshot", v)
	}
	for _, c := range *calls {
		t.Fatalf("no ethtool call may come out of a legacy snapshot, got %v", c)
	}
	if _, err := os.Stat(tuneKernelSnapshotPath); !os.IsNotExist(err) {
		t.Fatal("legacy snapshot must be consumed by rollback")
	}
}
