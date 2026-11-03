package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// C1-02 钉格（1.9.5 发版后审计）：open_api/sub/ 此前是全部凭据面
// 中唯一无限流的端点（修复前第 61 次请求仍直达查库/渲染＝红）。
// 现按源 IP 60 次/分钟（与 gh 镜像/flow 同量级）：同 IP 第 61 次
// 起 429，异 IP 不受牵连，有效 token 在预算内正常 200。

func TestOpenSubscriptionRateLimitedPerIP(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "c102.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	token := strings.Repeat("a", 32)
	if err := r.DB().Create(&model.Subscription{Name: "c102", Token: token, Enabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{repo: r}

	pull := func(ip, tok string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+tok, nil)
		req.RemoteAddr = ip + ":40000"
		rec := httptest.NewRecorder()
		h.openSubscription(rec, req)
		return rec.Code
	}

	badToken := strings.Repeat("b", 32)
	for i := 1; i <= openSubMaxRequestsPerMinute; i++ {
		if code := pull("203.0.113.10", badToken); code != http.StatusNotFound {
			t.Fatalf("request %d within budget: want 404, got %d", i, code)
		}
	}
	// 预算耗尽：同 IP 连有效 token 也先被限流挡下（不查库不渲染）。
	if code := pull("203.0.113.10", token); code != http.StatusTooManyRequests {
		t.Fatalf("request beyond budget: want 429, got %d", code)
	}
	// 异 IP 预算独立：有效 token 正常服务。
	if code := pull("203.0.113.11", token); code != http.StatusOK {
		t.Fatalf("other IP must be served: want 200, got %d", code)
	}
}
