package handler

import (
	"net/http"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
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
func subTraffic(hours []model.SubscriptionTraffic, rangeKey string, now time.Time) subTrafficView {
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
	b := 0
	for _, h := range hours {
		rowStart, rowEnd := h.Hour, h.Hour+hourMs
		if rowEnd <= view.Points[0].Time || rowStart >= ends[last] {
			continue
		}
		for b+1 < len(starts) && ends[b] <= rowStart {
			b++
		}
		upLeft, downLeft := h.UploadBytes, h.DownloadBytes
		for j := b; j < len(starts) && view.Points[j].Time < rowEnd; j++ {
			overlap := min(rowEnd, ends[j]) - max(rowStart, view.Points[j].Time)
			if overlap <= 0 {
				continue
			}
			up, down := upLeft, downLeft // last overlapped bucket: remainder
			if j+1 < len(starts) && view.Points[j+1].Time < rowEnd {
				up = h.UploadBytes * overlap / hourMs
				down = h.DownloadBytes * overlap / hourMs
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
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	view := subTraffic(hours, req.Range, now)
	view.Since = subTrafficSince(s)
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
			response.WriteJSON(w, response.Err(-2, err.Error()))
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
