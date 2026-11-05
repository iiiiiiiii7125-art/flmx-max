package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// D2-05 regression: the supervisor never checked stopCh before
// the first launch, and stop() fired exactly two signals through
// a path that is a no-op while cmd is unassigned — so a stop that
// raced a slow exec start (or landed before the first launch)
// could leave the kernel process running forever while stop()
// blocked on done, wedging every later Apply/Control behind
// applyMu.

func writeProcessScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake process")
	}
	path := filepath.Join(t.TempDir(), "proc.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSupervisorHonoursStopRequestedBeforeFirstLaunch(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "launched")
	script := writeProcessScript(t, "#!/bin/sh\ntouch \""+marker+"\"\nsleep 2\n")

	p := newProcess(script, nil, dir, &logRing{})
	close(p.stopCh) // stop requested before the supervisor starts
	p.start()

	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit after a pre-launch stop")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("process was launched even though stop was already requested")
	}
}

func TestStopTerminatesRunningProcess(t *testing.T) {
	dir := t.TempDir()
	script := writeProcessScript(t, "#!/bin/sh\nsleep 60\n")

	p := newProcess(script, nil, dir, &logRing{})
	p.start()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if running, _, _, _, _ := p.snapshot(); running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process never reached running state")
		}
		time.Sleep(20 * time.Millisecond)
	}

	stopped := make(chan struct{})
	go func() {
		p.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("stop() did not return within 15s for a running process")
	}
	if running, _, _, _, _ := p.snapshot(); running {
		t.Fatal("process still marked running after stop()")
	}
}
