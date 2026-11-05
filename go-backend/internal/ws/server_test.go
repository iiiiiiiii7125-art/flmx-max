package ws

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/store/repo"

	"github.com/gorilla/websocket"
)

func TestServeHTTPRejectsDisabledAdminToken(t *testing.T) {
	secret := "unit-test-secret"
	token, err := auth.GenerateToken(1, "admin_user", 0, secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	server := NewServer(nil, secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 0, PasswordChangedAt: 0}, nil
	})

	// R-WS-TICKET：握手改走票据；停用管理员的票据在消费复核处被拒。
	claims, err := auth.ParseClaims(token, secret)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	ticket, err := server.IssueWSTicket(1, claims.RoleID, claims.User, claims.IatMs, claims.Exp, 0)
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/system-info?type=0&ticket="+url.QueryEscape(ticket), nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden for disabled admin token, got %d", rec.Code)
	}
}

func TestServeHTTPRejectsNonAdminToken(t *testing.T) {
	secret := "unit-test-secret"
	token, err := auth.GenerateToken(2, "normal_user", 1, secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	server := NewServer(nil, secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 1, Status: 1, PasswordChangedAt: 0}, nil
	})

	// R-WS-TICKET：普通用户票据消费后进入 monitor 分发，按范围收窄
	// 口径（2026-11-01）一律拒绝。
	claims, err := auth.ParseClaims(token, secret)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	ticket, err := server.IssueWSTicket(2, claims.RoleID, claims.User, claims.IatMs, claims.Exp, 0)
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/system-info?type=0&ticket="+url.QueryEscape(ticket), nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden for non-admin token, got %d", rec.Code)
	}
}

func TestValidateAdminSessionRejectsAuthStateChanges(t *testing.T) {
	secret := "unit-test-secret"
	token, err := auth.GenerateToken(1, "admin_user", 0, secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	claims, err := auth.ParseClaims(token, secret)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}

	tests := []struct {
		name  string
		state *auth.UserAuthState
	}{
		{name: "disabled", state: &auth.UserAuthState{ID: 1, RoleID: 0, Status: 0, PasswordChangedAt: 0}},
		{name: "role changed", state: &auth.UserAuthState{ID: 1, RoleID: 1, Status: 1, PasswordChangedAt: 0}},
		{name: "password changed", state: &auth.UserAuthState{ID: 1, RoleID: 0, Status: 1, PasswordChangedAt: claims.IatMs + 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(nil, secret)
			server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
				return tt.state, nil
			})

			if ok := server.validateAdminSession(1, claims); ok {
				t.Fatalf("expected session validation to fail for %s state", tt.name)
			}
		})
	}
}

func TestValidateAdminSessionRejectsExpiredToken(t *testing.T) {
	secret := "unit-test-secret"
	token, err := auth.GenerateToken(1, "admin_user", 0, secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	claims, err := auth.ParseClaims(token, secret)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	claims.Exp = 1

	server := NewServer(nil, secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 0, Status: 1, PasswordChangedAt: 0}, nil
	})

	if ok := server.validateAdminSession(1, claims); ok {
		t.Fatal("expected expired token to be rejected")
	}
}

// 监控权限范围收窄（2026-11-01 用户拍板）：节点实时流仅管理员可
// 订阅，持有监控权限的普通用户拨号必须被拒（403）。本用例由
// TestServeHTTPAllowsMonitorTokenWithPermission 按新口径翻转而来。
func TestServeHTTPRejectsMonitorTokenWithPermission(t *testing.T) {
	secret := "unit-test-secret"
	token, err := auth.GenerateToken(2, "normal_user", 1, secret)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	r, err := repo.Open(t.TempDir() + "/monitor.db")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	if err := r.InsertMonitorPermission(2, 123); err != nil {
		t.Fatalf("insert permission: %v", err)
	}

	server := NewServer(r, secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 1, Status: 1, PasswordChangedAt: 0}, nil
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	claims, err := auth.ParseClaims(token, secret)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	ticket, err := server.IssueWSTicket(2, claims.RoleID, claims.User, claims.IatMs, claims.Exp, 0)
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(ts.URL, "http")+"/system-info?type=0&ticket="+url.QueryEscape(ticket),
		nil,
	)
	if err == nil {
		_ = conn.Close()
		t.Fatal("monitor-permission user must no longer connect to the realtime stream")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial websocket status = %d err = %v, want 403", code, err)
	}
}

func TestConnWrapSerializesConcurrentWrites(t *testing.T) {
	serverConn, clientConn := websocketTestPipe(t)
	defer serverConn.Close()
	defer clientConn.Close()

	cw := &connWrap{conn: serverConn}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for i := 0; i < 64; i++ {
			if _, _, err := clientConn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writeWSMessage(cw, websocket.TextMessage, []byte("x")); err != nil {
				t.Errorf("writeWSMessage() error = %v", err)
			}
		}()
	}

	wg.Wait()
	_ = clientConn.Close()
	<-readerDone
}

func websocketTestPipe(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverConnCh := make(chan *websocket.Conn, 1)
	serverErrCh := make(chan error, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		serverConnCh <- conn
	}))
	t.Cleanup(ts.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	select {
	case err := <-serverErrCh:
		t.Fatalf("upgrade websocket: %v", err)
	case serverConn := <-serverConnCh:
		return serverConn, clientConn
	}

	return nil, nil
}
