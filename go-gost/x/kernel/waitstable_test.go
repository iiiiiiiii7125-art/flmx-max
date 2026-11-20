package kernel

import (
	"testing"
	"time"
)

// TestWaitStableWaitsForSlowStart is the R18-D3-03 gate: a kernel
// whose first Running sighting comes after the old 1.5s window
// (slow exec: cold page-in of a large binary on a slow disk) was
// judged failed and rolled back. The outer window is now 10s; a
// slow-but-healthy start must pass once it holds steady.
//
// The flip is done directly on the process's running flag: a
// shell-script fake kernel would be Running the moment cmd.Start
// returns, so no script can reproduce a slow exec — the unit
// level is the only place this is observable.
func TestWaitStableWaitsForSlowStart(t *testing.T) {
	m := NewManager(t.TempDir())
	p := newProcess("/bin/true", nil, t.TempDir(), &logRing{})
	m.mu.Lock()
	m.instances["xray"] = &instance{logs: &logRing{}, procs: []*process{p}}
	m.mu.Unlock()
	// Detach before Shutdown: the process was never started, and
	// stopProcesses on it would block on the 30s hard limit.
	t.Cleanup(func() {
		m.mu.Lock()
		m.instances["xray"].procs = nil
		m.mu.Unlock()
		m.Shutdown()
	})
	go func() {
		time.Sleep(3 * time.Second)
		p.mu.Lock()
		p.running = true
		p.mu.Unlock()
	}()
	start := time.Now()
	if !m.waitStable("xray") {
		t.Fatalf("waitStable = false, want true for a kernel that starts at 3s and holds steady (elapsed %v)", time.Since(start))
	}
	elapsed := time.Since(start)
	if elapsed < 4*time.Second || elapsed > 9*time.Second {
		t.Fatalf("waitStable took %v, want ~4.5s (3s to first sighting + 1.5s steady)", elapsed)
	}
}

// TestWaitStableRejectsBriefRun guards the other side of the
// R18-D3-03 window change: a kernel that runs briefly and dies
// must still fail fast — the longer outer window must not let a
// crash-looping kernel through, and must not make waitStable sit
// out the whole window.
func TestWaitStableRejectsBriefRun(t *testing.T) {
	m := NewManager(t.TempDir())
	p := newProcess("/bin/true", nil, t.TempDir(), &logRing{})
	m.mu.Lock()
	m.instances["xray"] = &instance{logs: &logRing{}, procs: []*process{p}}
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		m.instances["xray"].procs = nil
		m.mu.Unlock()
		m.Shutdown()
	})
	go func() {
		time.Sleep(100 * time.Millisecond)
		p.mu.Lock()
		p.running = true
		p.mu.Unlock()
		time.Sleep(400 * time.Millisecond)
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()
	start := time.Now()
	if m.waitStable("xray") {
		t.Fatalf("waitStable = true, want false for a kernel that dies 400ms after starting")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waitStable took %v, want a fast failure, not the whole outer window", elapsed)
	}
}
