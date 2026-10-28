package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
)

func resetKernelVersionCache(t *testing.T) {
	t.Helper()
	kernelVersionCache.Lock()
	kernelVersionCache.at = map[string]time.Time{}
	kernelVersionCache.items = map[string][]kernelVersionItem{}
	kernelVersionCache.Unlock()
}

func stubReleasesAPI(t *testing.T, payload string) {
	t.Helper()
	// C-02：内核版本列表走加固 fetch 助手后，本地测试服务器须配
	// allowlist 才放行（与生产内网代理同口径）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
}

// 版本列表改为流式解码后，响应体积再大也不得失败：sing-box 的 releases
// 负载实测 10MiB 且持续膨胀，旧的固定上限（4MiB、乃至 32MiB）迟早被突破。
// 本用例负载约 36MiB，超过历史上出现过的任何上限。
func TestKernelReleaseVersionsHandlesLargePayload(t *testing.T) {
	big := strings.Repeat("a", 12<<20)
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 3; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"tag_name":"v1.14.%d","draft":false,"prerelease":false,"published_at":"2026-01-0%dT00:00:00Z","body":%q}`, i, i+1, big)
	}
	sb.WriteString("]")
	stubReleasesAPI(t, sb.String())
	resetKernelVersionCache(t)

	info, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	items, err := kernelReleaseVersions(info)
	if err != nil {
		t.Fatalf("kernelReleaseVersions: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
}

// 流式解码凑够 15 条即停：上游返回再多版本，下拉也只取最近 15 条。
func TestKernelReleaseVersionsStopsAt15(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 20; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"tag_name":"v1.13.%d","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z"}`, i)
	}
	sb.WriteString("]")
	stubReleasesAPI(t, sb.String())
	resetKernelVersionCache(t)

	info, ok := kernel.Lookup(kernel.TypeXray)
	if !ok {
		t.Fatal("xray info missing")
	}
	items, err := kernelReleaseVersions(info)
	if err != nil {
		t.Fatalf("kernelReleaseVersions: %v", err)
	}
	if len(items) != 15 {
		t.Fatalf("items = %d, want 15", len(items))
	}
}
