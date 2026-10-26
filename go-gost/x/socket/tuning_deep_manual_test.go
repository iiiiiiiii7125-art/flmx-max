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

// F-05 (PORT-REVIEW-L2-E01) regression tests: on links whose policer
// knee sits above what the engine's single-stream unshaped probe can
// reach (L2 live leg: knee 4750 Mbps, single-stream probe 3724–4284
// Mbps), the sweep always reports NO_KNEE and persistTuneShape is
// never reached. A deep job may now carry an operator-supplied
// manual shaper rate (manualShapeMbps): the sweep is skipped and the
// same persist + post-shape verification runs. The NO_KNEE summary
// additionally names the single-stream ceiling instead of reporting
// "no policer".
//
// Coverage note (L1 §6(a), unchanged by this fix): these tests run
// in container mode via fakeTuneAutomation, so persistTuneShape
// returns at the InContainer() early return; the non-container
// systemd branch (write unit → daemon-reload → enable) is still
// only covered live, and nothing here claims otherwise.

func waitManualTuneJobDone(t *testing.T) *tuneJob {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		j := readTuneJob()
		if j != nil && j.State != "running" {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatal("deep job did not finish within 30s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// setupManualDeep saves the L2-link detection (auto-measured 4577
// Mbps, estimated 125.9 GB) and stubs the engine sweep to the L2
// product-deep shape: single-stream 4182 Mbps, no loss jump,
// NO_KNEE. It returns a counter of engine sweep invocations.
func setupManualDeep(t *testing.T) *int {
	t.Helper()
	fakeTuneAutomation(t)
	if err := os.MkdirAll(tuneEngineDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	d := &tuneDetection{Applied: true, BandwidthMbps: 4577, Interface: "eth0", Peer: "127.0.0.1", Port: 5201, Family: "4", MeasuredAt: time.Now().UnixMilli(), EstimatedGB: 125.9}
	if err := saveTuneJSON(tuneStatePath("tuning-result.json"), d); err != nil {
		t.Fatal(err)
	}
	calls := 0
	base := tuneCommand
	tuneCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "bash" && len(args) > 2 && args[2] == "sweep" {
			calls++
			return "FLYX_SWEEP_NO_KNEE=1\nFLYX_SWEEP_UNSHAPED=4182\n", errors.New("exit 3")
		}
		return base(ctx, name, args...)
	}
	// Shaped verification samples on the L2 link: single 4030,
	// four-stream 4460 Mbps under a 4710 Mbit shaper.
	tuneMeasure = func(_ context.Context, _ *tuneDetection, n int) (*tuneSample, error) {
		if n == 1 {
			return &tuneSample{Mbps: 4030}, nil
		}
		return &tuneSample{Mbps: 4460}, nil
	}
	return &calls
}

func TestTuneDeepManualRateReachesPersist(t *testing.T) {
	sweepCalls := setupManualDeep(t)
	if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "deep", "maxTrafficGB": 157.4, "manualShapeMbps": 4710}); err != nil {
		t.Fatalf("manual deep start: %v", err)
	}
	j := waitManualTuneJobDone(t)
	if j.State != "succeeded" {
		t.Fatalf("job: %+v", j)
	}
	if *sweepCalls != 0 {
		t.Fatalf("manual deep still ran the engine sweep %d time(s)", *sweepCalls)
	}
	got := loadTuneDetection()
	if got == nil || got.ShapeMbps != 4710 {
		t.Fatalf("manual deep did not persist the shaper: %+v", got)
	}
	if !strings.Contains(got.DeepSummary, "手动整形") || strings.Contains(got.DeepSummary, "拐点 4710") {
		t.Fatalf("manual deep summary must not report a measured knee: %q", got.DeepSummary)
	}
	b, err := os.ReadFile(filepath.Join(tuneEngineDir(), "shape.sh"))
	if err != nil || string(b) != tuneShapeScript("eth0", 4710) {
		t.Fatalf("shape.sh missing or wrong: %q %v", b, err)
	}
}

func TestTuneDeepManualRateRejected(t *testing.T) {
	setupManualDeep(t)
	for _, rate := range []int{-1, 100001} {
		if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "deep", "maxTrafficGB": 157.4, "manualShapeMbps": rate}); err == nil {
			t.Fatalf("manual deep accepted %d Mbps", rate)
		}
	}
	// Direct executeDeepTuning callers get the same rejection,
	// before any queue is captured.
	direct := loadTuneDetection()
	ctx := context.WithValue(context.Background(), tuneDeepManualKey{}, -5)
	if err := executeDeepTuning(ctx, direct, 157.4); err == nil || !strings.Contains(err.Error(), "手动整形速率") {
		t.Fatalf("direct manual deep accepted -5 Mbps: %v", err)
	}
	// The deep front gate still applies to the manual path: no
	// measurement, a stale measurement, and an unconfirmed budget
	// are all refused before any job starts.
	if err := os.Remove(tuneStatePath("tuning-result.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "deep", "maxTrafficGB": 157.4, "manualShapeMbps": 4710}); err == nil || !strings.Contains(err.Error(), "请先完成自动调优") {
		t.Fatalf("manual deep without a measurement: %v", err)
	}
	d := &tuneDetection{Applied: true, BandwidthMbps: 4577, Interface: "eth0", Peer: "127.0.0.1", Port: 5201, Family: "4", MeasuredAt: time.Now().Add(-25 * time.Hour).UnixMilli(), EstimatedGB: 125.9}
	if err := saveTuneJSON(tuneStatePath("tuning-result.json"), d); err != nil {
		t.Fatal(err)
	}
	if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "deep", "maxTrafficGB": 157.4, "manualShapeMbps": 4710}); err == nil || !strings.Contains(err.Error(), "24 小时") {
		t.Fatalf("manual deep with a stale measurement: %v", err)
	}
	d.MeasuredAt = time.Now().UnixMilli()
	if err := saveTuneJSON(tuneStatePath("tuning-result.json"), d); err != nil {
		t.Fatal(err)
	}
	if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "deep", "maxTrafficGB": 5, "manualShapeMbps": 4710}); err == nil || !strings.Contains(err.Error(), "预计流量") {
		t.Fatalf("manual deep with an unconfirmed budget: %v", err)
	}
}

