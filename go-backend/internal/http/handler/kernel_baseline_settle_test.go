package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// FIX-BASELINE（R2-E2-01）：入站内核（xray/singbox）与订阅用户计数
// 的首采集窗口回归测试。
//
// 机制（与 realm 本体同形，FIX-REALM-METER 报告 §5 #3 点名立案）：
// 入账按累计计数差分、首个样本只建基线不入账；计数自 KernelApply
// 时刻起算，但独立下发路径（applyKernelWith）此前只对 realm 主动
// 拉计数面钉基线，入站内核只能等 60s 周期轮询的首个样本——新建
// 入站/新挂载订阅在下发后、首轮询前产生的全部流量被首样本基线
// 整段吞掉、永不入账（入站总量与订阅计量/配额两面）。
//
// 本测试按真机时序复现：节点先在线（重连对账轮已跑完）→ 其后新
// 建入站 + 新建分享订阅 → 独立下发该内核（计数为 0）→ 立即产生
// 流量 → 周期轮询到达——流量必须全额入账，且下发返回时基线必须
// 已钉在下发时刻（KernelStatus 次数断言 + 基线行断言）。

type inboundMeterMock struct {
	mu          sync.Mutex
	applied     bool
	appliedHash string
	applyCalls  int
	statusCalls int
	inboundID   int64
	userID      int64
	tcpIn       int64
	tcpOut      int64
	userUp      int64
	userDown    int64
}

func (m *inboundMeterMock) setTraffic(tcpIn, tcpOut, userUp, userDown int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tcpIn, m.tcpOut, m.userUp, m.userDown = tcpIn, tcpOut, userUp, userDown
}

func (m *inboundMeterMock) calls() (apply, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyCalls, m.statusCalls
}

