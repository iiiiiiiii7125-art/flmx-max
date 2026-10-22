package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
)

// X-P2-8（R1 [I]-02）：两类通知发送失败后永久丢失：
//   - kernel_error：裸 notify + 内核状态无条件落 error，下一轮
//     KernelStatus 因「已是 error」不再触发；
//   - route_change：失败后仍覆盖比对基准，下一轮 routeChanges 无变化。
// 修复口径（与 A7-03 notifyTracked 一致）：发送 Failed 时回滚置位——
// kernel_error 回滚 DB 中的 error 状态、route_change 恢复旧比对基准，
// 通道恢复后下一轮应补发且只补发一次。两事件各一用例。

// ---- kernel_error ----

type r1KernelMock struct {
	mu          sync.Mutex
	statusCalls int
}

func (m *r1KernelMock) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusCalls
}

// startR1KernelMockNode connects a fake agent that always reports its
// sing-box kernel in error state (configHash=mockhash).
func startR1KernelMockNode(t *testing.T, baseURL, secret string, mock *r1KernelMock) func() {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/system-info"
	q := u.Query()
	q.Set("type", "1")
	q.Set("secret", secret)
	q.Set("version", "v1")
	q.Set("http", "1")
	q.Set("tls", "1")
	q.Set("socks", "1")
	u.RawQuery = q.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial mock node: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
			plain := raw
			var wrap struct {
				Encrypted bool   `json:"encrypted"`
				Data      string `json:"data"`
			}
			if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Encrypted && strings.TrimSpace(wrap.Data) != "" {
				if crypto, cryptoErr := security.NewAESCrypto(secret); cryptoErr == nil {
					if dec, decErr := crypto.Decrypt(wrap.Data); decErr == nil {
						plain = []byte(dec)
					}
				}
			}
			var cmd struct {
				Type      string          `json:"type"`
				RequestID string          `json:"requestId"`
				Data      json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil || strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			resp := map[string]interface{}{
				"type":      fmt.Sprintf("%sResponse", cmd.Type),
				"success":   true,
				"message":   "OK",
				"requestId": cmd.RequestID,
			}
			if cmd.Type == "KernelStatus" {
				mock.mu.Lock()
				mock.statusCalls++
				mock.mu.Unlock()
				resp["data"] = map[string]interface{}{
					"kernels": []map[string]interface{}{
						{"type": "sing-box", "running": false, "enabled": true, "version": "1.0", "configHash": "mockhash", "error": "boom"},
					},
					"nftAvailable": false,
					"epoch":        "e1",
				}
			}
			respBytes, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = conn.Close()
			wg.Wait()
		})
	}
}

func TestR1KernelErrorNotifyFailureResendsNextRound(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	nodeID := mustCreateLatencyNode(t, r, "r1-kerr-node", 1, 0)

	mock := &r1KernelMock{}
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	stop := startR1KernelMockNode(t, srv.URL, "r1-kerr-node", mock)
	defer stop()
	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")
	// 等上线对账的那一轮 KernelStatus 完成（此时尚无内核行，只做孤儿清理），
	// 之后才建内核行，确保后续轮询的触发时机完全由本用例掌控。
	waitForCondition(t, 5*time.Second, func() bool { return mock.calls() >= 1 }, "online reconcile kernel status poll")
	time.Sleep(300 * time.Millisecond)

	now := time.Now().UnixMilli()
	k := &model.NodeKernel{
		NodeID: nodeID, KernelType: "sing-box", Version: "latest",
		InstalledVersion: "1.0", Status: kernelStatusRunning,
		ConfigHash: "mockhash", AppliedHash: "mockhash", Enabled: 1,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatalf("create kernel: %v", err)
	}
	readStatus := func() string {
		cur, err := r.GetNodeKernel(k.ID)
		if err != nil || cur == nil {
			return ""
		}
		return cur.Status
	}

	// 通道全失败下触发内核 error：发送 Failed 后 error 状态必须回滚，
	// 否则下一轮不再触发（修复前状态恒为 error，本断言超时 FAIL）。
	wh.fail.Store(true)
	h.refreshNodeKernelStatus(nodeID)
	if got := readStatus(); got != kernelStatusError {
		t.Fatalf("after poll the kernel status should be optimistically error, got %q", got)
	}
	waitForCondition(t, 12*time.Second, func() bool { return readStatus() == kernelStatusRunning },
		"kernel error status rolled back after failed notify")
	if got := wh.count(); got != 0 {
		t.Fatalf("failing webhook must not record deliveries, got %d", got)
	}

	// 通道恢复后下一轮 KernelStatus 应补发一次，状态落 error 后不再重复。
	wh.fail.Store(false)
	h.refreshNodeKernelStatus(nodeID)
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); len(titles) != 1 || titles[0] != "内核运行出错" {
		t.Fatalf("resent notification titles = %v, want [内核运行出错]", titles)
	}
	if got := readStatus(); got != kernelStatusError {
		t.Fatalf("after successful resend the kernel status must be error, got %q", got)
	}
	h.refreshNodeKernelStatus(nodeID)
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("kernel error must be resent exactly once, got %d (%v)", got, wh.titles())
	}
}

