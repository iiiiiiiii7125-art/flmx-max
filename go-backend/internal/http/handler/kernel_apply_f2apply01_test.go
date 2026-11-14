package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// F2-APPLY-01：KernelApply 回执超时不等于下发失败。假 agent 从不回
// KernelApply（回执在 20s 预算耗尽后超时），只回 KernelStatus：
//   - 回报的 configHash 与下发 hash 一致 → 下发已落地，applyKernel
//     必须返回 nil（不是失败），状态落 running；
//   - 回报的 configHash 不符 → 落地未证实，applyKernel 必须返回
//     errApplyConfirmPending，状态落 pending（不是 error）。
//
// 回执 hash 取自 fake agent 对 KernelStatus 的回答；landed 格里 fake
// agent 回读面板刚写下的 config_hash（模拟 agent 如实上报自己已
// 应用的配置），测的是面板「超时→核实→定性」的决策，不是 agent
// 的哈希算法。
type f2apply01FakeAgent struct {
	mu         sync.Mutex
	applySeen  int
	statusHash func() string
}

func newF2APPLY01Handler(t *testing.T, statusHash func() string) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/f2apply01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const secret = "f2apply01-node-secret"
	nodeID := seedRelayTargetNode(t, r, "f2apply01-node", "127.0.0.1")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.3")
	// InstalledVersion 非空且含版本号 → apply 走 20s 读预算而非 5 分钟
	// 安装预算（测试只等一轮回执超时）。
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"installed_version": "sing-box 1.14.3",
	}); err != nil {
		t.Fatal(err)
	}

	server := ws.NewServer(r, "test-jwt-secret")
	online := make(chan struct{})
	server.SetNodeOnlineHook(func(int64) { close(online) })
	serverDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(serverDone)
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	crypto, err := security.NewAESCrypto(secret)
	if err != nil {
		t.Fatal(err)
	}
	a := &f2apply01FakeAgent{statusHash: statusHash}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var envelope struct {
				Encrypted bool   `json:"encrypted"`
				Data      string `json:"data"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil {
				t.Errorf("decode command envelope: %v", err)
				return
			}
			if envelope.Encrypted {
				payload, err = crypto.Decrypt(envelope.Data)
				if err != nil {
					t.Errorf("decrypt command: %v", err)
					return
				}
			}
			var command cleanupAgentCommand
			if err := json.Unmarshal(payload, &command); err != nil {
				t.Errorf("decode command: %v", err)
				return
			}
			a.mu.Lock()
			isApply := command.Type == "KernelApply"
			if isApply {
				a.applySeen++
			}
			a.mu.Unlock()
			if isApply {
				// 故意不回：回执超时是本测试要复现的慢链路。
				continue
			}
			resp := map[string]interface{}{
				"type":      command.Type,
				"requestId": command.RequestID,
				"success":   true,
				"message":   "",
			}
			if command.Type == "KernelStatus" {
				hash := ""
				if a.statusHash != nil {
					hash = a.statusHash()
				}
				resp["data"] = map[string]interface{}{
					"kernels": []map[string]interface{}{{
						"type": kernel.TypeSingbox, "running": true, "enabled": true,
						"configHash": hash, "version": "sing-box 1.14.3",
					}},
				}
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-readerDone
		<-serverDone
	})
	select {
	case <-online:
	case <-time.After(3 * time.Second):
		t.Fatal("fake agent did not come online")
	}
	h := New(r, "unit-test-secret")
	// R5-B-03：测试结束停掉可能被懒启动的 notify 派发 worker。
	t.Cleanup(func() { h.StopBackgroundJobs() })
	h.wsServer = server
	return h, r, k.ID
}

func TestF2APPLY01SlowAckLandedIsNotFailure(t *testing.T) {
	var r *repo.Repository
	var kernelID int64
	var h *Handler
	// statusHash 在 fake agent 回 KernelStatus 时读面板刚写下的
	// config_hash：模拟 agent 如实上报已应用的配置。
	h, r, kernelID = newF2APPLY01Handler(t, func() string {
		cur, err := r.GetNodeKernel(kernelID)
		if err != nil || cur == nil {
			return ""
		}
		return cur.ConfigHash
	})
	start := time.Now()
	if err := h.applyKernel(kernelID, false); err != nil {
		t.Fatalf("landed apply must not report failure, got %v (took %v)", err, time.Since(start))
	}
	after, err := r.GetNodeKernel(kernelID)
	if err != nil || after == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	if after.Status != kernelStatusRunning {
		t.Fatalf("landed kernel status = %q, want %q", after.Status, kernelStatusRunning)
	}
	if after.AppliedHash == "" || after.AppliedHash != after.ConfigHash {
		t.Fatalf("landed kernel applied_hash = %q, config_hash = %q", after.AppliedHash, after.ConfigHash)
	}
}

func TestF2APPLY01UnconfirmedApplyIsPendingNotError(t *testing.T) {
	h, r, kernelID := newF2APPLY01Handler(t, func() string { return "not-the-dispatched-hash" })
	if err := h.applyKernel(kernelID, false); !errors.Is(err, errApplyConfirmPending) {
		t.Fatalf("unconfirmed apply must return errApplyConfirmPending, got %v", err)
	}
	after, err := r.GetNodeKernel(kernelID)
	if err != nil || after == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	if after.Status != kernelStatusPending {
		t.Fatalf("unconfirmed kernel status = %q, want %q (not error)", after.Status, kernelStatusPending)
	}
	if after.LastError != errApplyConfirmPending.Error() {
		t.Fatalf("unconfirmed last_error = %q, want %q", after.LastError, errApplyConfirmPending.Error())
	}
}

func TestIsKernelApplyTimeoutError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("等待节点响应超时"), true},
		{errors.New("节点不在线"), false},
		{errors.New("命令执行失败"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isKernelApplyTimeoutError(tc.err); got != tc.want {
			t.Fatalf("isKernelApplyTimeoutError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
