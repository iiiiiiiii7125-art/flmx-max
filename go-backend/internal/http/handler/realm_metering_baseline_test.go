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

// FIX-REALM-METER：原生 realm 转发流量不入账的复现/回归测试。
//
// 真机证据（EXEC-RERUN3）：新建 realm 隧道+转发后立即发一次回声流量，
// 60s/180s 窗内 forward 流量恒 0→0；同轮 gost 转发同驱动 0→54 正常
// 入账。定源：realm 流量由 agent 侧 nft 计数（与入站内核共用），面板
// 以累计计数差分入账——首个样本只建基线不入账（buildNftCounterDeltas
// 与入站 applyInboundCounterChunk 同语义）。计数自 KernelApply 时刻
// 起算，但普通下发路径（applyKernelWith）从不主动轮询建基线，基线
// 只能等 60s 周期轮询的第一个样本：下发后、首轮询前产生的全部流量
// 被基线整段吞掉、永不入账。R3-01 曾在引擎切换路径上实证并修复同
// 一机制（切换点 settle），但创建/更新/暂停恢复/重连对账等普通下发
// 路径漏修。本测试按真机时序复现：节点先在线 → 下发 realm（此时
// 计数为 0）→ 立即产生流量 → 周期轮询到达——流量必须入账，且下发
// 返回时基线必须已钉在下发时刻（状态轮询次数断言）。

type realmMeterMock struct {
	mu          sync.Mutex
	applied     bool
	appliedHash string
	applyCalls  int
	statusCalls int
	tcpIn       int64
	tcpOut      int64
	udpIn       int64
	udpOut      int64
	// FIX-029: packet counts behind the byte counts; zero means the
	// agent predates the field (bytes pass through unconverted).
	tcpInPkts  int64
	tcpOutPkts int64
	udpInPkts  int64
	udpOutPkts int64
}

func (m *realmMeterMock) setTraffic(tcpIn, tcpOut, udpIn, udpOut int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tcpIn, m.tcpOut, m.udpIn, m.udpOut = tcpIn, tcpOut, udpIn, udpOut
}

// setTrafficPackets replaces the byte counts with packet-carrying
// samples (FIX-029): the audit's controlled realm flow — 2000 bytes
// each way over 6/4 TCP packets — plus a UDP pair.
func (m *realmMeterMock) setTrafficPackets(tcpIn, tcpInPkts, tcpOut, tcpOutPkts, udpIn, udpInPkts, udpOut, udpOutPkts int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tcpIn, m.tcpInPkts = tcpIn, tcpInPkts
	m.tcpOut, m.tcpOutPkts = tcpOut, tcpOutPkts
	m.udpIn, m.udpInPkts = udpIn, udpInPkts
	m.udpOut, m.udpOutPkts = udpOut, udpOutPkts
}

func (m *realmMeterMock) calls() (apply, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyCalls, m.statusCalls
}

