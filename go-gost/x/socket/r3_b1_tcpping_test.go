package socket

import (
	"strings"
	"testing"
)

// R3 批1（R3X-011 纵深）：节点 handleTcpPing 对下发目标补同口径
// 字面量复检（PolicyHardDenyOrLoopback 等价集：硬拒 + 回环 + 保留 +
// 组播 + 链路本地）。红态（修复前）：下列目标会实际发起拨号，
// ErrorMessage 为拨号失败文案、不含「不允许」。
func TestR3B1HandleTcpPingRejectsDeniedLiterals(t *testing.T) {
	w := &WebSocketReporter{}
	deny := []string{
		"169.254.169.254", // 云元数据
		"127.0.0.1",       // 回环
		"::1",             // 回环 v6
		"0.1.2.3",         // 保留段
		"224.0.0.1",       // 组播
		"fe80::1",         // 链路本地
	}
	for _, ip := range deny {
		resp, err := w.handleTcpPing(map[string]interface{}{
			"ip": ip, "port": 80, "count": 1, "timeout": 200,
		})
		if err != nil {
			t.Fatalf("handleTcpPing(%s): %v", ip, err)
		}
		if resp.Success {
			t.Errorf("handleTcpPing(%s) must not succeed", ip)
		}
		if !strings.Contains(resp.ErrorMessage, "不允许") {
			t.Errorf("handleTcpPing(%s) must be rejected before dial with 不允许, got %q", ip, resp.ErrorMessage)
		}
	}
}

// 字面量复检的判定集直测（含翻译形态与防过修对照，随实现同批落：
// tcpPingLiteralTargetDenied 为本批新增函数，无旧实现可跑红，其
// 端到端红态由上一条 handleTcpPing 用例承担）。
func TestR3B1TcpPingLiteralTargetDeniedSet(t *testing.T) {
	deny := []string{
		"169.254.0.23", "0.0.0.0", "::", "240.0.0.1", "198.18.0.1",
		"192.0.0.1", "100::1", "ff02::1",
		"64:ff9b::a9fe:a9fe", // NAT64 内嵌元数据
		"64:ff9b::1:203",     // NAT64 内嵌 0.1.2.3（保留）
		"2002:a9fe:a9fe::1",  // 6to4 内嵌元数据
		"::ffff:127.0.0.1",   // mapped 回环
		"64:ff9b:1::808:808", // 本地 NAT64 整段
	}
	for _, ip := range deny {
		if !tcpPingLiteralTargetDenied(ip) {
			t.Errorf("tcpPingLiteralTargetDenied(%q) = false, want true", ip)
		}
	}
	allow := []string{
		"10.0.0.5", "192.168.1.1", "172.16.0.9", // 私网（节点间链路目标）
		"100.64.0.1",         // CGNAT
		"8.8.8.8", "1.1.1.1", // 公网
		"fd00::1",      // ULA 私网
		"2001:db8::1",  // 文档段（与面板分类器同判为 public）
		"www.bing.com", // 主机名不在字面量复检范围
	}
	for _, ip := range allow {
		if tcpPingLiteralTargetDenied(ip) {
			t.Errorf("tcpPingLiteralTargetDenied(%q) = true, want false", ip)
		}
	}
}
