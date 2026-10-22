package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R3 批 9 · R3X-015：删除守卫 fail-closed 回归。
//
// 三守卫（chainReferrers / kernelChainGuard / nodeCrossReferrers）的底层
// 查询是 ListNodeInbounds（ORDER BY inx）。本文件用「重命名 node_inbound
// 的 inx 列」注入查询错误：全局列表查询（守卫的数据源）必然失败，而
// 按 ID/按节点/按内核的单行与过滤查询不引用 inx、仍可正常工作——于是
// 守卫失败与前置读取成功被干净分离，断言对象只有守卫口径本身。
// 断言五路径（入站单删 / 入站批量删 / 远程来源删 / 内核卸载 / 删节点
// 单+批）在守卫查询失败时均拒绝执行且数据未动；健康对照组行为不变。

func r3b9Open(t *testing.T, name string) (*repo.Repository, *Handler) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, New(r, "unit-test-secret")
}

// r3b9BreakGuardQuery 让 ListNodeInbounds 失败（守卫查询注入错误），
// r3b9RestoreGuardQuery 还原后可正常断言数据未动。
func r3b9BreakGuardQuery(t *testing.T, r *repo.Repository) {
	t.Helper()
	if err := r.DB().Exec(`ALTER TABLE node_inbound RENAME COLUMN inx TO inx_r3b9_broken`).Error; err != nil {
		t.Fatalf("break guard query: %v", err)
	}
	if _, err := r.ListNodeInbounds(); err == nil {
		t.Fatal("injection ineffective: ListNodeInbounds must fail after renaming inx")
	}
}

func r3b9RestoreGuardQuery(t *testing.T, r *repo.Repository) {
	t.Helper()
	if err := r.DB().Exec(`ALTER TABLE node_inbound RENAME COLUMN inx_r3b9_broken TO inx`).Error; err != nil {
		t.Fatalf("restore guard query: %v", err)
	}
}

type r3b9Resp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func r3b9Call(t *testing.T, fn http.HandlerFunc, body string) r3b9Resp {
	t.Helper()
	rec := httptest.NewRecorder()
	fn(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	var out r3b9Resp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return out
}

func r3b9BatchData(t *testing.T, raw json.RawMessage) batchOperationResult {
	t.Helper()
	var out batchOperationResult
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode batch data: %v", err)
		}
	}
	return out
}

