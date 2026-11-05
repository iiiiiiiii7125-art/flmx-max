package hop

import (
	"context"
	"testing"
	"time"

	"github.com/go-gost/core/chain"
	corehop "github.com/go-gost/core/hop"
	"github.com/go-gost/x/routing"
	xs "github.com/go-gost/x/selector"
	xlogger "github.com/go-gost/x/logger"
)

// D3-04 regression: hop.Select's priority early-return bypassed the
// selector pipeline (FailFilter/BackupFilter) for matcher nodes, so a
// dead highest-priority exit was returned first on every connection
// no matter how often it had failed. Priority must order the
// candidates, not exempt them from failure filtering.

func matcherNode(t *testing.T, name, addr, rule string, priority int) *chain.Node {
	t.Helper()
	m, err := routing.NewMatcher(rule)
	if err != nil {
		t.Fatalf("NewMatcher(%q): %v", rule, err)
	}
	return chain.NewNode(name, addr,
		chain.MatcherNodeOption(m),
		chain.PriorityNodeOption(priority),
	)
}

func testHop(nodes ...*chain.Node) corehop.Hop {
	sel := xs.NewSelector(
		xs.FIFOStrategy[*chain.Node](),
		xs.FailFilter[*chain.Node](1, time.Minute),
		xs.BackupFilter[*chain.Node](),
	)
	return NewHop(
		NodeOption(nodes...),
		SelectorOption(sel),
		LoggerOption(xlogger.Nop()),
	)
}

func selectForHost(h corehop.Hop) *chain.Node {
	return h.Select(context.Background(),
		corehop.NetworkSelectOption("tcp"),
		corehop.HostSelectOption("a.com"),
	)
}

func TestHopSelectPriorityNodeMarkedFailedIsSkipped(t *testing.T) {
	dead := matcherNode(t, "dead", "192.0.2.1:8080", "Host(`a.com`)", 10)
	alive := matcherNode(t, "alive", "192.0.2.2:8080", "Host(`a.com`)", 5)
	h := testHop(dead, alive)

	// Sanity: with no failures recorded, priority wins.
	if got := selectForHost(h); got != dead {
		t.Fatalf("Select with no failures = %v, want the priority node", got)
	}

	dead.Marker().Mark() // one failure >= maxFails(1)

	if got := selectForHost(h); got != alive {
		t.Fatalf("Select with priority node marked failed = %v, want the surviving lower-priority node", got)
	}
}

func TestHopSelectAllPriorityNodesFailedFallsToLowerGroup(t *testing.T) {
	deadTop := matcherNode(t, "dead-top", "192.0.2.1:8080", "Host(`a.com`)", 10)
	deadTop2 := matcherNode(t, "dead-top-2", "192.0.2.3:8080", "Host(`a.com`)", 10)
	alive := matcherNode(t, "alive", "192.0.2.2:8080", "Host(`a.com`)", 5)
	h := testHop(deadTop, deadTop2, alive)

	deadTop.Marker().Mark()
	deadTop2.Marker().Mark()

	if got := selectForHost(h); got != alive {
		t.Fatalf("Select with the whole top priority group failed = %v, want the lower-priority survivor", got)
	}
}

// Availability guard: a hop whose only node is a marked-failed
// priority node must still return that node (FailFilter's
// last-remaining-node guard), not nil — filtering must not make a
// single-exit hop permanently unusable.
func TestHopSelectSinglePriorityNodeStillReturnedWhenFailed(t *testing.T) {
	only := matcherNode(t, "only", "192.0.2.1:8080", "Host(`a.com`)", 10)
	h := testHop(only)

	only.Marker().Mark()

	if got := selectForHost(h); got != only {
		t.Fatalf("Select on a single-node hop = %v, want the only node (availability guard)", got)
	}
}
