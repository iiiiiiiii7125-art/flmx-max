package handler

import (
	"net/http"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Subscription traffic history: hourly rows (model.SubscriptionTraffic)
// summed into the last 24 hours, 30 days or 12 months, in the viewer's
// time zone.

// subTrafficRetention keeps a little over a year, for the monthly view.
const subTrafficRetention = 400 * 24 * time.Hour

type subTrafficRequest struct {
	ID    int64  `json:"id"`
	Range string `json:"range"`
	// TZOffset is the viewer's offset from UTC in minutes (UTC+8 = 480).
	TZOffset int `json:"tzOffset"`
	// TZ is the viewer's IANA time zone name (A6-002/BC-12). When it
	// resolves, bucket boundaries use each date's real offset (DST-safe,
	// correct for non-whole-hour zones); TZOffset remains the fallback
	// for older clients.
	TZ string `json:"tz"`
}

// subTrafficLocation resolves the viewer's location: the IANA name when
// it loads, else the fixed numeric offset (the pre-BC-12 behavior).
func subTrafficLocation(req subTrafficRequest) *time.Location {
	if req.TZ != "" {
		if loc, err := time.LoadLocation(req.TZ); err == nil {
			return loc
		}
	}
	offset := req.TZOffset
	if offset < -14*60 || offset > 14*60 {
		offset = 0
	}
	return time.FixedZone("", offset*60)
}

type subTrafficPoint struct {
	// Time is the unix ms the bucket starts at.
	Time int64 `json:"time"`
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type subTrafficView struct {
	Range  string            `json:"range"`
	Points []subTrafficPoint `json:"points"`
	Up     int64             `json:"up"`
	Down   int64             `json:"down"`
	// Since is when the subscription's used-traffic counters started
	// counting (creation or last reset); the chart marks it so the
	// "used" figure and the chart range are not compared directly.
	Since int64 `json:"since"`
	// LastReportedAt is the newest SubscriptionCounter update (ms;
	// 0 = never reported) — D-06 freshness for the graph: without it
	// a frozen chart is indistinguishable from a fresh one.
	LastReportedAt int64 `json:"lastReportedAt,omitempty"`
}

// subTrafficBuckets returns the bucket starts of a range ending at now:
// 24 hours, 30 days or 12 months.
func subTrafficBuckets(rangeKey string, now time.Time) []time.Time {
	var out []time.Time
	switch rangeKey {
	case "day":
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		for i := 29; i >= 0; i-- {
			out = append(out, today.AddDate(0, 0, -i))
		}
	case "month":
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		for i := 11; i >= 0; i-- {
			out = append(out, month.AddDate(0, -i, 0))
		}
	default:
		hour := now.Truncate(time.Hour)
		for i := 23; i >= 0; i-- {
			out = append(out, hour.Add(-time.Duration(i)*time.Hour))
		}
	}
	return out
}

// subTraffic sums a subscription's hours into the range's buckets.
// Hour rows are UTC-aligned; in a non-whole-hour zone a local day (or a
// DST-shifted one) starts mid-row, so a row straddling a bucket boundary
// is split between the two buckets by minute share instead of being
// dumped whole into the earlier one (A6-002/BC-12). Shares are floored
// per bucket with the remainder going to the last overlapped bucket, so
// totals conserve exactly. Whole-hour zones never straddle and are
// byte-for-byte the pre-BC-12 result.
//
// A row straddling the WINDOW START gets the same treatment at its
// out-of-window edge (R3-C-01): the pre-window share is deducted up
// front by the remainder method — counted = value − floor(value ×
// pre-window minutes / 60), the exact formula NodeTrafficSince
// applies to its boundary bucket (BC-12) — instead of the whole row
// landing in the first bucket. Only the display is affected; stored
// buckets and the cumulative counters are untouched.
//
// B-M2: sinceMs is the subscription's traffic_since (creation or last
// reset). The effective floor is max(window start, sinceMs), so rows
// predating the reset contribute nothing — the chart totals match the
// subscription's "used" counters, per doc/guide/subscription.md
// ("起算之前的时段不在统计内"). Hourly rows stay in the database:
// the monthly auto-reset flows through the same ResetSubscriptionTraffic,
// and deleting rows there would wipe the 12-month history every month.
//
// The reset hour itself is exact, not a minute-share estimate: the
// subscription's counters (rowUp/rowDown) are the true post-since
// totals and are booked in the same transaction as the buckets (#88),
// so the reset hour's post-reset share is rowTotal minus the sum of
// the whole buckets after the reset hour. A minute-share split would
// undercount bursty traffic (e.g. 50MB in 30s right after a reset
// would mostly read as pre-reset) and would leave most of the reset
// hour visible when the reset lands early in the hour — the exact
// complaint of B-M2. Rows after the reset hour keep the existing
// minute-share treatment at bucket/window boundaries (R3-C-01/BC-12);
// only the reset hour is overridden.
func subTraffic(hours []model.SubscriptionTraffic, rangeKey string, now time.Time, sinceMs, rowUp, rowDown int64) subTrafficView {
	if rangeKey != "day" && rangeKey != "month" {
		rangeKey = "hour"
	}
	starts := subTrafficBuckets(rangeKey, now)
	view := subTrafficView{Range: rangeKey, Points: make([]subTrafficPoint, len(starts))}
	ends := make([]int64, len(starts))
	for i, s := range starts {
		view.Points[i].Time = s.UnixMilli()
		if i+1 < len(starts) {
			ends[i] = starts[i+1].UnixMilli()
		}
	}
	last := len(starts) - 1
	switch rangeKey {
	case "day":
		ends[last] = starts[last].AddDate(0, 0, 1).UnixMilli()
	case "month":
		ends[last] = starts[last].AddDate(0, 1, 0).UnixMilli()
	default:
		ends[last] = starts[last].Add(time.Hour).UnixMilli()
	}
	const hourMs = int64(time.Hour / time.Millisecond)
	// B-M2: floor is the later of the window start and the reset instant.
	effStart := view.Points[0].Time
	if sinceMs > effStart {
		effStart = sinceMs
	}
	// B-M2 exact reset-hour share: the reset hour's post-reset bytes
	// are the counters minus the whole buckets after that hour.
	var resetHour int64 = -1
	var exactUp, exactDown int64
	if sinceMs > 0 {
		resetHour = sinceMs - sinceMs%hourMs
		var afterUp, afterDown int64
		for _, h := range hours {
			if h.Hour > resetHour {
				afterUp += h.UploadBytes
				afterDown += h.DownloadBytes
			}
		}
		exactUp = max(0, rowUp-afterUp)
		exactDown = max(0, rowDown-afterDown)
	}
	b := 0
	for _, h := range hours {
		rowStart, rowEnd := h.Hour, h.Hour+hourMs
		if rowEnd <= effStart || rowStart >= ends[last] {
			continue
		}
		for b+1 < len(starts) && ends[b] <= rowStart {
			b++
		}
		// B-M2: the reset hour counts its exact post-reset share;
		// every other row keeps the remainder-method treatment.
		// The exact share covers [sinceMs, rowEnd), so a window
		// start inside that interval deducts proportionally to
		// that interval — not to the full hour.
		valUp, valDown := h.UploadBytes, h.DownloadBytes
		isResetHour := h.Hour == resetHour
		spanMs := hourMs
		if isResetHour {
			valUp, valDown = exactUp, exactDown
			spanMs = rowEnd - sinceMs
		}
		upLeft, downLeft := valUp, valDown
		if pre := effStart - rowStart; pre > 0 {
			if isResetHour {
				if preSince := effStart - sinceMs; preSince > 0 {
					upLeft -= repo.MulDivSaturating(valUp, preSince, spanMs)
					downLeft -= repo.MulDivSaturating(valDown, preSince, spanMs)
				}
			} else {
				// 窗外/重置前份额先行扣除（R3-C-01，余量法）：一小时行至多
				// 横跨窗口起点一次，扣除后余量即窗内份额，后续分桶
				// 逻辑（首桶取余量）自动精确。窗口内跨桶行的分摊
				// 不受影响，总量在窗内守恒。
				upLeft -= repo.MulDivSaturating(h.UploadBytes, pre, hourMs)
				downLeft -= repo.MulDivSaturating(h.DownloadBytes, pre, hourMs)
			}
		}
		for j := b; j < len(starts) && view.Points[j].Time < rowEnd; j++ {
			// B-M2: a bucket starting before the reset instant only
			// counts from the reset instant on.
			from := max(rowStart, view.Points[j].Time)
			if from < effStart {
				from = effStart
			}
			overlap := min(rowEnd, ends[j]) - from
			if overlap <= 0 {
				continue
			}
			up, down := upLeft, downLeft // last overlapped bucket: remainder
			if j+1 < len(starts) && view.Points[j+1].Time < rowEnd {
				up = repo.MulDivSaturating(valUp, overlap, spanMs)
				down = repo.MulDivSaturating(valDown, overlap, spanMs)
				upLeft -= up
				downLeft -= down
			}
			view.Points[j].Up += up
			view.Points[j].Down += down
			view.Up += up
			view.Down += down
		}
	}
	return view
}

func (h *Handler) respondSubTraffic(w http.ResponseWriter, s *model.Subscription, req subTrafficRequest) {
	now := time.Now().In(subTrafficLocation(req))
	starts := subTrafficBuckets(req.Range, now)
	// One extra hour back: a boundary-straddling row starts before the
	// first bucket in non-whole-hour zones and part of it belongs here.
	hours, err := h.repo.ListSubscriptionTraffic(s.ID, starts[0].UnixMilli()-int64(time.Hour/time.Millisecond))
	if err != nil {
		// RR4-C1-01：本出口用户面与管理员面共用，普通用户可达，
		// 仓储错误不得原文直出。
		respondInternalError(w, "respondSubTraffic: ListSubscriptionTraffic failed", err)
		return
	}
	view := subTraffic(hours, req.Range, now, subTrafficSince(s), s.UploadBytes, s.DownloadBytes)
	view.Since = subTrafficSince(s)
	// D-06：回填计数最近上报时刻（0＝从未上报），图表据此显示新鲜度。
	if updates, err := h.repo.LastSubscriptionCounterUpdates([]int64{s.ID}); err == nil {
		view.LastReportedAt = updates[s.ID]
	}
	response.WriteJSON(w, response.OK(view))
}

// subscriptionTraffic is the admin history view (route is under the
// /api/v1/subscription/ admin prefix in middleware).
func (h *Handler) subscriptionTraffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req subTrafficRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	s, err := h.repo.GetSubscription(req.ID)
	if err != nil || s == nil {
		response.WriteJSON(w, response.ErrDefault("订阅不存在"))
		return
	}
	h.respondSubTraffic(w, s, req)
}

// userSubscriptionTraffic is the same history for one of the caller's
// assigned subscriptions. 与上游属主模型不同：我方订阅没有属主字段，
// 按 UserSubscription 分配表校验——被分配该订阅的用户或管理员才放行。
// 曲线是整条订阅的合计流量（凭据共用、计数拆不开），不是个人流量。
func (h *Handler) userSubscriptionTraffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	uid, roleID, err := userRoleFromRequest(r)
	if err != nil || uid <= 0 {
		response.WriteJSON(w, response.ErrDefault("未登录"))
		return
	}
	var req subTrafficRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if roleID != 0 {
		ids, err := h.repo.ListUserSubscriptionIDs(uid)
		if err != nil {
			respondInternalError(w, "userSubscriptionTraffic: ListUserSubscriptionIDs failed", err)
			return
		}
		assigned := false
		for _, id := range ids {
			if id == req.ID {
				assigned = true
				break
			}
		}
		if !assigned {
			response.WriteJSON(w, response.Err(403, "无权限"))
			return
		}
	}
	s, err := h.repo.GetSubscription(req.ID)
	if err != nil || s == nil {
		response.WriteJSON(w, response.ErrDefault("订阅不存在"))
		return
	}
	h.respondSubTraffic(w, s, req)
}
