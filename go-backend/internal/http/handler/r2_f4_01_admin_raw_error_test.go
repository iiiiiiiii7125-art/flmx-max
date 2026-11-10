package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// R2-F4-01 回归（管理员面仓储错误原文直出 13 处收口）：掉表法——
// 删 user_tunnel 表后，管理员面三端点须回通用文案 "Internal
// error"，不得回 "SQL logic error: no such table..." 原文；幻影
// ID 的定型文案（用户不存在／隧道不存在）保留原样。
func TestR2F401AdminRawErrorGeneralized(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)

	// 幻影 ID 定型文案保留（DB 完好时）。
	assign := func(body string) string {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user-tunnel/assign", strings.NewReader(body))
		rec := httptest.NewRecorder()
		a.h.userTunnelAssign(rec, req)
		return rec.Body.String()
	}
	if got := assign(`{"userId":999,"tunnelId":1}`); !strings.Contains(got, "用户不存在") {
		t.Fatalf("phantom user must keep fixed text, got %s", got)
	}
	if got := assign(`{"userId":1,"tunnelId":999}`); !strings.Contains(got, "隧道不存在") {
		t.Fatalf("phantom tunnel must keep fixed text, got %s", got)
	}
	_ = tunnelID

	// 掉表：user_tunnel 表消失，仓储错误原文不得出回面。
	if err := a.h.repo.DB().Exec("DROP TABLE user_tunnel").Error; err != nil {
		t.Fatalf("drop user_tunnel: %v", err)
	}
	msgOf := func(body string) string {
		var envelope struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(body), &envelope); err != nil {
			t.Fatalf("decode envelope %s: %v", body, err)
		}
		return envelope.Msg
	}
	mustBeInternalError := func(name, body string) {
		if msg := msgOf(body); msg != "Internal error" {
			t.Fatalf("%s: msg=%q, want %q (body %s)", name, msg, "Internal error", body)
		}
		if strings.Contains(body, "no such table") || strings.Contains(body, "SQL logic error") {
			t.Fatalf("%s: storage error leaked into response: %s", name, body)
		}
	}

	listReq := httptest.NewRequest(http.MethodPost, "/api/v1/user-tunnel/list", strings.NewReader(`{"userId":1}`))
	listRec := httptest.NewRecorder()
	a.h.userTunnelList(listRec, listReq)
	mustBeInternalError("userTunnelList", listRec.Body.String())

	removeReq := httptest.NewRequest(http.MethodPost, "/api/v1/user-tunnel/remove", strings.NewReader(`{"id":1}`))
	removeRec := httptest.NewRecorder()
	a.h.userTunnelRemove(removeRec, removeReq)
	mustBeInternalError("userTunnelRemove", removeRec.Body.String())

	// 掉表后 assign 的 ValidateIDsExist 读 user/tunnel 表（仍在）
	// → 幻影判定通过；写面 InsertUserTunnel 撞缺表，须泛化。
	assignBody := `{"userId":1,"tunnelId":` + strconv.FormatInt(tunnelID, 10) + `}`
	if got := assign(assignBody); msgOf(got) != "Internal error" {
		t.Fatalf("userTunnelAssign after drop: msg=%q, want Internal error (body %s)", msgOf(got), got)
	}
	if got := assign(assignBody); strings.Contains(got, "no such table") {
		t.Fatalf("userTunnelAssign after drop leaked storage text: %s", got)
	}
}
