package handler

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// 复扫（fix/final-resweep）：从头机械重推发现的新增同族残留。
// 先红后绿：每项修复前应失败，修复后须通过；含合法对照（防过修）。

// R-03：配置 `notify_webhook_url` 保存面接受 `https://a..b/hook`。
// normalizeAndValidateConfigValue default 直通，仅使用期（validateNotifyURL）
// DNS fail-closed；与 L1-F-08 同形。
func TestResweepNotifyWebhookURLConfigRejectsEmptyLabel(t *testing.T) {
	for _, u := range []string{"https://a..b/hook", "https://a...b/x"} {
		if _, err := normalizeAndValidateConfigValue("notify_webhook_url", u); err == nil {
			t.Errorf("normalizeAndValidateConfigValue(notify_webhook_url, %q) = nil, want error", u)
		} else if !strings.Contains(err.Error(), "空标签") {
			t.Errorf("normalizeAndValidateConfigValue(notify_webhook_url, %q) = %v, want empty-label error", u, err)
		}
	}
	for _, u := range []string{"", "https://example.com/hook", "https://203.0.113.9/hook"} {
		if _, err := normalizeAndValidateConfigValue("notify_webhook_url", u); err != nil {
			t.Errorf("normalizeAndValidateConfigValue(notify_webhook_url, %q) = %v, want nil", u, err)
		}
	}
}

// R-02：一键测算探测 ReferenceHost / SpeedtestURL 接受空标签主机名。
// nodeTune probe 动作只做 SplitHostPort / scheme 前缀检查，两值透传给 agent
// 拨号/下载，探测期才 loud 失败。Handler 边界测试：修复前应走到
// sendNodeCommand（无空标签文案），修复后须在校验期以空标签拒。
func TestResweepTuneProbeRejectsEmptyLabelInputs(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "tune-probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "tune-node", "198.51.100.21")

	call := func(payload map[string]interface{}) (int, string) {
		res := httptest.NewRecorder()
		h.nodeTune(res, newAuthenticatedJSONRequest(t, payload))
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v body=%s", err, res.Body.String())
		}
		return env.Code, env.Msg
	}

	code, msg := call(map[string]interface{}{
		"id": nodeID, "action": "probe", "referenceHost": "a..b:443",
	})
	if code == 0 || !strings.Contains(msg, "空标签") {
		t.Fatalf("R-02: probe referenceHost=a..b:443 must be rejected naming empty label, got code=%d msg=%s", code, msg)
	}
	code, msg = call(map[string]interface{}{
		"id": nodeID, "action": "probe", "speedtestURL": "https://a..b/file.bin",
	})
	if code == 0 || !strings.Contains(msg, "空标签") {
		t.Fatalf("R-02: probe speedtestURL=https://a..b/file.bin must be rejected naming empty label, got code=%d msg=%s", code, msg)
	}
	// 合法对照：须越过校验期（走到节点命令下发失败，而非空标签拒）。
	// 无 wsServer 时下发失败文案为“调优失败”，不含“空标签”。
	code, msg = call(map[string]interface{}{
		"id": nodeID, "action": "probe", "referenceHost": "example.com:443",
	})
	if strings.Contains(msg, "空标签") {
		t.Fatalf("R-02: probe referenceHost=example.com:443 must pass validation, got code=%d msg=%s", code, msg)
	}
	code, msg = call(map[string]interface{}{
		"id": nodeID, "action": "probe", "speedtestURL": "https://example.com/file.bin",
	})
	if strings.Contains(msg, "空标签") {
		t.Fatalf("R-02: probe speedtestURL=https://example.com/file.bin must pass validation, got code=%d msg=%s", code, msg)
	}
}

