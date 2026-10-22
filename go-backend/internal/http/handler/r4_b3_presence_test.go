package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R4 批 3 回归：presence 与零值口径（R4X-004/010/018/019/012）。
// 每条先红后绿：省略保留 / 显式清零两向逐键矩阵。

// ─── R4X-004 + R4X-010(a)：0 倍率隧道 ───────────────────────────────────

func TestR4B3TunnelUpdatePartialKeepsZeroRatio(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4b3-zero-ratio.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.16")
	tunnelID := seedTunnelForNftables(t, h, "zero-ratio-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET flow = 2, traffic_ratio = 0 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":       tunnelID,
		"inNodeId": []map[string]interface{}{{"nodeId": nodeID, "protocol": "tls"}},
	}))
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("tunnelUpdate failed: code=%d msg=%s body=%s", code, msg, res.Body.String())
	}
	var ratio float64
	if err := r.DB().Raw(`SELECT traffic_ratio FROM tunnel WHERE id = ?`, tunnelID).Scan(&ratio).Error; err != nil {
		t.Fatal(err)
	}
	if ratio != 0 {
		t.Fatalf("R4X-004: partial update rewrote stored ratio 0 to %v, want 0 preserved", ratio)
	}
}

func TestR4B3TunnelCreateExplicitZeroRatio(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4b3-create-zero.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()

	insert := func(name string, ratio float64) float64 {
		tx := r.BeginTx()
		if tx.Error != nil {
			t.Fatalf("begin tx: %v", tx.Error)
		}
		tun := &model.Tunnel{Name: name, TrafficRatio: ratio, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1}
		if err := r.InsertTunnelTx(tx, tun); err != nil {
			_ = tx.Rollback()
			t.Fatalf("InsertTunnelTx: %v", err)
		}
		if err := tx.Commit().Error; err != nil {
			t.Fatalf("commit: %v", err)
		}
		var stored float64
		if err := r.DB().Raw(`SELECT traffic_ratio FROM tunnel WHERE id = ?`, tun.ID).Scan(&stored).Error; err != nil {
			t.Fatal(err)
		}
		return stored
	}
	if got := insert("explicit-zero", 0); got != 0 {
		t.Fatalf("R4X-010(a): InsertTunnelTx explicit ratio 0 stored as %v, want 0", got)
	}
	if got := insert("explicit-default", 1); got != 1 {
		t.Fatalf("R4X-010(a): InsertTunnelTx ratio 1 stored as %v, want 1", got)
	}

	// CreateTunnelTx 同型落库口。
	tx := r.BeginTx()
	id, err := r.CreateTunnelTx(tx, "create-tx-zero", 0, 1, 1, now, 1, nil, 1, "", "", 0)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("CreateTunnelTx: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	var stored float64
	if err := r.DB().Raw(`SELECT traffic_ratio FROM tunnel WHERE id = ?`, id).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("R4X-010(a): CreateTunnelTx explicit ratio 0 stored as %v, want 0", stored)
	}
}

