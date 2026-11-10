package handler

// AUDIT-R3 修复波·包 C 定向格（handler 包内）：
//   - R3-GEN-03：validateRealmTunnelTx 的「节点不存在」定型支——
//     tunnelCreate 的 HTTP 面上 prepare 先行同文案拦截、无法单独
//     触达本支，故在此直测 helper 的定型与未标记性（守卫放行原文
//     的前提）。
//   - R3-GEN-05 旁注：retargetRelays 的 problems 净化——定型校验
//     原文保留、仓储原文（含未标记直通）替换为通用文案，不再拼入
//     成功附注回执/同步摘要。

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func TestR3Gen03ValidateRealmTunnelTypedErrors(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/gen03-realm.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	// type!=1 定型：realm 引擎仅支持端口转发隧道。
	if err := h.validateRealmTunnelTx(r.DB(), 2, nil); err == nil ||
		err.Error() != "realm 引擎仅支持端口转发隧道" || isRepoError(err) {
		t.Fatalf("realm type gate must stay typed and unmarked, got %v", err)
	}
	// 节点不存在定型（GetNodeRecordTx 缺失行回 (nil, nil)）。
	if err := h.validateRealmTunnelTx(r.DB(), 1, []int64{999999}); err == nil ||
		err.Error() != "节点不存在" || isRepoError(err) {
		t.Fatalf("realm missing-node must stay typed and unmarked, got %v", err)
	}
}

func TestR3Gen05RetargetRelaysSanitizesProblems(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/gen05-retarget.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	db := r.DB()

	landing := seedRelayTargetNode(t, r, "gen05-landing", "192.0.2.40")
	row := &model.NodeInbound{
		NodeID: landing, Name: "gen05-in", Protocol: "vless",
		Listen: "127.0.0.1", Port: 443, CreatedTime: now, UpdatedTime: now,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	tunnel := model.Tunnel{Name: "gen05-line", Type: 1, Protocol: "tls", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "gen05-fwd", TunnelID: tunnel.ID, RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	rel := model.NodeInboundRelay{InboundID: row.ID, ForwardID: fwd.ID, Name: "gen05-relay", Managed: 1, CreatedTime: now}
	if err := db.Create(&rel).Error; err != nil {
		t.Fatal(err)
	}

	// 定型校验（只监听本机）原文保留——净化只挡仓储原文。
	problems := h.retargetRelays(row)
	if len(problems) != 1 || !strings.Contains(problems[0], "只监听本机") {
		t.Fatalf("typed relayTarget failure must survive sanitization, got %v", problems)
	}
	if strings.Contains(problems[0], "内部错误") {
		t.Fatalf("typed failure must not be generalized, got %v", problems)
	}

	// 仓储故障（表缺失的原始 gorm 文本）必须被替换为通用文案。
	r2, err := repo.Open(t.TempDir() + "/gen05-retarget-broken.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	h2 := New(r2, "unit-test-secret")
	if err := r2.DB().Exec(`DROP TABLE node_inbound_relay`).Error; err != nil {
		t.Fatal(err)
	}
	problems = h2.retargetRelays(&model.NodeInbound{ID: 1})
	if len(problems) != 1 || problems[0] != "内部错误，请稍后重试" {
		t.Fatalf("storage failure must be generalized, got %v", problems)
	}
}
