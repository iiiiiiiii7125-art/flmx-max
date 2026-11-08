package service

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// GARBAGE-AUDIT A4: orphan spools were named but never deleted, so
// every panel/secret rotation left one more file behind forever.
// The sweep may only remove an orphan when it is provably safe:
// older than orphanSpoolMaxAge AND fully valid AND carrying no
// unconfirmed traffic at all (no Pending, no Buffered, no Retry).
// Anything else — younger, corrupt, or holding any unconfirmed
// bytes — must survive and keep being named by the orphan scan.

func writeOrphanSpool(t *testing.T, dir, name string, mutate func(*trafficSpoolState), age time.Duration, now time.Time) string {
	t.Helper()
	var stream [16]byte
	for i := range stream {
		stream[i] = byte(i + 1)
	}
	state := trafficSpoolState{Stream: hex.EncodeToString(stream[:]), Next: 1}
	if mutate != nil {
		mutate(&state)
	}
	spool := &trafficSpool{path: filepath.Join(dir, name)}
	if err := spool.save(state); err != nil {
		t.Fatal(err)
	}
	mt := now.Add(-age)
	if err := os.Chtimes(spool.path, mt, mt); err != nil {
		t.Fatal(err)
	}
	return spool.path
}

func TestSweepOrphanTrafficSpoolsRemovesOnlyProvablyEmptyAgedOrphans(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	aged := orphanSpoolMaxAge + 24*time.Hour

	emptyAged := writeOrphanSpool(t, dir, "flmx_traffic_empty.json", nil, aged, now)
	pendingAged := writeOrphanSpool(t, dir, "flmx_traffic_pending.json", func(s *trafficSpoolState) {
		s.Pending = &trafficEnvelope{Stream: s.Stream, Sequence: 1, Items: []TrafficReportItem{{N: "svc", U: 1}}}
	}, aged, now)
	bufferedAged := writeOrphanSpool(t, dir, "flmx_traffic_buffered.json", func(s *trafficSpoolState) {
		s.Buffered = []TrafficReportItem{{N: "svc", D: 2}}
	}, aged, now)
	retryAged := writeOrphanSpool(t, dir, "flmx_traffic_retry.json", func(s *trafficSpoolState) {
		s.Retry = [][]TrafficReportItem{{{N: "svc", U: 3}}}
	}, aged, now)
	emptyYoung := writeOrphanSpool(t, dir, "flmx_traffic_young.json", nil, 24*time.Hour, now)
	ownPath := writeOrphanSpool(t, dir, "flmx_traffic_own.json", nil, aged, now)

	corrupt := writeSpoolDirFile(t, dir, "flmx_traffic_corrupt.json", aged, now) // "{}" is not a valid spool
	archive := writeSpoolDirFile(t, dir, "flmx_traffic_x.json.rejected-stream-1.json", aged, now)

	removed := sweepOrphanTrafficSpools(dir, ownPath, now)
	if len(removed) != 1 || removed[0] != "flmx_traffic_empty.json" {
		t.Fatalf("only the provably empty aged orphan may be removed, got %v", removed)
	}
	mustNotExist(t, emptyAged)
	for _, p := range []string{pendingAged, bufferedAged, retryAged, emptyYoung, ownPath, corrupt, archive} {
		mustExist(t, p)
	}
}

func TestSweepStaleUpgradeStagingRemovesOnlyAgedUpgradeTemps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	aged := staleUpgradeStagingMaxAge + 24*time.Hour

	stale := writeSpoolDirFile(t, dir, ".flmx_agent.upgrade-123456", aged, now)
	staleRollback := writeSpoolDirFile(t, dir, ".flmx_agent.upgrade-rollback-abcdef", aged, now)
	fresh := writeSpoolDirFile(t, dir, ".flmx_agent.upgrade-999999", time.Hour, now)
	rollbackOnly := writeSpoolDirFile(t, dir, ".flmx_agent.rollback-123456", aged, now)
	noDot := writeSpoolDirFile(t, dir, "flmx_agent.upgrade-123456", aged, now)
	upgradeLog := writeSpoolDirFile(t, dir, "agent-upgrade.log", aged, now)

	removed := sweepStaleUpgradeStaging(dir, now)
	if len(removed) != 2 {
		t.Fatalf("exactly the two aged .upgrade- staging files may be removed, got %v", removed)
	}
	mustNotExist(t, stale)
	mustNotExist(t, staleRollback)
	for _, p := range []string{fresh, rollbackOnly, noDot, upgradeLog} {
		mustExist(t, p)
	}
}

func TestGarbageSweepsEmptyAndMissingDirs(t *testing.T) {
	if removed := sweepOrphanTrafficSpools(t.TempDir(), "", time.Now()); len(removed) != 0 {
		t.Fatalf("empty dir must remove nothing, got %v", removed)
	}
	if removed := sweepStaleUpgradeStaging(filepath.Join(t.TempDir(), "absent"), time.Now()); len(removed) != 0 {
		t.Fatalf("missing dir must remove nothing, got %v", removed)
	}
}
