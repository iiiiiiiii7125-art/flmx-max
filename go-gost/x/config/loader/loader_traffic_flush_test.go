package loader_test

import (
	"net"
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	"github.com/go-gost/x/registry"
)

// 换代卸载旧服务前必须先 FlushTraffic（审计 A-T-03）：reload 走
// loader 换代服务时，旧服务 stats 里未被周期采样换出的尾量此前
// 无人承接、随旧 stats 对象一并丢弃（每被换代服务 <1 个采样周
// 期）。修复后 loader 的服务卸载点（register 全量卸载与 Reload
// 失败回滚卸载共用 unloadService）先换出尾量再注销关闭。

type fakeFlushableService struct {
	flushes int
	closed  bool
}

func (f *fakeFlushableService) Serve() error   { return nil }
func (f *fakeFlushableService) Addr() net.Addr { return nil }
func (f *fakeFlushableService) Close() error   { f.closed = true; return nil }
func (f *fakeFlushableService) FlushTraffic()  { f.flushes++ }

func TestLoadFlushesServicesBeforeUnloadingThem(t *testing.T) {
	unlock := config.LockMutation()
	defer unlock()

	reg := registry.ServiceRegistry()
	fake := &fakeFlushableService{}
	if err := reg.Register("loader-flush-fake", fake); err != nil {
		t.Fatal(err)
	}
	defer reg.Unregister("loader-flush-fake")

	// 空配置 Load 仍会全量卸载既有服务（新配置无服务）：卸载前
	// 尾量必须已被换出，且服务被关闭、注销。
	if err := loader.Load(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	if fake.flushes != 1 {
		t.Fatalf("卸载旧服务前应先 FlushTraffic 一次: flushes=%d", fake.flushes)
	}
	if !fake.closed {
		t.Fatal("旧服务应被 Unregister 连带关闭")
	}
	if reg.IsRegistered("loader-flush-fake") {
		t.Fatal("旧服务应已注销")
	}
}
