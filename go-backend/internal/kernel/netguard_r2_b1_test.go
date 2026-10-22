package kernel

import (
	"encoding/base64"
	"testing"
)

// R2 批1（R2X-005 / R2X-043）：导入主机判定收口回归。
// 红态（修复前）：ip6-localhost 等别名未在名单内、元数据字面量被放行。

func TestR2B1ValidateImportHostLoopbackAliases(t *testing.T) {
	for _, host := range []string{"ip6-localhost", "ip6-loopback", "localhost.localdomain"} {
		if err := ValidateImportHost(host); err == nil {
			t.Fatalf("ValidateImportHost(%q) must be rejected (loopback alias)", host)
		}
	}
}

func TestR2B1ParseImportLoopbackAliasesRejected(t *testing.T) {
	for _, host := range []string{"ip6-localhost", "ip6-loopback", "localhost.localdomain"} {
		link := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw")) + "@" + host + ":8388#x"
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 {
			t.Fatalf("ParseImport(%q) nodes=%d, want 0", host, len(nodes))
		}
		if len(errs) == 0 {
			t.Fatalf("ParseImport(%q) must report an error", host)
		}
	}
}

func TestR2B1ValidateImportHostHardDenyLiterals(t *testing.T) {
	for _, host := range []string{
		"169.254.169.254", "169.254.0.23", "169.254.1.1", // 169.254/16 整段硬拒（R2X-043/026 口径）
		"100.100.100.200",   // 阿里云元数据精确集
		"fd00:ec2::254",     // EC2 IPv6 元数据精确集
		"::ffff:169.254.169.254", // mapped 形态不得绕过
		"64:ff9b::a9fe:a9fe",     // NAT64 内嵌元数据
	} {
		if err := ValidateImportHost(host); err == nil {
			t.Fatalf("ValidateImportHost(%q) must be rejected (hard deny)", host)
		}
	}
}

func TestN01ValidateImportHostLinkLocalV6Rejected(t *testing.T) {
	// N-01：fe80::/10 在导入面与 169.254/16 同样被拒——机理是
	// ValidateImportHost 的严格政策 PolicyHardDenyOrLoopback 补拒
	// ClassLinkLocal（R3X-010 政策分层，非硬拒层），导入面与分享面
	// （共用 ValidateImportHost）同时生效。zone 写法经主机名分支
	// 的字符集校验拒绝，同样不得放行。
	for _, host := range []string{"fe80::1", "fe80::", "febf::ffff", "fe80::1%eth0"} {
		if err := ValidateImportHost(host); err == nil {
			t.Fatalf("ValidateImportHost(%q) must be rejected (link-local v6)", host)
		}
	}
	if _, _, err := ParseImportHostPort("[fe80::1]:8388"); err == nil {
		t.Fatal("ParseImportHostPort([fe80::1]:8388) must be rejected")
	}
	if _, _, err := ParseImportHostPort("fe80::1:8388"); err == nil {
		t.Fatal("ParseImportHostPort(fe80::1:8388) must be rejected")
	}
}

func TestN01ValidateImportHostULAStillAllowed(t *testing.T) {
	// 防过修：ULA fc00::/7 不是链路本地，维持现状放行；公网文档
	// 段 v6 字面量同前。
	for _, host := range []string{"fd00::5", "fc00::1", "fd12:3456::1", "2001:db8::9"} {
		if err := ValidateImportHost(host); err != nil {
			t.Fatalf("ValidateImportHost(%q) must stay allowed: %v", host, err)
		}
	}
}

func TestR2B1ValidateImportHostPrivateLiteralsStillAllowed(t *testing.T) {
	// 防过修：RFC1918 / CGNAT 字面量是合法导入对象（R2X-043 只加硬拒层）。
	for _, host := range []string{"10.0.0.5", "192.168.1.10", "172.16.0.9", "100.64.1.2", "example.com", "8.8.8.8"} {
		if err := ValidateImportHost(host); err != nil {
			t.Fatalf("ValidateImportHost(%q) must stay allowed: %v", host, err)
		}
	}
}

func TestR2B1ParseImportPrivateAndPublicStillWork(t *testing.T) {
	for _, host := range []string{"example.com", "10.0.0.5"} {
		link := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw")) + "@" + host + ":8388#x"
		nodes, errs := ParseImport(link)
		if len(nodes) != 1 {
			t.Fatalf("ParseImport(%q) nodes=%d errs=%v, want 1 node", host, len(nodes), errs)
		}
	}
}
