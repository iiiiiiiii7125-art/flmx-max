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

// Lane B：节点连接日志就绪指示 + 一键重新下发。
//
// 覆盖：
//   - agent 版本解析/支持度（上报串含 "-cand" 后缀与 "(distro/arch)"
//     尾巴；缺失/"dev"/不可解析一律 unknown，绝不按支持处理）；
//   - readiness 各态（off / not_ready / unknown / ready）与
//     lastPush 恒 unknown 的诚实口径；
//   - repush 的显式失败（离线/nftables/不存在/参数错）与在线
//     空载节点的成功回执；
//   - 两端点均为管理员专属（普通用户 403、未登录 401）。
// 本文件同时含两条路由的路径字面量，供路由覆盖门（C1 口径）
// 引用计数。

func TestConnlogAgentSupportParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"2.1.5", "supported"},
		{"2.1.5 (debian/amd64)", "supported"},
		{"2.1.8-cand (debian/amd64)", "supported"},
		{"2.1.8-cand", "supported"},
		{"v2.1.6", "supported"},
		{"2.1.5+build1", "supported"},
		{"2.1.10", "supported"},
		{"2.1.4", "unsupported"},
		{"2.1.4-cand (debian/amd64)", "unsupported"},
		{"1.3.0 (debian/amd64)", "unsupported"},
		{"", "unknown"},
		{"dev", "unknown"},
		{"dev (debian/amd64)", "unknown"},
		{"bogus", "unknown"},
		{"2.1.x", "unknown"},
	}
	for _, c := range cases {
		if got := connlogAgentSupport(c.raw); got != c.want {
			t.Errorf("connlogAgentSupport(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

type connlogReadinessData struct {
	NodeID         int64  `json:"nodeId"`
	AgentVersion   string `json:"agentVersion"`
	AgentSupport   string `json:"agentSupport"`
	AgentMin       string `json:"agentMinVersion"`
	MasterEnabled  bool   `json:"masterEnabled"`
	ConnLogEnabled int    `json:"connLogEnabled"`
	Online         bool   `json:"online"`
	ForwardMode    string `json:"forwardMode"`
	LastPush       struct {
		Status   string `json:"status"`
		PushedAt *int64 `json:"pushedAt"`
		Source   string `json:"source"`
		Note     string `json:"note"`
	} `json:"lastPush"`
	LastRowAt *int64   `json:"lastRowAt"`
	Readiness string   `json:"readiness"`
	Reason    string   `json:"reason"`
	Reasons   []string `json:"reasons"`
}

func (rig *connlogTestRig) callReadiness(t *testing.T, claims *auth.Claims, nodeID int64) (int, connlogReadinessData) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/node/connlog-readiness?nodeId=%d", nodeID), nil)
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, *claims))
	}
	rec := httptest.NewRecorder()
	rig.h.nodeConnlogReadiness(rec, req)
	var envelope struct {
		Code int                  `json:"code"`
		Msg  string               `json:"msg"`
		Data connlogReadinessData `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("readiness response not an envelope: %s", rec.Body.String())
	}
	return envelope.Code, envelope.Data
}

func TestConnlogReadinessStates(t *testing.T) {
	rig := newConnlogTestRig(t)
	admin := &auth.Claims{Sub: "1", RoleID: 0}
	user := &auth.Claims{Sub: "2", RoleID: 1}

	// 未登录 401、普通用户 403。
	if code, _ := rig.callReadiness(t, nil, rig.entryID); code != 401 {
		t.Fatalf("anonymous readiness code = %d, want 401", code)
	}
	if code, _ := rig.callReadiness(t, user, rig.entryID); code != 403 {
		t.Fatalf("non-admin readiness code = %d, want 403", code)
	}
	// 不存在的节点：信封码 -1（ErrDefault）。
	if code, _ := rig.callReadiness(t, admin, 999999); code != -1 {
		t.Fatalf("missing node readiness code = %d, want -1", code)
	}

	// 初始态：开关关、版本未上报 → off，agent 未知，lastPush 恒 unknown。
	code, data := rig.callReadiness(t, admin, rig.entryID)
	if code != 0 {
		t.Fatalf("admin readiness code = %d, want 0", code)
	}
	if data.Readiness != "off" || data.ConnLogEnabled != 0 || data.AgentSupport != "unknown" {
		t.Fatalf("initial readiness = %+v, want off / connLogEnabled 0 / agent unknown", data)
	}
	if data.LastPush.Status != "unknown" || data.LastPush.PushedAt != nil || data.LastPush.Source != "none" {
		t.Fatalf("lastPush = %+v, want status unknown / pushedAt null / source none (no push record exists)", data.LastPush)
	}
	if data.AgentMin != connlogAgentMinVersion {
		t.Fatalf("agentMinVersion = %q, want %q", data.AgentMin, connlogAgentMinVersion)
	}

	// 开关开 + 新版 agent，但尚无记录 → unknown（无法确认生效）。
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 1); err != nil {
		t.Fatalf("enable connlog: %v", err)
	}
	if err := rig.r.UpdateNodeOnline(rig.entryID, 1, "2.1.8-cand (debian/amd64)", 0, 0, 0); err != nil {
		t.Fatalf("set agent version: %v", err)
	}
	code, data = rig.callReadiness(t, admin, rig.entryID)
	if code != 0 || data.Readiness != "unknown" || data.AgentSupport != "supported" || data.LastRowAt != nil {
		t.Fatalf("no-rows readiness = (%d, %+v), want unknown / supported / lastRowAt null", code, data)
	}
	if !strings.Contains(data.Reason, "尚无连接记录") {
		t.Fatalf("no-rows reason = %q, want it to say no rows yet", data.Reason)
	}

	// 上报一行（最近时刻）→ ready，lastRowAt 为该行 startTime。
	now := time.Now().UnixMilli()
	events := fmt.Sprintf(`[{"type":"open","connId":"r1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"tcp_conn","startTime":%d}]`, now-1000)
	if rec := rig.postBatch(t, rig.entrySec, 1, events); rec.Code != http.StatusOK {
		t.Fatalf("seed ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	code, data = rig.callReadiness(t, admin, rig.entryID)
	if code != 0 || data.Readiness != "ready" || data.LastRowAt == nil || *data.LastRowAt != now-1000 {
		t.Fatalf("with-rows readiness = (%d, %+v), want ready / lastRowAt %d", code, data, now-1000)
	}
	if data.Reason != "" || len(data.Reasons) != 0 {
		t.Fatalf("ready reason/reasons = %q/%v, want empty", data.Reason, data.Reasons)
	}

	// 只有 25 小时前的旧行 → unknown（实证过期，不冒充 ready）。
	if err := rig.r.DB().Where("entry_node_id = ?", rig.entryID).Delete(&model.InboundConnLog{}).Error; err != nil {
		t.Fatalf("clear rows: %v", err)
	}
	stale := &model.InboundConnLog{EntryNodeID: rig.entryID, ConnID: "r0", EntryKind: repo.ConnlogEntryForward, EntryID: "77_2_3_tcp", ClientIP: "203.0.113.7", Network: "tcp", SessionKind: "tcp_conn", StartTime: now - 25*60*60*1000, Status: repo.ConnlogStatusOpen, CreatedTime: now - 25*60*60*1000}
	if err := rig.r.DB().Create(stale).Error; err != nil {
		t.Fatalf("seed stale row: %v", err)
	}
	code, data = rig.callReadiness(t, admin, rig.entryID)
	if code != 0 || data.Readiness != "unknown" {
		t.Fatalf("stale-rows readiness = (%d, %+v), want unknown", code, data)
	}
	if !strings.Contains(data.Reason, "24 小时") {
		t.Fatalf("stale-rows reason = %q, want it to name the 24h window", data.Reason)
	}

	// 旧版 agent → not_ready（版本过旧），即使已有记录也不报 ready。
	if err := rig.r.UpdateNodeOnline(rig.entryID, 1, "2.1.4 (debian/amd64)", 0, 0, 0); err != nil {
		t.Fatalf("downgrade agent version: %v", err)
	}
	code, data = rig.callReadiness(t, admin, rig.entryID)
	if code != 0 || data.Readiness != "not_ready" || data.AgentSupport != "unsupported" {
		t.Fatalf("old-agent readiness = (%d, %+v), want not_ready / unsupported", code, data)
	}
	if !strings.Contains(data.Reason, "版本过旧") {
		t.Fatalf("old-agent reason = %q, want it to name the old version", data.Reason)
	}

	// 离线 → not_ready（离线原因优先于版本原因的次序外，本格
	// 只断言离线态本身：先把版本恢复为新版再离线）。
	if err := rig.r.UpdateNodeOnline(rig.entryID, 1, "2.1.8-cand (debian/amd64)", 0, 0, 0); err != nil {
		t.Fatalf("restore agent version: %v", err)
	}
	if err := rig.r.UpdateNodeStatus(rig.entryID, 0); err != nil {
		t.Fatalf("take node offline: %v", err)
	}
	code, data = rig.callReadiness(t, admin, rig.entryID)
	if code != 0 || data.Readiness != "not_ready" || data.Online {
		t.Fatalf("offline readiness = (%d, %+v), want not_ready / online false", code, data)
	}
	if !strings.Contains(data.Reason, "离线") {
		t.Fatalf("offline reason = %q, want it to say offline", data.Reason)
	}
	if err := rig.r.UpdateNodeStatus(rig.entryID, 1); err != nil {
		t.Fatalf("bring node back online: %v", err)
	}

	// 总开关关 → off（主因须为总开关）。
	if err := rig.r.UpsertConfig(repo.ConnLogEnabledConfigKey, "false", time.Now().UnixMilli()); err != nil {
		t.Fatalf("disable master switch: %v", err)
	}
	code, data = rig.callReadiness(t, admin, rig.entryID)
	if code != 0 || data.Readiness != "off" || data.MasterEnabled {
		t.Fatalf("master-off readiness = (%d, %+v), want off / masterEnabled false", code, data)
	}
	if !strings.Contains(data.Reason, "总开关") {
		t.Fatalf("master-off reason = %q, want it to name the master switch", data.Reason)
	}
	if err := rig.r.DeleteConfig(repo.ConnLogEnabledConfigKey); err != nil {
		t.Fatalf("restore master switch: %v", err)
	}

	// nftables 模式 → not_ready（无 agent 通道）。
	if err := rig.r.DB().Model(&model.Node{}).Where("id = ?", rig.entryID).Update("forward_mode", "nftables").Error; err != nil {
		t.Fatalf("set nftables mode: %v", err)
	}
	code, data = rig.callReadiness(t, admin, rig.entryID)
	if code != 0 || data.Readiness != "not_ready" || data.ForwardMode != "nftables" {
		t.Fatalf("nftables readiness = (%d, %+v), want not_ready / nftables", code, data)
	}
	if !strings.Contains(data.Reason, "nftables") {
		t.Fatalf("nftables reason = %q, want it to name nftables", data.Reason)
	}
}

type connlogRepushData struct {
	NodeID       int64 `json:"nodeId"`
	Redeployed   bool  `json:"redeployed"`
	TunnelCount  int   `json:"tunnelCount"`
	ForwardCount int   `json:"forwardCount"`
}

func (rig *connlogTestRig) callRepush(t *testing.T, claims *auth.Claims, method, target, body string) (int, string, connlogRepushData) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, *claims))
	}
	rec := httptest.NewRecorder()
	rig.h.nodeConnlogRepush(rec, req)
	var envelope struct {
		Code    int               `json:"code"`
		Msg     string            `json:"msg"`
		Warning string            `json:"warning"`
		Data    connlogRepushData `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("repush response not an envelope: %s", rec.Body.String())
	}
	return envelope.Code, envelope.Msg, envelope.Data
}

func TestConnlogRepushExplicitResults(t *testing.T) {
	rig := newConnlogTestRig(t)
	admin := &auth.Claims{Sub: "1", RoleID: 0}
	user := &auth.Claims{Sub: "2", RoleID: 1}
	repushPath := "/api/v1/node/connlog-repush"

	// 未登录 401、普通用户 403。
	if code, _, _ := rig.callRepush(t, nil, http.MethodPost, repushPath, `{"nodeId":1}`); code != 401 {
		t.Fatalf("anonymous repush code = %d, want 401", code)
	}
	if code, _, _ := rig.callRepush(t, user, http.MethodPost, repushPath, `{"nodeId":1}`); code != 403 {
		t.Fatalf("non-admin repush code = %d, want 403", code)
	}
	// 方法错、参数错、节点不存在——一律显式失败，不报成功。
	if code, _, _ := rig.callRepush(t, admin, http.MethodGet, repushPath, ""); code != -1 {
		t.Fatalf("GET repush code = %d, want -1", code)
	}
	if code, _, _ := rig.callRepush(t, admin, http.MethodPost, repushPath, `{"nodeId":0}`); code != -1 {
		t.Fatalf("bad nodeId repush code = %d, want -1", code)
	}
	if code, msg, _ := rig.callRepush(t, admin, http.MethodPost, repushPath, `{"nodeId":999999}`); code != -1 || !strings.Contains(msg, "节点不存在") {
		t.Fatalf("missing node repush = (%d, %q), want -1 / 节点不存在", code, msg)
	}

	// 离线节点：显式失败，文案须点名离线且说明未发送任何配置。
	if err := rig.r.UpdateNodeStatus(rig.entryID, 0); err != nil {
		t.Fatalf("take node offline: %v", err)
	}
	code, msg, _ := rig.callRepush(t, admin, http.MethodPost, repushPath, fmt.Sprintf(`{"nodeId":%d}`, rig.entryID))
	if code != -1 || !strings.Contains(msg, "离线") {
		t.Fatalf("offline repush = (%d, %q), want -1 / 离线", code, msg)
	}
	if err := rig.r.UpdateNodeStatus(rig.entryID, 1); err != nil {
		t.Fatalf("bring node back online: %v", err)
	}

	// nftables 节点：显式拒绝。
	if err := rig.r.DB().Model(&model.Node{}).Where("id = ?", rig.entryID).Update("forward_mode", "nftables").Error; err != nil {
		t.Fatalf("set nftables mode: %v", err)
	}
	code, msg, _ = rig.callRepush(t, admin, http.MethodPost, repushPath, fmt.Sprintf(`{"nodeId":%d}`, rig.entryID))
	if code != -1 || !strings.Contains(msg, "nftables") {
		t.Fatalf("nftables repush = (%d, %q), want -1 / nftables", code, msg)
	}
	if err := rig.r.DB().Model(&model.Node{}).Where("id = ?", rig.entryID).Update("forward_mode", "agent").Error; err != nil {
		t.Fatalf("restore agent mode: %v", err)
	}

	// 在线 agent 节点、无隧道/无转发/无内核：重推同步成功，
	// 回执点名 redeployed 与计数（零服务即零下发对象，仍为
	// 成功——与 redeployLocalNodeRuntime 的空集语义一致）。
	code, msg, data := rig.callRepush(t, admin, http.MethodPost, repushPath, fmt.Sprintf(`{"nodeId":%d}`, rig.entryID))
	if code != 0 || !data.Redeployed || data.NodeID != rig.entryID || data.TunnelCount != 0 || data.ForwardCount != 0 {
		t.Fatalf("online empty repush = (%d, %q, %+v), want 0 / redeployed / counts 0", code, msg, data)
	}
}
