package handler

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// userSubscriptionList 返回某用户被分配的订阅 ID 列表（仅管理员）。
// 1.0.12 起"订阅分配"按订阅（而非入站）绑定用户。
func (h *Handler) userSubscriptionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if _, roleID, err := userRoleFromRequest(r); err != nil || roleID != 0 {
		response.WriteJSON(w, response.Err(403, "无权限"))
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.UserID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	ids, err := h.repo.ListUserSubscriptionIDs(req.UserID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if ids == nil {
		ids = []int64{}
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"userId":          req.UserID,
		"subscriptionIds": ids,
	}))
}

// userSubscriptionSet 全量替换某用户的订阅分配（仅管理员）。
// 与旧的按入站分配不同：订阅 ID 必须真实存在，不存在的 ID 直接报错，
// 避免静默丢弃导致管理员误以为已分配。
func (h *Handler) userSubscriptionSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if _, roleID, err := userRoleFromRequest(r); err != nil || roleID != 0 {
		response.WriteJSON(w, response.Err(403, "无权限"))
		return
	}
	var req struct {
		UserID          int64   `json:"userId"`
		SubscriptionIDs []int64 `json:"subscriptionIds"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.UserID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	seen := make(map[int64]bool, len(req.SubscriptionIDs))
	for _, id := range req.SubscriptionIDs {
		if id <= 0 {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("订阅 ID 无效: %d", id)))
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		sub, err := h.repo.GetSubscription(id)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if sub == nil {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("订阅不存在（ID %d）", id)))
			return
		}
	}
	if err := h.repo.SetUserSubscriptions(req.UserID, req.SubscriptionIDs); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(true))
}

// userSubscriptionMineView 是"我的订阅"页的精简订阅视图。
type userSubscriptionMineView struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	NodeCount     int    `json:"nodeCount"`
	UploadBytes   int64  `json:"uploadBytes"`
	DownloadBytes int64  `json:"downloadBytes"`
	TrafficLimit  int64  `json:"trafficLimit"`
	ExpireTime    int64  `json:"expireTime"`
	OverQuota     bool   `json:"overQuota"`
	Enabled       bool   `json:"enabled"`
}

// userSubscriptionMine 返回当前登录用户被分配的订阅（任意登录用户，仅自己的）。
func (h *Handler) userSubscriptionMine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	userID, err := userIDFromRequest(r)
	if err != nil || userID <= 0 {
		response.WriteJSON(w, response.ErrDefault("未登录"))
		return
	}
	// 请求体可选：format 指定 NodeCount 的目标格式口径（A5-02/BC-11）。
	var mineReq struct {
		Format string `json:"format"`
	}
	_ = decodeJSON(r.Body, &mineReq)
	subs, err := h.repo.ListUserSubscriptions(userID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// FIX2-B（V5-1）：计数须按当前用户的入站分配收紧——该页主链接是
	// 带 ?user=&sig= 的个性化链接，拉取侧按 UserInboundAssignment
	// 过滤端点；此前 mine 以全量 rows 计数，部分分配用户看到的节点
	// 数大于其链接实得。与拉取侧同口径：查询失败时按空分配计 0
	// （拉取侧同为失败即空集），不回退全量口径以免高估。
	assignments, aerr := h.repo.ListUserInboundAssignments(userID)
	if aerr != nil {
		log.Printf("userSubscriptionMine: list assignments for user %d failed, counting as none: %v", userID, aerr)
		assignments = []repo.UserInboundAssignment{}
	}
	if assignments == nil {
		assignments = []repo.UserInboundAssignment{}
	}
	now := time.Now()
	items := make([]userSubscriptionMineView, 0, len(subs))
	for i := range subs {
		var s *model.Subscription = &subs[i]
		// 复用 subscriptionView 保证 path 与 nodeCount（中转过滤后）口径一致。
		v := h.subscriptionViewForAssignments(s, rows, now, mineReq.Format, assignments)
		items = append(items, userSubscriptionMineView{
			ID: v.ID, Name: v.Name, Path: v.Path, NodeCount: v.NodeCount,
			UploadBytes: v.UploadBytes, DownloadBytes: v.DownloadBytes,
			TrafficLimit: v.TrafficLimit, ExpireTime: v.ExpireTime,
			OverQuota: v.OverQuota, Enabled: v.Enabled,
		})
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"subscriptions": items,
	}))
}
