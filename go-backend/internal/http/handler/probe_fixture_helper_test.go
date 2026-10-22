package handler

import (
	"net"
	"testing"
)

// probeFixtureLocalAddr returns the first non-loopback RFC1918 IPv4
// address assigned to a local interface, for use as a probe fixture
// target (R2 登记 #3 同型迁移，与 tests/contract 的同名 helper 同口径).
//
// 口径来源：R2X-005/R2X-043 —— probe 拨号前过 netguard
// PolicyHardDenyOrLoopback，命中 loopback/硬拒即不拨，故 probe 夹具
// 不能再用 127.0.0.1 字面量 + loopback listener 驱动 up/down 跃迁。
// RFC1918 私网地址不在 probe 的拒绝集内，可作被探测目标；本机无此
// 地址时夹具密封性无法在本地验证，整条用例 Skip，留待 CI/真实
// 网络环境验收（R2 登记 #3）。
func probeFixtureLocalAddr(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("本机无非 loopback RFC1918 地址，probe 夹具密封性待 CI/真实网络验收（R2 登记 #3）: %v", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() {
			continue
		}
		// RFC1918: 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16.
		if ip[0] == 10 || (ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31) || (ip[0] == 192 && ip[1] == 168) {
			return ip.String()
		}
	}
	t.Skipf("本机无非 loopback RFC1918 地址，probe 夹具密封性待 CI/真实网络验收（R2 登记 #3）")
	return ""
}
