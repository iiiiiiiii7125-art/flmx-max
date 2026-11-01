package kernel

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-gost/x/internal/util/download"
)

// 面板源三期：内核面的面板线路注册表——未注册时行为与现状逐字
// 一致（panelExtraRoutes 为 nil），注册后每个 Plan 构造点拿到
// 恰好这一条线路；空前缀注册等同未注册。
func TestPanelExtraRoutesRegistration(t *testing.T) {
	savedRoute, savedSet := panelRouteState.route, panelRouteState.set
	t.Cleanup(func() {
		panelRouteState.Lock()
		panelRouteState.route, panelRouteState.set = savedRoute, savedSet
		panelRouteState.Unlock()
	})

	SetPanelRoute(download.Route{})
	if got := panelExtraRoutes(); got != nil {
		t.Fatalf("空前缀注册应等同未注册, got %v", got)
	}

	want := download.Route{Prefix: "https://panel.example.com/api/v1/open_api/gh"}
	SetPanelRoute(want)
	got := panelExtraRoutes()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("注册后应返回恰好该线路, got %v", got)
	}

	SetPanelRoute(download.Route{Prefix: "https://other.example.com/api/v1/open_api/gh"})
	got = panelExtraRoutes()
	if len(got) != 1 || got[0].Prefix != "https://other.example.com/api/v1/open_api/gh" {
		t.Fatalf("后注册应替换先注册, got %v", got)
	}
}

func setPanelRouteForTest(t *testing.T, prefix string) {
	t.Helper()
	savedRoute, savedSet := panelRouteState.route, panelRouteState.set
	t.Cleanup(func() {
		panelRouteState.Lock()
		panelRouteState.route, panelRouteState.set = savedRoute, savedSet
		panelRouteState.Unlock()
	})
	SetPanelRoute(download.Route{Prefix: prefix})
}

// 面板源三期端到端：直连已死、无内置镜像（TestMain 已清空候选），
// 只有面板线路能到达资产时 install 必须经面板线路完成；面板线路
// 的 URL 形态为 前缀 + "/" + 直连地址（与镜像线路同构）。
func TestInstallViaPanelRouteWhenDirectDead(t *testing.T) {
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

	dead := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	var panelHits atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		panelHits.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip"):
			_, _ = rw.Write(zipBuf.Bytes())
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			_, _ = rw.Write([]byte(dgst))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer panel.Close()
	setPanelRouteForTest(t, panel.URL)

	d := &Download{
		URL:         deadURL + "/Xray-linux-64.zip",
		ChecksumURL: deadURL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	binDir := filepath.Join(t.TempDir(), "xray")
	if err := install(d, binDir); err != nil {
		t.Fatalf("直连已死时应经面板线路完成安装, 实际失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "xray"))
	if err != nil || string(got) != "fake-xray-binary" {
		t.Fatalf("安装后的二进制不符: %v %q", err, got)
	}
	if panelHits.Load() == 0 {
		t.Fatal("面板线路应被真实使用")
	}
}

// geodata 同口径：校验与本体两腿都经面板线路，直连已死时
// stageGeoFiles 仍能把新数据文件 staged 下来。
func TestStageGeoFilesViaPanelRoute(t *testing.T) {
	body := []byte("fake-geosite-content")
	sum := sha256.Sum256(body)
	sumText := fmt.Sprintf("%s  geosite.dat\n", hex.EncodeToString(sum[:]))

	dead := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	var panelHits atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		panelHits.Add(1)
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = rw.Write([]byte(sumText))
			return
		}
		_, _ = rw.Write(body)
	}))
	defer panel.Close()
	setPanelRouteForTest(t, panel.URL)

	binDir := t.TempDir()
	files := []GeoFile{{
		Name:        "geosite.dat",
		URL:         deadURL + "/geosite.dat",
		ChecksumURL: deadURL + "/geosite.dat.sha256",
	}}
	staged, err := stageGeoFiles(context.Background(), binDir, files)
	if err != nil {
		t.Fatalf("直连已死时 geodata 应经面板线路完成, 实际失败: %v", err)
	}
	if len(staged) != 1 || staged[0] != "geosite.dat" {
		t.Fatalf("staged 应为 [geosite.dat], got %v", staged)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "geosite.dat.new"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("staged 内容不符: %v", err)
	}
	if panelHits.Load() == 0 {
		t.Fatal("面板线路应被真实使用")
	}
}
