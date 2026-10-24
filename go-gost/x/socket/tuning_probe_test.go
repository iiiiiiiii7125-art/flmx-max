package socket

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Probe seams are restored after each test so later tests (and the
// real sampler) are unaffected.
func stubProbeSeams(t *testing.T, rtts []uint32, rttErr error) {
	t.Helper()
	oldRTT, oldSpeed, oldRef := tuneProbeTCPRTTs, tuneProbeSpeedtest, tuneProbeReferenceRT
	tuneProbeTCPRTTs = func() (tcpRTTSamples, error) { return tcpRTTSamples{RTTs: rtts}, rttErr }
	tuneProbeSpeedtest = func(context.Context, string) (int64, time.Duration, error) {
		return 0, 0, errors.New("speedtest must be stubbed per test")
	}
	tuneProbeReferenceRT = func(string) (int, error) { return 0, errors.New("no reference") }
	t.Cleanup(func() {
		tuneProbeTCPRTTs, tuneProbeSpeedtest, tuneProbeReferenceRT = oldRTT, oldSpeed, oldRef
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

// The no-write guard: a probe must leave every surface Apply writes
// byte-identical (sysctl file, backup file, params file, plus a live
// sysctl value), and must not create files that did not exist.
func TestTuneProbeDoesNotWrite(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, []uint32{120000, 130000, 110000}, nil)
	// Seed all three write surfaces, as an already-tuned node has.
	_ = os.MkdirAll(filepath.Dir(tuneConfPath), 0o755)
	_ = os.WriteFile(tuneConfPath, []byte("net.core.rmem_max = 39597152\n"), 0o644)
	_ = os.MkdirAll(filepath.Dir(tuneBackupPath), 0o755)
	_ = os.WriteFile(tuneBackupPath, []byte(`{"net.core.rmem_max":"0"}`), 0o600)
	_ = os.WriteFile(tuneParamsPath, []byte(`{"bandwidthMbps":100,"rttMs":50}`), 0o644)
	beforeConf, confExists := snapshotFile(t, tuneConfPath)
	beforeBackup, _ := snapshotFile(t, tuneBackupPath)
	beforeParams, _ := snapshotFile(t, tuneParamsPath)
	beforeBBR, bbrExists := snapshotFile(t, tuneBBRModule)
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
	if res.BandwidthSrc != "not_requested" || res.BandwidthAvail {
		t.Fatalf("bandwidth must default to not-run: %+v", res)
	}
	afterConf, _ := snapshotFile(t, tuneConfPath)
	afterBackup, _ := snapshotFile(t, tuneBackupPath)
	afterParams, _ := snapshotFile(t, tuneParamsPath)
	afterBBR, bbrNow := snapshotFile(t, tuneBBRModule)
	afterRmem, _ := readSysctl("net.core.rmem_max")
	if !confExists || string(afterConf) != string(beforeConf) {
		t.Fatalf("conf changed: %q -> %q", beforeConf, afterConf)
	}
	if string(afterBackup) != string(beforeBackup) || string(afterParams) != string(beforeParams) {
		t.Fatalf("backup/params changed")
	}
	if bbrExists != bbrNow || string(afterBBR) != string(beforeBBR) {
		t.Fatalf("bbr module file changed")
	}
	if afterRmem != beforeRmem {
		t.Fatalf("live sysctl changed: %q -> %q", beforeRmem, afterRmem)
	}
}

func TestTuneProbeFallbackOnFailure(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, nil, errors.New("netlink denied"))
	// Speed test requested but no URL: bandwidth leg must degrade,
	// not error the whole probe.
	out, err := handleTuneCommand("TuneProbe", map[string]interface{}{"measureBandwidth": true})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(tuneProbeResult)
	if res.Suggested != defaultTuneParams {
		t.Fatalf("fallback suggested: %+v", res.Suggested)
	}
	if res.RTTAvailable || res.BandwidthAvail || len(res.Warnings) < 2 {
		t.Fatalf("fallback legs: %+v", res)
	}
}

func TestTuneProbeSpeedtestOptionalAndReference(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, []uint32{80000}, nil)
	speedCalls := 0
	tuneProbeSpeedtest = func(context.Context, string) (int64, time.Duration, error) {
		speedCalls++
		return 12500000, time.Second, nil // 12.5 MB in 1s = 100 Mbps
	}
	tuneProbeReferenceRT = func(string) (int, error) { return 42, nil }

	// Not requested: speed test must not run even if a URL is present.
	out, err := handleTuneCommand("TuneProbe", map[string]interface{}{"speedtestURL": "https://example.invalid/10mb"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(tuneProbeResult)
	if speedCalls != 0 || res.BandwidthSrc != "not_requested" || res.Suggested.BandwidthMbps != 1000 {
		t.Fatalf("speed test ran unrequested: calls=%d %+v", speedCalls, res)
	}
	if res.PanelRTTMs != nil || res.PanelRTTAvail {
		t.Fatalf("reference must be absent when not requested: %+v", res)
	}

	// Explicitly requested (contract field withSpeed): speed test runs, reference runs.
	out, err = handleTuneCommand("TuneProbe", map[string]interface{}{
		"withSpeed": true, "speedtestURL": "https://example.invalid/10mb", "referenceHost": "panel.example:443",
	})
	if err != nil {
		t.Fatal(err)
	}
	res = out.(tuneProbeResult)
	if speedCalls != 1 || res.BandwidthSrc != "speedtest" || res.BandwidthMbps == nil || *res.BandwidthMbps != 100 {
		t.Fatalf("speed leg: calls=%d %+v", speedCalls, res)
	}
	if res.RTTMedianMs == nil || *res.RTTMedianMs != 80 {
		t.Fatalf("contract rttMedianMs: %+v", res)
	}
	if res.PanelRTTMs == nil || *res.PanelRTTMs != 42 || !res.PanelRTTAvail {
		t.Fatalf("reference leg: %+v", res)
	}
	if res.Suggested != (tuneParams{BandwidthMbps: 100, RTTMs: 80}) {
		t.Fatalf("suggested: %+v", res.Suggested)
	}
}

func TestTuneProbeRejectsBadSpeedtestURL(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, []uint32{80000}, nil)
	if _, err := handleTuneCommand("TuneProbe", map[string]interface{}{
		"withSpeed": true, "speedtestURL": "ftp://example.invalid/f",
	}); err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("expected http(s) error, got %v", err)
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
