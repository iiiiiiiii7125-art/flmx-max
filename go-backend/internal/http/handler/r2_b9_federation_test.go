package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
)

// R2 批 9 · R2X-010 联邦离线三件套组合用例（本条首次动态证据）：
// ① 删除类对端 503 不中止删库、绑定留 pending、恢复后重试环释放；
// ② 非删除类（编辑）503 中止后绑定回 status=1、恢复后重试环不释放；
// ③ 重试环释放前复核隧道存续，隧道仍在则跳过。

func TestR2B9TunnelDeleteFederationOfflineCombination(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	var releases atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "release-role") {
			releases.Add(1)
		}
		if unavailable.Load() {
			http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
			return
		}
		response.WriteJSON(w, response.OKEmpty())
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
		t.Fatalf("R2X-010①: federation-offline delete must succeed, got code=%d msg=%q", payload.Code, payload.Msg)
	}
	exists, err := r.TunnelExists(42)
	if err != nil || exists {
		t.Fatalf("R2X-010①: tunnel must be deleted despite peer 503, exists=%v err=%v", exists, err)
	}
	pending, err := r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 1 {
		t.Fatalf("R2X-010①: binding must stay pending for the retry loop, pending=%v err=%v", pending, err)
	}

	unavailable.Store(false)
	if err := h.retryPendingFederationRuntimeCleanup(); err != nil {
		t.Fatal(err)
	}
	if releases.Load() == 0 {
		t.Fatalf("R2X-010①: retry loop must release the role after the peer recovers")
	}
	pending, err = r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 0 {
		t.Fatalf("R2X-010①: binding row must be cleared after release, pending=%v err=%v", pending, err)
	}
	remaining, err := r.ListFederationTunnelBindingsForCleanup(42)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("R2X-010①: no binding rows may remain, got %v err=%v", remaining, err)
	}
}

func TestR2B9TunnelBatchDeleteFederationOfflineCombination(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)

	rec := httptest.NewRecorder()
	h.tunnelBatchDelete(rec, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/batch-delete", strings.NewReader(`{"ids":[42]}`)))
	var payload struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != 0 || payload.Data.SuccessCount != 1 || payload.Data.FailCount != 0 {
		t.Fatalf("R2X-010①: batch delete must succeed despite peer 503, got %+v body=%s", payload, rec.Body.String())
	}
	exists, err := r.TunnelExists(42)
	if err != nil || exists {
		t.Fatalf("R2X-010①: tunnel must be deleted, exists=%v err=%v", exists, err)
	}
	pending, err := r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 1 {
		t.Fatalf("R2X-010①: binding must stay pending, pending=%v err=%v", pending, err)
	}
}

func TestR2B9TunnelUpdateFederationOfflineRollbackCombination(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	var releases atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "release-role") {
			releases.Add(1)
		}
		if unavailable.Load() {
			http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
			return
		}
		response.WriteJSON(w, response.OKEmpty())
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
	if payload.Code == 0 {
		t.Fatalf("R2X-010②: update must abort when federation cleanup fails, got success body=%s", rec.Body.String())
	}
	if name, err := r.GetTunnelName(42); err != nil || name != "live tunnel" {
		t.Fatalf("R2X-010②: tunnel metadata must be preserved, name=%q err=%v", name, err)
	}
	active, err := r.ListActiveFederationTunnelBindingsByTunnel(42)
	if err != nil || len(active) != 1 {
		t.Fatalf("R2X-010②: binding must be rolled back to status=1, active=%v err=%v", active, err)
	}
	pending, err := r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 0 {
		t.Fatalf("R2X-010②: no binding may stay pending after an aborted update, pending=%v err=%v", pending, err)
	}
	before := releases.Load()
	unavailable.Store(false)
	if err := h.retryPendingFederationRuntimeCleanup(); err != nil {
		t.Fatal(err)
	}
	if releases.Load() != before {
		t.Fatalf("R2X-010②: retry loop must not release a live tunnel's binding after recovery")
	}
	active, err = r.ListActiveFederationTunnelBindingsByTunnel(42)
	if err != nil || len(active) != 1 {
		t.Fatalf("R2X-010②: binding must remain active, active=%v err=%v", active, err)
	}
	exists, err := r.TunnelExists(42)
	if err != nil || !exists {
		t.Fatalf("R2X-010②: tunnel must remain usable, exists=%v err=%v", exists, err)
	}
}

// ③ 纵深防线：绑定被置 pending 但其隧道仍存在（例如旧版本失败删除尝试
// 的遗留），重试环不得释放它。
func TestR2B9RetrySkipsPendingBindingWhoseTunnelStillExists(t *testing.T) {
	var releases atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "release-role") {
			releases.Add(1)
		}
		response.WriteJSON(w, response.OKEmpty())
	}))
	defer remote.Close()
	r, h := consumerCleanupFixture(t, remote.URL)
	bindings, err := r.ListFederationTunnelBindingsForCleanup(42)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("fixture bindings: %v err=%v", bindings, err)
	}
	if err := r.MarkFederationTunnelBindingPendingRelease(bindings[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := h.retryPendingFederationRuntimeCleanup(); err != nil {
		t.Fatal(err)
	}
	if releases.Load() != 0 {
		t.Fatalf("R2X-010③: retry loop must not release a binding whose tunnel still exists")
	}
	pending, err := r.ListPendingFederationTunnelBindings()
	if err != nil || len(pending) != 1 {
		t.Fatalf("R2X-010③: binding row must be retained for a live tunnel, pending=%v err=%v", pending, err)
	}
}
