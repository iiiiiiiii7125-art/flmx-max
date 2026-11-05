package handler

import (
	"crypto/md5"
	"encoding/hex"
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

// kr4LegacyMD5Hex 仅供测试构造 legacy MD5 存储夹具（生产侧
// security.MD5 已随 R-MD5 sunset 整删）。
func kr4LegacyMD5Hex(plain string) string {
	sum := md5.Sum([]byte(plain))
	return hex.EncodeToString(sum[:])
}

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
//     【R-MD5 sunset 取代（2026-11-05 发版批）：透明 rehash 整体退役
//     ——legacy 存储不再验密放行，登录面把账户收口为与导入面相同的
//     终态（凭据作废＋停用＋推进 password_changed_at 吊销会话）。
//     下方两条 002 用例已按 sunset 语义改写并更名，断言强度不降；
//     「真实改密仍推进并踢旧会话」一条不受影响、原样保留。】
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
	legacyHash := kr4LegacyMD5Hex(password)
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

// ---- R4B01-002 → R-MD5 sunset（改写，见文件头取代注记） ----

// sub_store 面 sunset：legacy 存储携正确口令拉取也鉴权失败，且账户
// 被收口为导入面终态——存储换成不验旧口令的随机 bcrypt、status=0、
// password_changed_at 推进、既有面板会话被吊销。
func TestR4B01_SubStoreLegacySunsetAppliesImportEndState(t *testing.T) {
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
	// sunset 口径：鉴权不得通过（成功才有 subscription-userinfo 头，
	// 失败为 JSON 信封错误）。
	if hdr := w.Header().Get("subscription-userinfo"); hdr != "" {
		t.Fatalf("legacy sub_store auth must not pass after sunset, got userinfo header %q body %s", hdr, w.Body.String())
	}

	after, err := r.GetUserByID(uid)
	if err != nil || after == nil {
		t.Fatalf("reload user after sub_store: %v", err)
	}
	if !strings.HasPrefix(after.Pwd, "$2") {
		t.Fatalf("sunset must replace stored hash with bcrypt, got prefix %.2s", after.Pwd)
	}
	if ok, legacy := security.VerifyPassword(after.Pwd, "legacy-password"); ok || legacy {
		t.Fatalf("sunset replacement must not verify the old password, got (%v,%v)", ok, legacy)
	}
	if after.Status != 0 {
		t.Fatalf("sunset must disable the account pending admin reset, status=%d", after.Status)
	}
	if after.PasswordChangedAt <= pca {
		t.Fatalf("sunset is a credential invalidation and must advance password_changed_at: before=%d after=%d", pca, after.PasswordChangedAt)
	}
	if body := probe(oldToken); !strings.Contains(body, `"code":401`) {
		t.Fatalf("pre-existing panel session must be revoked by sunset, got %s", body)
	}
}

// login 面 sunset：legacy 存储携正确口令登录返回与错密相同的通用
// 错误、不签发 token，账户同被收口为导入面终态；错密尝试对另一个
// legacy 账户触发同一处置（处置只依赖存储形态）。
func TestR4B01_LoginLegacySunsetAppliesImportEndState(t *testing.T) {
	h, r := setupD11LoginHandler(t)
	resetD11ForTest()
	uid, pca := kr4LegacyUser(t, r, "kr4loginuser", "legacy-password")

	probe := kr4SessionProbe(t, r)
	oldToken, err := auth.GenerateTokenAt(uid, "kr4loginuser", 1, "test-secret", time.UnixMilli(pca+1000))
	if err != nil {
		t.Fatalf("generate pre-existing session token: %v", err)
	}
	if body := probe(oldToken); body != "reached" {
		t.Fatalf("pre-existing session must be valid before login, got %s", body)
	}

	login := func(username, password, remoteAddr string) envelope {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/login",
			strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = remoteAddr
		h.login(w, req)
		return decodeEnvelope(t, w)
	}

	env := login("kr4loginuser", "legacy-password", "198.51.100.78:4000")
	if env.Code == 0 || env.Msg != "账号或密码错误" {
		t.Fatalf("legacy login must fail with the generic credential error, got code=%d msg=%q", env.Code, env.Msg)
	}

	after, err := r.GetUserByID(uid)
	if err != nil || after == nil {
		t.Fatalf("reload user after login: %v", err)
	}
	if !strings.HasPrefix(after.Pwd, "$2") {
		t.Fatalf("sunset must replace stored hash with bcrypt, got prefix %.2s", after.Pwd)
	}
	if ok, legacy := security.VerifyPassword(after.Pwd, "legacy-password"); ok || legacy {
		t.Fatalf("sunset replacement must not verify the old password, got (%v,%v)", ok, legacy)
	}
	if after.Status != 0 {
		t.Fatalf("sunset must disable the account pending admin reset, status=%d", after.Status)
	}
	if after.PasswordChangedAt <= pca {
		t.Fatalf("sunset is a credential invalidation and must advance password_changed_at: before=%d after=%d", pca, after.PasswordChangedAt)
	}
	if body := probe(oldToken); !strings.Contains(body, `"code":401`) {
		t.Fatalf("pre-existing panel session must be revoked by sunset, got %s", body)
	}

	// 错密尝试同样触发处置：终态与本人首登一致、不可区分。
	uid2, _ := kr4LegacyUser(t, r, "kr4loginuser2", "legacy-password-2")
	env = login("kr4loginuser2", "definitely-wrong", "198.51.100.79:4000")
	if env.Code == 0 || env.Msg != "账号或密码错误" {
		t.Fatalf("wrong-password legacy login must fail generically, got code=%d msg=%q", env.Code, env.Msg)
	}
	after2, err := r.GetUserByID(uid2)
	if err != nil || after2 == nil {
		t.Fatalf("reload second user: %v", err)
	}
	if after2.Status != 0 || security.IsLegacyPasswordHash(after2.Pwd) {
		t.Fatalf("wrong-password attempt must sunset the legacy account too: status=%d legacy=%v", after2.Status, security.IsLegacyPasswordHash(after2.Pwd))
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
