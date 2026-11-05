package kernel

// FIX-R2-10: anchor corroboration verdicts are layered by the
// winning route's identity (the FIX-R2-9 law, applied to this
// package's corroborateAnchorDigest). A direct or panel-source
// winner with no corroboration available degrades to the single
// anchor with a named notice; a third-party mirror winner in the
// same situation fails closed — one mirror must not be able to
// serve a self-consistent fake (file, digest) pair while every
// corroborating route is dead. An empty corroboration set is
// never a silent pass.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gost/x/internal/util/download"
)

// --- unit matrix over corroborateAnchorDigest ---

func TestCorroborateAnchorDigestWinnerLayersFixR210(t *testing.T) {
	target := "https://example.invalid/release.sha256"
	dead := func(context.Context, *download.Plan) (string, error) {
		return "", errors.New("其他线路不可达")
	}
	same := func(context.Context, *download.Plan) (string, error) { return "abc123", nil }
	diff := func(context.Context, *download.Plan) (string, error) { return "def456", nil }

	collect := func() (func(string), *[]string) {
		notes := &[]string{}
		return func(msg string) { *notes = append(*notes, msg) }, notes
	}

	t.Run("proxy winner without corroboration refuses", func(t *testing.T) {
		setKernelMirrorCandidates(t, "https://other.invalid")
		note, notes := collect()
		winner := download.Route{Prefix: "https://proxy.invalid"}
		err := corroborateAnchorDigest(context.Background(), winner, target, dead, "abc123", note)
		if err == nil {
			t.Fatal("代理获胜且复核不可用时必须拒绝安装")
		}
		if !strings.Contains(err.Error(), "获胜线路为代理") || !strings.Contains(err.Error(), "FIX-R2-10") {
			t.Fatalf("错误应点名代理获胜与条目号, got: %v", err)
		}
		if len(*notes) != 0 {
			t.Fatalf("拒绝时不得发出降级点名, got notes %v", *notes)
		}
	})

	t.Run("proxy winner with agreeing corroboration passes silently", func(t *testing.T) {
		setKernelMirrorCandidates(t, "https://other.invalid")
		note, notes := collect()
		winner := download.Route{Prefix: "https://proxy.invalid"}
		if err := corroborateAnchorDigest(context.Background(), winner, target, same, "abc123", note); err != nil {
			t.Fatalf("复核一致时代理获胜应放行: %v", err)
		}
		if len(*notes) != 0 {
			t.Fatalf("复核一致时应静默, got notes %v", *notes)
		}
	})

	t.Run("proxy winner with differing corroboration refuses", func(t *testing.T) {
		setKernelMirrorCandidates(t, "https://other.invalid")
		note, _ := collect()
		winner := download.Route{Prefix: "https://proxy.invalid"}
		err := corroborateAnchorDigest(context.Background(), winner, target, diff, "abc123", note)
		if err == nil || !strings.Contains(err.Error(), "不一致") {
			t.Fatalf("复核不一致必须点名拒绝, got: %v", err)
		}
	})

	t.Run("direct winner without corroboration degrades with notice", func(t *testing.T) {
		setKernelMirrorCandidates(t, "https://other.invalid")
		note, notes := collect()
		err := corroborateAnchorDigest(context.Background(), download.Route{}, target, dead, "abc123", note)
		if err != nil {
			t.Fatalf("直连获胜且复核不可用应降级放行: %v", err)
		}
		if len(*notes) != 1 || !strings.Contains((*notes)[0], "跨线路锚复核已降级") {
			t.Fatalf("降级必须点名恰一次, got %v", *notes)
		}
	})

	t.Run("panel winner without corroboration degrades with notice", func(t *testing.T) {
		setPanelRouteForTest(t, "https://panel.example.com/api/v1/open_api/gh")
		note, notes := collect()
		winner := download.Route{Prefix: "https://panel.example.com/api/v1/open_api/gh"}
		err := corroborateAnchorDigest(context.Background(), winner, target, dead, "abc123", note)
		if err != nil {
			t.Fatalf("面板源获胜且复核不可用应降级放行: %v", err)
		}
		if len(*notes) != 1 || !strings.Contains((*notes)[0], "跨线路锚复核已降级") {
			t.Fatalf("降级必须点名恰一次, got %v", *notes)
		}
	})

	t.Run("empty corroboration set is named for a direct winner", func(t *testing.T) {
		setKernelMirrorCandidates(t) // no mirrors: the plan minus the direct winner is empty
		note, notes := collect()
		called := false
		guard := func(context.Context, *download.Plan) (string, error) {
			called = true
			return "", errors.New("不应被调用")
		}
		err := corroborateAnchorDigest(context.Background(), download.Route{}, target, guard, "abc123", note)
		if err != nil {
			t.Fatalf("直连获胜且复核集为空应降级放行: %v", err)
		}
		if called {
			t.Fatal("复核集为空时不得调用 fetchOne")
		}
		if len(*notes) != 1 || !strings.Contains((*notes)[0], "跨线路锚复核已降级") ||
			!strings.Contains((*notes)[0], "无其他线路可供复核") {
			t.Fatalf("空集降级必须点名恰一次, got %v", *notes)
		}
	})
}

