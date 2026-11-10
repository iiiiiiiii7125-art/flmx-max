package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R2-ISSUEDAT (AUDIT-R2-C2 §4.1 / F2 §2.0 / LIVE R2-01, three lanes
// independently reproduced): the panel dispatches UninstallAgent
// with issuedAt as a JSON number (UnixMilli) and decodes the final
// frame with IssuedAt int64, but the agent parsed it as a string
// (swallowing the type error) and emitted the final frame with an
// empty string — every online uninstall's final report was discarded
// as malformed. These tests drive the agent handler with the exact
// production wire shape and assert the numeric round-trip; the
// RFC3339-string shape survives only as a defensive-compat leg
// (see TestFix212UninstallNewKeyExecutesAfterStoredFinal) and must
// never again be the sole proof of this contract.

const r2IssuedAtMillis = int64(1794276644653)

// panelUninstallResultEnvelope mirrors the panel's real decode
// struct (go-backend/internal/ws/uninstall.go:50-60) verbatim:
// IssuedAt is int64, so a string-issuedAt frame fails the whole
// decode. The mirror is deliberate — the x module cannot import
// go-backend — and the field names/types are pinned here so any
// drift is caught by TestR2IssuedAtFinalFrameDecodesAsPanelInt64.
type panelUninstallResultEnvelope struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		UninstallKey string `json:"uninstallKey"`
		IssuedAt     int64  `json:"issuedAt"`
		RequestID    string `json:"requestId"`
		State        string `json:"state"`
		FailedStep   string `json:"failedStep"`
	} `json:"data"`
}

func TestR2IssuedAtNumericDispatchKeyAndStateFile(t *testing.T) {
	w4Setup()
	root := t.TempDir()
	installDir := filepath.Join(root, "flmx_agent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(installDir, "flmx_agent")
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubUninstallHooks(t, binPath, false)
	origAckWait, origAttempts := uninstallAckWaitTimeout, uninstallReportAttempts
	uninstallAckWaitTimeout = 50 * time.Millisecond
	uninstallReportAttempts = 1
	t.Cleanup(func() { uninstallAckWaitTimeout, uninstallReportAttempts = origAckWait, origAttempts })
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })

	w := &WebSocketReporter{}
	// Production arrival shape: the websocket layer decodes JSON
	// into interface{} without UseNumber, so the number arrives as
	// float64 — exactly what the panel's bytes decode to.
	data, err := w.handleUninstallAgent(map[string]interface{}{"issuedAt": float64(r2IssuedAtMillis)}, "req-prod-1")
	if err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	if m := data.(map[string]interface{}); m["uninstallKey"] != "1794276644653" || m["state"] != "acked" {
		t.Fatalf("ack data: %+v", data)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && (st.State == "succeeded" || st.State == "failed") {
			if st.State != "succeeded" {
				t.Fatalf("final state = %q (failedStep %q)", st.State, st.FailedStep)
			}
			if int64(st.IssuedAt) != r2IssuedAtMillis {
				t.Fatalf("stored IssuedAt = %d, want %d", int64(st.IssuedAt), r2IssuedAtMillis)
			}
			if st.Key != "1794276644653" {
				t.Fatalf("stored key = %q, want decimal UnixMilli", st.Key)
			}
			path, err := uninstallStatePath()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// The state file must carry issuedAt as a JSON number —
			// the pre-fix file had no issuedAt key at all (omitempty
			// on the empty string), which is how the key was lost.
			if !strings.Contains(string(raw), `"issuedAt":1794276644653`) {
				t.Fatalf("state file = %s, want numeric issuedAt", string(raw))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not reach a final state")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestR2IssuedAtFinalFrameDecodesAsPanelInt64(t *testing.T) {
	st := &uninstallState{
		Key:        "1794276644653",
		IssuedAt:   issuedAtMillis(r2IssuedAtMillis),
		RequestID:  "req-prod-1",
		State:      "failed",
		FailedStep: "uninstall-kernels",
		Reason:     "kernels failed",
	}
	raw, err := json.Marshal(buildUninstallFinalResponse(st))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"issuedAt":"`) {
		t.Fatalf("final frame = %s, issuedAt must be a JSON number, not a string", string(raw))
	}
	if !strings.Contains(string(raw), `"issuedAt":1794276644653`) {
		t.Fatalf("final frame = %s, want numeric issuedAt", string(raw))
	}
	// The panel's real decode shape accepts the frame.
	var env panelUninstallResultEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("panel-shaped decode of the final frame failed: %v (frame %s)", err, string(raw))
	}
	if env.Data.IssuedAt != r2IssuedAtMillis {
		t.Fatalf("panel-decoded issuedAt = %d, want %d", env.Data.IssuedAt, r2IssuedAtMillis)
	}
	// Negative control: the pre-fix shape (string issuedAt) must
	// fail the panel's int64 decode — this is the exact breakage
	// R2-ISSUEDAT records, and the reason a string-only test can
	// never prove this contract.
	legacy := `{"type":"UninstallResult","success":false,"data":{"issuedAt":"","state":"failed"}}`
	var legacyEnv panelUninstallResultEnvelope
	if err := json.Unmarshal([]byte(legacy), &legacyEnv); err == nil {
		t.Fatal("panel-shaped decode must reject the pre-fix empty-string issuedAt frame")
	}
}

