package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3 批 5 回归（分组三件套已于 1.9.6 整套移除，原 R3X-004 组操作
// 不撤销授权与 R3X-020 groupUpdate presence 两族随功能删除）：
//   - R3X-022：setUserInbounds 回执补 skippedRelays 计数，跳过语义与
//     整体成败不变。
// 复用 b2_fixture_test.go 的 seedB2Fixture（UT10–UT13、forward20/21）
// 与 b2Fixture 的 post/postB7Admin/execB7 帮手。

// ─── R3X-022 skippedRelays 回执 ────────────────────────────────────────────

func seedB5Inbound(t *testing.T, f *b2Fixture) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	f.execB7(t, `INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('n-b5', 's', '203.0.113.10', '1000-2000', 1000, 1)`)
	nodeID := mustLastInsertID(t, f.r, "node")
	if err := f.r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, nodeID, now, now).Error; err != nil {
		t.Fatalf("seed kernel: %v", err)
	}
	kernelID := mustLastInsertID(t, f.r, "kernel")
	if err := f.r.DB().Exec(`INSERT INTO node_inbound(kernel_id, node_id, name, protocol, listen, port, settings, enabled, created_time, updated_time) VALUES(?, ?, 'ib-b5', 'trojan', '::', 443, '{"password":"x"}', 1, ?, ?)`, kernelID, nodeID, now, now).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	return mustLastInsertID(t, f.r, "inbound")
}

func TestR3B5_022_SetUserInboundsPhantomRelayReceipt(t *testing.T) {
	f := seedB2Fixture(t)
	inboundID := seedB5Inbound(t, f)

	// 幻影 relay：整体成功（code=0）、不落行、回执含 skippedRelays=1。
	code, body := f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId":      2,
		"assignments": []map[string]interface{}{{"inboundId": inboundID, "relayId": 99999}},
	})
	if code != 0 {
		t.Fatalf("phantom relay must keep overall success, code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"skippedRelays":1`) {
		t.Fatalf("receipt must carry skippedRelays=1; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 2`); n != 0 {
		t.Fatalf("phantom relay must not persist rows, got %d", n)
	}

	// 混合：直连落行 + 幻影 relay 跳过计数。
	code, body = f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId": 2,
		"assignments": []map[string]interface{}{
			{"inboundId": inboundID, "relayId": 0},
			{"inboundId": inboundID, "relayId": 99999},
		},
	})
	if code != 0 {
		t.Fatalf("mixed assignments code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"skippedRelays":1`) {
		t.Fatalf("mixed receipt must carry skippedRelays=1; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 2 AND inbound_id = ? AND relay_id = 0`, inboundID); n != 1 {
		t.Fatalf("direct assignment must persist, got %d", n)
	}

	// 真实 relay：不跳过、计数 0、行落库。
	relay := &model.NodeInboundRelay{InboundID: inboundID, ForwardID: 20, Name: "relay-b5", Managed: 1, CreatedTime: time.Now().UnixMilli()}
	if err := f.r.CreateInboundRelay(relay); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	relayID := int64(f.count(t, `SELECT COALESCE(MAX(id),0) FROM node_inbound_relay`))
	if relayID <= 0 {
		t.Fatalf("relay id not assigned")
	}
	code, body = f.postB7Admin(t, f.h.setUserInbounds, map[string]interface{}{
		"userId":      2,
		"assignments": []map[string]interface{}{{"inboundId": inboundID, "relayId": relayID}},
	})
	if code != 0 {
		t.Fatalf("valid relay assignment code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"skippedRelays":0`) {
		t.Fatalf("valid relay receipt must carry skippedRelays=0; body=%s", body)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM user_inbound WHERE user_id = 2 AND inbound_id = ? AND relay_id = ?`, inboundID, relayID); n != 1 {
		t.Fatalf("valid relay assignment must persist, got %d", n)
	}
}
