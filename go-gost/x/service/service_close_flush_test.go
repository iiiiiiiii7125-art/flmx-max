package service

import (
	"context"
	"errors"
	"net"
	"testing"

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

func (f *fakeCloseListener) Init(metadata.Metadata) error     { return nil }
func (f *fakeCloseListener) Accept() (net.Conn, error)        { return nil, errors.New("closed") }
func (f *fakeCloseListener) Addr() net.Addr                   { return nil }
func (f *fakeCloseListener) Close() error                     { f.closed = true; return nil }

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
