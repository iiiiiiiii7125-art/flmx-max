package kernel

// R3-WDG-01: the shared fetch primitive carries a no-progress
// watchdog (fetchStallWindow, production value = the shared
// download.StallWindow 45s). A peer that answers 200 and then
// stalls or trickles must fail the attempt fast — with
// download.ErrNoProgress — so the mirror/direct fallback leg and,
// for geodata, the rest of the round still have budget. These
// tests shrink the window to hundreds of milliseconds.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

func shrinkFetchStallWindow(t *testing.T, window time.Duration) {
	t.Helper()
	old := fetchStallWindow
	fetchStallWindow = window
	t.Cleanup(func() { fetchStallWindow = old })
}

// stallServer answers 200 with a small first chunk, then goes
// silent until the client goes away.
func stallServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
	}))
}

func TestR3WDGFetchStallFailsFast(t *testing.T) {
	shrinkFetchStallWindow(t, 300*time.Millisecond)
	srv := stallServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var buf bytes.Buffer
	start := time.Now()
	err := fetch(ctx, srv.URL+"/payload", 1<<20, &buf)
	elapsed := time.Since(start)
	if !errors.Is(err, download.ErrNoProgress) {
		t.Fatalf("stalled fetch error = %v, want download.ErrNoProgress", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("stalled fetch took %v; the watchdog must fail it in ~300ms, not at the outer deadline", elapsed)
	}
}

// A slow but steadily progressing body is NOT a stall: chunks
// arriving inside the window must complete normally.
func TestR3WDGFetchSlowProgressCompletes(t *testing.T) {
	shrinkFetchStallWindow(t, 800*time.Millisecond)
	payload := bytes.Repeat([]byte("abcd"), 1024) // 4KB in 8 chunks
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for off := 0; off < len(payload); off += 512 {
			end := off + 512
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := w.Write(payload[off:end]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(150 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var buf bytes.Buffer
	if err := fetch(ctx, srv.URL+"/payload", 1<<20, &buf); err != nil {
		t.Fatalf("slow-but-progressing fetch must succeed, got: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Fatalf("body mismatch: got %d bytes, want %d", buf.Len(), len(payload))
	}
}

// Mirror leg stalls after 200 -> watchdog fails it -> the next
// route of the plan (direct) succeeds with the real content.
// KU4 (1.7.7): the old fetchWithMirrorFallback two-try became
// fetchRanked over an explicit route plan; the stall caliber is
// unchanged.
func TestR3WDGFetchRankedStallFallsBackToDirect(t *testing.T) {
	shrinkFetchStallWindow(t, 300*time.Millisecond)
	payload := []byte("kernel-bytes-via-direct")
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer direct.Close()
	mirror := stallServer()
	defer mirror.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var buf bytes.Buffer
	start := time.Now()
	plan := download.NewStaticPlan([]download.Route{{Prefix: mirror.URL}, {}})
	_, err := fetchRanked(ctx, plan, direct.URL+"/file", 1<<20, nil, &buf)
	if err != nil {
		t.Fatalf("stalled mirror must fall back to direct, got: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Fatalf("fallback body = %q, want %q", buf.String(), payload)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("mirror-stall fallback took %v, want ~one stall window", elapsed)
	}
}

// Geodata round ruling (triage §5.4): the first file stalls, its
// failure is recorded, and the remaining files of the round still
// complete — one stalled file must not starve the round.
func TestR3WDGGeoRoundContinuesAfterStalledFile(t *testing.T) {
	shrinkFetchStallWindow(t, 300*time.Millisecond)

	// File A: checksum is served fast, the data body stalls.
	dataA := []byte("geo-a")
	sumA := sha256.Sum256(dataA)
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			_, _ = w.Write([]byte(hex.EncodeToString(sumA[:]) + "  geosite.dat\n"))
			return
		}
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
	}))
	defer srvA.Close()

	// File B: fully healthy.
	dataB := []byte("geo-b-content")
	sumB := sha256.Sum256(dataB)
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			_, _ = w.Write([]byte(hex.EncodeToString(sumB[:]) + "  geoip.dat\n"))
			return
		}
		_, _ = w.Write(dataB)
	}))
	defer srvB.Close()

	files := []GeoFile{
		{Name: "geosite.dat", URL: srvA.URL + "/geosite.dat", ChecksumURL: srvA.URL + "/geosite.dat.sha256sum"},
		{Name: "geoip.dat", URL: srvB.URL + "/geoip.dat", ChecksumURL: srvB.URL + "/geoip.dat.sha256sum"},
	}
	binDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	staged, err := stageGeoFiles(ctx, binDir, files, nil)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "geosite.dat") {
		t.Fatalf("round error = %v, want it to name the stalled geosite.dat only", err)
	}
	if len(staged) != 1 || staged[0] != "geoip.dat" {
		t.Fatalf("staged = %v, want [geoip.dat] (the healthy file must still complete)", staged)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("round took %v; the stalled file must cost ~one stall window, not the round budget", elapsed)
	}
	if _, serr := os.Stat(filepath.Join(binDir, "geoip.dat.new")); serr != nil {
		t.Fatalf("healthy file was not staged: %v", serr)
	}
}

// Registration pin: the production window is the shared 45s
// constant, registered as WDG-01 in timeout-budgets.tsv rows
// A001/A002, and sits far inside the whole-operation budgets.
func TestR3WDGStallWindowRegistration(t *testing.T) {
	if download.StallWindow != 45*time.Second {
		t.Fatalf("download.StallWindow = %v, want 45s (registered value)", download.StallWindow)
	}
	if download.StallWindow >= downloadTimeout {
		t.Fatalf("stall window %v must be far inside the kernel whole-fetch budget %v", download.StallWindow, downloadTimeout)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "doc", "audit", "timeout-budgets.tsv"))
	if err != nil {
		t.Fatalf("read timeout budget registry: %v", err)
	}
	for _, row := range []string{"A001", "A002"} {
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, row+"\t") {
				found = true
				if !strings.Contains(line, "WDG-01") || !strings.Contains(line, "45s") {
					t.Fatalf("registry row %s does not register the WDG-01 45s stall window: %s", row, line)
				}
			}
		}
		if !found {
			t.Fatalf("registry row %s missing", row)
		}
	}
}
