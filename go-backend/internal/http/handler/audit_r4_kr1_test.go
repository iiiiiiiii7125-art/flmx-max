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

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// ─── Round 4 KR1（R4B03-001/002/004）回归测试 ───

// kr1SeedNode 以 raw SQL 播种节点行（含 forward_mode/is_remote），
// 与 batch_upgrade_wave_test 的 seedWaveNode 同范式。
func kr1SeedNode(t *testing.T, r *repo.Repository, name, ip string, status, isRemote int, forwardMode string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, forward_mode)
		VALUES(?, ?, ?, ?, '', '30000-30010', '', 0, 0, 0, ?, ?, ?, '[::]', '[::]', 0, ?, ?)`,
		name, name+"-secret", ip, ip, now, now, status, isRemote, forwardMode).Error; err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// kr1CommandRecorder 记录 mock agent 收到的命令（按 type 计数并留
// SetProtocol 的 data 载荷），所有带 requestId 的命令立即回成功。
type kr1CommandRecorder struct {
	mu              sync.Mutex
	counts          map[string]int
	setProtocolData []map[string]interface{}
}

func (m *kr1CommandRecorder) count(typ string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[typ]
}

func (m *kr1CommandRecorder) lastSetProtocol() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.setProtocolData) == 0 {
		return nil
	}
	return m.setProtocolData[len(m.setProtocolData)-1]
}

func startKR1MockNode(t *testing.T, baseURL, secret string, rec *kr1CommandRecorder) func() {
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
	q.Set("http", "0")
	q.Set("tls", "0")
	q.Set("socks", "0")
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
				Type      string                 `json:"type"`
				RequestID string                 `json:"requestId"`
				Data      map[string]interface{} `json:"data"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil || strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			if rec != nil {
				rec.mu.Lock()
				if rec.counts == nil {
					rec.counts = make(map[string]int)
				}
				rec.counts[cmd.Type]++
				if cmd.Type == "SetProtocol" {
					rec.setProtocolData = append(rec.setProtocolData, cmd.Data)
				}
				rec.mu.Unlock()
			}
			respBytes, err := json.Marshal(map[string]interface{}{
				"type":      fmt.Sprintf("%sResponse", cmd.Type),
				"success":   true,
				"message":   "OK",
				"requestId": cmd.RequestID,
			})
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

func kr1NodeStatus(t *testing.T, r *repo.Repository, id int64) int {
	t.Helper()
	node, err := r.GetNodeByID(id)
	if err != nil || node == nil {
		t.Fatalf("get node %d: %v", id, err)
	}
	return node.Status
}

// TestReconcileNodeOnlineStatuses 钉周期对账的四条腿：僵尸在线在
// 宽限内不复位、过宽限复位；有实时会话的节点永不复位（重连不误
// 清）；nftables 与远程节点不在对账范围。
func TestReconcileNodeOnlineStatuses(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4-reconcile.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	staleID := kr1SeedNode(t, r, "r4-stale", "198.51.100.21", 1, 0, "agent")
	onlineID := kr1SeedNode(t, r, "r4-online", "198.51.100.22", 0, 0, "agent")
	nftID := kr1SeedNode(t, r, "r4-nft", "198.51.100.23", 1, 0, "nftables")
	remoteID := kr1SeedNode(t, r, "r4-remote", "198.51.100.24", 1, 1, "agent")

	stopOnline := startKR1MockNode(t, srv.URL, "r4-online-secret", nil)
	defer stopOnline()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(onlineID) }, "online mock node connected")
	// 会话登记（IsNodeOnline）与 DB status 落盘是两步异步动作，一次性
	// 读 DB 会与落盘竞速（CI 高负载下偶发读到 0）；改为轮询等落盘。
	waitForCondition(t, 10*time.Second, func() bool {
		node, err := r.GetNodeByID(onlineID)
		return err == nil && node != nil && node.Status == 1
	}, "online node status persisted as 1")

	offlineSince := make(map[int64]time.Time)
	t0 := time.Now()

	// 第一轮：僵尸节点仅登记计时，宽限内不复位。
	h.reconcileNodeOnlineStatuses(t0, offlineSince)
	if got := kr1NodeStatus(t, r, staleID); got != 1 {
		t.Fatalf("宽限内僵尸节点 status = %d, want 1（不得立即复位）", got)
	}
	if _, ok := offlineSince[staleID]; !ok {
		t.Fatal("僵尸节点应进入离线计时")
	}
	if _, ok := offlineSince[onlineID]; ok {
		t.Fatal("有实时会话的节点不得进入离线计时")
	}

	// 宽限未满：仍不复位；在线/nftables/远程全程不动。
	h.reconcileNodeOnlineStatuses(t0.Add(nodeStatusReconcileGrace-time.Second), offlineSince)
	if got := kr1NodeStatus(t, r, staleID); got != 1 {
		t.Fatalf("宽限未满僵尸节点 status = %d, want 1", got)
	}

	// 宽限已过：僵尸复位，其余三类保持 status=1。
	h.reconcileNodeOnlineStatuses(t0.Add(nodeStatusReconcileGrace+time.Second), offlineSince)
	if got := kr1NodeStatus(t, r, staleID); got != 0 {
		t.Fatalf("过宽限僵尸节点 status = %d, want 0", got)
	}
	if got := kr1NodeStatus(t, r, onlineID); got != 1 {
		t.Fatalf("在线节点 status = %d, want 1（重连/在线不误清）", got)
	}
	if got := kr1NodeStatus(t, r, nftID); got != 1 {
		t.Fatalf("nftables 节点 status = %d, want 1（既定口径不误伤）", got)
	}
	if got := kr1NodeStatus(t, r, remoteID); got != 1 {
		t.Fatalf("远程节点 status = %d, want 1（不在对账范围）", got)
	}
	if _, ok := offlineSince[staleID]; ok {
		t.Fatal("复位后离线计时应清除")
	}

	// 复位后重连：握手置回 1，后续对账轮次不得再清。
	stopStale := startKR1MockNode(t, srv.URL, "r4-stale-secret", nil)
	defer stopStale()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(staleID) }, "reconnected mock node online")
	h.reconcileNodeOnlineStatuses(t0.Add(2*nodeStatusReconcileGrace), offlineSince)
	if got := kr1NodeStatus(t, r, staleID); got != 1 {
		t.Fatalf("重连节点 status = %d, want 1", got)
	}
}
