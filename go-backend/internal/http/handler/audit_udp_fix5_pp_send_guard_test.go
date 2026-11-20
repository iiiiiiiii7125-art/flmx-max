package handler

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// FIX-UDP-FIX5 (R1)：PROXY 发送侧守卫。覆盖三面：
//   - 判定本身（SS 恒拒、非 SS 看 Accept＋内核、未知/损坏行 fail-open）；
//   - 转发同步回执（proxyProtocolSendWarningsForForward 经
//     syncForwardServicesWithWarnings 的 warnings，直连拨号与托管
//     中转两形）；
//   - 面板推导面（syncRelayProxyProtocol 的拦写＋清回、
//     inboundRelayCreate 的 ppSend 置 0＋警告）。

func fix5SeedKernel(t *testing.T, h *Handler, nodeID int64, kernelType string) int64 {
	t.Helper()
	k := &model.NodeKernel{NodeID: nodeID, KernelType: kernelType, Version: "latest",
		Status: "running", Enabled: 1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := h.repo.CreateNodeKernel(k); err != nil {
		t.Fatalf("seed kernel %s: %v", kernelType, err)
	}
	return k.ID
}

func fix5SeedInbound(t *testing.T, h *Handler, kernelID, nodeID int64, name, protocol string, port, accept int) *model.NodeInbound {
	t.Helper()
	row := &model.NodeInbound{
		KernelID: kernelID, NodeID: nodeID, Name: name, Protocol: protocol,
		Listen: "::", Port: port, AcceptProxyProtocol: accept,
		Security: `{"type":"none"}`,
		Settings: `{"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA=="}`,
		Enabled:  1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli(),
	}
	if err := h.repo.CreateNodeInbound(row); err != nil {
		t.Fatalf("seed inbound %s: %v", name, err)
	}
	return row
}

func fix5WarningHas(warnings []string, substrs ...string) bool {
	for _, w := range warnings {
		hit := true
		for _, s := range substrs {
			if !strings.Contains(w, s) {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

func TestProxyProtocolSendTargetCheck(t *testing.T) {
	cases := []struct {
		name       string
		row        *model.NodeInbound
		kernelType string
		wantOK     bool
	}{
		{"ss accept0", &model.NodeInbound{Name: "ss0", Protocol: kernel.ProtocolShadowsocks, AcceptProxyProtocol: 0}, "", false},
		{"ss accept1 xray", &model.NodeInbound{Name: "ss1", Protocol: kernel.ProtocolShadowsocks, AcceptProxyProtocol: 1}, "xray", false},
		{"ss accept1 sing-box", &model.NodeInbound{Name: "ss2", Protocol: kernel.ProtocolShadowsocks, AcceptProxyProtocol: 1}, "sing-box", false},
		{"vless accept0 xray", &model.NodeInbound{Name: "vl0", Protocol: kernel.ProtocolVLESS, AcceptProxyProtocol: 0}, "xray", false},
		{"vless accept1 xray", &model.NodeInbound{Name: "vl1", Protocol: kernel.ProtocolVLESS, AcceptProxyProtocol: 1}, "xray", true},
		{"vless accept1 sing-box", &model.NodeInbound{Name: "vl2", Protocol: kernel.ProtocolVLESS, AcceptProxyProtocol: 1}, "sing-box", false},
		{"vless accept1 singbox-companion", &model.NodeInbound{Name: "vl3", Protocol: kernel.ProtocolVLESS, AcceptProxyProtocol: 1}, "singbox-companion", false},
		{"vless accept1 unknown kernel", &model.NodeInbound{Name: "vl4", Protocol: kernel.ProtocolVLESS, AcceptProxyProtocol: 1}, "", true},
		{"damaged settings", &model.NodeInbound{Name: "bad", Protocol: kernel.ProtocolVLESS, AcceptProxyProtocol: 0, Security: `{bad json`}, "xray", true},
		{"nil row", nil, "xray", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, _, _ := proxyProtocolSendTargetCheck(tc.row, tc.kernelType)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
		})
	}
}

func TestProxyProtocolSendWarningsForForward(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	h.nftablesManager = newSerializedNftManager(&fakeNftablesManager{})
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "fix5-ppguard-tunnel", fixture.nodeID)
	now := time.Now().UnixMilli()

	xrayID := fix5SeedKernel(t, h, fixture.nodeID, "xray")
	singboxID := fix5SeedKernel(t, h, fixture.nodeID, "sing-box")
	ssPlain := fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-ss-plain", kernel.ProtocolShadowsocks, 28401, 0)
	fix5SeedInbound(t, h, xrayID, fixture.nodeID, "fix5-ss-accept", kernel.ProtocolShadowsocks, 28402, 1)
	fix5SeedInbound(t, h, xrayID, fixture.nodeID, "fix5-vl-noaccept", kernel.ProtocolVLESS, 28403, 0)
	vlessXray := fix5SeedInbound(t, h, xrayID, fixture.nodeID, "fix5-vl-xray", kernel.ProtocolVLESS, 28404, 1)
	fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-vl-singbox", kernel.ProtocolVLESS, 28405, 1)

	setSend := func(fwd *forwardRecord, send int) *forwardRecord {
		t.Helper()
		if err := h.repo.UpdateForward(fwd.ID, fwd.Name, fwd.TunnelID, fwd.RemoteAddr, fwd.Strategy, now,
			fwd.SpeedID, fwd.MaxConn, fwd.IPMaxConn, fwd.IPSpeedID, 0, 0, send); err != nil {
			t.Fatalf("set send: %v", err)
		}
		updated, err := h.getForwardRecord(fwd.ID)
		if err != nil {
			t.Fatalf("re-get forward: %v", err)
		}
		return updated
	}
	syncHasWarning := func(fwd *forwardRecord, name string) bool {
		t.Helper()
		warnings, err := h.syncForwardServicesWithWarnings(fwd, "UpdateService", true)
		if err != nil {
			t.Fatalf("syncForwardServicesWithWarnings: %v", err)
		}
		return fix5WarningHas(warnings, "PROXY", name)
	}

	// 直连拨号形：节点公网 IP + 入站端口。入口端口逐个分配唯一
	// 值——同一节点上入口端口不可复用。
	nextEntryPort := 20100
	entryPort := func() int {
		nextEntryPort++
		return nextEntryPort
	}
	direct := func(port int, send int) *forwardRecord {
		t.Helper()
		fwd := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "198.51.100.10:"+strconv.Itoa(port), entryPort())
		return setSend(fwd, send)
	}
	if !syncHasWarning(direct(28401, 1), "fix5-ss-plain") {
		t.Fatalf("send=1 to plain SS must warn")
	}
	if !syncHasWarning(direct(28402, 1), "fix5-ss-accept") {
		t.Fatalf("send=1 to SS must warn even with AcceptProxyProtocol on")
	}
	if !syncHasWarning(direct(28403, 1), "fix5-vl-noaccept") {
		t.Fatalf("send=1 to VLESS without AcceptProxyProtocol must warn")
	}
	if syncHasWarning(direct(28404, 1), "fix5-vl-xray") {
		t.Fatalf("send=1 to Xray VLESS with AcceptProxyProtocol must not warn")
	}
	if !syncHasWarning(direct(28405, 1), "fix5-vl-singbox") {
		t.Fatalf("send=1 to sing-box VLESS with AcceptProxyProtocol must warn")
	}
	if syncHasWarning(direct(28401, 0), "fix5-ss-plain") {
		t.Fatalf("send=0 to SS must not warn")
	}
	if !syncHasWarning(direct(28401, 2), "fix5-ss-plain") {
		t.Fatalf("send=2 to SS must warn")
	}
	// 遗留 proxyProtocol 字段归一为 send：同样警告。
	legacy := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "198.51.100.10:28401", entryPort())
	if err := h.repo.UpdateForward(legacy.ID, legacy.Name, legacy.TunnelID, legacy.RemoteAddr, legacy.Strategy, now,
		legacy.SpeedID, legacy.MaxConn, legacy.IPMaxConn, legacy.IPSpeedID, 1, 0, 0); err != nil {
		t.Fatalf("set legacy proxyProtocol: %v", err)
	}
	legacy, err := h.getForwardRecord(legacy.ID)
	if err != nil {
		t.Fatalf("re-get legacy forward: %v", err)
	}
	if legacy.ProxyProtocolSend != 1 {
		t.Fatalf("legacy proxyProtocol must normalize to send=1, got %d", legacy.ProxyProtocolSend)
	}
	if !syncHasWarning(legacy, "fix5-ss-plain") {
		t.Fatalf("legacy proxyProtocol=1 to SS must warn")
	}
	// 托管中转形：转发经中转行落地到入站（RemoteAddr 与入站口无关）。
	seedRelay := func(inboundID, forwardID int64) {
		t.Helper()
		if err := h.repo.CreateInboundRelay(&model.NodeInboundRelay{
			InboundID: inboundID, ForwardID: forwardID, Name: "rel", Managed: 1, CreatedTime: now,
		}); err != nil {
			t.Fatalf("seed relay: %v", err)
		}
	}
	fRelaySS := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", entryPort())
	fRelaySS = setSend(fRelaySS, 1)
	seedRelay(ssPlain.ID, fRelaySS.ID)
	if !syncHasWarning(fRelaySS, "fix5-ss-plain") {
		t.Fatalf("managed relay landing on SS must warn")
	}
	fRelayOK := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", entryPort())
	fRelayOK = setSend(fRelayOK, 1)
	seedRelay(vlessXray.ID, fRelayOK.ID)
	if syncHasWarning(fRelayOK, "fix5-vl-xray") {
		t.Fatalf("managed relay landing on Xray VLESS with AcceptProxyProtocol must not warn")
	}
	// 非托管中转：不误报。
	fUnmanaged := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", entryPort())
	fUnmanaged = setSend(fUnmanaged, 1)
	if err := h.repo.DB().Exec(`INSERT INTO node_inbound_relay(inbound_id, forward_id, name, share_host, managed, created_time) VALUES(?, ?, 'rel', '', 0, ?)`, ssPlain.ID, fUnmanaged.ID, now).Error; err != nil {
		t.Fatalf("seed unmanaged relay: %v", err)
	}
	if syncHasWarning(fUnmanaged, "fix5-ss-plain") {
		t.Fatalf("unmanaged relay must not warn")
	}
	// 域名目标：无 DNS 解析面，不误报。本格放最后、直接断言
	// helper：域名转发一旦落库，后续的节点级 nft 同步会因它
	// 校验全量转发而失败（见 ShadowTLS 测试同顺序）。
	domain := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "example.com:28401", entryPort())
	domain = setSend(domain, 1)
	if fix5WarningHas(h.proxyProtocolSendWarningsForForward(domain), "PROXY", "fix5-ss-plain") {
		t.Fatalf("domain-form RemoteAddr must not match (no DNS resolution in sync path)")
	}
}

