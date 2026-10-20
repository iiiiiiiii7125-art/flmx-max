package contract_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/auth"
)

// F-025 (audit FINAL §5-B8 / A6-004): 订阅源同步无批内去重——源内重复条目
// 会重复建行（external 行 kernel_id=0，部分唯一索引不兜底），或后续轮次
// 撞约束每轮报错。手动导入早有 seen 去重，同步路径遗漏。
// 修复前本测试失败：同一节点建出 2 行且无重复提示。
func TestRemoteSourceSyncDedupesBatchF025(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	secret := "unit" + "-test-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	body := strings.Join([]string{
		ssLink("203.0.113.1", "8388", "香港 01"),
		ssLink("203.0.113.1", "8388", "香港 01"), // 源内重复条目（同名同址）
		ssLink("203.0.113.2", "8388", "日本 01"),
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(body))))
	}))
	defer srv.Close()

	out := kernelContractPost(t, router, admin, "/api/v1/subscription/source/save",
		`{"name":"机场","url":"`+srv.URL+`","intervalHours":12}`)
	if out.Code != 0 {
		t.Fatalf("create source: %+v", out)
	}
	src := out.Data.(map[string]interface{})["source"].(map[string]interface{})
	srcID := int64(src["id"].(float64))
	if got := valueAsInt(src["nodeCount"]); got != 2 {
		t.Fatalf("duplicated entry must be imported once, nodeCount=%d (%v)", got, src)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound WHERE source_id = ? AND name = '香港 01'`, srcID); n != 1 {
		t.Fatalf("expected exactly 1 row for the duplicated node, got %d", n)
	}

	// 再刷新一轮：不得新增行、不得撞约束报错，重复提示要在 errors 里。
	out = kernelContractPost(t, router, admin, "/api/v1/subscription/source/refresh", `{"id":`+jsonInt64(srcID)+`}`)
	if out.Code != 0 {
		t.Fatalf("refresh: %+v", out)
	}
	res := out.Data.(map[string]interface{})["result"].(map[string]interface{})
	if got := valueAsInt(res["added"]); got != 0 {
		t.Fatalf("second sync must not add rows, added=%d (%v)", got, res)
	}
	errs, _ := res["errors"].([]interface{})
	foundDup := false
	for _, e := range errs {
		if s, _ := e.(string); strings.Contains(s, "重复") {
			foundDup = true
		}
	}
	if !foundDup {
		t.Fatalf("sync errors must flag the in-batch duplicate, got %v", errs)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound WHERE source_id = ?`, srcID); n != 2 {
		t.Fatalf("rows must stay 2 after re-sync, got %d", n)
	}
}
