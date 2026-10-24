package socket

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
)

// R14 F-04 / 148-P3-5: a single-family sock_diag failure was silently
// dropped — sockDiagTCPRTTs returned (samples, nil) and probeTune had no
// way to know the median covered only the surviving family. The probe
// result must carry a warning naming the failed family (surfaced in the
// panel's fallback display), and the failure must be logged.
func TestR14F06F04SingleFamilyFailureWarns(t *testing.T) {
	old := tuneProbeTCPRTTs
	defer func() { tuneProbeTCPRTTs = old }()

	stub := func(rtts []uint32, v4err, v6err error) {
		tuneProbeTCPRTTs = func() (tcpRTTSamples, error) {
			return tcpRTTSamples{RTTs: rtts, FamilyErrors: [2]error{v4err, v6err}}, nil
		}
	}
	containsWarning := func(t *testing.T, warnings []string, want ...string) {
		t.Helper()
		joined := strings.Join(warnings, "\n")
		for _, w := range want {
			if !strings.Contains(joined, w) {
				t.Fatalf("warnings %q must contain %q", joined, w)
			}
		}
	}

	// IPv6 dump fails, IPv4 survives: median still from IPv4 samples,
	// warning + log must name IPv6 and say the median is IPv4-only.
	stub([]uint32{100000, 120000, 110000}, nil, errors.New("v6 dump failed"))
	var buf bytes.Buffer
	log.SetOutput(&buf)
	res, err := probeTune(tuneProbeRequest{})
	log.SetOutput(os.Stderr)
	if err != nil {
		t.Fatalf("probeTune: %v", err)
	}
	if !res.RTTAvailable || res.RTTMedianMs == nil || *res.RTTMedianMs != 110 {
		t.Fatalf("median must come from surviving IPv4 samples: %+v", res)
	}
	containsWarning(t, res.Warnings, "IPv6", "仅基于 IPv4")
	if !strings.Contains(buf.String(), "IPv6") {
		t.Fatalf("single-family failure must be logged, got log %q", buf.String())
	}

	// Mirror leg: IPv4 fails, IPv6 survives.
	stub([]uint32{100000, 120000, 110000}, errors.New("v4 dump failed"), nil)
	res, err = probeTune(tuneProbeRequest{})
	if err != nil {
		t.Fatalf("probeTune: %v", err)
	}
	if !res.RTTAvailable || res.RTTMedianMs == nil || *res.RTTMedianMs != 110 {
		t.Fatalf("median must come from surviving IPv6 samples: %+v", res)
	}
	containsWarning(t, res.Warnings, "IPv4", "仅基于 IPv6")

	// Both families fine: no family warning at all.
	stub([]uint32{100000, 120000, 110000}, nil, nil)
	res, err = probeTune(tuneProbeRequest{})
	if err != nil {
		t.Fatalf("probeTune: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "仅基于") {
			t.Fatalf("both families fine, must not warn single-family: %q", res.Warnings)
		}
	}

	// Both families fail: the old whole-leg fallback warning stays.
	tuneProbeTCPRTTs = func() (tcpRTTSamples, error) {
		return tcpRTTSamples{}, errors.New("sock_diag 取数失败 (v4: boom; v6: boom)")
	}
	res, err = probeTune(tuneProbeRequest{})
	if err != nil {
		t.Fatalf("probeTune: %v", err)
	}
	if res.RTTAvailable || res.RTTMedianMs != nil {
		t.Fatalf("both families failed, RTT must be unavailable: %+v", res)
	}
	containsWarning(t, res.Warnings, "sock_diag 取数失败")
}
