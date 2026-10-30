package udp

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/bypass"
)

// A02-001 回归：UDP 中继的逐包目标若是主机名（SOCKS5 域名头等），
// Bypass.Contains 与 PacketConn.WriteTo 各自解析会留下重绑定缝。
// bypass 实现 DialTargetPinner 时，中继必须把目标钉死为已验证的
// IP 字面量再 WriteTo；钉死失败则丢包（fail-closed）。

type fakeAddr string

func (a fakeAddr) Network() string { return "udp" }
func (a fakeAddr) String() string  { return string(a) }

type stubPinnerBypass struct {
	blocked bool
}

func (s stubPinnerBypass) Contains(context.Context, string, string, ...bypass.Option) bool {
	return s.blocked
}
func (s stubPinnerBypass) IsWhitelist() bool { return false }
func (s stubPinnerBypass) PinDialTarget(_ context.Context, _, addr string) (string, error) {
	switch addr {
	case "target.test:53":
		return "203.0.113.7:53", nil
	case "bad.test:53":
		return "", errors.New("pin denied")
	default:
		return addr, nil
	}
}

type plainBypass struct{ blocked bool }

func (s plainBypass) Contains(context.Context, string, string, ...bypass.Option) bool {
	return s.blocked
}
func (s plainBypass) IsWhitelist() bool { return false }

type fakePacketConn struct {
	mu       sync.Mutex
	writes   []net.Addr
	writeSig chan struct{}
}

func (c *fakePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return 0, nil, errors.New("unused")
}
func (c *fakePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, addr)
	c.mu.Unlock()
	if c.writeSig != nil {
		select {
		case c.writeSig <- struct{}{}:
		default:
		}
	}
	return len(b), nil
}
func (c *fakePacketConn) Close() error                       { return nil }
func (c *fakePacketConn) LocalAddr() net.Addr                { return fakeAddr("0.0.0.0:0") }
func (c *fakePacketConn) SetDeadline(time.Time) error        { return nil }
func (c *fakePacketConn) SetReadDeadline(time.Time) error    { return nil }
func (c *fakePacketConn) SetWriteDeadline(time.Time) error   { return nil }
func (c *fakePacketConn) writtenAddrs() []net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]net.Addr(nil), c.writes...)
}

// scriptedConn 按脚本应答 ReadFrom：第 i 次调用走 reads[i]，超出后阻塞
// 在 gate 上直到测试结束（Close 时以错误收尾）。
type scriptedConn struct {
	fakePacketConn
	mu     sync.Mutex
	calls  int
	reads  []func() (int, net.Addr, error)
	onCall func(call int)
}

func (c *scriptedConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	if c.onCall != nil {
		c.onCall(call)
	}
	var fn func() (int, net.Addr, error)
	if call <= len(c.reads) {
		fn = c.reads[call-1]
	}
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	select {}
}

func packetFrom(addr net.Addr) func() (int, net.Addr, error) {
	return func() (int, net.Addr, error) { return 4, addr, nil }
}

func TestRelayPinTargetUnit(t *testing.T) {
	r := NewRelay(nil, nil).WithBypass(stubPinnerBypass{})

	// 主机名目标 → 钉死为已验证字面量。
	got, err := r.pinTarget(context.Background(), fakeAddr("target.test:53"))
	if err != nil {
		t.Fatal(err)
	}
	ua, ok := got.(*net.UDPAddr)
	if !ok || ua.IP.String() != "203.0.113.7" || ua.Port != 53 {
		t.Fatalf("expected pinned UDPAddr 203.0.113.7:53, got %v", got)
	}

	// 字面量目标原样通过（不产生 *net.UDPAddr 替换）。
	lit := &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 53}
	got, err = r.pinTarget(context.Background(), lit)
	if err != nil || got != net.Addr(lit) {
		t.Fatalf("literal target must pass through, got %v err %v", got, err)
	}

	// 钉死失败 → 错误（调用方丢包）。
	if _, err := r.pinTarget(context.Background(), fakeAddr("bad.test:53")); err == nil {
		t.Fatal("pin failure must error")
	}

	// 无 pinner 的 bypass：原样通过。
	r2 := NewRelay(nil, nil).WithBypass(plainBypass{})
	got, err = r2.pinTarget(context.Background(), fakeAddr("target.test:53"))
	if err != nil || got.String() != "target.test:53" {
		t.Fatalf("non-pinning bypass must pass through, got %v err %v", got, err)
	}
}

func TestRelayRunWritesToPinnedTargetOnly(t *testing.T) {
	consumed := make(chan struct{})
	var once sync.Once
	pc1 := &scriptedConn{
		reads: []func() (int, net.Addr, error){
			packetFrom(fakeAddr("target.test:53")),
			packetFrom(fakeAddr("bad.test:53")),
		},
		onCall: func(call int) {
			if call == 3 {
				// 第 3 次 ReadFrom 意味着前两包已被处理完。
				once.Do(func() { close(consumed) })
			}
		},
	}
	pc2 := &scriptedConn{
		reads: []func() (int, net.Addr, error){
			func() (int, net.Addr, error) { return 0, nil, errors.New("test done") },
		},
	}

	r := NewRelay(pc1, pc2).WithBypass(stubPinnerBypass{})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	select {
	case <-consumed:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not consume scripted packets")
	}

	writes := pc2.writtenAddrs()
	if len(writes) != 1 {
		t.Fatalf("expected exactly 1 write (bad.test dropped), got %v", writes)
	}
	ua, ok := writes[0].(*net.UDPAddr)
	if !ok || ua.IP.String() != "203.0.113.7" || ua.Port != 53 {
		t.Fatalf("packet must be written to pinned 203.0.113.7:53, got %v", writes[0])
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay Run did not exit after pc2 error")
	}
}
