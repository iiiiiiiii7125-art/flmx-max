package socket

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// R4 批7（R4X-008 agent 侧 / R4X-020）：
//   - ServiceMonitorCheck 补硬拒子集复检（只取硬拒层：元数据精确集 ∪
//     169.254/16 ∪ 未指定 ∪ 本地 NAT64，翻译内嵌同判）；私网/回环/
//     CGNAT/保留/组播/链路本地 NOT 在拒绝集（LAN 监控正当用途）。
//   - Traceroute 发送前字面量复检（与 TcpPing 同口径整集）。
//   - 主机名目标在 agent 解析后以同口径复检解析落点（TcpPing 家族）。
//
// 红态（修复前）：serviceMonitorTargetHardDenied 不存在（编译红）；
// handleServiceMonitorCheck 对元数据目标返回底层 dial 错误而非政策
// 拒绝；handleTraceroute 对 127.0.0.1 真实追踪成功；handleTcpPing
// 对 localhost（解析到回环）真实拨号成功。

func TestR4B7ServiceMonitorHardDenySet(t *testing.T) {
	deny := []string{
		"169.254.169.254",     // 云元数据
		"169.254.0.23",        // 169.254/16 整段
		"100.100.100.200",     // 阿里云元数据
		"fd00:ec2::254",       // AWS IPv6 元数据
		"0.0.0.0", "::",       // 未指定
		"64:ff9b::a9fe:a9fe",  // NAT64 内嵌元数据
		"2002:a9fe:a9fe::1",   // 6to4 内嵌元数据
		"64:ff9b:1::808:808",  // 本地 NAT64 整段
		"::ffff:169.254.169.254", // mapped 元数据
	}
	for _, h := range deny {
		if !serviceMonitorTargetHardDenied(h) {
			t.Errorf("serviceMonitorTargetHardDenied(%q) = false, want true", h)
		}
	}
	allow := []string{
		"127.0.0.1", "::1", // 回环：节点监控本机/LAN 服务正当
		"10.0.0.1", "192.168.1.1", "172.16.0.9", // 私网
		"100.64.0.1",   // CGNAT
		"fe80::1",      // 链路本地（非硬拒层）
		"0.1.2.3",      // 保留段（非硬拒层，监控面不得顺手禁）
		"224.0.0.1",    // 组播（同上）
		"240.0.0.1",    // 保留（同上）
		"8.8.8.8", "1.1.1.1", "fd00::1", "2001:db8::1", // 公网/ULA
		"example.com", // 主机名不在字面量复检范围
		"",            // 空由调用方格式校验负责
	}
	for _, h := range allow {
		if serviceMonitorTargetHardDenied(h) {
			t.Errorf("serviceMonitorTargetHardDenied(%q) = true, want false (防过修)", h)
		}
	}
}

func TestR4B7HandleServiceMonitorCheckHardDeny(t *testing.T) {
	w := &WebSocketReporter{}
	for _, target := range []string{"169.254.169.254:80", "169.254.0.23:443", "[fd00:ec2::254]:80"} {
		res, err := w.handleServiceMonitorCheck(map[string]interface{}{
			"monitorId": 1, "type": "tcp", "target": target, "timeoutSec": 1,
		})
		if err != nil {
			t.Fatalf("handleServiceMonitorCheck(%s): %v", target, err)
		}
		if res.Success {
			t.Errorf("handleServiceMonitorCheck(%s) must not succeed", target)
		}
		if !strings.Contains(res.ErrorMessage, "不允许") {
			t.Errorf("handleServiceMonitorCheck(%s) must be policy-rejected before dial, got %q", target, res.ErrorMessage)
		}
	}
	// ICMP 分支同拒（目标为裸主机形态）。
	res, err := w.handleServiceMonitorCheck(map[string]interface{}{
		"monitorId": 2, "type": "icmp", "target": "169.254.169.254", "timeoutSec": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || !strings.Contains(res.ErrorMessage, "不允许") {
		t.Errorf("icmp monitor to metadata must be policy-rejected, got %+v", res)
	}
}

// LAN/回环监控不得被硬拒子集误杀：对本机监听端口的 TCP 监控应成功。
func TestR4B7HandleServiceMonitorCheckLANAllowed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	w := &WebSocketReporter{}
	res, err := w.handleServiceMonitorCheck(map[string]interface{}{
		"monitorId": 3, "type": "tcp", "target": ln.Addr().String(), "timeoutSec": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Errorf("loopback LAN monitor must succeed, got %+v", res)
	}
}

func TestR4B7HandleTracerouteRejectsDeniedLiterals(t *testing.T) {
	w := &WebSocketReporter{}
	for _, host := range []string{"169.254.169.254", "127.0.0.1"} {
		_, err := w.handleTraceroute(map[string]interface{}{"host": host, "maxHops": 3})
		if err == nil || !strings.Contains(err.Error(), "不允许") {
			t.Errorf("handleTraceroute(%s) must be rejected before sending, got err=%v", host, err)
		}
	}
}

// 主机名解析落点复检：localhost 解析到回环，TcpPing 必须在拨号前拒绝
//（修复前会真实拨号并成功）。
func TestR4B7HandleTcpPingHostnameResolvingToLoopbackDenied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	w := &WebSocketReporter{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := w.handleTcpPing(ctx, map[string]interface{}{
		"ip": "localhost", "port": port, "count": 1, "timeout": 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Fatalf("TcpPing to localhost (resolves to loopback) must not succeed")
	}
	if !strings.Contains(resp.ErrorMessage, "不允许") {
		t.Fatalf("TcpPing to localhost must be rejected by resolved-address check, got %q", resp.ErrorMessage)
	}
}
