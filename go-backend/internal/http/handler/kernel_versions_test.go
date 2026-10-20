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

// 版本列表 JSON 体积超过旧的 4MiB 解码上限时不得再报 unexpected EOF：
// sing-box 的 releases 负载实测可达 10MiB（每版资产极多），旧上限必截断。
func TestKernelReleaseVersionsHandlesLargePayload(t *testing.T) {
	big := strings.Repeat("a", 3<<20)
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 3; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"tag_name":"v1.14.%d","draft":false,"prerelease":false,"published_at":"2026-01-0%dT00:00:00Z","body":%q}`, i, i+1, big)
	}
	sb.WriteString("]")
	payload := sb.String()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	old := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = old }()

	kernelVersionCache.Lock()
	kernelVersionCache.at = map[string]time.Time{}
	kernelVersionCache.items = map[string][]kernelVersionItem{}
	kernelVersionCache.Unlock()

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
