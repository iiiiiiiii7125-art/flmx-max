package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R6 批 3 handler 侧回归：R6X-010 + E6-S1 同型补齐。
// 全部用例先红后绿：修复前 backupImport 不触发 reconcileSSMetering
// （恢复后伴生须等订阅同步循环才物化）、删原入站先删伴生（原入站
// 删除失败时伴生已被删、计量链接指向的伴生以新端口/新凭据重建）。

// R6X-010：backupImport 导入含 metering origin + 活跃分享订阅的
// 备份后，不等同步循环即有伴生行（handler 同步触发 reconcile）。
func TestR6B3BackupImportTriggersMeteringReconcile(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r6b3-import-metering.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}
	nodeID := seedRelayTargetNode(t, r, "r6-node", "203.0.113.20")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)

	// Origin 仅存在于备份中（目标库尚无此行）：自建 SS2022 入站、
	// 计量开关开。行内容经 Normalize + applyInboundToModel 生成，
	// 与真实导出备份同形态。
	in := kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: meteringMethod, Password: meteringSharedPW}}
	if err := kernel.Normalize(k.KernelType, &in); err != nil {
		t.Fatal(err)
	}
	row := model.NodeInbound{KernelID: k.ID, NodeID: nodeID, Name: "r6-ss-origin", Enabled: 1, CreatedTime: now}
	if err := applyInboundToModel(in, &row); err != nil {
		t.Fatal(err)
	}
	const originID = int64(50)
	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Inbounds: []model.InboundBackup{
				{ID: originID, KernelID: k.ID, NodeID: nodeID, Name: row.Name, Protocol: row.Protocol,
					Listen: row.Listen, Port: row.Port, Transport: row.Transport, Security: row.Security,
					Settings: row.Settings, Extra: row.Extra, Enabled: 1, CreatedTime: now, MeteringEnabled: 1},
			},
			Subscriptions: []model.SubscriptionBackup{
				{ID: 60, Name: "r6-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
					UUID: "u-r6", Password: meteringSubPW, SourceAll: 1, Enabled: 1, CreatedTime: now},
			},
		},
	}
	body := struct {
		Types []string `json:"types"`
		*model.BackupData
	}{Types: []string{"inbounds"}, BackupData: backup}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h.backupImport(rec, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("backupImport: code=%d err=%v body=%s", env.Code, err, rec.Body.String())
	}
	restored, err := r.GetNodeInbound(originID)
	if err != nil || restored == nil {
		t.Fatalf("origin must be restored: %v %v", restored, err)
	}
	if restored.MeteringEnabled != 1 {
		t.Fatalf("restored origin must keep metering on, got %d", restored.MeteringEnabled)
	}
	// 关键断言：响应返回时伴生已物化，不等订阅同步循环。
	comp, err := r.GetCompanionInbound(originID)
	if err != nil || comp == nil {
		t.Fatalf("R6X-010: companion must exist immediately after backupImport returns: %v %v", comp, err)
	}
}

// E6-S1 同型补齐（不计关闭数）：删原入站失败路径——伴生删除移至
// DeleteNodeInbound 成功之后。故障注入：令原入站行 DELETE 失败，
// 断言伴生仍在；解除注入后删除成功且伴生随之删除。
func TestR6B3OriginDeleteFailureKeepsCompanion(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	h.reconcileSSMetering()
	if comp, _ := r.GetCompanionInbound(origin.ID); comp == nil {
		t.Fatal("companion must exist before the delete")
	}
	trigger := fmt.Sprintf(`CREATE TRIGGER r6b3_block_origin_delete BEFORE DELETE ON node_inbound WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`, origin.ID)
	if err := r.DB().Exec(trigger).Error; err != nil {
		t.Fatal(err)
	}
	callDelete := func() int {
		w := httptest.NewRecorder()
		h.inboundDelete(w, claimsCtxReq(http.MethodPost, "/api/v1/inbound/delete", "",
			`{"id":`+strconv.FormatInt(origin.ID, 10)+`}`, 1, 0))
		var env struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode delete resp: %v (body=%s)", err, w.Body.String())
		}
		return env.Code
	}
	if code := callDelete(); code == 0 {
		t.Fatal("injected failure must make the delete fail")
	}
	if row, _ := r.GetNodeInbound(origin.ID); row == nil {
		t.Fatal("origin must survive its failed delete")
	}
	if comp, _ := r.GetCompanionInbound(origin.ID); comp == nil {
		t.Fatal("E6-S1: companion must survive a failed origin delete (it may only be removed after the origin delete succeeds)")
	}
	if err := r.DB().Exec(`DROP TRIGGER r6b3_block_origin_delete`).Error; err != nil {
		t.Fatal(err)
	}
	if code := callDelete(); code != 0 {
		t.Fatal("delete must succeed once the fault is removed")
	}
	if row, _ := r.GetNodeInbound(origin.ID); row != nil {
		t.Fatal("origin must be deleted")
	}
	if comp, _ := r.GetCompanionInbound(origin.ID); comp != nil {
		t.Fatal("companion must be deleted with its origin")
	}
}
