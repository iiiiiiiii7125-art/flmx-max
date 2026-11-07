package stats_test

import (
	"testing"

	corestats "github.com/go-gost/core/observer/stats"
	utilstats "github.com/go-gost/x/internal/util/stats"
)

// TestHandlerStatsEviction verifies the P1-07 eviction: an entry with
// no live connections and no updates across consecutive Events sweeps
// is dropped (a later Stats call starts fresh), while an entry with
// live connections is retained no matter how quiet it is.
func TestHandlerStatsEviction(t *testing.T) {
	hs := utilstats.NewHandlerStats("svc", false)

	alice := hs.Stats("alice")
	alice.Add(corestats.KindCurrentConns, 1)
	alice.Add(corestats.KindTotalConns, 1)

	bob := hs.Stats("bob")
	bob.Add(corestats.KindCurrentConns, 1)
	bob.Add(corestats.KindTotalConns, 1)

	if evs := hs.Events(); len(evs) != 2 {
		t.Fatalf("first Events() = %d events, want 2", len(evs))
	}

	// Alice's connection closes; the close itself is an update and is
	// reported once.
	alice.Add(corestats.KindCurrentConns, -1)
	if evs := hs.Events(); len(evs) != 1 {
		t.Fatalf("second Events() = %d events, want 1 (alice close)", len(evs))
	}

	// Two quiet sweeps: alice (0 conns, no updates) must be evicted,
	// bob (1 live conn) must be retained.
	hs.Events()
	hs.Events()

	if got := hs.Stats("alice").Get(corestats.KindTotalConns); got != 0 {
		t.Fatalf("alice entry not evicted: TotalConns = %d, want 0 (fresh entry)", got)
	}
	if got := hs.Stats("bob").Get(corestats.KindTotalConns); got != 1 {
		t.Fatalf("bob entry wrongly evicted: TotalConns = %d, want 1", got)
	}
}
