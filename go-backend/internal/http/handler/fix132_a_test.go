package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustStoredHash(t *testing.T, r *repo.Repository, username string) string {
	t.Helper()
	u, err := r.GetUserByUsername(username)
	if err != nil || u == nil {
		t.Fatalf("get user %s: %v", username, err)
	}
	return u.Pwd
}

// FIX-A #4（A1-D2，P2）：密码登录的 requirePasswordChange、JWT 受限 claim、
// passkey 口径（isStoredDefaultPassword）三方必须一致——只由「存储口令
// 是否仍为种子默认口令」决定，与用户名是否保留 admin@user 无关。
// 修复前：handler.go 用「用户名或密码任一命中默认值字符串」现算标志，
// 已改密但保留默认用户名的管理员每次登录被误报 → 本测试 FAIL。
func TestLoginRequirePasswordChangeThreeWayConsistency(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	login := func(username, password, ip string) (bool, auth.Claims) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/login",
			strings.NewReader(`{"username":`+jsonQuote(username)+`,"password":`+jsonQuote(password)+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip
		h.login(w, req)
		env := decodeEnvelope(t, w)
		if env.Code != 0 {
			t.Fatalf("login %s must succeed, got code=%d msg=%q", username, env.Code, env.Msg)
		}
		var data struct {
			Token                 string `json:"token"`
			RequirePasswordChange bool   `json:"requirePasswordChange"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil || data.Token == "" {
			t.Fatalf("login data: err=%v data=%s", err, env.Data)
		}
		claims, err := auth.ParseClaims(data.Token, "test-secret")
		if err != nil {
			t.Fatalf("parse token: %v", err)
		}
		return data.RequirePasswordChange, claims
	}

	// 场景一：种子管理员（admin@user / admin@user，均未改）→ 三方都必须为 true。
	flag, claims := login("admin@user", "admin@user", "198.51.100.241:4000")
	if !flag {
		t.Fatal("seed default admin: requirePasswordChange must be true")
	}
	if !claims.PwdChangeRequired {
		t.Fatal("seed default admin: JWT must carry pwd_change_required")
	}
	if !isStoredDefaultPassword(0, mustStoredHash(t, r, "admin@user")) {
		t.Fatal("seed default admin: stored-hash basis must agree (true)")
	}

	// 管理员改密但保留默认用户名 admin@user。
	admin, err := r.GetUserByUsername("admin@user")
	if err != nil || admin == nil {
		t.Fatalf("get seed admin: %v", err)
	}
	newHash, err := security.HashPassword("changed-strong-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := r.UpdateUserPassword(admin.ID, newHash, time.Now().UnixMilli()); err != nil {
		t.Fatalf("update password: %v", err)
	}

	// 场景二：已改密 + 保留默认用户名 → 三方都必须为 false（修复前 flag=true）。
	flag, claims = login("admin@user", "changed-strong-password", "198.51.100.242:4000")
	if flag {
		t.Fatal("changed-password admin keeping default username: requirePasswordChange must be false (was permanently true before fix)")
	}
	if claims.PwdChangeRequired {
		t.Fatal("changed-password admin: JWT must not be restricted")
	}
	if isStoredDefaultPassword(0, mustStoredHash(t, r, "admin@user")) {
		t.Fatal("changed-password admin: stored-hash basis must agree (false)")
	}
}

// FIX-A #5（A1 P3）：sub_store 用户不存在时必须跑一次 dummy bcrypt，
// 与 login 的 K-001 同口径，消除用户名枚举时序差。
// 修复前不存在用户立即返回（毫秒级）→ 本测试以耗时下限断言 → FAIL。
func TestSubStoreNonexistentUserRunsDummyBcrypt(t *testing.T) {
	resetSubStoreLimiterForTest()
	h := newSubStoreTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
		strings.NewReader(`{"user":"definitely-not-a-user-fix132","password":"wrong-password","tunnel":"-1"}`))
	req.RemoteAddr = "127.0.0.9:5000"
	w := httptest.NewRecorder()
	start := time.Now()
	h.openAPISubStore(w, req)
	missing := time.Since(start)
	// bcrypt cost 10 一次约几十 ms；修复前该路径仅查库即返回（~ms 级）。
	if missing < 20*time.Millisecond {
		t.Fatalf("nonexistent-user path returned in %v; dummy bcrypt missing (username enumeration timing channel)", missing)
	}
}

// FIX-A #9（D6-05）：gh_mirror 限流桶必须清理过期桶——历史上累计
// 10000 个不同源 IP 后，新 IP 不得被 fail-closed 永久拒绝。
// 修复前桶满即拒且从不清理 → 本测试 FAIL。
func TestGhMirrorRateLimiterEvictsExpiredBuckets(t *testing.T) {
	ghMirrorRateMu.Lock()
	saved := ghMirrorRate
	savedMaxHosts := ghMirrorRateMaxHosts
	ghMirrorRate = make(map[string]*ghMirrorBucket, ghMirrorRateMaxHosts)
	now := time.Now()
	for i := 0; i < ghMirrorRateMaxHosts; i++ {
		ghMirrorRate["10.0."+strconv.Itoa(i/256)+"."+strconv.Itoa(i%256)] = &ghMirrorBucket{
			count: 1, windowEnd: now.Add(-time.Minute), // 全部已过期
		}
	}
	ghMirrorRateMu.Unlock()
	defer func() {
		ghMirrorRateMu.Lock()
		ghMirrorRate = saved
		ghMirrorRateMaxHosts = savedMaxHosts
		ghMirrorRateMu.Unlock()
	}()

	if !ghMirrorAllow("203.0.113.200") {
		t.Fatal("new IP rejected although all existing buckets are expired (limiter permanently saturated)")
	}
	ghMirrorRateMu.Lock()
	remaining := len(ghMirrorRate)
	ghMirrorRateMu.Unlock()
	if remaining > ghMirrorRateMaxHosts {
		t.Fatalf("bucket map grew beyond cap: %d > %d", remaining, ghMirrorRateMaxHosts)
	}
}
