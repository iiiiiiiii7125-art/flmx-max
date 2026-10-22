package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

func consumerCleanupFixture(t *testing.T, remoteURL string) (*repo.Repository, *Handler) {
	t.Helper()
	// FIX-132 D3-01 后联邦出站客户端在拨号期复检目标地址；本文件的
	// 假提供方是 httptest 回环服务，与 tests/contract 同口径开启测试
	// 旁路（这些用例的主题是清理/回滚语义，不是 SSRF 校验本身）。
	DisableSafeRemoteAddrCheckForTesting = true
	t.Cleanup(func() { DisableSafeRemoteAddrCheckForTesting = false })
	r, err := repo.Open(filepath.Join(t.TempDir(), "consumer-cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.DB().Create(&model.Node{ID: 1, Name: "remote", Secret: "remote", ServerIP: "127.0.0.1", Port: "31000-31010", IsRemote: 1, Status: 1, RemoteURL: sql.NullString{String: remoteURL, Valid: true}, RemoteToken: sql.NullString{String: "token", Valid: true}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertFederationTunnelBinding(&repo.FederationTunnelBinding{TunnelID: 42, NodeID: 1, ChainType: 3, RemoteURL: remoteURL, ResourceKey: "tunnel:42:node:1:type:3:hop:0", RemoteBindingID: "binding-old", AllocatedPort: 31001, Status: 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 42, Name: "live tunnel", Type: 2, Protocol: "tls", Flow: 1, TrafficRatio: 1, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	return r, &Handler{repo: r, wsServer: ws.NewServer(r, "consumer-cleanup")}
}

func TestTunnelUpdateValidationPreservesSharedRuntime(t *testing.T) {
	for _, body := range []string{
		`{"id":42,"type":2,"name":"invalid update","inNodeId":[]}`,
		`{"id":42,"type":2,"name":"invalid update","inNodeId":[{"nodeId":999}],"outNodeId":[{"nodeId":1,"port":31001}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			var releases atomic.Int64
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				releases.Add(1)
				response.WriteJSON(w, response.OKEmpty())
			}))
			defer remote.Close()
			r, h := consumerCleanupFixture(t, remote.URL)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			h.tunnelUpdate(rec, req)
			var payload response.R
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			bindings, err := r.ListActiveFederationTunnelBindingsByTunnel(42)
			if err != nil {
				t.Fatal(err)
			}
			name, err := r.GetTunnelName(42)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Code == 0 || releases.Load() != 0 || len(bindings) != 1 || name != "live tunnel" {
				t.Fatalf("invalid update changed runtime: code=%d releases=%d bindings=%v name=%s", payload.Code, releases.Load(), bindings, name)
			}
		})
	}
}

func TestFederationCleanupRetainsFailedBindingAndRetriesOnlyPending(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	var releases atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		releases.Add(1)
		if unavailable.Load() {
			http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
			return
		}
		response.WriteJSON(w, response.OKEmpty())
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)
	if err := r.UpsertFederationTunnelBinding(&repo.FederationTunnelBinding{TunnelID: 43, NodeID: 1, ChainType: 3, RemoteURL: remote.URL, RemoteBindingID: "unrelated", ResourceKey: "unrelated", Status: 1}); err != nil {
		t.Fatal(err)
	}
	if err := h.cleanupFederationRuntime(42); err == nil {
		t.Fatal("expected release failure")
	}
	pending, err := r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 1 {
		t.Fatalf("lost pending cleanup: %v %v", pending, err)
	}
	if err := h.cleanupFederationRuntime(42); err == nil {
		t.Fatal("expected second release failure")
	}
	if releases.Load() != 2 {
		t.Fatalf("cleanup was not retried: %d", releases.Load())
	}
	// 断言更新登记 #2 附带（verdicts §2.5③）：重试环释放前复核隧道
	// 存续，隧道仍在时不得释放。本用例模拟的是删除流程的清理收敛，
	// 故在重试前补上删除流程的删库动作（级联保留 pending 绑定行）。
	if err := r.DeleteTunnelCascade(42); err != nil {
		t.Fatal(err)
	}
	unavailable.Store(false)
	if err := h.retryPendingFederationRuntimeCleanup(); err != nil {
		t.Fatal(err)
	}
	pending, err = r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 0 {
		t.Fatalf("completed cleanup remains: %v %v", pending, err)
	}
	active, err := r.ListActiveFederationTunnelBindingsByTunnel(43)
	if err != nil || len(active) != 1 || releases.Load() != 3 {
		t.Fatalf("retry touched active binding: %v %v calls=%d", active, err, releases.Load())
	}
}

// 断言更新登记 #2（R2 裁定驱动，verdicts §2.5① / BC-R2-01）：本用例
// 原名 TestTunnelDeleteReportsRemoteFailureAndKeepsTunnel，锁的是旧
// 中止口径「联邦清理失败 → 报错且不删隧道」。裁定①改为删除离线容忍
// （删库照常、绑定留 pending 由重试环收敛，与 delete-with-forwards
// 统一），断言按新口径改写：删除成功 + 隧道已删 + 绑定 pending 留存。
func TestTunnelDeleteToleratesRemoteFailureAndDeletesTunnel(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)
	rec := httptest.NewRecorder()
	h.tunnelDelete(rec, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/delete", strings.NewReader(`{"id":42}`)))
	var payload response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != 0 {
		t.Fatalf("delete must succeed despite peer cleanup failure, got code=%d msg=%q", payload.Code, payload.Msg)
	}
	if exists, err := r.TunnelExists(42); err != nil || exists {
		t.Fatalf("tunnel must be deleted: exists=%v err=%v", exists, err)
	}
	pending, err := r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 1 {
		t.Fatalf("binding must stay pending for the retry loop: %v %v", pending, err)
	}
}

func TestFederationRollbackReleaseIsDurableAndRetryable(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if unavailable.Load() {
			http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
			return
		}
		response.WriteJSON(w, response.OKEmpty())
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)
	refs := []federationRuntimeReleaseRef{{RemoteURL: remote.URL, RemoteToken: "token", BindingID: "new-binding", ReservationID: "new-reservation", ResourceKey: "new-key"}}
	if err := h.releaseFederationRuntimeRefs(refs); err == nil {
		t.Fatal("expected release error")
	}
	if err := h.releaseFederationRuntimeRefs(refs); err == nil {
		t.Fatal("expected repeat error")
	}
	pending, err := r.ListPendingFederationReleases()
	if err != nil || len(pending) != 1 {
		t.Fatalf("rollback queue lost or duplicated release: %v %v", pending, err)
	}
	unavailable.Store(false)
	// A new Handler has no in-memory state from the failed rollback.
	restarted := &Handler{repo: r}
	if err := restarted.retryPendingFederationRuntimeCleanup(); err != nil {
		t.Fatal(err)
	}
	pending, err = r.ListPendingFederationReleases()
	if err != nil || len(pending) != 0 {
		t.Fatalf("completed rollback remains queued: %v %v", pending, err)
	}
	active, err := r.ListActiveFederationTunnelBindingsByTunnel(42)
	if err != nil || len(active) != 1 {
		t.Fatalf("rollback retry removed active binding: %v %v", active, err)
	}
}

func TestFederationApplyFailureReturnsReservationForRollback(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "reserve-port") {
			response.WriteJSON(w, response.OK(map[string]interface{}{"reservationId": "reserved", "bindingId": "binding", "allocatedPort": 31001}))
			return
		}
		http.Error(w, "apply failed", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	_, h := consumerCleanupFixture(t, remote.URL)
	state := &tunnelCreateState{TunnelID: 43, Type: 2, OutNodes: []tunnelRuntimeNode{{NodeID: 1, Port: 31001}}, Nodes: map[int64]*nodeRecord{1: {ID: 1, Name: "remote", IsRemote: 1, RemoteURL: remote.URL, RemoteToken: "token"}}}
	_, refs, err := h.applyFederationRuntime(state, "")
	if err == nil || len(refs) != 1 || refs[0].ReservationID != "reserved" {
		t.Fatalf("failed apply lost reservation: refs=%+v err=%v", refs, err)
	}
}

func TestTunnelUpdateDatabaseValidationPreservesSharedRuntime(t *testing.T) {
	var releases atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		releases.Add(1)
		response.WriteJSON(w, response.OKEmpty())
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)
	if err := r.DB().Create(&model.Node{ID: 2, Name: "entry", Secret: "entry", ServerIP: "127.0.0.2", Port: "31000-31010", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER reject_tunnel_update BEFORE UPDATE ON tunnel BEGIN SELECT RAISE(ABORT, 'test constraint rejected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.tunnelUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", strings.NewReader(`{"id":42,"type":2,"name":"new name","inNodeId":[{"nodeId":2}],"outNodeId":[{"nodeId":1,"port":31001}]}`)))
	var payload response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	active, err := r.ListActiveFederationTunnelBindingsByTunnel(42)
	if err != nil || payload.Code == 0 || !strings.Contains(payload.Msg, "test constraint rejected") || releases.Load() != 0 || len(active) != 1 {
		t.Fatalf("DB validation touched runtime: payload=%+v bindings=%v calls=%d err=%v", payload, active, releases.Load(), err)
	}
}

func TestTunnelUpdateRemoteReleaseFailurePreservesMetadata(t *testing.T) {
	var requests atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)
	if err := r.DB().Create(&model.Node{ID: 2, Name: "entry", Secret: "entry", ServerIP: "127.0.0.2", Port: "31000-31010", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.tunnelUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", strings.NewReader(`{"id":42,"type":2,"name":"new name","inNodeId":[{"nodeId":2}],"outNodeId":[{"nodeId":1,"port":31001}]}`)))
	var payload response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	name, err := r.GetTunnelName(42)
	pending, pendingErr := r.ListPendingFederationTunnelBindings()
	active, activeErr := r.ListActiveFederationTunnelBindingsByTunnel(42)
	// 断言更新登记 #2 附带（verdicts §2.5②）：旧断言要求失败编辑后绑定
	// 留 pending(==1)；裁定②要求中止时把本轮新置 pending 回滚为
	// status=1，故改断言为 pending==0 且 active==1（元数据保留与中止
	// 口径不变）。组合证据见 r2_b9_federation_test.go 的
	// TestR2B9TunnelUpdateFederationOfflineRollbackCombination。
	if err != nil || pendingErr != nil || activeErr != nil || payload.Code == 0 || name != "live tunnel" || requests.Load() != 1 || len(pending) != 0 || len(active) != 1 {
		t.Fatalf("failed cleanup continued update: payload=%+v name=%s calls=%d pending=%v active=%v err=%v/%v/%v", payload, name, requests.Load(), pending, active, err, pendingErr, activeErr)
	}
}

func TestTunnelUpdateChainWriteValidationPreservesSharedRuntime(t *testing.T) {
	var calls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		response.WriteJSON(w, response.OKEmpty())
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)
	if err := r.DB().Create(&model.Node{ID: 2, Name: "entry", Secret: "entry", ServerIP: "127.0.0.2", Port: "31000-31010", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER reject_chain_insert BEFORE INSERT ON chain_tunnel BEGIN SELECT RAISE(ABORT, 'chain write rejected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.tunnelUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", strings.NewReader(`{"id":42,"type":2,"name":"new name","inNodeId":[{"nodeId":2}],"outNodeId":[{"nodeId":1,"port":31001}]}`)))
	var payload response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	active, err := r.ListActiveFederationTunnelBindingsByTunnel(42)
	if err != nil || payload.Code == 0 || !strings.Contains(payload.Msg, "chain write rejected") || calls.Load() != 0 || len(active) != 1 {
		t.Fatalf("chain validation touched runtime: payload=%+v active=%v calls=%d err=%v", payload, active, calls.Load(), err)
	}
	if name, err := r.GetTunnelName(42); err != nil || name != "live tunnel" {
		t.Fatalf("preflight metadata update was committed: %s %v", name, err)
	}
}
