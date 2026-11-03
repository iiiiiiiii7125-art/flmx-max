package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R10-04（I-F3-03）钉格：导入节点手工删除必须粘性——删除同写屏蔽登
// 记，订阅源后续同步不复活；身份按同步匹配的同一身份元组判（改名/
// 改址即新节点、不受屏蔽）；屏蔽随源删除而清；批删同口径。
// 基线红证：同一场景在 0cb4a7ba 上同步建行 id=1 → 手工删 → 再同步
// 以 id=2 复活（zz 探针实测 RESURRECTED）。

const (
	r10LinkA       = "vless://11111111-2222-3333-4444-555555555555@203.0.113.30:443?security=none#probe-node"
	r10LinkRenamed = "vless://11111111-2222-3333-4444-555555555555@203.0.113.30:443?security=none#probe-renamed"
	r10LinkMoved   = "vless://11111111-2222-3333-4444-555555555555@203.0.113.31:443?security=none#probe-renamed"
	r10LinkB       = "vless://11111111-2222-3333-4444-555555555555@203.0.113.40:443?security=none#probe-peer"
)

type r10Resp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func r10ShieldSetup(t *testing.T) (*repo.Repository, *Handler, *model.RemoteSource) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "r10shield.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{Name: "源", Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source: %v", err)
	}
	return r, h, src
}

func r10Call(t *testing.T, fn func(http.ResponseWriter, *http.Request), path, body string) r10Resp {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	fn(rec, req)
	var out r10Resp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("call %s: undecodable response %q: %v", path, rec.Body.String(), err)
	}
	return out
}

func r10SyncOne(t *testing.T, h *Handler, src *model.RemoteSource, body string) remoteSyncResult {
	t.Helper()
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return res
}

func r10OnlyRow(t *testing.T, r *repo.Repository, sourceID int64) *model.NodeInbound {
	t.Helper()
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	var found *model.NodeInbound
	for i := range rows {
		if rows[i].SourceID == sourceID {
			if found != nil {
				t.Fatalf("want at most one row for source %d, got more", sourceID)
			}
			found = &rows[i]
		}
	}
	return found
}

