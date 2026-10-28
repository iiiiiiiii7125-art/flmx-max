package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// C-01（P0）：节点安装命令注入——重定向兜底 tag 未校验即拼入 root 执行
// 的安装命令。纵深两道：① fetchLatestTagByRedirect 校验 Location host
// 与 tag 字符集（仅 [0-9A-Za-z.-]）；② nodeInstall 解析后调
// isValidAgentVersion，不合法即拒。先红后绿：修复前恶意 Location 应
// 被接受（测试失败），修复后必须被拒。

func c01TagServer(t *testing.T, location string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestC01RedirectTagRejectsShellMetacharacters(t *testing.T) {
	// 恶意代理返回含 shell 元字符的 tag：修复前原样提取、修复后必须拒。
	srv := c01TagServer(t, "https://github.com/x/y/releases/tag/1.7.1;touch${IFS}/tmp/cprobe-pwned;echo${IFS}done")
	if tag, err := fetchLatestTagByRedirect(srv.URL + "/x/y/releases/latest"); err == nil {
		t.Fatalf("malicious Location must be rejected, got tag %q", tag)
	}
}

func TestC01RedirectTagRejectsForeignHost(t *testing.T) {
	// Location host 不是 github.com：修复前只按 marker 取子串、修复后必须拒。
	srv := c01TagServer(t, "https://evil.example.com/x/y/releases/tag/1.7.1")
	if tag, err := fetchLatestTagByRedirect(srv.URL + "/x/y/releases/latest"); err == nil {
		t.Fatalf("foreign-host Location must be rejected, got tag %q", tag)
	}
}

func TestC01RedirectTagRejectsSpaceInTag(t *testing.T) {
	srv := c01TagServer(t, "https://github.com/x/y/releases/tag/1.7.1 | id")
	if tag, err := fetchLatestTagByRedirect(srv.URL + "/x/y/releases/latest"); err == nil {
		t.Fatalf("tag with space must be rejected, got tag %q", tag)
	}
}

func TestC01RedirectTagAcceptsValidTags(t *testing.T) {
	// 合法 tag（绝对 Location 与相对 Location）修复前后均须通过。
	srv := c01TagServer(t, "https://github.com/x/y/releases/tag/1.7.1")
	if tag, err := fetchLatestTagByRedirect(srv.URL + "/x/y/releases/latest"); err != nil || tag != "1.7.1" {
		t.Fatalf("valid absolute Location: tag=%q err=%v", tag, err)
	}
	rel := c01TagServer(t, "/x/y/releases/tag/v1.7.1")
	if tag, err := fetchLatestTagByRedirect(rel.URL + "/x/y/releases/latest"); err != nil || tag != "v1.7.1" {
		t.Fatalf("valid relative Location: tag=%q err=%v", tag, err)
	}
}

// TestC01NodeInstallRejectsInvalidVersion：第二道纵深——即使 tag 绕过
// 生产处校验进入 nodeInstall，也必须在拼入 shell 命令前被
// isValidAgentVersion 拦下（与 nodeUpgrade/nodeBatchUpgrade 同口径）。
func TestC01NodeInstallRejectsInvalidVersion(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	r, err := repo.Open(t.TempDir() + "/c01-nodeinstall.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "c01-node", "c01-secret", "10.90.80.71", "10.90.80.71", "", "31000-31099", "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0, 0, "", "", "").Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "c01-node")
	if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, "ip", "192.0.2.10:6366", now).Error; err != nil {
		t.Fatalf("seed vite_config ip: %v", err)
	}
	if err := r.UpsertConfig("github_proxy_enabled", "false", now); err != nil {
		t.Fatalf("disable github proxy: %v", err)
	}

	// API 返回一个含 shell 元字符的 dev tag：dev 渠道按 releaseChannelFromTag
	// 命中（beta 关键字），resolve 层原样返回；nodeInstall 必须拒。
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"1.7.2-beta1;id>/tmp/c01-pwn","name":"x","draft":false,"prerelease":true}]`)
	}))
	defer api.Close()
	swapGitHubBasesForTest(t, api.URL, api.URL)

	h := New(r, "test-jwt-secret")
	call := func(channel string) (int, string, string) {
		raw, _ := json.Marshal(map[string]interface{}{"id": nodeID, "channel": channel})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/node/install", bytes.NewReader(raw))
		res := httptest.NewRecorder()
		h.nodeInstall(res, req)
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data string `json:"data"`
		}
		if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
			t.Fatalf("decode nodeInstall response: %v body=%s", err, res.Body.String())
		}
		return env.Code, env.Msg, env.Data
	}

	if code, msg, cmd := call("dev"); code == 0 {
		t.Fatalf("nodeInstall must reject shell metacharacters in version, got code 0 cmd=%q", cmd)
	} else if !strings.Contains(msg, "版本号格式无效") {
		t.Fatalf("nodeInstall rejection must name the version format, got msg=%q", msg)
	}

	// 正例：合法稳定版仍能生成安装命令。
	api2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"1.7.1","name":"x","draft":false,"prerelease":false}]`)
	}))
	defer api2.Close()
	clearReleasesCacheForTest(t)
	swapGitHubBasesForTest(t, api2.URL, api2.URL)
	if code, msg, cmd := call("stable"); code != 0 {
		t.Fatalf("nodeInstall with valid version must succeed, got code=%d msg=%s", code, msg)
	} else if !strings.Contains(cmd, "VERSION=1.7.1 ") {
		t.Fatalf("install command must pin VERSION=1.7.1, got %q", cmd)
	}
}
