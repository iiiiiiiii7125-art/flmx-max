package chain

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	"github.com/go-gost/core/resolver"
)

// A02-001 回归：Router 必须在拨号落点对最终解析结果复核——判定与
// 拨号两次独立解析的分叉（服务解析器答私网、判定答公网）在落点
// 被拦截；无解析器时主机名在落点被钉死，不留给直连拨号器二次
// 解析。

type fixedResolver struct {
	ips []net.IP
	err error
}

func (r fixedResolver) Resolve(_ context.Context, _, _ string, _ ...resolver.Option) ([]net.IP, error) {
	return r.ips, r.err
}

// echoServer 起一个回环 echo 服务，返回地址。
func echoServer(t *testing.T) (addr string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := c.Read(buf); err == nil {
					_, _ = c.Write(buf)
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestRouterDialGuardBlocksResolverPrivateAnswer(t *testing.T) {
	// 服务解析器被重绑定 DNS 控制：拨号解析得到回环（echo 在此
	// 监听），判定面若看的是另一个解析器的公网答案就会放行。
	// guard 在落点复核解析结果 → 必须拒绝且不得连上 echo。
	echoAddr, closeFn := echoServer(t)
	defer closeFn()
	_, port, _ := net.SplitHostPort(echoAddr)

	r := NewRouter(corechain.ResolverRouterOption(fixedResolver{
		ips: []net.IP{net.ParseIP("127.0.0.1")},
	}))
	r.SetDialTargetGuard(func(_ context.Context, network, address string) (string, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return "", err
		}
		if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
			return "", errors.New("guard: non-public dial target")
		}
		return address, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := r.Dial(ctx, "tcp", "rebind.test:"+port)
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("dial to resolver-supplied loopback address must be blocked by the guard")
	}
	if !strings.Contains(err.Error(), "guard") {
		t.Fatalf("expected guard rejection, got %v", err)
	}
}

func TestRouterDialGuardPinsHostnameBeforeDial(t *testing.T) {
	// 无服务解析器：Resolve 原样返回主机名，旧路径靠 DefaultRoute
	// 的 net 拨号器二次解析。guard 把主机名钉死为已验证字面量后
	// 才允许拨号——断言 guard 看到的是主机名、且拨号确实连到
	// guard 钉死的回环 echo。
	echoAddr, closeFn := echoServer(t)
	defer closeFn()

	var guardSaw string
	r := NewRouter()
	r.SetDialTargetGuard(func(_ context.Context, network, address string) (string, error) {
		guardSaw = address
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return "", err
		}
		if host != "pin.test" {
			return "", errors.New("guard: unexpected host " + host)
		}
		return echoAddr, nil // 钉死到已验证地址
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := r.Dial(ctx, "tcp", "pin.test:12345")
	if err != nil {
		t.Fatalf("pinned dial should succeed: %v", err)
	}
	defer conn.Close()
	if guardSaw != "pin.test:12345" {
		t.Fatalf("guard should see the unresolved hostname, saw %q", guardSaw)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := conn.Read(buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo over pinned dial failed: %v %q", err, buf)
	}
}

func TestRouterDialWithoutGuardUnchanged(t *testing.T) {
	// 无 guard 的 Router 行为不变：直连回环 echo 成功。
	echoAddr, closeFn := echoServer(t)
	defer closeFn()

	r := NewRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := r.Dial(ctx, "tcp", echoAddr)
	if err != nil {
		t.Fatalf("unguarded dial: %v", err)
	}
	conn.Close()
}
