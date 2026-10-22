package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R1 顺手项（C 观察 1，V-23，不占号）：订阅抓取此前只能走 SecureFetch
// 的 50MB 全局上限，超限判定发生在整包下载之后；fetchSubscriptionInfo
// 自身已有 maxSubscriptionBytes（5MB）的领域上限，却无从在抓取层
// 生效。本测试钉死：调用点可传更小上限、抓取层即拒；默认上限路径
// 不受影响（防过修正对照）。

func TestR1B5SecureFetchWithSmallerBodyLimit(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	big := bytes.Repeat([]byte("a"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, _, err := secureFetchWithHeaderLimit(ctx, srv.URL, 1024); err == nil ||
		!strings.Contains(err.Error(), "response too large") {
		t.Fatalf("secureFetchWithHeaderLimit(1024) error = %v, want response too large", err)
	}
	// 对照：默认（全局）上限路径仍可取回同一响应。
	body, _, err := SecureFetchWithHeader(ctx, srv.URL)
	if err != nil || len(body) != len(big) {
		t.Fatalf("SecureFetchWithHeader default limit: body=%d err=%v, want %d bytes", len(body), err, len(big))
	}
	// 取值依据钉死：订阅上限必须小于全局上限。
	if maxSubscriptionBytes >= secureFetchMaxBody {
		t.Fatalf("maxSubscriptionBytes (%d) must be smaller than secureFetchMaxBody (%d)",
			maxSubscriptionBytes, secureFetchMaxBody)
	}
}

// TestR1B5FetchSubscriptionInfoRejectsOversized 走订阅真实入口：
// 超过 maxSubscriptionBytes 的源必须报错且不返回内容。
func TestR1B5FetchSubscriptionInfoRejectsOversized(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	big := bytes.Repeat([]byte("b"), maxSubscriptionBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body, _, err := fetchSubscriptionInfo(ctx, srv.URL)
	if err == nil {
		t.Fatalf("fetchSubscriptionInfo with %d bytes must fail", len(big))
	}
	if body != "" {
		t.Fatalf("oversized subscription must return no body, got %d bytes", len(body))
	}
}
