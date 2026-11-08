package socket

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// FIX-T8: RX and TX preset maxima differ per driver (ens5: RX 2048 /
// TX 1024). A single shared ring value makes `ethtool -G rx <v> tx
// <v>` fail wholesale when <v> exceeds the smaller maximum, and the
// failure used to be swallowed, so apply-nic reported success with
// the rings untouched.

func TestParseEthtoolMaxRings(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		wantRX int
		wantTX int
		wantOK bool
	}{
		{
			name:   "asymmetric ens5 maxima",
			out:    "Ring parameters for ens5:\nPre-set maximums:\nRX:\t\t2048\nRX Mini:\t0\nRX Jumbo:\t0\nTX:\t\t1024\nCurrent hardware settings:\nRX:\t\t256\nTX:\t\t256\n",
			wantRX: 2048, wantTX: 1024, wantOK: true,
		},
		{
			name:   "symmetric maxima",
			out:    "Ring parameters for eth0:\nPre-set maximums:\nRX:\t\t4096\nTX:\t\t4096\nCurrent hardware settings:\nRX:\t\t256\n",
			wantRX: 4096, wantTX: 4096, wantOK: true,
		},
		{
			name:   "TX line absent keeps RX",
			out:    "Pre-set maximums:\nRX:\t\t512\nCurrent hardware settings:\nRX:\t\t256\n",
			wantRX: 512, wantTX: 0, wantOK: true,
		},
		{
			name:   "garbage",
			out:    "no rings here",
			wantOK: false,
		},
		{
			name:   "unparsable RX",
			out:    "Pre-set maximums:\nRX:\t\tabc\nTX:\t\t1024\nCurrent hardware settings:\nRX:\t\t256\n",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			max, ok := parseEthtoolMaxRings(tc.out)
			if ok != tc.wantOK || max.RX != tc.wantRX || max.TX != tc.wantTX {
				t.Fatalf("parseEthtoolMaxRings = %+v, %v; want {%d %d}, %v", max, ok, tc.wantRX, tc.wantTX, tc.wantOK)
			}
		})
	}
}

func TestEthtoolRingArgs(t *testing.T) {
	cases := []struct {
		name string
		max  ethtoolRingMax
		want string
	}{
		{"asymmetric ens5", ethtoolRingMax{RX: 2048, TX: 1024}, "-G ens5 rx 2048 tx 1024"},
		{"symmetric", ethtoolRingMax{RX: 4096, TX: 4096}, "-G eth0 rx 4096 tx 4096"},
		{"missing maxima fall back per direction", ethtoolRingMax{}, "-G eth0 rx 1024 tx 1024"},
		{"TX missing falls back for TX only", ethtoolRingMax{RX: 2048}, "-G eth0 rx 2048 tx 1024"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iface := "eth0"
			if strings.Contains(tc.want, "ens5") {
				iface = "ens5"
			}
			if got := strings.Join(ethtoolRingArgs(iface, tc.max), " "); got != tc.want {
				t.Fatalf("ethtoolRingArgs = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyNICSteeringRingFailureWarns(t *testing.T) {
	fakeProcSys(t, "reno cubic bbr")
	oldCmd, oldLook := tuneCommand, tuneLookPath
	var calls [][]string
	tuneLookPath = func(name string) (string, error) {
		if name == "ethtool" {
			return "/usr/bin/ethtool", nil
		}
		return "", os.ErrNotExist
	}
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "ethtool" && len(args) > 0 && args[0] == "-g" {
			return "Ring parameters for ens5:\nPre-set maximums:\nRX:\t\t2048\nTX:\t\t1024\nCurrent hardware settings:\nRX:\t\t256\nTX:\t\t256\n", nil
		}
		if name == "ethtool" && len(args) > 0 && args[0] == "-G" {
			// Mirrors the live ens5 evidence: TX 2048 exceeds the TX
			// maximum, so the whole -G is rejected (rc=81).
			return "", errors.New("ethtool: TX ring exceeds maximum (rc=81)")
		}
		return "", nil
	}
	t.Cleanup(func() { tuneCommand, tuneLookPath = oldCmd, oldLook })

	warnings := applyNICSteering(context.Background(), "ens5", "f")
	if len(warnings) == 0 {
		t.Fatal("rejected ethtool -G must produce a warning, got none")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "rx 2048 tx 1024") || !strings.Contains(joined, "ens5") {
		t.Fatalf("warnings = %q, want them to name ens5 with rx 2048 tx 1024", joined)
	}
	var gCall []string
	for _, c := range calls {
		if len(c) > 1 && c[1] == "-G" {
			gCall = c
		}
	}
	if got := strings.Join(gCall, " "); got != "ethtool -G ens5 rx 2048 tx 1024" {
		t.Fatalf("-G call = %q, want %q", got, "ethtool -G ens5 rx 2048 tx 1024")
	}
}
