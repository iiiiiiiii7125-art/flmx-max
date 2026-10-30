package udp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-gost/core/bypass"
)

// R2-A02-001 回归：UDP 逐包 pin 必须按关联备忘。
//
// 背景：Relay.pinTarget 对同一关联的每个包都调 PinDialTarget（内含
// DNS 解析取 ips[0]）；多 A 记录轮询时相邻包可落不同后端 IP，按流钉
// 后端的协议会话漂移，且逐包解析受解析器限速。本测试用轮换答案的
// pinner 坐实：同关联同目标只解析一次且结果恒定；新关联（新 Relay）
// 重新解析——备忘作用域严格限单关联，严禁跨关联全局缓存；失败不进
// 备忘（fail-closed 逐包重试，恢复后可成功）。

type rotatingPinner struct {
	mu    sync.Mutex
	calls map[string]int
}

func (p *rotatingPinner) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (p *rotatingPinner) IsWhitelist() bool { return false }

// PinDialTarget 对 rot.test 每次调用轮换返回两个后端（模拟多 A 轮询）；
// flaky.test 首呼失败、其后成功（验失败不缓存）。
func (p *rotatingPinner) PinDialTarget(_ context.Context, _, addr string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[addr]++
	switch addr {
	case "rot.test:53":
		if p.calls[addr]%2 == 1 {
			return "203.0.113.1:53", nil
		}
		return "203.0.113.2:53", nil
	case "flaky.test:53":
		if p.calls[addr] == 1 {
			return "", errors.New("transient resolver failure")
		}
		return "203.0.113.3:53", nil
	default:
		return addr, nil
	}
}

func (p *rotatingPinner) callCount(addr string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[addr]
}

func TestRelayPinMemoizedPerAssociation(t *testing.T) {
	pinner := &rotatingPinner{}
	ctx := context.Background()

	r := NewRelay(nil, nil).WithBypass(pinner)
	first, err := r.pinTarget(ctx, fakeAddr("rot.test:53"))
	if err != nil {
		t.Fatal(err)
	}
	if first.String() != "203.0.113.1:53" {
		t.Fatalf("first pin = %v, want 203.0.113.1:53", first)
	}
	// 同关联后续包：不得再解析、不得漂移到轮换的另一后端。
	for i := 0; i < 5; i++ {
		got, err := r.pinTarget(ctx, fakeAddr("rot.test:53"))
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != first.String() {
			t.Fatalf("packet %d drifted: got %v, want pinned %v", i, got, first)
		}
	}
	if n := pinner.callCount("rot.test:53"); n != 1 {
		t.Fatalf("PinDialTarget called %d times for one association/target, want 1", n)
	}

	// 新关联（新 Relay）必须重新解析：备忘不跨关联。
	r2 := NewRelay(nil, nil).WithBypass(pinner)
	got, err := r2.pinTarget(ctx, fakeAddr("rot.test:53"))
	if err != nil {
		t.Fatal(err)
	}
	if n := pinner.callCount("rot.test:53"); n != 2 {
		t.Fatalf("new association did not re-pin: calls = %d, want 2", n)
	}
	if got.String() != "203.0.113.2:53" {
		t.Fatalf("new association pin = %v, want freshly resolved 203.0.113.2:53", got)
	}
}

func TestRelayPinFailureNotMemoized(t *testing.T) {
	pinner := &rotatingPinner{}
	ctx := context.Background()
	r := NewRelay(nil, nil).WithBypass(pinner)

	if _, err := r.pinTarget(ctx, fakeAddr("flaky.test:53")); err == nil {
		t.Fatal("first flaky pin must fail")
	}
	got, err := r.pinTarget(ctx, fakeAddr("flaky.test:53"))
	if err != nil {
		t.Fatalf("second flaky pin must retry and succeed, got %v", err)
	}
	if got.String() != "203.0.113.3:53" {
		t.Fatalf("flaky pin = %v, want 203.0.113.3:53", got)
	}
	// 成功后进入备忘。
	if _, err := r.pinTarget(ctx, fakeAddr("flaky.test:53")); err != nil {
		t.Fatal(err)
	}
	if n := pinner.callCount("flaky.test:53"); n != 2 {
		t.Fatalf("PinDialTarget calls for flaky target = %d, want 2 (fail + success, then memoized)", n)
	}
}

func TestPinMemoConcurrentAccess(t *testing.T) {
	memo := &PinMemo{}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "target.test:53"
			for j := 0; j < 100; j++ {
				if _, hit := memo.Lookup(key); !hit {
					memo.Store(key, fakeAddr("203.0.113.7:53"))
				}
			}
		}(i)
	}
	wg.Wait()
	if _, hit := memo.Lookup("target.test:53"); !hit {
		t.Fatal("memo lost entry after concurrent stores")
	}
}
