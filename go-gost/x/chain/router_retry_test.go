package chain

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	ctxvalue "github.com/go-gost/x/ctx"
)

// F-215-C G7: Router retry-engine unit gates (FIX-DESIGN §2 D1/D2/D6,
// §6 G7). Written against the Router's public behaviour only: a fake
// Chainer/Route pair counts selections and dials and records the
// per-attempt context deadlines it is handed, so envelope carry-over,
// backoff charging, the merge floor and the D2 fallback are all
// asserted from observable values, not internals. Small envelope
// values keep the whole file in the seconds range.

var errRetryProbe = errors.New("retry probe dial failure")

type retryAttemptRec struct {
	start       time.Time
	end         time.Time
	deadline    time.Time
	hasDeadline bool
}

func (a retryAttemptRec) slice() time.Duration { return a.deadline.Sub(a.start) }

// retryRoute is a fake chain.Route whose Dial behaviour is scripted
// per call index (0-based) by fn.
type retryRoute struct {
	node     *corechain.Node
	fn       func(call int, ctx context.Context) (net.Conn, error)
	attempts []retryAttemptRec
	ctxs     []context.Context
}

func (s *retryRoute) Dial(ctx context.Context, network, address string, opts ...corechain.DialOption) (net.Conn, error) {
	rec := retryAttemptRec{start: time.Now()}
	if dl, ok := ctx.Deadline(); ok {
		rec.deadline, rec.hasDeadline = dl, true
	}
	s.attempts = append(s.attempts, rec)
	s.ctxs = append(s.ctxs, ctx)
	conn, err := s.fn(len(s.attempts)-1, ctx)
	s.attempts[len(s.attempts)-1].end = time.Now()
	return conn, err
}

func (s *retryRoute) Bind(ctx context.Context, network, address string, opts ...corechain.BindOption) (net.Listener, error) {
	return nil, errRetryProbe
}

func (s *retryRoute) Nodes() []*corechain.Node { return []*corechain.Node{s.node} }

// retryChainer records the exclude list of every route selection and
// delegates the result to fn (nil fn = always the fixed route).
type retryChainer struct {
	route corechain.Route
	fn    func(exclude []string, call int) corechain.Route
	seen  [][]string
}

func (s *retryChainer) Route(ctx context.Context, network, address string, opts ...corechain.RouteOption) corechain.Route {
	ex := append([]string(nil), ctxvalue.ExcludeNodesFromContext(ctx)...)
	s.seen = append(s.seen, ex)
	if s.fn != nil {
		return s.fn(ex, len(s.seen)-1)
	}
	return s.route
}

func failAlways(int, context.Context) (net.Conn, error) { return nil, errRetryProbe }

func retryTestRouter(ch corechain.Chainer, timeout time.Duration, retries int) *Router {
	return NewRouter(
		corechain.ChainRouterOption(ch),
		corechain.TimeoutRouterOption(timeout),
		corechain.RetriesRouterOption(retries),
	)
}

