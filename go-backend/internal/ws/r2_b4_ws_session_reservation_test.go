package ws

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/auth"
)

// R2X-022 回归（批 4）：WS 每用户会话上限的 check-then-act 必须原子化。
// 修复前 handleAdmin/handleMonitor 先以 userSessionCounts 计数放行、
// Upgrade 之后才登记，40 并发齐发时计数检查全部在登记之前通过，
// 实测登记数可达 11+（> 上限 10）。并发用例只用既有行为面
// （真实拨号 + userSessionCounts），保证在旧实现上可编译、可复现红；
// Upgrade 失败用例另加预留计数断言（新机制防漏门，修复后生效）。

func r2b4MonitorToken(t *testing.T, userID int64) string {
	t.Helper()
	token, err := auth.GenerateToken(userID, "monitor", 1, fix132Secret)
	if err != nil {
		t.Fatalf("generate monitor token: %v", err)
	}
	return token
}

func r2b4MonitorServer(t *testing.T, userIDs ...int64) (*Server, *httptest.Server) {
	t.Helper()
	r := fix132Repo(t)
	for _, id := range userIDs {
		if err := r.InsertMonitorPermission(id, 123); err != nil {
			t.Fatalf("insert monitor permission for %d: %v", id, err)
		}
	}
	server := NewServer(r, fix132Secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 1, Status: 1, PasswordChangedAt: 0}, nil
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return server, ts
}

func dialMonitor(t *testing.T, ts *httptest.Server, userID int64) *websocket.Conn {
	t.Helper()
	conn, _, err := dialWS(t, ts, "type=0&secret="+url.QueryEscape(r2b4MonitorToken(t, userID)))
	if err != nil {
		t.Fatalf("dial monitor ws: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// r2b4ConcurrentDial 从同一时刻并发发起 n 次拨号，返回成功连接与失败数。
// 不在 goroutine 内用 t.Fatalf，失败经结果通道回收。
func r2b4ConcurrentDial(ts *httptest.Server, query string, n int) (conns []*websocket.Conn, failures int) {
	type result struct {
		conn *websocket.Conn
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, n)
	var wg sync.WaitGroup
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/system-info?" + query
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			conn, _, err := websocket.DefaultDialer.Dial(u, nil)
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
		query := "type=0&secret=" + url.QueryEscape(fix132AdminToken(t, 21))
		conns, failures := r2b4ConcurrentDial(ts, query, 40)
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

func TestR2B4ConcurrentMonitorSessionLimit(t *testing.T) {
	for round := 0; round < 3; round++ {
		server, ts := r2b4MonitorServer(t, 22)
		query := "type=0&secret=" + url.QueryEscape(r2b4MonitorToken(t, 22))
		conns, failures := r2b4ConcurrentDial(ts, query, 40)
		registered := r2b4WaitSettled(server, 22, "monitor", len(conns))
		for _, c := range conns {
			_ = c.Close()
		}
		if len(conns) > wsMaxSessionsPerUser {
			t.Fatalf("round %d: monitor successes = %d, want <= %d (failures=%d)", round, len(conns), wsMaxSessionsPerUser, failures)
		}
		if registered > wsMaxSessionsPerUser {
			t.Fatalf("round %d: monitor registered = %d, want <= %d", round, registered, wsMaxSessionsPerUser)
		}
		if len(conns) != wsMaxSessionsPerUser {
			t.Fatalf("round %d: monitor successes = %d, want exactly %d", round, len(conns), wsMaxSessionsPerUser)
		}
	}
}

// Upgrade 失败（非 WS 请求走完鉴权后 Upgrade 报错）不得泄漏名额：
// 失败注入次数远超上限后，串行仍应能建满 10 条、且第 11 条被拒。
// 该用例在旧实现上无预留可漏、属新机制的防漏回归门。
func TestR2B4UpgradeFailureReleasesReservation(t *testing.T) {
	t.Run("admin", func(t *testing.T) {
		server, ts := fix132AdminServer(t)
		token := fix132AdminToken(t, 23)
		for i := 0; i < 20; i++ {
			req := httptest.NewRequest(http.MethodGet, "/system-info?type=0&secret="+url.QueryEscape(token), nil)
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
			conns = append(conns, dialAdmin(t, ts, 23))
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
		conn, resp, err := dialWS(t, ts, "type=0&secret="+url.QueryEscape(fix132AdminToken(t, 23)))
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

	t.Run("monitor", func(t *testing.T) {
		server, ts := r2b4MonitorServer(t, 24)
		token := r2b4MonitorToken(t, 24)
		for i := 0; i < 20; i++ {
			req := httptest.NewRequest(http.MethodGet, "/system-info?type=0&secret="+url.QueryEscape(token), nil)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code == http.StatusSwitchingProtocols {
				t.Fatalf("attempt %d: non-websocket request unexpectedly upgraded", i)
			}
		}
		if got := server.wsReservationCount(24, wsSessionKindMonitor); got != 0 {
			t.Fatalf("monitor reservations after failed upgrades = %d, want 0", got)
		}
		var conns []*websocket.Conn
		for i := 0; i < wsMaxSessionsPerUser; i++ {
			conns = append(conns, dialMonitor(t, ts, 24))
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, monitors := server.userSessionCounts(24); monitors == wsMaxSessionsPerUser {
				break
			} else if time.Now().After(deadline) {
				t.Fatalf("after failed upgrades, monitor registered = %d, want %d (reservation leaked?)", monitors, wsMaxSessionsPerUser)
			}
			time.Sleep(10 * time.Millisecond)
		}
		// 同 admin 子例：登记等待之后再断言预留归零。
		if got := server.wsReservationCount(24, wsSessionKindMonitor); got != 0 {
			t.Fatalf("monitor reservations after successful upgrades = %d, want 0 (must convert to registered)", got)
		}
		conn, resp, err := dialWS(t, ts, "type=0&secret="+url.QueryEscape(r2b4MonitorToken(t, 24)))
		if err == nil {
			_ = conn.Close()
			t.Fatal("11th monitor session accepted after filling 10 post-failure")
		}
		if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Fatalf("11th monitor dial status = %d err = %v, want 429", code, err)
		}
		for _, c := range conns {
			_ = c.Close()
		}
	})
}

// 不同用户互不占用名额：A 建满 10 后 B 仍可建连（admin 与 monitor 各验）。
func TestR2B4SessionLimitPerUserIsolation(t *testing.T) {
	t.Run("admin", func(t *testing.T) {
		_, ts := fix132AdminServer(t)
		for i := 0; i < wsMaxSessionsPerUser; i++ {
			dialAdmin(t, ts, 31)
		}
		_ = dialAdmin(t, ts, 32)
	})
	t.Run("monitor", func(t *testing.T) {
		_, ts := r2b4MonitorServer(t, 33, 34)
		for i := 0; i < wsMaxSessionsPerUser; i++ {
			dialMonitor(t, ts, 33)
		}
		_ = dialMonitor(t, ts, 34)
	})
}
