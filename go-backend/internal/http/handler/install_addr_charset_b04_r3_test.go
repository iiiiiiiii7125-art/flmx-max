package handler

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R3 B04-001 回归：安装命令的面板地址与 GitHub 代理地址此前只过结构
// 字符集（@ # , / ? 空白/控制），; $() `` | & ' 等 shell 元字符可原样
// 进入无引号拼入、以 root 执行的安装命令。修复：IsValidNodeAddress
// 落 LDH 字符集门（写面与生成面共同祖先）、网站配置 ip 写面先拒、
// github_proxy_url 写面完整校验 + getGithubProxyConfig 消费面兜底。

var shellMetaAddrSamples = []string{
	"x.com;id",
	"x.com$(id)",
	"x.com`id`",
	"x.com|id",
	"x.com&id",
	"x.com';id;'",
	`x.com"id"`,
	`x.com\id`,
	"x.com(id)",
	"x.com<id>",
	"x.com>id",
	"x.com{id}",
	"x.com!id",
}

func TestB04R3NodeAddressCharsetGate(t *testing.T) {
	for _, addr := range shellMetaAddrSamples {
		if err := IsValidNodeAddress(addr); err == nil {
			t.Fatalf("IsValidNodeAddress(%q) must reject shell metacharacters", addr)
		}
		if err := validatePanelInstallAddr(addr); err == nil {
			t.Fatalf("validatePanelInstallAddr(%q) must reject shell metacharacters", addr)
		}
		if err := validatePanelInstallAddr("https://" + addr); err == nil {
			t.Fatalf("validatePanelInstallAddr(%q) must reject shell metacharacters behind a scheme", "https://"+addr)
		}
	}
	// 既有合法形态一概保留（含回环/RFC1918/括号 IPv6/zone id）。
	for _, addr := range []string{
		"panel.example.com",
		"panel.example.com:8443",
		"192.0.2.5",
		"192.0.2.5:8443",
		"127.0.0.1:6365",
		"10.0.0.8",
		"localhost",
		"localhost:6365",
		"[2001:db8::9]:8443",
		"2001:db8::9",
		"fe80::1%eth0",
	} {
		if err := IsValidNodeAddress(addr); err != nil {
			t.Fatalf("IsValidNodeAddress(%q) must stay valid: %v", addr, err)
		}
		if err := validatePanelInstallAddr(addr); err != nil {
			t.Fatalf("validatePanelInstallAddr(%q) must stay valid: %v", addr, err)
		}
	}
	for _, addr := range []string{"https://panel.example.com", "wss://panel.example.com:8443", "https://192.0.2.5:8443"} {
		if err := validatePanelInstallAddr(addr); err != nil {
			t.Fatalf("validatePanelInstallAddr(%q) must stay valid: %v", addr, err)
		}
	}
}

func TestB04R3ConfigWriteFacesRejectMetachars(t *testing.T) {
	// 面板地址写面（网站配置 ip）：元字符保存期即拒，合法形态放行。
	for _, v := range shellMetaAddrSamples {
		if _, err := normalizeAndValidateConfigValue("ip", v); err == nil {
			t.Fatalf("config ip=%q must be rejected at the write face", v)
		}
	}
	for _, v := range []string{"panel.example.com", "panel.example.com:8443", "https://panel.example.com:8443", "192.0.2.5:8443"} {
		if _, err := normalizeAndValidateConfigValue("ip", v); err != nil {
			t.Fatalf("config ip=%q must stay valid: %v", v, err)
		}
	}

	// GitHub 代理地址写面：Host 元字符、路径命令替换、用户信息、
	// 查询串形态全拒；前缀型合法地址（含路径前缀）放行。
	for _, v := range []string{
		"https://p.example.com;id",
		"p.example.com/$(id)",
		"https://p.example.com/$(id)",
		"https://p.example.com/a|b",
		"https://p.example.com/`id`",
		"https://p.example.com/a'b",
		"https://user:pass@p.example.com",
		"https://p.example.com/?a=1",
		"https://p.example.com/#frag",
		"ftp://p.example.com",
	} {
		if _, err := normalizeAndValidateConfigValue("github_proxy_url", v); err == nil {
			t.Fatalf("config github_proxy_url=%q must be rejected at the write face", v)
		}
	}
	for _, v := range []string{
		"",
		"https://ghfast.top",
		"gcode.hostcentral.cc",
		"https://proxy.example.com/gh-proxy",
		"http://192.0.2.9:8080",
		"https://proxy.example.com:8443/prefix/v2",
	} {
		if _, err := normalizeAndValidateConfigValue("github_proxy_url", v); err != nil {
			t.Fatalf("config github_proxy_url=%q must stay valid: %v", v, err)
		}
	}
}

// 消费面兜底：存量坏值（绕过写面落库）经 getGithubProxyConfig 必须
// 回落内置默认，不得进入安装命令；存量好值原样归一化下发。
func TestB04R3GithubProxyConfigBackstop(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if err := r.UpsertConfig("github_proxy_enabled", "true", now); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertConfig("github_proxy_url", "https://p.example.com;id", now); err != nil {
		t.Fatal(err)
	}
	enabled, proxyURL := h.getGithubProxyConfig()
	if !enabled {
		t.Fatal("proxy must stay enabled")
	}
	if proxyURL != defaultGithubProxyURL {
		t.Fatalf("hostile stored proxy URL must fall back to default, got %q", proxyURL)
	}

	if err := r.UpsertConfig("github_proxy_url", "proxy.example.com/gh-proxy/", now); err != nil {
		t.Fatal(err)
	}
	_, proxyURL = h.getGithubProxyConfig()
	if proxyURL != "https://proxy.example.com/gh-proxy" {
		t.Fatalf("legit stored proxy URL must pass through normalized, got %q", proxyURL)
	}
}