func TestTuneDeepNoKneeSummaryNamesUnshapedCeiling(t *testing.T) {
	setupManualDeep(t)
	if _, err := handleTuneCommand("TuneStart", map[string]interface{}{"mode": "deep", "maxTrafficGB": 157.4}); err != nil {
		t.Fatalf("deep start: %v", err)
	}
	j := waitManualTuneJobDone(t)
	if j.State != "succeeded" {
		t.Fatalf("job: %+v", j)
	}
	got := loadTuneDetection()
	if got == nil || got.ShapeMbps != 0 {
		t.Fatalf("NO_KNEE must not shape: %+v", got)
	}
	if !strings.Contains(got.DeepSummary, "4182") || !strings.Contains(got.DeepSummary, "手动") {
		t.Fatalf("NO_KNEE summary hides the single-stream ceiling: %q", got.DeepSummary)
	}
}

func TestTuneDeepManualVerificationFailureRestores(t *testing.T) {
	setupManualDeep(t)
	d := loadTuneDetection()
	old := "#!/bin/sh\necho original-shape\n"
	shape := filepath.Join(tuneEngineDir(), "shape.sh")
	if err := os.WriteFile(shape, []byte(old), 0o700); err != nil {
		t.Fatal(err)
	}
	tuneMeasure = func(context.Context, *tuneDetection, int) (*tuneSample, error) {
		return nil, errors.New("verification failed")
	}
	ctx := context.WithValue(context.Background(), tuneDeepManualKey{}, 4710)
	if err := executeDeepTuning(ctx, d, 157.4); err == nil || !strings.Contains(err.Error(), "验证失败") {
		t.Fatalf("manual deep with failing verification: %v", err)
	}
	b, err := os.ReadFile(shape)
	if err != nil || string(b) != old {
		t.Fatalf("persisted a failed manual shaper: %q %v", b, err)
	}
	if got := loadTuneDetection(); got.ShapeMbps != 0 {
		t.Fatalf("failed manual shaper published: %+v", got)
	}
}

func TestTuneDeepManualSkipsSweepAtExecuteLevel(t *testing.T) {
	sweepCalls := setupManualDeep(t)
	d := loadTuneDetection()
	ctx := context.WithValue(context.Background(), tuneDeepManualKey{}, 4710)
	if err := executeDeepTuning(ctx, d, 157.4); err != nil {
		t.Fatal(err)
	}
	if *sweepCalls != 0 {
		t.Fatalf("manual deep ran the engine sweep %d time(s)", *sweepCalls)
	}
	got := loadTuneDetection()
	if got == nil || got.ShapeMbps != 4710 {
		t.Fatalf("manual deep did not persist the shaper: %+v", got)
	}
}
