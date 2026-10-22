package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/http/middleware"
	"go-backend/internal/store/repo"
)

// X-P3-26 (L-10-2): 批量升级循环派发前查 r.Context——请求断连/
// 取消后剩余节点不再派发。以已取消 ctx 调用 nodeBatchUpgrade，
// 全部结果应为「已取消」而非逐个真下发（离线错误）。
func TestR1B8_XP326_BatchUpgradeStopsOnCancelledContext(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp326.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	req := newAuthenticatedJSONRequest(t, map[string]interface{}{
		"ids":     []int64{101, 102, 103},
		"version": "9.9.9",
	})
	claims := req.Context().Value(middleware.ClaimsContextKey)
	base, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := context.WithValue(base, middleware.ClaimsContextKey, claims)
	req = req.WithContext(ctx)

	res := httptest.NewRecorder()
	h.nodeBatchUpgrade(res, req)

	var payload struct {
		Code int `json:"code"`
		Data struct {
			Results []struct {
				ID      int64  `json:"id"`
				Success bool   `json:"success"`
				Message string `json:"message"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	if len(payload.Data.Results) != 3 {
		t.Fatalf("应有 3 条结果, got %s", res.Body.String())
	}
	for _, item := range payload.Data.Results {
		if item.Success {
			t.Fatalf("取消后不应派发成功: %+v", item)
		}
		if item.Message == "" || !strings.Contains(item.Message, "取消") {
			t.Fatalf("取消后的结果应明示已取消, got %+v", item)
		}
	}
}
