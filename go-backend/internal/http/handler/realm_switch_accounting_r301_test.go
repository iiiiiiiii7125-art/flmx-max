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
	"go-backend/internal/store/repo"
)

// FIX-B R3-01：引擎切换路径的 realm 计费基线/结算。
// 真机证据（R3 T10）：gost→realm 后 realm 转发计数 ≥8 分钟不动、其后仅
// 约一次下载迟到入账；原生 realm 隧道正常。切换路径的问题：switchTunnelEngine
// 从不主动拉一次 KernelStatus——新引擎计数的基线/结算只能等 60s 轮询，
// 且 realm→gost 拆除后 agent nft 表重建、端口计数消失，拆除前未结算的
// 增量永久漏计（A4-010 的 realm 侧窗口）；gost→realm 同理，切换后首个
// 轮询前产生的流量要么被首采样基线吞掉、要么随 agent 计数代际变化整段
// 丢失。修复：gost→realm 同步完成后立即轮询一次（基线钉在切换点、切换
// 前后增量即刻入账）；realm→gost 在拆除前先轮询一次结算最终计数。
// 本测试以 mock 节点驱动 switchTunnelEngine，断言两侧切换返回时计数
// 已结算到最新值（基线状态与 forward 流量均不滞后于切换点）。

type switchAcctMock struct {
	mu       sync.Mutex
	inBytes  int64
	outBytes int64
	statusN  int
}

func (m *switchAcctMock) setCounters(in, out int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inBytes, m.outBytes = in, out
}

func (m *switchAcctMock) statusCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusN
}

func startSwitchAcctMockNode(t *testing.T, baseURL, secret string, counterID int64, mock *switchAcctMock) func() {
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
			case "KernelStatus":
				mock.mu.Lock()
				mock.statusN++
				inB, outB := mock.inBytes, mock.outBytes
				mock.mu.Unlock()
				resp["data"] = map[string]interface{}{
					"kernels": []map[string]interface{}{
						{"type": "realm", "running": true, "enabled": true, "version": "1.0", "configHash": "mock"},
					},
					"nftAvailable": true,
					"epoch":        "e1",
					"counters": []map[string]interface{}{
						{"inboundId": counterID, "proto": "tcp", "dir": "in", "bytes": inB},
						{"inboundId": counterID, "proto": "tcp", "dir": "out", "bytes": outB},
					},
				}
			case "KernelApply":
				resp["data"] = map[string]interface{}{
					"type": "realm", "running": true, "enabled": true, "version": "1.0", "configHash": "mock",
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

func TestEngineSwitchRealmAccountingSettlesAtSwitch(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-switch-acct.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.15")
	tunnelID := seedTunnelForNftables(t, h, "switch-acct-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 30005)

	readFlow := func() (int64, int64) {
		var flow struct{ InFlow, OutFlow int64 }
		if err := r.DB().Raw(`SELECT in_flow, out_flow FROM forward WHERE id = ?`, forward.ID).Scan(&flow).Error; err != nil {
			t.Fatal(err)
		}
		return flow.InFlow, flow.OutFlow
	}
	readBaseline := func() (int64, int64, bool) {
		states, err := h.repo.GetNftCounterStatesByNode(nodeID)
		if err != nil {
			t.Fatalf("load states: %v", err)
		}
		var inB, outB int64
		found := false
		for _, st := range states {
			if st.ForwardID != forward.ID || st.RuleHash != "realm:e1" {
				continue
			}
			found = true
			if st.Direction == "to-target" {
				inB = st.Bytes
			}
			if st.Direction == "from-target" {
				outB = st.Bytes
			}
		}
		return inB, outB, found
	}

	mock := &switchAcctMock{}
	mock.setCounters(1000, 5000)
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	stop := startSwitchAcctMockNode(t, srv.URL, "entry-secret", kernel.RealmCounterID(forward.ID), mock)
	defer stop()
	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")
	// 节点上线对账可能已拉过一次状态并建基线；记录切换前的基线/流量
	// 作为期望推导的起点（两种接线形态都成立，断言只针对切换本身）。
	preIn, preOut, preBaseFound := readBaseline()
	preFlowIn, preFlowOut := readFlow()
	if preBaseFound && (preIn != 1000 || preOut != 5000) {
		t.Fatalf("pre-switch baseline = %d/%d, want 1000/5000", preIn, preOut)
	}

	// 切换窗口内计数继续增长；切到 realm 返回时，计数必须已结算到
	// 最新值：基线推进到 1600/5600，且基线之后产生的增量已入账。
	mock.setCounters(1600, 5600)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.switchTunnelEngine(tunnelID, false, []int64{nodeID}, []int64{nodeID}); err != nil {
		t.Fatalf("switch to realm: %v", err)
	}
	baseIn, baseOut, found := readBaseline()
	if !found || baseIn != 1600 || baseOut != 5600 {
		t.Fatalf("R3-01: baseline after gost->realm switch = %d/%d (found=%v), want 1600/5600 pinned at switch", baseIn, baseOut, found)
	}
	wantFlowIn, wantFlowOut := preFlowIn, preFlowOut
	if preBaseFound {
		// 基线在切换前已存在：切换窗口增量 600/600 应在切换结算时入账。
		wantFlowIn += 600
		wantFlowOut += 600
	}
	if inF, outF := readFlow(); inF != wantFlowIn || outF != wantFlowOut {
		t.Fatalf("R3-01: flow after switch = %d/%d, want %d/%d", inF, outF, wantFlowIn, wantFlowOut)
	}

	// realm 下继续产生流量；切回 gost 返回前必须先结算最终计数
	//（拆除后计数器消失，无第二次机会）。
	mock.setCounters(1900, 8100)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = '' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.switchTunnelEngine(tunnelID, true, []int64{nodeID}, []int64{nodeID}); err != nil {
		t.Fatalf("switch back to gost: %v", err)
	}
	if inF, outF := readFlow(); inF != wantFlowIn+300 || outF != wantFlowOut+2500 {
		t.Fatalf("R3-01: final realm counters not settled before teardown: flow = %d/%d, want %d/%d",
			inF, outF, wantFlowIn+300, wantFlowOut+2500)
	}
	if baseIn, baseOut, found := readBaseline(); !found || baseIn != 1900 || baseOut != 8100 {
		t.Fatalf("R3-01: baseline after realm->gost switch = %d/%d (found=%v), want 1900/8100", baseIn, baseOut, found)
	}
}
