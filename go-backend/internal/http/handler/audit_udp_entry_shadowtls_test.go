package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// FIX-UDP-ENTRY (a)：ShadowTLS 前置 SS 入站的 UDP 静默丢弃必须经
// 警告通道上行，不得静默。覆盖三面：
//   - 判定本身（SS+ShadowTLS 才报、其余形态不误报）；
//   - 转发同步回执（syncForwardServicesWithWarnings 的 warnings，
//     中转落地与直接拨号两形态）；
//   - 入站侧（中转创建/入站保存共用的 shadowTLSRelayWarningsForInbound）。

func shadowTLSWarningSeedInbound(t *testing.T, h *Handler, nodeID int64, name string, protocol, security string, port int) *model.NodeInbound {
	t.Helper()
	row := &model.NodeInbound{
		KernelID: 0, NodeID: nodeID, Name: name, Protocol: protocol,
		Listen: "::", Port: port,
		Security: security,
		Settings: `{"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA=="}`,
		Enabled:  1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli(),
	}
	if err := h.repo.CreateNodeInbound(row); err != nil {
		t.Fatalf("seed inbound %s: %v", name, err)
	}
	return row
}

func shadowTLSWarningHas(warnings []string) bool {
	for _, w := range warnings {
		if strings.Contains(w, "ShadowTLS") && strings.Contains(w, "UDP") {
			return true
		}
	}
	return false
}

func TestShadowTLSShadowsocksInboundWarning(t *testing.T) {
	shadowtls := `{"type":"shadowtls","sni":"www.example.com","shadowTlsPassword":"stls-pass-1"}`
	cases := []struct {
		name     string
		row      *model.NodeInbound
		wantWarn bool
	}{
		{"ss+shadowtls", &model.NodeInbound{Name: "ss-stls", Protocol: kernel.ProtocolShadowsocks, Security: shadowtls}, true},
		{"ss+none", &model.NodeInbound{Name: "ss-plain", Protocol: kernel.ProtocolShadowsocks, Security: `{"type":"none"}`}, false},
		{"ss+tls", &model.NodeInbound{Name: "ss-tls", Protocol: kernel.ProtocolShadowsocks, Security: `{"type":"tls","sni":"example.com"}`}, false},
		{"trojan+shadowtls", &model.NodeInbound{Name: "tj-stls", Protocol: kernel.ProtocolTrojan, Security: shadowtls}, false},
		{"damaged security", &model.NodeInbound{Name: "ss-bad", Protocol: kernel.ProtocolShadowsocks, Security: `{bad json`}, false},
		{"nil row", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := shadowTLSShadowsocksInboundWarning(tc.row)
			if ok != tc.wantWarn {
				t.Fatalf("warn=%v, want %v", ok, tc.wantWarn)
			}
		})
	}
}

func TestShadowTLSUDPWarningsForForwardSync(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	h.nftablesManager = newSerializedNftManager(&fakeNftablesManager{})
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "udp-entry-shadowtls-tunnel", fixture.nodeID)
	now := time.Now().UnixMilli()

	stls := shadowTLSWarningSeedInbound(t, h, fixture.nodeID, "ss-stls",
		kernel.ProtocolShadowsocks, `{"type":"shadowtls","sni":"www.example.com","shadowTlsPassword":"stls-pass-1"}`, 28389)
	plain := shadowTLSWarningSeedInbound(t, h, fixture.nodeID, "ss-plain",
		kernel.ProtocolShadowsocks, `{"type":"none"}`, 28390)

	seedRelay := func(inboundID, forwardID int64, managed int) {
		t.Helper()
		if err := h.repo.CreateInboundRelay(&model.NodeInboundRelay{
			InboundID: inboundID, ForwardID: forwardID, Name: "rel", Managed: managed, CreatedTime: now,
		}); err != nil {
			t.Fatalf("seed relay: %v", err)
		}
	}
	syncHasWarning := func(fwd *forwardRecord) bool {
		t.Helper()
		warnings, err := h.syncForwardServicesWithWarnings(fwd, "UpdateService", true)
		if err != nil {
			t.Fatalf("syncForwardServicesWithWarnings: %v", err)
		}
		return shadowTLSWarningHas(warnings)
	}

	// 中转落地 ShadowTLS SS：警告。
	fRelay := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20111)
	seedRelay(stls.ID, fRelay.ID, 1)
	if !syncHasWarning(fRelay) {
		t.Fatalf("managed relay landing on ShadowTLS SS must warn")
	}
	// 非托管中转：不误报。注意 Managed=0 不能经 GORM Create 写入
	// （零值字段会被省略、落库走 default 1），须用 SQL 直写。
	fUnmanaged := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20112)
	if err := h.repo.DB().Exec(`INSERT INTO node_inbound_relay(inbound_id, forward_id, name, share_host, managed, created_time) VALUES(?, ?, 'rel', '', 0, ?)`, stls.ID, fUnmanaged.ID, now).Error; err != nil {
		t.Fatalf("seed unmanaged relay: %v", err)
	}
	if syncHasWarning(fUnmanaged) {
		t.Fatalf("unmanaged relay must not warn")
	}
	// 中转落地普通 SS：不误报。
	fPlain := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20113)
	seedRelay(plain.ID, fPlain.ID, 1)
	if syncHasWarning(fPlain) {
		t.Fatalf("managed relay landing on plain SS must not warn")
	}
	// 无中转、无直接引用：不误报。
	fNone := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20114)
	if syncHasWarning(fNone) {
		t.Fatalf("forward with no ShadowTLS reference must not warn")
	}
	// 直接拨号：节点公网 IP + ShadowTLS SS 端口（无中转行）：警告。
	fDirect := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "198.51.100.10:28389", 20115)
	if !syncHasWarning(fDirect) {
		t.Fatalf("direct forward dialing ShadowTLS SS public port must warn")
	}
	// 直接拨号端口不对：不误报。
	fWrongPort := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "198.51.100.10:28390", 20117)
	if syncHasWarning(fWrongPort) {
		t.Fatalf("direct forward dialing the plain SS port must not warn")
	}
	// 直接拨号普通 SS 公网口：不误报。
	fDirectPlain := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "198.51.100.10:28390", 20119)
	if syncHasWarning(fDirectPlain) {
		t.Fatalf("direct forward dialing plain SS must not warn")
	}
	// 直接拨号域名形态：不匹配（无 DNS 解析面），不误报。域名
	// 目标进不了 nftables 同步面（仅支持 IP 目标），且节点级 nft
	// 对账会连带校验它，故本格放最后、直接断言 helper，不走 nft
	// 同步路径。
	fDomain := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "example.com:28389", 20118)
	if shadowTLSWarningHas(h.shadowTLSUDPWarningsForForward(fDomain)) {
		t.Fatalf("domain-form RemoteAddr must not match (no DNS resolution in sync path)")
	}
}

