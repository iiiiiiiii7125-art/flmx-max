package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// R4 补修 S2（R4X-024，R4-FIX-REPORT §6-2 / 批 6 E4-S1 顺查坐实）：
// federationShareDelete 经 cleanupFederationTunnels 裸级联删 Share-*
// 隧道时未做 tunnelDelete 的「删前收集 + 删后刷新」：受影响白名单
// 入站的 landing 零 KernelApply，allow 残留已删隧道入口。本用例即
// 批 6 一次性探针的正式化：修复前删后无新下发、allow 残留（红）；
// 修复后 landing 收到刷新且 allow 去掉已删隧道入口。
func TestR4S2FederationShareDeleteRefreshesWhitelist(t *testing.T) {
	s := setupR1B3Whitelist(t, 1)
	r := s.repo
	now := time.Now().UnixMilli()

	shareToken := strings.Join([]string{"r4s2", "share", "token"}, "-")
	if err := r.CreatePeerShare(&repo.PeerShare{
		Name:           "r4s2-share",
		NodeID:         s.frontID,
		Token:          shareToken,
		MaxBandwidth:   4096,
		PortRangeStart: 40000,
		PortRangeEnd:   40010,
		IsActive:       1,
		CreatedTime:    now,
		UpdatedTime:    now,
	}); err != nil {
		t.Fatalf("create peer share: %v", err)
	}
	share, err := r.GetPeerShareByToken(shareToken)
	if err != nil || share == nil {
		t.Fatalf("load peer share: %v", err)
	}
	// 共享落地隧道按联邦命名口径改名，cleanupFederationTunnels 以
	// Share-{id}-Port- 前缀认领。
	shareTunnelName := fmt.Sprintf("Share-%d-Port-20000", share.ID)
	if err := r.DB().Exec(`UPDATE tunnel SET name = ? WHERE id = ?`, shareTunnelName, s.tunnelID).Error; err != nil {
		t.Fatalf("rename tunnel to share name: %v", err)
	}

	baseline := s.allowBaseline()
	initial := s.landingRC.kernelApplyAllows()
	if len(initial) == 0 || !strings.Contains(initial[len(initial)-1], "198.51.100.1/32") {
		t.Fatalf("setup: initial allow must contain front entry 198.51.100.1/32, got %v", initial)
	}

	body, err := json.Marshal(deletePeerShareRequest{ID: share.ID})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/share/delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	s.h.federationShareDelete(res, req)
	var payload response.R
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 {
		t.Fatalf("federationShareDelete failed: code=%d msg=%q", payload.Code, payload.Msg)
	}

	// 级联删除本身不回退：隧道与转发须已删。
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, s.tunnelID); n != 0 {
		t.Fatalf("share tunnel still present after delete, rows=%d", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM forward WHERE id = ?`, s.forwardID); n != 0 {
		t.Fatalf("relay forward still present after delete, rows=%d", n)
	}

	// 删后刷新：landing 必须收到新 KernelApply，且 allow 不再含已删
	// 隧道的入口 198.51.100.1/32。
	waitForCondition(t, 5*time.Second, func() bool {
		return len(s.landingRC.kernelApplyAllows()) > baseline
	}, "KernelApply pushed to landing after federation share delete")
	fresh := s.newAllowsSince(baseline)
	last := fresh[len(fresh)-1]
	if strings.Contains(last, "198.51.100.1/32") {
		t.Fatalf("refreshed allow must drop the deleted share tunnel's entry IP, got %s", last)
	}
}
