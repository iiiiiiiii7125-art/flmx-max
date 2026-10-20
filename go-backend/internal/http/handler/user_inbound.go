package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// K-002: v1 签名弃用提示只记一次，避免日志刷屏。
var subSigV1DeprecationOnce sync.Once

func logSubSigV1Used() {
	subSigV1DeprecationOnce.Do(func() {
		log.Printf("warning: legacy flvx-subsig-v1 signature accepted; regenerate the subscription link to use token-bound v2")
	})
}

// subURLSig returns the HMAC signature binding a subscription URL to a user.
// The open subscription endpoint verifies it before applying ?user= filtering,
// so the user parameter cannot be tampered with (IDOR) by anyone holding the
// subscription token.
//
// K-002: v2 signatures bind the subscription token as well
// (flvx-subsig-v2:{token}:{userID}), so a sig issued for one subscription
// cannot be replayed against another subscription's token. v1 signatures
// (flvx-subsig-v1:{userID}) are still accepted for backwards compatibility,
// with a deprecation log. Existing links keep working.
func (h *Handler) subURLSig(userID int64) string {
	mac := hmac.New(sha256.New, []byte(h.jwtSecret))
	fmt.Fprintf(mac, "flvx-subsig-v1:%d", userID)
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// K-002: subURLSigForToken issues a v2 signature bound to a subscription token.
func (h *Handler) subURLSigForToken(token string, userID int64) string {
	mac := hmac.New(sha256.New, []byte(h.jwtSecret))
	fmt.Fprintf(mac, "flvx-subsig-v2:%s:%d", token, userID)
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (h *Handler) verifySubURLSig(userID int64, sig string) bool {
	// v1（遗留格式）：仅绑定 userID。
	want := h.subURLSig(userID)
	if len(sig) != len(want) {
		return false
	}
	ok := subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1
	if ok {
		// K-002: 记录弃用，提醒该链接可升级为 token 绑定的 v2 签名。
		logSubSigV1Used()
	}
	return ok
}

// verifySubURLSigForToken verifies a sig against the v2 (token-bound) format
// first, then falls back to the v1 legacy format.
func (h *Handler) verifySubURLSigForToken(token string, userID int64, sig string) bool {
	if len(sig) != 32 {
		return false
	}
	want := h.subURLSigForToken(token, userID)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1 {
		return true
	}
	// 兼容旧链接：回退 v1。
	return h.verifySubURLSig(userID, sig)
}

// getUserInbounds returns the inbound/relay assignments for a user (admin only).
// Each item: {inboundId, relayId} where relayId=0 means the inbound itself.
func (h *Handler) getUserInbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	_, roleID, err := userRoleFromRequest(r)
	if err != nil || roleID != 0 {
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
	assignments, err := h.repo.ListUserInboundAssignments(req.UserID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	type assignment struct {
		InboundID int64 `json:"inboundId"`
		RelayID   int64 `json:"relayId"`
	}
	out := make([]assignment, 0, len(assignments))
	for _, a := range assignments {
		out = append(out, assignment{InboundID: a.InboundID, RelayID: a.RelayID})
	}
	response.WriteJSON(w, response.OK(out))
}

// setUserInbounds replaces the inbound/relay assignments for a user (admin only).
// Accepts the new [{inboundId, relayId}] format; the legacy [inboundId] format
// is also accepted and treated as relayId=0 (direct).
func (h *Handler) setUserInbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	_, roleID, err := userRoleFromRequest(r)
	if err != nil || roleID != 0 {
		response.WriteJSON(w, response.Err(403, "无权限"))
		return
	}
	var raw struct {
		UserID      int64            `json:"userId"`
		InboundIDs  []int64          `json:"inboundIds"`
		Assignments []jsonAssignment `json:"assignments"`
	}
	if err := decodeJSON(r.Body, &raw); err != nil || raw.UserID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	var assignments []repo.UserInboundAssignment
	if len(raw.Assignments) > 0 {
		for _, a := range raw.Assignments {
			assignments = append(assignments, repo.UserInboundAssignment{InboundID: a.InboundID, RelayID: a.RelayID})
		}
		if err := h.repo.SetUserInboundAssignments(raw.UserID, assignments); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	} else {
		// Legacy format: plain inbound IDs (direct only).
		if err := h.repo.SetUserInbounds(raw.UserID, raw.InboundIDs); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	response.WriteJSON(w, response.OK(true))
}

type jsonAssignment struct {
	InboundID int64 `json:"inboundId"`
	RelayID   int64 `json:"relayId"`
}

// getMyInbounds returns the inbound and relay details assigned to the current user.
func (h *Handler) getMyInbounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	userID, err := userIDFromRequest(r)
	if err != nil || userID <= 0 {
		response.WriteJSON(w, response.ErrDefault("未登录"))
		return
	}
	// K-002: 可选 token 参数（前端把将要拼接的订阅 token 一并提交），
	// 以便签发与该 token 绑定的 v2 签名；缺省时绑定默认订阅 token。
	var mineReq struct {
		Token string `json:"token"`
	}
	_ = decodeJSON(r.Body, &mineReq)
	sigToken := strings.TrimSpace(mineReq.Token)
	if sigToken == "" {
		if def, derr := h.repo.DefaultSubscription(time.Now().UnixMilli()); derr == nil && def != nil {
			sigToken = def.Token
		}
	}
	assignments, err := h.repo.ListUserInboundAssignments(userID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// Fetch inbound details for the assigned IDs
	type inboundInfo struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
		NodeName string `json:"nodeName"`
	}
	type relayInfo struct {
		ID        int64  `json:"id"`
		InboundID int64  `json:"inboundId"`
		Name      string `json:"name"`
	}
	var inbounds []inboundInfo
	var relays []relayInfo
	if len(assignments) > 0 {
		rows, err := h.repo.ListNodeInbounds()
		if err == nil {
			allowedDirect := make(map[int64]bool)
			allowedRelays := make(map[int64]int64) // relayID -> inboundID
			for _, a := range assignments {
				if a.RelayID == 0 {
					allowedDirect[a.InboundID] = true
				} else {
					allowedRelays[a.RelayID] = a.InboundID
				}
			}
			// Get node names
			nodes := make(map[int64]string)
			for _, row := range rows {
				if row.Enabled != 1 {
					continue
				}
				if allowedDirect[row.ID] {
					if _, ok := nodes[row.NodeID]; !ok {
						if node, _ := h.repo.GetNodeByID(row.NodeID); node != nil {
							nodes[row.NodeID] = node.Name
						}
					}
					inbounds = append(inbounds, inboundInfo{
						ID:       row.ID,
						Name:     row.Name,
						Protocol: row.Protocol,
						NodeName: nodes[row.NodeID],
					})
				}
			}
			// Fetch relay details for assigned relays
			if len(allowedRelays) > 0 {
				relayRows, err := h.repo.ListNodeInboundRelays()
				if err == nil {
					for _, rel := range relayRows {
						if inboundID, ok := allowedRelays[rel.ID]; ok {
							relays = append(relays, relayInfo{
								ID:        rel.ID,
								InboundID: inboundID,
								Name:      rel.Name,
							})
						}
					}
				}
			}
		}
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"userId":   userID,
		"inbounds": inbounds,
		"relays":   relays,
		// K-002：优先签发 token 绑定的 v2 签名；无可用 token 时退回 v1。
		"sig": func() string {
			if sigToken != "" {
				return h.subURLSigForToken(sigToken, userID)
			}
			return h.subURLSig(userID)
		}(),
	}))
}