func TestShadowTLSUDPWarningsForForwardLoopback(t *testing.T) {
	// 直接拨号回环：入口即落地节点。回环目标进不了 nftables
	// 同步面（validateNftablesForwardRequest 拒回环目标，且节
	// 点级 nft 对账会连带校验它），故本格用独立夹具、直接断言
	// helper，不走 nft 同步路径。
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "udp-entry-shadowtls-loopback", fixture.nodeID)
	shadowTLSWarningSeedInbound(t, h, fixture.nodeID, "ss-stls",
		kernel.ProtocolShadowsocks, `{"type":"shadowtls","sni":"www.example.com","shadowTlsPassword":"stls-pass-1"}`, 28389)
	fLoopback := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "127.0.0.1:28389", 20116)
	if !shadowTLSWarningHas(h.shadowTLSUDPWarningsForForward(fLoopback)) {
		t.Fatalf("direct forward dialing ShadowTLS SS via loopback on the landing node must warn")
	}
}

func TestShadowTLSRelayWarningsForInbound(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "udp-entry-shadowtls-inbound", fixture.nodeID)
	now := time.Now().UnixMilli()

	stls := shadowTLSWarningSeedInbound(t, h, fixture.nodeID, "ss-stls",
		kernel.ProtocolShadowsocks, `{"type":"shadowtls","sni":"www.example.com","shadowTlsPassword":"stls-pass-1"}`, 28389)
	plain := shadowTLSWarningSeedInbound(t, h, fixture.nodeID, "ss-plain",
		kernel.ProtocolShadowsocks, `{"type":"none"}`, 28390)

	// 无任何引用：不警告。
	if got := h.shadowTLSRelayWarningsForInbound(stls.ID); len(got) != 0 {
		t.Fatalf("unreferenced ShadowTLS SS must not warn, got %v", got)
	}
	// 存续托管中转：警告。
	fRelay := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20211)
	if err := h.repo.CreateInboundRelay(&model.NodeInboundRelay{
		InboundID: stls.ID, ForwardID: fRelay.ID, Name: "rel", Managed: 1, CreatedTime: now,
	}); err != nil {
		t.Fatalf("seed relay: %v", err)
	}
	if got := h.shadowTLSRelayWarningsForInbound(stls.ID); !shadowTLSWarningHas(got) {
		t.Fatalf("ShadowTLS SS with a live managed relay must warn, got %v", got)
	}
	// 普通 SS 有存续中转：不警告。
	fPlain := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20212)
	if err := h.repo.CreateInboundRelay(&model.NodeInboundRelay{
		InboundID: plain.ID, ForwardID: fPlain.ID, Name: "rel", Managed: 1, CreatedTime: now,
	}); err != nil {
		t.Fatalf("seed relay: %v", err)
	}
	if got := h.shadowTLSRelayWarningsForInbound(plain.ID); len(got) != 0 {
		t.Fatalf("plain SS with a live managed relay must not warn, got %v", got)
	}
	// 直接拨号引用（无中转行）：警告。
	stlsDirect := shadowTLSWarningSeedInbound(t, h, fixture.nodeID, "ss-stls-direct",
		kernel.ProtocolShadowsocks, `{"type":"shadowtls","sni":"www.example.com","shadowTlsPassword":"stls-pass-1"}`, 28391)
	seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "198.51.100.10:28391", 20213)
	if got := h.shadowTLSRelayWarningsForInbound(stlsDirect.ID); !shadowTLSWarningHas(got) {
		t.Fatalf("ShadowTLS SS dialed by a direct forward must warn, got %v", got)
	}
}
