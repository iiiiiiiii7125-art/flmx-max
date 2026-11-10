package handler

import "testing"

// Package K: panel host disk self-check math and guards.

func TestDiskUsagePercent(t *testing.T) {
	cases := []struct {
		blocks, bfree uint64
		want          float64
		ok            bool
	}{
		{1000, 1000, 0, true},
		{1000, 200, 80, true},
		{1000, 0, 100, true},
		{0, 0, 0, false},
		{1000, 1001, 0, false}, // inconsistent stat: refuse to judge
	}
	for _, c := range cases {
		got, ok := diskUsagePercent(c.blocks, c.bfree)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("diskUsagePercent(%d, %d) = %v, %v; want %v, %v", c.blocks, c.bfree, got, ok, c.want, c.ok)
		}
	}
}

func TestPanelDiskCheckGuards(t *testing.T) {
	// No repo / no tracker: must be a silent no-op, never a panic.
	h := &Handler{}
	h.checkPanelDiskUsage()
	var nilHandler *Handler
	nilHandler.checkPanelDiskUsage()
}

func TestPanelDiskSurfaceIsIsolatedInTracker(t *testing.T) {
	// The panel surface (nodeID 0, "panel-disk") must not share
	// alert state with a real node's disk surface.
	tr := newResourceAlertTracker()
	if act := tr.decide(resourceAlertKey(0, "panel-disk"), 90); act != resourceActionEnter {
		t.Fatalf("panel enter action = %d, want Enter", act)
	}
	if tr.isAlerted(resourceAlertKey(1, "disk")) {
		t.Fatal("node 1 disk surface shares state with the panel surface")
	}
	if act := tr.decide(resourceAlertKey(1, "disk"), 90); act != resourceActionEnter {
		t.Fatalf("node enter action = %d, want Enter", act)
	}
}
