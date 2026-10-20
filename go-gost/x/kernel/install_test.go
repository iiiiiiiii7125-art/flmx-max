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
	"strings"
	"testing"
)

// 镜像黑名单回归：代理前缀对资产与校验文件都回 403（模拟 ghfast 对
// XTLS/Xray-core 的 "Forbidden by black list."），install 必须自动剥离
// 前缀、回退直连完成安装。
func TestInstallFallsBackToDirectWhenMirrorBlacklists(t *testing.T) {
	// 构造一个含 xray 二进制的 zip 及其 sha256。
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create("xray")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("fake-xray-binary")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zipBuf.Bytes())
	dgst := fmt.Sprintf("%s  Xray-linux-64.zip\n", hex.EncodeToString(sum[:]))

	direct := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip"):
			_, _ = rw.Write(zipBuf.Bytes())
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			_, _ = rw.Write([]byte(dgst))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer direct.Close()

	mirror := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte("Forbidden by black list."))
	}))
	defer mirror.Close()

	d := &Download{
		URL:         mirror.URL + "/" + direct.URL + "/Xray-linux-64.zip",
		ChecksumURL: mirror.URL + "/" + direct.URL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	binDir := filepath.Join(t.TempDir(), "xray")
	if err := install(d, binDir); err != nil {
		t.Fatalf("镜像 403 时应回退直连完成安装，实际失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "xray"))
	if err != nil {
		t.Fatalf("安装后的二进制不存在: %v", err)
	}
	if string(got) != "fake-xray-binary" {
		t.Fatalf("二进制内容不符: %q", got)
	}
}

// 镜像与直连都失败时，错误信息必须包含回退尝试，便于定位。
func TestInstallReportsFallbackFailure(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusForbidden)
	}))
	defer blocked.Close()

	d := &Download{
		URL:         blocked.URL + "/" + blocked.URL + "/Xray-linux-64.zip",
		ChecksumURL: blocked.URL + "/" + blocked.URL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	err := install(d, filepath.Join(t.TempDir(), "xray"))
	if err == nil {
		t.Fatal("两条路径都 403 时安装必须失败")
	}
	if !strings.Contains(err.Error(), "回退直连亦失败") {
		t.Fatalf("错误信息应提及回退尝试，实际: %v", err)
	}
}

func TestStripMirrorPrefix(t *testing.T) {
	cases := map[string]string{
		"https://ghfast.top/https://github.com/XTLS/Xray-core/releases/download/v1/Xray-linux-64.zip": "https://github.com/XTLS/Xray-core/releases/download/v1/Xray-linux-64.zip",
		"https://mirror.example/http://example.com/a.zip":                                             "http://example.com/a.zip",
		"https://github.com/XTLS/Xray-core/releases/download/v1/Xray-linux-64.zip":                    "",
		"": "",
	}
	for in, want := range cases {
		if got := stripMirrorPrefix(in); got != want {
			t.Errorf("stripMirrorPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
