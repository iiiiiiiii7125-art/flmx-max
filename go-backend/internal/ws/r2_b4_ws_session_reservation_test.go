package ws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// R2X-022 回归（批 4）：WS 每用户会话上限的 check-then-act 必须原子化。
// 修复前 handleAdmin/handleMonitor 先以 userSessionCounts 计数放行、
// Upgrade 之后才登记，40 并发齐发时计数检查全部在登记之前通过，
// 实测登记数可达 11+（> 上限 10）。并发用例只用既有行为面
// （真实拨号 + userSessionCounts），保证在旧实现上可编译、可复现红；
// Upgrade 失败用例另加预留计数断言（新机制防漏门，修复后生效）。
//
// 注（2026-11-01 监控权限范围收窄）：monitor 通道准入已关闭，原以
// monitor 会话为载体的三个用例（并发上限 / Upgrade 失败 / 用户隔离
// 的 monitor 分支）随之退役；预留机制是 kind 通用代码，admin 通道
// 用例继续全量覆盖。monitor 准入关闭本身由 server_test.go 的
// TestServeHTTPRejectsMonitorTokenWithPermission 与
// monitor_scope_test.go 把门。

// r2b4ConcurrentDial 从同一时刻并发发起 n 次拨号，返回成功连接与失败数。
// 不在 goroutine 内用 t.Fatalf，失败经结果通道回收。
// R-WS-TICKET 后票据单次使用：每次拨号须各自取票，故入参为查询串
// 工厂而非共享查询串（旧形态共享一条 JWT 查询串已随票据失效）。
func r2b4ConcurrentDial(ts *httptest.Server, queryFor func() (string, error), n int) (conns []*websocket.Conn, failures int) {
	type result struct {
		conn *websocket.Conn
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, n)
	var wg sync.WaitGroup
	base := "ws" + strings.TrimPrefix(ts.URL, "http") + "/system-info?"
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			query, err := queryFor()
			if err != nil {
				results <- result{err: err}
				return
			}
			conn, _, err := websocket.DefaultDialer.Dial(base+query, nil)
			results <- result{conn: conn, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for res := range results {
		if res.err != nil {
			failures++
			continue
		}
		conns = append(conns, res.conn)
	}
	return conns, failures
}

// r2b4WaitSettled 等服务端登记数追上客户端成功数（或超时），返回最终登记数。
func r2b4WaitSettled(server *Server, userID int64, kind string, successes int) int {
	deadline := time.Now().Add(3 * time.Second)
	count := 0
	for {
		admins, monitors := server.userSessionCounts(userID)
		if kind == "monitor" {
			count = monitors
		} else {
			count = admins
		}
		if count >= successes || time.Now().After(deadline) {
			return count
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestR2B4ConcurrentAdminSessionLimit(t *testing.T) {
	for round := 0; round < 3; round++ {
		server, ts := fix132AdminServer(t)
		queryFor := func() (string, error) { return fix132TicketQuery(server, 21) }
		conns, failures := r2b4ConcurrentDial(ts, queryFor, 40)
		registered := r2b4WaitSettled(server, 21, "admin", len(conns))
		for _, c := range conns {
			_ = c.Close()
		}
		if len(conns) > wsMaxSessionsPerUser {
			t.Fatalf("round %d: admin successes = %d, want <= %d (failures=%d)", round, len(conns), wsMaxSessionsPerUser, failures)
		}
		if registered > wsMaxSessionsPerUser {
			t.Fatalf("round %d: admin registered = %d, want <= %d", round, registered, wsMaxSessionsPerUser)
		}
		if len(conns) != wsMaxSessionsPerUser {
			t.Fatalf("round %d: admin successes = %d, want exactly %d", round, len(conns), wsMaxSessionsPerUser)
		}
	}
}

// Upgrade 失败（非 WS 请求走完鉴权后 Upgrade 报错）不得泄漏名额：
// 失败注入次数远超上限后，串行仍应能建满 10 条、且第 11 条被拒。
// 该用例在旧实现上无预留可漏、属新机制的防漏回归门。
func TestR2B4UpgradeFailureReleasesReservation(t *testing.T) {
	t.Run("admin", func(t *testing.T) {
		server, ts := fix132AdminServer(t)
		for i := 0; i < 20; i++ {
			// 票据单次使用且在 Upgrade 失败时也已烧毁：每次尝试各取新票。
			query, err := fix132TicketQuery(server, 23)
			if err != nil {
				t.Fatalf("issue ticket query: %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, "/system-info?"+query, nil)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code == http.StatusSwitchingProtocols {
				t.Fatalf("attempt %d: non-websocket request unexpectedly upgraded", i)
			}
		}
		if got := server.wsReservationCount(23, wsSessionKindAdmin); got != 0 {
			t.Fatalf("admin reservations after failed upgrades = %d, want 0", got)
		}
		var conns []*websocket.Conn
		for i := 0; i < wsMaxSessionsPerUser; i++ {
			conns = append(conns, dialAdmin(t, server, ts, 23))
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if admins, _ := server.userSessionCounts(23); admins == wsMaxSessionsPerUser {
				break
			} else if time.Now().After(deadline) {
				t.Fatalf("after failed upgrades, admin registered = %d, want %d (reservation leaked?)", admins, wsMaxSessionsPerUser)
			}
			time.Sleep(10 * time.Millisecond)
		}
		// 登记落点与预留消费在同一次加锁内完成，故登记满 10 后预留必为 0；
		// 客户端 Dial 返回只证明握手完成、可能早于服务端登记（R1-B10 同理），
		// 必须在登记等待之后断言，不能在 Dial 直后断言。
		if got := server.wsReservationCount(23, wsSessionKindAdmin); got != 0 {
			t.Fatalf("admin reservations after successful upgrades = %d, want 0 (must convert to registered)", got)
		}
		ticketQuery, err := fix132TicketQuery(server, 23)
		if err != nil {
			t.Fatalf("issue ticket query: %v", err)
		}
		conn, resp, err := dialWS(t, ts, ticketQuery)
		if err == nil {
			_ = conn.Close()
			t.Fatal("11th admin session accepted after filling 10 post-failure")
		}
		if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Fatalf("11th admin dial status = %d err = %v, want 429", code, err)
		}
		for _, c := range conns {
			_ = c.Close()
		}
	})

}

// 不同用户互不占用名额：A 建满 10 后 B 仍可建连（admin 通道验；
// monitor 通道已随范围收窄退役，见文件头注）。
func TestR2B4SessionLimitPerUserIsolation(t *testing.T) {
	t.Run("admin", func(t *testing.T) {
		server, ts := fix132AdminServer(t)
		for i := 0; i < wsMaxSessionsPerUser; i++ {
			dialAdmin(t, server, ts, 31)
		}
		_ = dialAdmin(t, server, ts, 32)
	})
}
