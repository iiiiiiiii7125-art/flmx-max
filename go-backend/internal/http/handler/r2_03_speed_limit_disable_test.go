package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R2-03 回归：限速规则禁用（status 1→0）此前只 resync 服务（新口
// 径不再含已禁用规则的 limiter 名单），节点侧已落盘的 limiter
// 对象无人删——LIVE 实测：禁用规则 15 后条目
// rule_traffic_limit_96 在 300s 内每 10s 采样全程在场，触发链
// （speedLimitUpdate/resync/cleanOrphanedLimiters）零动作。禁
// 用面现与删除面 A1-02 同款：resync 后按名补发 DeleteLimiters
// 清理节点侧条目；重启用时 resync 重推即重建，故清理幂等无害。
func TestR203SpeedLimitDisableSweepsNodeLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 43, 30)
	// F1 以规则 43 作总速；F2 以规则 43 作 IP 限速（专属条目名按转发 ID）。
	totalForward := seedA102Forward(t, a, tunnelID, "r2-03-total", 22103, int64(43), nil)
	ipForward := seedA102Forward(t, a, tunnelID, "r2-03-ip", 22104, nil, int64(43))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/speed-limit/update", strings.NewReader(`{"id":43,"status":0}`))
	rec := httptest.NewRecorder()
	a.h.speedLimitUpdate(rec, req)
	a.probe(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("speed limit update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Code != 0 {
		t.Fatalf("speed limit update envelope = %s (err %v)", rec.Body.String(), err)
	}
	names := a102DeleteLimiterNames(t, a)
	// 两条受影响转发同在节点 1：规则条目按（节点，名）去重，只发一次。
	if got := a102NameCount(names, "43"); got != 1 {
		t.Fatalf("expected exactly 1 DeleteLimiters for rule 43, got %d (all: %v)", got, names)
	}
	if got := a102NameCount(names, fmt.Sprintf("rule_traffic_limit_%d", ipForward.ID)); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for the IP-referencing forward's traffic limiter, got %d (all: %v)", got, names)
	}
	if got := a102NameCount(names, fmt.Sprintf("rule_traffic_limit_%d", totalForward.ID)); got != 0 {
		t.Fatalf("total-only forward has no traffic limiter entry, got %d (all: %v)", got, names)
	}
	// 禁用 ≠ 删除：规则行仍在，仅 status 落 0。
	rule, err := a.h.repo.GetSpeedLimit(43)
	if err != nil || rule == nil {
		t.Fatalf("rule 43 must survive disable, rule=%v err=%v", rule, err)
	}
	if rule.Status != 0 {
		t.Fatalf("rule 43 status = %d, want 0", rule.Status)
	}
}

// 禁用面反向对照：仅改值（status 仍 1）不触发清理——resync 重推
// 即按新值重建 limiter，无孤儿可清。
func TestR203SpeedLimitValueEditSendsNoLimiterCleanup(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 44, 30)
	seedA102Forward(t, a, tunnelID, "r2-03-edit", 22105, int64(44), nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/speed-limit/update", strings.NewReader(`{"id":44,"speed":50}`))
	rec := httptest.NewRecorder()
	a.h.speedLimitUpdate(rec, req)
	a.probe(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("speed limit update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if names := a102DeleteLimiterNames(t, a); len(names) != 0 {
		t.Fatalf("value-only edit must not sweep limiters, got %v", names)
	}
}
