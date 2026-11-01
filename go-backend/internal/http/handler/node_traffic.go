package handler

import (
	"net/http"
	"strconv"
	"time"

	"go-backend/internal/http/response"
)

// nodeTrafficMonthStart returns local midnight on the 1st of now's
// month in loc. With an IANA location the 1st resolves with its own
// UTC offset, so a DST change later in the month does not shift the
// window start.
func nodeTrafficMonthStart(now time.Time, loc *time.Location) time.Time {
	n := now.In(loc)
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
}

// nodeTrafficMonth returns each node's NIC traffic since the 1st of
// this month in the viewer's time zone, for the node list:
// {"<id>": [in, out]}. These are whole-machine NIC counters from
// node_traffic — a different metric from tunnel billing / subscription
// quota counters; the two never reconcile and must not be mixed in UI.
func (h *Handler) nodeTrafficMonth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		TZOffset int `json:"tzOffset"`
		// TZ is the viewer's IANA time zone name (R4B10-001). When it
		// resolves, the month start carries the 1st's own UTC offset
		// (DST-safe); TZOffset remains the fallback for older clients.
		// Resolution is shared with the subscription traffic view.
		TZ string `json:"tz"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	loc := subTrafficLocation(subTrafficRequest{TZOffset: req.TZOffset, TZ: req.TZ})
	month := nodeTrafficMonthStart(time.Now(), loc)
	traffic, err := h.repo.NodeTrafficSince(month.UnixMilli())
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	out := make(map[string][2]int64, len(traffic))
	for id, v := range traffic {
		out[strconv.FormatInt(id, 10)] = v
	}
	response.WriteJSON(w, response.OK(out))
}
