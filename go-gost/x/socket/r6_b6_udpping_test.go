package socket

import (
	"net"
	"strings"
	"testing"
)

// R6 批6（R6X-015）：agent UdpPing 镜像 TcpPing——字面量硬拒整集 +
// 解析落点逐 IP 复检，文案同口径「目标地址不允许」。
//
// 红态（修复前）：127.0.0.1 / 0.0.0.0 / localhost 的 UdpPing 回
// success=true；169.254.169.254 无任何政策判定（回底层拨号失败文案，
// 不含「不允许」）。udpPingHost 从不 Write 的事实不变，本批只补
// 谓词与复检、不加发包行为。

func TestR6B6HandleUdpPingRejectsDeniedTargets(t *testing.T) {
	w := &WebSocketReporter{}
	for _, ip := range []string{"127.0.0.1", "0.0.0.0", "localhost", "169.254.169.254"} {
		resp, err := w.handleUdpPing(map[string]interface{}{
			"ip": ip, "port": 53, "count": 1, "timeout": 200,
		})
		if err != nil {
			t.Fatalf("handleUdpPing(%s): %v", ip, err)
		}
		if resp.Success {
			t.Errorf("handleUdpPing(%s) must not succeed", ip)
		}
		if resp.ErrorMessage != "目标地址不允许" {
			t.Errorf("handleUdpPing(%s) ErrorMessage = %q, want 目标地址不允许", ip, resp.ErrorMessage)
		}
	}
}

// 解析落点复检直测：udpPingHost 对解析到回环的主机名整体拒绝。
func TestR6B6UdpPingHostResolvingToLoopbackDenied(t *testing.T) {
	_, _, err := udpPingHost("localhost", 53, 1, 200)
	if err == nil || err.Error() != "目标地址不允许" {
		t.Fatalf("udpPingHost(localhost) err = %v, want 目标地址不允许", err)
	}
}

// 防过修对照：合法公网/私网 UDP 目标不得被政策拒绝——结果（成功或
// 底层拨号失败）与修复前同口径，不得出现「目标地址不允许」。
func TestR6B6HandleUdpPingAllowsPublicPrivateTargets(t *testing.T) {
	w := &WebSocketReporter{}
	for _, ip := range []string{"8.8.8.8", "192.168.1.1", "10.0.0.5", "172.16.0.9"} {
		resp, err := w.handleUdpPing(map[string]interface{}{
			"ip": ip, "port": 53, "count": 1, "timeout": 200,
		})
		if err != nil {
			t.Fatalf("handleUdpPing(%s): %v", ip, err)
		}
		if resp.ErrorMessage == "目标地址不允许" {
			t.Errorf("handleUdpPing(%s) must not be policy-denied (防过修), got %+v", ip, resp)
		}
	}
}

// 防过修正向格：本机政策放行地址（私网/ULA 接口地址，判定复用产品
// 谓词 tcpPingLiteralTargetDenied 防漂移，与 tcp_ping_queue_test 的
// localPingTargetIP 同口径）上的 UDP 监听目标应探测成功，与修复前
// 一致；本机无可用地址时 Skip。
func TestR6B6HandleUdpPingPrivateListenerSucceeds(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	tried := false
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || tcpPingLiteralTargetDenied(ip.String()) {
			continue
		}
		pc, err := net.ListenPacket("udp", net.JoinHostPort(ip.String(), "0"))
		if err != nil {
			continue
		}
		port := pc.LocalAddr().(*net.UDPAddr).Port
		w := &WebSocketReporter{}
		resp, herr := w.handleUdpPing(map[string]interface{}{
			"ip": ip.String(), "port": port, "count": 1, "timeout": 500,
		})
		_ = pc.Close()
		if herr != nil {
			t.Fatalf("handleUdpPing(%s): %v", ip.String(), herr)
		}
		if strings.Contains(resp.ErrorMessage, "不允许") {
			t.Fatalf("policy-allowed local address %s must not be policy-denied, got %q", ip.String(), resp.ErrorMessage)
		}
		tried = true
		if resp.Success {
			return // 正向格成立
		}
		// 该地址在当前沙箱不可自拨（如出口拦截），换下一个候选。
	}
	if !tried {
		t.Skip("no policy-allowed local interface address for UDP probe target")
	}
	t.Skip("no self-dialable policy-allowed local interface address in this environment")
}
