package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// A6-002/BC-12：非整小时时区的本地零点落在 UTC 整点小时行中间，该行
// 必须按分钟比例拆进相邻两日（总量守恒），而不是按行起点整行归入
// 前一日。Asia/Kolkata = UTC+5:30，本地零点 = UTC 18:30。
func TestSubTrafficKolkataDayBoundarySplit(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 3, 2, 10, 30, 0, 0, loc)
	utcHour := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 0, 0, 0, time.UTC).UnixMilli()
	}
	hours := []model.SubscriptionTraffic{
		// UTC 18:00–19:00 = 本地 23:30–00:30，横跨 3/1 与 3/2 两日。
		{Hour: utcHour(2026, 3, 1, 18), DownloadBytes: 60, UploadBytes: 30},
		// UTC 19:00–20:00 完整落在 3/2。
		{Hour: utcHour(2026, 3, 1, 19), DownloadBytes: 10},
	}
	day := subTraffic(hours, "day", now)
	if len(day.Points) != 30 {
		t.Fatalf("points: %d", len(day.Points))
	}
	// 末桶为 3/2、次末桶为 3/1（本地日界 = UTC 前日 18:30）。
	if got := time.UnixMilli(day.Points[29].Time).UTC(); got.Hour() != 18 || got.Minute() != 30 {
		t.Fatalf("day bucket must start at local midnight (18:30 UTC): %v", got)
	}
	if day.Points[28].Down != 30 || day.Points[29].Down != 30+10 {
		t.Fatalf("straddling hour row must split 30/30 across local days: %+v", day.Points[27:])
	}
	if day.Points[28].Up != 15 || day.Points[29].Up != 15 {
		t.Fatalf("upload must split likewise: %+v", day.Points[27:])
	}
	if day.Down != 70 || day.Up != 30 {
		t.Fatalf("totals must conserve: down=%d up=%d", day.Down, day.Up)
	}
}

// A6-002/BC-12：DST 切换周的历史日期必须按各自当日的实际偏移算桶，
// 而不是全程套用查看者当前单一偏移（旧 FixedZone 口径）。经请求参数
// 解析出的 IANA 位置驱动分桶。
func TestSubTrafficDSTBucketStarts(t *testing.T) {
	loc := subTrafficLocation(subTrafficRequest{TZ: "America/New_York", TZOffset: -240})
	if loc.String() != "America/New_York" {
		t.Fatalf("IANA name must win over the numeric offset: %v", loc)
	}
	// 2026-03-08 美国春季拨快：3/8 零点仍是 EST（UTC-5 → 05:00 UTC），
	// 3/9 零点已是 EDT（UTC-4 → 04:00 UTC）。
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)
	starts := subTrafficBuckets("day", now)
	byDay := map[int]int64{}
	for _, s := range starts {
		byDay[s.Day()] = s.UnixMilli()
	}
	want8 := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC).UnixMilli()
	want9 := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC).UnixMilli()
	if byDay[8] != want8 || byDay[9] != want9 {
		t.Fatalf("DST week bucket starts: 3/8=%v 3/9=%v", time.UnixMilli(byDay[8]).UTC(), time.UnixMilli(byDay[9]).UTC())
	}

	// 回落口径不变：无 IANA 名或名字无效时用数值偏移（旧客户端兼容）。
	if got := subTrafficLocation(subTrafficRequest{TZOffset: 480}); got.String() == "America/New_York" {
		t.Fatal("empty TZ must fall back to the numeric offset")
	}
	bad := subTrafficLocation(subTrafficRequest{TZ: "Not/AZone", TZOffset: 480})
	probe := time.Date(2026, 3, 2, 0, 0, 0, 0, bad)
	if probe.UTC().Hour() != 16 {
		t.Fatalf("invalid TZ must fall back to UTC+8: %v", probe.UTC())
	}
	// UTC+8 用户无感：整小时时区无跨界行，IANA 与旧口径结果一致。
	cst, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	nowCST := time.Date(2026, 3, 2, 10, 30, 0, 0, cst)
	hours := []model.SubscriptionTraffic{
		{Hour: time.Date(2026, 3, 1, 16, 0, 0, 0, time.UTC).UnixMilli(), DownloadBytes: 42}, // 本地 3/2 00:00
	}
	day := subTraffic(hours, "day", nowCST)
	if day.Points[29].Down != 42 || day.Down != 42 {
		t.Fatalf("whole-hour zones must be unaffected: %+v", day.Points[29])
	}
}
