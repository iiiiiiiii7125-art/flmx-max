package kernel

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// C NEW-2: 重启/重部署不得先删旧内核——替换件落盘验证前旧二进制
// 必须在位；安装失败时旧内核继续在位、loud 报错；同版本重装离
// 线时用本地已验安装包缓存。

func TestSwapBinDirKeepsOldOnRenameFailure(t *testing.T) {
	base := t.TempDir()
	binDir := filepath.Join(base, "1.0")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sing-box"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// staging 不存在：改名落位必败，旧目录必须原样恢复。
	if err := swapBinDir(binDir, filepath.Join(base, "missing-staging")); err == nil {
		t.Fatal("落位失败时 swapBinDir 必须返回错误")
	}
	got, err := os.ReadFile(filepath.Join(binDir, "sing-box"))
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("落位失败后旧二进制必须原样在位，实际 (%q, %v)", got, err)
	}
	if _, err := os.Stat(binDir + ".old"); !os.IsNotExist(err) {
		t.Fatalf(".old 备份落位失败后不得残留，实际 err=%v", err)
	}
}

func TestSwapBinDirReplacesAndDropsOld(t *testing.T) {
	base := t.TempDir()
	binDir := filepath.Join(base, "1.0")
	staging := filepath.Join(base, "1.0.staging")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sing-box"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "sing-box"), []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapBinDir(binDir, staging); err != nil {
		t.Fatalf("swapBinDir = %v, want nil", err)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "sing-box"))
	if err != nil || string(got) != "new-binary" {
		t.Fatalf("交换后活目录应为新二进制，实际 (%q, %v)", got, err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatal("staging 目录交换后不得残留")
	}
	if _, err := os.Stat(binDir + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old 备份交换成功后不得残留")
	}
}

func TestRestoreInterruptedSwap(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "1.0")
	backup := dir + ".old"
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "sing-box"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !restoreInterruptedSwap(dir, "sing-box") {
		t.Fatal("活目录缺失、备份在位时必须恢复")
	}
	got, err := os.ReadFile(filepath.Join(dir, "sing-box"))
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("恢复后二进制必须在位，实际 (%q, %v)", got, err)
	}
	if restoreInterruptedSwap(dir, "sing-box") {
		t.Fatal("已恢复后再次调用不得重复报告恢复")
	}
	// 活目录在位（即使为空）时绝不覆盖。
	dir2 := filepath.Join(base, "2.0")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	backup2 := dir2 + ".old"
	if err := os.MkdirAll(backup2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup2, "sing-box"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if restoreInterruptedSwap(dir2, "sing-box") {
		t.Fatal("活目录在位时不得用备份覆盖")
	}
}

func TestArchiveCacheRoundTrip(t *testing.T) {
	base := t.TempDir()
	binDir := filepath.Join(base, "singbox", "bin", "1.14.4")
	archive := []byte("fake-archive-bytes")
	sum := sha256.Sum256(archive)
	want := hex.EncodeToString(sum[:])
	d := &Download{BinName: "sing-box", Archive: "tar.gz"}
	src := filepath.Join(base, "src.tar.gz")
	if err := os.WriteFile(src, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	storeArchiveCache(binDir, d, "https://example.invalid/sing-box-1.14.4.tar.gz", src, want)
	got, ok := lookupArchiveCache(binDir, d, "https://example.invalid/sing-box-1.14.4.tar.gz", want)
	if !ok {
		t.Fatal("刚存入的缓存必须命中")
	}
	if b, err := os.ReadFile(got); err != nil || !bytes.Equal(b, archive) {
		t.Fatalf("缓存内容不符: %v", err)
	}
	// 篡改缓存文件：命中必须失败且脏条目被清除，永不信任。
	if err := os.WriteFile(got, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupArchiveCache(binDir, d, "https://example.invalid/sing-box-1.14.4.tar.gz", want); ok {
		t.Fatal("篡改后的缓存不得命中")
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatal("篡改的缓存条目必须被清除")
	}
	// 缓存目录在整棵类型树之外，pruneStaleBinDirs 的范围与整树
	// 删除都碰不到它。
	if got := archiveCacheDir(binDir); got != filepath.Join(base, "cache", "singbox") {
		t.Fatalf("archiveCacheDir = %s", got)
	}
}

func TestPruneArchiveCacheKeepsNewestThree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("sing-box-%d.tar.gz", i)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p+".sha256", []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// i 越大越新。
		if err := os.Chtimes(p, now.Add(time.Duration(i)*time.Hour), now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	pruneArchiveCache(dir)
	for i := 0; i < 2; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("sing-box-%d.tar.gz", i))); !os.IsNotExist(err) {
			t.Fatalf("最旧的缓存 %d 应被回收", i)
		}
	}
	for i := 2; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("sing-box-%d.tar.gz", i))); err != nil {
			t.Fatalf("最新的缓存 %d 应保留: %v", i, err)
		}
	}
}

