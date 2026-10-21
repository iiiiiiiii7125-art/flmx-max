package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FIX-A #1（A8-01）：凭据不得写入后端日志。
// RequestLog / Recover 记录 r.URL.Path，而订阅 token 就在路径里
// （/api/v1/open_api/sub/{token}）。修复前日志原文含 token → FAIL。

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	fn()
	return buf.String()
}

func TestRequestLogRedactsSubscriptionToken(t *testing.T) {
	const token = "SECRETTOKEN1234567890abcdef"
	h := RequestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	out := captureLog(t, func() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+token, nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	})
	if strings.Contains(out, token) {
		t.Fatalf("request log leaked subscription token: %q", out)
	}
	if !strings.Contains(out, "/api/v1/open_api/sub/") {
		t.Fatalf("request log should keep the route template, got: %q", out)
	}
	// 普通路径保持原样可读。
	out = captureLog(t, func() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/node/list", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	})
	if !strings.Contains(out, "/api/v1/node/list") {
		t.Fatalf("ordinary path must be logged verbatim, got: %q", out)
	}
}

func TestRecoverLogRedactsSubscriptionToken(t *testing.T) {
	const token = "SECRETTOKEN1234567890abcdef"
	h := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	out := captureLog(t, func() {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+token, nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	})
	if strings.Contains(out, token) {
		t.Fatalf("panic log leaked subscription token: %q", out)
	}
}

// FIX-A #1/#2/#3 的部署面静态回归：nginx 凭据 location 必须关 access_log，
// CI 的 UPX 安装步必须做 sha256 校验。文件缺失（裁剪检出）时跳过。
func repoRootFile(t *testing.T, rel string) (string, bool) {
	t.Helper()
	p := filepath.Join("..", "..", "..", "..", filepath.FromSlash(rel))
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// nginxLocationBlocks 按 location 标记粗切配置块（块到下一个 location 为止）。
func nginxLocationBlocks(conf string) map[string]string {
	blocks := map[string]string{}
	idx := 0
	for {
		i := strings.Index(conf[idx:], "location")
		if i < 0 {
			break
		}
		start := idx + i
		next := strings.Index(conf[start+len("location"):], "location")
		end := len(conf)
		if next >= 0 {
			end = start + len("location") + next
		}
		headerEnd := strings.Index(conf[start:], "{")
		if headerEnd < 0 {
			break
		}
		header := strings.TrimSpace(conf[start : start+headerEnd])
		blocks[header] = conf[start:end]
		idx = end
	}
	return blocks
}

func TestNginxCredentialLocationsDisableAccessLog(t *testing.T) {
	conf, ok := repoRootFile(t, "vite-frontend/nginx.conf")
	if !ok {
		t.Skip("nginx.conf not present in this checkout")
	}
	blocks := nginxLocationBlocks(conf)
	wantOff := []string{
		"location /system-info",                 // D2-01：WS 握手 ?secret=<JWT/节点 secret>
		"location /flow/upload",                 // A8-01：/flow/* ?secret=<节点 secret>
		"location /flow/config",                 // A8-01：同上
		"location ^~ /api/v1/open_api/sub/",     // A8-01：订阅 token 在路径中
		"location = /api/v1/open_api/sub_store", // 既有先例，防回退
	}
	for _, header := range wantOff {
		block, found := blocks[header]
		if !found {
			t.Fatalf("nginx location %q missing (want a dedicated block with access_log off)", header)
		}
		if !strings.Contains(block, "access_log off") {
			t.Fatalf("nginx location %q does not disable access_log; credentials would be logged", header)
		}
	}
}

func TestCIWorkflowVerifiesUPXChecksum(t *testing.T) {
	wf, ok := repoRootFile(t, ".github/workflows/docker-build.yml")
	if !ok {
		t.Skip("workflow not present in this checkout")
	}
	step := strings.Index(wf, "Install UPX")
	if step < 0 {
		t.Fatal("Install UPX step not found")
	}
	rest := wf[step:]
	end := strings.Index(rest, "- name:")
	if end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "sha256sum -c") {
		t.Fatalf("Install UPX step performs no sha256 verification:\n%s", rest)
	}
	if !strings.Contains(rest, "UPX_SHA256") {
		t.Fatalf("Install UPX step does not pin UPX_SHA256:\n%s", rest)
	}
}
