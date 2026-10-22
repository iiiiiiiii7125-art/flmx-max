package kernel

import (
	"encoding/base64"
	"testing"
)

// R1 X-P2-1 (C-01, BC-04): ValidateImportHost 此前只拦字面 IP，
// localhost 与非规范 IP 编码（整数/十六进制/八进制/简写）全部经
// 主机名分支放行，面板直拨构成回环扫描面。本测试逐条钉死拒绝，
// 并以 example.com / 规范 IP 作防过修正对照。

func TestR1B5ValidateImportHostRejectsNonCanonical(t *testing.T) {
	bad := []string{
		"localhost",
		"LOCALHOST",
		"foo.localhost",
		"a.b.localhost",
		"2130706433",  // 127.0.0.1 的整数编码
		"0x7f.1",      // 十六进制段 + 简写
		"0x7f000001",  // 单段十六进制
		"0177.0.0.1",  // 八进制首段
		"127.1",       // 简写回环
		"127.000.0.1", // 前导零（八进制歧义）
	}
	for _, host := range bad {
		if err := ValidateImportHost(host); err == nil {
			t.Errorf("ValidateImportHost(%q) = nil, want error", host)
		}
	}
	good := []string{
		"example.com",
		"10.0.0.5",
		"203.0.113.5",
		"jp.example.com",
	}
	for _, host := range good {
		if err := ValidateImportHost(host); err != nil {
			t.Errorf("ValidateImportHost(%q) = %v, want nil (防过修正)", host, err)
		}
	}
}

// TestR1B5ParseImportRejectsNonCanonicalHosts 走完整导入路径：
// 坏主机必须 nodes=0 且带错误，好主机仍可导入。
func TestR1B5ParseImportRejectsNonCanonicalHosts(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw"))
	for _, host := range []string{"localhost", "2130706433", "0x7f.1", "0177.0.0.1", "127.1"} {
		link := "ss://" + userinfo + "@" + host + ":8388#bad"
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("ParseImport(host=%q): nodes=%d errs=%v, want 0 nodes with error", host, len(nodes), errs)
		}
	}
	for _, host := range []string{"example.com", "10.0.0.5"} {
		link := "ss://" + userinfo + "@" + host + ":8388#good"
		nodes, errs := ParseImport(link)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Errorf("ParseImport(host=%q): nodes=%d errs=%v, want 1 node (防过修正)", host, len(nodes), errs)
		}
	}
}
