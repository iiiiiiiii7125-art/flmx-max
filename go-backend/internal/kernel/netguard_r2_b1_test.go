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
