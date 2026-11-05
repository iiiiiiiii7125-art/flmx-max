package handler

import (
	"math"
	"testing"

	"go-backend/internal/store/model"
)

// F2-04 (arithmetic face): subUsed summed UploadBytes+DownloadBytes
// with a plain add — a restored pair of MaxInt64 counters wrapped to
// -2, so subOverQuota stayed false forever and even a later sync
// never cut the subscription off (live-d scenario 3). The sum must
// saturate.
func TestSubUsedSaturatesF204(t *testing.T) {
	s := &model.Subscription{TrafficLimit: 1048576, UploadBytes: math.MaxInt64, DownloadBytes: math.MaxInt64}
	if got := subUsed(s); got != math.MaxInt64 {
		t.Fatalf("F2-04: subUsed(MaxInt64, MaxInt64) = %d, want saturated MaxInt64", got)
	}
	if !subOverQuota(s) {
		t.Fatal("F2-04: double-MaxInt64 subscription must read over quota")
	}
	normal := &model.Subscription{TrafficLimit: 1000, UploadBytes: 300, DownloadBytes: 400}
	if got := subUsed(normal); got != 700 || subOverQuota(normal) {
		t.Fatalf("F2-04: normal sum changed: used=%d over=%v, want 700/false", got, subOverQuota(normal))
	}
}
