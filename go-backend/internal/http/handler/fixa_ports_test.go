package handler

import (
	"testing"
)

// F2-F1-02 钉格：转发目标端口数值＋1–65535 范围校验（格式层、全角色）。

func TestFixAValidateRemoteAddrPorts(t *testing.T) {
	for _, addr := range []string{
		"example.com:8080",
		"example.com", // 无端口：不在本校验职责（既有口径放行）
		"example.com:8080/path",
		"http://example.com:8080/path",
		"[2001:db8::1]:443",
		"example.com:1",
		"example.com:65535",
		"a.com:80,b.com:443\nc.com:22",
		"2001:db8::1", // 裸 IPv6 无可识别端口段，本校验沉默（其余面口径不变）
	} {
		if err := validateRemoteAddrPorts(addr); err != nil {
			t.Fatalf("target %q must pass port validation: %v", addr, err)
		}
	}
	for _, addr := range []string{
		"example.com:99999",
		"example.com:65536",
		"example.com:0",
		"example.com:abc",
		"example.com:http",
		"example.com:",
		"a.com:80,b.com:70000",
	} {
		if err := validateRemoteAddrPorts(addr); err == nil {
			t.Fatalf("target %q must be rejected by port validation", addr)
		}
	}
}

