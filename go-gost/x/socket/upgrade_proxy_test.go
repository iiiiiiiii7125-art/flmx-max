package socket

import (
	"os"
	"path/filepath"
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

func TestResolveAgentUpgradeProxy(t *testing.T) {
	// 无 config.json 的目录中运行，避免开发机残留配置干扰
	dir := t.TempDir()
	t.Chdir(dir)

	if got := resolveAgentUpgradeProxy(""); got != "" {
		t.Errorf("无任何代理配置时应返回空, got %q", got)
	}
	if got := resolveAgentUpgradeProxy("https://proxy.example.com/"); got != "https://proxy.example.com" {
		t.Errorf("面板代理应去尾斜杠返回, got %q", got)
	}
	if got := resolveAgentUpgradeProxy("notaurl"); got != "" {
		t.Errorf("无 host 的代理应被忽略, got %q", got)
	}
	if got := resolveAgentUpgradeProxy("ftp://proxy.example.com"); got != "" {
		t.Errorf("非 http/https 代理应被忽略, got %q", got)
	}

	// config.json 落盘代理作为兜底，面板下发优先
	cfg := []byte("{\n  \"addr\": \"1.2.3.4:8080\",\n  \"secret\": \"x\",\n  \"proxy\": \"https://cfg.example.com\"\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	if got := resolveAgentUpgradeProxy(""); got != "https://cfg.example.com" {
		t.Errorf("应回退到 config.json 的 proxy, got %q", got)
	}
	if got := resolveAgentUpgradeProxy("https://panel.example.com"); got != "https://panel.example.com" {
		t.Errorf("面板下发应优先于 config.json, got %q", got)
	}
	if got := resolveAgentUpgradeProxy("::bad::"); got != "https://cfg.example.com" {
		t.Errorf("面板代理无效时应回退 config.json, got %q", got)
	}
}
