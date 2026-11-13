package handler

import (
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Mihomo 抗量子标记版本门：Xray + 可确立的版本 ≥ 25.5.16 才为真；
// 低于门槛、未知/不可解析版本、非 Xray 内核一律为假。版本先取
// agent 上报的 InstalledVersion banner 内首个可解析 token（实装
// 二进制为准），banner 无 token 时回落干净 Version 字段。
func TestKernelSupportsPostQuantumRealityGate(t *testing.T) {
	const xrayBannerNew = "Xray 26.11.9 (Xray, Penetrates Everything.) 676eaf6 (go1.27.3 linux/amd64)"
	const xrayBannerOld = "Xray 25.5.15 (Xray, Penetrates Everything.) 676eaf6 (go1.27.3 linux/amd64)"
	for _, tc := range []struct {
		kernelType string
		version    string
		installed  string
		want       bool
	}{
		{kernel.TypeXray, "", "25.5.16", true},    // 门槛版本本身
		{kernel.TypeXray, "", "25.6.8", true},     // 高于门槛
		{kernel.TypeXray, "", "v25.10.4", true},   // v 前缀可解析
		{kernel.TypeXray, "", "25.5.15", false},   // 低一号
		{kernel.TypeXray, "", "25.5.15.9", false}, // 四段：按前三段比较，仍低于门槛
		{kernel.TypeXray, "", "25.5.16.1", true},  // 四段：前三段达门槛
		{kernel.TypeXray, "", "24.1.2.3", false},  // 四段：主版本已低于门槛
		{kernel.TypeXray, "", "25.5", false},      // 补齐为 25.5.0，仍低于门槛
		{kernel.TypeXray, "", "24.12.31", false},
		{kernel.TypeXray, "", "", false},       // 未知版本不算
		{kernel.TypeXray, "", "latest", false}, // 不可解析不算
		{kernel.TypeSingbox, "", "25.9.9", false},
		{kernel.TypeSingbox, "", "1.12.0", false},
		// PORT-043：真实 banner 形态（U87-U5 活证）。
		{kernel.TypeXray, "26.11.9", xrayBannerNew, true},  // 干净 Version＋banner 一致
		{kernel.TypeXray, "", xrayBannerNew, true},         // 仅 banner 也能确立版本
		{kernel.TypeXray, "latest", xrayBannerNew, true},   // 哨兵 Version 不挡 banner 实证
		{kernel.TypeXray, "25.5.15", xrayBannerOld, false}, // 旧版 banner 仍拒绝
		{kernel.TypeXray, "26.11.9", xrayBannerOld, false}, // banner 是实装为准：Version 新、实装旧不放行
		{kernel.TypeXray, "26.11.9", "", true},             // banner 缺失时回落干净 Version
		{kernel.TypeXray, "", "sing-box version 1.14.4", false}, // 异核 banner 形：token 1.14.4 低于门槛
		{kernel.TypeXray, "25.5.15", "", false},            // 干净 Version 低版仍拒绝
	} {
		k := &model.NodeKernel{KernelType: tc.kernelType, Version: tc.version, InstalledVersion: tc.installed}
		if got := kernelSupportsPostQuantumReality(k); got != tc.want {
			t.Fatalf("kernel %s version %q installed %q: got %v want %v", tc.kernelType, tc.version, tc.installed, got, tc.want)
		}
	}
	if kernelSupportsPostQuantumReality(nil) {
		t.Fatal("nil kernel must not qualify")
	}
}

// 组装面三态：同一 VLESS+Reality 入站，宿主内核分别为
// Xray ≥25.5.16 / Xray 低于门槛 / sing-box 时，端点标志依次为
// 真 / 假 / 假。
func TestInboundEndpointsPostQuantumFlag(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/pq.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	realityIn := kernel.Inbound{Protocol: kernel.ProtocolVLESS, Listen: "::", Port: 443,
		Settings:  kernel.Settings{UUID: "22222222-2222-4222-8222-222222222222"},
		Transport: kernel.Transport{Type: kernel.TransportTCP},
		Security: kernel.Security{Type: kernel.SecurityReality, SNI: "www.example.com",
			RealityDest: "www.example.com:443", RealityPublicKey: "pubkey",
			RealityShortIDs: []string{"ab"}, Fingerprint: "chrome"}}

	flagFor := func(kernelType, installed string) bool {
		nodeID := seedRelayTargetNode(t, r, "pq-"+kernelType+installed, "203.0.113.10")
		k := seedChainKernel(t, r, nodeID, kernelType)
		k.InstalledVersion = installed
		if err := r.DB().Save(k).Error; err != nil {
			t.Fatal(err)
		}
		row := seedChainInbound(t, r, k, "pq-in-"+kernelType+installed, realityIn, 0)
		endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*row})
		if len(endpoints) != 1 {
			t.Fatalf("kernel %s %s: want exactly 1 endpoint, got %d", kernelType, installed, len(endpoints))
		}
		return endpoints[0].PostQuantumReality
	}

	if !flagFor(kernel.TypeXray, "25.6.8") {
		t.Fatal("xray 25.6.8 hosting VLESS+Reality must set PostQuantumReality")
	}
	// PORT-043：真实 banner 形态的 InstalledVersion 必须同样立标。
	if !flagFor(kernel.TypeXray, "Xray 26.11.9 (Xray, Penetrates Everything.) 676eaf6 (go1.27.3 linux/amd64)") {
		t.Fatal("xray 26.11.9 banner hosting VLESS+Reality must set PostQuantumReality")
	}
	if flagFor(kernel.TypeXray, "Xray 25.5.15 (Xray, Penetrates Everything.) 676eaf6 (go1.27.3 linux/amd64)") {
		t.Fatal("xray 25.5.15 banner is below the PQ floor and must NOT set PostQuantumReality")
	}
	if flagFor(kernel.TypeXray, "25.3.6") {
		t.Fatal("xray 25.3.6 is below the PQ floor and must NOT set PostQuantumReality")
	}
	if flagFor(kernel.TypeSingbox, "1.12.0") {
		t.Fatal("sing-box hosting VLESS+Reality must NOT set PostQuantumReality")
	}
}
