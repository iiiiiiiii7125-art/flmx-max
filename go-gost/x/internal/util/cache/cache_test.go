package cache

import (
	"fmt"
	"testing"
	"time"
)

// TestCacheCapEviction verifies the hard cap (P1-07): no matter how
// many distinct keys are written, the entry count stays bounded.
func TestCacheCapEviction(t *testing.T) {
	c := NewCacheWithMaxItems(time.Hour, 100)
	for i := 0; i < 500; i++ {
		c.Set(fmt.Sprintf("key-%d", i), NewItem(i, time.Hour))
	}
	if got := c.Len(); got > 100 {
		t.Fatalf("cap violated: Len() = %d, want <= 100", got)
	}
	if got := c.Len(); got == 0 {
		t.Fatal("cap eviction emptied the cache")
	}
}

// TestCacheSweepExpired verifies expired entries are physically
// deleted by the amortized sweep on Set, not merely ignored on Get.
func TestCacheSweepExpired(t *testing.T) {
	c := NewCache(10 * time.Millisecond)
	c.Set("short", NewItem(1, time.Millisecond))
	if got := c.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}

	time.Sleep(30 * time.Millisecond) // past both expiry and cleanup interval

	c.Set("other", NewItem(2, time.Hour)) // triggers the sweep
	if item := c.Get("short"); item != nil {
		t.Fatal("expired entry was not swept")
	}
	if got := c.Len(); got != 1 {
		t.Fatalf("Len() = %d after sweep, want 1", got)
	}
}

// TestCacheDelete verifies explicit deletion.
func TestCacheDelete(t *testing.T) {
	c := NewCache(time.Minute)
	c.Set("k", NewItem(1, time.Minute))
	c.Delete("k")
	if item := c.Get("k"); item != nil {
		t.Fatal("deleted entry still present")
	}
	if got := c.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
}