// --- consumer: install() checksum-file anchor (install.go) ---

func TestInstallProxyWinnerNoCorroborationRefusedFixR210(t *testing.T) {
	goodZip, dgst := makeKernelZip(t, "proxy-served-xray")

	// The direct leg answers the checksum path with unparseable
	// content, so it can never win the anchor leg nor corroborate;
	// the mirror serves a self-consistent (checksum, body) pair.
	direct := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".dgst") {
			_, _ = rw.Write([]byte("this is not a checksum file"))
			return
		}
		http.NotFound(rw, r)
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

	var notes []string
	d := &Download{
		URL:         direct.URL + "/Xray-linux-64.zip",
		ChecksumURL: direct.URL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	binDir := filepath.Join(t.TempDir(), "xray")
	err := install(d, binDir, func(msg string) { notes = append(notes, msg) })
	if err == nil {
		t.Fatal("代理获胜且无线路可复核时必须拒绝安装（单锚自洽对不得定版）")
	}
	if !strings.Contains(err.Error(), "获胜线路为代理") {
		t.Fatalf("错误应点名代理获胜, got: %v", err)
	}
	if _, statErr := os.Stat(binDir); !os.IsNotExist(statErr) {
		t.Fatalf("拒绝后不得留下安装目录, stat err = %v", statErr)
	}
	for _, n := range notes {
		if strings.Contains(n, "已降级") {
			t.Fatalf("拒绝路径不得发出降级点名, got %q", n)
		}
	}
}

func TestInstallDirectWinnerNoCorroborationDegradesFixR210(t *testing.T) {
	goodZip, dgst := makeKernelZip(t, "direct-served-xray")

	direct := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip"):
			_, _ = rw.Write(goodZip)
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			_, _ = rw.Write([]byte(dgst))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer direct.Close()
	deadMirror := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusBadGateway)
	}))
	defer deadMirror.Close()
	setKernelMirrorCandidates(t, deadMirror.URL)

	var notes []string
	d := &Download{
		URL:         direct.URL + "/Xray-linux-64.zip",
		ChecksumURL: direct.URL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	binDir := filepath.Join(t.TempDir(), "xray")
	if err := install(d, binDir, func(msg string) { notes = append(notes, msg) }); err != nil {
		t.Fatalf("直连获胜且复核不可用应降级完成安装: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "xray"))
	if err != nil || string(got) != "direct-served-xray" {
		t.Fatalf("安装产物不符: %v %q", err, got)
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "跨线路锚复核已降级") {
			found = true
		}
	}
	if !found {
		t.Fatalf("降级必须经 note 点名, got %v", notes)
	}
}

func TestInstallPanelWinnerNoCorroborationDegradesFixR210(t *testing.T) {
	goodZip, dgst := makeKernelZip(t, "panel-served-xray")

	// Direct target is a closed server; the only live route is the
	// registered panel source.
	deadDirect := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {}))
	deadDirectURL := deadDirect.URL
	deadDirect.Close()
	deadMirror := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusBadGateway)
	}))
	defer deadMirror.Close()
	setKernelMirrorCandidates(t, deadMirror.URL)

	panel := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".zip"):
			_, _ = rw.Write(goodZip)
		case strings.HasSuffix(r.URL.Path, ".dgst"):
			_, _ = rw.Write([]byte(dgst))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer panel.Close()
	setPanelRouteForTest(t, panel.URL)

	var notes []string
	d := &Download{
		URL:         deadDirectURL + "/Xray-linux-64.zip",
		ChecksumURL: deadDirectURL + "/Xray-linux-64.zip.dgst",
		Archive:     "zip",
		BinName:     "xray",
	}
	binDir := filepath.Join(t.TempDir(), "xray")
	if err := install(d, binDir, func(msg string) { notes = append(notes, msg) }); err != nil {
		t.Fatalf("面板源获胜且复核不可用应降级完成安装: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "xray"))
	if err != nil || string(got) != "panel-served-xray" {
		t.Fatalf("安装产物不符: %v %q", err, got)
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "跨线路锚复核已降级") {
			found = true
		}
	}
	if !found {
		t.Fatalf("降级必须经 note 点名, got %v", notes)
	}
}

