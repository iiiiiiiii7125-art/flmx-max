package handler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// FIX-D / D6-04：内核状态轮询的有限并发 helper——并发不得超过上限，
// 每个 id 都必须执行恰好一次。
func TestFixdRunParallelNodeTasksBounded(t *testing.T) {
	const total = 40
	ids := make([]int64, total)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	var running, maxRunning atomic.Int32
	var mu sync.Mutex
	seen := make(map[int64]int)
	runParallelNodeTasks(ids, kernelStatusParallel, func(id int64) {
		cur := running.Add(1)
		for {
			m := maxRunning.Load()
			if cur <= m || maxRunning.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		seen[id]++
		mu.Unlock()
		running.Add(-1)
	})
	if got := maxRunning.Load(); got > kernelStatusParallel {
		t.Fatalf("max concurrency = %d, want <= %d", got, kernelStatusParallel)
	}
	if len(seen) != total {
		t.Fatalf("executed %d ids, want %d", len(seen), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("id %d executed %d times", id, n)
		}
	}

	// 空列表与非法 limit 不得挂死/崩溃。
	runParallelNodeTasks(nil, 0, func(int64) { t.Error("must not run") })
}

// FIX-D / D6-07：通知配置缓存——写路径失效后立即读到新值，
// TTL 内重复读不回源（同一 map 实例）。
func TestFixdNotifyConfigCacheInvalidation(t *testing.T) {
	h, r := setupFixdNotifyHandler(t, newFixdWebhook(t))

	if _, _, ok := h.notifyConfig(notifyEventNodeOffline); !ok {
		t.Fatal("offline event should be enabled via setup config")
	}
	first, err := h.cachedConfigs()
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.cachedConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatal("cache must serve repeated reads")
	}

	// 直接改库（未经失效）在 TTL 内仍是旧值——这是缓存语义本身；
	// 配置写入口的失效钩子保证 UI 保存立即生效。
	if err := r.UpsertConfig("notify_enabled", "false", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := h.notifyConfig(notifyEventNodeOffline); !ok {
		t.Fatal("within TTL the stale value is served (expected cache semantics)")
	}
	h.invalidateNotifyConfigCache()
	if _, _, ok := h.notifyConfig(notifyEventNodeOffline); ok {
		t.Fatal("after invalidation the new value must take effect")
	}
}
