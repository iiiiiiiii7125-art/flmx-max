package handler

import (
	"strings"
	"testing"

	"go-backend/internal/kernel"
)

// 修复轮 2 · 版本门家族（R2-015/R2-016/R2-032）。本文件只用基线已有
// 符号（kernelVersionGate / encDialGate / validateOutboundIPVersion /
// snellMajor / snellServes / 宽松版三函数），使同一文件能在基线树上
// 跑出红灯、修复树上转绿，作为先红后绿证据。

// R2-016（B-sub-03，BC）：硬门对畸形具体版本串 fail-closed。台账点名
// 两形态 × 两门：「25.9」过 ENC 门、「25.5.16-rc1」过 ENC 与 prefer 门
// ——修复前两形态在两门均放行（宽松版解析失败/缺段不视为过低）。
func TestHardGatesRejectMalformedVersions(t *testing.T) {
	enc := &kernel.Inbound{Protocol: kernel.ProtocolVLESS,
		Settings: kernel.Settings{EncryptionAuth: kernel.EncryptionAuthX25519}}
	ep := &kernel.Endpoint{Inbound: *enc}

	for _, v := range []string{"25.9", "25.5.16-rc1"} {
		if msg := kernelVersionGate(kernel.TypeXray, v, enc); msg == "" {
			t.Errorf("ENC 入站自有门：Xray %q 必须拒绝（fail-closed），却放行", v)
		}
		if msg := encDialGate(kernel.TypeXray, v, ep); msg == "" {
			t.Errorf("ENC 拨号门：Xray %q 必须拒绝（fail-closed），却放行", v)
		}
		if err := validateOutboundIPVersion(kernel.TypeXray, v, kernel.OutboundPreferIPv4); err == nil {
			t.Errorf("prefer 门：Xray %q 必须拒绝（fail-closed），却放行", v)
		}
	}
	// 防过修：合法形态与哨兵在两门维持原判。
	for _, v := range []string{"25.9.5", "25.10.21", "26.10.21", "latest", ""} {
		if msg := kernelVersionGate(kernel.TypeXray, v, enc); msg != "" {
			t.Errorf("ENC 入站自有门：Xray %q 须放行，got %q", v, msg)
		}
		if msg := encDialGate(kernel.TypeXray, v, ep); msg != "" {
			t.Errorf("ENC 拨号门：Xray %q 须放行，got %q", v, msg)
		}
	}
	for _, v := range []string{"25.6.8", "25.9", "25.10.21", "latest", ""} {
		// "25.9" 对 prefer 下限 25.6.8 是数值达标（补 0 为 25.9.0），
		// 与 ENC 门缺段拒绝不矛盾：两门下限与段数要求不同。
		if err := validateOutboundIPVersion(kernel.TypeXray, v, kernel.OutboundPreferIPv4); err != nil {
			t.Errorf("prefer 门：Xray %q 须放行，got %v", v, err)
		}
	}
	// 数值过低的具体版本在硬门维持拒绝（严格变体不得放宽真比较）。
	if msg := kernelVersionGate(kernel.TypeXray, "25.9.4", enc); msg == "" || !strings.Contains(msg, "25.9.5 及以上") {
		t.Errorf("ENC 门：25.9.4 须拒绝且文案达意，got %q", msg)
	}
	if err := validateOutboundIPVersion(kernel.TypeXray, "25.6.7", kernel.OutboundPreferIPv6); err == nil {
		t.Error("prefer 门：25.6.7 须拒绝")
	}
}

// R2-016 同族：两段下限的硬门（ACME/AnyTLS/出站 IP 策略）对缺段与
// 段内带后缀形态同样 fail-closed。
func TestHardGatesRejectMalformedTwoSegmentFloors(t *testing.T) {
	acme := &kernel.Inbound{Protocol: kernel.ProtocolVLESS}
	acme.Security.CertMode = kernel.CertModeACME
	anytls := &kernel.Inbound{Protocol: kernel.ProtocolAnyTLS}
	for _, v := range []string{"1", "1.14-rc1"} {
		if msg := kernelVersionGate(kernel.TypeSingbox, v, acme); msg == "" {
			t.Errorf("ACME 门：sing-box %q 必须拒绝，却放行", v)
		}
		if msg := kernelVersionGate(kernel.TypeSingbox, v, anytls); msg == "" {
			t.Errorf("AnyTLS 门：sing-box %q 必须拒绝，却放行", v)
		}
		if err := validateOutboundIPVersion(kernel.TypeSingbox, v, kernel.OutboundIPv4Only); err == nil {
			t.Errorf("出站 IP 策略门：sing-box %q 必须拒绝，却放行", v)
		}
	}
	for _, v := range []string{"1.14", "1.14.3", "latest", ""} {
		if msg := kernelVersionGate(kernel.TypeSingbox, v, acme); msg != "" {
			t.Errorf("ACME 门：sing-box %q 须放行，got %q", v, msg)
		}
	}
}

// R2-015（B-sub-02）：snellMajor 健壮解析——枚举形态逐个钉死。
func TestSnellMajorRobustForms(t *testing.T) {
	for version, want := range map[string]int{
		"4.1.1":   4,
		"4":       4,
		"5.0.1":   5,
		"6.0.0":   6,
		"v6.0.0":  6,  // 剥可选 v 前缀
		"6":       6,  // 裸大版本
		"10.0.0":  10, // 两位数大版本按数值
		"6.0.0b1": 6,  // 首段数字之后的后缀不影响大版本
		" 6.0.0 ": 6,  // TrimSpace 与家族同口径
		"latest":  5,  // 不可解析保守回退 5
		"":        5,
		"V6.0.0":  5, // 口径登记：家族只认小写 v 前缀，不擅自扩（R2-032）
	} {
		if got := snellMajor(version); got != want {
			t.Errorf("snellMajor(%q) = %d, want %d", version, got, want)
		}
	}
	// 端到端：v 前缀形态的真 6.x 不得再被当 5.x 放行 v5 客户端。
	if err := snellServes("v6.0.0", 5); err == nil {
		t.Error("snellServes(v6.0.0, v5 client) must reject")
	}
	if err := snellServes("v6.0.0", 6); err != nil {
		t.Errorf("snellServes(v6.0.0, v6 client) must serve: %v", err)
	}
	if err := snellServes("10.0.0", 6); err != nil {
		t.Errorf("snellServes(10.0.0, v6 client) must serve: %v", err)
	}
}

// R2-032（B-sub-04）：版本家族 TrimSpace 口径对齐——versionBelow /
// versionBelow3 与 versionBelowPadded 同口径先 TrimSpace。此前
// " 1.13 " 在 versionBelow 因首段带空格解析失败被判「不低于」，与
// padded 变体结论相反；对齐后三函数对带空白形态结论一致。畸形形态
// 当前经安装面归一不可达，本条为纯一致性对齐、无行为面变化。
func TestVersionFamilyTrimSpaceAlignment(t *testing.T) {
	if !versionBelow(" 1.13 ", 1, 14) {
		t.Error("versionBelow must TrimSpace like the padded variant")
	}
	if !versionBelow3(" 25.9.4 ", 25, 9, 5) {
		t.Error("versionBelow3 must TrimSpace like the padded variant")
	}
	if versionBelow3(" 25.9.5 ", 25, 9, 5) {
		t.Error("versionBelow3( 25.9.5 ) must not be below after trim")
	}
	if !versionBelowPadded(" 25.6.7 ", 25, 6, 8) {
		t.Error("versionBelowPadded TrimSpace caliber (pre-existing) changed")
	}
}
