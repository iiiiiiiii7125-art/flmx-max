package handler

import (
	"testing"
	"time"
)

// R4B10-001: the node month-traffic window start must be computed in
// the viewer's IANA zone so the 1st of the month carries its own UTC
// offset; the old fixed-offset computation used *today's* offset for
// the whole month and shifted the window by the DST delta.

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%s): %v", name, err)
	}
	return loc
}

// DST switches after the 1st (spring forward on Mar 8, 2026 in
// America/New_York): viewed on Mar 15 (EDT, UTC-4), the month must
// still start at Mar 1 00:00 EST (UTC-5) = 05:00 UTC, not at the
// fixed-offset 04:00 UTC.
func TestR4KR6_NodeTrafficMonthStartDSTAfterFirst(t *testing.T) {
	loc := mustLoadLocation(t, "America/New_York")
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, loc)

	got := nodeTrafficMonthStart(now, loc)
	want := time.Date(2026, 3, 1, 5, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("month start = %v, want %v", got.UTC(), want)
	}

	// The pre-fix fixed-offset form (offset taken from "now") lands an
	// hour earlier; pin the difference so the regression is explicit.
	fixed := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0,
		time.FixedZone("", -4*60*60))
	if got.Equal(fixed) {
		t.Fatalf("IANA month start should differ from fixed-offset form %v", fixed.UTC())
	}
}

// DST switches on the 1st itself (fall back on Nov 1, 2026 in
// America/New_York): viewed on Nov 15 (EST, UTC-5), the month starts
// at Nov 1 00:00 EDT (UTC-4) = 04:00 UTC, not the fixed-offset
// 05:00 UTC.
func TestR4KR6_NodeTrafficMonthStartDSTOnFirst(t *testing.T) {
	loc := mustLoadLocation(t, "America/New_York")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, loc)

	got := nodeTrafficMonthStart(now, loc)
	want := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("month start = %v, want %v", got.UTC(), want)
	}
}

// The shared resolution: a loadable IANA name wins over the numeric
// offset, an unloadable one falls back to the fixed offset unchanged.
func TestR4KR6_NodeTrafficLocationResolution(t *testing.T) {
	loc := subTrafficLocation(subTrafficRequest{TZ: "America/New_York", TZOffset: 480})
	if loc.String() != "America/New_York" {
		t.Fatalf("IANA name should win, got location %q", loc.String())
	}

	fallback := subTrafficLocation(subTrafficRequest{TZ: "Not/AZone", TZOffset: 480})
	probe := time.Date(2026, 6, 1, 0, 0, 0, 0, fallback)
	_, off := probe.Zone()
	if off != 480*60 {
		t.Fatalf("fallback offset = %d, want %d", off, 480*60)
	}
}
