package router

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestRouteCacheHardCap verifies P1-07 for the strongest member of
// the grow-only map family: routeCache keys are arbitrary,
// client-driven destination addresses, so the entry count must stay
// under its hard cap no matter how many distinct destinations are
// resolved.
func TestRouteCacheHardCap(t *testing.T) {
	h, ok := NewHandler().(*routerHandler)
	if !ok {
		t.Fatal("NewHandler did not return a *routerHandler")
	}
	h.md.routerCacheEnabled = true
	h.md.routerCacheExpiration = time.Minute

	const lookups = 20000 // > the 16384 cap
	for i := 0; i < lookups; i++ {
		h.getRoute(context.Background(), "no-such-router", fmt.Sprintf("dst-%d.example.com:443", i))
	}
	if got := h.routeCache.Len(); got > 16384 {
		t.Fatalf("routeCache exceeded hard cap: Len() = %d, want <= 16384", got)
	}
	if got := h.routeCache.Len(); got == 0 {
		t.Fatal("routeCache unexpectedly empty after lookups")
	}
}
