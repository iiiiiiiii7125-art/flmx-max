package handler

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// L8-F-02 第二锚测试组：面板自升级资产的校验值必须经与获胜线路
// 不同的另一条线路复核——不一致硬失败、异路全灭降级点名、单线
// 路环境降级点名。桩形态与既有 download 测试一致：直连基址指向
// srvA，代理前缀指向 srvB/srvC（代理 URL = 前缀 + "/" + 直连
// URL，桩按路径后缀分发 .sha256 / 本体）。

var anchorTestCompose = []byte("services:\n  backend:\n    image: example/backend:1\n  frontend:\n    image: example/frontend:1\n")

func anchorHash(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// anchorStubServer 起一个按路径后缀分发本体/.sha256 的桩服务：
// shaContent 为 .sha256 的响应内容（"" 表示用本体的真实哈希），
// status 非 200 时全路径回该状态码（模拟线路不可用）。
func anchorStubServer(t *testing.T, body []byte, shaContent string, status int) *httptest.Server {
	t.Helper()
	if shaContent == "" {
		shaContent = anchorHash(body) + "  docker-compose-v4.yml\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(shaContent))
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedAnchorEnv(t *testing.T, direct *httptest.Server, proxies ...string) {
	t.Helper()
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	originalBase := systemUpgradeReleaseBaseURL
	systemUpgradeReleaseBaseURL = direct.URL
	t.Cleanup(func() { systemUpgradeReleaseBaseURL = originalBase })
	seedGitHubRoutesForTest(t, true, proxies)
}

func TestDownloadReleaseAssetSecondAnchorCorroborated(t *testing.T) {
	srvA := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	srvB := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	seedAnchorEnv(t, srvA, srvB.URL)

	h := &Handler{}
	body, note, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want corroborated success", err)
	}
	if note != "" {
		t.Fatalf("anchor note = %q, want empty (second route corroborated)", note)
	}
	if string(body) != string(anchorTestCompose) {
		t.Fatalf("body mismatch: %q", body)
	}
}

func TestDownloadReleaseAssetSecondAnchorMismatchAborts(t *testing.T) {
	other := []byte("services:\n  backend:\n    image: evil/backend:1\n  frontend:\n    image: evil/frontend:1\n")
	srvA := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	// 异路给出的是另一个本体的合法哈希（单镜像失陷自洽篡改形态）。
	srvB := anchorStubServer(t, other, "", http.StatusOK)
	seedAnchorEnv(t, srvA, srvB.URL)

	h := &Handler{}
	body, _, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err == nil {
		t.Fatalf("downloadReleaseAsset() succeeded with body %q, want hard abort on anchor mismatch", body)
	}
	if !strings.Contains(err.Error(), "不一致") || !strings.Contains(err.Error(), "已中止升级") {
		t.Fatalf("error = %v, want named anchor-mismatch abort", err)
	}
	if body != nil {
		t.Fatalf("body = %q, want nil on abort", body)
	}
}

func TestDownloadReleaseAssetSecondAnchorDegradesWhenSecondRouteDead(t *testing.T) {
	srvA := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	srvB := anchorStubServer(t, anchorTestCompose, "", http.StatusInternalServerError)
	seedAnchorEnv(t, srvA, srvB.URL)

	h := &Handler{}
	body, note, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want degraded single-anchor success", err)
	}
	if string(body) != string(anchorTestCompose) {
		t.Fatalf("body mismatch: %q", body)
	}
	if !strings.Contains(note, "跨线路锚复核已降级") || !strings.Contains(note, "按单锚继续") {
		t.Fatalf("note = %q, want named single-anchor degradation", note)
	}
}

func TestDownloadReleaseAssetSecondAnchorDegradesSingleRoute(t *testing.T) {
	srvA := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	seedAnchorEnv(t, srvA) // 直连-only：候选集只有一条线路

	h := &Handler{}
	_, note, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want success", err)
	}
	if !strings.Contains(note, "跨线路锚复核已降级") {
		t.Fatalf("note = %q, want named degradation for single-route environment", note)
	}
}

func TestDownloadReleaseAssetSecondAnchorSkipsGarbageRoute(t *testing.T) {
	srvA := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	srvB := anchorStubServer(t, anchorTestCompose, "garbage-not-a-hash\n", http.StatusOK)
	srvC := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	seedAnchorEnv(t, srvA, srvB.URL, srvC.URL)

	h := &Handler{}
	_, note, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want success via third route", err)
	}
	if note != "" {
		t.Fatalf("note = %q, want empty (garbage route skipped, next route corroborated)", note)
	}
}

func TestValidateComposeAsset(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"valid", "services:\n  backend:\n    image: x\n  frontend:\n    image: y\n", ""},
		{"valid with postgres", "services:\n  backend:\n    image: x\n  postgres:\n    image: p\n  frontend:\n    image: y\n", ""},
		{"broken yaml", "services: [unclosed\n", "不是合法 YAML"},
		{"root not mapping", "- just\n- a\n- list\n", "根节点不是 mapping"},
		{"no services", "name: only\n", "缺少 services"},
		{"empty services", "services: {}\n", "缺少 services"},
		{"missing frontend", "services:\n  backend:\n    image: x\n", "\"frontend\""},
		{"missing backend", "services:\n  frontend:\n    image: y\n", "\"backend\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateComposeAsset([]byte(tc.data))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateComposeAsset() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateComposeAsset() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestReplaceComposeRejectsInvalidAssetKeepsOld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	old := []byte("services:\n  backend:\n    image: old/backend:1\n  frontend:\n    image: old/frontend:1\n")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	exec := &systemUpgradeExecutor{}

	if err := exec.replaceCompose(path, []byte("services:\n  backend:\n    image: evil/backend:1\n")); err == nil {
		t.Fatal("replaceCompose() accepted an asset missing the frontend service")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("compose on disk = %q, want untouched old content %q", got, old)
	}

	valid := []byte("services:\n  backend:\n    image: new/backend:2\n  frontend:\n    image: new/frontend:2\n")
	if err := exec.replaceCompose(path, valid); err != nil {
		t.Fatalf("replaceCompose(valid) = %v, want nil", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "new/backend:2") {
		t.Fatalf("compose on disk = %q, want new asset landed", got)
	}
}
