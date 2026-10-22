package bypass

import (
	"context"
	"testing"
)

// R2 批1（R2X-006 agent 镜像面 + C-S1 子面）：bypass 补翻译/元数据层。
// 红态（修复前）：NAT64/6to4 内嵌非公网地址被当公网 IPv6 放行。

func TestR2B1NoPrivateBypassTranslatedForms(t *testing.T) {
	bp := NewNoPrivateBypass()
	blocked := []string{
		"[64:ff9b::a9fe:a9fe]:80",   // NAT64 内嵌 169.254.169.254
		"[64:ff9b::a00:1]:80",       // NAT64 内嵌 10.0.0.1
		"[64:ff9b::7f00:1]:80",      // NAT64 内嵌 127.0.0.1
		"[64:ff9b:1::808:808]:80",   // 本地 NAT64 整段（内嵌公网也不放行）
		"[64:ff9b:1::a9fe:a9fe]:80", // 本地 NAT64
		"[2002:a9fe:a9fe::1]:80",    // 6to4 内嵌元数据
		"[2002:0a00:0001::1]:80",    // 6to4 内嵌 10.0.0.1
		"[::ffff:169.254.169.254]:80",
		"100.100.100.200:80", // 阿里云元数据精确集
		"[fd00:ec2::254]:80", // EC2 IPv6 元数据精确集
		"240.0.0.1:80",
		"255.255.255.255:80",
	}
	for _, addr := range blocked {
		if !bp.Contains(context.Background(), "tcp", addr) {
			t.Fatalf("expected %s to be blocked", addr)
		}
	}
	allowed := []string{
		"[64:ff9b::808:808]:80",  // NAT64 内嵌公网 8.8.8.8
		"[2002:0808:0808::1]:80", // 6to4 内嵌公网
	}
	for _, addr := range allowed {
		if bp.Contains(context.Background(), "tcp", addr) {
			t.Fatalf("expected %s to be allowed", addr)
		}
	}
}
