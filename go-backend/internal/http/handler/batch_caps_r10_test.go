package handler

// 修复轮 10 路 caps：R10-08（I-F2-01）批量端点计数上限钉格。
//
// 基线（0cb4a7ba）红证：idsFromBody 对 5000 个互异 ID 原样返回
// 5000（无帽）；forwardBatchDelete 对 5000 个不存在 ID 逐条处理
// 4.59s、fail=5000、回执 205KB（I-F2 探针 5 万 ID 为 58.3s/2.1MB，
// 线性同量级）。修复后：共享解码点（idsFromBody/
// idsFromBodyWithInvalid）与各自有解析的批量面在去重/归一后统一
// 以 maxBatchIDs=1000 为帽、超限点名拒绝；批量升级的 50 帽不动。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

func r1008IDs(n int) []int64 {
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, int64(900000+i))
	}
	return ids
}

func r1008Envelope(t *testing.T, body string) (int, string) {
	t.Helper()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%.200s", err, body)
	}
	return env.Code, env.Msg
}

func r1008IDsBody(ids []int64) string {
	var sb strings.Builder
	sb.WriteString(`{"ids":[`)
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "%d", id)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

func TestR1008IDsFromBodyCapBoundaries(t *testing.T) {
	// 帽内：恰 1000 个互异 ID 放行。
	req := httptest.NewRequest("POST", "/", strings.NewReader(r1008IDsBody(r1008IDs(1000))))
	res := httptest.NewRecorder()
	if ids := idsFromBody(req, res); len(ids) != 1000 {
		t.Fatalf("idsFromBody(1000 distinct) = %d ids, want 1000", len(ids))
	}

	// 超帽：1001 个互异 ID 拒绝（返回 nil＋点名回执）。
	req = httptest.NewRequest("POST", "/", strings.NewReader(r1008IDsBody(r1008IDs(1001))))
	res = httptest.NewRecorder()
	if ids := idsFromBody(req, res); ids != nil {
		t.Fatalf("idsFromBody(1001 distinct) = %d ids, want nil (cap rejection)", len(ids))
	}
	if code, msg := r1008Envelope(t, res.Body.String()); code == 0 || !strings.Contains(msg, "单次批量操作最多 1000 条") {
		t.Fatalf("over-cap rejection must name the cap, got code=%d msg=%q", code, msg)
	}

	// 去重后计帽：5000 条同一 ID 去重为 1，不占额度、放行。
	dup := make([]int64, 5000)
	for i := range dup {
		dup[i] = 424242
	}
	req = httptest.NewRequest("POST", "/", strings.NewReader(r1008IDsBody(dup)))
	res = httptest.NewRecorder()
	ids := idsFromBody(req, res)
	if len(ids) != 1 || ids[0] != 424242 {
		t.Fatalf("idsFromBody(5000x same id) = %v, want [424242] (cap applies after dedup)", ids)
	}
}

func TestR1008IDsFromBodyWithInvalidCapBoundaries(t *testing.T) {
	call := func(ids []int64) ([]int64, []int64, string) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(r1008IDsBody(ids)))
		res := httptest.NewRecorder()
		valid, invalid := idsFromBodyWithInvalid(req, res)
		return valid, invalid, res.Body.String()
	}

	// 帽内：600 valid + 400 invalid = 1000，放行且两段齐全。
	mixed := append(r1008IDs(600), make([]int64, 400)...)
	valid, invalid, _ := call(mixed)
	if len(valid) != 600 || len(invalid) != 400 {
		t.Fatalf("600 valid + 400 invalid: got valid=%d invalid=%d, want 600/400", len(valid), len(invalid))
	}

	// 超帽：600 valid + 401 invalid = 1001（invalid 逐条计失败、
	// 同属工作量），整体拒绝。
	mixed = append(r1008IDs(600), make([]int64, 401)...)
	valid, _, body := call(mixed)
	if valid != nil {
		t.Fatalf("600 valid + 401 invalid must be rejected, got %d valid", len(valid))
	}
	if code, msg := r1008Envelope(t, body); code == 0 || !strings.Contains(msg, "单次批量操作最多 1000 条") {
		t.Fatalf("over-cap rejection must name the cap, got code=%d msg=%q", code, msg)
	}

	// 超帽：1001 个互异 valid 同样拒绝。
	valid, _, _ = call(r1008IDs(1001))
	if valid != nil {
		t.Fatalf("1001 valid must be rejected, got %d valid", len(valid))
	}
}

