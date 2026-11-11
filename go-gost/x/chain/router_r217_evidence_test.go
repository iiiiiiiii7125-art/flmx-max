package chain

import (
	"context"
	"net"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	corelogger "github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
)

// AUDIT-R217 evidence test (written before the fix; RED on the
// f8b07fd2 candidate). The A-01 companion lives in
// router_r217_a01_test.go (external test package: the real hop
// package cannot be imported from this one).

// B-01(a): a context-retaining transport observes the successful
// attempt's context. With E=2.4s/N=3, attempt 2 succeeds at ~0.2s
// with a ≈1.1s slice: pre-fix the retained ctx is cancelled when the
// slice expires (~1.3s into the call) even though the conn is open;
// the fixed behaviour keeps it alive until the envelope end (2.4s)
// or conn Close, whichever comes first.
func TestRouterR217SuccessCtxPromotedToEnvelope(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())
	newRouter := func() (*Router, *retryRoute) {
		node := corechain.NewNode("node-1", "192.0.2.1:8080")
		rt := &retryRoute{node: node, fn: func(call int, ctx context.Context) (net.Conn, error) {
			if call == 0 {
				return nil, errRetryProbe
			}
			c1, c2 := net.Pipe()
			t.Cleanup(func() { c2.Close() })
			return c1, nil
		}}
		return retryTestRouter(&retryChainer{route: rt}, 2400*time.Millisecond, 2), rt
	}

	t.Run("AlivePastSliceUntilClose", func(t *testing.T) {
		r, rt := newRouter()
		conn, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		okCtx := rt.ctxs[1]
		// Wait until 300ms past attempt 2's slice deadline, still
		// ~800ms before the 2.4s envelope end.
		wait := time.Until(rt.attempts[1].deadline) + 300*time.Millisecond
		if wait < 0 {
			wait = 0
		}
		time.Sleep(wait)
		if err := okCtx.Err(); err != nil {
			t.Fatalf("successful attempt's ctx died at its slice while its conn is open: %v (slice was %v)", err, rt.attempts[1].slice())
		}
		conn.Close()
		if err := okCtx.Err(); err != context.Canceled {
			t.Fatalf("successful attempt's ctx not released on conn Close: %v", err)
		}
	})

	t.Run("DiesAtEnvelopeEndIfConnLeftOpen", func(t *testing.T) {
		r, rt := newRouter()
		t0 := time.Now()
		conn, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer conn.Close()
		okCtx := rt.ctxs[1]
		time.Sleep(time.Until(t0.Add(2900 * time.Millisecond)))
		if err := okCtx.Err(); err != context.DeadlineExceeded {
			t.Fatalf("successful attempt's ctx err = %v after the envelope end, want DeadlineExceeded", err)
		}
	})
}
