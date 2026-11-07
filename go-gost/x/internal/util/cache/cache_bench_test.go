package cache

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkCacheGetHit measures the hot read path used by the router
// route cache: a Get that hits a live entry.
func BenchmarkCacheGetHit(b *testing.B) {
	c := NewCache(time.Minute)
	for i := 0; i < 1024; i++ {
		c.Set(fmt.Sprintf("dst-%d", i), NewItem(i, time.Minute))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Get("dst-512")
	}
}

// BenchmarkCacheSet measures the write path (cache miss fill).
func BenchmarkCacheSet(b *testing.B) {
	c := NewCache(time.Minute)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set(fmt.Sprintf("dst-%d", i%4096), NewItem(i, time.Minute))
	}
}