func TestR1008ForwardBatchDeleteCap(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r1008-fbd.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	// 超帽：1001 个不存在 ID 在逐条处理前整单拒绝。
	req := newAuthenticatedJSONRequest(t, map[string]any{"ids": r1008IDs(1001)})
	res := httptest.NewRecorder()
	h.forwardBatchDelete(res, req)
	if code, msg := r1008Envelope(t, res.Body.String()); code == 0 || !strings.Contains(msg, "单次批量操作最多 1000 条") {
		t.Fatalf("forwardBatchDelete(1001 ids) must be cap-rejected, got code=%d msg=%q", code, msg)
	}

	// 帽内：恰 1000 个不存在 ID 正常逐条处理、全记失败。
	req = newAuthenticatedJSONRequest(t, map[string]any{"ids": r1008IDs(1000)})
	res = httptest.NewRecorder()
	h.forwardBatchDelete(res, req)
	var env struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%.200s", err, res.Body.String())
	}
	if env.Code != 0 || env.Data.SuccessCount != 0 || env.Data.FailCount != 1000 {
		t.Fatalf("forwardBatchDelete(1000 nonexistent) = code %d %+v, want code 0 fail 1000", env.Code, env.Data)
	}
}

// r1008OrderEntries 构造 update-order 族的条目数组（id/inx 对）。
func r1008OrderEntries(n int) []map[string]any {
	entries := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, map[string]any{"id": int64(900000 + i), "inx": i})
	}
	return entries
}

// 批量面计数帽全集登记（C1-B01 收口；普查正本＝发版后审计
// C1-caps-idem §1B 的 45 行表，本清单须与代码现状一致）：
//   - maxBatchIDs=1000、超限整单点名拒绝：idsFromBody 系全集
//     （node/tunnel/inbound/forward 的批删、批暂停/恢复/重部署/
//     换隧道、隧道删除预览与连带转发删除）＋下方自有解析面 13 个
//     （R10-08 的 10 面＋C1-04 补的 update-order 三面）；
//   - 更严的专用帽先行判定、原样保留：node/batch-upgrade 50；
//   - 语义不同的帽各有钉格、不并入本门：手动导入/订阅源同步 500
//     （kernel_external、remote_source 各自的测试）、subscription
//     save 选集 100、latency 目标 200/30；
//   - 无帽但已裁定不属本族（信息级登记）：单资源的结构型列表字段
//     （tunnel create/update 的节点列表、user 的 groupIds——单事务
//     校验，C1-I08）；备份导入各段行数（users 段哈希预算 1000 另
//     钉，C1-I07）；
//   - 随功能移除消解（本轮代码尚在、暂无帽，移除落地后本注记须
//     同步删除、届时无帽批量面清单归零）：/group/tunnel/assign、
//     /group/user/assign、/subscription/group/order、
//     /subscription/group/save 的选集——由节点组/分组三件套移除路
//     （fixD/fixE）删除路由时收尾本注记。
//
// TestR1008SelfParsedFacesOverCap 逐面钉死不走 idsFromBody 系的
// 批量面（各自解析）在超帽时同口径点名拒绝。
func TestR1008SelfParsedFacesOverCap(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r1008-faces.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	over := r1008IDs(1001)
	tunnels := make([]map[string]any, 0, 1001)
	for i := 0; i < 1001; i++ {
		tunnels = append(tunnels, map[string]any{"tunnelId": 7})
	}

	faces := []struct {
		name string
		body map[string]any
		call func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{"nodeBatchDelete", map[string]any{"ids": over}, (*Handler).nodeBatchDelete},
		{"tunnelBatchDelete", map[string]any{"ids": over}, (*Handler).tunnelBatchDelete},
		{"inboundBatchDelete", map[string]any{"ids": over}, (*Handler).inboundBatchDelete},
		{"inboundBatchToggle", map[string]any{"ids": over, "enabled": true}, (*Handler).inboundBatchToggle},
		{"tunnelBatchDeletePreview", map[string]any{"ids": over}, (*Handler).tunnelBatchDeletePreview},
		{"tunnelBatchDeleteWithForwards", map[string]any{"ids": over, "action": ""}, (*Handler).tunnelBatchDeleteWithForwards},
		{"forwardBatchChangeTunnel", map[string]any{"forwardIds": over, "targetTunnelId": 1}, (*Handler).forwardBatchChangeTunnel},
		{"userTunnelBatchAssign", map[string]any{"userId": 1, "tunnels": tunnels}, (*Handler).userTunnelBatchAssign},
		{"userSubscriptionSet", map[string]any{"userId": 1, "subscriptionIds": over}, (*Handler).userSubscriptionSet},
		{"setUserInbounds", map[string]any{"userId": 1, "inboundIds": over}, (*Handler).setUserInbounds},
		{"nodeUpdateOrder", map[string]any{"nodes": r1008OrderEntries(1001)}, (*Handler).nodeUpdateOrder},
		{"tunnelUpdateOrder", map[string]any{"tunnels": r1008OrderEntries(1001)}, (*Handler).tunnelUpdateOrder},
		{"forwardUpdateOrder", map[string]any{"forwards": r1008OrderEntries(1001)}, (*Handler).forwardUpdateOrder},
	}
	for _, f := range faces {
		req := newAuthenticatedJSONRequest(t, f.body)
		res := httptest.NewRecorder()
		f.call(h, res, req)
		code, msg := r1008Envelope(t, res.Body.String())
		if code == 0 || !strings.Contains(msg, "单次批量操作最多 1000 条") {
			t.Fatalf("%s over-cap: got code=%d msg=%q, want cap rejection naming 1000", f.name, code, msg)
		}
	}
}