func TestSyncRelayProxyProtocolSuppressesSend(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	h.nftablesManager = newSerializedNftManager(&fakeNftablesManager{})
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "fix5-ppguard-sync-tunnel", fixture.nodeID)
	now := time.Now().UnixMilli()

	xrayID := fix5SeedKernel(t, h, fixture.nodeID, "xray")
	singboxID := fix5SeedKernel(t, h, fixture.nodeID, "sing-box")

	seedRelayForward := func(inboundID int64, send int, port int) *forwardRecord {
		t.Helper()
		fwd := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", port)
		if err := h.repo.UpdateForward(fwd.ID, fwd.Name, fwd.TunnelID, fwd.RemoteAddr, fwd.Strategy, now,
			fwd.SpeedID, fwd.MaxConn, fwd.IPMaxConn, fwd.IPSpeedID, 0, 0, send); err != nil {
			t.Fatalf("set send: %v", err)
		}
		if err := h.repo.CreateInboundRelay(&model.NodeInboundRelay{
			InboundID: inboundID, ForwardID: fwd.ID, Name: "rel", Managed: 1, CreatedTime: now,
		}); err != nil {
			t.Fatalf("seed relay: %v", err)
		}
		updated, err := h.getForwardRecord(fwd.ID)
		if err != nil {
			t.Fatalf("re-get forward: %v", err)
		}
		return updated
	}
	forwardSend := func(fwd *forwardRecord) int {
		t.Helper()
		got, err := h.getForwardRecord(fwd.ID)
		if err != nil {
			t.Fatalf("re-get forward: %v", err)
		}
		return got.ProxyProtocolSend
	}
	rowOf := func(id int64) model.NodeInbound {
		t.Helper()
		row, err := h.repo.GetNodeInbound(id)
		if err != nil || row == nil {
			t.Fatalf("get inbound %d: %v", id, err)
		}
		return *row
	}

	// SS 入站开 Accept：存量 send=1 的中转转发须清回 0，并警告。
	ss := fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-sync-ss", kernel.ProtocolShadowsocks, 28411, 1)
	fSS := seedRelayForward(ss.ID, 1, 20211)
	if warnings := h.syncRelayProxyProtocol(rowOf(ss.ID)); !fix5WarningHas(warnings, "PROXY", "fix5-sync-ss") {
		t.Fatalf("SS Accept flip must warn, got %v", warnings)
	}
	if got := forwardSend(fSS); got != 0 {
		t.Fatalf("SS relay forward send must be cleared to 0, got %d", got)
	}
	// sing-box VLESS 入站开 Accept：同样拦写＋清回。
	vlSB := fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-sync-vl-sb", kernel.ProtocolVLESS, 28412, 1)
	fVLSB := seedRelayForward(vlSB.ID, 1, 20212)
	if warnings := h.syncRelayProxyProtocol(rowOf(vlSB.ID)); !fix5WarningHas(warnings, "PROXY", "fix5-sync-vl-sb") {
		t.Fatalf("sing-box VLESS Accept flip must warn, got %v", warnings)
	}
	if got := forwardSend(fVLSB); got != 0 {
		t.Fatalf("sing-box relay forward send must be cleared to 0, got %d", got)
	}
	// Xray VLESS 入站开 Accept：原样置 send=1，无警告。
	vlX := fix5SeedInbound(t, h, xrayID, fixture.nodeID, "fix5-sync-vl-x", kernel.ProtocolVLESS, 28413, 1)
	fVLX := seedRelayForward(vlX.ID, 0, 20213)
	if warnings := h.syncRelayProxyProtocol(rowOf(vlX.ID)); len(warnings) != 0 {
		t.Fatalf("Xray VLESS Accept flip must not warn, got %v", warnings)
	}
	if got := forwardSend(fVLX); got != 1 {
		t.Fatalf("Xray relay forward send must become 1, got %d", got)
	}
	// 无存续托管中转的 SS 入站开 Accept：无警告（什么都没拦下）。
	ssAlone := fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-sync-ss-alone", kernel.ProtocolShadowsocks, 28414, 1)
	if warnings := h.syncRelayProxyProtocol(rowOf(ssAlone.ID)); len(warnings) != 0 {
		t.Fatalf("Accept flip with no live managed relay must not warn, got %v", warnings)
	}
}

