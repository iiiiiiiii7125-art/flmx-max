package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

func decodeCode(t *testing.T, body string) response.R {
	t.Helper()
	var out response.R
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// X-P3-28 (L-11-3, V-12): user/forward/inbound/subscription 四类
// delete 对不存在统一返回成功（与 node/tunnel/speedLimit 对齐）。
// 修复前四类均回「不存在」错误。
func TestR1B8_XP328_DeleteNonexistentReturnsSuccess(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp328.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	if out := decodeCode(t, postJSONToHandler(t, h.userDelete, map[string]interface{}{"id": 999999}).Body.String()); out.Code != 0 {
		t.Fatalf("userDelete 不存在应成功: %+v", out)
	}
	if out := decodeCode(t, postJSONToHandler(t, h.inboundDelete, map[string]interface{}{"id": 999999}).Body.String()); out.Code != 0 {
		t.Fatalf("inboundDelete 不存在应成功: %+v", out)
	}
	if out := decodeCode(t, postJSONToHandler(t, h.subscriptionDelete, map[string]interface{}{"id": 999999}).Body.String()); out.Code != 0 {
		t.Fatalf("subscriptionDelete 不存在应成功: %+v", out)
	}
	req := newAuthenticatedJSONRequest(t, map[string]interface{}{"id": 999999})
	res := httptest.NewRecorder()
	h.forwardDelete(res, req)
	if out := decodeCode(t, res.Body.String()); out.Code != 0 {
		t.Fatalf("forwardDelete 不存在应成功: %+v", out)
	}
	req = newAuthenticatedJSONRequest(t, map[string]interface{}{"id": 999999})
	res = httptest.NewRecorder()
	h.forwardForceDelete(res, req)
	if out := decodeCode(t, res.Body.String()); out.Code != 0 {
		t.Fatalf("forwardForceDelete 不存在应成功: %+v", out)
	}
}

// X-P3-29 (L-11-4): 节点升级同版本短路——cmp==0 时提示已最新、
// 不下发 UpgradeAgent、不重启。修复前同版本会真下发（本用例节点
// 离线，下发必失败回 -2，可作区分）。
func TestR1B8_XP329_NodeUpgradeSameVersionShortCircuits(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp329.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status, version) VALUES('xp329-node', 's329', '198.51.100.41', '1000-2000', ?, 1, '1.3.4')`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")

	out := decodeCode(t, postJSONToHandler(t, h.nodeUpgrade, map[string]interface{}{"id": nodeID, "version": "1.3.4"}).Body.String())
	if out.Code != 0 {
		t.Fatalf("同版本升级应短路成功（已最新），got %+v", out)
	}

	// 防过修：降级仍拒。
	out = decodeCode(t, postJSONToHandler(t, h.nodeUpgrade, map[string]interface{}{"id": nodeID, "version": "1.3.3"}).Body.String())
	if out.Code == 0 {
		t.Fatalf("降级应被拒, got %+v", out)
	}
	// 防过修：真升级仍走下发（节点离线 → 下发失败 -2，证明未被短路误吞）。
	out = decodeCode(t, postJSONToHandler(t, h.nodeUpgrade, map[string]interface{}{"id": nodeID, "version": "1.3.5"}).Body.String())
	if out.Code == 0 {
		t.Fatalf("更高版本应尝试下发并因节点离线失败, got %+v", out)
	}
}
