package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// F-085 (audit FINAL §5-B8 / A7-012): federationRemoteUsageList 对每个
// 远端节点串行 fc.Connect（单个超时 10s），N 个慢远端让接口线性变慢。
// 改有界并发（上限 10，与同文件 syncRemoteNodeStatuses 的 O12-003
// 信号量同口径），结果顺序与错误语义不变。
// 修复前本测试失败：3 个各延迟 2s 的远端串行耗时 ≥6s。
func TestFederationRemoteUsageListConcurrentF085(t *testing.T) {
	// FIX-132 D3-01 后联邦出站客户端在拨号期复检目标地址；本用例的
	// 假提供方是 httptest 回环服务，与 tests/contract 同口径开启测试
	// 旁路（主题是并发预取，不是 SSRF 校验本身）。
	DisableSafeRemoteAddrCheckForTesting = true
	defer func() { DisableSafeRemoteAddrCheckForTesting = false }()
	r, err := repo.Open(filepath.Join(t.TempDir(), "b8-fed.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "unit"+"-test-secret")
	now := time.Now().UnixMilli()

	shareByNode := map[int64]int64{}
	for i := 0; i < 3; i++ {
		shareID := int64(101 + i)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			time.Sleep(2 * time.Second)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"shareId":%d,"maxBandwidth":100,"currentFlow":5,"expiryTime":0,"portRangeStart":30000,"portRangeEnd":30100},"ts":0}`, shareID)
		}))
		defer srv.Close()
		name := fmt.Sprintf("f085-remote-%d", i)
		if err := r.DB().Exec(`
			INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
			VALUES(?, ?, '10.0.0.1', '10.0.0.1', '', '30000-30100', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0, 1, ?, 'tok', '')
		`, name, name+"-secret", now, now, srv.URL).Error; err != nil {
			t.Fatal(err)
		}
		shareByNode[mustLastInsertID(t, r, name)] = shareID
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/remote-usage/list", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.federationRemoteUsageList(rec, req)
	elapsed := time.Since(start)
	if elapsed >= 4500*time.Millisecond {
		t.Fatalf("remote usage list must connect concurrently: took %v for 3 remotes x 2s", elapsed)
	}

	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code != 0 {
		t.Fatalf("expected success, got %+v", out)
	}
	raw, err := json.Marshal(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	var items []remoteUsageNodeItem
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d (%s)", len(items), raw)
	}
	// 顺序与 ListRemoteNodes 一致（id DESC），且每个节点的 shareId 对应
	// 其远端返回，证明并发没有串位。
	prevID := int64(1) << 62
	for _, it := range items {
		if it.NodeID >= prevID {
			t.Fatalf("items must keep the node list order (id DESC): %+v", items)
		}
		prevID = it.NodeID
		if want := shareByNode[it.NodeID]; it.ShareID != want {
			t.Fatalf("node %d: shareId=%d, want %d", it.NodeID, it.ShareID, want)
		}
		if it.SyncError != "" {
			t.Fatalf("node %d: unexpected syncError %q", it.NodeID, it.SyncError)
		}
	}
}
