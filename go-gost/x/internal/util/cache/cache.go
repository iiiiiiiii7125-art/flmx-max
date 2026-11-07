package cache

import (
	"sync"
	"time"
)

type Item struct {
	v          interface{}
	expiration int64
}

func NewItem(v interface{}, d time.Duration) *Item {
	var expiration int64
	if d > 0 {
		expiration = time.Now().Add(d).UnixNano()
	}

	return &Item{
		v:          v,
		expiration: expiration,
	}
}

func (p *Item) Expired() bool {
	if p == nil {
		return true
	}
	return p.expiration > 0 && time.Now().UnixNano() > p.expiration
}

func (p *Item) Value() interface{} {
	if p == nil {
		return nil
	}
	return p.v
}

// Cache is a key/value store with per-item expiration.
//
// Entries are reclaimed in two ways (P1-07: the previous implementation
// only ever *ignored* expired items on the read side and never deleted
// them, so every cache built on this type grew monotonically for the
// life of the process):
//
//   - Amortized sweep: Set triggers a sweep of expired entries at most
//     once per cleanupInterval. There is no janitor goroutine, so a
//     discarded Cache never leaks one; memory can only grow via Set,
//     and Set is exactly where the sweep runs.
//   - Optional hard cap (maxItems > 0): once the cap is exceeded, Set
//     evicts arbitrary entries until the count fits (Go map iteration
//     order is randomized, so this is effectively random eviction).
//     Evicting a live entry is semantically identical to it expiring:
//     the caller simply recomputes it on the next miss.
type Cache struct {
	items           map[string]*Item
	cleanupInterval time.Duration
	maxItems        int
	lastCleanup     time.Time
	mu              sync.RWMutex
}

func NewCache(cleanupInterval time.Duration) *Cache {
	return &Cache{
		cleanupInterval: cleanupInterval,
		items:           make(map[string]*Item),
	}
}

// NewCacheWithMaxItems is NewCache with a hard bound on the number of
// retained entries. maxItems <= 0 means unbounded (sweep still applies).
func NewCacheWithMaxItems(cleanupInterval time.Duration, maxItems int) *Cache {
	c := NewCache(cleanupInterval)
	c.maxItems = maxItems
	return c
}

func (c *Cache) Set(key string, item *Item) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = item
	c.maybeCleanupLocked()
	c.enforceLimitLocked()
}

func (c *Cache) Get(key string) *Item {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.items[key]
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
}

func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.items)
}

// maybeCleanupLocked drops expired entries at most once per
// cleanupInterval. Caller must hold the write lock.
func (c *Cache) maybeCleanupLocked() {
	if c.cleanupInterval <= 0 {
		return
	}
	now := time.Now()
	if now.Sub(c.lastCleanup) < c.cleanupInterval {
		return
	}
	c.lastCleanup = now
	for k, item := range c.items {
		if item.Expired() {
			delete(c.items, k)
		}
	}
}

// enforceLimitLocked keeps the entry count at or below maxItems by
// evicting arbitrary entries (expired ones included — they are dead
// weight anyway, and reclaiming them in bulk is the sweep's job, so
// this stays O(excess) on the write path). Caller must hold the
// write lock.
func (c *Cache) enforceLimitLocked() {
	if c.maxItems <= 0 {
		return
	}
	for k := range c.items {
		if len(c.items) <= c.maxItems {
			break
		}
		delete(c.items, k)
	}
}