func startInboundMeterMockNode(t *testing.T, baseURL, secret string, mock *inboundMeterMock) func() {
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
			switch cmd.Type {
			case "KernelApply":
				var body struct {
					Hash string `json:"hash"`
				}
				_ = json.Unmarshal(cmd.Data, &body)
				mock.mu.Lock()
				mock.applied = true
				mock.appliedHash = body.Hash
				mock.applyCalls++
				hash := mock.appliedHash
				mock.mu.Unlock()
				resp["data"] = map[string]interface{}{
					"type": "xray", "running": true, "enabled": true, "pid": 4242, "startedAt": 1,
					"version": "v25.1.1", "configHash": hash,
				}
			case "KernelStatus":
				mock.mu.Lock()
				mock.statusCalls++
				applied, hash := mock.applied, mock.appliedHash
				inboundID, userID := mock.inboundID, mock.userID
				tcpIn, tcpOut, userUp, userDown := mock.tcpIn, mock.tcpOut, mock.userUp, mock.userDown
				mock.mu.Unlock()
				data := map[string]interface{}{
					"kernels":      []map[string]interface{}{},
					"nftAvailable": true,
					"epoch":        "e1",
					"counters":     []map[string]interface{}{},
					"users":        []map[string]interface{}{},
				}
				if applied {
					data["kernels"] = []map[string]interface{}{
						{"type": "xray", "running": true, "enabled": true, "pid": 4242, "startedAt": 1,
							"version": "v25.1.1", "configHash": hash},
					}
					if inboundID > 0 {
						data["counters"] = []map[string]interface{}{
							{"inboundId": inboundID, "proto": "tcp", "dir": "in", "bytes": tcpIn},
							{"inboundId": inboundID, "proto": "tcp", "dir": "out", "bytes": tcpOut},
						}
					}
					if userID > 0 {
						data["users"] = []map[string]interface{}{
							{"id": userID, "dir": "up", "bytes": userUp},
							{"id": userID, "dir": "down", "bytes": userDown},
						}
					}
				}
				resp["data"] = data
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

func TestInboundKernelTrafficAfterApplyIsCredited(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/inbound-meter.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.31")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	// 具体版本避开 apply 路径的 "latest" 解析（测试环境无外网）。
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"version": "v25.1.1"}); err != nil {
		t.Fatal(err)
	}
	// 先于节点在线存在的入站：重连对账轮会下发它，其基线由轮尾
	// poll 钉定（R4X-009 轮次口径），不是本测试的被测面。
	seedChainInbound(t, r, k, "pre-existing", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)

	mock := &inboundMeterMock{}
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	stop := startInboundMeterMockNode(t, srv.URL, "entry-secret", mock)
	defer stop()
	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")
	// 等重连对账轮下发完成且命令计数稳定（轮内不逐个 settle，基线
	// 由轮尾 poll 钉；与 R4B5 的 settleR4B5Baseline 同口径，先等静
	// 默再取基线计数，避免轮尾 poll 与下方快照竞态）。
	{
		lastApply, lastStatus, stable := -1, -1, 0
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			a, s := mock.calls()
			if a == lastApply && s == lastStatus && a >= 1 {
				stable++
				if stable >= 3 {
					break
				}
			} else {
				stable = 0
				lastApply, lastStatus = a, s
			}
			time.Sleep(100 * time.Millisecond)
		}
		if lastApply < 1 {
			t.Fatal("reconnect round never applied the kernel")
		}
	}

	// 被测时序：节点已在线，其后新建入站 + 新建分享订阅，再走独立
	// 下发路径（入站创建/更新走的同一入口）下发该内核。此刻新入站
	// 与新订阅在面板侧均无计数基线行。
	inbound2 := seedChainInbound(t, r, k, "late-inbound", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 9443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	sub := &model.Subscription{Name: "late-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "u-late", Password: "late-pw", Enabled: 1, SourceAll: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE subscription SET applied_active = 1 WHERE id = ?`, sub.ID).Error; err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	mock.inboundID = inbound2.ID
	mock.userID = sub.ID
	mock.mu.Unlock()

	if n := mustHandlerCount(t, h, `SELECT COUNT(*) FROM node_inbound_counter WHERE inbound_id = ?`, inbound2.ID); n != 0 {
		t.Fatalf("no inbound baseline may exist before the standalone apply, got %d rows", n)
	}
	if n := mustHandlerCount(t, h, `SELECT COUNT(*) FROM subscription_counter WHERE subscription_id = ?`, sub.ID); n != 0 {
		t.Fatalf("no subscription baseline may exist before the standalone apply, got %d rows", n)
	}

	_, statusBefore := mock.calls()
	if err := h.applyKernel(k.ID, false); err != nil {
		t.Fatalf("standalone apply: %v", err)
	}
	// 修复点：下发返回时必须已主动拉一次计数面把新入站/新订阅的
	// 基线钉在下发时刻；否则下发后、首个周期轮询（60s）前产生的
	// 流量被首样本基线整段吞掉。
	_, statusAfter := mock.calls()
	if statusAfter != statusBefore+1 {
		t.Fatalf("FIX-BASELINE: standalone apply must settle counters exactly once (KernelStatus %d -> %d)", statusBefore, statusAfter)
	}
	if n := mustHandlerCount(t, h, `SELECT COUNT(*) FROM node_inbound_counter WHERE inbound_id = ? AND bytes = 0`, inbound2.ID); n != 2 {
		t.Fatalf("inbound baseline after apply: want 2 rows pinned at 0 (tcp in/out), got %d", n)
	}
	if n := mustHandlerCount(t, h, `SELECT COUNT(*) FROM subscription_counter WHERE subscription_id = ? AND bytes = 0`, sub.ID); n != 2 {
		t.Fatalf("subscription baseline after apply: want 2 rows pinned at 0 (up/down), got %d", n)
	}

	// 下发后立即产生流量（真机上即创建后数秒内的首批业务流量）。
	mock.setTraffic(2000, 5000, 700, 1300)

	// 周期轮询到达：下发后流量必须按差分全额入账（入站总量与订
	// 阅累计两面）。
	h.refreshNodeKernelStatus(nodeID)
	row, err := r.GetNodeInbound(inbound2.ID)
	if err != nil || row == nil {
		t.Fatalf("load inbound2: %v", err)
	}
	if row.UploadBytes != 2000 || row.DownloadBytes != 5000 {
		t.Fatalf("inbound totals after first poll = %d/%d, want 2000/5000 (post-apply traffic credited)", row.UploadBytes, row.DownloadBytes)
	}
	subRow, err := r.GetSubscription(sub.ID)
	if err != nil || subRow == nil {
		t.Fatalf("load subscription: %v", err)
	}
	if subRow.UploadBytes != 700 || subRow.DownloadBytes != 1300 {
		t.Fatalf("subscription totals after first poll = %d/%d, want 700/1300 (post-apply traffic credited)", subRow.UploadBytes, subRow.DownloadBytes)
	}

	// 再轮询一轮（计数不变）：不得重复入账。
	h.refreshNodeKernelStatus(nodeID)
	row, _ = r.GetNodeInbound(inbound2.ID)
	if row.UploadBytes != 2000 || row.DownloadBytes != 5000 {
		t.Fatalf("inbound totals after second poll = %d/%d, want unchanged 2000/5000", row.UploadBytes, row.DownloadBytes)
	}
	subRow, _ = r.GetSubscription(sub.ID)
	if subRow.UploadBytes != 700 || subRow.DownloadBytes != 1300 {
		t.Fatalf("subscription totals after second poll = %d/%d, want unchanged 700/1300", subRow.UploadBytes, subRow.DownloadBytes)
	}
}
