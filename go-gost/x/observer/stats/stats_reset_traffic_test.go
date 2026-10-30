package stats

import (
	"sync"
	"testing"

	"github.com/go-gost/core/observer/stats"
)

// A02-004 回归：ResetTraffic 必须按快照量原子扣减。旧契约是调用方
// 先 Get 算剩余、再由 ResetTraffic Store 回写——Get 与 Store 之间
// 数据面新计入的字节被覆盖丢失。本测试以确定性时序复现该窗口：
// 快照后、扣减前计入的字节必须保留。

func TestResetTrafficSubtractsSnapshotAtomically(t *testing.T) {
	s := NewStats(true).(*Stats)

	s.Add(stats.KindInputBytes, 100)
	s.Add(stats.KindOutputBytes, 200)

	// 观察者取快照（与 service.observeStats 相同读法）。
	snapIn := s.Get(stats.KindInputBytes)
	snapOut := s.Get(stats.KindOutputBytes)

	// 窗口内数据面继续计入（旧实现会在随后的 Store 中丢掉这部分）。
	s.Add(stats.KindInputBytes, 30)
	s.Add(stats.KindOutputBytes, 40)

	// 按快照量扣减：剩余必须是窗口内新增的 30/40。
	s.ResetTraffic(snapIn, snapOut)

	if got := s.Get(stats.KindInputBytes); got != 30 {
		t.Fatalf("input bytes after reset: got %d, want 30 (snapshot 100 deducted, concurrent 30 kept)", got)
	}
	if got := s.Get(stats.KindOutputBytes); got != 40 {
		t.Fatalf("output bytes after reset: got %d, want 40 (snapshot 200 deducted, concurrent 40 kept)", got)
	}
}

func TestResetTrafficConcurrentAddsAreNotLost(t *testing.T) {
	s := NewStats(true).(*Stats)

	const workers = 8
	const perWorker = 5000

	s.Add(stats.KindInputBytes, 1000)
	snapshot := s.Get(stats.KindInputBytes)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				s.Add(stats.KindInputBytes, 1)
			}
		}()
	}
	// 与并发 Add 同时做快照扣减：无论交错如何，总量必须守恒。
	s.ResetTraffic(snapshot, 0)
	wg.Wait()

	want := uint64(workers * perWorker)
	if got := s.Get(stats.KindInputBytes); got != want {
		t.Fatalf("concurrent adds lost: got %d, want %d", got, want)
	}
}

func TestResetTrafficExactDrainToZero(t *testing.T) {
	s := NewStats(true).(*Stats)
	s.Add(stats.KindInputBytes, 42)
	s.Add(stats.KindOutputBytes, 42)
	s.ResetTraffic(42, 42)
	if got := s.Get(stats.KindInputBytes); got != 0 {
		t.Fatalf("input: got %d, want 0", got)
	}
	if got := s.Get(stats.KindOutputBytes); got != 0 {
		t.Fatalf("output: got %d, want 0", got)
	}
}
