package socket

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gost/x/config"
)

// R9-06 (I-F2-01) regression pin, the inversion of round-9 probe S:
// UninstallAgent used to be dispatched on the read-command path, so
// its async uninstall sequence ran fully concurrent with an in-flight
// mutation command (e.g. an UpgradeAgent download parked in the
// single mutation worker). UninstallAgent is now in the mutation
// command set: dispatched while the worker is blocked, the uninstall
// must not start until the in-flight mutation settles — and must
// then proceed to completion.
func TestR9UninstallQueuesBehindBlockedMutation(t *testing.T) {
	uninstallStarted.Store(false)

	restored := make(chan struct{}, 1)
	exited := make(chan int, 1)
	dir := filepath.Join(t.TempDir(), "flmx_agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	origRT, origK := uninstallRestoreTuning, uninstallKernels
	origIC, origWD, origBP, origExit := uninstallInContainer, uninstallWorkingDir, uninstallBinaryPath, uninstallExit
	defer func() {
		uninstallRestoreTuning, uninstallKernels = origRT, origK
		uninstallInContainer, uninstallWorkingDir, uninstallBinaryPath, uninstallExit = origIC, origWD, origBP, origExit
		uninstallStarted.Store(false)
	}()
	uninstallRestoreTuning = func() error { restored <- struct{}{}; return nil }
	uninstallKernels = func() error { return nil }
	uninstallInContainer = func() bool { return true }
	uninstallWorkingDir = func() (string, error) { return dir, nil }
	uninstallBinaryPath = func() (string, error) { return "/usr/local/bin/flux_agent", nil }
	uninstallExit = func(code int) { exited <- code }

	w := NewWebSocketReporter("ws://127.0.0.1:1", "0123456789abcdef0123456789abcdef")
	defer w.Stop()
	go w.runMutationCommands()

	// Occupy the single mutation worker: hold the global mutation
	// lock so the queued AddService blocks inside routeCommand.
	unlock := config.LockMutation()
	w.dispatchCommand(CommandMessage{Type: "AddService", RequestId: "r9-mut"})
	time.Sleep(300 * time.Millisecond) // worker picked it up and is parked on the lock

	w.dispatchCommand(CommandMessage{Type: "UninstallAgent", RequestId: "r9-un"})

	// While the mutation is still in flight, the uninstall sequence
	// must not start (pre-fix it fired the restore step ~1.3s in and
	// exited while the mutation was still blocked).
	select {
	case <-restored:
		t.Fatal("R9-06: uninstall restore step fired while a mutation command was still blocked in flight")
	case <-time.After(2 * time.Second):
	}
	select {
	case code := <-exited:
		t.Fatalf("R9-06: uninstall reached exit(%d) while a mutation command was still blocked in flight", code)
	default:
	}

	// Once the in-flight mutation settles, the queued uninstall runs
	// to completion in order.
	unlock()
	select {
	case <-restored:
	case <-time.After(5 * time.Second):
		t.Fatal("R9-06: uninstall did not start after the in-flight mutation settled")
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("R9-06: uninstall exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("R9-06: uninstall did not complete after the in-flight mutation settled")
	}
}
