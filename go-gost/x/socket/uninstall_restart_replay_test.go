package socket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/go-gost/x/internal/util/crypto"
)

// R18-R2-D2-02: D2-03 only closed the in-process exit — a container
// restarted by an external cause (host reboot, `docker restart`,
// OOM kill) while the final report was still undelivered never
// re-reports: the restarted process idles at AgentUninstalled()
// and the persisted state (reported=false) is never read again.
// The fix carries the panel address + secret in the uninstall
// state (written before any destructive step, same 0600 file that
// already holds the uninstall record) so the restarted process
// can make one bounded re-report attempt before idling.

// oneShotPanel is a live panel endpoint that records every frame
// it receives, for the post-restart re-report tests.
type oneShotPanel struct {
	srv    *httptest.Server
	frames chan []byte
}

func startOneShotPanel(t *testing.T) *oneShotPanel {
	t.Helper()
	p := &oneShotPanel{frames: make(chan []byte, 8)}
	upgrader := websocket.Upgrader{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			p.frames <- msg
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// decryptUninstallFrame decodes one encrypted UninstallResult frame
// received by the panel.
func decryptUninstallFrame(t *testing.T, raw []byte, secret string) (string, map[string]interface{}) {
	t.Helper()
	var wrap struct {
		Encrypted bool   `json:"encrypted"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || !wrap.Encrypted {
		t.Fatalf("final frame must be an encrypted envelope, got %s", raw)
	}
	dec, err := crypto.NewAESCrypto(secret)
	if err != nil {
		t.Fatalf("gate crypto: %v", err)
	}
	plain, err := dec.Decrypt(wrap.Data)
	if err != nil {
		t.Fatalf("decrypt final frame: %v", err)
	}
	var resp struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(plain, &resp); err != nil {
		t.Fatalf("unmarshal final frame %q: %v", plain, err)
	}
	return resp.Type, resp.Data
}

// TestFlushPendingUninstallAfterRestartDeliversFinalReport is the
// end-to-end half of R18-R2-D2-02: a stored final state with
// reported=false and persisted credentials is delivered by one
// bounded attempt, and the state is marked reported.
func TestFlushPendingUninstallAfterRestartDeliversFinalReport(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })
	panel := startOneShotPanel(t)

	const secret = "d202-restart-secret-0123456789abcdef"
	saveUninstallState(&uninstallState{
		Key: "k-d202", RequestID: "req-d202", State: "succeeded",
		PanelAddr: panel.srv.URL, PanelSecret: secret,
	})
	// connect() reads config.json from the working directory: run
	// from an empty directory so no stray config can redirect
	// the dial.
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if ok := FlushPendingUninstallAfterRestart(10 * time.Second); !ok {
		t.Fatal("one-shot post-restart re-report must deliver the stored final state")
	}
	if st, ok := loadUninstallState(); !ok || !st.Reported {
		t.Fatalf("state must be marked reported after the re-report, got %+v ok=%v", st, ok)
	}
	select {
	case raw := <-panel.frames:
		typ, data := decryptUninstallFrame(t, raw, secret)
		if typ != "UninstallResult" || data["state"] != "succeeded" {
			t.Fatalf("final frame = %s/%v, want UninstallResult/succeeded", typ, data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no final frame arrived at the panel")
	}
}

// TestFlushPendingUninstallAfterRestartReportsInterrupted covers
// the process killed mid-uninstall (OOM kill / host reboot during
// execution): the stored non-final state must be honestly
// reported as failed/interrupted, not left unreported.
func TestFlushPendingUninstallAfterRestartReportsInterrupted(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })
	panel := startOneShotPanel(t)

	const secret = "d202-restart-secret-0123456789abcdef"
	saveUninstallState(&uninstallState{
		Key: "k-d202-int", RequestID: "req-d202-int", State: "executing",
		PanelAddr: panel.srv.URL, PanelSecret: secret,
	})
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if ok := FlushPendingUninstallAfterRestart(10 * time.Second); !ok {
		t.Fatal("one-shot post-restart re-report must deliver the interrupted state")
	}
	if st, ok := loadUninstallState(); !ok || !st.Reported || st.State != "failed" {
		t.Fatalf("interrupted state must converge to failed/reported, got %+v ok=%v", st, ok)
	}
	select {
	case raw := <-panel.frames:
		typ, data := decryptUninstallFrame(t, raw, secret)
		if typ != "UninstallResult" || data["state"] != "failed" || data["failedStep"] != "interrupted" {
			t.Fatalf("final frame = %s/%v, want UninstallResult/failed/interrupted", typ, data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no final frame arrived at the panel")
	}
}

// TestFlushPendingUninstallAfterRestartWithoutCredentials: a state
// stored before the fix (or from a no-secret reporter) carries no
// credentials — the one-shot must not dial anywhere and must just
// report that it could not re-report.
func TestFlushPendingUninstallAfterRestartWithoutCredentials(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)
	dials := 0
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials++
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	saveUninstallState(&uninstallState{
		Key: "k-d202-nocred", RequestID: "req-d202-nocred", State: "succeeded",
	})
	if ok := FlushPendingUninstallAfterRestart(5 * time.Second); ok {
		t.Fatal("one-shot re-report without stored credentials must return false")
	}
	if dials != 0 {
		t.Fatalf("one-shot re-report without credentials must not dial, got %d dials", dials)
	}
	if st, ok := loadUninstallState(); !ok || st.Reported {
		t.Fatalf("state must stay unreported when no re-report was possible, got %+v ok=%v", st, ok)
	}
}
