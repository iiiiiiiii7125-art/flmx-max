package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Hours land in the viewer's local days and months; older ones are left
// out.
func TestSubTrafficBuckets(t *testing.T) {
	cst := time.FixedZone("", 8*3600)
	now := time.Date(2026, 3, 2, 10, 30, 0, 0, cst)
	at := func(y int, m time.Month, d, h int) int64 { return time.Date(y, m, d, h, 0, 0, 0, cst).UnixMilli() }
	hours := []model.SubscriptionTraffic{
		{Hour: at(2025, 1, 31, 23), DownloadBytes: 1}, // before the 12 months
		{Hour: at(2026, 2, 28, 23), DownloadBytes: 10, UploadBytes: 1},
		{Hour: at(2026, 3, 1, 0), DownloadBytes: 20},
		{Hour: at(2026, 3, 2, 9), DownloadBytes: 30},
		{Hour: at(2026, 3, 2, 10), DownloadBytes: 40, UploadBytes: 4},
	}
	hour := subTraffic(hours, "hour", now, 0, 0, 0)
	if len(hour.Points) != 24 || hour.Points[23].Down != 40 || hour.Points[22].Down != 30 || hour.Down != 70 ||
		hour.Points[0].Time != at(2026, 3, 1, 11) {
		t.Fatalf("hour: %+v", hour)
	}
	day := subTraffic(hours, "day", now, 0, 0, 0)
	if len(day.Points) != 30 || day.Points[29].Down != 70 || day.Points[28].Down != 20 || day.Points[27].Down != 10 || day.Up != 5 {
		t.Fatalf("day: %+v", day)
	}
	month := subTraffic(hours, "month", now, 0, 0, 0)
	if len(month.Points) != 12 || month.Points[11].Down != 90 || month.Points[10].Down != 10 || month.Down != 100 ||
		month.Points[0].Time != at(2025, 4, 1, 0) {
		t.Fatalf("month: %+v", month)
	}
}

