package socket

import (
	"testing"
)

// 回归测试：S-02 跳转白名单曾遗漏 githubusercontent.com，导致 GitHub
// release 下载的 302 跳转（release-assets.githubusercontent.com）被拒，
// 面板内节点升级全部失败。此测试锁死官方域名集合。
func TestIsGitHubDownloadHost(t *testing.T) {
	allowed := []string{
		"github.com",
		"api.github.com",
		"codeload.github.com",
		"githubusercontent.com",
		"release-assets.githubusercontent.com",
		"objects.githubusercontent.com",
		"GITHUB.COM",
		"Release-Assets.GitHubUserContent.com",
	}
	for _, h := range allowed {
		if !isGitHubDownloadHost(h) {
			t.Errorf("isGitHubDownloadHost(%q) = false, want true", h)
		}
	}
	rejected := []string{
		"evil.com",
		"github.com.evil.com",
		"evilgithub.com",
		"notgithubusercontent.com",
		"githubusercontent.com.evil.com",
		"",
	}
	for _, h := range rejected {
		if isGitHubDownloadHost(h) {
			t.Errorf("isGitHubDownloadHost(%q) = true, want false", h)
		}
	}
}

// KU4 (1.7.7): TestResolveAgentUpgradeProxy is deleted together
// with the function — the panel-pushed proxyUrl and the local
// config.json "proxy" fallback are both retired as routing inputs;
// the self-upgrade's route is probe-selected from the built-in
// candidate set (see ku4_upgrade_routes_test.go).
