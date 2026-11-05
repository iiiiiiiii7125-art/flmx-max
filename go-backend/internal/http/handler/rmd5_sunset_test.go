package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/security"
)

// R-MD5 sunset 的非登录校验面：updatePassword 对 legacy 存储必须
// fail-closed——旧口令的知识在 sunset 后不构成任何凭据，持旧会话
// token 亦然；失败尝试不得改动存储（处置只发生在登录面）。
func TestRMd5UpdatePasswordLegacyFailsClosed(t *testing.T) {
	h, r := setupD11LoginHandler(t)
	resetD11ForTest()
	uid, pca := kr4LegacyUser(t, r, "rmd5selfuser", "legacy-password")
	before, err := r.GetUserByID(uid)
	if err != nil || before == nil {
		t.Fatalf("reload fixture user: %v", err)
	}

	req := claimsCtxReq(http.MethodPost, "/api/v1/user/updatePassword", "198.51.100.90:4000",
		`{"newUsername":"rmd5selfuser","currentPassword":"legacy-password","newPassword":"brand-new-pass-1","confirmPassword":"brand-new-pass-1"}`,
		uid, 1)
	w := httptest.NewRecorder()
	h.updatePassword(w, req)
	if env := decodeEnvelope(t, w); env.Code == 0 {
		t.Fatalf("legacy self-service password change must fail closed, got code=%d msg=%q", env.Code, env.Msg)
	}

	after, err := r.GetUserByID(uid)
	if err != nil || after == nil {
		t.Fatalf("reload user after updatePassword: %v", err)
	}
	if after.Pwd != before.Pwd || after.PasswordChangedAt != pca || after.Status != 1 {
		t.Fatalf("failed updatePassword must not mutate the account: pwdChanged=%v pca %d->%d status=%d",
			after.Pwd != before.Pwd, pca, after.PasswordChangedAt, after.Status)
	}
	if !security.IsLegacyPasswordHash(after.Pwd) {
		t.Fatal("stored hash must remain the original legacy fixture")
	}
}
