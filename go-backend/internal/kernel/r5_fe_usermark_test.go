package kernel

import "testing"

// L4-08: the metering mark capacity boundary is a named, guarded
// limit — ids up to MaxUserMarkID are counted, above it UserMark is
// 0 and the panel-side guard (view flag + one-time warning) is what
// keeps the drop from being silent.
func TestR5FEL4UserMarkCapacityBoundary(t *testing.T) {
	if MaxUserMarkID != 0xffff {
		t.Fatalf("MaxUserMarkID = %d, want 0xffff (mirrors the agent's userMarkMax)", MaxUserMarkID)
	}
	if UserMark(MaxUserMarkID) == 0 {
		t.Fatalf("id at capacity must still be counted")
	}
	if UserMark(MaxUserMarkID+1) != 0 {
		t.Fatalf("id above capacity must yield mark 0 (guarded on the panel side, L4-08)")
	}
	if got := UserFromMark(UserMark(MaxUserMarkID)); got != MaxUserMarkID {
		t.Fatalf("boundary roundtrip: got %d, want %d", got, MaxUserMarkID)
	}
}
