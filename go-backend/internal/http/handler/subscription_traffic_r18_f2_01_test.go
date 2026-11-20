package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R18-F2-01: the charts multiplied first (v*overlap/spanMs), so a
// single hour bucket over ~2.56TB wrapped int64 and the chart showed
// negative or wildly wrong buckets while the stored counters stayed
// correct. A 6TB bucket is used: 6e12*1.8e6 wraps to about -7.6e18
// under the old formula, so every assertion below fails on the
// pre-fix code. Non-whole-hour zone Asia/Kolkata (+5:30) puts the
// local day boundary 30 minutes into a UTC hour bucket, which is
// what exercises the share cuts.
func TestR18F201SubTrafficHugeHourBucket(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	utcHour := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 0, 0, 0, time.UTC).UnixMilli()
	}
	now := time.Date(2026, 3, 2, 10, 30, 0, 0, loc)

	// 1. Window-start cut (non-reset hour): the bucket straddles the
	// day-view window start; its first half is outside the window
	// and must be deducted as exactly 3e12, leaving 3e12 in Feb 1.
	day := subTraffic([]model.SubscriptionTraffic{
		{Hour: utcHour(2026, 1, 31, 18), DownloadBytes: 6e12},
	}, "day", now, 0, 0, 0)
	if day.Down != 3e12 || day.Points[0].Down != 3e12 {
		t.Fatalf("window-start cut of 6TB bucket: total down=%d want 3000000000000, first bucket=%+v", day.Down, day.Points[0])
	}
	if day.Down < 0 || day.Points[0].Down < 0 {
		t.Fatalf("huge bucket must never go negative: %+v", day)
	}

	// 2. Interior day split: the bucket straddles a local midnight
	// fully inside the window; each side takes exactly half.
	day = subTraffic([]model.SubscriptionTraffic{
		{Hour: utcHour(2026, 2, 28, 18), DownloadBytes: 6e12},
	}, "day", now, 0, 0, 0)
	if day.Down != 6e12 || day.Points[27].Down != 3e12 || day.Points[28].Down != 3e12 {
		t.Fatalf("interior split of 6TB bucket: total down=%d want 6000000000000, Feb28=%+v Mar1=%+v",
			day.Down, day.Points[27], day.Points[28])
	}

	// 3. Reset-hour window-start slice: reset 1ms into the bucket
	// hour, window starts 30 minutes in. The pre-reset share is
	// 6e12*1799999/3599999 = 2999999166666, leaving 3000000833334.
	day = subTraffic([]model.SubscriptionTraffic{
		{Hour: utcHour(2026, 1, 31, 18), DownloadBytes: 6e12},
	}, "day", now, utcHour(2026, 1, 31, 18)+1, 0, 6e12)
	if day.Down != 3000000833334 || day.Points[0].Down != 3000000833334 {
		t.Fatalf("reset-hour slice of 6TB bucket: total down=%d want 3000000833334, first bucket=%+v", day.Down, day.Points[0])
	}
}
