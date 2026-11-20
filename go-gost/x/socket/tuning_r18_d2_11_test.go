package socket

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// R18-D2-11: TuneStatus's BBR/NIC faces were live-value inference:
// another tool setting tcp_congestion_control=bbr or
// rps_sock_flow_entries=32768 made the panel report "tuned" on a
// node the panel never tuned. Faces are now footprint + live: the
// panel's own conf/script must be in place AND the value in effect.

func writeTuneTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestR18D211TuneStatusIgnoresForeignLiveValues(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	// Foreign live values, no panel footprint anywhere.
	writeTuneTestFile(t, sysctlPath("net.ipv4.tcp_congestion_control"), "bbr\n")
	writeTuneTestFile(t, sysctlPath("net.core.rps_sock_flow_entries"), "32768\n")
	st := readTuneStatus()
	if st.BBR {
		t.Fatalf("BBR must be false with only a foreign live value: %+v", st)
	}
	if st.NIC {
		t.Fatalf("NIC must be false with only a foreign live value: %+v", st)
	}
	// Headlines stay live: the operator can still see the values.
	if st.CongestionControl != "bbr" {
		t.Fatalf("congestion headline = %q, want bbr", st.CongestionControl)
	}
	if st.RPSSockFlowEntries != 32768 {
		t.Fatalf("rps headline = %d, want 32768", st.RPSSockFlowEntries)
	}
	if st.Faces.BBR || st.Faces.NIC {
		t.Fatalf("canonical faces must mirror the flat faces: %+v", st.Faces)
	}
}

func TestR18D211TuneStatusReportsPanelApply(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubTuneCommands(t, map[string]bool{}) // no systemctl: script still lands, unit does not
	if _, err := applyBBR(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := applyNICTuning(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := readTuneStatus()
	if !st.BBR {
		t.Fatalf("BBR must be true after the panel applied it: %+v", st)
	}
	if !st.NIC {
		t.Fatalf("NIC must be true after the panel applied it: %+v", st)
	}
	// The footprint lands even without systemd: the RPS script is
	// the record of the applied steering on container nodes too.
	if _, err := os.Stat(tuneRPSScriptPath); err != nil {
		t.Fatalf("RPS script must exist after NIC apply without systemd: %v", err)
	}
}

func TestR18D211TuneStatusFootprintWithoutLiveValueIsNotTuned(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	// Panel footprint in place, but the kernel cannot honor it
	// (live values still cubic/0): not in effect, not "tuned".
	writeTuneTestFile(t, tuneBBRConfPath, tuneBBRConfBody)
	writeTuneTestFile(t, tuneRPSScriptPath, tuneRPSScript([]string{"eth0"}, "f"))
	st := readTuneStatus()
	if st.BBR {
		t.Fatalf("BBR must be false when the footprint is present but bbr is not in effect: %+v", st)
	}
	if st.NIC {
		t.Fatalf("NIC must be false when the footprint is present but 32768 is not in effect: %+v", st)
	}
}

func TestR18D211TuneStatusKernelFaceBBRCounts(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	// Kernel face only: no 10-bbr.conf, but the bbr line sits in
	// the kernel conf the panel wrote, and bbr is live.
	writeTuneTestFile(t, tuneKernelConfPath, "# Written by the FLMX panel (节点调优); remove via the panel to restore.\nnet.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n")
	writeTuneTestFile(t, sysctlPath("net.ipv4.tcp_congestion_control"), "bbr\n")
	if _, err := os.Stat(tuneBBRConfPath); !os.IsNotExist(err) {
		t.Fatalf("10-bbr.conf must be absent in this case")
	}
	if st := readTuneStatus(); !st.BBR {
		t.Fatalf("BBR must be true when the kernel conf carries the panel's bbr line and bbr is live: %+v", st)
	}
}