// 事故回归：断网期重启重装——第一次安装写缓存；服务器关闭后删掉
// 整个版本目录再装，必须用缓存离线成功，二进制内容一致。
func TestInstallReusesArchiveCacheOffline(t *testing.T) {
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create("sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("fake-sing-box-binary")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zipBuf.Bytes())
	dgst := fmt.Sprintf("%s  sing-box-1.14.4-linux-amd64.zip\n", hex.EncodeToString(sum[:]))

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip"):
			_, _ = rw.Write(zipBuf.Bytes())
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			_, _ = rw.Write([]byte(dgst))
		default:
			http.NotFound(rw, r)
		}
	}))

	d := &Download{
		URL:         srv.URL + "/sing-box-1.14.4-linux-amd64.zip",
		ChecksumURL: srv.URL + "/sing-box-1.14.4-linux-amd64.zip.dgst",
		Archive:     "zip",
		BinName:     "sing-box",
	}
	binDir := filepath.Join(t.TempDir(), "singbox", "bin", "1.14.4")
	if err := install(d, binDir, nil); err != nil {
		t.Fatalf("首次安装失败: %v", err)
	}
	// 缓存已落盘（整棵类型树之外）。
	entries, err := os.ReadDir(archiveCacheDir(binDir))
	if err != nil || len(entries) == 0 {
		t.Fatalf("首次安装后缓存目录应有条目: entries=%v err=%v", entries, err)
	}
	// 断网：关掉服务器，再删掉整个版本目录模拟 03:59 的零内核态。
	srv.Close()
	if err := os.RemoveAll(binDir); err != nil {
		t.Fatal(err)
	}
	if err := install(d, binDir, nil); err != nil {
		t.Fatalf("离线重装必须用缓存成功，实际失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "sing-box"))
	if err != nil || string(got) != "fake-sing-box-binary" {
		t.Fatalf("离线重装后二进制不符: (%q, %v)", got, err)
	}
}

// 安装失败（断网、无包）时旧二进制必须继续在位。
func TestInstallOfflineFailureKeepsOldBinary(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "singbox", "bin", "1.14.4")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sing-box"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 127.0.0.1:1 拒绝连接，立即失败，不碰外网。
	d := &Download{
		URL:         "http://127.0.0.1:1/sing-box-9.9.9-linux-amd64.zip",
		ChecksumURL: "http://127.0.0.1:1/sing-box-9.9.9-linux-amd64.zip.dgst",
		Archive:     "zip",
		BinName:     "sing-box",
	}
	if err := install(d, binDir, nil); err == nil {
		t.Fatal("断网安装必须失败")
	}
	got, err := os.ReadFile(filepath.Join(binDir, "sing-box"))
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("安装失败后旧二进制必须在位，实际 (%q, %v)", got, err)
	}
	if _, err := os.Stat(binDir + ".old"); !os.IsNotExist(err) {
		t.Fatal("失败的安装不得留下 .old 备份")
	}
}

func TestEnsureBinaryRestoresInterruptedSwap(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "bin", "1.0")
	backup := dir + ".old"
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "sing-box"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir())
	inst := &instance{logs: &logRing{}}
	d := &Download{BinName: "sing-box", Archive: "raw"}
	path, err := m.ensureBinary(inst, "sing-box 1.0", d, dir)
	if err != nil {
		t.Fatalf("ensureBinary 恢复中断交换失败: %v", err)
	}
	if path != filepath.Join(dir, "sing-box") {
		t.Fatalf("path = %s", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("恢复后二进制不符: (%q, %v)", got, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("恢复后 .old 备份不得残留")
	}
}

// 恢复的二进制哈希不符时仍重装；重装失败（无网络）时恢复的旧
// 二进制继续在位，不留零内核态。
func TestEnsureBinaryRestoredMismatchFailsLoudKeepingOld(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "bin", "1.0")
	backup := dir + ".old"
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "sing-box"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("expected-content"))
	// URL 为空：重装尝试立即以“下载地址不完整”失败。
	d := &Download{BinName: "sing-box", Archive: "raw", SHA256: map[string]string{runtime.GOARCH: hex.EncodeToString(sum[:])}}
	m := NewManager(t.TempDir())
	inst := &instance{logs: &logRing{}}
	path, err := m.ensureBinary(inst, "sing-box 1.0", d, dir)
	if err == nil || path != "" {
		t.Fatalf("ensureBinary = (%q, %v), want reinstall failure", path, err)
	}
	if !strings.Contains(err.Error(), "安装") {
		t.Fatalf("err = %v, want install failure", err)
	}
	got, rerr := os.ReadFile(filepath.Join(dir, "sing-box"))
	if rerr != nil || string(got) != "tampered" {
		t.Fatalf("重装失败后恢复的旧二进制必须在位，实际 (%q, %v)", got, rerr)
	}
}

// R18-D3-05: a cache entry is only a hit when its recorded digest
// matches the digest the current install wants. A self-consistent
// but stale entry (same URL, different pinned digest) must miss.
func TestArchiveCacheRejectsStaleDigest(t *testing.T) {
	base := t.TempDir()
	binDir := filepath.Join(base, "singbox", "bin", "1.14.4")
	archive := []byte("fake-archive-bytes")
	sum := sha256.Sum256(archive)
	want := hex.EncodeToString(sum[:])
	d := &Download{BinName: "sing-box", Archive: "tar.gz"}
	src := filepath.Join(base, "src.tar.gz")
	if err := os.WriteFile(src, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	url := "https://example.invalid/sing-box-1.14.4.tar.gz"
	storeArchiveCache(binDir, d, url, src, want)
	otherSum := sha256.Sum256([]byte("other-digest"))
	other := hex.EncodeToString(otherSum[:])
	if _, ok := lookupArchiveCache(binDir, d, url, other); ok {
		t.Fatal("a cache entry recorded for another digest must miss when the install wants a different digest")
	}
	// The stale entry must survive the miss (it is self-consistent;
	// only a corrupt entry is purged) and still hit for its own digest.
	if _, ok := lookupArchiveCache(binDir, d, url, want); !ok {
		t.Fatal("the same entry must still hit when the install wants its recorded digest")
	}
	// No want at all (digest resolved later, e.g. GitHub/sumURL
	// flows): the old self-consistency behavior is kept.
	if _, ok := lookupArchiveCache(binDir, d, url, ""); !ok {
		t.Fatal("an empty want must keep the self-consistency hit")
	}
}
