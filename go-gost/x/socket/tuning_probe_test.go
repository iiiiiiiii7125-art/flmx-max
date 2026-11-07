package socket

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Probe seams are restored after each test so later tests (and the
// real sampler) are unaffected.
func stubProbeSeams(t *testing.T, rtts []uint32, rttErr error) {
	t.Helper()
	oldRTT, oldRef := tuneProbeTCPRTTs, tuneProbeReferenceRT
	tuneProbeTCPRTTs = func() (tcpRTTSamples, error) { return tcpRTTSamples{RTTs: rtts}, rttErr }
	tuneProbeReferenceRT = func(string) (probeSeriesStats, error) {
		return probeSeriesStats{}, errors.New("no reference")
	}
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
	tuneProbeReferenceRT = func(string) (probeSeriesStats, error) {
		return probeSeriesStats{Sent: 3, Received: 3, RTTsMs: []int{42, 42, 42}}, nil
	}

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

func TestProbeSeriesStatsMath(t *testing.T) {
	st := probeSeriesStats{Sent: 10, Received: 8, RTTsMs: []int{10, 14, 12, 20, 11, 13, 12, 40}}
	if med, ok := st.medianMs(); !ok || med != 12 { // sorted: 10 11 12 12 13 14 20 40 -> (12+13)/2
		t.Errorf("median: got (%d,%v) want (12,true)", med, ok)
	}
	if rate, ok := st.lossRate(); !ok || rate != 0.2 {
		t.Errorf("loss: got (%v,%v) want (0.2,true)", rate, ok)
	}
	// consecutive diffs in send order: 4,2,8,9,2,1,28 -> 54/7
	if jit, ok := st.jitterMs(); !ok || jit != float64(54)/float64(7) {
		t.Errorf("jitter: got (%v,%v) want (%v,true)", jit, ok, float64(54)/float64(7))
	}
	if mn, p95, mx, ok := st.distributionMs(); !ok || mn != 10 || p95 != 40 || mx != 40 {
		t.Errorf("dist: got (%d,%d,%d,%v) want (10,40,40,true)", mn, p95, mx, ok)
	}
	// Total loss: rate is a real 1, everything else unmeasurable.
	dead := probeSeriesStats{Sent: 10}
	if rate, ok := dead.lossRate(); !ok || rate != 1 {
		t.Errorf("dead loss: got (%v,%v) want (1,true)", rate, ok)
	}
	if _, ok := dead.medianMs(); ok {
		t.Error("dead median must be unavailable")
	}
	if _, ok := dead.jitterMs(); ok {
		t.Error("dead jitter must be unavailable")
	}
	// Single success: median/min/max exist, jitter does not.
	one := probeSeriesStats{Sent: 10, Received: 1, RTTsMs: []int{33}}
	if med, ok := one.medianMs(); !ok || med != 33 {
		t.Errorf("one median: got (%d,%v) want (33,true)", med, ok)
	}
	if _, ok := one.jitterMs(); ok {
		t.Error("single-sample jitter must be unavailable")
	}
	// Never ran: even the loss rate is unavailable.
	if _, ok := (probeSeriesStats{}).lossRate(); ok {
		t.Error("empty series loss must be unavailable")
	}
}

func TestTuneProbeRTTDistributionMs(t *testing.T) {
	// 20 samples 1..20 ms: min 1, max 20, p95 = rank ceil(.95*20)=19.
	var samples []uint32
	for ms := 1; ms <= 20; ms++ {
		samples = append(samples, uint32(ms*1000))
	}
	mn, p95, mx, ok := tuneProbeRTTDistributionMs(samples)
	if !ok || mn != 1 || p95 != 19 || mx != 20 {
		t.Errorf("got (%d,%d,%d,%v) want (1,19,20,true)", mn, p95, mx, ok)
	}
	if _, _, _, ok := tuneProbeRTTDistributionMs([]uint32{0, 0}); ok {
		t.Error("zero-only samples must be unavailable")
	}
	// Sub-ms samples floor at 1 ms like the median.
	if mn, _, _, _ := tuneProbeRTTDistributionMs([]uint32{400}); mn != 1 {
		t.Errorf("sub-ms min: got %d want 1", mn)
	}
}

func TestTuneProbeReferenceSeriesFields(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, []uint32{10000, 30000, 20000, 90000}, nil)
	tuneProbeReferenceRT = func(string) (probeSeriesStats, error) {
		return probeSeriesStats{Sent: 10, Received: 8, RTTsMs: []int{10, 14, 12, 20, 11, 13, 12, 40}}, nil
	}
	out, err := handleTuneCommand("TuneProbe", map[string]interface{}{"referenceHost": "panel.example:443"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(tuneProbeResult)
	if res.PanelRTTSent != 10 || res.PanelRTTReceived != 8 {
		t.Fatalf("series counts: %+v", res)
	}
	if res.PanelRTTLossRate == nil || *res.PanelRTTLossRate != 0.2 {
		t.Fatalf("loss rate: %+v", res)
	}
	if res.PanelRTTMs == nil || *res.PanelRTTMs != 12 || !res.PanelRTTAvail {
		t.Fatalf("series median: %+v", res)
	}
	if res.PanelRTTMinMs == nil || *res.PanelRTTMinMs != 10 ||
		res.PanelRTTP95Ms == nil || *res.PanelRTTP95Ms != 40 ||
		res.PanelRTTMaxMs == nil || *res.PanelRTTMaxMs != 40 {
		t.Fatalf("series distribution: %+v", res)
	}
	if res.PanelRTTJitterMs == nil || *res.PanelRTTJitterMs != float64(54)/float64(7) {
		t.Fatalf("series jitter: %+v", res)
	}
	// sock_diag leg distribution from the same samples as the median
	// (10/30/20/90 ms): min 10, p95 90, max 90.
	if res.RTTMinMs == nil || *res.RTTMinMs != 10 ||
		res.RTTP95Ms == nil || *res.RTTP95Ms != 90 ||
		res.RTTMaxMs == nil || *res.RTTMaxMs != 90 {
		t.Fatalf("sock_diag distribution: %+v", res)
	}
}

func TestTuneProbeReferenceTotalLoss(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	stubProbeSeams(t, []uint32{80000}, nil)
	tuneProbeReferenceRT = func(string) (probeSeriesStats, error) {
		return probeSeriesStats{Sent: 10, Received: 0}, nil
	}
	out, err := handleTuneCommand("TuneProbe", map[string]interface{}{"referenceHost": "panel.example:443"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(tuneProbeResult)
	// Total loss is an explicit 100% measurement; latency / jitter /
	// distribution stay absent rather than surfacing as 0 values.
	if res.PanelRTTLossRate == nil || *res.PanelRTTLossRate != 1 {
		t.Fatalf("total-loss rate: %+v", res)
	}
	if res.PanelRTTAvail || res.PanelRTTMs != nil || res.PanelRTTJitterMs != nil ||
		res.PanelRTTMinMs != nil || res.PanelRTTP95Ms != nil || res.PanelRTTMaxMs != nil {
		t.Fatalf("total-loss fields must stay absent: %+v", res)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "丢包 100%") {
			found = true
		}
	}
	if !found {
		t.Fatalf("total-loss warning missing: %+v", res.Warnings)
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