// #88 用户端鉴权按我方分配模型：未分配查他人订阅 403、已分配可查、
// 管理员可查任意订阅；管理端 handler 同口径出数。
func TestUserSubscriptionTrafficGuards(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/sub_traffic_guard.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	mkSub := func(token string) *model.Subscription {
		// B-M2：起算时间落在整小时起点，否则图表按分钟份额裁剪
		// 创建小时（创建后产生的流量也会被按比例扣掉一部分），
		// 本用例只测鉴权，不测裁剪。
		s := &model.Subscription{Name: "s-" + token[:2], Kind: model.SubscriptionShare, Token: token,
			UUID: "u-" + token[:2], Password: "p", Enabled: 1, CreatedTime: now - now%int64(time.Hour/time.Millisecond), UpdatedTime: now}
		if err := r.SaveSubscription(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	assigned := mkSub(strings.Repeat("a", 32))
	other := mkSub(strings.Repeat("b", 32))
	if err := r.SetUserSubscriptions(2, []int64{assigned.ID}); err != nil {
		t.Fatal(err)
	}
	// FIX132-C 订正（A3-04）：首样本只建基线不计入，先落零值基线，
	// 再以第二笔样本产生增量；下方鉴权与出数断言不变。
	if err := r.ApplySubscriptionCounterSamples(1, "e", []repo.SubscriptionCounterSample{
		{SubscriptionID: assigned.ID, Direction: "up", Bytes: 0},
		{SubscriptionID: assigned.ID, Direction: "down", Bytes: 0},
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := r.ApplySubscriptionCounterSamples(1, "e", []repo.SubscriptionCounterSample{
		{SubscriptionID: assigned.ID, Direction: "up", Bytes: 100},
		{SubscriptionID: assigned.ID, Direction: "down", Bytes: 200},
	}, now); err != nil {
		t.Fatal(err)
	}

	type trafficEnv struct {
		Code int            `json:"code"`
		Data subTrafficView `json:"data"`
	}
	call := func(userFn func(w http.ResponseWriter, r *http.Request), userID int64, roleID int, subID int64) trafficEnv {
		req := httptest.NewRequest("POST", "/api/v1/user/subscription/traffic",
			strings.NewReader(fmt.Sprintf(`{"id":%d,"range":"hour","tzOffset":480}`, subID)))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: fmt.Sprint(userID), RoleID: roleID})
		rec := httptest.NewRecorder()
		userFn(rec, req.WithContext(ctx))
		var env trafficEnv
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode traffic response: %v (body=%s)", err, rec.Body.String())
		}
		return env
	}

	// 已分配用户查自己的订阅：出数且合计与记账一致。
	env := call(h.userSubscriptionTraffic, 2, 1, assigned.ID)
	if env.Code != 0 || env.Data.Range != "hour" || len(env.Data.Points) != 24 ||
		env.Data.Up != 100 || env.Data.Down != 200 {
		t.Fatalf("assigned user traffic: %+v", env)
	}
	// 未分配用户查他人订阅：403。
	if env := call(h.userSubscriptionTraffic, 2, 1, other.ID); env.Code != 403 {
		t.Fatalf("unassigned user querying other's subscription must be 403, got %+v", env)
	}
	// 无任何分配的用户查已分配给别人的订阅：403。
	if env := call(h.userSubscriptionTraffic, 3, 1, assigned.ID); env.Code != 403 {
		t.Fatalf("user without assignment must be 403, got %+v", env)
	}
	// 管理员查任意订阅：放行。
	if env := call(h.userSubscriptionTraffic, 1, 0, other.ID); env.Code != 0 {
		t.Fatalf("admin via user endpoint must pass, got %+v", env)
	}
	// 管理端 handler 出数口径一致。
	env = call(h.subscriptionTraffic, 1, 0, assigned.ID)
	if env.Code != 0 || env.Data.Up != 100 || env.Data.Down != 200 {
		t.Fatalf("admin endpoint traffic: %+v", env)
	}
}

// B-M2：图表按 traffic_since 裁剪，重置前的小时行不计入合计；
// 重置小时取精确值（订阅行计数器 − 其后整桶之和），不是分钟份额。
func TestSubTrafficResetClipsPreResetBuckets(t *testing.T) {
	cst := time.FixedZone("", 8*3600)
	now := time.Date(2026, 3, 2, 10, 30, 0, 0, cst)
	at := func(y int, m time.Month, d, h int) int64 { return time.Date(y, m, d, h, 0, 0, 0, cst).UnixMilli() }
	atMin := func(y int, m time.Month, d, h, min int) int64 {
		return time.Date(y, m, d, h, min, 0, 0, cst).UnixMilli()
	}
	hours := []model.SubscriptionTraffic{
		{Hour: at(2026, 2, 28, 23), DownloadBytes: 10, UploadBytes: 1},
		{Hour: at(2026, 3, 1, 0), DownloadBytes: 20},
		{Hour: at(2026, 3, 2, 9), DownloadBytes: 30},
		{Hour: at(2026, 3, 2, 10), DownloadBytes: 40, UploadBytes: 4},
	}
	// 重置在 3/2 09:30，订阅行计数器（重置后真实流量）为下行 70 /
	// 上行 4：09:00 行的 30 下行全在重置后发生（突发流量），10:00
	// 行 40 下行 / 4 上行也在重置后。分钟份额会把 09:00 行算成
	// 15，精确值必须是 30。
	since := atMin(2026, 3, 2, 9, 30)
	hour := subTraffic(hours, "hour", now, since, 4, 70)
	if hour.Down != 70 || hour.Up != 4 {
		t.Fatalf("hour after reset: up=%d down=%d, want 4/70", hour.Up, hour.Down)
	}
	if hour.Points[22].Down != 30 || hour.Points[23].Down != 40 || hour.Points[23].Up != 4 {
		t.Fatalf("reset-hour must be exact, not minute-share: 09:00=%+v 10:00=%+v", hour.Points[22], hour.Points[23])
	}
	if hour.Points[0].Down != 0 || hour.Points[21].Down != 0 {
		t.Fatalf("pre-reset buckets must be zero: %+v", hour.Points[:23])
	}
	// 日视图同口径：3/1 及更早的桶归零，当天只剩重置后份额。
	day := subTraffic(hours, "day", now, since, 4, 70)
	if day.Down != 70 || day.Up != 4 || day.Points[27].Down != 0 || day.Points[28].Down != 0 || day.Points[29].Down != 70 {
		t.Fatalf("day after reset: up=%d down=%d points[27..29]=%+v", day.Up, day.Down, day.Points[27:])
	}
	// 重置早于窗口起点：与旧口径一致，不裁剪（小时窗内只有
	// 09:00/10:00 两行，合计 70 下行 / 4 上行）。
	old := subTraffic(hours, "hour", now, at(2026, 2, 1, 0), 5, 100)
	if old.Down != 70 || old.Up != 4 {
		t.Fatalf("reset before window must not clip: up=%d down=%d", old.Up, old.Down)
	}
	// 重置在 now 之后：全部归零。
	future := subTraffic(hours, "hour", now, atMin(2026, 3, 2, 11, 0), 0, 0)
	if future.Down != 0 || future.Up != 0 {
		t.Fatalf("reset after now must zero everything: up=%d down=%d", future.Up, future.Down)
	}
}

// B-M2：窗口起点落在重置小时内的切片（仅非整小时时区出现）：
// 重置小时的精确值按其自身区间 [since, 行尾) 比例扣除窗外部分，
// 总量仍精确守恒。
func TestSubTrafficResetHourWindowStartSlice(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 3, 2, 10, 30, 0, 0, loc)
	utcHour := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 0, 0, 0, time.UTC).UnixMilli()
	}
	utcMin := func(y int, m time.Month, d, h, min int) int64 {
		return time.Date(y, m, d, h, min, 0, 0, time.UTC).UnixMilli()
	}
	// 重置在 1/31 18:15 UTC（本地 2/1 00:15 前夜）；日视图首桶
	// 2/1 00:00 本地 = UTC 1/31 18:30，落在重置小时（18:00–19:00）
	// 内、重置时刻之后。行计数器下行 80：重置小时精确值 70
	//（80 − 其后 2/5 行的 10），窗外 15 分钟扣 70×15/45=23，
	// 窗内 47 全进 2/1 桶。
	hours := []model.SubscriptionTraffic{
		{Hour: utcHour(2026, 1, 31, 18), DownloadBytes: 90},
		{Hour: utcHour(2026, 2, 5, 10), DownloadBytes: 10},
	}
	day := subTraffic(hours, "day", now, utcMin(2026, 1, 31, 18, 15), 0, 80)
	if day.Down != 57 || day.Points[0].Down != 47 {
		t.Fatalf("window-start slice of reset hour: total down=%d want 57, first bucket=%+v", day.Down, day.Points[0])
	}
}

