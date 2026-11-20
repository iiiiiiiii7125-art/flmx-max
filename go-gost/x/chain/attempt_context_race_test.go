package chain

import (
	"context"
	"sync"
	"testing"
	"time"
)

// R18-D4-01 regression: promote racing an already-fired slice timer
// must not resurrect the old timer's finish. The stale callback may be
// blocked on c.mu while promote swaps in a fresh timer; without a
// generation check it then takes the lock, marks the context
// DeadlineExceeded and stops the new timer — so a successfully promoted
// (dial succeeded) context comes back dead.
//
// The race only exists at the slice boundary, so the promote spin
// starts just before the slice deadline: a promote landing early
// simply wins cleanly, and one landing after the timer fired is a
// legitimate expiry. Only a promote that observably extended the
// deadline may never be undone by the stale callback.
func TestAttemptContextPromoteRacesExpiredTimer(t *testing.T) {
	const iterations = 3000
	for i := 0; i < iterations; i++ {
		ctx, actx, cancel := newAttemptContext(context.Background(), 20*time.Millisecond)
		sliceEnd, _ := ctx.Deadline()

		// Wait until just before the slice fires, then spin several
		// promoters across the boundary.
		time.Sleep(19 * time.Millisecond)
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				deadline := time.Now().Add(time.Hour)
				for j := 0; j < 2000; j++ {
					actx.promote(deadline)
					if d, _ := ctx.Deadline(); d.After(sliceEnd.Add(time.Minute)) {
						return
					}
					if ctx.Err() != nil {
						return
					}
				}
			}()
		}
		wg.Wait()
		if d, _ := ctx.Deadline(); d.After(sliceEnd.Add(time.Minute)) {
			// Give an already-fired stale timer callback time to
			// run after promote released the lock: on the buggy
			// code it then marks the context dead.
			time.Sleep(2 * time.Millisecond)
			if err := ctx.Err(); err != nil {
				cancel()
				t.Fatalf("iter %d: promoted past slice end but Err()=%v (stale timer finished the context)", i, err)
			}
		}
		cancel()
	}
}

// R18-D4-01: a promote that lands after the context already ended
// (timer or parent cancel) stays a no-op and never revives it.
func TestAttemptContextPromoteAfterFinishIsNoop(t *testing.T) {
	ctx, actx, cancel := newAttemptContext(context.Background(), time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if err := ctx.Err(); err != context.DeadlineExceeded {
		t.Fatalf("Err()=%v, want DeadlineExceeded before promote", err)
	}
	actx.promote(time.Now().Add(time.Hour))
	if err := ctx.Err(); err != context.DeadlineExceeded {
		t.Fatalf("Err()=%v after late promote, want DeadlineExceeded", err)
	}
	cancel()
	if err := ctx.Err(); err != context.DeadlineExceeded {
		t.Fatalf("Err()=%v after cancel, want first error to win", err)
	}
}