func TestRelayProxyProtocolSendForInbound(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	xrayID := fix5SeedKernel(t, h, fixture.nodeID, "xray")
	singboxID := fix5SeedKernel(t, h, fixture.nodeID, "sing-box")

	cases := []struct {
		name     string
		row      *model.NodeInbound
		wantSend int
		wantWarn bool
	}{
		{"ss accept1", fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-decide-ss", kernel.ProtocolShadowsocks, 28421, 1), 0, true},
		{"ss accept0", fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-decide-ss0", kernel.ProtocolShadowsocks, 28422, 0), 0, false},
		{"vless xray accept1", fix5SeedInbound(t, h, xrayID, fixture.nodeID, "fix5-decide-vlx", kernel.ProtocolVLESS, 28423, 1), 1, false},
		{"vless singbox accept1", fix5SeedInbound(t, h, singboxID, fixture.nodeID, "fix5-decide-vlsb", kernel.ProtocolVLESS, 28424, 1), 0, true},
		{"vless xray accept0", fix5SeedInbound(t, h, xrayID, fixture.nodeID, "fix5-decide-vlx0", kernel.ProtocolVLESS, 28425, 0), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			send, warning := h.relayProxyProtocolSendForInbound(tc.row)
			if send != tc.wantSend {
				t.Fatalf("send=%d, want %d", send, tc.wantSend)
			}
			if (warning != "") != tc.wantWarn {
				t.Fatalf("warning=%q, wantWarn=%v", warning, tc.wantWarn)
			}
			if tc.wantWarn && !strings.Contains(warning, "PROXY") {
				t.Fatalf("warning must name PROXY, got %q", warning)
			}
		})
	}
}

func TestInboundRelayCreateSuppressesProxySend(t *testing.T) {
	r, h := r2b2Open(t)
	if _, err := r.CreateUser("admin", "hash", 0, time.Now().Add(24*time.Hour).UnixMilli(), 1, 1, 100, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	nodeID := seedRelayTargetNode(t, r, "fix5-relay-entry", "198.51.100.31")
	tunnelID := seedTunnelForNftables(t, h, "fix5-relay-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	inbound := &model.NodeInbound{
		Name: "fix5-relay-ss", Protocol: kernel.ProtocolShadowsocks, Listen: "198.51.100.19", Port: 8388,
		AcceptProxyProtocol: 1, Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.DB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	body := r2b2AuthPost(t, h.inboundRelayCreate, "/api/v1/inbound/relay/create",
		map[string]interface{}{"inboundId": inbound.ID, "tunnelId": tunnelID, "inPort": 30007, "name": "fix5-relay"}, "fix5-relay-key-1")
	var env struct {
		Code    int                    `json:"code"`
		Warning string                 `json:"warning"`
		Data    map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode relay create response: %v (%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("relay create must succeed, got %s", body)
	}
	if !strings.Contains(env.Warning, "PROXY") || !strings.Contains(env.Warning, "fix5-relay-ss") {
		t.Fatalf("relay create must warn about suppressed PROXY send, got warning=%q", env.Warning)
	}
	forwardID, _ := env.Data["forwardId"].(float64)
	if forwardID <= 0 {
		t.Fatalf("relay create response must carry forwardId, got %s", body)
	}
	fwd, err := h.getForwardRecord(int64(forwardID))
	if err != nil {
		t.Fatalf("get relay forward: %v", err)
	}
	if fwd.ProxyProtocolSend != 0 {
		t.Fatalf("relay forward to SS must not carry ProxyProtocolSend, got %d", fwd.ProxyProtocolSend)
	}
}

// OPEN-3: Xray's acceptProxyProtocol has no source allowlist and
// the panel's RelayOnly is advertisement-level only, so a
// PROXY-protocol inbound without a relay whitelist lets any direct
// client forge the ClientIP recorded in the connlog. Saving such an
// inbound warns (it does not refuse — the operator may knowingly
// run an open relay face) so the exposure is disclosed at save
// time, not discovered from the logs.
func TestAcceptProxyProtocolSpoofWarning(t *testing.T) {
	cases := []struct {
		name string
		row  model.NodeInbound
		want bool
	}{
		{"pp on, whitelist off warns", model.NodeInbound{AcceptProxyProtocol: 1, RelayWhitelist: 0}, true},
		{"pp on, whitelist on is quiet", model.NodeInbound{AcceptProxyProtocol: 1, RelayWhitelist: 1}, false},
		{"pp off, whitelist off is quiet", model.NodeInbound{AcceptProxyProtocol: 0, RelayWhitelist: 0}, false},
		{"pp off, whitelist on is quiet", model.NodeInbound{AcceptProxyProtocol: 0, RelayWhitelist: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := acceptProxyProtocolSpoofWarning(tc.row)
			if tc.want && got == "" {
				t.Fatal("pp on without a relay whitelist must warn, got empty")
			}
			if tc.want {
				if !strings.Contains(got, "白名单") || !strings.Contains(got, "伪造") {
					t.Fatalf("warning must name the whitelist and the forgery, got %q", got)
				}
			}
			if !tc.want && got != "" {
				t.Fatalf("must not warn, got %q", got)
			}
		})
	}
}
