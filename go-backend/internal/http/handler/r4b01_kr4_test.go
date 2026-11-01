package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// R4B01 KR4 回归门（Round 4 认证面）：
//   - R4B01-001：tightenedLoginIPs 无容量上限、无过期批量清扫，是未认证
//     可写内存表全集唯一漏网（其余六表均「上限+淘汰」）。修复后达上限
//     写入新键须先清扫过期条目、仍满淘汰到期最早者，映射恒有界；
//     紧缩判定语义（阈值 3/min、到期自动解除）不变。
//   - R4B01-002：legacy 哈希透明迁移（rehash）旧经 UpdateUserPassword
//     无条件推进 password_changed_at，sub_store 路径不签发 token、无从
//     补偿，一次只读拉取即吊销该用户全部既有面板会话。修复后两条
//     rehash 路径统一走 MigrateUserPasswordHash（只换哈希不动
//     password_changed_at）；真实改密仍须推进并踢掉旧会话。
//
// 红绿判据：001 的两条在修复前 tightenedLoginMaxEntries 与淘汰逻辑不
// 存在（编译期即红）；002 的两条在修复前 PasswordChangedAt 断言必红
// （rehash 后 pca 被推进到当前时刻、旧会话探针返回 401）。

// kr4SessionProbe 以仓库实时状态构造与生产同判据的 JWT 中间件探针：
// IatMs<=PasswordChangedAt 拒绝（信封 code 401），放行返回 "reached"。
func kr4SessionProbe(t *testing.T, r *repo.Repository) func(token string) string {
	t.Helper()
	state := func(userID int64) (*auth.UserAuthState, error) {
		u, err := r.GetUserByID(userID)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, fmt.Errorf("user %d not found", userID)
		}
		return &auth.UserAuthState{ID: u.ID, RoleID: u.RoleID, Status: u.Status, PasswordChangedAt: u.PasswordChangedAt}, nil
	}
	mw := middleware.JWT(middleware.AuthOptions{JWTSecret: "test-secret", GetUserAuthState: state})(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte("reached")) }))
	return func(token string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/package", nil)
		req.Header.Set("Authorization", token)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec.Body.String()
	}
}

// kr4LegacyUser 造一个 legacy MD5 哈希存量用户，并把
// password_changed_at 钉在过去某时刻 pca（毫秒），返回 (uid, pca)。
func kr4LegacyUser(t *testing.T, r *repo.Repository, username, password string) (int64, int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	legacyHash := security.MD5(password)
	uid, err := r.CreateUser(username, legacyHash, 1, now+86400000, 100, 1, 10, 1, 0, now)
	if err != nil {
		t.Fatalf("create legacy user: %v", err)
	}
	pca := now - 3600000
	if err := r.UpdateUserPassword(uid, legacyHash, pca); err != nil {
		t.Fatalf("pin password_changed_at fixture: %v", err)
	}
	u, err := r.GetUserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("reload legacy user: %v", err)
	}
	if u.PasswordChangedAt != pca || !security.IsLegacyPasswordHash(u.Pwd) {
		t.Fatalf("fixture broken: pca=%d legacy=%v", u.PasswordChangedAt, security.IsLegacyPasswordHash(u.Pwd))
	}
	return uid, pca
}

// ---- R4B01-001 ----

func kr4FillTightened(t *testing.T, entries map[string]time.Time) {
	t.Helper()
	tightenedLoginIPs.Lock()
	tightenedLoginIPs.m = entries
	tightenedLoginIPs.Unlock()
}

func kr4TightenedLen() int {
	tightenedLoginIPs.Lock()
	defer tightenedLoginIPs.Unlock()
	return len(tightenedLoginIPs.m)
}

func kr4TightenedHas(ip string) bool {
	tightenedLoginIPs.Lock()
	defer tightenedLoginIPs.Unlock()
	_, ok := tightenedLoginIPs.m[ip]
	return ok
}