func TestR4B3TunnelRecordRawVsNormalized(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4b3-raw.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.16")
	tunnelID := seedTunnelForNftables(t, h, "raw-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET traffic_ratio = 0 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	raw, err := r.GetTunnelRecordRaw(tunnelID)
	if err != nil || raw == nil {
		t.Fatalf("GetTunnelRecordRaw: %v", err)
	}
	if raw.TrafficRatio != 0 {
		t.Fatalf("R4X-004: raw read must return stored 0, got %v", raw.TrafficRatio)
	}
	normalized, err := r.GetTunnelRecord(tunnelID)
	if err != nil || normalized == nil {
		t.Fatalf("GetTunnelRecord: %v", err)
	}
	if normalized.TrafficRatio != 1 {
		t.Fatalf("read-only normalized view must keep presenting 0 as 1, got %v", normalized.TrafficRatio)
	}
}

// ─── R4X-019：speedLimitUpdate presence ──────────────────────────────────

func r4b3SpeedLimitRow(t *testing.T, r *repo.Repository, id int64) (string, int, int) {
	t.Helper()
	var name string
	var speed, status int
	if err := r.DB().Raw(`SELECT name, speed, status FROM speed_limit WHERE id = ?`, id).Row().Scan(&name, &speed, &status); err != nil {
		t.Fatalf("read speed_limit: %v", err)
	}
	return name, speed, status
}

func TestR4B3SpeedLimitUpdatePresence(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4b3-speed.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	id, err := r.CreateSpeedLimit("limit-pres", 250, time.Now().UnixMilli(), 0)
	if err != nil {
		t.Fatalf("create speed limit: %v", err)
	}

	// 红探针形态：只改名（缺席 speed/status）——停用规则不得被启用、speed 不得变 100。
	res := httptest.NewRecorder()
	h.speedLimitUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": id, "name": "limit-pres-renamed"}))
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("speedLimitUpdate failed: code=%d msg=%s", code, msg)
	}
	name, speed, status := r4b3SpeedLimitRow(t, r, id)
	if name != "limit-pres-renamed" {
		t.Fatalf("name not updated: %q", name)
	}
	if speed != 250 {
		t.Fatalf("R4X-019: absent speed rewritten to %d, want 250 preserved", speed)
	}
	if status != 0 {
		t.Fatalf("R4X-019: absent status rewritten to %d, want 0 preserved (disabled rule silently enabled)", status)
	}

	// 显式传值仍生效。
	res2 := httptest.NewRecorder()
	h.speedLimitUpdate(res2, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": id, "name": "limit-pres-renamed", "speed": 300, "status": 1}))
	if code, msg := decodeEnvelopeA204(t, res2); code != 0 {
		t.Fatalf("speedLimitUpdate explicit failed: code=%d msg=%s", code, msg)
	}
	_, speed, status = r4b3SpeedLimitRow(t, r, id)
	if speed != 300 || status != 1 {
		t.Fatalf("explicit values not applied: speed=%d status=%d", speed, status)
	}
}

// ─── R4X-012：分组校验 + 撤销谓词 ───────────────────────────────────────

func TestR4B3GroupValidation(t *testing.T) {
	f := seedB2Fixture(t)

	// create：status∉{0,1} 拒绝且不落库。
	code, body := f.post(t, f.h.groupUserCreate, map[string]interface{}{"name": "g-bad", "status": 2})
	if code == 0 {
		t.Fatalf("R4X-012: group create status=2 must be rejected, body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_group WHERE name = 'g-bad'`); n != 0 {
		t.Fatalf("rejected group create must not persist, got %d rows", n)
	}
	code, body = f.post(t, f.h.groupTunnelCreate, map[string]interface{}{"name": "tg-bad", "status": 7})
	if code == 0 {
		t.Fatalf("R4X-012: tunnel group create status=7 must be rejected, body=%s", body)
	}
	// update：status=2 拒绝、存量不变（僵尸组形态：启用组 G1 不得落入 2）。
	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "status": 2})
	if code == 0 {
		t.Fatalf("R4X-012: group update status=2 must be rejected, body=%s", body)
	}
	if _, status := b5GroupNameStatus(t, f, "user_group", 1); status != 1 {
		t.Fatalf("rejected update must not change stored status, got %d", status)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_tunnel WHERE id = 10`); n != 1 {
		t.Fatalf("grants of enabled group must survive rejected zombie update, got %d", n)
	}
	// update：显式空名拒绝、组名不变。
	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 1, "name": ""})
	if code == 0 {
		t.Fatalf("R4X-012: group update empty name must be rejected, body=%s", body)
	}
	if name, _ := b5GroupNameStatus(t, f, "user_group", 1); name != "g-one" {
		t.Fatalf("rejected empty-name update must not clear the name, got %q", name)
	}
	// update：合法 status=0 仍触发撤销（谓词 !=1 路径）。
	seedB5Stock(t, f, 0, 1)
	code, body = f.post(t, f.h.groupUserUpdate, map[string]interface{}{"id": 2, "status": 0})
	if code != 0 {
		t.Fatalf("group update status=0 must succeed, code=%d body=%s", code, body)
	}
	assertB5StockRevoked(t, f)

	// create：status=0 合法（置于存量种子之后，避免自增 ID 与 seedB5Stock 的显式 id=2 相撞）。
	code, body = f.post(t, f.h.groupUserCreate, map[string]interface{}{"name": "g-off-new", "status": 0})
	if code != 0 {
		t.Fatalf("group create status=0 must succeed, code=%d body=%s", code, body)
	}
}

// ─── R4X-018：入站整对象更新 8 键 presence ──────────────────────────────

func r4b3SeedPresenceInbound(t *testing.T, r *repo.Repository, k *model.NodeKernel, targetID int64) *model.NodeInbound {
	t.Helper()
	entry := seedChainInbound(t, r, k, "pres-entry", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, targetID)
	entry.ShareHost = "share.example.com"
	entry.ShareAddressMode = "custom"
	entry.Remark = "keep-remark"
	entry.RelayOnly = 1
	entry.AcceptProxyProtocol = 0
	entry.RelayWhitelist = 1
	entry.WhitelistExtra = "10.0.0.0/8"
	entry.SplitRules = `[{"categories":["netflix"],"action":"proxy","outboundId":` + jsonID(targetID) + `}]`
	if err := r.SaveNodeInbound(entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func r4b3InboundBody(t *testing.T, entry *model.NodeInbound, extra map[string]interface{}) string {
	t.Helper()
	old, err := inboundFromModel(*entry)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]interface{}{
		"id": entry.ID, "name": entry.Name, "protocol": old.Protocol, "listen": old.Listen, "port": old.Port,
		"settings": old.Settings, "transport": old.Transport, "security": old.Security, "extra": string(old.Extra),
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestR4B3InboundUpdatePresenceMatrix(t *testing.T) {
	setup := func(t *testing.T, db string) (*repo.Repository, *Handler, *model.NodeInbound, int64) {
		r, err := repo.Open(t.TempDir() + "/" + db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		h := New(r, "unit-test-secret")
		nodeID := seedRelayTargetNode(t, r, "pres-node", "198.51.100.50")
		k := seedB8Kernel(t, r, nodeID, kernel.TypeXray, "latest")
		target := seedChainInbound(t, r, k, "pres-target", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24444,
			Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
		entry := r4b3SeedPresenceInbound(t, r, k, target.ID)
		return r, h, entry, target.ID
	}

	t.Run("omit all eight keys preserves stored values", func(t *testing.T) {
		r, h, entry, targetID := setup(t, "r4b3-pres-omit.db")
		body := r4b3InboundBody(t, entry, map[string]interface{}{"name": "pres-entry-renamed"})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("partial update must succeed: %+v", out)
		}
		got, err := r.GetNodeInbound(entry.ID)
		if err != nil || got == nil {
			t.Fatalf("reload: %v", err)
		}
		if got.Name != "pres-entry-renamed" {
			t.Fatalf("rename must apply: %q", got.Name)
		}
		if got.ShareHost != "share.example.com" || got.ShareAddressMode != "custom" {
			t.Fatalf("R4X-018(a): shareHost cleared by omission: host=%q mode=%q", got.ShareHost, got.ShareAddressMode)
		}
		if got.Remark != "keep-remark" {
			t.Fatalf("R4X-018(a): remark cleared by omission: %q", got.Remark)
		}
		if got.RelayOnly != 1 || got.AcceptProxyProtocol != 0 || got.RelayWhitelist != 1 {
			t.Fatalf("R4X-018(b/c): bools rewritten by omission: relayOnly=%d pp=%d whitelist=%d", got.RelayOnly, got.AcceptProxyProtocol, got.RelayWhitelist)
		}
		if got.WhitelistExtra != "10.0.0.0/8" {
			t.Fatalf("R4X-018(c): whitelistExtra cleared by omission: %q", got.WhitelistExtra)
		}
		if got.OutboundID != targetID {
			t.Fatalf("R4X-018(d): outboundId dropped by omission: %d want %d", got.OutboundID, targetID)
		}
		if len(rowSplitRules(*got)) != 1 {
			t.Fatalf("R4X-018(d): splitRules cleared by omission: %q", got.SplitRules)
		}
	})

	t.Run("explicit clearing of all eight keys still applies", func(t *testing.T) {
		r, h, entry, _ := setup(t, "r4b3-pres-clear.db")
		body := r4b3InboundBody(t, entry, map[string]interface{}{
			"shareHost": "", "remark": "", "relayOnly": false, "acceptProxyProtocol": false,
			"relayWhitelist": false, "whitelistExtra": "", "outboundId": 0, "splitRules": []interface{}{},
		})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("explicit-clear update must succeed: %+v", out)
		}
		got, err := r.GetNodeInbound(entry.ID)
		if err != nil || got == nil {
			t.Fatalf("reload: %v", err)
		}
		if got.ShareHost != "" || got.Remark != "" || got.RelayOnly != 0 || got.RelayWhitelist != 0 || got.WhitelistExtra != "" || got.OutboundID != 0 || got.SplitRules != "" {
			t.Fatalf("explicit clearing must apply: %+v", got)
		}
	})

	t.Run("proxy protocol backfill precedes relayOnly linkage", func(t *testing.T) {
		r, h, entry, _ := setup(t, "r4b3-pres-pp.db")
		entry.AcceptProxyProtocol = 1
		entry.RelayOnly = 1
		if err := r.SaveNodeInbound(entry); err != nil {
			t.Fatal(err)
		}
		// 显式 relayOnly=false、省略 acceptProxyProtocol：PP 回填为 1 后联动须把 RelayOnly 强制回 1。
		body := r4b3InboundBody(t, entry, map[string]interface{}{"relayOnly": false})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("update must succeed: %+v", out)
		}
		got, err := r.GetNodeInbound(entry.ID)
		if err != nil || got == nil {
			t.Fatalf("reload: %v", err)
		}
		if got.AcceptProxyProtocol != 1 {
			t.Fatalf("R4X-018(b): acceptProxyProtocol cleared by omission: %d", got.AcceptProxyProtocol)
		}
		if got.RelayOnly != 1 {
			t.Fatalf("R4X-018(b): PP=1 linkage must force relayOnly=1 after backfill, got %d", got.RelayOnly)
		}
	})
}
