package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// R2-040（B-node-05）：批删 ids 不去重——同一 ID 出现两次时第二次
// 迭代因节点已删记「节点不存在」假失败，成功/失败计数同批自相
// 矛盾。钉死：节点批删（含其自有解析）与共用 idsFromBody 都在计数
// 前去重，与批量升级 nodeBatchUpgrade 的 uniqueInt64s 口径对齐。

func TestNodeBatchDeleteDeduplicatesIDs(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/batch-node-dedup.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.19")
	idStr := strconv.FormatInt(nodeID, 10)

	body := strings.NewReader(`{"ids":[` + idStr + `,` + idStr + `]}`)
	req := httptest.NewRequest("POST", "/api/v1/node/batch-delete", body)
	res := httptest.NewRecorder()
	h.nodeBatchDelete(res, req)

	var env struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
			Failures     []struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			} `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("batch delete code=%d body=%s", env.Code, res.Body.String())
	}
	if env.Data.SuccessCount != 1 || env.Data.FailCount != 0 || len(env.Data.Failures) != 0 {
		t.Fatalf("R2-040: duplicated id must count once as success, got %+v body=%s", env.Data, res.Body.String())
	}
	if n, err := r.GetNodeByID(nodeID); err != nil || n != nil {
		t.Fatalf("node should have been deleted, got %+v / %v", n, err)
	}
}

func TestIDsFromBodyDeduplicates(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/tunnel/batch-delete", strings.NewReader(`{"ids":[3,1,3,2,1,0,-5]}`))
	res := httptest.NewRecorder()
	ids := idsFromBody(req, res)
	want := []int64{1, 2, 3}
	if len(ids) != len(want) {
		t.Fatalf("idsFromBody = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("idsFromBody = %v, want %v", ids, want)
		}
	}
}
