package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R18-B10-02：公开镜像端点不得把上游 Go 原始错误文本直出给未鉴权
// 调用方——拨号/TLS 细节会泄漏面板出口侧的运维信息（上游 URL、
// 解析结果、拨号错误），且与“未鉴权面错误不得原文直出”的既有纪律
//（respondInternalError / TestUnauthenticatedRawErrorGate）不一致。
// 上游失败时只回固定文案，原始错误只记服务端日志。
func TestR18B1002GithubMirrorUpstreamErrorNotLeaked(t *testing.T) {
	// L8-F-04：tag 查询走钉死拨号；本地桩须显式放行回环段，使失败
	// 模式保持“拨号被拒”而非 allowlist 拦截（否则旧代码下的错误
	// 文本不是 *url.Error，红证不成立）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	h, _ := setupBC01Test(t)

	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorDir = savedDir })

	// 上游基址指向已关闭的本地端口：Head 必以 connection refused
	// 失败，ghMirrorLatestTag 的 *url.Error 原样进入回执（旧形态）。
	savedBases := ghMirrorBases
	ghMirrorBases = struct{ github, api, raw string }{"http://127.0.0.1:1", "http://127.0.0.1:1", "http://127.0.0.1:1"}
	t.Cleanup(func() { ghMirrorBases = savedBases })

	// 排空该仓库的 tag 缓存，避免他例留下的新鲜缓存让本次查询
	// 不走上游（那样旧代码也不会泄漏，红证失效）。
	const repo = "iiiiiiiii7125-art/flmx-max"
	ghMirrorTagMu.Lock()
	delete(ghMirrorTags, strings.ToLower(repo))
	ghMirrorTagMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/gh/https://github.com/"+repo+"/releases/latest", nil)
	// 唯一客户端 IP：独占限流桶，不受他例共享桶水位影响。
	req.RemoteAddr = "192.0.2.55:1234"
	rec := httptest.NewRecorder()
	h.githubMirror(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream-down tag lookup must be HTTP 502, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leaked := range []string{"dial tcp", "127.0.0.1:1", "connection refused", "Head \"http"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("upstream raw error must not leak to unauthenticated caller: body contains %q (full body %q)", leaked, body)
		}
	}
	if !strings.Contains(body, "GitHub 上游暂时不可达") {
		t.Fatalf("upstream-down response must carry the fixed wording, got body=%q", body)
	}
}