func startRealmMeterMockNode(t *testing.T, baseURL, secret string, counterID int64, mock *realmMeterMock) func() {
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
	// 模拟节点报当前 agent 版本：V1-01 版本门以 2.1.13 为界，
	// 'v1' 不是真实 agent 版本串，会被版本门 fail-closed 拦在下发之前。
	q.Set("version", "2.1.13")
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
					"type": "realm", "running": true, "enabled": true, "version": kernel.RealmVersion, "configHash": hash,
				}
			case "KernelStatus":
				mock.mu.Lock()
				mock.statusCalls++
				applied, hash := mock.applied, mock.appliedHash
				tcpIn, tcpOut, udpIn, udpOut := mock.tcpIn, mock.tcpOut, mock.udpIn, mock.udpOut
				tcpInPkts, tcpOutPkts, udpInPkts, udpOutPkts := mock.tcpInPkts, mock.tcpOutPkts, mock.udpInPkts, mock.udpOutPkts
				mock.mu.Unlock()
				data := map[string]interface{}{
					"kernels":      []map[string]interface{}{},
					"nftAvailable": true,
					"epoch":        "e1",
					"counters":     []map[string]interface{}{},
				}
				if applied {
					data["kernels"] = []map[string]interface{}{
						{"type": "realm", "running": true, "enabled": true, "version": kernel.RealmVersion, "configHash": hash},
					}
					data["counters"] = []map[string]interface{}{
						{"inboundId": counterID, "proto": "tcp", "dir": "in", "bytes": tcpIn, "packets": tcpInPkts},
						{"inboundId": counterID, "proto": "tcp", "dir": "out", "bytes": tcpOut, "packets": tcpOutPkts},
						{"inboundId": counterID, "proto": "udp", "dir": "in", "bytes": udpIn, "packets": udpInPkts},
						{"inboundId": counterID, "proto": "udp", "dir": "out", "bytes": udpOut, "packets": udpOutPkts},
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

func TestRealmForwardTrafficAfterApplyIsCredited(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-meter.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.21")
	tunnelID := seedTunnelForNftables(t, h, "realm-meter-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm', flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 30007)

	readFlow := func() (int64, int64) {
		var flow struct{ InFlow, OutFlow int64 }
		if err := r.DB().Raw(`SELECT in_flow, out_flow FROM forward WHERE id = ?`, forward.ID).Scan(&flow).Error; err != nil {
			t.Fatal(err)
		}
		return flow.InFlow, flow.OutFlow
	}
	baseline := func() (map[string]int64, bool) {
		states, err := h.repo.GetNftCounterStatesByNode(nodeID)
		if err != nil {
			t.Fatalf("load states: %v", err)
		}
		out := map[string]int64{}
		for _, st := range states {
			if st.ForwardID == forward.ID && st.RuleHash == "realm:e1" {
				out[st.Protocol+"/"+st.Direction] = st.Bytes
			}
		}
		return out, len(out) == 4
	}

	// R5-B-02：下发前「无基线」的前提检查必须放在节点上线之前。
	// 节点一上线，onNodeOnline 的同步重推（realm 隧道经
	// syncRealmNodes 建行＋下发）与异步 reconcileNodeKernels（下发
	// ＋轮尾 poll）就可能抢在检查之前造出 realm:e1 四计数基线——
	// 旧检查放在 waitForCondition 之后，满载调度一抖即翻红
	// （suite-gate flake，非产品回归）。节点从未上线时没有任何
	// 路径能下发/轮询，基线不可能存在，此处检查才是确定性的。
	if _, found := baseline(); found {
		t.Fatal("no realm baseline may exist before the kernel is applied")
	}

	// 节点先在线（与真机 Rig 同形态：节点长期在线、转发其后新建）。
	mock := &realmMeterMock{}
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	stop := startRealmMeterMockNode(t, srv.URL, "entry-secret", kernel.RealmCounterID(forward.ID), mock)
	defer stop()
	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")
	// R5-B-02：上线后故意静置 1 秒，让 onNodeOnline 的同步重推与
	// 异步对账先跑完——本测试的后续断言（下发必轮询、基线钉 0、
	// 下发后流量入账）对「谁先下发」必须不敏感；静置后仍通过，
	// 才算竞态真正闭合，而非靠调度侥幸。
	time.Sleep(time.Second)
	// 普通下发路径（forward 创建/更新走的同一入口）：realm 内核下发，
	// 此刻 agent 计数为 0。
	_, statusBefore := mock.calls()
	warnings, err := h.syncRealmNodes([]int64{nodeID}, nil)
	if err != nil {
		t.Fatalf("sync realm nodes: %v (warnings %v)", err, warnings)
	}
	applyCalls, statusAfter := mock.calls()
	if applyCalls == 0 {
		t.Fatal("realm kernel was not applied")
	}
	// 修复点：下发返回时必须已主动轮询一次把基线钉在下发时刻；
	// 否则下发后、首个周期轮询（60s）前产生的流量被首样本基线吞掉。
	if statusAfter <= statusBefore {
		t.Fatal("FIX-REALM-METER: apply path never polls kernel status, so the accounting baseline is left to the first periodic poll and early traffic is swallowed")
	}
	if base, found := baseline(); !found || base["tcp/to-target"] != 0 || base["tcp/from-target"] != 0 || base["udp/to-target"] != 0 || base["udp/from-target"] != 0 {
		t.Fatalf("baseline after apply = %v (found=%v), want all four counters pinned at 0", base, found)
	}

	// 下发后立即产生流量（真机上即创建后数秒内的一次回声）。
	mock.setTraffic(300, 1500, 40, 90)

	// 周期轮询到达：下发后流量必须按差分入账（方向口径：in =
	// to-target，与 N-01 钉死口径一致），TCP 与 UDP 同账。
	h.refreshNodeKernelStatus(nodeID)
	if inF, outF := readFlow(); inF != 340 || outF != 1590 {
		t.Fatalf("forward flow after first poll = %d/%d, want 340/1590 (post-apply traffic credited)", inF, outF)
	}
	if got := mustHandlerCount(t, h, `SELECT COALESCE((SELECT daily_used_bytes FROM user_quota WHERE user_id = 1), 0)`); got != 1930 {
		t.Fatalf("expected daily quota usage=1930, got %d", got)
	}
}

// TestRealmForwardTrafficCountsPayloadLayer (FIX-029, AUDIT-HIST):
// realm-phase traffic must bill at the same layer as gost-phase
// traffic. The audit's controlled flow — exactly 2000 payload bytes
// each way over one TCP connection — counted 2320 in / 2216 out on
// realm (nft IP layer: 6/4 packets, headers included) versus exactly
// 2000/2000 on gost (payload layer). With packet counts the panel
// strips the per-packet headers (52 TCP / 28 UDP): 2320-6*52=2008,
// 2216-4*52=2008, 128-2*28=72, 1000-10*28=720. The 8-byte residue per
// TCP direction is the two SYN packets' extra header bytes, documented
// on realmPayloadHeaderBytes — not a second bug.
func TestRealmForwardTrafficCountsPayloadLayer(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-payload.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry-payload", "198.51.100.22")
	tunnelID := seedTunnelForNftables(t, h, "realm-payload-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm', flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 30008)

	readFlow := func() (int64, int64) {
		var flow struct{ InFlow, OutFlow int64 }
		if err := r.DB().Raw(`SELECT in_flow, out_flow FROM forward WHERE id = ?`, forward.ID).Scan(&flow).Error; err != nil {
			t.Fatal(err)
		}
		return flow.InFlow, flow.OutFlow
	}

	mock := &realmMeterMock{}
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	stop := startRealmMeterMockNode(t, srv.URL, "entry-payload-secret", kernel.RealmCounterID(forward.ID), mock)
	defer stop()
	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")

	if _, err := h.syncRealmNodes([]int64{nodeID}, nil); err != nil {
		t.Fatalf("sync realm nodes: %v", err)
	}

	// The audit's exact controlled flow, as cumulative nft samples.
	mock.setTrafficPackets(2320, 6, 2216, 4, 128, 2, 1000, 10)
	h.refreshNodeKernelStatus(nodeID)
	if inF, outF := readFlow(); inF != 2080 || outF != 2728 {
		t.Fatalf("forward flow after first poll = %d/%d, want 2080/2728 (IP-layer bytes minus per-packet headers)", inF, outF)
	}
	if got := mustHandlerCount(t, h, `SELECT COALESCE((SELECT daily_used_bytes FROM user_quota WHERE user_id = 1), 0)`); got != 4808 {
		t.Fatalf("expected daily quota usage=4808, got %d", got)
	}

	// Mutation guard: without packets the same bytes must pass through
	// unconverted (pre-fix agent compat), so the conversion is proven
	// to come from the packet counts, not from a scaled constant.
	if got := realmCounterPayload(agentKernelCounter{Proto: "tcp", Dir: "in", Bytes: 2320, Packets: 0}); got != 2320 {
		t.Fatalf("packets==0 must pass bytes through, got %d", got)
	}
	if got := realmCounterPayload(agentKernelCounter{Proto: "tcp", Dir: "in", Bytes: 2320, Packets: 6}); got != 2008 {
		t.Fatalf("2320 bytes / 6 packets must convert to 2008, got %d", got)
	}
	if got := realmCounterPayload(agentKernelCounter{Proto: "udp", Dir: "out", Bytes: 1000, Packets: 10}); got != 720 {
		t.Fatalf("1000 bytes / 10 UDP packets must convert to 720, got %d", got)
	}
}
