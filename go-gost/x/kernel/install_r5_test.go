package kernel

// Round-5 FB cluster regression tests (L2-03 / L2-08 / L2-15 /
// L2-16 / L2-17): in-leg body verification, cross-route anchor
// corroboration, dial-time full re-judgment, asset-probe content
// admission, geodata checksum size parity.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// --- L2-15: dial-time re-judgment must match the hop judgment ---
//
// The cross-host hop check resolves the target once and applies
// the full non-public judgment; the pinned dial then re-resolves.
// If the second answer rebinds to a private address, the dial
// must refuse it with the full judgment too (it used to re-judge
// hard-deny only and dial the private address).
func TestFetchDialRebindingCrossHostRejected(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("internal-content"))
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	oldLookup := fetchLookupIP
	fetchLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		if host != "rebind.test" {
			return oldLookup(ctx, host)
		}
		if calls.Add(1) == 1 {
			// Hop check: a public answer — the hop is admitted.
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		// Dial: the name has been rebound to a private address.
		return []net.IP{net.ParseIP("192.168.0.9")}, nil
	}
	defer func() { fetchLookupIP = oldLookup }()

	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://rebind.test:"+targetURL.Port()+"/payload", http.StatusFound)
	}))
	defer entry.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	err = fetch(ctx, entry.URL+"/start", 1<<20, &buf)
	if err == nil {
		t.Fatalf("rebound private dial must be rejected, got content %q", buf.String())
	}
	if !strings.Contains(err.Error(), "非公网") {
		t.Fatalf("want full non-public judgment at dial time, got: %v", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("rebound target contacted %d times, want 0", targetHits.Load())
	}
}

// --- L2-03: complete-but-wrong body from one route fails the leg,
// not the install ---

func makeKernelZip(t *testing.T, content string) ([]byte, string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create("xray")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zipBuf.Bytes())
	return zipBuf.Bytes(), fmt.Sprintf("%s  Xray-linux-64.zip\n", hex.EncodeToString(sum[:]))
}

func TestInstallWrongBodyRouteFallsOverToNextRoute(t *testing.T) {
	goodZip, dgst := makeKernelZip(t, "good-xray-binary")
	wrongZip, _ := makeKernelZip(t, "complete-but-wrong-binary")

	var directBodyHits atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip"):
			directBodyHits.Add(1)
			_, _ = rw.Write(wrongZip) // complete, well-formed, wrong content
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			_, _ = rw.Write([]byte(dgst))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer direct.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip"):
			_, _ = rw.Write(goodZip)
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			_, _ = rw.Write([]byte(dgst))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer mirror.Close()
	setKernelMirrorCandidates(t, mirror.URL)

	d := &Download{
		URL:         direct.URL + "/Xray-linux-64.zip",
		ChecksumURL: direct.URL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	binDir := filepath.Join(t.TempDir(), "xray")
	if err := install(d, binDir); err != nil {
		t.Fatalf("一条线路给出错误本体时必须换线完成安装, got: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "xray"))
	if err != nil {
		t.Fatalf("安装后的二进制不存在: %v", err)
	}
	if string(got) != "good-xray-binary" {
		t.Fatalf("二进制内容 = %q, want 正确线路的 good-xray-binary", got)
	}
	if directBodyHits.Load() == 0 {
		t.Fatal("错误本体线路应至少被尝试过一次（先试先废）")
	}
}

// --- L2-08: two routes disagreeing on the checksum anchor refuse
// the install before any body is downloaded ---

func TestInstallChecksumAnchorDisagreementRefused(t *testing.T) {
	_, dgstA := makeKernelZip(t, "binary-a")
	_, dgstB := makeKernelZip(t, "binary-b")

	serve := func(dgst string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".dgst") {
				_, _ = rw.Write([]byte(dgst))
				return
			}
			http.NotFound(rw, r)
		}))
	}
	direct := serve(dgstA)
	defer direct.Close()
	mirror := serve(dgstB)
	defer mirror.Close()
	setKernelMirrorCandidates(t, mirror.URL)

	d := &Download{
		URL:         direct.URL + "/Xray-linux-64.zip",
		ChecksumURL: direct.URL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	err := install(d, filepath.Join(t.TempDir(), "xray"))
	if err == nil {
		t.Fatal("两条线路给出的校验值不一致时必须拒绝安装")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("错误应点名锚不一致, got: %v", err)
	}
}

// --- L2-16: assetProbe must not admit an HTML landing page ---

func TestAssetProbeRejectsLandingPage(t *testing.T) {
	htmlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>mirror landing</body></html>"))
	}))
	defer htmlSrv.Close()
	if err := assetProbe(context.Background(), htmlSrv.URL+"/asset.zip"); err == nil {
		t.Fatal("HTML 落地页 200 不得准入资产探针")
	}

	binSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("binary-bytes"))
	}))
	defer binSrv.Close()
	if err := assetProbe(context.Background(), binSrv.URL+"/asset.zip"); err != nil {
		t.Fatalf("真实二进制 200 应准入, got: %v", err)
	}
}

// --- L2-17: geodata checksum leg reads up to the probe's 64KiB,
// not 4096 ---

func TestStageGeoFileAcceptsChecksumLargerThan4096(t *testing.T) {
	data := []byte("geoip-content-for-size-parity")
	sum := sha256.Sum256(data)
	// A checksum file padded with comment lines: >4096 bytes, <
	// 64KiB, with the digest line at the end.
	checksumText := strings.Repeat("# mirror checksum bundle\n", 300) +
		fmt.Sprintf("%s  geoip.dat\n", hex.EncodeToString(sum[:]))
	if len(checksumText) <= 4096 || len(checksumText) >= 64<<10 {
		t.Fatalf("fixture size %d outside the (4096, 64KiB) window", len(checksumText))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			_, _ = w.Write([]byte(checksumText))
		case strings.HasSuffix(r.URL.Path, ".dat"):
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	binDir := t.TempDir()
	f := GeoFile{
		Name:        "geoip.dat",
		URL:         srv.URL + "/geoip.dat",
		ChecksumURL: srv.URL + "/geoip.dat.sha256",
	}
	plan := download.NewStaticPlan([]download.Route{{}})
	changed, err := stageGeoFile(context.Background(), binDir, f, plan)
	if err != nil {
		t.Fatalf("4–64KiB 校验文件必须可拉取并完成暂存, got: %v", err)
	}
	if !changed {
		t.Fatal("新文件应被暂存 (changed=true)")
	}
	staged, err := os.ReadFile(filepath.Join(binDir, "geoip.dat.new"))
	if err != nil {
		t.Fatalf("暂存文件不存在: %v", err)
	}
	if !bytes.Equal(staged, data) {
		t.Fatalf("暂存内容不符: %q", staged)
	}
}
