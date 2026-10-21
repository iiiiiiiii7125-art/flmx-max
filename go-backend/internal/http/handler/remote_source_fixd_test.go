package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// FIX-D / A6-001：已有文件订阅源只切换 RelayOnly（不上传新文件）时，旧
// 实现把保存送进整源同步——文件源无 upload 必然失败、写回假 last_error，
// 且节点行 RelayOnly 永不更新（管理员以为已收紧为仅中转，节点实际仍可
// 直连）。期望：不触发同步、直接把新值落到该源的导入节点行、响应成功
// 且不写错误。
func TestFixdFileSourceRelayOnlyToggleAppliesDirectly(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/remote-fixd.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	src := &model.RemoteSource{
		Name: "文件源", Kind: model.RemoteSourceFile, RelayOnly: 0,
		Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source: %v", err)
	}
	mkInbound := func(name string, kernelID int64, relayOnly int) int64 {
		row := &model.NodeInbound{
			Name: name, Protocol: "ss", Listen: "203.0.113.5", Port: 8388,
			SourceID: src.ID, KernelID: kernelID, RelayOnly: relayOnly,
			Enabled: 1, CreatedTime: now, UpdatedTime: now,
		}
		if err := r.DB().Create(row).Error; err != nil {
			t.Fatalf("create inbound %s: %v", name, err)
		}
		return row.ID
	}
	importedID := mkInbound("导入节点", 0, 0)
	kernelRowID := mkInbound("内核节点", 9, 0)

	save := func(relayOnly bool) (int, string) {
		body := fmt.Sprintf(`{"id":%d,"name":"文件源","kind":"file","relayOnly":%v}`, src.ID, relayOnly)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/remote-source/save", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		h.remoteSourceSave(rec, req)
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode save response: %v (%s)", err, rec.Body.String())
		}
		return out.Code, out.Msg
	}
	reloadInbound := func(id int64) *model.NodeInbound {
		row, err := r.GetNodeInbound(id)
		if err != nil || row == nil {
			t.Fatalf("reload inbound %d: %v", id, err)
		}
		return row
	}

	// 打开 RelayOnly。
	code, msg := save(true)
	if code != 0 {
		t.Fatalf("save(relayOnly=true) code = %d msg = %q", code, msg)
	}
	got, err := r.GetRemoteSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RelayOnly != 1 {
		t.Fatalf("source relay_only = %d, want 1", got.RelayOnly)
	}
	if got.LastError != "" {
		t.Fatalf("A6-001: bogus last_error written: %q", got.LastError)
	}
	if row := reloadInbound(importedID); row.RelayOnly != 1 {
		t.Fatalf("A6-001: imported inbound relay_only = %d, want 1", row.RelayOnly)
	}
	if row := reloadInbound(kernelRowID); row.RelayOnly != 0 {
		t.Fatalf("kernel inbound must not be touched, relay_only = %d", row.RelayOnly)
	}

	// 关回去同样直接生效。
	code, msg = save(false)
	if code != 0 {
		t.Fatalf("save(relayOnly=false) code = %d msg = %q", code, msg)
	}
	if row := reloadInbound(importedID); row.RelayOnly != 0 {
		t.Fatalf("imported inbound relay_only = %d, want 0 after toggle off", row.RelayOnly)
	}
	got, _ = r.GetRemoteSource(src.ID)
	if got.LastError != "" {
		t.Fatalf("bogus last_error after toggle off: %q", got.LastError)
	}
}
