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

// 1.7.7 批量升级一波全下发回归（用户 2026-11-01 口径）：单批内全部
// 节点同时派发，不再按 batchWorkers=5 分波等待；单批上限 50 台。

type waveBarrier struct {
	mu       sync.Mutex
	arrivals int
	release  chan struct{}
}

func (b *waveBarrier) arrive() {
	b.mu.Lock()
	b.arrivals++
	b.mu.Unlock()
}

func (b *waveBarrier) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.arrivals
}

// startWaveMockNode 起一个 mock agent：普通命令立即回成功，
// UpgradeAgent 先登记到达再阻塞到 barrier 放行后回成功——以此观测
// 批量派发是否同时在途（旧 5 工人信号量下到达数会卡在 5）。
func startWaveMockNode(t *testing.T, baseURL, secret string, b *waveBarrier) func() {
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
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil || strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			if cmd.Type == "UpgradeAgent" {
				b.arrive()
				<-b.release
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

func seedWaveNode(t *testing.T, r *repo.Repository, name, ip string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	// version 留 NULL：批量路径跳过版本比较直达派发；status=0 使
	// onNodeOnline 不触发重部署，mock 只需应付 UpgradeAgent。
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, '', '30000-30010', '', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`, name, name+"-secret", ip, ip, now, now).Error; err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

type batchResultsPayload struct {
	Code int `json:"code"`
	Data struct {
		Results []struct {
			ID      int64  `json:"id"`
			Success bool   `json:"success"`
			Message string `json:"message"`
		} `json:"results"`
	} `json:"data"`
}

func TestNodeBatchUpgradeDispatchesAllInOneWave(t *testing.T) {
	stubGHMirror404(t) // 下发预热本地化＋排空（GATE F3），不研究预热本身
	r, err := repo.Open(t.TempDir() + "/batch-wave.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	const n = 8
	barrier := &waveBarrier{release: make(chan struct{})}
	ids := make([]int64, 0, n)
	stops := make([]func(), 0, n)
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("wave-%d", i)
		ids = append(ids, seedWaveNode(t, r, name, fmt.Sprintf("198.51.100.%d", 10+i)))
		stops = append(stops, startWaveMockNode(t, srv.URL, name+"-secret", barrier))
	}
	for _, id := range ids {
		id := id
		waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, "wave mock node online")
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := newAuthenticatedJSONRequest(t, map[string]interface{}{
			"ids":     ids,
			"version": "9.9.9",
		})
		rec := httptest.NewRecorder()
		h.nodeBatchUpgrade(rec, req)
		done <- rec
	}()

	// 核心断言：8 条命令同时在途。旧信号量（5）下到达数会停在 5、
	// 直到先行者返回才续派——本等待必然超时。
	deadline := time.Now().Add(15 * time.Second)
	for barrier.count() < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := barrier.count(); got != n {
		close(barrier.release)
		<-done
		t.Fatalf("同时在途的 UpgradeAgent = %d, want %d（一波全下发）", got, n)
	}
	close(barrier.release)

	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("批量升级放行后未在限时内返回整批回执")
	}
	var payload batchResultsPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Data.Results) != n {
		t.Fatalf("回执条数 = %d, want %d", len(payload.Data.Results), n)
	}
	for _, item := range payload.Data.Results {
		if !item.Success {
			t.Fatalf("节点 %d 应升级成功: %+v", item.ID, item)
		}
	}
}

// TestNodeBatchUpgradeAcceptsFiftyNodes 是上限边界（1.7.7 由 25 抬到
// 50）：50 台必须放行进入派发（节点离线逐条失败回执属正常结果），
// 不得被上限误拒；51 台由 TestC09BatchUpgradeRejectsTooManyIDs 钉
// （其用例数按 maxBatchUpgradeNodes+1 动态生成）。
func TestNodeBatchUpgradeAcceptsFiftyNodes(t *testing.T) {
	stubGHMirror404(t) // 下发预热本地化＋排空（GATE F3），不研究预热本身
	r, err := repo.Open(t.TempDir() + "/batch-fifty.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	ids := make([]int64, 0, maxBatchUpgradeNodes)
	for i := 0; i < maxBatchUpgradeNodes; i++ {
		ids = append(ids, int64(1000+i))
	}
	req := newAuthenticatedJSONRequest(t, map[string]interface{}{
		"ids":     ids,
		"version": "9.9.9",
	})
	rec := httptest.NewRecorder()
	h.nodeBatchUpgrade(rec, req)
	if strings.Contains(rec.Body.String(), "单次批量升级最多") {
		t.Fatalf("50 台不得被上限拒绝: %s", rec.Body.String())
	}
	var payload batchResultsPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Data.Results) != maxBatchUpgradeNodes {
		t.Fatalf("回执条数 = %d, want %d（整批汇总）", len(payload.Data.Results), maxBatchUpgradeNodes)
	}
}
