package health

import (
	"net"
	"os"
	"testing"
)

// sealMonitorDNSForTest 把本包监控校验的 DNS 解析面密封到静态表：
// 表内域名返回钉住的地址，其余一律返回未找到错误。校验类断言的
// 路径（放行/拒绝/解析失败分支）由此与运行环境的真实 DNS 脱钩——
// 此前沙箱 DNS 把 example.com 解析到 198.18.0.0/15 保留段，公网
// 域名用例在未改动的基线上同样假红（非密封测试同族残留）。
func sealMonitorDNSForTest(t *testing.T, table map[string][]net.IP) {
	t.Helper()
	t.Cleanup(PinMonitorLookupIPForTesting(func(host string) ([]net.IP, error) {
		if ips, ok := table[host]; ok {
			return ips, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}))
}

// AUDIT-R2 (F1X-001): 面板本机 TCP 监控目标 SSRF 校验回归测试。
func TestValidateMonitorTCPTarget(t *testing.T) {
	// 域名子例全走密封解析：公网→真公网地址、私网/保留/元数据/
	// 环回→对应段地址、混合→任一命中即拒、未登记→解析失败即拒。
	sealMonitorDNSForTest(t, map[string][]net.IP{
		"example.com":      {net.ParseIP("93.184.216.34")},
		"private.example":  {net.ParseIP("10.0.0.5")},
		"reserved.example": {net.ParseIP("198.18.0.7")},
		"metadata.example": {net.ParseIP("169.254.169.254")},
		"loopback.example": {net.ParseIP("127.0.0.1")},
		"mixed.example":    {net.ParseIP("8.8.8.8"), net.ParseIP("192.168.0.1")},
	})
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"公网 IP 放行", "8.8.8.8:53", false},
		{"公网域名放行", "example.com:443", false},
		{"私网域名拒绝", "private.example:80", true},
		{"保留段域名拒绝", "reserved.example:80", true},
		{"元数据域名拒绝", "metadata.example:80", true},
		{"环回域名拒绝", "loopback.example:80", true},
		{"混合解析任一命中拒绝", "mixed.example:443", true},
		{"不可解析域名拒绝", "missing.example:80", true},
		{"环回 IPv4 拒绝", "127.0.0.1:80", true},
		{"环回 IPv6 拒绝", "[::1]:80", true},
		{"私网 10/8 拒绝", "10.1.2.3:8080", true},
		{"私网 172.16/12 拒绝", "172.16.5.4:8080", true},
		{"私网 192.168/16 拒绝", "192.168.1.1:8080", true},
		{"链路本地拒绝", "169.254.10.20:80", true},
		{"元数据地址拒绝", "169.254.169.254:80", true},
		{"CGNAT 拒绝", "100.64.0.1:80", true},
		{"IPv6 私有拒绝", "[fc00::1]:80", true},
		{"未指定地址拒绝", "0.0.0.0:80", true},
		{"组播拒绝", "224.0.0.1:80", true},
		{"缺端口拒绝", "8.8.8.8", true},
		{"端口越界拒绝", "8.8.8.8:99999", true},
		{"端口为零拒绝", "8.8.8.8:0", true},
		{"空主机拒绝", ":80", true},
		// 注：不可解析域名的 fail-closed 已由密封解析确定性断言
		// （见上「不可解析域名拒绝」：密封面对未登记域名恒返回错误）。
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateMonitorTCPTarget(c.target)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateMonitorTCPTarget(%q) err=%v, wantErr=%v", c.target, err, c.wantErr)
			}
		})
	}
}

// AUDIT-R2 (F1X-001): allowlist 放行通道回归测试。
func TestValidateMonitorTCPTargetAllowlist(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "10.9.0.0/16")
	if err := ValidateMonitorTCPTarget("10.9.1.2:80"); err != nil {
		t.Fatalf("allowlisted IP 被拒绝: %v", err)
	}
	if err := ValidateMonitorTCPTarget("10.10.1.2:80"); err == nil {
		t.Fatal("非 allowlist 私网 IP 被放行")
	}
	os.Unsetenv("FLMX_FETCH_PRIVATE_ALLOWLIST")
}