func TestR2IssuedAtDuplicateNumericDispatchOnlyReReports(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	saveUninstallState(&uninstallState{Key: "1794276644653", IssuedAt: issuedAtMillis(r2IssuedAtMillis), RequestID: "req-prod-1", State: "succeeded", Reported: true})
	if uninstallStarted.Load() {
		t.Skip("uninstall already started by an earlier test in this process")
	}
	origAckWait := uninstallAckWaitTimeout
	uninstallAckWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { uninstallAckWaitTimeout = origAckWait })

	w := &WebSocketReporter{}
	// Same issuance, new requestId (the panel's reconnect re-send):
	// duplicate — re-report only, never re-execute.
	data, err := w.handleUninstallAgent(map[string]interface{}{"issuedAt": float64(r2IssuedAtMillis)}, "req-prod-2")
	if err != nil {
		t.Fatalf("duplicate dispatch ack: %v", err)
	}
	m := data.(map[string]interface{})
	if m["alreadyFinal"] != true || m["state"] != "succeeded" || m["uninstallKey"] != "1794276644653" {
		t.Fatalf("duplicate dispatch must only re-report, got %+v", data)
	}
	if uninstallStarted.Load() {
		t.Fatal("duplicate dispatch must not start a second execution")
	}
	if st, ok := loadUninstallState(); !ok || st.Key != "1794276644653" || int64(st.IssuedAt) != r2IssuedAtMillis {
		t.Fatalf("stored final state was disturbed by the duplicate dispatch: %+v", st)
	}
}

func TestR2IssuedAtMalformedReturnsError(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	w := &WebSocketReporter{}
	for _, bad := range []interface{}{
		map[string]interface{}{"issuedAt": "not-a-time"},
		map[string]interface{}{"issuedAt": true},
		map[string]interface{}{"issuedAt": 1794276644653.5},
		map[string]interface{}{"issuedAt": []interface{}{1}},
	} {
		if _, err := w.handleUninstallAgent(bad, "req-bad"); err == nil {
			t.Fatalf("malformed issuedAt %v must be a visible error, got an ack", bad)
		}
	}
	if st, ok := loadUninstallState(); ok {
		t.Fatalf("malformed dispatch must not persist any state, got %+v", st)
	}
}

func TestR2IssuedAtAbsentFallsBackToRequestID(t *testing.T) {
	w4Setup()
	root := t.TempDir()
	installDir := filepath.Join(root, "flmx_agent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(installDir, "flmx_agent")
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubUninstallHooks(t, binPath, false)
	origAckWait, origAttempts := uninstallAckWaitTimeout, uninstallReportAttempts
	uninstallAckWaitTimeout = 50 * time.Millisecond
	uninstallReportAttempts = 1
	t.Cleanup(func() { uninstallAckWaitTimeout, uninstallReportAttempts = origAckWait, origAttempts })
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })

	w := &WebSocketReporter{}
	data, err := w.handleUninstallAgent(nil, "req-legacy")
	if err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	if m := data.(map[string]interface{}); m["uninstallKey"] != "req-legacy" || m["state"] != "acked" {
		t.Fatalf("ack data without issuedAt: %+v", data)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && (st.State == "succeeded" || st.State == "failed") {
			if st.Key != "req-legacy" {
				t.Fatalf("key without issuedAt = %q, want the requestId fallback", st.Key)
			}
			if st.IssuedAt != 0 {
				t.Fatalf("IssuedAt = %d, want absent (0)", int64(st.IssuedAt))
			}
			path, err := uninstallStatePath()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"issuedAt"`) {
				t.Fatalf("state file without issuedAt must omit the key, got %s", string(raw))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not reach a final state")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestR2IssuedAtLegacyStateFileMigratesKey(t *testing.T) {
	w4Setup()
	dir := stubUninstallStatePath(t)
	// A state file as written by the pre-fix agent: RFC3339 string
	// issuedAt, keyed by that same string.
	legacy := `{"key":"2026-11-10T01:00:00Z","issuedAt":"2026-11-10T01:00:00Z","requestId":"req-old","state":"succeeded","reported":true,"updatedAt":"2026-11-10T01:00:00Z"}`
	path, err := uninstallStatePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = dir
	st, ok := loadUninstallState()
	if !ok {
		t.Fatal("legacy state file must still load")
	}
	if int64(st.IssuedAt) != 1794272400000 {
		t.Fatalf("migrated IssuedAt = %d, want 1794272400000", int64(st.IssuedAt))
	}
	if st.Key != "1794272400000" {
		t.Fatalf("migrated key = %q, want decimal UnixMilli so the same issuance is still recognized", st.Key)
	}
	if uninstallStarted.Load() {
		t.Skip("uninstall already started by an earlier test in this process")
	}
	origAckWait := uninstallAckWaitTimeout
	uninstallAckWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { uninstallAckWaitTimeout = origAckWait })
	// The same issuance re-dispatched in the new numeric form must
	// still be recognized as a duplicate of the legacy final state.
	w := &WebSocketReporter{}
	data, err := w.handleUninstallAgent(map[string]interface{}{"issuedAt": float64(1794272400000)}, "req-after-upgrade")
	if err != nil {
		t.Fatalf("duplicate dispatch ack: %v", err)
	}
	if m := data.(map[string]interface{}); m["alreadyFinal"] != true {
		t.Fatalf("legacy final state must be recognized as a duplicate, got %+v", data)
	}
	if uninstallStarted.Load() {
		t.Fatal("duplicate dispatch must not start a second execution")
	}
}
