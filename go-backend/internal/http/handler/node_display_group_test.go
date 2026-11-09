package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// 节点展示分组 v2（用户 2026-11-09 23:34 改版）：批量移入端点的
// 逐条回执（M-0051 口径）与分组管理三端点（建组注册/改名级联/
// 删组回落）的行为钉死。回执只认信封计数＋库内实值，不认 200。

type nodeGroupEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func postNodeGroup(t *testing.T, fn func(http.ResponseWriter, *http.Request), path, body string) nodeGroupEnvelope {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	res := httptest.NewRecorder()
	fn(res, req)
	var env nodeGroupEnvelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %s: %v body=%s", path, err, res.Body.String())
	}
	return env
}

func seedGroupedNode(t *testing.T, r *repo.Repository, name, ip, group string) int64 {
	t.Helper()
	id := seedRelayTargetNode(t, r, name, ip)
	if group != "" {
		if err := r.UpdateNodeDisplayGroup(id, group); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func readNodeGroup(t *testing.T, r *repo.Repository, id int64) string {
	t.Helper()
	n, err := r.GetNodeByID(id)
	if err != nil || n == nil {
		t.Fatalf("read node #%d: %v", id, err)
	}
	return n.DisplayGroup
}

func readGroupRegistry(t *testing.T, r *repo.Repository) []string {
	t.Helper()
	raw, err := r.GetViteConfigValue(nodeGroupOrderConfigKey)
	if err != nil {
		return []string{}
	}
	return parseNodeGroupOrderValue(raw)
}

func TestNodeBatchSetGroupAllSuccessAndMoveOut(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/group-batch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	id1 := seedGroupedNode(t, r, "g-a", "198.51.100.31", "")
	id2 := seedGroupedNode(t, r, "g-b", "198.51.100.32", "")
	id3 := seedGroupedNode(t, r, "g-c", "198.51.100.33", "")

	env := postNodeGroup(t, h.nodeBatchSetGroup, "/api/v1/node/batch-set-group",
		`{"ids":[`+itoa(id1)+`,`+itoa(id2)+`],"group":" 落地 "}`)
	if env.Code != 0 {
		t.Fatalf("batch set code=%d msg=%s", env.Code, env.Msg)
	}
	var result batchOperationResult
	if err := json.Unmarshal(env.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 2 || result.FailCount != 0 || len(result.Failures) != 0 {
		t.Fatalf("receipt = %+v, want 2 ok / 0 fail", result)
	}
	if g := readNodeGroup(t, r, id1); g != "落地" {
		t.Fatalf("node1 group = %q, want trimmed 落地", g)
	}
	if g := readNodeGroup(t, r, id2); g != "落地" {
		t.Fatalf("node2 group = %q, want 落地", g)
	}

	// 移出：空 group 回未分组；重复 id 只计一次。
	env = postNodeGroup(t, h.nodeBatchSetGroup, "/api/v1/node/batch-set-group",
		`{"ids":[`+itoa(id1)+`,`+itoa(id1)+`,`+itoa(id3)+`],"group":""}`)
	if env.Code != 0 {
		t.Fatalf("move-out code=%d msg=%s", env.Code, env.Msg)
	}
	if err := json.Unmarshal(env.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 2 || result.FailCount != 0 {
		t.Fatalf("move-out receipt = %+v, want 2 ok (dedup) / 0 fail", result)
	}
	if g := readNodeGroup(t, r, id1); g != "" {
		t.Fatalf("node1 group = %q, want empty after move-out", g)
	}
}

func TestNodeBatchSetGroupPartialFailure(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/group-batch-partial.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	id1 := seedGroupedNode(t, r, "g-p", "198.51.100.34", "")

	env := postNodeGroup(t, h.nodeBatchSetGroup, "/api/v1/node/batch-set-group",
		`{"ids":[`+itoa(id1)+`,999999],"group":"中转"}`)
	if env.Code != 0 {
		t.Fatalf("partial batch code=%d msg=%s (逐条回执面整单不得失败)", env.Code, env.Msg)
	}
	var result batchOperationResult
	if err := json.Unmarshal(env.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 1 || result.FailCount != 1 || len(result.Failures) != 1 {
		t.Fatalf("receipt = %+v, want 1 ok / 1 fail / 1 detail", result)
	}
	if result.Failures[0].ID != 999999 || !strings.Contains(result.Failures[0].Reason, "节点不存在") {
		t.Fatalf("failure detail = %+v, want id 999999 + 节点不存在", result.Failures[0])
	}
	if g := readNodeGroup(t, r, id1); g != "中转" {
		t.Fatalf("surviving node group = %q, want 中转 (partial failure must not roll back successes)", g)
	}

	// 整单拒绝面：空 ids、超长组名。
	if env := postNodeGroup(t, h.nodeBatchSetGroup, "/api/v1/node/batch-set-group", `{"ids":[],"group":"中转"}`); env.Code == 0 {
		t.Fatalf("empty ids must be rejected, got code 0")
	}
	long := strings.Repeat("组", 101)
	if env := postNodeGroup(t, h.nodeBatchSetGroup, "/api/v1/node/batch-set-group", `{"ids":[`+itoa(id1)+`],"group":"`+long+`"}`); env.Code == 0 {
		t.Fatalf("overlong group must be rejected, got code 0")
	}
}

func TestNodeGroupCreateRegistersAndRejectsDuplicates(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/group-create.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	seedGroupedNode(t, r, "g-u", "198.51.100.35", "在用组")

	env := postNodeGroup(t, h.nodeGroupCreate, "/api/v1/node/group/create", `{"name":" 机房A "}`)
	if env.Code != 0 {
		t.Fatalf("create code=%d msg=%s", env.Code, env.Msg)
	}
	var created nodeGroupCreateResult
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "机房A" || len(created.Order) != 1 || created.Order[0] != "机房A" {
		t.Fatalf("create result = %+v, want trimmed name registered", created)
	}
	if got := readGroupRegistry(t, r); len(got) != 1 || got[0] != "机房A" {
		t.Fatalf("registry = %v, want [机房A] persisted", got)
	}

	// 重名（名单内）、重名（节点在用未在册）、保留名、空名全拒。
	for _, body := range []string{
		`{"name":"机房A"}`,
		`{"name":"在用组"}`,
		`{"name":"未分组"}`,
		`{"name":"  "}`,
	} {
		if env := postNodeGroup(t, h.nodeGroupCreate, "/api/v1/node/group/create", body); env.Code == 0 {
			t.Fatalf("create %s must be rejected, got code 0", body)
		}
	}
	if got := readGroupRegistry(t, r); len(got) != 1 {
		t.Fatalf("registry after rejections = %v, want unchanged [机房A]", got)
	}
}

func TestNodeGroupRenameCascades(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/group-rename.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	id1 := seedGroupedNode(t, r, "g-r1", "198.51.100.36", "旧名")
	id2 := seedGroupedNode(t, r, "g-r2", "198.51.100.37", "旧名")
	id3 := seedGroupedNode(t, r, "g-r3", "198.51.100.38", "其他")
	id4 := seedGroupedNode(t, r, "g-r4", "198.51.100.39", "野组")
	if err := r.UpsertConfig(nodeGroupOrderConfigKey, `["旧名","其他"]`, 1); err != nil {
		t.Fatal(err)
	}

	env := postNodeGroup(t, h.nodeGroupRename, "/api/v1/node/group/rename", `{"oldName":"旧名","newName":"新名"}`)
	if env.Code != 0 {
		t.Fatalf("rename code=%d msg=%s", env.Code, env.Msg)
	}
	var renamed nodeGroupRenameResult
	if err := json.Unmarshal(env.Data, &renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.UpdatedCount != 2 {
		t.Fatalf("rename updatedCount = %d, want 2 members cascaded", renamed.UpdatedCount)
	}
	if len(renamed.Order) != 2 || renamed.Order[0] != "新名" || renamed.Order[1] != "其他" {
		t.Fatalf("rename order = %v, want in-place [新名 其他]", renamed.Order)
	}
	if g := readNodeGroup(t, r, id1); g != "新名" {
		t.Fatalf("node1 group = %q, want 新名", g)
	}
	if g := readNodeGroup(t, r, id2); g != "新名" {
		t.Fatalf("node2 group = %q, want 新名", g)
	}
	if g := readNodeGroup(t, r, id3); g != "其他" {
		t.Fatalf("node3 group = %q, want 其他 untouched", g)
	}

	// 名单外在用标签改名：级联＋新名追加进名单。
	env = postNodeGroup(t, h.nodeGroupRename, "/api/v1/node/group/rename", `{"oldName":"野组","newName":"正规组"}`)
	if env.Code != 0 {
		t.Fatalf("rename unregistered code=%d msg=%s", env.Code, env.Msg)
	}
	if err := json.Unmarshal(env.Data, &renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.UpdatedCount != 1 || len(renamed.Order) != 3 || renamed.Order[2] != "正规组" {
		t.Fatalf("unregistered rename = %+v, want 1 member + appended order", renamed)
	}
	if g := readNodeGroup(t, r, id4); g != "正规组" {
		t.Fatalf("node4 group = %q, want 正规组", g)
	}

	// 改到已存在组名、改不存在的组、同名改名全拒。
	for _, body := range []string{
		`{"oldName":"新名","newName":"其他"}`,
		`{"oldName":"不存在","newName":"随便"}`,
		`{"oldName":"新名","newName":"新名"}`,
		`{"oldName":"新名","newName":"未分组"}`,
	} {
		if env := postNodeGroup(t, h.nodeGroupRename, "/api/v1/node/group/rename", body); env.Code == 0 {
			t.Fatalf("rename %s must be rejected, got code 0", body)
		}
	}
}

func TestNodeGroupDeleteFallsBackToUngrouped(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/group-delete.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	id1 := seedGroupedNode(t, r, "g-d1", "198.51.100.40", "待删组")
	id2 := seedGroupedNode(t, r, "g-d2", "198.51.100.41", "待删组")
	id3 := seedGroupedNode(t, r, "g-d3", "198.51.100.42", "保留组")
	if err := r.UpsertConfig(nodeGroupOrderConfigKey, `["待删组","保留组","空组"]`, 1); err != nil {
		t.Fatal(err)
	}

	env := postNodeGroup(t, h.nodeGroupDelete, "/api/v1/node/group/delete", `{"name":"待删组"}`)
	if env.Code != 0 {
		t.Fatalf("delete code=%d msg=%s", env.Code, env.Msg)
	}
	var deleted nodeGroupDeleteResult
	if err := json.Unmarshal(env.Data, &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.UpdatedCount != 2 {
		t.Fatalf("delete updatedCount = %d, want 2 members fell back", deleted.UpdatedCount)
	}
	if len(deleted.Order) != 2 || deleted.Order[0] != "保留组" || deleted.Order[1] != "空组" {
		t.Fatalf("delete order = %v, want [保留组 空组]", deleted.Order)
	}
	if g := readNodeGroup(t, r, id1); g != "" {
		t.Fatalf("node1 group = %q, want empty (ungrouped fallback)", g)
	}
	if g := readNodeGroup(t, r, id2); g != "" {
		t.Fatalf("node2 group = %q, want empty (ungrouped fallback)", g)
	}
	if g := readNodeGroup(t, r, id3); g != "保留组" {
		t.Fatalf("node3 group = %q, want 保留组 untouched", g)
	}

	// 空组删除：零成员、回执 0、名单摘除。
	env = postNodeGroup(t, h.nodeGroupDelete, "/api/v1/node/group/delete", `{"name":"空组"}`)
	if env.Code != 0 {
		t.Fatalf("delete empty group code=%d msg=%s", env.Code, env.Msg)
	}
	if err := json.Unmarshal(env.Data, &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.UpdatedCount != 0 || len(deleted.Order) != 1 {
		t.Fatalf("delete empty = %+v, want 0 members + order [保留组]", deleted)
	}

	if env := postNodeGroup(t, h.nodeGroupDelete, "/api/v1/node/group/delete", `{"name":"不存在"}`); env.Code == 0 {
		t.Fatalf("delete of unknown group must be rejected, got code 0")
	}
}