// ---- route_change ----

func TestR1RouteChangeNotifyFailureResendsNextRound(t *testing.T) {
	wh := newFixdWebhook(t)
	h, r := setupFixdNotifyHandler(t, wh)
	if err := r.UpsertConfig(latencyTargetsConfigKey,
		`[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	nodeID := mustCreateLatencyNode(t, r, "r1-route-node", 1, 0)

	// 旧比对基准：电信走 163。
	h.nodeRoutes.set(nodeID, nodeRoute{
		CheckedAt: time.Now().UnixMilli(),
		Points:    []routePoint{{Name: "电信", Route: "电信 163", host: "ct.example.com"}},
	})

	old := nodeTraceroute
	t.Cleanup(func() { nodeTraceroute = old })
	nodeTraceroute = func(_ *Handler, _ int64, _ string, family int) (map[string]interface{}, error) {
		if family == 6 {
			return nil, errors.New("no ipv6")
		}
		// 新一轮识别为 CN2 GIA，与旧基准构成一次回程变化。
		return map[string]interface{}{
			"reached": true,
			"hops": []interface{}{
				map[string]interface{}{"ttl": 1.0, "ip": "59.43.180.1"},
			},
		}, nil
	}
	storedRoute := func() string {
		stored, ok := h.nodeRoutes.get(nodeID)
		if !ok || len(stored.Points) != 1 {
			return ""
		}
		return stored.Points[0].Route
	}

	// 通道全失败下触发回程变化：发送 Failed 后比对基准必须保留旧值，
	// 否则下一轮无变化可比（修复前基准被新值覆盖，本断言超时 FAIL）。
	wh.fail.Store(true)
	if _, err := h.traceNodeRoutes(context.Background(), []int64{nodeID}, true); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 12*time.Second, func() bool { return storedRoute() == "电信 163" },
		"route comparison baseline restored after failed notify")
	if got := wh.count(); got != 0 {
		t.Fatalf("failing webhook must not record deliveries, got %d", got)
	}

	// 通道恢复后下一轮应补发一次；补发成功后基准推进到新线路、不再重复。
	wh.fail.Store(false)
	if _, err := h.traceNodeRoutes(context.Background(), []int64{nodeID}, true); err != nil {
		t.Fatal(err)
	}
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); len(titles) != 1 || titles[0] != "回程线路变化" {
		t.Fatalf("resent notification titles = %v, want [回程线路变化]", titles)
	}
	time.Sleep(300 * time.Millisecond)
	if got := storedRoute(); got != "电信 CN2 GIA" {
		t.Fatalf("after successful resend the baseline must advance, got %q", got)
	}
	if _, err := h.traceNodeRoutes(context.Background(), []int64{nodeID}, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("route change must be resent exactly once, got %d (%v)", got, wh.titles())
	}
}
