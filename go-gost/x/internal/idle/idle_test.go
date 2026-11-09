package idle

import (
	"testing"
	"time"
)

func TestSetGetResolve(t *testing.T) {
	old := Get()
	t.Cleanup(func() { Set(old) })

	Set(0)
	if got := Get(); got != 0 {
		t.Fatalf("Get after Set(0) = %v, want 0 (disabled)", got)
	}
	if got := Resolve(0); got != 0 {
		t.Fatalf("Resolve(0) with no window = %v, want 0", got)
	}

	Set(300 * time.Second)
	if got := Get(); got != 300*time.Second {
		t.Fatalf("Get = %v, want 300s", got)
	}
	if got := Resolve(0); got != 300*time.Second {
		t.Fatalf("Resolve(0) = %v, want node window 300s", got)
	}
	if got := Resolve(time.Minute); got != time.Minute {
		t.Fatalf("Resolve(override) = %v, want override 1m", got)
	}
	if got := Resolve(-time.Second); got != -time.Second {
		t.Fatalf("Resolve(negative) = %v, want the negative override (pinned off)", got)
	}

	// A negative Set clamps to disabled rather than arming a
	// window in the past.
	Set(-time.Second)
	if got := Get(); got != 0 {
		t.Fatalf("Get after Set(-1s) = %v, want 0 (disabled)", got)
	}
}
