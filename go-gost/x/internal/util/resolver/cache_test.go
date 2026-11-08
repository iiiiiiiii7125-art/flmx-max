package resolver

import (
	"context"
	"fmt"
	"github.com/go-gost/core/logger"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func testMsg(name string, ttl uint32) *dns.Msg {
	m := new(dns.Msg)
	m.Answer = []dns.RR{
		&dns.A{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    ttl,
			},
			A: []byte{192, 0, 2, 1},
		},
	}
	return m
}

func testKey(name string) CacheKey {
	return NewCacheKey(&dns.Question{
		Name:   name,
		Qclass: dns.ClassINET,
		Qtype:  dns.TypeA,
	})
}

// TestCacheWithoutLogger covers the nil-logger path: the original
// implementation dereferenced c.logger unconditionally in Load/Store,
// so a Cache used without WithLogger panicked. The reworked
// implementation guards the nil logger instead. Production
// constructor chains (resolver.go, handler/dns) always attach one.
func TestCacheWithoutLogger(t *testing.T) {
	c := NewCache()
	ctx := context.Background()
	key := testKey("nolog.example.")

	c.Store(ctx, key, testMsg("nolog.example.", 60), time.Minute)
	if msg, _ := c.Load(ctx, key); msg == nil {
		t.Fatal("expected hit without logger attached")
	}
}

func TestCacheHitSemanticsUnchanged(t *testing.T) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()
	key := testKey("example.com.")

	c.Store(ctx, key, testMsg("example.com.", 120), 2*time.Minute)

	msg, ttl := c.Load(ctx, key)
	if msg == nil {
		t.Fatal("expected cache hit")
	}
	if ttl <= 0 || ttl > 2*time.Minute {
		t.Fatalf("ttl out of range: %v", ttl)
	}
	if got := msg.Answer[0].Header().Ttl; got == 0 || got > 120 {
		t.Fatalf("answer ttl not aged correctly: %d", got)
	}

	// The stored message must be insulated from caller mutation of
	// the loaded copy (Copy semantics preserved).
	msg.Answer[0].Header().Ttl = 9999
	msg2, _ := c.Load(ctx, key)
	if msg2.Answer[0].Header().Ttl == 9999 {
		t.Fatal("loaded copy aliases stored message")
	}

	if msg, _ := c.Load(ctx, testKey("absent.example.")); msg != nil {
		t.Fatal("expected miss for unknown key")
	}
}

func TestCacheExpiredEntryReclaimedOnLoad(t *testing.T) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()
	key := testKey("short.example.")

	c.Store(ctx, key, testMsg("short.example.", 1), time.Nanosecond)
	// Let the entry expire without manipulating internals; the
	// returned ttl would already be non-positive, which is how the
	// pre-fix implementation signalled expiry to callers.
	time.Sleep(2 * time.Millisecond)

	msg, ttl := c.Load(ctx, key)
	if msg != nil || ttl > 0 {
		t.Fatalf("expected miss shape for expired entry, got msg=%v ttl=%v", msg != nil, ttl)
	}
	if n := c.len(); n != 0 {
		t.Fatalf("expired entry not reclaimed on Load: len=%d", n)
	}
}

func TestCacheRefreshTTLKeepsLiveEntryDropsExpired(t *testing.T) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()

	live := testKey("live.example.")
	c.Store(ctx, live, testMsg("live.example.", 60), time.Minute)
	c.RefreshTTL(live)
	if n := c.len(); n != 1 {
		t.Fatalf("live entry dropped by RefreshTTL: len=%d", n)
	}
	if msg, _ := c.Load(ctx, live); msg == nil {
		t.Fatal("live entry missing after RefreshTTL")
	}

	dead := testKey("dead.example.")
	c.Store(ctx, dead, testMsg("dead.example.", 1), time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	c.RefreshTTL(dead)
	if _, ok := c.items[dead]; ok {
		t.Fatal("expired entry resurrected/retained by RefreshTTL")
	}
}

func TestCacheHardCap(t *testing.T) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()

	total := maxCacheItems + 500
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("h%d.example.", i)
		c.Store(ctx, testKey(name), testMsg(name, 300), 5*time.Minute)
	}
	if n := c.len(); n > maxCacheItems {
		t.Fatalf("cache exceeded hard cap: len=%d cap=%d", n, maxCacheItems)
	}
	if n := c.len(); n == 0 {
		t.Fatal("cache empty after cap enforcement")
	}
}

func TestCacheSweepReclaimsExpired(t *testing.T) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("s%d.example.", i)
		c.Store(ctx, testKey(name), testMsg(name, 1), time.Nanosecond)
	}
	time.Sleep(2 * time.Millisecond)

	// A Store past the cleanup interval must sweep the expired
	// entries even if nothing ever Loads them.
	c.lastCleanup = time.Time{}
	live := testKey("fresh.example.")
	c.Store(ctx, live, testMsg("fresh.example.", 60), time.Minute)

	if n := c.len(); n != 1 {
		t.Fatalf("sweep left %d entries, want only the live one", n)
	}
	if msg, _ := c.Load(ctx, live); msg == nil {
		t.Fatal("live entry lost in sweep")
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				name := fmt.Sprintf("w%d-%d.example.", w, i%64)
				key := testKey(name)
				c.Store(ctx, key, testMsg(name, 60), time.Minute)
				c.Load(ctx, key)
				c.RefreshTTL(key)
			}
		}(w)
	}
	wg.Wait()

	if n := c.len(); n > maxCacheItems {
		t.Fatalf("cache exceeded hard cap under concurrency: len=%d", n)
	}
}

// TestCacheConcurrentSameKey hammers a single key from many goroutines
// with the exact production access mix (Load + Store + RefreshTTL, the
// latter issued per request by the DNS handler) so the race detector
// can catch any unsynchronized access to the shared cache item.
func TestCacheConcurrentSameKey(t *testing.T) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()
	key := testKey("hot.example.")
	c.Store(ctx, key, testMsg("hot.example.", 60), time.Minute)

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				c.Load(ctx, key)
				c.RefreshTTL(key)
				if i%97 == 0 {
					c.Store(ctx, key, testMsg("hot.example.", 60), time.Minute)
				}
			}
		}()
	}
	wg.Wait()

	if msg, _ := c.Load(ctx, key); msg == nil {
		t.Fatal("hot entry missing after concurrent access")
	}
}

func BenchmarkCacheLoadHit(b *testing.B) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()
	key := testKey("bench.example.")
	c.Store(ctx, key, testMsg("bench.example.", 300), 5*time.Minute)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg, _ := c.Load(ctx, key)
		if msg == nil {
			b.Fatal("unexpected miss")
		}
	}
}

func BenchmarkCacheStore(b *testing.B) {
	c := NewCache().WithLogger(logger.Default())
	ctx := context.Background()
	msg := testMsg("bench.example.", 300)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := testKey(fmt.Sprintf("b%d.example.", i%1024))
		c.Store(ctx, key, msg, 5*time.Minute)
	}
}
