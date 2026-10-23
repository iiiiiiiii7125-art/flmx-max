package handler

import (
	"strings"
	"testing"

	"go-backend/internal/kernel"
)

// R8 批 1（R8X-001）：ENC 版本门下限更正为 25.9.5 的逐面边界断言。
// 依据 = Xray 官方二进制逐版二分（25.8.29 对面板 ENC 产物双面 exit 23、
// 25.9.5 起 exit 0），原始日志 audit-out-132/r8/R8-FIX-B1-version-bisect.log
// 与 R8-D-enc-version-probe.log 逐格一致。
func TestR8B1ENCVersionGateBoundary(t *testing.T) {
	in := &kernel.Inbound{Protocol: kernel.ProtocolVLESS,
		Settings: kernel.Settings{EncryptionAuth: kernel.EncryptionAuthX25519}}
	ep := &kernel.Endpoint{Inbound: *in}

	for _, v := range []string{"25.6.8", "25.8.29", "25.9.4"} {
		if msg := kernelVersionGate(kernel.TypeXray, v, in); msg == "" || !strings.Contains(msg, "25.9.5 及以上") {
			t.Fatalf("入站自有门：Xray %s 须拒绝且文案含「25.9.5 及以上」，got %q", v, msg)
		}
		if msg := encDialGate(kernel.TypeXray, v, ep); msg == "" || !strings.Contains(msg, "25.9.5 及以上") {
			t.Fatalf("ENC 拨号门：Xray %s 须拒绝且文案含「25.9.5 及以上」，got %q", v, msg)
		}
	}
	// 合法区间上界全放行对照（防过修）：25.9.5 起含 latest 全放行。
	for _, v := range []string{"25.9.5", "25.9.10", "26.10.21", "latest"} {
		if msg := kernelVersionGate(kernel.TypeXray, v, in); msg != "" {
			t.Fatalf("入站自有门：Xray %s 须放行，got %q", v, msg)
		}
		if msg := encDialGate(kernel.TypeXray, v, ep); msg != "" {
			t.Fatalf("ENC 拨号门：Xray %s 须放行，got %q", v, msg)
		}
	}
}
