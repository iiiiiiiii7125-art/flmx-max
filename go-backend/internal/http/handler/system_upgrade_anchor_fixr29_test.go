package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FIX-R2-9 面1（面板自升级第二锚按获胜线路分层，与脚本侧 R2-01-01
// 同律）：既有五例锚测试全部以直连为获胜线（seedAnchorEnv 钉
// directAlive=true）。本组补代理获胜形态——seedGitHubRoutesForTest
// 以 directAlive=false 播种后候选序为「代理排名＋直连殿后兜底」，
// 直连桩回 500 时首个代理候选即获胜线。

func seedAnchorEnvProxyWinner(t *testing.T, direct *httptest.Server, proxies ...string) {
	t.Helper()
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	originalBase := systemUpgradeReleaseBaseURL
	systemUpgradeReleaseBaseURL = direct.URL
	t.Cleanup(func() { systemUpgradeReleaseBaseURL = originalBase })
	seedGitHubRoutesForTest(t, false, proxies)
}

// 代理获胜＋异路全灭（复核不可用）→ fail-closed 硬中止并点名，
// 不得像直连获胜那样降级单锚放行：此时定版依据只剩获胜代理自给
// 的（资产，.sha256）自洽对，等于接受代理自证。
func TestDownloadReleaseAssetProxyWinnerNoCorroborationAborts(t *testing.T) {
	srvDirect := anchorStubServer(t, anchorTestCompose, "", http.StatusInternalServerError)
	srvProxy := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	srvDead := anchorStubServer(t, anchorTestCompose, "", http.StatusInternalServerError)
	seedAnchorEnvProxyWinner(t, srvDirect, srvProxy.URL, srvDead.URL)

	h := &Handler{}
	body, _, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err == nil {
		t.Fatalf("downloadReleaseAsset() succeeded with body %q, want fail-closed abort when a proxy wins and no route corroborates", body)
	}
	if !strings.Contains(err.Error(), "获胜线路为代理") || !strings.Contains(err.Error(), "已中止升级") {
		t.Fatalf("error = %v, want named proxy-winner anchor abort", err)
	}
	if body != nil {
		t.Fatalf("body = %q, want nil on abort", body)
	}
}

// 代理获胜＋异路给出同一合法校验值 → 复核通过、正常定版。
func TestDownloadReleaseAssetProxyWinnerCorroborated(t *testing.T) {
	srvDirect := anchorStubServer(t, anchorTestCompose, "", http.StatusInternalServerError)
	srvProxy := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	srvSecond := anchorStubServer(t, anchorTestCompose, "", http.StatusOK)
	seedAnchorEnvProxyWinner(t, srvDirect, srvProxy.URL, srvSecond.URL)

	h := &Handler{}
	body, note, err := h.downloadReleaseAsset("2.1.9", "docker-compose-v4.yml")
	if err != nil {
		t.Fatalf("downloadReleaseAsset() error = %v, want corroborated success via second proxy", err)
	}
	if note != "" {
		t.Fatalf("anchor note = %q, want empty (second route corroborated)", note)
	}
	if string(body) != string(anchorTestCompose) {
		t.Fatalf("body mismatch: %q", body)
	}
}
