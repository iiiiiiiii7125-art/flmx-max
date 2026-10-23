package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// R6 批5 · R6X-012（R6-C-01）：gh_mirror 文件面 stale 回退的年龄上限
// 被回退自身的 Chtimes 续期架空——mtime 同时承担「新鲜期」「节流」
// 「内容年龄」三职，每次回退把 mtime 续到当前时刻，内容真实年龄
// 永不超 24h 上限。修复口径：年龄基准与节流解耦，年龄以内容真实
// 年龄（自成功下载起，mtime 不再被回退改写）为准，节流另计。

// setupR6B5GHFetch 备一个恒失败（500）的上游与独立缓存目录，返回
// up 与其缓存文件路径；up.url 每例唯一，避免缓存/节流状态串扰。
func setupR6B5GHFetch(t *testing.T, name string) (*ghMirrorUpstream, string, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorDir = savedDir })

	up := &ghMirrorUpstream{url: srv.URL + "/" + name, fresh: ghMirrorFreshAPI}
	sum := sha256.Sum256([]byte(up.url))
	path := filepath.Join(ghMirrorCacheDir(), hex.EncodeToString(sum[:]))
	return up, path, &hits
}

func seedR6B5Cache(t *testing.T, path string, downloadedAt time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale-content"), 0o644); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := os.Chtimes(path, downloadedAt, downloadedAt); err != nil {
		t.Fatalf("seed mtime: %v", err)
	}
}

func TestR6B5GHMirrorFileStaleWithinCapFallbackPreservesAge(t *testing.T) {
	// 上限内组（防过修对照 + 年龄基准断言）：内容 2h 前下载、已过
	// 10min 新鲜期、未过 24h 上限，上游恒失败 → 回退旧副本成功；
	// 且回退不得改写内容 mtime——改写即把内容年龄清零（先红点：
	// 修复前 Chtimes 续期后 mtime ≈ now，年龄断言失败）。
	up, path, _ := setupR6B5GHFetch(t, "within-cap")
	downloadedAt := time.Now().Add(-2 * time.Hour)
	seedR6B5Cache(t, path, downloadedAt)

	got, err := ghMirrorFetch(up)
	if err != nil {
		t.Fatalf("上限内 stale 回退应成功，got err=%v", err)
	}
	if got != path {
		t.Fatalf("应返回缓存文件 %q，got %q", path, got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if age := time.Since(st.ModTime()); age < time.Hour {
		t.Fatalf("R6X-012：stale 回退不得续期内容 mtime（年龄基准被架空），回退后 mtime 年龄=%s，期望仍 ≈2h", age.Round(time.Second))
	}

	// 周期请求第二发：上限内行为不变，仍回退成功。
	if _, err := ghMirrorFetch(up); err != nil {
		t.Fatalf("上限内周期请求回退应仍成功，got err=%v", err)
	}
}

func TestR6B5GHMirrorFileStalePeriodicCrossingCap(t *testing.T) {
	// 验收写死格：连续失败 + 周期请求下，内容真实年龄越过 24h
	// 必须停止回退并报错。每轮把 mtime 相对其当前值回拨 3h，模拟
	// 「距上次触碰又过 3h」：修复后 mtime 即下载时刻、年龄逐轮累积
	// 3/6/…/27h；修复前每轮回退都把 mtime 续到 now，年龄恒为 3h、
	// 永不越限（先红点：第 9 轮期望报错却回退成功）。
	up, path, _ := setupR6B5GHFetch(t, "periodic-crossing")
	seedR6B5Cache(t, path, time.Now())

	for step := 1; step <= 10; step++ {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("step %d stat: %v", step, err)
		}
		advanced := st.ModTime().Add(-3 * time.Hour)
		if err := os.Chtimes(path, advanced, advanced); err != nil {
			t.Fatalf("step %d chtimes: %v", step, err)
		}
		_, ferr := ghMirrorFetch(up)
		cumAge := time.Duration(step) * 3 * time.Hour
		switch {
		case cumAge < ghMirrorMaxStaleAge:
			if ferr != nil {
				t.Fatalf("step %d（内容年龄 %s < 上限）：回退应成功，got err=%v", step, cumAge, ferr)
			}
		case cumAge > ghMirrorMaxStaleAge:
			if ferr == nil {
				t.Fatalf("R6X-012：step %d 内容真实年龄 %s 已越上限 %s，连续失败下必须停止回退并报错，却回退成功", step, cumAge, ghMirrorMaxStaleAge)
			}
		default:
			// 恰在上限边界一轮不作断言（实测耗时使年龄略超/略欠）。
		}
	}
}

func TestR6B5GHMirrorFileStaleAbsoluteOldRejected(t *testing.T) {
	// 对照组：闲置 72h 的旧副本 + 上游失败 → 拒绝回退（修复前后
	// 均应成立，防把上限本身改没）。
	up, path, _ := setupR6B5GHFetch(t, "absolute-old")
	seedR6B5Cache(t, path, time.Now().Add(-72*time.Hour))
	if _, err := ghMirrorFetch(up); err == nil {
		t.Fatalf("72h 旧副本不得回退，got err=nil")
	}
}

func TestR6B5GHMirrorFreshCopyServedWithoutUpstream(t *testing.T) {
	// 防过修对照：新鲜期内副本直接返回、不打上游（请求计数 0）。
	up, path, hits := setupR6B5GHFetch(t, "fresh-copy")
	seedR6B5Cache(t, path, time.Now().Add(-time.Minute))
	got, err := ghMirrorFetch(up)
	if err != nil || got != path {
		t.Fatalf("新鲜副本应直接返回，got path=%q err=%v", got, err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("新鲜期内的请求不得打上游，got %d 次", n)
	}
}
