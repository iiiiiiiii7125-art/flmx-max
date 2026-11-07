package handler

import (
	"fmt"
	"sync"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// TestKernelApplyLockRecycled verifies P1-07 for kernelApplyLocks:
// entries are reference-counted, mutual exclusion per kernel is
// preserved, and the map returns to empty once all users release.
func TestKernelApplyLockRecycled(t *testing.T) {
	countLocks := func() int {
		n := 0
		kernelApplyLocks.Range(func(_, _ interface{}) bool { n++; return true })
		return n
	}
	if n := countLocks(); n != 0 {
		t.Fatalf("precondition: kernelApplyLocks has %d entries, want 0", n)
	}

	// Sequential use: released entries disappear immediately.
	unlock := lockKernelApply(42)
	unlock()
	unlock() // release is idempotent
	if n := countLocks(); n != 0 {
		t.Fatalf("after release: kernelApplyLocks has %d entries, want 0", n)
	}

	// Concurrent use of one kernel: critical sections never overlap
	// and the entry is recycled at the end.
	var wg sync.WaitGroup
	var inFlight, maxInFlight, total int
	var mu sync.Mutex
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				release := lockKernelApply(7)
				mu.Lock()
				inFlight++
				if inFlight > maxInFlight {
					maxInFlight = inFlight
				}
				mu.Unlock()

				total++ // only safe if the kernel lock truly serializes

				mu.Lock()
				inFlight--
				mu.Unlock()
				release()
			}
		}()
	}
	wg.Wait()
	if maxInFlight != 1 {
		t.Fatalf("kernel lock did not serialize: max in-flight = %d, want 1", maxInFlight)
	}
	if total != 200 {
		t.Fatalf("lost updates under kernel lock: total = %d, want 200", total)
	}
	if n := countLocks(); n != 0 {
		t.Fatalf("after concurrent use: kernelApplyLocks has %d entries, want 0", n)
	}
}

// TestFlowCryptoCacheCap verifies P1-07 for flowCryptoCache: past the
// hard cap the table is swept instead of growing forever, and lookups
// keep working afterwards.
func TestFlowCryptoCacheCap(t *testing.T) {
	flowCryptoCache.Range(func(k, _ interface{}) bool {
		flowCryptoCache.Delete(k)
		return true
	})
	flowCryptoCacheSize.Store(0)

	countEntries := func() int {
		n := 0
		flowCryptoCache.Range(func(_, _ interface{}) bool { n++; return true })
		return n
	}

	for i := 0; i < flowCryptoCacheMaxEntries+100; i++ {
		if c := getOrCreateFlowCrypto(fmt.Sprintf("secret-%d", i)); c == nil {
			t.Fatalf("getOrCreateFlowCrypto returned nil at i=%d", i)
		}
	}
	if n := countEntries(); n > flowCryptoCacheMaxEntries {
		t.Fatalf("flowCryptoCache exceeded cap: %d entries, want <= %d", n, flowCryptoCacheMaxEntries)
	}
	// A repeat lookup still resolves (recreated after a sweep if needed).
	if c := getOrCreateFlowCrypto("secret-0"); c == nil {
		t.Fatal("getOrCreateFlowCrypto returned nil for repeat secret")
	}
}

// TestUncountableSubWarnedRecycle verifies the dedupe entry lifecycle
// for P1-07: the warning fires once per subscription, and deleting
// the entry (as completePendingSubscriptionDelete now does after the
// hard delete) re-arms the warning for a future id reuse.
func TestUncountableSubWarnedRecycle(t *testing.T) {
	id := int64(kernel.MaxUserMarkID) + 4242
	uncountableSubWarned.Delete(id)
	s := &model.Subscription{ID: id, Name: "over-cap"}

	warnUncountableSubscription(s)
	if _, ok := uncountableSubWarned.Load(id); !ok {
		t.Fatal("warning dedupe entry was not recorded")
	}
	uncountableSubWarned.Delete(id)
	if _, ok := uncountableSubWarned.Load(id); ok {
		t.Fatal("warning dedupe entry was not recycled by Delete")
	}
	warnUncountableSubscription(s)
	if _, ok := uncountableSubWarned.Load(id); !ok {
		t.Fatal("warning did not re-arm after recycle")
	}
	uncountableSubWarned.Delete(id)
}