// --- consumer: resolveGitHub GitHub-API digest anchor ---

func TestResolveGitHubProxyWinnerNoCorroborationRefusedFixR210(t *testing.T) {
	backend := f061APIServer(t)

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer direct.Close()
	proxy := f061ForwardingProxy(t, backend)
	setKernelMirrorCandidates(t, proxy.URL)

	old := githubAPIBase
	githubAPIBase = direct.URL
	defer func() { githubAPIBase = old }()

	g := &GitHubRelease{Repo: "SagerNet/sing-box", Asset: "sing-box-{VERSION}-{ARCH}.tar.gz"}
	_, _, err := resolveGitHub(context.Background(), g, "amd64", nil)
	if err == nil {
		t.Fatal("代理获胜且直连不可复核时必须拒绝采用 API 摘要")
	}
	if !strings.Contains(err.Error(), "获胜线路为代理") {
		t.Fatalf("错误应点名代理获胜, got: %v", err)
	}
}

// --- consumer: geodata checksum anchor (geodata.go) ---

func TestStageGeoFileProxyWinnerNoCorroborationRefusedFixR210(t *testing.T) {
	data := []byte("geo data via proxy only")
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:]) + "  geoip.dat\n"

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256sum") {
			_, _ = w.Write([]byte("this is not a checksum file"))
			return
		}
		http.NotFound(w, r)
	}))
	defer direct.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".sha256sum"):
			_, _ = w.Write([]byte(checksum))
		case strings.HasSuffix(r.URL.Path, ".dat"):
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mirror.Close()
	setKernelMirrorCandidates(t, mirror.URL)

	binDir := t.TempDir()
	f := GeoFile{
		Name:        "geoip.dat",
		URL:         direct.URL + "/geoip.dat",
		ChecksumURL: direct.URL + "/geoip.dat.sha256sum",
	}
	changed, err := stageGeoFile(context.Background(), binDir, f, nil, func(string) {})
	if err == nil {
		t.Fatal("代理获胜且无线路可复核时必须拒绝暂存 geodata")
	}
	if !strings.Contains(err.Error(), "获胜线路为代理") {
		t.Fatalf("错误应点名代理获胜, got: %v", err)
	}
	if changed {
		t.Fatal("拒绝时不得报告已暂存")
	}
	if _, statErr := os.Stat(filepath.Join(binDir, "geoip.dat.new")); !os.IsNotExist(statErr) {
		t.Fatalf("拒绝后不得留下暂存文件, stat err = %v", statErr)
	}
}

func TestStageGeoFileDirectWinnerNoCorroborationDegradesFixR210(t *testing.T) {
	data := []byte("geo data via direct")
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:]) + "  geoip.dat\n"

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".sha256sum"):
			_, _ = w.Write([]byte(checksum))
		case strings.HasSuffix(r.URL.Path, ".dat"):
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer direct.Close()
	deadMirror := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusBadGateway)
	}))
	defer deadMirror.Close()
	setKernelMirrorCandidates(t, deadMirror.URL)

	var notes []string
	binDir := t.TempDir()
	f := GeoFile{
		Name:        "geoip.dat",
		URL:         direct.URL + "/geoip.dat",
		ChecksumURL: direct.URL + "/geoip.dat.sha256sum",
	}
	changed, err := stageGeoFile(context.Background(), binDir, f, nil, func(msg string) { notes = append(notes, msg) })
	if err != nil {
		t.Fatalf("直连获胜且复核不可用应降级完成暂存: %v", err)
	}
	if !changed {
		t.Fatal("应报告文件已暂存")
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "跨线路锚复核已降级") {
			found = true
		}
	}
	if !found {
		t.Fatalf("降级必须经 note 点名, got %v", notes)
	}
}
