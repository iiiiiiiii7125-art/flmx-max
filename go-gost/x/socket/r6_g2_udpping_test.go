package socket

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

// R6-L4-01：agent UdpPing 契约改版——UDP 无通用回声协议、只 Dial
// 不发包，拨通不构成连通性结论。可拨目标必须回未验证形态
// （Success=false＋Unverified=true），且不得回拨号耗时/0 丢包这类
// 伪造实测值；面板组装层据 unverified 标记（或老 agent 的
// success=true）收口呈现。探测前失败（政策拒绝/DNS 失败）保持
// 真实失败、不带未验证标记（由 r6_b6 与 r5_frm2 两组测试钉住）。

func TestG2HandleUdpPingDialableTargetReturnsUnverified(t *testing.T) {
	// 目标选取与 B6 正向格同模式：政策放行（复用产品谓词）的本机
	// 接口地址＋真实 UDP 监听。UDP Dial 不发包，本机地址必可拨，
	// 不依赖 localPingTargetIP 的 TCP 自拨前提（沙箱 ULA 地址的
	// TCP 自拨会被平台出口拦截、该助手在此跳过）。
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
		resp, herr := w.handleUdpPing(context.Background(), map[string]interface{}{
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
		if !resp.Unverified {
			continue // 该地址不可拨（底层失败为真实失败），换下一个候选
		}
		if resp.Success {
			t.Fatalf("dialable UDP target must not report success (dial proves nothing), got %+v", resp)
		}
		if resp.AverageTime != 0 || resp.PacketLoss != 0 {
			t.Fatalf("unverified response must not carry measured values, got avg=%v loss=%v", resp.AverageTime, resp.PacketLoss)
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		if !strings.Contains(string(raw), `"unverified":true`) {
			t.Fatalf("serialized response must carry unverified:true, got %s", raw)
		}
		if strings.Contains(string(raw), `"success":true`) {
			t.Fatalf("serialized response must not claim success, got %s", raw)
		}
		return
	}
	if !tried {
		t.Skip("no policy-allowed local interface address for UDP probe target")
	}
	t.Skip("no dialable policy-allowed local interface address in this environment")
}

// 对照：TCP 面逐字不变——可拨 TCP 目标仍回实测成功、无未验证标记。
func TestG2HandleTcpPingDialableTargetStillSucceeds(t *testing.T) {
	ip := localPingTargetIP(t)
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	w := &WebSocketReporter{}
	resp, err := w.handleTcpPing(context.Background(), TcpPingRequest{
		IP: ip, Port: port, Count: 2, Timeout: 500,
	})
	if err != nil {
		t.Fatalf("handleTcpPing: %v", err)
	}
	if !resp.Success {
		t.Fatalf("dialable TCP target must still report measured success, got %+v", resp)
	}
	if resp.Unverified {
		t.Fatalf("TCP response must not carry the unverified mark, got %+v", resp)
	}
}
