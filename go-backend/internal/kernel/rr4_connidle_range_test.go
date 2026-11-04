package kernel

import (
	"strings"
	"testing"
)

// RR4-F2-02: ConnIdle 范围格（-1 / 0 / 1-86400）此前只在 Normalize
// 本体内、下发复检的共享谓词 checkConnIdleKernel 只判内核归属——
// Xray 上的越界存量值（999999、-5）在下发面逐字渲染进 policy。
// 范围格已并入共享谓词：创建面与下发面判定集逐字一致。
func TestConnIdleRangeAgreesAcrossFaces(t *testing.T) {
	base := func(seconds int) Inbound {
		return Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			ConnIdleSeconds: seconds}
	}
	for _, seconds := range []int{999999, 86401, -5, -2} {
		in := base(seconds)
		err := CheckSettingsConditions(TypeXray, &in)
		if err == nil || !strings.Contains(err.Error(), "空闲超时须为") {
			t.Errorf("dispatch recheck: connIdle=%d must be range-rejected, got %v", seconds, err)
		}
		norm := base(seconds)
		nerr := Normalize(TypeXray, &norm)
		if nerr == nil || nerr.Error() != err.Error() {
			t.Errorf("faces disagree on connIdle=%d: Normalize=%v, recheck=%v", seconds, nerr, err)
		}
	}
	for _, seconds := range []int{-1, 0, 1, 300, 86400} {
		in := base(seconds)
		if err := CheckSettingsConditions(TypeXray, &in); err != nil {
			t.Errorf("dispatch recheck: connIdle=%d is in range and must pass, got %v", seconds, err)
		}
	}
	// 范围格先行于内核格：非 Xray 上的越界值报范围错误（与 Normalize
	// 既有顺序一致），范围内非零值仍报内核归属错误。
	in := base(999999)
	if err := CheckSettingsConditions(TypeSingbox, &in); err == nil ||
		!strings.Contains(err.Error(), "空闲超时须为") {
		t.Errorf("range cell must win over kernel cell, got %v", err)
	}
	in = base(300)
	if err := CheckSettingsConditions(TypeSingbox, &in); err == nil ||
		!strings.Contains(err.Error(), "仅支持 Xray 内核") {
		t.Errorf("in-range non-zero on sing-box must hit the kernel cell, got %v", err)
	}
}
