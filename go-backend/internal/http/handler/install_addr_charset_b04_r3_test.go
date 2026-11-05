package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R3 B04-001 回归：安装命令的面板地址此前只过结构
// 字符集（@ # , / ? 空白/控制），; $() `` | & ' 等 shell 元字符可原样
// 进入无引号拼入、以 root 执行的安装命令。修复：IsValidNodeAddress
// 落 LDH 字符集门（写面与生成面共同祖先）、网站配置 ip 写面先拒。
// 1.7.7（KU5）：GitHub 代理地址面整体消失——「开启 GitHub 加速」
// 设置与 getGithubProxyConfig 消费面已移除，安装命令只拼内置候选
// 常量（逐项加引号）；原 github_proxy_url 写面校验/消费兜底用例由
// TestB04R3InstallCommandIgnoresStoredGithubProxy 取代（存量坏值
// 零影响为新的守护口径）。

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
}

// 1.7.7 消费面口径（取代原 getGithubProxyConfig 兜底用例）：设置已
// 整体移除后，存量 github_proxy_url 坏值对安装命令零影响——命令
// 不得出现存量值、不得注入 PROXY_URL/PROXY_ENABLED，且候选清单为
// 内置常量逐项加引号，元字符无从进入以 root 执行的命令。
func TestB04R3InstallCommandIgnoresStoredGithubProxy(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "b04-node", "0123456789abcdef0123456789abcdef", "10.90.80.73", "10.90.80.73", "", "31000-31099", "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0, 0, "", "", "").Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "b04-node")
	if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, "ip", "192.0.2.11:6366", now).Error; err != nil {
		t.Fatalf("seed vite_config ip: %v", err)
	}
	if err := r.UpsertConfig("github_proxy_enabled", "true", now); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertConfig("github_proxy_url", "https://p.example.com;id", now); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"1.7.2","name":"x","draft":false,"prerelease":false}]`)
	}))
	t.Cleanup(api.Close)
	swapGitHubBasesForTest(t, api.URL, api.URL)
	// 同 r7_j01：nodeInstall 的安装资产预热须本地化＋排空（GATE F3）。
	swapGHMirrorGitHubBase(t, api.URL)

	raw, _ := json.Marshal(map[string]interface{}{"id": nodeID, "channel": "stable"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/install", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	h.nodeInstall(res, req)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode nodeInstall response: %v", err)
	}
	if env.Code != 0 {
		t.Fatalf("nodeInstall must succeed despite hostile stored proxy value, got code=%d msg=%s", env.Code, env.Msg)
	}
	if strings.Contains(env.Data, "p.example.com") {
		t.Fatalf("install command must not contain the stored proxy value, got %q", env.Data)
	}
	if strings.Contains(env.Data, "PROXY_URL") || strings.Contains(env.Data, "PROXY_ENABLED") {
		t.Fatalf("install command must not inject PROXY_* env, got %q", env.Data)
	}
	if !strings.Contains(env.Data, `"https://ghfast.top"`) {
		t.Fatalf("install command must carry the built-in quoted candidate list, got %q", env.Data)
	}
}
