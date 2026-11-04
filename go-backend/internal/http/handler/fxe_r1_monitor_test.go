package handler

import (
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R1-09-L2 回归：storeResult 的时间戳守卫与落库门单飞——
// ① 较旧快照不得覆盖缓存中较新的快照，也不得落库；
// ② 同一节流窗口内并发 storeResult 至多写一行历史。
func TestTunnelQualityStoreResultGuardAndGate(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fxe_tq.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	p := &tunnelQualityProber{handler: h}

	countRows := func(tunnelID int64) int64 {
		var n int64
		if err := r.DB().Table("tunnel_quality").Where("tunnel_id = ?", tunnelID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	cachedTs := func(tunnelID int64) int64 {
		v, ok := p.cache.Load(tunnelID)
		if !ok {
			return -1
		}
		return v.(*tunnelQualitySnapshot).Timestamp
	}

	base := time.Now().UnixMilli()

	// 首份快照：落缓存并写一行历史（lastDBWrite 从 0 起、必过门）。
	p.storeResult(&tunnelQualitySnapshot{TunnelID: 77, Timestamp: base, Success: true})
	if got := cachedTs(77); got != base {
		t.Fatalf("cache ts = %d, want %d", got, base)
	}
	if n := countRows(77); n != 1 {
		t.Fatalf("history rows = %d, want 1", n)
	}

	// 旧快照后到：缓存与历史都不得被它改变。
	p.storeResult(&tunnelQualitySnapshot{TunnelID: 77, Timestamp: base - 5000, Success: false})
	if got := cachedTs(77); got != base {
		t.Fatalf("stale snapshot overwrote cache: ts = %d, want %d", got, base)
	}
	if n := countRows(77); n != 1 {
		t.Fatalf("stale snapshot wrote history: rows = %d, want 1", n)
	}

	// 并发同窗口新快照：缓存收敛到最大时间戳，历史仍只一行。
	var wg sync.WaitGroup
	for i := 1; i <= 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.storeResult(&tunnelQualitySnapshot{TunnelID: 77, Timestamp: base + int64(i), Success: true})
		}(i)
	}
	wg.Wait()
	if got := cachedTs(77); got != base+16 {
		t.Fatalf("cache ts after concurrent stores = %d, want %d", got, base+16)
	}
	if n := countRows(77); n != 1 {
		t.Fatalf("concurrent stores wrote %d history rows in one window, want 1", n)
	}
}
