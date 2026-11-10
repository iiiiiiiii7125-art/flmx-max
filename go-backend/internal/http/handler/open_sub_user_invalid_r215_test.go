package handler

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// A-02 (AUDIT-R215): ?user= 在场但非法时 fail-closed——解析失败或
// <=0 不得丢弃 userFilter、按裸 token 出全量，改为 200 空节点列表
// （与已签名用户零分配同口径）。合法有签名无分配 → 空列表；合法
// 有分配 → 过滤后列表；无 ?user= → 裸 token 全量不变。
func TestR215OpenSubscriptionInvalidUserFailClosed(t *testing.T) {
	h, r, ids := subFixture(t)
	token := strings.Repeat("7", 32)
	sub := createShareSub(t, r, &model.Subscription{
		Name: "a02", Kind: model.SubscriptionShare, Token: token,
		UUID: "uuid-a02", Password: "pw-a02", SourceAll: 1, Enabled: 1,
	})
	// 用户 2：有订阅分配，只分到 hk-1 的直连端点。
	if err := r.SetUserSubscriptions(2, []int64{sub.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetUserInboundAssignments(2, []repo.UserInboundAssignment{{InboundID: ids["hk-1"], RelayID: 0}}); err != nil {
		t.Fatal(err)
	}
	// 用户 3：有订阅分配，无任何入站分配。
	if err := r.SetUserSubscriptions(3, []int64{sub.ID}); err != nil {
		t.Fatal(err)
	}

	pullCount := func(url string) (int, int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		// 独立源 IP：openSubscription 按 IP 限流 60/分钟，本格
		// 不与同包其他拉取共用默认 192.0.2.1 的桶。
		req.RemoteAddr = "203.0.113.99:40000"
		rec := httptest.NewRecorder()
		h.openSubscription(rec, req)
		if rec.Code != http.StatusOK {
			return 0, rec.Code, rec.Body.String()
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
		if err != nil {
			t.Fatalf("pull %s: body not base64: %v", url, err)
		}
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				n++
			}
		}
		return n, rec.Code, ""
	}

	// 裸 token 全量不变（夹具自检：4 个入站）。
	if n, code, body := pullCount(fmt.Sprintf("/api/v1/open_api/sub/%s?format=base64", token)); n != 4 || code != 200 {
		t.Fatalf("bare token pull = %d nodes code=%d body=%s, want 4/200", n, code, body)
	}

	// 非法值：abc / 0 / -1，一律 200 空列表，不得出全量。
	for _, bad := range []string{"abc", "0", "-1", "1.5", ""} {
		url := fmt.Sprintf("/api/v1/open_api/sub/%s?format=base64&user=%s", token, bad)
		if bad == "" {
			// user= 空串等价于参数缺省（裸 token 口径），不在非法格内。
			continue
		}
		if n, code, body := pullCount(url); n != 0 || code != 200 {
			t.Fatalf("?user=%s pull = %d nodes code=%d body=%s, want 0/200", bad, n, code, body)
		}
	}

	// 合法有签名无分配 → 200 空列表（既有口径守卫）。
	sig3 := h.subURLSigForToken(token, 3)
	if n, code, body := pullCount(fmt.Sprintf("/api/v1/open_api/sub/%s?format=base64&user=3&sig=%s", token, sig3)); n != 0 || code != 200 {
		t.Fatalf("signed user without assignments pull = %d nodes code=%d body=%s, want 0/200", n, code, body)
	}

	// 合法有签名有分配 → 只出 hk-1 的 1 个直连端点。
	sig2 := h.subURLSigForToken(token, 2)
	if n, code, body := pullCount(fmt.Sprintf("/api/v1/open_api/sub/%s?format=base64&user=2&sig=%s", token, sig2)); n != 1 || code != 200 {
		t.Fatalf("signed user with hk-1 assignment pull = %d nodes code=%d body=%s, want 1/200", n, code, body)
	}

	// 合法值无签名仍 403（既有 IDOR 门不变）。
	if _, code, _ := pullCount(fmt.Sprintf("/api/v1/open_api/sub/%s?format=base64&user=2", token)); code != http.StatusForbidden {
		t.Fatalf("unsigned legal user pull code=%d, want 403", code)
	}
}
