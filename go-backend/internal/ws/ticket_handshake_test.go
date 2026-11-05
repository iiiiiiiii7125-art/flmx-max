package ws

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"go-backend/internal/auth"
)

// R-WS-TICKET 握手矩阵：浏览器 type=0 通道只认短期单次票据。
// 裸 JWT（哪怕完全合法）、重放、纪元漂移、过期 JWT 换出的票、
// 未知/缺失票据一律 403；合法票据一次通过。

func ticketTestServer(t *testing.T, state *auth.UserAuthState) (*Server, *httptest.Server) {
	t.Helper()
	server := NewServer(nil, fix132Secret)
	server.SetUserAuthStateLookup(func(userID int64) (*auth.UserAuthState, error) {
		return state, nil
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return server, ts
}

func dialExpectForbidden(t *testing.T, ts *httptest.Server, query string) {
	t.Helper()
	_, resp, err := dialWS(t, ts, query)
	if err == nil {
		t.Fatalf("dial %q unexpectedly succeeded", query)
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %q status = %d err = %v, want 403", query, code, err)
	}
}

func TestWSHandshakeRejectsBareJWTInQuery(t *testing.T) {
	state := &auth.UserAuthState{ID: 51, RoleID: 0, Status: 1}
	_, ts := ticketTestServer(t, state)
	token := fix132AdminToken(t, 51)

	// 完全合法的管理员 JWT 直接放查询参数：修复前放行、现必须 403。
	dialExpectForbidden(t, ts, "type=0&secret="+url.QueryEscape(token))
	// secret 参数放任意值同样不读。
	dialExpectForbidden(t, ts, "type=0&secret=whatever")
	// 缺票、未知票一律拒。
	dialExpectForbidden(t, ts, "type=0")
	dialExpectForbidden(t, ts, "type=0&ticket=no-such-ticket")
}

func TestWSHandshakeValidTicketOnceThenReplayRejected(t *testing.T) {
	state := &auth.UserAuthState{ID: 52, RoleID: 0, Status: 1}
	server, ts := ticketTestServer(t, state)

	query, err := fix132TicketQuery(server, 52)
	if err != nil {
		t.Fatalf("ticket query: %v", err)
	}
	conn, _, err := dialWS(t, ts, query)
	if err != nil {
		t.Fatalf("valid ticket dial: %v", err)
	}
	_ = conn.Close()

	// 同一票据重放：单次使用，必须 403。
	dialExpectForbidden(t, ts, query)
}

func TestWSHandshakeRejectsTicketAfterEpochAdvance(t *testing.T) {
	// 签发后用户登出/改密（吊销纪元推进）：未消费票据作废。
	// 票据的 IatMs 远大于新水位，TokenStale 本身不触发——拦截来自
	// 票据绑定的纪元等值复核。
	state := &auth.UserAuthState{ID: 53, RoleID: 0, Status: 1}
	server, ts := ticketTestServer(t, state)

	query, err := fix132TicketQuery(server, 53)
	if err != nil {
		t.Fatalf("ticket query: %v", err)
	}
	state.TokensRevokedBefore = 500 // 模拟 E1-02 登出推进水位
	dialExpectForbidden(t, ts, query)
}

func TestWSHandshakeRejectsTicketFromExpiredJWT(t *testing.T) {
	state := &auth.UserAuthState{ID: 54, RoleID: 0, Status: 1}
	server, ts := ticketTestServer(t, state)

	past := time.Now().Add(-time.Hour)
	ticket, err := server.IssueWSTicket(54, 0, "admin", past.UnixMilli(), past.Unix(), 0)
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	dialExpectForbidden(t, ts, "type=0&ticket="+url.QueryEscape(ticket))
}

func TestWSHandshakeRejectsTicketOnRoleDrift(t *testing.T) {
	state := &auth.UserAuthState{ID: 55, RoleID: 0, Status: 1}
	server, ts := ticketTestServer(t, state)

	query, err := fix132TicketQuery(server, 55)
	if err != nil {
		t.Fatalf("ticket query: %v", err)
	}
	state.RoleID = 1 // 签发后角色被调整
	dialExpectForbidden(t, ts, query)
}