// G7① Envelope carry-over: attempt 1 fails at ~0.2s of a 3s envelope
// with N=3. Attempt 1's cap is E/N = 1s; attempt 2 must inherit the
// unused share: (3 - 0.2 - ~0.2 backoff) / 2 ≈ 1.3s, strictly larger
// than attempt 1's cap; attempt 3 takes the rest, and no attempt's
// deadline may pass the shared envelope end.
func TestRouterRetryEnvelopeCarryover(t *testing.T) {
	node := corechain.NewNode("node-1", "192.0.2.1:8080")
	rt := &retryRoute{node: node, fn: func(call int, ctx context.Context) (net.Conn, error) {
		if call == 0 {
			time.Sleep(200 * time.Millisecond)
		}
		return nil, errRetryProbe
	}}
	r := retryTestRouter(&retryChainer{route: rt}, 3*time.Second, 2)

	t0 := time.Now()
	_, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	if !errors.Is(err, errRetryProbe) {
		t.Fatalf("Dial err = %v, want the probe dial failure", err)
	}
	if len(rt.attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", len(rt.attempts))
	}
	for i, a := range rt.attempts {
		if !a.hasDeadline {
			t.Fatalf("attempt %d context has no deadline", i+1)
		}
	}
	if s := rt.attempts[0].slice(); s < 800*time.Millisecond || s > 1300*time.Millisecond {
		t.Fatalf("attempt 1 cap = %v, want ≈ E/N = 1s", s)
	}
	if s := rt.attempts[1].slice(); s < 1050*time.Millisecond || s > 1700*time.Millisecond {
		t.Fatalf("attempt 2 cap = %v, want ≈ (3s-0.2s-0.2s)/2 = 1.3s carried over", s)
	}
	if rt.attempts[1].slice() <= rt.attempts[0].slice()+100*time.Millisecond {
		t.Fatalf("attempt 2 cap %v did not grow over attempt 1 cap %v: unused share not carried over",
			rt.attempts[1].slice(), rt.attempts[0].slice())
	}
	if rt.attempts[2].slice() <= rt.attempts[1].slice()+300*time.Millisecond {
		t.Fatalf("attempt 3 cap %v, want the whole remainder (> attempt 2 cap %v)",
			rt.attempts[2].slice(), rt.attempts[1].slice())
	}
	if dl := rt.attempts[2].deadline; dl.After(t0.Add(3*time.Second + 250*time.Millisecond)) {
		t.Fatalf("attempt 3 deadline %v passes the 3s envelope end", dl)
	}
}

// G7② Backoff is charged to the envelope: with a blackhole route
// (every attempt runs to its own cap), the inter-attempt gaps show
// the 200ms/400ms backoffs, yet the total stays inside E — the second
// attempt's cap already shrank by attempt 1's cap plus the backoff,
// and the last deadline never passes t0+E.
func TestRouterRetryBackoffChargedToEnvelope(t *testing.T) {
	node := corechain.NewNode("node-1", "192.0.2.1:8080")
	rt := &retryRoute{node: node, fn: func(call int, ctx context.Context) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	r := retryTestRouter(&retryChainer{route: rt}, 2*time.Second, 2)

	t0 := time.Now()
	_, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatal("Dial to a blackhole route must fail")
	}
	if len(rt.attempts) != 3 {
		t.Fatalf("attempts = %d, want 3", len(rt.attempts))
	}
	if elapsed < 1750*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Fatalf("total dial time = %v, want ≈ the 2s envelope (backoffs included, not added on top)", elapsed)
	}
	if gap := rt.attempts[1].start.Sub(rt.attempts[0].end); gap < 100*time.Millisecond || gap > 500*time.Millisecond {
		t.Fatalf("gap before attempt 2 = %v, want ≈ 200ms backoff (±20%% jitter)", gap)
	}
	if gap := rt.attempts[2].start.Sub(rt.attempts[1].end); gap < 250*time.Millisecond || gap > 700*time.Millisecond {
		t.Fatalf("gap before attempt 3 = %v, want ≈ 400ms backoff (±20%% jitter)", gap)
	}
	if rt.attempts[1].slice() >= rt.attempts[0].slice() {
		t.Fatalf("attempt 2 cap %v not smaller than attempt 1 cap %v: backoff not deducted from the remainder",
			rt.attempts[1].slice(), rt.attempts[0].slice())
	}
	if dl := rt.attempts[2].deadline; dl.After(t0.Add(2*time.Second + 250*time.Millisecond)) {
		t.Fatalf("attempt 3 deadline %v passes the 2s envelope end", dl)
	}
}