// 达上限后写入新键：集合不得越过上限，到期最早的旧条目被淘汰，
// 新键与次旧条目保留。
func TestR4B01_TightenedSetBoundedEvictsEarliest(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}
	base := time.Now()
	entries := make(map[string]time.Time, tightenedLoginMaxEntries)
	earliestIP := ""
	for i := 0; i < tightenedLoginMaxEntries; i++ {
		ip := fmt.Sprintf("198.51.%d.%d", i/256, i%256)
		if i == 0 {
			earliestIP = ip
		}
		// 全部未过期，到期时刻随 i 递增：i=0 最早到期。
		entries[ip] = base.Add(time.Duration(i+1) * time.Second)
	}
	kr4FillTightened(t, entries)

	h.tightenIPRateLimit("203.0.113.250")

	if got := kr4TightenedLen(); got != tightenedLoginMaxEntries {
		t.Fatalf("tightened set must stay bounded at %d, got %d", tightenedLoginMaxEntries, got)
	}
	if kr4TightenedHas(earliestIP) {
		t.Fatalf("earliest-expiring entry %s must be evicted when full", earliestIP)
	}
	if !kr4TightenedHas("203.0.113.250") {
		t.Fatal("newly tightened IP must be present after eviction")
	}
	secondIP := fmt.Sprintf("198.51.%d.%d", 1/256, 1%256)
	if !kr4TightenedHas(secondIP) {
		t.Fatalf("second-earliest entry %s must survive single eviction", secondIP)
	}
}

// 达上限且混有过期条目时：写入新键先批量清扫过期条目腾位，
// 未过期条目不得被误淘汰。
func TestR4B01_TightenedSetSweepsExpiredWhenFull(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}
	base := time.Now()
	entries := make(map[string]time.Time, tightenedLoginMaxEntries)
	expiredCount := 0
	var firstActiveIP, lastActiveIP string
	for i := 0; i < tightenedLoginMaxEntries; i++ {
		ip := fmt.Sprintf("198.51.%d.%d", i/256, i%256)
		if i%2 == 0 {
			entries[ip] = base.Add(-time.Minute) // 已过期
			expiredCount++
		} else {
			entries[ip] = base.Add(tightenedIPRateLimitWindow)
			if firstActiveIP == "" {
				firstActiveIP = ip
			}
			lastActiveIP = ip
		}
	}
	kr4FillTightened(t, entries)

	h.tightenIPRateLimit("203.0.113.251")

	want := tightenedLoginMaxEntries - expiredCount + 1
	if got := kr4TightenedLen(); got != want {
		t.Fatalf("after expired sweep + insert want %d entries, got %d", want, got)
	}
	if !kr4TightenedHas(firstActiveIP) || !kr4TightenedHas(lastActiveIP) {
		t.Fatal("active entries must not be evicted while expired ones can be swept")
	}
	if !kr4TightenedHas("203.0.113.251") {
		t.Fatal("newly tightened IP must be present after sweep")
	}
}

// 紧缩判定语义不变：紧缩 IP 阈值为 3/min（普通 IP 为 10/min），
// 条目到期后自动解除。
func TestR4B01_TightenedJudgementUnchanged(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}
	now := time.Now()

	h.tightenIPRateLimit("203.0.113.7")
	if !isIPTightened("203.0.113.7") {
		t.Fatal("freshly tightened IP must report tightened")
	}
	for i := 1; i <= tightenedLoginMaxAttempts; i++ {
		if !globalLoginLimiter.allow("203.0.113.7", now) {
			t.Fatalf("tightened attempt %d must be allowed", i)
		}
	}
	if globalLoginLimiter.allow("203.0.113.7", now) {
		t.Fatal("tightened IP must be blocked beyond tightened threshold")
	}
	for i := 1; i <= tightenedLoginMaxAttempts+1; i++ {
		if !globalLoginLimiter.allow("203.0.113.8", now) {
			t.Fatalf("non-tightened attempt %d must be allowed (threshold is %d)", i, loginRateLimitMaxAttempts)
		}
	}

	// 到期条目：isIPTightened 惰性删除并返回 false（旧语义保留）。
	tightenedLoginIPs.Lock()
	tightenedLoginIPs.m["203.0.113.9"] = now.Add(-time.Minute)
	tightenedLoginIPs.Unlock()
	if isIPTightened("203.0.113.9") {
		t.Fatal("expired tightened entry must report not tightened")
	}
	if kr4TightenedHas("203.0.113.9") {
		t.Fatal("expired tightened entry must be lazily removed on lookup")
	}
}

