package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/listener"
	"github.com/go-gost/core/metadata"
	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
	xstats "github.com/go-gost/x/observer/stats"
)

// 服务关闭尾量收口回归（A-T-03 同机制全集）：服务对象的一切卸载
// 路径（停机、loader 换代、面板/API 驱动的删除/换代/暂停）都经
// Close 或由 Unregister 连带至 Close。此前只有停机路径显式
// FlushTraffic，其余路径下 stats 里未采样尾量随对象丢弃。本测试
// 钉住 Close 本身先承接尾量、重复 Close 无副作用。

type fakeCloseListener struct{ closed bool }

func (f *fakeCloseListener) Init(metadata.Metadata) error { return nil }
func (f *fakeCloseListener) Accept() (net.Conn, error)    { return nil, errors.New("closed") }
func (f *fakeCloseListener) Addr() net.Addr               { return nil }
func (f *fakeCloseListener) Close() error                 { f.closed = true; return nil }

type fakeCloseHandler struct{}

func (fakeCloseHandler) Init(metadata.Metadata) error { return nil }
func (fakeCloseHandler) Handle(context.Context, net.Conn, ...handler.HandleOption) error {
	return nil
}

type fakeCloseObserver struct{}

func (fakeCloseObserver) Observe(context.Context, []observer.Event, ...observer.Option) error {
	return nil
}

var (
	_ listener.Listener = (*fakeCloseListener)(nil)
	_ handler.Handler   = fakeCloseHandler{}
	_ observer.Observer = fakeCloseObserver{}
)

func TestServiceCloseFlushesUnsampledTailTraffic(t *testing.T) {
	// R18-R3-01：本测试经 FlushTraffic 触发 GetGlobalTrafficManager，
	// 其 5s 定时上报 goroutine 此前在整个测试二进制期间泄漏，
	// 后续测试重配 stub 面板时它仍在 encryptReportPayload 里读
	// httpAESCrypto。测试结束即停表（Stop 会 join 该 goroutine）。
	t.Cleanup(func() { GetGlobalTrafficManager().Stop() })

	st := xstats.NewStats(false)
	st.Add(stats.KindInputBytes, 300)
	st.Add(stats.KindOutputBytes, 500)

	ln := &fakeCloseListener{}
	svc := &defaultService{
		name:     "svc-close-flush",
		listener: ln,
		handler:  fakeCloseHandler{},
		status:   &Status{stats: st},
		options:  options{observer: fakeCloseObserver{}},
	}

	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if !ln.closed {
		t.Fatal("Close 应关闭监听")
	}
	// 尾量已交进全局管理器：up=gost OutputBytes、down=InputBytes。
	if up, down := GetGlobalTrafficManager().GetServiceTraffic("svc-close-flush"); up != 500 || down != 300 {
		t.Fatalf("Close 应先承接未采样尾量: up=%d down=%d, want 500/300", up, down)
	}

	// 重复 Close：TakeTraffic 恒为零，不得重复计入。
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if up, down := GetGlobalTrafficManager().GetServiceTraffic("svc-close-flush"); up != 500 || down != 300 {
		t.Fatalf("重复 Close 不得重复承接: up=%d down=%d, want 500/300", up, down)
	}
}

// R18-R3-01 回归：单例上报 goroutine 泄漏——前一测试触发
// GetGlobalTrafficManager 后，5s 定时上报 goroutine 在整个测试二
// 进制期间持续运行；后续测试重配 stub 面板（stubV2Panel 写
// httpAESCrypto）时，它仍在 encryptReportPayload 里读同一加密器
// （aes.go:41 读 vs NewAESCrypto :28 写）。修复：前一测试结束时
// Stop() join 上报 goroutine。本测试把 ticker 加速到 10ms 并在
// 窗口内两次重配 stub，断言停表后上报 goroutine 不再访问 stub。
// 须与前一测试同包全量跑（它先启动单例）；单独 -run 本测试时单
// 例未启动，直接 skip。
func TestR18R301StoppedReporterDoesNotTouchReconfiguredCrypto(t *testing.T) {
	if globalManager == nil || globalManager.reportTicker == nil {
		t.Skip("单例上报器未启动，须与 TestServiceCloseFlushesUnsampledTailTraffic 同包全量跑")
	}
	m := globalManager

	origDo, origURL, origCrypto := reportDo, httpReportURL, httpAESCrypto
	t.Cleanup(func() { reportDo, httpReportURL, httpAESCrypto = origDo, origURL, origCrypto })

	var calls atomic.Int32
	stubV2Panel(t, "r18-r3-01-a", func(env trafficEnvelope) (*http.Response, error) {
		calls.Add(1)
		return v2AckResponse(env)
	})
	// 待上报流量：若 goroutine 泄漏，下一 tick 必走到加密并调用 stub。
	m.addServiceTraffic("svc-r18-r3-01", 10, 20)
	m.reportTicker.Reset(10 * time.Millisecond)
	defer m.reportTicker.Stop()

	// 200ms ≈ 20 个 tick；泄漏的 goroutine 在此窗口内必上报。
	time.Sleep(200 * time.Millisecond)
	// 窗口中途重配 stub（写 httpAESCrypto）：泄漏时与加密读竞态。
	stubV2Panel(t, "r18-r3-01-b", func(env trafficEnvelope) (*http.Response, error) {
		calls.Add(1)
		return v2AckResponse(env)
	})
	time.Sleep(300 * time.Millisecond)

	if n := calls.Load(); n != 0 {
		t.Fatalf("上报 goroutine 在停表后仍访问 stub 面板 %d 次（R18-R3-01 泄漏）", n)
	}
}
