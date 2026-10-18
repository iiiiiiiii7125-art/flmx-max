package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// subURLSig returns the HMAC signature binding a subscription URL to a user.
// The open subscription endpoint verifies it before applying ?user= filtering,
// so the user parameter cannot be tampered with (IDOR) by anyone holding the
// subscription token.
func (h *Handler) subURLSig(userID int64) string {
	mac := hmac.New(sha256.New, []byte(h.jwtSecret))
	fmt.Fprintf(mac, "flvx-subsig-v1:%d", userID)
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (h *Handler) verifySubURLSig(userID int64, sig string) bool {
	want := h.subURLSig(userID)
	if len(sig) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1
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
		UserID      int64             `json:"userId"`
		InboundIDs  []int64           `json:"inboundIds"`
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
		"sig":      h.subURLSig(userID),
	}))
}
