package repo

import (
	"math"
	"math/big"
	"testing"
)

// R18-F2-01: the traffic charts multiplied first (v*m/d), so a single
// hour bucket over ~2.56TB wrapped int64 and the charts showed
// negative or wildly wrong buckets while the stored counters stayed
// correct. mulDivSaturating must return the exact floored share for
// those magnitudes; the old formula is asserted wrong on the same
// inputs, so the test fails on the pre-fix code by construction.
func TestMulDivSaturatingR18F201(t *testing.T) {
	cases := []struct{ v, m, d int64 }{
		{3_000_000_000_000, 3_599_999, 3_600_000},
		{6_000_000_000_000, 1_800_000, 3_600_000},
		{6_000_000_000_000, 1_799_999, 3_599_999},
		{9_000_000_000_000_000_000, 3_599_999, 3_600_000},
		{math.MaxInt64, 3_600_000, 3_600_000},
		{math.MaxInt64, 2, 1}, // saturates
		{1000, 1, 3},
		{0, 10, 10},
		{100, 0, 10},
		{100, 10, 0},
		{-5, 10, 10},
	}
	for _, c := range cases {
		got := mulDivSaturating(c.v, c.m, c.d)
		// Independent oracle: exact big.Int floor, clamped to MaxInt64.
		// Non-positive inputs are defined to yield 0; the oracle
		// must not divide by zero to say so.
		want := big.NewInt(0)
		if c.v > 0 && c.m > 0 && c.d > 0 {
			want.Mul(big.NewInt(c.v), big.NewInt(c.m))
			want.Quo(want, big.NewInt(c.d))
			if want.Cmp(big.NewInt(math.MaxInt64)) > 0 {
				want.SetInt64(math.MaxInt64)
			}
		}
		if got != want.Int64() {
			t.Errorf("mulDivSaturating(%d, %d, %d) = %d, want %d", c.v, c.m, c.d, got, want.Int64())
		}
		// The old multiply-first formula must be wrong on the
		// overflowing cases, or this test would pass on old code.
		if c.v > 0 && c.m > 0 && c.d > 0 && c.v > math.MaxInt64/c.m {
			if old := c.v * c.m / c.d; old == got {
				t.Errorf("old formula unexpectedly right for (%d, %d, %d)", c.v, c.m, c.d)
			}
		}
	}
}
