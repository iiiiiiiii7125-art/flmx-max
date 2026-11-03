package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
)

// C1-03 钉格（1.9.5 发版后审计）：sub_store 此前只有含 IP 的两桶，
// 轮换源 IP 即可对单账号线性放大口令猜测预算（修复前第 31 次跨
// IP 尝试仍直达鉴权＝红）。现与 login 共用用户名桶：单账号全局
// 30 次/10 分钟触发后，本面硬拒 429——与来源 IP 无关、连正确
// 口令也先挡下（预算优先于单次鉴权）；其他用户名不受牵连。

func TestSubStoreUsernameBucketCapsRotatingIPSpray(t *testing.T) {
	resetSubStoreLimiterForTest()
	resetLoginLimitersForTest()
	h := newSubStoreTestHandler(t)

	const username = "c103victim"
	const password = "c103-correct-password"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateUser(username, hash, 1, now+86400000, 100, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}

	attempt := func(user, pwd string, ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
			strings.NewReader(fmt.Sprintf(`{"user":%q,"password":%q,"tunnel":"-1"}`, user, pwd)))
		req.RemoteAddr = ip + ":40000"
		rec := httptest.NewRecorder()
		h.openAPISubStore(rec, req)
		return decodeEnvelopeCode(t, rec.Body.Bytes())
	}

	// 30 次错误口令、每次换源 IP：IP 两桶永不触发（每 IP 仅 1 次），
	// 用户名桶计数至阈值内——全部走完鉴权、回鉴权失败而非 429。
	for i := 1; i <= usernameRateLimitMaxAttempts; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i)
		if code := attempt(username, "wrong-password", ip); code == 429 {
			t.Fatalf("attempt %d within username budget must reach auth, got 429", i)
		}
	}
	// 第 31 次（新 IP）：用户名桶触发，本面硬拒 429。
	if code := attempt(username, "wrong-password", "198.51.100.250"); code != 429 {
		t.Fatalf("attempt beyond username budget: want 429, got %d", code)
	}
	// 触发期间连正确口令也先被预算挡下（全局预算优先于单次鉴权）。
	if code := attempt(username, password, "198.51.100.251"); code != 429 {
		t.Fatalf("correct password while triggered: want 429, got %d", code)
	}
	// 其他用户名不受牵连：照常走鉴权（回鉴权失败，非 429）。
	if code := attempt("c103bystander", "whatever", "198.51.100.252"); code == 429 {
		t.Fatalf("other username must not be username-bucket limited, got 429")
	}
}