// TestC104UpdateOrderAtCapProcessed 钉格帽边界内侧：恰 1000 条
// update-order 条目须照常逐条处理，不得被帽误拒（帽只拦第 1001 条，
// 超帽侧由 TestR1008SelfParsedFacesOverCap 的三面钉死）。
func TestC104UpdateOrderAtCapProcessed(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/c104-uo.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	atCap := []struct {
		name string
		body map[string]any
		call func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{"nodeUpdateOrder", map[string]any{"nodes": r1008OrderEntries(1000)}, (*Handler).nodeUpdateOrder},
		{"tunnelUpdateOrder", map[string]any{"tunnels": r1008OrderEntries(1000)}, (*Handler).tunnelUpdateOrder},
	}
	for _, f := range atCap {
		req := newAuthenticatedJSONRequest(t, f.body)
		res := httptest.NewRecorder()
		f.call(h, res, req)
		var env struct {
			Code int `json:"code"`
			Data struct {
				SuccessCount int `json:"successCount"`
				FailCount    int `json:"failCount"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s decode: %v body=%.200s", f.name, err, res.Body.String())
		}
		if env.Code != 0 || env.Data.SuccessCount != 0 || env.Data.FailCount != 1000 {
			t.Fatalf("%s(1000 nonexistent) = code %d %+v, want code 0 fail 1000 (at-cap must be processed, not cap-rejected)", f.name, env.Code, env.Data)
		}
	}

	// forward 面帽内放行后进两段式归属校验：不存在的转发以「转发
	// 不存在」整单回执（非帽文案），证明 1000 条未被帽拦下。
	req := newAuthenticatedJSONRequest(t, map[string]any{"forwards": r1008OrderEntries(1000)})
	res := httptest.NewRecorder()
	h.forwardUpdateOrder(res, req)
	code, msg := r1008Envelope(t, res.Body.String())
	if strings.Contains(msg, "单次批量操作最多") {
		t.Fatalf("forwardUpdateOrder(1000) must not be cap-rejected, got msg=%q", msg)
	}
	if code == 0 || !strings.Contains(msg, "转发不存在") {
		t.Fatalf("forwardUpdateOrder(1000 nonexistent) = code %d msg=%q, want 转发不存在 from the access check", code, msg)
	}
}
