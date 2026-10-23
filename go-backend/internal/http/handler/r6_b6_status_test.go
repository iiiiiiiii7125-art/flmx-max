package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// R6 批6（R6X-014）：login / sub_store 两入口停用判据由 Status == 0
// 改为 Status != 1，与全族 fail-closed 口径对齐。
//
// 红态（修复前）：status=2 / -1 的存量僵尸行在 login 取得 token、
// 在 sub_store 取得本人用量头；status=0/1 行为逐字不变（防过修对照）。

func TestR6B6LoginSubStoreStatusPredicate(t *testing.T) {
	resetLoginLimitersForTest()
	resetSubStoreLimiterForTest()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	const password = "probe-pass-123"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	now := time.Now().UnixMilli()

	statuses := []int{2, -1, 0, 1}
	for i, status := range statuses {
		username := fmt.Sprintf("r6b6user%d", i)
		// CreateUser(username, pwdHash, roleID, expTime, flow, flowResetTime, num, status, maxConn, now)
		if _, err := r.CreateUser(username, hash, 1, now+86400000, 100, 1, 100, status, 0, now); err != nil {
			t.Fatalf("create user status=%d: %v", status, err)
		}

		// --- login ---
		lw := httptest.NewRecorder()
		lreq := httptest.NewRequest(http.MethodPost, "/api/v1/user/login",
			strings.NewReader(`{"username":`+jsonQuote(username)+`,"password":`+jsonQuote(password)+`}`))
		lreq.Header.Set("Content-Type", "application/json")
		lreq.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", 10+i)
		h.login(lw, lreq)
		lenv := decodeEnvelope(t, lw)

		// --- sub_store ---
		sw := httptest.NewRecorder()
		sreq := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
			strings.NewReader(`{"user":`+jsonQuote(username)+`,"password":`+jsonQuote(password)+`,"tunnel":"-1"}`))
		sreq.Header.Set("Content-Type", "application/json")
		sreq.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", 50+i)
		h.openAPISubStore(sw, sreq)

		if status == 1 {
			// 合法格：行为逐字不变。
			if lenv.Code != 0 {
				t.Errorf("status=1 login must succeed, got code=%d msg=%q", lenv.Code, lenv.Msg)
			}
			var data struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(lenv.Data, &data); err != nil || data.Token == "" {
				t.Errorf("status=1 login must issue token, err=%v data=%s", err, lenv.Data)
			}
			if got := sw.Header().Get("subscription-userinfo"); !strings.Contains(got, "total=107374182400") {
				t.Errorf("status=1 sub_store must return userinfo with total, got header=%q body=%q", got, sw.Body.String())
			}
			continue
		}

		// 停用格（含存量非法值 2/-1 与合法停用 0）：两入口均拒绝。
		if lenv.Code == 0 {
			t.Errorf("status=%d login must be rejected, got code=0 data=%s", status, lenv.Data)
		}
		if lenv.Msg != "账号被停用" {
			t.Errorf("status=%d login msg = %q, want 账号被停用", status, lenv.Msg)
		}
		if strings.Contains(string(lenv.Data), "token") {
			t.Errorf("status=%d login must not issue token, data=%s", status, lenv.Data)
		}
		if got := sw.Header().Get("subscription-userinfo"); got != "" {
			t.Errorf("status=%d sub_store must not return userinfo, got header=%q", status, got)
		}
		var senv struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(sw.Body.Bytes(), &senv); err != nil {
			t.Errorf("status=%d sub_store must return envelope rejection, body=%q err=%v", status, sw.Body.String(), err)
		} else {
			if senv.Code == 0 {
				t.Errorf("status=%d sub_store must be rejected, got code=0 body=%q", status, sw.Body.String())
			}
			if senv.Msg != "账号被停用" {
				t.Errorf("status=%d sub_store msg = %q, want 账号被停用", status, senv.Msg)
			}
		}
	}
}