// B-M2 端到端：重置后管理端图表合计精确归零（与订阅行「已用 0」
// 一致：重置前份额不再计入合计），重置后新产生的流量按精确值
// 计入图表，与订阅行计数器逐值一致。
func TestSubscriptionResetTrafficClearsChartTotals(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/sub_traffic_reset_e2e.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nowMs := time.Now().UnixMilli()
	hourStart := nowMs - nowMs%int64(time.Hour/time.Millisecond)
	nextHour := hourStart + int64(time.Hour/time.Millisecond)

	// B-M2：起算时间取整小时起点，否则图表按分钟份额裁剪创建小时，
	// 重置前的断言（100/200）会被按比例扣掉——裁剪本身由
	// TestSubTrafficResetClipsPreResetBuckets 覆盖。
	s := &model.Subscription{Name: "s-reset", Kind: model.SubscriptionShare, Token: strings.Repeat("r", 32),
		UUID: "u-reset", Password: "p", Enabled: 1, CreatedTime: hourStart, UpdatedTime: nowMs, TrafficSince: hourStart}
	if err := r.SaveSubscription(s); err != nil {
		t.Fatal(err)
	}
	seed := func(at int64, up, down int64) {
		if err := r.ApplySubscriptionCounterSamples(1, "e", []repo.SubscriptionCounterSample{
			{SubscriptionID: s.ID, Direction: "up", Bytes: up},
			{SubscriptionID: s.ID, Direction: "down", Bytes: down},
		}, at); err != nil {
			t.Fatalf("seed at %d: %v", at, err)
		}
	}
	seed(nowMs, 0, 0)       // 基线
	seed(nowMs+1000, 100, 200) // 本小时桶 +100/+200

	call := func() subTrafficView {
		req := httptest.NewRequest("POST", "/api/v1/subscription/traffic",
			strings.NewReader(fmt.Sprintf(`{"id":%d,"range":"hour","tzOffset":0}`, s.ID)))
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: "1", RoleID: 0})
		rec := httptest.NewRecorder()
		h.subscriptionTraffic(rec, req.WithContext(ctx))
		var env struct {
			Code int           `json:"code"`
			Data subTrafficView `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode traffic response: %v (body=%s)", err, rec.Body.String())
		}
		if env.Code != 0 {
			t.Fatalf("traffic call failed: %+v", env)
		}
		return env.Data
	}
	before := call()
	if before.Up != 100 || before.Down != 200 {
		t.Fatalf("before reset: up=%d down=%d, want 100/200", before.Up, before.Down)
	}

	// 重置在本小时内：重置小时取精确值——重置后计数器为 0，其后
	// 无整桶，精确值 0，图表合计精确归零（不是分钟份额余量）。
	// 若离整点不足 10 秒则等下一小时再取时刻，避免重置跨整点。
	nowMs = time.Now().UnixMilli()
	hourStart = nowMs - nowMs%int64(time.Hour/time.Millisecond)
	nextHour = hourStart + int64(time.Hour/time.Millisecond)
	if nextHour-nowMs < 10000 {
		time.Sleep(time.Duration(nextHour-nowMs+1000) * time.Millisecond)
		nowMs = time.Now().UnixMilli()
		hourStart = nowMs - nowMs%int64(time.Hour/time.Millisecond)
	}
	resetMs := nowMs
	if err := r.ResetSubscriptionTraffic(s.ID, resetMs); err != nil {
		t.Fatal(err)
	}
	after := call()
	if after.Up != 0 || after.Down != 0 || after.Since != resetMs {
		t.Fatalf("after reset: up=%d down=%d since=%d, want 0/0/%d",
			after.Up, after.Down, after.Since, resetMs)
	}
	var sub model.Subscription
	if err := r.DB().First(&sub, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	if sub.UploadBytes != 0 || sub.DownloadBytes != 0 {
		t.Fatalf("subscription counters after reset = %d/%d, want 0/0", sub.UploadBytes, sub.DownloadBytes)
	}

	// 重置后新流量：桶总量 150/300，图表取精确值 50/100，与订阅
	// 行计数器（只计重置后增量）逐值一致。
	seed(nowMs+3000, 150, 300)
	afterNew := call()
	if afterNew.Up != 50 || afterNew.Down != 100 {
		t.Fatalf("after new traffic: up=%d down=%d, want 50/100", afterNew.Up, afterNew.Down)
	}
	if err := r.DB().First(&sub, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	if sub.UploadBytes != 50 || sub.DownloadBytes != 100 {
		t.Fatalf("subscription counters after new traffic = %d/%d, want 50/100", sub.UploadBytes, sub.DownloadBytes)
	}
}
