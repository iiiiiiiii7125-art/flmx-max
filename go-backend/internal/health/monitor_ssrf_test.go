package health

import (
	"os"
	"testing"
)

// AUDIT-R2 (F1X-001): 面板本机 TCP 监控目标 SSRF 校验回归测试。
func TestValidateMonitorTCPTarget(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"公网 IP 放行", "8.8.8.8:53", false},
		{"公网域名放行", "example.com:443", false},
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
		// 注：不可解析域名的 fail-closed 行为依赖真实 DNS，
		// 沙箱 DNS 拦截所有查询故不在此断言（ValidateMonitorTCPTarget 对 LookupIP 失败返回错误）。
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
	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "10.9.0.0/16")
	if err := ValidateMonitorTCPTarget("10.9.1.2:80"); err != nil {
		t.Fatalf("allowlisted IP 被拒绝: %v", err)
	}
	if err := ValidateMonitorTCPTarget("10.10.1.2:80"); err == nil {
		t.Fatal("非 allowlist 私网 IP 被放行")
	}
	os.Unsetenv("FLVX_FETCH_PRIVATE_ALLOWLIST")
}