func r10RowByName(t *testing.T, r *repo.Repository, sourceID int64, name string) *model.NodeInbound {
	t.Helper()
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatalf("list inbounds: %v", err)
	}
	for i := range rows {
		if rows[i].SourceID == sourceID && rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

func r10DeleteInbound(t *testing.T, h *Handler, id int64) r10Resp {
	t.Helper()
	return r10Call(t, h.inboundDelete, "/api/v1/inbound/delete", `{"id":`+strconv.FormatInt(id, 10)+`}`)
}

// 主格：同步建行 → 单删（回执点名屏蔽）→ 再同步不复活且计数点名，
// 第三轮仍屏蔽（粘性不是一轮性的）。
func TestR10ShieldDeletedImportedNodeStaysDeleted(t *testing.T) {
	r, h, src := r10ShieldSetup(t)

	res := r10SyncOne(t, h, src, r10LinkA)
	if res.Added != 1 {
		t.Fatalf("sync 1: added=%d, want 1 (errors=%v)", res.Added, res.Errors)
	}
	row := r10OnlyRow(t, r, src.ID)
	if row == nil {
		t.Fatal("sync 1: row must exist")
	}

	out := r10DeleteInbound(t, h, row.ID)
	if out.Code != 0 {
		t.Fatalf("delete: code=%d msg=%q", out.Code, out.Msg)
	}
	if !strings.Contains(out.Msg, "删除屏蔽") || !strings.Contains(out.Msg, "不会再自动恢复") {
		t.Fatalf("delete receipt must name the shield semantics, got msg=%q", out.Msg)
	}
	if got, _ := r.GetNodeInbound(row.ID); got != nil {
		t.Fatalf("row must be gone after delete, got %+v", got)
	}
	shields, err := r.ListRemoteSourceShields(src.ID)
	if err != nil || len(shields) != 1 {
		t.Fatalf("after delete: shields=%v err=%v, want exactly 1", shields, err)
	}
	wantKey := importedIdentityKey("probe-node", "203.0.113.30", 443)
	if shields[0].IdentityKey != wantKey {
		t.Fatalf("shield identity key = %q, want %q (sync matching tuple)", shields[0].IdentityKey, wantKey)
	}

	for round := 2; round <= 3; round++ {
		res = r10SyncOne(t, h, src, r10LinkA)
		if res.Added != 0 || res.Shielded != 1 {
			t.Fatalf("sync %d: added=%d shielded=%d, want added=0 shielded=1 (errors=%v)", round, res.Added, res.Shielded, res.Errors)
		}
		if len(res.Errors) != 0 {
			t.Fatalf("sync %d: shielded skip must not pollute errors, got %v", round, res.Errors)
		}
		if got := r10OnlyRow(t, r, src.ID); got != nil {
			t.Fatalf("sync %d: deleted node resurrected as id=%d", round, got.ID)
		}
	}
}

// 身份元组格：改名（同址）与改址（同名）都是新元组、新节点，照常
// 新建、不受旧屏蔽影响。
func TestR10ShieldRenamedOrMovedEntryIsNewNode(t *testing.T) {
	r, h, src := r10ShieldSetup(t)

	r10SyncOne(t, h, src, r10LinkA)
	row := r10OnlyRow(t, r, src.ID)
	if out := r10DeleteInbound(t, h, row.ID); out.Code != 0 {
		t.Fatalf("delete original: code=%d msg=%q", out.Code, out.Msg)
	}

	// 改名：同地址、名称变 → 新元组。
	res := r10SyncOne(t, h, src, r10LinkRenamed)
	if res.Added != 1 || res.Shielded != 0 {
		t.Fatalf("renamed entry: added=%d shielded=%d, want added=1 shielded=0 (errors=%v)", res.Added, res.Shielded, res.Errors)
	}
	row = r10OnlyRow(t, r, src.ID)
	if row == nil || row.Name != "probe-renamed" {
		t.Fatalf("renamed entry must be created as probe-renamed, got %+v", row)
	}
	if out := r10DeleteInbound(t, h, row.ID); out.Code != 0 {
		t.Fatalf("delete renamed: code=%d msg=%q", out.Code, out.Msg)
	}

	// 改址：同名称、地址变 → 新元组。
	res = r10SyncOne(t, h, src, r10LinkMoved)
	if res.Added != 1 || res.Shielded != 0 {
		t.Fatalf("moved entry: added=%d shielded=%d, want added=1 shielded=0 (errors=%v)", res.Added, res.Shielded, res.Errors)
	}
	row = r10OnlyRow(t, r, src.ID)
	if row == nil || row.Listen != "203.0.113.31" {
		t.Fatalf("moved entry must be created at the new address, got %+v", row)
	}
}

// 屏蔽随源删除而清：连节点删与保留节点两路同口径。
func TestR10ShieldClearedWithSourceDelete(t *testing.T) {
	for _, keepNodes := range []bool{false, true} {
		r, h, src := r10ShieldSetup(t)
		r10SyncOne(t, h, src, r10LinkA+"\n"+r10LinkB)
		row := r10RowByName(t, r, src.ID, "probe-node")
		if row == nil {
			t.Fatalf("keepNodes=%v: probe-node row must exist after sync", keepNodes)
		}
		if out := r10DeleteInbound(t, h, row.ID); out.Code != 0 {
			t.Fatalf("keepNodes=%v: delete node: code=%d msg=%q", keepNodes, out.Code, out.Msg)
		}
		if shields, _ := r.ListRemoteSourceShields(src.ID); len(shields) != 1 {
			t.Fatalf("keepNodes=%v: want 1 shield before source delete, got %v", keepNodes, shields)
		}
		body := `{"id":` + strconv.FormatInt(src.ID, 10) + `,"keepNodes":` + strconv.FormatBool(keepNodes) + `}`
		out := r10Call(t, h.remoteSourceDelete, "/api/v1/subscription/source/delete", body)
		if out.Code != 0 {
			t.Fatalf("keepNodes=%v: source delete: code=%d msg=%q", keepNodes, out.Code, out.Msg)
		}
		if shields, err := r.ListRemoteSourceShields(src.ID); err != nil || len(shields) != 0 {
			t.Fatalf("keepNodes=%v: shields must be cleared with the source, got %v err=%v", keepNodes, shields, err)
		}
		if got, _ := r.GetRemoteSource(src.ID); got != nil {
			t.Fatalf("keepNodes=%v: source must be gone, got %+v", keepNodes, got)
		}
	}
}

// 批删同口径：经 inboundBatchDelete 删除的导入节点同样登记屏蔽、
// 不被同步复活。
func TestR10ShieldBatchDeleteAlsoShields(t *testing.T) {
	r, h, src := r10ShieldSetup(t)

	r10SyncOne(t, h, src, r10LinkA)
	row := r10OnlyRow(t, r, src.ID)
	out := r10Call(t, h.inboundBatchDelete, "/api/v1/inbound/batch-delete", `{"ids":[`+strconv.FormatInt(row.ID, 10)+`]}`)
	if out.Code != 0 {
		t.Fatalf("batch delete: code=%d msg=%q", out.Code, out.Msg)
	}
	var batchRes struct {
		SuccessCount int `json:"successCount"`
		FailCount    int `json:"failCount"`
	}
	if err := json.Unmarshal(out.Data, &batchRes); err != nil || batchRes.SuccessCount != 1 {
		t.Fatalf("batch delete: success=%d fail=%d err=%v body=%s", batchRes.SuccessCount, batchRes.FailCount, err, out.Data)
	}
	if shields, _ := r.ListRemoteSourceShields(src.ID); len(shields) != 1 {
		t.Fatalf("batch delete must record the shield, got %v", shields)
	}
	res := r10SyncOne(t, h, src, r10LinkA)
	if res.Added != 0 || res.Shielded != 1 {
		t.Fatalf("sync after batch delete: added=%d shielded=%d, want 0/1", res.Added, res.Shielded)
	}
	if got := r10OnlyRow(t, r, src.ID); got != nil {
		t.Fatalf("batch-deleted node resurrected as id=%d", got.ID)
	}
}

// 摘要点名：后端 summary 与前端 syncText 同文案报屏蔽计数。
func TestRemoteSyncResultSummaryNamesShielded(t *testing.T) {
	s := remoteSyncResult{Shielded: 2}.summary()
	if !strings.Contains(s, "2 个节点已手工删除屏蔽，本轮同步未恢复") {
		t.Fatalf("summary must name the shielded count: %s", s)
	}
	if got := (remoteSyncResult{}).summary(); strings.Contains(got, "屏蔽") {
		t.Fatalf("zero shielded must not mention shielding: %s", got)
	}
}
