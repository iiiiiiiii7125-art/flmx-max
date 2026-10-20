package health

import (
	"net"
	"testing"
)

// F-023 变体（A6 handoff A6-3）：监控侧 isMonitorBlockedIP 与 SecureFetch
// 的 isPrivateIP 同型缺口——IsUnspecified 只命中 0.0.0.0 单地址，
// 0.0.0.0/8 其余地址未进黑名单，面板本机 TCP 探测可被导向本机服务。
// 修复前本测试失败。
func TestIsMonitorBlockedIPCoversZeroSlash8F023(t *testing.T) {
	for _, s := range []string{"0.0.0.0", "0.1.2.3", "0.255.255.255"} {
		if !isMonitorBlockedIP(net.ParseIP(s)) {
			t.Fatalf("isMonitorBlockedIP(%s) must be true (0.0.0.0/8 整段)", s)
		}
	}
	if isMonitorBlockedIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("isMonitorBlockedIP(8.8.8.8) must stay false (公网)")
	}
}
