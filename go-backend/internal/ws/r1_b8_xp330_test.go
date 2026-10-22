package ws

import (
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/auth"
	"go-backend/internal/store/model"
)

// X-P3-30 (L-12-1): WS 指标 hook 改直调。修复前每帧 `go onMetric`，
// 阻塞型 hook 下 N 帧同时堆积 N 个 goroutine；直调后读循环串行，
// 同一时刻至多 1 个 hook 在执行。
func TestR1B8_XP330_MetricHookRunsInline(t *testing.T) {
	r := fix132Repo(t)
	if err := r.DB().Create(&model.Node{ID: 43, Name: "n43", Secret: "node-secret-43", ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	server := NewServer(r, fix132Secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 1, PasswordChangedAt: 0}, nil
	})
	gate := make(chan struct{})
	var started int64
	server.SetNodeMetricHook(func(nodeID int64, info SystemInfo) {
		atomic.AddInt64(&started, 1)
		<-gate
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	node := dialNode(t, ts, "node-secret-43")
	waitNodeOnline(t, server, 43)

	msg := `{"uptime":10,"cpu_usage":1.5,"memory_usage":20,"disk_usage":30}`
	for i := 0; i < 8; i++ {
		if err := node.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			t.Fatalf("write metric %d: %v", i, err)
		}
	}
	// 给读循环充分时间处理已收帧：直调实现下第 1 个 hook 阻塞 gate，
	// 其余帧留在读缓冲中，started 恒为 1；旧实现 8 帧各起 goroutine，
	// started 会涨到 8。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&started) > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := atomic.LoadInt64(&started)
	close(gate)
	if got != 1 {
		t.Fatalf("指标 hook 应直调串行执行: started=%d, want 1（每帧起 goroutine 会堆积）", got)
	}
	// gate 打开后其余帧应被依次处理完。
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&started) == 8 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gate 打开后 8 帧应全部处理, started=%d", atomic.LoadInt64(&started))
}
