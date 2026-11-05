package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"go-backend/internal/auth"
	"go-backend/internal/http/handler"
	"go-backend/internal/store/repo"
)

// R-WS-TICKET 全链：登录态 JWT 经 Authorization 头换票 → 浏览器
// WS 只认票据建连；裸 JWT 进查询参数被拒；票据重放被拒；登出
// （E1-02）后未消费票据随纪元推进作废、旧 JWT 亦不能再换票。
func TestWSTicketIssuanceAndHandshakeEndToEnd(t *testing.T) {
	const secret = "router-ticket-secret"
	r, err := repo.Open(filepath.Join(t.TempDir(), "router-ticket.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()

	h := handler.New(r, secret)
	ts := httptest.NewServer(NewRouter(h, secret))
	defer ts.Close()

	adminToken, err := auth.GenerateToken(1, "admin", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	issueTicket := func(token string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/system-info/ticket", bytes.NewBufferString(`{}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("issue ticket request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var env struct {
			Code int `json:"code"`
			Data struct {
				Ticket string `json:"ticket"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("decode ticket response: %v", err)
		}
		return env.Code, env.Data.Ticket
	}

	dial := func(query string) (int, error) {
		t.Helper()
		conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/system-info?"+query, nil)
		if err == nil {
			_ = conn.Close()
			return http.StatusSwitchingProtocols, nil
		}
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		return code, err
	}

	// 未登录不得换票。
	if code, _ := issueTicket(""); code != 401 {
		t.Fatalf("unauthenticated ticket issue code = %d, want 401", code)
	}

	code, ticket := issueTicket(adminToken)
	if code != 0 || ticket == "" {
		t.Fatalf("issue ticket code = %d ticket empty = %v, want 0 + ticket", code, ticket == "")
	}
	if status, err := dial("type=0&ticket=" + url.QueryEscape(ticket)); err != nil || status != http.StatusSwitchingProtocols {
		t.Fatalf("dial with valid ticket: status = %d err = %v, want 101", status, err)
	}
	// 重放同票被拒。
	if status, _ := dial("type=0&ticket=" + url.QueryEscape(ticket)); status != http.StatusForbidden {
		t.Fatalf("replayed ticket dial status = %d, want 403", status)
	}
	// 裸 JWT 进查询参数被拒（修复前放行）。
	if status, _ := dial("type=0&secret=" + url.QueryEscape(adminToken)); status != http.StatusForbidden {
		t.Fatalf("bare JWT dial status = %d, want 403", status)
	}

	// 登出联动：先换一张新票，再登出——未消费票据随纪元推进作废，
	// 旧 JWT 也不能再换票。
	_, ticket2 := issueTicket(adminToken)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/user/logout", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("new logout request: %v", err)
	}
	req.Header.Set("Authorization", adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logout request: %v", err)
	}
	_ = resp.Body.Close()
	if status, _ := dial("type=0&ticket=" + url.QueryEscape(ticket2)); status != http.StatusForbidden {
		t.Fatalf("post-logout ticket dial status = %d, want 403", status)
	}
	if code, _ := issueTicket(adminToken); code != 401 {
		t.Fatalf("post-logout ticket issue code = %d, want 401", code)
	}
}
