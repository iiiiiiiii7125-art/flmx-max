package service

import (
	"net"
	"sync"
	"testing"

	"github.com/go-gost/core/service"
)

// 停机两遍换出回归（审计 A-T-02）：program.stopRuntime 先全集注
// 销关停采样来源，再经 FlushServicesTrafficTwice 两遍换出——第
// 一遍之后排空中的连接仍可能把最后字节计入 stats，第二遍必须
// 将其承接。旧逐服务交错单遍顺序下这批尾量无人再换、随进程退
// 出丢失。本测试以 fake 服务在第一遍换出后注入排空字节钉住两
// 遍语义。（编排本体在 go-gost 根包 program.stopRuntime；根包
// main.go 的 init 含 flag.Parse，测试二进制无法在该包落点，故
// 助手与测试同置本包。）

type fakeFlushTwiceService struct {
	mu        sync.Mutex
	pending   int64 // stats 里尚未换出的字节
	captured  int64 // 已被 FlushTraffic 换出的总量
	flushes   int
	afterTake func(s *fakeFlushTwiceService) // 第一遍换出后的排空注入钩子
}

func (f *fakeFlushTwiceService) Serve() error   { return nil }
func (f *fakeFlushTwiceService) Addr() net.Addr { return nil }
func (f *fakeFlushTwiceService) Close() error   { return nil }
func (f *fakeFlushTwiceService) FlushTraffic() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured += f.pending
	f.pending = 0
	f.flushes++
	if f.flushes == 1 && f.afterTake != nil {
		f.afterTake(f)
	}
}

func TestFlushServicesTrafficTwiceCatchesLateArrivingTail(t *testing.T) {
	a := &fakeFlushTwiceService{pending: 100}
	a.afterTake = func(s *fakeFlushTwiceService) {
		// 排空中的连接在第一遍换出后才把最后字节计入 stats。
		s.pending += 40
	}
	b := &fakeFlushTwiceService{pending: 7}

	FlushServicesTrafficTwice(map[string]service.Service{
		"svc-a": a,
		"svc-b": b,
	}, 0)

	if a.captured != 140 {
		t.Fatalf("第一遍换出后计入的尾量应被第二遍承接: captured=%d, want 140", a.captured)
	}
	if b.captured != 7 {
		t.Fatalf("常规尾量应全量换出: captured=%d, want 7", b.captured)
	}
	if a.flushes != 2 || b.flushes != 2 {
		t.Fatalf("每服务应恰两遍换出: a=%d b=%d", a.flushes, b.flushes)
	}
}
