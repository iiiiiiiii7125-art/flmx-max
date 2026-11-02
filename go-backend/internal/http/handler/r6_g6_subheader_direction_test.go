package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// ─── R6-G6：Subscription-Userinfo 头 upload/download 数值钉死门 ───
// VERIFY-subheader.md 定论：buildSubscriptionHeader 旧形态靠两层反序
// 抵消才正确（调用点传 (OutFlow, InFlow) 给形参 (upload, download)，
// 函数体 Sprintf 又以 (download, upload) 输出）。G6 已把两层同改归正：
// 形参序即计量方向——InFlow = 客户端→远端字节 = 用户上传（upload=），
// OutFlow = 远端→客户端字节 = 用户下载（download=），与 flow_upload_batch.go
// 的 N-01 方向钉死同口径。本测试把整头四值逐字钉死：任一层再被单独
// 「顺手修正」都会让 upload/download 静默翻转并在此变红。
// 取值纪律：两面 in_flow ≠ out_flow 且四值互不相同，任一翻转必可见。

func TestR6G6BuildSubscriptionHeaderParamOrder(t *testing.T) {
	got := buildSubscriptionHeader(11, 22, 33, 44)
	want := "upload=11; download=22; total=33; expire=44"
	if got != want {
		t.Errorf("buildSubscriptionHeader(11, 22, 33, 44) = %q, want %q", got, want)
	}
}

func TestR6G6SubStoreHeaderDirectionPinned(t *testing.T) {
	resetSubStoreLimiterForTest()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	const password = "r6g6-probe-pass"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	now := time.Now().UnixMilli()

	// user 面：InFlow=111（上传）、OutFlow=222（下载）、Flow=100GiB。
	const userExpMs = int64(1893456000000) // 2030-01-01T00:00:00Z，固定值便于整头逐字断言
	userID, err := r.CreateUser("r6g6user", hash, 1, userExpMs, 100, 1, 10, 1, 0, now, 0)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user SET in_flow = 111, out_flow = 222 WHERE id = ?`, userID).Error; err != nil {
		t.Fatalf("set user flows: %v", err)
	}

	// UT 面：InFlow=333（上传）、OutFlow=444（下载）、Flow=3GiB。
	const tunnelID = int64(7100)
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(?, 'r6g6-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, tunnelID, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	const utExpMs = int64(1893542400000) // 2030-01-02T00:00:00Z
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, ?, 10, 3, 0, 333, 444, 1, ?, 1)`, userID, tunnelID, utExpMs).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
	var utID int64
	if err := r.DB().Raw(`SELECT id FROM user_tunnel WHERE user_id = ? AND tunnel_id = ?`, userID, tunnelID).Scan(&utID).Error; err != nil || utID <= 0 {
		t.Fatalf("lookup user_tunnel id: id=%d err=%v", utID, err)
	}

	call := func(tunnelArg string, ipOctet int) (string, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store",
			strings.NewReader(`{"user":`+jsonQuote("r6g6user")+`,"password":`+jsonQuote(password)+`,"tunnel":`+jsonQuote(tunnelArg)+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", ipOctet)
		w := httptest.NewRecorder()
		h.openAPISubStore(w, req)
		header := w.Header().Get("subscription-userinfo")
		if header == "" {
			t.Fatalf("sub_store tunnel=%s: no subscription-userinfo header, body=%q", tunnelArg, w.Body.String())
		}
		return header, w.Body.String()
	}

	// user 面（tunnel="-1"）：upload=InFlow=111、download=OutFlow=222。
	wantUser := "upload=111; download=222; total=107374182400; expire=" + strconv.FormatInt(userExpMs/1000, 10)
	gotHeader, gotBody := call("-1", 10)
	t.Logf("user face header: %s", gotHeader)
	if gotHeader != wantUser {
		t.Errorf("user face header = %q, want %q", gotHeader, wantUser)
	}
	if gotBody != wantUser {
		t.Errorf("user face body = %q, want %q", gotBody, wantUser)
	}

	// UT 面（tunnel=user_tunnel 行 id）：upload=InFlow=333、download=OutFlow=444。
	wantUT := "upload=333; download=444; total=3221225472; expire=" + strconv.FormatInt(utExpMs/1000, 10)
	gotHeader, gotBody = call(strconv.FormatInt(utID, 10), 50)
	t.Logf("UT face header: %s", gotHeader)
	if gotHeader != wantUT {
		t.Errorf("UT face header = %q, want %q", gotHeader, wantUT)
	}
	if gotBody != wantUT {
		t.Errorf("UT face body = %q, want %q", gotBody, wantUT)
	}
}
