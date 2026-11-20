package handler

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// R18-B8-02：版本列表缓存（10 分钟）无 singleflight，TTL 到期瞬间
// 并发调用各烧一次未认证 GitHub API 配额。以下测试先于修复落盘
// （先红后绿：旧代码无 fetchReleasesCached 入口，编译即红）。
func TestR18B802ReleasesFetchSingleflight(t *testing.T) {
	key := "releases:test-singleflight-2.1.18"
	releasesCache.Lock()
	delete(releasesCache.entries, key)
	delete(releasesCache.flights, key)
	releasesCache.Unlock()

	var calls atomic.Int64
	fetch := func() ([]githubRelease, error) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond)
		return []githubRelease{{TagName: "2.1.18"}, {TagName: "2.1.17"}}, nil
	}
	const n = 8
	results := make([][]githubRelease, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = fetchReleasesCached(key, fetch)
		}(i)
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("R18-B8-02：8 路并发冷缓存应只拉取 1 次，got %d 次", got)
	}
	for i := range results {
		if errs[i] != nil || len(results[i]) != 2 || results[i][0].TagName != "2.1.18" {
			t.Fatalf("R18-B8-02：等待方应共享同一结果，i=%d err=%v releases=%v", i, errs[i], results[i])
		}
	}
	// 第二轮：缓存命中，不再拉取。
	if _, err := fetchReleasesCached(key, fetch); err != nil {
		t.Fatalf("cache hit: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("R18-B8-02：缓存命中不得再拉取，calls=%d", got)
	}
}

func TestR18B802ReleasesFetchErrorSharedAndNotCached(t *testing.T) {
	key := "releases:test-singleflight-err"
	releasesCache.Lock()
	delete(releasesCache.entries, key)
	delete(releasesCache.flights, key)
	releasesCache.Unlock()

	boom := errors.New("upstream down")
	var calls atomic.Int64
	fetchErr := func() ([]githubRelease, error) {
		calls.Add(1)
		time.Sleep(100 * time.Millisecond)
		return nil, boom
	}
	const n = 4
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = fetchReleasesCached(key, fetchErr)
		}(i)
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("R18-B8-02：并发失败也应只拉取 1 次，got %d 次", got)
	}
	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("R18-B8-02：等待方应共享同一错误，i=%d err=%v", i, err)
		}
	}
	// 失败不入缓存：下一轮重新拉取。
	if _, err := fetchReleasesCached(key, fetchErr); !errors.Is(err, boom) {
		t.Fatalf("R18-B8-02：失败后下一轮应重试，got err=%v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("R18-B8-02：失败不得入缓存，calls=%d，期望 2", got)
	}
}
