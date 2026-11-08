package resolver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-gost/core/logger"
	ctxvalue "github.com/go-gost/x/ctx"
	"github.com/miekg/dns"
)

const (
	defaultTTL = 60 * time.Second

	// maxCacheItems is the hard bound on retained resolver cache
	// entries. Resolver caches live for the life of the process (the
	// local resolver and the DNS handler are never recycled), and
	// their keys are driven by external query traffic, so an
	// unbounded cache grows monotonically — the P1-07 grow-only
	// family. 8192 matches the bound the FIX-LONGTAIL lane applied
	// to the router's identity cache (sdCache), which has the same
	// key shape: one entry per distinct name an external party
	// chooses to ask about.
	maxCacheItems = 8192

	// cacheCleanupInterval bounds how often Store runs its amortized
	// sweep of expired entries, mirroring the util/cache base.
	cacheCleanupInterval = time.Minute
)

type CacheKey string

// NewCacheKey generates resolver cache key from question of dns query.
func NewCacheKey(q *dns.Question) CacheKey {
	if q == nil {
		return ""
	}
	key := fmt.Sprintf("%s%s.%s", q.Name, dns.Class(q.Qclass).String(), dns.Type(q.Qtype).String())
	return CacheKey(key)
}

type cacheItem struct {
	msg *dns.Msg
	ts  time.Time
	ttl time.Duration
}

func (i *cacheItem) expired(now time.Time) bool {
	return now.Sub(i.ts) >= i.ttl
}

// Cache is a DNS response cache with per-entry TTL expiry.
//
// Reclamation (P1-07 family: the previous sync.Map implementation
// only ever Stored — expired entries were ignored on the read side by
// callers keying off the returned ttl, but the entries, each holding
// a full copy of a dns.Msg, were never deleted, so the map grew
// monotonically for the life of the process):
//
//   - Load returns an expired entry's ttl as a non-positive value
//     exactly as before (callers treat that as a miss) and now also
//     deletes the entry, so the common read path is itself a
//     reclamation path. RefreshTTL on an expired entry likewise
//     drops it instead of resurrecting it.
//   - Store runs an amortized sweep of expired entries at most once
//     per cacheCleanupInterval. There is no janitor goroutine.
//   - Store enforces a hard cap (maxCacheItems) by evicting
//     arbitrary entries; an evicted entry is semantically identical
//     to an expired one — the caller simply re-resolves on the next
//     miss.
type Cache struct {
	mu          sync.Mutex
	items       map[CacheKey]*cacheItem
	lastCleanup time.Time
	logger      logger.Logger
}

func NewCache() *Cache {
	return &Cache{
		items: make(map[CacheKey]*cacheItem),
	}
}

func (c *Cache) WithLogger(log logger.Logger) *Cache {
	c.logger = log
	return c
}

func (c *Cache) Load(ctx context.Context, key CacheKey) (msg *dns.Msg, ttl time.Duration) {
	c.mu.Lock()
	item, ok := c.items[key]
	if ok && item.expired(time.Now()) {
		// Reclaim on the read path; the returned zero values are
		// exactly the miss shape callers already handle.
		delete(c.items, key)
		ok = false
	}
	var (
		stored *dns.Msg
		ts     time.Time
		rawTTL time.Duration
	)
	if ok {
		// Snapshot under the lock: RefreshTTL rewrites item.ts, so
		// no field of the shared item may be read after unlocking.
		// stored itself is never mutated after Store (RefreshTTL
		// only touches ts), so copying from it below is race-free.
		stored, ts, rawTTL = item.msg, item.ts, item.ttl
	}
	c.mu.Unlock()
	if !ok {
		return
	}

	msg = stored.Copy()
	for i := range msg.Answer {
		d := uint32(time.Since(ts).Seconds())
		if msg.Answer[i].Header().Ttl > d {
			msg.Answer[i].Header().Ttl -= d
		} else {
			msg.Answer[i].Header().Ttl = 1
		}
	}
	ttl = rawTTL - time.Since(ts)

	if log := c.logger; log != nil && log.IsLevelEnabled(logger.DebugLevel) {
		if sid := ctxvalue.SidFromContext(ctx); sid != "" {
			log = log.WithFields(map[string]any{
				"sid": sid,
			})
		}
		log.Debugf("resolver cache hit: %s, ttl: %v", key, ttl)
	}

	return
}

func (c *Cache) Store(ctx context.Context, key CacheKey, mr *dns.Msg, ttl time.Duration) {
	if key == "" || mr == nil || ttl < 0 {
		return
	}

	if ttl == 0 {
		for _, answer := range mr.Answer {
			v := time.Duration(answer.Header().Ttl) * time.Second
			if ttl == 0 || ttl > v {
				ttl = v
			}
		}
		if ttl == 0 {
			ttl = defaultTTL
		}
	} else {
		for i := range mr.Answer {
			mr.Answer[i].Header().Ttl = uint32(ttl.Seconds())
		}
	}

	c.mu.Lock()
	c.items[key] = &cacheItem{
		msg: mr.Copy(),
		ts:  time.Now(),
		ttl: ttl,
	}
	c.maybeCleanupLocked()
	c.enforceLimitLocked()
	c.mu.Unlock()

	if log := c.logger; log != nil && log.IsLevelEnabled(logger.DebugLevel) {
		if sid := ctxvalue.SidFromContext(ctx); sid != "" {
			log = log.WithFields(map[string]any{
				"sid": sid,
			})
		}
		log.Debugf("resolver cache store: %s, ttl: %v", key, ttl)
	}
}

func (c *Cache) RefreshTTL(key CacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.items[key]
	if !ok {
		return
	}
	if item.expired(time.Now()) {
		// An expired entry is dead weight: drop it rather than
		// resurrecting it, so a late refresh cannot pin an entry
		// past its TTL.
		delete(c.items, key)
		return
	}
	item.ts = time.Now()
}

// maybeCleanupLocked drops expired entries at most once per
// cacheCleanupInterval. Caller must hold mu.
func (c *Cache) maybeCleanupLocked() {
	now := time.Now()
	if now.Sub(c.lastCleanup) < cacheCleanupInterval {
		return
	}
	c.lastCleanup = now
	for k, item := range c.items {
		if item.expired(now) {
			delete(c.items, k)
		}
	}
}

// enforceLimitLocked keeps the entry count at or below maxCacheItems
// by evicting arbitrary entries. Caller must hold mu.
func (c *Cache) enforceLimitLocked() {
	for k := range c.items {
		if len(c.items) <= maxCacheItems {
			break
		}
		delete(c.items, k)
	}
}

// len reports the number of retained entries. It exists for tests
// and diagnostics; production callers do not need it.
func (c *Cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
