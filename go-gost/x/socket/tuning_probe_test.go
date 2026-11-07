package socket

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Probe seams are restored after each test so later tests (and the
// real sampler) are unaffected.
func stubProbeSeams(t *testing.T, rtts []uint32, rttErr error) {
	t.Helper()
	oldRTT, oldRef := tuneProbeTCPRTTs, tuneProbeReferenceRT
	tuneProbeTCPRTTs = func() (tcpRTTSamples, error) { return tcpRTTSamples{RTTs: rtts}, rttErr }
	tuneProbeReferenceRT = func(string) (int, error) { return 0, errors.New("no reference") }
	t.Cleanup(func() {
		tuneProbeTCPRTTs, tuneProbeReferenceRT = oldRTT, oldRef
	})
}

func TestTuneProbeMedianRTTMs(t *testing.T) {
	for _, c := range []struct {
		samples []uint32
		wantMs  int
		wantN   int
		wantOK  bool
	}{
		{[]uint32{1000, 2000, 3000}, 2, 3, true},
		{[]uint32{1000, 2000}, 2, 2, true},     // even: mean 1500µs, half-up to 2ms
		{[]uint32{500}, 1, 1, true},            // sub-ms floors at 1, not 0
		{[]uint32{0, 0, 120000}, 120, 1, true}, // unmeasured sockets dropped
		{[]uint32{}, 0, 0, false},
		{[]uint32{0, 0}, 0, 0, false},
	} {
		ms, n, ok := tuneProbeMedianRTTMs(c.samples)
		if ms != c.wantMs || n != c.wantN || ok != c.wantOK {
			t.Errorf("%v: got (%d,%d,%v) want (%d,%d,%v)", c.samples, ms, n, ok, c.wantMs, c.wantN, c.wantOK)
		}
	}
}

func snapshotFile(t *testing.T, path string) (data []byte, exists bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false
		}
		t.Fatal(err)
	}
	return b, true
}

// The no-write guard: a probe must leave every surface the tuning
// apply writes byte-identical (kernel conf, BBR conf, limits conf,
// plus a live sysctl value), and must not create files that did not
// exist (the gai.conf backup). Adapted to the second-generation
// tuning model: the seeded surfaces are the new model's files; the
// assertion is unchanged.
func TestTuneProbeDoesNotWrite(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, []uint32{120000, 130000, 110000}, nil)
	// Seed the write surfaces, as an already-tuned node has.
	_ = os.MkdirAll(filepath.Dir(tuneKernelConfPath), 0o755)
	_ = os.WriteFile(tuneKernelConfPath, []byte("net.core.rmem_max = 39597152\n"), 0o644)
	_ = os.MkdirAll(filepath.Dir(tuneBBRConfPath), 0o755)
	_ = os.WriteFile(tuneBBRConfPath, []byte(tuneBBRConfBody), 0o644)
	_ = os.MkdirAll(filepath.Dir(tuneLimitsConfPath), 0o755)
	_ = os.WriteFile(tuneLimitsConfPath, []byte(tuneLimitsConfBody), 0o644)
	beforeConf, confExists := snapshotFile(t, tuneKernelConfPath)
	beforeBBR, bbrExists := snapshotFile(t, tuneBBRConfPath)
	beforeLimits, _ := snapshotFile(t, tuneLimitsConfPath)
	_, gaiBakExists := snapshotFile(t, tuneGaiBackupPath)
	beforeRmem, _ := readSysctl("net.core.rmem_max")

	out, err := handleTuneCommand("TuneProbe", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(tuneProbeResult)
	if res.Suggested != (tuneParams{BandwidthMbps: 1000, RTTMs: 120}) {
		t.Fatalf("suggested: %+v", res.Suggested)
	}
	if !res.RTTAvailable || res.RTTSource != "tcp_info_median" || res.RTTSamples != 3 {
		t.Fatalf("rtt leg: %+v", res)
	}
	afterConf, _ := snapshotFile(t, tuneKernelConfPath)
	afterBBR, bbrNow := snapshotFile(t, tuneBBRConfPath)
	afterLimits, _ := snapshotFile(t, tuneLimitsConfPath)
	_, gaiBakNow := snapshotFile(t, tuneGaiBackupPath)
	afterRmem, _ := readSysctl("net.core.rmem_max")
	if !confExists || string(afterConf) != string(beforeConf) {
		t.Fatalf("conf changed: %q -> %q", beforeConf, afterConf)
	}
	if bbrExists != bbrNow || string(afterBBR) != string(beforeBBR) {
		t.Fatalf("bbr conf changed")
	}
	if string(afterLimits) != string(beforeLimits) {
		t.Fatalf("limits conf changed")
	}
	if gaiBakExists != gaiBakNow {
		t.Fatalf("gai backup existence changed: %v -> %v", gaiBakExists, gaiBakNow)
	}
	if afterRmem != beforeRmem {
		t.Fatalf("live sysctl changed: %q -> %q", beforeRmem, afterRmem)
	}
}

func TestTuneProbeFallbackOnFailure(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, nil, errors.New("netlink denied"))
	out, err := handleTuneCommand("TuneProbe", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(tuneProbeResult)
	if res.Suggested != defaultTuneParams {
		t.Fatalf("fallback suggested: %+v", res.Suggested)
	}
	if res.RTTAvailable || len(res.Warnings) < 1 {
		t.Fatalf("fallback legs: %+v", res)
	}
}

func TestTuneProbeReferenceLeg(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, []uint32{80000}, nil)
	tuneProbeReferenceRT = func(string) (int, error) { return 42, nil }

	// No reference host: the leg stays absent.
	out, err := handleTuneCommand("TuneProbe", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(tuneProbeResult)
	if res.PanelRTTMs != nil || res.PanelRTTAvail {
		t.Fatalf("reference must be absent when not requested: %+v", res)
	}
	if res.Suggested != (tuneParams{BandwidthMbps: 1000, RTTMs: 80}) {
		t.Fatalf("suggested: %+v", res.Suggested)
	}

	// Explicit reference host: the leg runs and the RTT contract
	// field is populated.
	out, err = handleTuneCommand("TuneProbe", map[string]interface{}{
		"referenceHost": "panel.example:443",
	})
	if err != nil {
		t.Fatal(err)
	}
	res = out.(tuneProbeResult)
	if res.RTTMedianMs == nil || *res.RTTMedianMs != 80 {
		t.Fatalf("contract rttMedianMs: %+v", res)
	}
	if res.PanelRTTMs == nil || *res.PanelRTTMs != 42 || !res.PanelRTTAvail {
		t.Fatalf("reference leg: %+v", res)
	}
	if res.Suggested != (tuneParams{BandwidthMbps: 1000, RTTMs: 80}) {
		t.Fatalf("suggested: %+v", res.Suggested)
	}
}

// panelRemoteHost with no live connection must be empty, so the
// router skips injection and panelRttMs stays contract-optional
// (absent) instead of pointing at a bogus host.
func TestPanelRemoteHostDisconnected(t *testing.T) {
	r := &WebSocketReporter{}
	if got := r.panelRemoteHost(); got != "" {
		t.Fatalf("disconnected reporter: %q", got)
	}
}
