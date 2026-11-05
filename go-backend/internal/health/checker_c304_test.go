package health

import (
	"context"
	"net"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// C3-04: 本地 TCP 检查的总时长必须被 timeout 一次封顶——多 IP
// 目标不许每 IP 用满一份 timeout（N× 预算）。拨号面用确定性桩
// 模拟黑洞 IP（拨号阻塞至自身 ctx 截止），断言墙钟 ≈ timeout
// 而非 IP 数 × timeout。

func TestCheckTCPSharesOneDeadlineAcrossIPs(t *testing.T) {
	restoreLookup := PinMonitorLookupIPForTesting(func(host string) ([]net.IP, error) {
		return []net.IP{
			net.ParseIP("198.51.100.1"),
			net.ParseIP("198.51.100.2"),
			net.ParseIP("198.51.100.3"),
			net.ParseIP("198.51.100.4"),
		}, nil
	})
	defer restoreLookup()

	prevDial := tcpDialContext
	tcpDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		<-ctx.Done() // 黑洞：无响应直至调用方截止期
		return nil, ctx.Err()
	}
	defer func() { tcpDialContext = prevDial }()

	c := &Checker{}
	result := &model.ServiceMonitorResult{}
	start := time.Now()
	c.checkTCP("example.test:80", time.Second, result)
	elapsed := time.Since(start)

	if result.Success != 0 {
		t.Fatalf("blackholed target must fail, got success=%d", result.Success)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("total check time %v exceeds the single 1s budget shared across 4 IPs (baseline defect: per-IP timeouts sum to 4s)", elapsed)
	}
}
