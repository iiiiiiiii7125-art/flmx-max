package socket

import (
	"context"
	"net"
	"strings"
	"testing"
)

// FRM2 回归（RM-VERIFY S5 观察②）：handleTcpPing/handleUdpPing 的旧组装
// 只在成功分支给 AverageTime/PacketLoss 赋值，探测函数在失败时算出的
// 实测值（全失败 packetLoss=100）被错误分支丢弃，死目标回传成 0% 丢包，
// 面板据此呈现与实测相反的数字（L4-04：诊断数必须真实）。
// 本文件钉死：全失败 → 回传 packetLoss=100；部分失败 → 实际丢包率。

// closedPortOn 返回 host 上一个当前无监听的端口（绑后立即释放）。
func closedPortOn(t *testing.T, host string) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestFRM2TcpPingFullFailureReportsPacketLoss100(t *testing.T) {
	host := localPingTargetIP(t)
	port := closedPortOn(t, host)
	w := &WebSocketReporter{}
	resp, err := w.handleTcpPing(context.Background(), TcpPingRequest{
		IP: host, Port: port, Count: 4, Timeout: 500,
	})
	if err != nil {
		t.Fatalf("handleTcpPing: %v", err)
	}
	if resp.Success {
		t.Fatal("全失败目标不应报 success")
	}
	if !strings.Contains(resp.ErrorMessage, "所有TCP连接尝试都失败") {
		t.Fatalf("ErrorMessage 应为探测函数的全失败原因，got %q", resp.ErrorMessage)
	}
	if resp.PacketLoss != 100 {
		t.Fatalf("全失败回传 packetLoss = %v, want 100（旧组装丢弃为 0）", resp.PacketLoss)
	}
	if resp.AverageTime != 0 {
		t.Fatalf("全失败回传 averageTime = %v, want 0", resp.AverageTime)
	}
}

func TestFRM2TcpPingPartialFailureReportsActualPacketLoss(t *testing.T) {
	host := localPingTargetIP(t)
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	// 只接第一条连接随即关监听：后续拨号被拒，构成确定的部分失败。
	go func() {
		if conn, err := ln.Accept(); err == nil {
			_ = conn.Close()
		}
		_ = ln.Close()
	}()
	w := &WebSocketReporter{}
	resp, err := w.handleTcpPing(context.Background(), TcpPingRequest{
		IP: host, Port: port, Count: 4, Timeout: 500,
	})
	if err != nil {
		t.Fatalf("handleTcpPing: %v", err)
	}
	if !resp.Success {
		t.Fatalf("部分失败应报 success 并带部分结果，got %+v", resp)
	}
	// 4 次尝试 1 成功 → 丢包率 75%。
	if resp.PacketLoss != 75 {
		t.Fatalf("部分失败回传 packetLoss = %v, want 75", resp.PacketLoss)
	}
}

// UDP 腿同形态：探测前失败（DNS 解析失败）时 udpPingHost 返回
// packetLoss=100，组装层不得丢弃。仅测组装回传，不涉及 UDP 探测语义。
func TestFRM2UdpPingFailureReportsPacketLoss100(t *testing.T) {
	const badHost = "nonexistent.invalid"
	if _, err := net.LookupHost(badHost); err == nil {
		t.Skipf("本机 DNS 居然解析了 %s，无法构造解析失败场景", badHost)
	}
	w := &WebSocketReporter{}
	resp, err := w.handleUdpPing(TcpPingRequest{
		IP: badHost, Port: 53, Count: 4, Timeout: 500,
	})
	if err != nil {
		t.Fatalf("handleUdpPing: %v", err)
	}
	if resp.Success {
		t.Fatal("解析失败目标不应报 success")
	}
	if resp.PacketLoss != 100 {
		t.Fatalf("UDP 解析失败回传 packetLoss = %v, want 100（旧组装丢弃为 0）", resp.PacketLoss)
	}
}