// R-04（写面）：nftables 节点 SSH 配置 host `a..b` 被接受并落库。
// persistNodeSSHConfig 只查非空 + 端口范围，显式 sshConfig.host 无主机名校验。
func TestResweepNodeSSHConfigRejectsEmptyLabelHost(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "ssh-host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "ssh-node", "198.51.100.22")
	now := time.Now().UnixMilli()

	req := map[string]interface{}{
		"serverIp": "198.51.100.22",
		"sshConfig": map[string]interface{}{
			"host": "a..b", "port": 22, "username": "root",
			"authType": "password", "password": "x",
		},
	}
	if err := h.persistNodeSSHConfig(nodeID, req, "nftables", now, false); err == nil {
		t.Fatal("R-04: persistNodeSSHConfig(host=a..b) = nil, want error")
	} else if !strings.Contains(err.Error(), "空标签") {
		t.Fatalf("R-04: persistNodeSSHConfig(host=a..b) = %v, want empty-label error", err)
	}
	// 合法对照：域名与 IP 字面量放行。
	for _, host := range []string{"example.com", "203.0.113.9"} {
		req["sshConfig"] = map[string]interface{}{
			"host": host, "port": 22, "username": "root",
			"authType": "password", "password": "x",
		}
		if err := h.persistNodeSSHConfig(nodeID, req, "nftables", now, false); err != nil {
			t.Fatalf("R-04: persistNodeSSHConfig(host=%q) = %v, want nil", host, err)
		}
	}
}

// R-07：联邦 tunnel/create Target `a..b:443` 被接受并存库。
// federationTunnelCreate 对持 share token 的对端提交的 Target 无任何校验。
func TestResweepFederationTunnelCreateRejectsEmptyLabelTarget(t *testing.T) {
	h, _, share := fix132ShareFixture(t, 31000, 31099)
	payload := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, federationTunnelRequest{
		Protocol: "tcp", RemotePort: 31001, Target: "a..b:443",
	}, h.federationTunnelCreate)
	if payload.Code == 0 {
		t.Fatal("R-07: tunnel/create Target=a..b:443 must be rejected, got success")
	}
	if !strings.Contains(payload.Msg, "空标签") {
		t.Fatalf("R-07: rejection must name empty label, msg=%s", payload.Msg)
	}
	// 合法对照：须越过 Target 校验（端口在范围内、目标合法，创建成功）。
	payload = fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, federationTunnelRequest{
		Protocol: "tcp", RemotePort: 31002, Target: "203.0.113.5:443",
	}, h.federationTunnelCreate)
	if payload.Code != 0 {
		t.Fatalf("R-07: tunnel/create Target=203.0.113.5:443 must succeed, got code=%d msg=%s", payload.Code, payload.Msg)
	}
}

// R-08（写面）：联邦分享 AllowedDomains 接受 `a..b` 条目并落库。
// 创建面直存 req.AllowedDomains，无任何校验；消费面为等值匹配，坏条目永不命中。
func TestResweepPeerShareAllowedDomainsRejectsEmptyLabel(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "share-domains.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "share-node", "198.51.100.23")

	call := func(payload map[string]interface{}) (int, string) {
		res := httptest.NewRecorder()
		h.federationShareCreate(res, newAuthenticatedJSONRequest(t, payload))
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v body=%s", err, res.Body.String())
		}
		return env.Code, env.Msg
	}

	code, msg := call(map[string]interface{}{
		"name": "bad-domains", "nodeId": nodeID,
		"portRangeStart": 31000, "portRangeEnd": 31099,
		"allowedDomains": "example.com,a..b",
	})
	if code == 0 || !strings.Contains(msg, "空标签") {
		t.Fatalf("R-08: share create allowedDomains=example.com,a..b must be rejected naming empty label, got code=%d msg=%s", code, msg)
	}
	// 合法对照：须创建成功。
	code, msg = call(map[string]interface{}{
		"name": "good-domains", "nodeId": nodeID,
		"portRangeStart": 31000, "portRangeEnd": 31099,
		"allowedDomains": "example.com,peer.example.org",
	})
	if code != 0 {
		t.Fatalf("R-08: share create allowedDomains=example.com,peer.example.org must succeed, got code=%d msg=%s", code, msg)
	}
}