// G7③ D2 fallback: a single-node hop excludes its only node after
// attempt 1 fails, so the plain reselection is empty. The router must
// peel off its own advisory exclude, reselect the same node and
// really dial it again — not spin out an emptyRouteError.
func TestRouterRetryFallbackReselectsSingleNode(t *testing.T) {
	node := corechain.NewNode("node-1", "192.0.2.1:8080")
	client, server := net.Pipe()
	defer server.Close()
	rt := &retryRoute{node: node, fn: func(call int, ctx context.Context) (net.Conn, error) {
		if call == 0 {
			return nil, errRetryProbe
		}
		return client, nil
	}}
	// Hop behaviour: a node on the exclude list selects nothing.
	ch := &retryChainer{fn: func(exclude []string, call int) corechain.Route {
		if containsAll(exclude, "node-1") {
			return nil
		}
		return rt
	}}
	r := retryTestRouter(ch, 5*time.Second, 2)

	conn, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	if err != nil {
		t.Fatalf("Dial err = %v, want success via the D2 fallback reselection", err)
	}
	if errors.Is(err, ErrEmptyRoute) {
		t.Fatal("Dial ended in ErrEmptyRoute instead of redialling the single node")
	}
	defer conn.Close()
	if len(rt.attempts) != 2 {
		t.Fatalf("dials = %d, want 2 (attempt 2 must really dial the reselected node)", len(rt.attempts))
	}
	if len(ch.seen) < 3 {
		t.Fatalf("route selections = %d, want ≥3 (attempt 1, attempt 2 full-exclude, attempt 2 fallback)", len(ch.seen))
	}
	if !containsAll(ch.seen[1], "node-1", "192.0.2.1:8080") {
		t.Fatalf("attempt 2 first selection exclude = %v, want attempt 1's node on it", ch.seen[1])
	}
	if containsAll(ch.seen[2], "node-1") {
		t.Fatalf("fallback selection exclude = %v, want the advisory own-exclude peeled off", ch.seen[2])
	}
}

// G7④ D6 classification: three failure classes must not be retried —
// exactly one attempt each and the original error returned as-is.
func TestRouterRetryNoRetryClassification(t *testing.T) {
	t.Run("GuardRejection", func(t *testing.T) {
		errGuard := errors.New("retry guard rejection")
		node := corechain.NewNode("node-1", "192.0.2.1:8080")
		rt := &retryRoute{node: node, fn: failAlways}
		r := retryTestRouter(&retryChainer{route: rt}, 5*time.Second, 2)
		guardCalls := 0
		r.SetDialTargetGuard(func(ctx context.Context, network, address string) (string, error) {
			guardCalls++
			return "", errGuard
		})

		_, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
		if !errors.Is(err, errGuard) {
			t.Fatalf("Dial err = %v, want the guard error as-is", err)
		}
		if guardCalls != 1 {
			t.Fatalf("guard calls = %d, want 1 (no retry of a deterministic verdict)", guardCalls)
		}
		if len(rt.attempts) != 0 {
			t.Fatalf("dials = %d, want 0 behind a rejected guard", len(rt.attempts))
		}
	})

	t.Run("ParentContextCancelled", func(t *testing.T) {
		node := corechain.NewNode("node-1", "192.0.2.1:8080")
		rt := &retryRoute{node: node, fn: func(call int, ctx context.Context) (net.Conn, error) {
			time.Sleep(200 * time.Millisecond)
			return nil, errRetryProbe
		}}
		r := retryTestRouter(&retryChainer{route: rt}, 5*time.Second, 2)

		parent, cancel := context.WithCancel(context.Background())
		time.AfterFunc(60*time.Millisecond, cancel)
		start := time.Now()
		_, err := r.Dial(parent, "tcp", "203.0.113.1:443")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Dial err = %v, want the parent context error as-is", err)
		}
		if len(rt.attempts) != 1 {
			t.Fatalf("attempts = %d, want 1 (a cancelled parent ends the call)", len(rt.attempts))
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Dial took %v after parent cancellation, want a prompt return", elapsed)
		}
	})

	t.Run("EmptyRouteAfterFallback", func(t *testing.T) {
		node := corechain.NewNode("node-1", "192.0.2.1:8080")
		rt := &retryRoute{node: node, fn: failAlways}
		// Attempt 1 selects the node and fails; every later selection
		// is empty even with the advisory exclude peeled off (the
		// candidate set is genuinely gone).
		ch := &retryChainer{fn: func(exclude []string, call int) corechain.Route {
			if call == 0 {
				return rt
			}
			return nil
		}}
		r := retryTestRouter(ch, 5*time.Second, 2)

		start := time.Now()
		_, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
		if !errors.Is(err, ErrEmptyRoute) {
			t.Fatalf("Dial err = %v, want ErrEmptyRoute as-is", err)
		}
		if len(rt.attempts) != 1 {
			t.Fatalf("attempts = %d, want 1 (empty-after-fallback does not loop)", len(rt.attempts))
		}
		if len(ch.seen) < 3 {
			t.Fatalf("route selections = %d, want ≥3 (the fallback reselection must have been tried)", len(ch.seen))
		}
		if containsAll(ch.seen[2], "node-1") {
			t.Fatalf("fallback selection exclude = %v, want the advisory own-exclude peeled off", ch.seen[2])
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Dial took %v on an empty route, want fail-closed promptness", elapsed)
		}
	})

	t.Run("EmptyRouteFromStart", func(t *testing.T) {
		ch := &retryChainer{fn: func(exclude []string, call int) corechain.Route { return nil }}
		r := retryTestRouter(ch, 5*time.Second, 2)

		_, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
		if !errors.Is(err, ErrEmptyRoute) {
			t.Fatalf("Dial err = %v, want ErrEmptyRoute as-is", err)
		}
		if len(ch.seen) != 1 {
			t.Fatalf("route selections = %d, want 1 (no retries against an empty candidate set)", len(ch.seen))
		}
	})
}

