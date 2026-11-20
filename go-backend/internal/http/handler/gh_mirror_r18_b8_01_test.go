package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// R18-B8-01：gh 镜像 tag 资产永久缓存（fresh==0），tag 被移动/重切
// 后节点升级的期望哈希信任根永久中毒，且无任何管理面清除入口。
// 以下测试先于修复落盘（先红后绿）。

// TestR18B801TagDownloadCacheHasBoundedTTL：tag 下 release 资产的
// 缓存必须带上限 TTL（24h），不得永久有效。修复前 fresh==0。
func TestR18B801TagDownloadCacheHasBoundedTTL(t *testing.T) {
	up, err := parseGHMirrorPath("https://github.com/"+githubRepo+"/releases/download/2.1.18/gost-amd64", "")
	if err != nil {
		t.Fatalf("parse tag download path: %v", err)
	}
	if up.fresh != 24*time.Hour {
		t.Fatalf("R18-B8-01：tag release 资产缓存 fresh=%s，期望 24h 上限（永久缓存会在 tag 移动后永久污染期望哈希信任根）", up.fresh)
	}
}

// TestR18B801MovedTagRefreshesExpectedHash：已缓存 25h 的旧 .sha256
// + 上游同名 tag 已重切（stub 返回新哈希）→ 期望哈希必须取到新值。
// 修复前永久缓存直接返回旧文件，上游一次也不打。
func TestR18B801MovedTagRefreshesExpectedHash(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	var hits atomic.Int64
	newHash := strings.Repeat("b", 64)
	oldHash := strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(newHash + "  gost-amd64\n"))
	}))
	t.Cleanup(srv.Close)

	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorDir = savedDir })
	savedBases := ghMirrorBases
	ghMirrorBases.github = srv.URL
	t.Cleanup(func() { ghMirrorBases = savedBases })

	up, err := ghMirrorReleaseAssetUpstream("2.1.18", "gost-amd64.sha256")
	if err != nil {
		t.Fatalf("ghMirrorReleaseAssetUpstream: %v", err)
	}
	sum := sha256.Sum256([]byte(up.url))
	path := filepath.Join(ghMirrorCacheDir(), hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(oldHash+"  gost-amd64\n"), 0o644); err != nil {
		t.Fatalf("seed old sha256 cache: %v", err)
	}
	seededAt := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, seededAt, seededAt); err != nil {
		t.Fatalf("seed mtime: %v", err)
	}

	got := ghMirrorReleaseSHA256("2.1.18", "amd64")
	if got != newHash {
		t.Fatalf("R18-B8-01：tag 重切后期望哈希仍为旧值 %q（期望新值 %q），上游命中 %d 次", got, newHash, hits.Load())
	}
	if hits.Load() == 0 {
		t.Fatalf("R18-B8-01：过期缓存必须回源重验证，上游命中 0 次")
	}
}

// r18B801SeedReleaseAssetCache 按 version/file 把内容写入镜像缓存
// （与 ghMirrorFetch 同一路径推导），返回缓存文件路径。
func r18B801SeedReleaseAssetCache(t *testing.T, version, file, content string) string {
	t.Helper()
	up, err := ghMirrorReleaseAssetUpstream(version, file)
	if err != nil {
		t.Fatalf("ghMirrorReleaseAssetUpstream(%q, %q): %v", version, file, err)
	}
	sum := sha256.Sum256([]byte(up.url))
	path := filepath.Join(ghMirrorCacheDir(), hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed cache %q: %v", file, err)
	}
	return path
}

// TestR18B801PurgeReleaseAssets：按版本清除镜像缓存——目标版本
// 的 6 个预热资产（install.sh×2 + 双架构二进制×2 + .sha256×2）
// 全部清除、其它版本不动；非法版本 fail-closed（报错、不删任何文件）。
func TestR18B801PurgeReleaseAssets(t *testing.T) {
	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorDir = savedDir })

	for _, file := range []string{"install.sh", "install.sh.sha256", "gost-amd64", "gost-arm64", "gost-amd64.sha256", "gost-arm64.sha256"} {
		r18B801SeedReleaseAssetCache(t, "2.1.18", file, "cached-"+file)
	}
	otherPath := r18B801SeedReleaseAssetCache(t, "2.1.17", "gost-amd64", "cached-other")

	removed, err := ghMirrorPurgeReleaseAssets("2.1.18")
	if err != nil {
		t.Fatalf("purge 2.1.18: %v", err)
	}
	if removed != 6 {
		t.Fatalf("R18-B8-01：purge 2.1.18 应清除 6 个资产文件，got %d", removed)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("R18-B8-01：purge 不得清除其它版本的缓存，2.1.17 文件已消失: %v", err)
	}
	removed, err = ghMirrorPurgeReleaseAssets("2.1.18")
	if err != nil || removed != 0 {
		t.Fatalf("R18-B8-01：重复 purge 应返回 removed=0 err=nil，got removed=%d err=%v", removed, err)
	}

	before := r18B801SeedReleaseAssetCache(t, "2.1.18", "gost-amd64", "cached-again")
	if _, err := ghMirrorPurgeReleaseAssets("bad/version"); err == nil {
		t.Fatalf("R18-B8-01：非法版本必须 fail-closed 报错，got nil")
	}
	if _, err := os.Stat(before); err != nil {
		t.Fatalf("R18-B8-01：非法版本的 purge 不得删除任何文件: %v", err)
	}
}

// TestR18B801PurgeEndpoint：管理面清除入口——仅管理员可用；
// 成功时回执点名版本与清除份数。
func TestR18B801PurgeEndpoint(t *testing.T) {
	h, _ := setupBC01Test(t)
	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorDir = savedDir })
	for _, file := range []string{"gost-amd64", "gost-amd64.sha256"} {
		r18B801SeedReleaseAssetCache(t, "2.1.18", file, "cached-"+file)
	}

	body, _ := json.Marshal(map[string]string{"version": "2.1.18"})
	rec := httptest.NewRecorder()
	h.ghMirrorPurgeCache(rec, adminRequest(t, http.MethodPost, "/api/v1/system/gh-cache/purge", body))
	code, _, data := decodeBC01Envelope(t, rec)
	if code != 0 {
		t.Fatalf("R18-B8-01：管理员 purge 应成功，got code=%d body=%s", code, rec.Body.String())
	}
	var out struct {
		Removed int    `json:"removed"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode purge data: %v body=%s", err, rec.Body.String())
	}
	if out.Removed != 2 || out.Version != "2.1.18" {
		t.Fatalf("R18-B8-01：purge 回执应为 removed=2 version=2.1.18，got %+v", out)
	}

	// 非管理员：403（信封 code 非 0）；GET：方法拒绝。
	rec = httptest.NewRecorder()
	h.ghMirrorPurgeCache(rec, nonAdminRequest(t, http.MethodPost, "/api/v1/system/gh-cache/purge", body))
	code, _, _ = decodeBC01Envelope(t, rec)
	if code == 0 {
		t.Fatalf("R18-B8-01：非管理员 purge 必须被拒绝，got code=0")
	}
	rec = httptest.NewRecorder()
	h.ghMirrorPurgeCache(rec, adminRequest(t, http.MethodGet, "/api/v1/system/gh-cache/purge", nil))
	code, _, _ = decodeBC01Envelope(t, rec)
	if code == 0 {
		t.Fatalf("R18-B8-01：GET purge 必须被拒绝，got code=0")
	}
}
