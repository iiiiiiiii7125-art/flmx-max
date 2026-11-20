package socket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/go-gost/x/internal/util/crypto"
)

// R18-D2-03: a container-form node whose final uninstall report
// cannot be delivered must not exit first. After the wipe the
// restarted container holds no config.json (and therefore no panel
// address or secret) to reconnect with, so an exit strands the
// panel in pending_uninstall forever even though the state file
// says reported=false. The process must stay alive and keep
// retrying the report with the credentials it still holds in
// memory, and only cross the point of no return once the report
// has landed (or another path has marked it reported).
func TestContainerUninstallWaitsForFinalReport(t *testing.T) {
	w4Setup()
	stubUninstallStatePath(t)

	// Container-form layout in its own tree (stubUninstallStatePath
	// already owns a file named flmx_agent under its dir): the data
	// dir is named flmx_agent, the binary lives elsewhere (as in
	// the image layout).
	root := t.TempDir()
	dataDir := filepath.Join(root, "flmx_agent")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(root, "bin", "flmx_agent")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := stubUninstallHooksWithDataDir(t, binPath, dataDir, true)

	origAckWait, origAttempts, origRetryWait := uninstallAckWaitTimeout, uninstallReportAttempts, uninstallReportRetryWait
	uninstallAckWaitTimeout = 30 * time.Second // only the ack signal may release execution
	uninstallReportAttempts = 1
	uninstallReportRetryWait = 20 * time.Millisecond
	t.Cleanup(func() {
		uninstallAckWaitTimeout, uninstallReportAttempts, uninstallReportRetryWait = origAckWait, origAttempts, origRetryWait
	})
	uninstallStarted.Store(false)
	t.Cleanup(func() { uninstallStarted.Store(false) })

	// If this test fails before the wait completes, the waiter
	// goroutine would otherwise spin forever and — once the hooks
	// are restored — call the real os.Exit. Marking the stored
	// state reported lets the waiter return while the exit hook is
	// still stubbed; this cleanup runs before the hook restore
	// (registered earlier) by LIFO order.
	t.Cleanup(func() {
		if st, ok := loadUninstallState(); ok && !st.Reported {
			st.Reported = true
			saveUninstallState(st)
		}
		deadline := time.Now().Add(5 * time.Second)
		for stubs.exit == -1 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	// Live panel endpoint, dialled only later: the reporter starts
	// disconnected, so the inline final report must fail.
	frames := make(chan []byte, 4)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			frames <- msg
		}
	}))
	defer srv.Close()

	const secret = "d203-gate-secret-0123456789abcdef"
	w := NewWebSocketReporter("ws://127.0.0.1:1", secret)
	if _, err := w.handleUninstallAgent(nil, "req-d203"); err != nil {
		t.Fatalf("handleUninstallAgent: %v", err)
	}
	w.signalUninstallAck("req-d203", true)

	// Wait until the uninstall has run to its final state.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if st, ok := loadUninstallState(); ok && st.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not reach a final state")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The report is undeliverable (no connection): the container
	// must NOT exit while it is unreported.
	time.Sleep(600 * time.Millisecond)
	if stubs.exit != -1 {
		t.Fatalf("container must not exit before the final report lands, but exit(%d) was called with reported=false", stubs.exit)
	}

	// The panel comes back: plug a live connection into the same
	// reporter. The waiter must deliver the stored final state and
	// only then exit.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial gate panel endpoint: %v", err)
	}
	defer conn.Close()
	w.connMutex.Lock()
	w.conn = conn
	w.connected = true
	w.connMutex.Unlock()

	deadline = time.Now().Add(10 * time.Second)
	for stubs.exit == -1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if stubs.exit != 0 {
		t.Fatalf("container must exit once the final report lands, exit=%d", stubs.exit)
	}
	select {
	case raw := <-frames:
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
		if resp.Type != "UninstallResult" || resp.Data["state"] != "succeeded" {
			t.Fatalf("final frame = %+v, want UninstallResult/succeeded", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no final frame arrived after the connection came back")
	}
}