// G7⑤ Merge floor: when the even share of the remaining envelope is
// below 300ms, the current attempt takes the whole remainder and is
// the last one. E=2.4s with N=10 gives a 240ms share from the start,
// so the very first attempt must already be the merged last one.
func TestRouterRetryMergeFloor(t *testing.T) {
	node := corechain.NewNode("node-1", "192.0.2.1:8080")
	rt := &retryRoute{node: node, fn: failAlways}
	r := retryTestRouter(&retryChainer{route: rt}, 2400*time.Millisecond, 9)

	start := time.Now()
	_, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	if !errors.Is(err, errRetryProbe) {
		t.Fatalf("Dial err = %v, want the probe dial failure", err)
	}
	if len(rt.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 merged last attempt (share 240ms < 300ms floor)", len(rt.attempts))
	}
	if s := rt.attempts[0].slice(); s < 1900*time.Millisecond || s > 2900*time.Millisecond {
		t.Fatalf("merged attempt cap = %v, want the whole ≈2.4s remainder, not the 240ms share", s)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("Dial took %v, want a single fast-failing merged attempt", elapsed)
	}
}

// G7⑥ Cancel transfer is unchanged under the retry engine: after a
// failed attempt 1, attempt 2 succeeds — its context stays alive for
// exactly as long as the returned conn and is cancelled when the conn
// closes (cancelOnCloseConn, D3-07). Extended by AUDIT-R217 B-01: the
// aliveness check crosses the successful attempt's slice boundary —
// with E=2.4s/N=3 attempt 2's slice is ≈1.1s, so the check runs at
// ≈1.6s into the call, past the slice but inside the envelope; the
// context must still be alive there (promotion to the envelope
// deadline), and Close must still cancel it.
func TestRouterRetrySuccessCancelTransfersToConn(t *testing.T) {
	node := corechain.NewNode("node-1", "192.0.2.1:8080")
	client, server := net.Pipe()
	defer server.Close()
	rt := &retryRoute{node: node, fn: func(call int, ctx context.Context) (net.Conn, error) {
		if call == 0 {
			return nil, errRetryProbe
		}
		return client, nil
	}}
	r := retryTestRouter(&retryChainer{route: rt}, 2400*time.Millisecond, 2)

	conn, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if len(rt.attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (fail then succeed)", len(rt.attempts))
	}
	okCtx := rt.ctxs[1]
	if err := okCtx.Err(); err != nil {
		t.Fatalf("successful attempt's context cancelled while its conn is open: %v", err)
	}
	// Cross the slice boundary: 300ms past attempt 2's own deadline,
	// still inside the 2.4s envelope.
	if wait := time.Until(rt.attempts[1].deadline) + 300*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if err := okCtx.Err(); err != nil {
		t.Fatalf("successful attempt's context died at its slice (%v) while its conn is open: %v",
			rt.attempts[1].slice(), err)
	}
	conn.Close()
	if err := okCtx.Err(); err != context.Canceled {
		t.Fatalf("successful attempt's context not released when the conn closed: %v", err)
	}
}