// r3b9SeedChain 建 front/landing 两节点：landing 上 target 入站，front
// 上 entry 入站以 target 为出站（跨节点引用成立）。
func r3b9SeedChain(t *testing.T, r *repo.Repository) (frontID, landingID int64, target, entry *model.NodeInbound, landingKernel *model.NodeKernel) {
	t.Helper()
	frontID = seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landingID = seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	frontKernel := seedChainKernel(t, r, frontID, kernel.TypeXray)
	landingKernel = seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target = seedChainInbound(t, r, landingKernel, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	entry = seedChainInbound(t, r, frontKernel, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)
	return frontID, landingID, target, entry, landingKernel
}

func r3b9InboundExists(t *testing.T, r *repo.Repository, id int64) bool {
	t.Helper()
	row, err := r.GetNodeInbound(id)
	if err != nil {
		t.Fatalf("reload inbound %d: %v", id, err)
	}
	return row != nil
}

func TestR3B9GuardFailClosedInboundDelete(t *testing.T) {
	r, h := r3b9Open(t, "r3b9-inbound-delete.db")
	_, _, target, _, _ := r3b9SeedChain(t, r)

	r3b9BreakGuardQuery(t, r)
	out := r3b9Call(t, h.inboundDelete, `{"id":`+jsonID(target.ID)+`}`)
	r3b9RestoreGuardQuery(t, r)
	if out.Code == 0 {
		t.Fatalf("R3X-015: inbound delete must be refused when the guard query fails, got %+v", out)
	}
	if !r3b9InboundExists(t, r, target.ID) {
		t.Fatal("R3X-015: inbound must survive a guard-failed delete")
	}
}

func TestR3B9GuardFailClosedInboundBatchDelete(t *testing.T) {
	r, h := r3b9Open(t, "r3b9-inbound-batch.db")
	_, _, target, entry, _ := r3b9SeedChain(t, r)

	r3b9BreakGuardQuery(t, r)
	out := r3b9Call(t, h.inboundBatchDelete, `{"ids":[`+jsonID(target.ID)+`,`+jsonID(entry.ID)+`]}`)
	r3b9RestoreGuardQuery(t, r)
	batch := r3b9BatchData(t, out.Data)
	if batch.SuccessCount != 0 || batch.FailCount != 2 {
		t.Fatalf("R3X-015: batch delete must refuse every item when the guard query fails, got %+v (resp %+v)", batch, out)
	}
	if !r3b9InboundExists(t, r, target.ID) || !r3b9InboundExists(t, r, entry.ID) {
		t.Fatal("R3X-015: inbounds must survive a guard-failed batch delete")
	}
}

func TestR3B9GuardFailClosedRemoteSourceDelete(t *testing.T) {
	r, h := r3b9Open(t, "r3b9-remote-source.db")
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{Name: "guard-src", Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatal(err)
	}
	row := &model.NodeInbound{Name: "imported", Protocol: "ss", Listen: "203.0.113.5", Port: 8388,
		SourceID: src.ID, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatal(err)
	}

	r3b9BreakGuardQuery(t, r)
	out := r3b9Call(t, h.remoteSourceDelete, `{"id":`+jsonID(src.ID)+`,"keepNodes":false}`)
	r3b9RestoreGuardQuery(t, r)
	if out.Code == 0 {
		t.Fatalf("R3X-015: remote source delete must be refused when the guard query fails, got %+v", out)
	}
	gotSrc, err := r.GetRemoteSource(src.ID)
	if err != nil || gotSrc == nil {
		t.Fatalf("R3X-015: source must survive a guard-failed delete: %v %v", gotSrc, err)
	}
	if !r3b9InboundExists(t, r, row.ID) {
		t.Fatal("R3X-015: imported inbound must survive a guard-failed source delete")
	}
}

func TestR3B9GuardFailClosedKernelRemove(t *testing.T) {
	r, h := r3b9Open(t, "r3b9-kernel-remove.db")
	_, _, _, _, landingKernel := r3b9SeedChain(t, r)

	r3b9BreakGuardQuery(t, r)
	// Force 只放行「已知的引用清单」，不得放行守卫查询失败。
	out := r3b9Call(t, h.kernelControl, `{"id":`+jsonID(landingKernel.ID)+`,"action":"remove","force":true}`)
	r3b9RestoreGuardQuery(t, r)
	if out.Code == 0 {
		t.Fatalf("R3X-015: kernel remove must be refused when the guard query fails (even with force), got %+v", out)
	}
	k, err := r.GetNodeKernel(landingKernel.ID)
	if err != nil || k == nil {
		t.Fatalf("R3X-015: kernel must survive a guard-failed remove: %v %v", k, err)
	}
}

func TestR3B9GuardFailClosedNodeDelete(t *testing.T) {
	r, h := r3b9Open(t, "r3b9-node-delete.db")
	_, landingID, target, _, _ := r3b9SeedChain(t, r)

	r3b9BreakGuardQuery(t, r)
	out := r3b9Call(t, h.nodeDelete, `{"id":`+jsonID(landingID)+`}`)
	if out.Code == 0 {
		r3b9RestoreGuardQuery(t, r)
		t.Fatalf("R3X-015: node delete must be refused when the guard query fails, got %+v", out)
	}
	outBatch := r3b9Call(t, h.nodeBatchDelete, `{"ids":[`+jsonID(landingID)+`]}`)
	r3b9RestoreGuardQuery(t, r)
	batch := r3b9BatchData(t, outBatch.Data)
	if batch.SuccessCount != 0 || batch.FailCount != 1 {
		t.Fatalf("R3X-015: node batch delete must refuse the item when the guard query fails, got %+v", batch)
	}
	node, err := r.GetNodeByID(landingID)
	if err != nil || node == nil {
		t.Fatalf("R3X-015: node must survive guard-failed deletes: %v %v", node, err)
	}
	if !r3b9InboundExists(t, r, target.ID) {
		t.Fatal("R3X-015: node inbounds must survive guard-failed deletes")
	}
}

// 健康对照组：守卫查询正常时五路径行为不变——无引用可删、有引用按
// 既有口径（入站拒绝 / 删节点回传清单）处置。
func TestR3B9GuardHealthyControlsUnchanged(t *testing.T) {
	r, h := r3b9Open(t, "r3b9-controls.db")
	_, landingID, target, entry, landingKernel := r3b9SeedChain(t, r)

	// 有引用：入站单删被拒、数据未动。
	out := r3b9Call(t, h.inboundDelete, `{"id":`+jsonID(target.ID)+`}`)
	if out.Code == 0 || !strings.Contains(out.Msg, "front-vless") {
		t.Fatalf("control: referenced inbound delete must be refused with referrer named, got %+v", out)
	}
	if !r3b9InboundExists(t, r, target.ID) {
		t.Fatal("control: referenced inbound must survive")
	}

	// 有引用：内核卸载（非 Force）被拒并回传清单。
	out = r3b9Call(t, h.kernelControl, `{"id":`+jsonID(landingKernel.ID)+`,"action":"remove"}`)
	if out.Code == 0 {
		t.Fatalf("control: kernel remove with external referrers must be refused, got %+v", out)
	}
	if k, _ := r.GetNodeKernel(landingKernel.ID); k == nil {
		t.Fatal("control: kernel must survive a refused remove")
	}

	// 无引用：入站批量删放行。
	out = r3b9Call(t, h.inboundBatchDelete, `{"ids":[`+jsonID(entry.ID)+`]}`)
	if batch := r3b9BatchData(t, out.Data); batch.SuccessCount != 1 || batch.FailCount != 0 {
		t.Fatalf("control: unreferenced batch delete must succeed, got %+v (resp %+v)", batch, out)
	}
	if r3b9InboundExists(t, r, entry.ID) {
		t.Fatal("control: unreferenced inbound must be deleted")
	}

	// 引用解除后删节点：按 R2X-045 口径删除并回传清单（此处清单为空）。
	out = r3b9Call(t, h.nodeDelete, `{"id":`+jsonID(landingID)+`}`)
	if out.Code != 0 {
		t.Fatalf("control: node delete must succeed once unreferenced, got %+v", out)
	}
	if node, _ := r.GetNodeByID(landingID); node != nil {
		t.Fatal("control: node must be deleted")
	}
}
