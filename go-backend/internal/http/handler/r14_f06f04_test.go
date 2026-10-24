package handler

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R14 F-04 / 148-P3-4: attachNodeLatency swallowed a
// ListNodeLatencyTargets query error silently (targets, _ := ...): the
// whole list's latencyTargets vanished with no log line, indistinguishable
// from "no node ever chose targets". The list must still render (latency
// attached, targets omitted) but the failure must be logged.
func TestR14F06F04AttachLogsTargetsQueryError(t *testing.T) {
	// A repo with no DB: ListNodeLatencyTargets returns
	// "repository not initialized" (repository_monitor_nodes.go).
	h := New(&repo.Repository{}, "unit-test-secret")
	now := time.Now().UnixMilli()
	h.nodeLatency.replace(map[int64]externalProbe{
		5: {OK: true, LatencyMs: 23, CheckedAt: now, Points: []latencyPoint{{Name: "电信", Pinned: true, OK: true, LatencyMs: 23}}},
	}, false)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	items := []map[string]interface{}{{"id": int64(5)}}
	h.attachNodeLatency(items)

	if _, ok := items[0]["latency"]; !ok {
		t.Fatal("latency must still be attached when the targets query fails")
	}
	if _, ok := items[0]["latencyTargets"]; ok {
		t.Fatal("latencyTargets must be omitted when the targets query fails")
	}
	if !strings.Contains(buf.String(), "ListNodeLatencyTargets") {
		t.Fatalf("targets query failure must be logged, got log %q", buf.String())
	}
}
