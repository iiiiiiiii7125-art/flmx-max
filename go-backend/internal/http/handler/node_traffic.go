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
// {"traffic": {"<id>": [in, out]}, "lastHour": {"<id>": <ms>}}.
// lastHour is the newest hourly bucket per node (D-06 freshness; the
// table has no updated_time column). These are whole-machine NIC
// counters from node_traffic — a different metric from tunnel billing /
// subscription quota counters; the two never reconcile and must not be
// mixed in UI.
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
		respondInternalError(w, "nodeTrafficMonth: NodeTrafficSince failed", err)
		return
	}
	out := make(map[string][2]int64, len(traffic))
	for id, v := range traffic {
		out[strconv.FormatInt(id, 10)] = v
	}
	// D-06：最新桶时刻（取失败时留空、不阻断流量返回，新鲜度为建议性）。
	lastHour := make(map[string]int64)
	if lh, lerr := h.repo.NodeTrafficLastHour(); lerr == nil {
		for id, v := range lh {
			lastHour[strconv.FormatInt(id, 10)] = v
		}
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"traffic":  out,
		"lastHour": lastHour,
	}))
}

// nodeTrafficDropped returns each node's cumulative dead-lettered
// traffic bytes (traffic audit A-中-1), for the node list:
// {"<id>": droppedBytes}. Nodes without dead letters are absent
// (implicitly 0). This is lost traffic the agent quarantined after
// its spool batch went unconfirmed — it never entered any flow
// counter, and must not be mixed with billed traffic.
func (h *Handler) nodeTrafficDropped(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	dropped, err := h.repo.ListTrafficNodeDropped()
	if err != nil {
		respondInternalError(w, "nodeTrafficDropped: ListTrafficNodeDropped failed", err)
		return
	}
	out := make(map[string]int64, len(dropped))
	for id, v := range dropped {
		out[strconv.FormatInt(id, 10)] = v
	}
	response.WriteJSON(w, response.OK(out))
}