// ---- R4B01-002 ----

// sub_store 透明 rehash：鉴权成功、哈希升级为 bcrypt，且
// password_changed_at 纹丝不动——rehash 之前签发的面板会话仍有效。
func TestR4B01_SubStoreRehashKeepsExistingSession(t *testing.T) {
	h, r := setupD11LoginHandler(t)
	resetD11ForTest()
	subStoreLimiter = &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}
	uid, pca := kr4LegacyUser(t, r, "kr4subuser", "legacy-password")

	probe := kr4SessionProbe(t, r)
	oldToken, err := auth.GenerateTokenAt(uid, "kr4subuser", 1, "test-secret", time.UnixMilli(pca+1000))
	if err != nil {
		t.Fatalf("generate pre-existing session token: %v", err)
	}
	if body := probe(oldToken); body != "reached" {
		t.Fatalf("pre-existing session must be valid before sub_store, got %s", body)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
		strings.NewReader(`{"user":"kr4subuser","password":"legacy-password","tunnel":"-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.77:4000"
	h.openAPISubStore(w, req)
	// 成功口径：响应头/body 为纯文本 subscription-userinfo（鉴权失败
	// 则是 JSON 信封错误），以此断言鉴权确已通过、流程走到用量下发。
	if hdr := w.Header().Get("subscription-userinfo"); !strings.HasPrefix(hdr, "upload=") {
		t.Fatalf("sub_store auth must succeed and emit subscription-userinfo, got header %q body %s", hdr, w.Body.String())
	}

	after, err := r.GetUserByID(uid)
	if err != nil || after == nil {
		t.Fatalf("reload user after sub_store: %v", err)
	}
	if !strings.HasPrefix(after.Pwd, "$2") {
		t.Fatalf("rehash must have upgraded stored hash to bcrypt, got prefix %.2s", after.Pwd)
	}
	if after.PasswordChangedAt != pca {
		t.Fatalf("transparent rehash must not advance password_changed_at: before=%d after=%d (修复前 FAIL)", pca, after.PasswordChangedAt)
	}
	if body := probe(oldToken); body != "reached" {
		t.Fatalf("pre-existing panel session must survive sub_store rehash, got %s (修复前 FAIL)", body)
	}
}

// login 透明 rehash：登录成功、token 口径不变（IatMs 严格大于
// password_changed_at、中间件放行），且 password_changed_at 不被
// 推进——rehash 前签发的既有会话同样不被附带吊销。
func TestR4B01_LoginRehashKeepsPasswordChangedAt(t *testing.T) {
	h, r := setupD11LoginHandler(t)
	resetD11ForTest()
	uid, pca := kr4LegacyUser(t, r, "kr4loginuser", "legacy-password")

	probe := kr4SessionProbe(t, r)
	oldToken, err := auth.GenerateTokenAt(uid, "kr4loginuser", 1, "test-secret", time.UnixMilli(pca+1000))
	if err != nil {
		t.Fatalf("generate pre-existing session token: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login",
		strings.NewReader(`{"username":"kr4loginuser","password":"legacy-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.78:4000"
	h.login(w, req)
	env := decodeEnvelope(t, w)
	if env.Code != 0 {
		t.Fatalf("legacy login must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	var loginData struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(env.Data, &loginData); err != nil || loginData.Token == "" {
		t.Fatalf("login token missing: err=%v data=%s", err, env.Data)
	}
	claims, err := auth.ParseClaims(loginData.Token, "test-secret")
	if err != nil {
		t.Fatalf("parse login token: %v", err)
	}
	if claims.IatMs <= pca {
		t.Fatalf("login token session shape changed: IatMs=%d must exceed PasswordChangedAt=%d", claims.IatMs, pca)
	}
	if body := probe(loginData.Token); body != "reached" {
		t.Fatalf("fresh login token must pass middleware, got %s", body)
	}

	after, err := r.GetUserByID(uid)
	if err != nil || after == nil {
		t.Fatalf("reload user after login: %v", err)
	}
	if !strings.HasPrefix(after.Pwd, "$2") {
		t.Fatalf("rehash must have upgraded stored hash to bcrypt, got prefix %.2s", after.Pwd)
	}
	if after.PasswordChangedAt != pca {
		t.Fatalf("transparent rehash must not advance password_changed_at: before=%d after=%d (修复前 FAIL)", pca, after.PasswordChangedAt)
	}
	if body := probe(oldToken); body != "reached" {
		t.Fatalf("pre-existing panel session must survive login rehash, got %s (修复前 FAIL)", body)
	}
}

// 真实改密路径（本人改密 updatePassword → UpdateUserNameAndPassword）
// 必须仍推进 password_changed_at 并吊销旧会话：防 002 的修复把真实
// 凭据变更的吊销语义一并阉割。
func TestR4B01_RealPasswordChangeStillRevokes(t *testing.T) {
	h, r := setupD11LoginHandler(t)
	resetD11ForTest()
	now := time.Now().UnixMilli()
	hash, err := security.HashPassword("old-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid, err := r.CreateUser("kr4chguser", hash, 1, now+86400000, 100, 1, 10, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 把初始 password_changed_at 钉到一小时前：旧 token 的 iat 取
	// pca+1000（仍在 7 天 TTL 内），改密推进到真实当前时刻必然越过
	// 它——推进量与本机执行速度无关（若以测试开始时刻为 pca 基准，
	// 改密链在一秒内跑完时新 pca 尚未越过 iat，断言会随机变红）。
	pca := now - 3600000
	if err := r.UpdateUserPassword(uid, hash, pca); err != nil {
		t.Fatalf("pin password_changed_at fixture: %v", err)
	}

	probe := kr4SessionProbe(t, r)
	oldToken, err := auth.GenerateTokenAt(uid, "kr4chguser", 1, "test-secret", time.UnixMilli(pca+1000))
	if err != nil {
		t.Fatalf("generate session token: %v", err)
	}
	if body := probe(oldToken); body != "reached" {
		t.Fatalf("session must be valid before password change, got %s", body)
	}

	req := claimsCtxReq(http.MethodPost, "/api/v1/user/updatePassword", "198.51.100.79:4000",
		`{"newUsername":"kr4chguser","currentPassword":"old-password","newPassword":"new-password-1","confirmPassword":"new-password-1"}`,
		uid, 1)
	w := httptest.NewRecorder()
	h.updatePassword(w, req)
	if env := decodeEnvelope(t, w); env.Code != 0 {
		t.Fatalf("real password change must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}

	after, err := r.GetUserByID(uid)
	if err != nil || after == nil {
		t.Fatalf("reload user after password change: %v", err)
	}
	if after.PasswordChangedAt <= pca {
		t.Fatalf("real password change must advance password_changed_at: before=%d after=%d", pca, after.PasswordChangedAt)
	}
	if body := probe(oldToken); !strings.Contains(body, `"code":401`) {
		t.Fatalf("old session must be revoked after real password change, got %s", body)
	}
}
