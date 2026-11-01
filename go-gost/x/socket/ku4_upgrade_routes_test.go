package socket

// KU4 (1.7.7): the self-upgrade's route policy moved from the
// deleted two-leg proxy->direct fallback helper (R7-J-02's
// upgradeWithProxyFallback / getWithUpgradeProxyFallback, which
// had no production caller left) to the probe-selected route plan
// (download.Plan + download.TryPlan; direct-first short-circuit).
// The J-02 behavioral assertions survive here against the leg
// functions: a failing first route falls through to the next, a
// winning first route leaves later routes untouched, and total
// failure names every route and cause. New KU4 assertions: probe
// admission requires the real .sha256 with a parseable hash (a
// landing-page 200 is NOT alive), direct admission short-circuits
// all proxy probing, and proxy ranking follows measured latency.

import (
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
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// 首条线路 403 -> 切下一线路（直连）成功，直连恰命中一次。
func TestKU4BinaryFirstRouteForbiddenFallsToNext(t *testing.T) {
	payload := []byte("new-agent-binary-payload")
	var directHits atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Forbidden by black list.")
	}))
	defer proxy.Close()

	plan := download.NewStaticPlan([]download.Route{{Prefix: proxy.URL}, {}})
	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, hash, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, direct.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("首线路 403 必须切直连成功: %v", err)
	}
	sum := sha256.Sum256(payload)
	if n != int64(len(payload)) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("下载结果错误: n=%d hash=%s", n, hash)
	}
	if directHits.Load() != 1 {
		t.Fatalf("直连应恰命中一次, got %d", directHits.Load())
	}
}

// 首条线路成功 -> 后续线路不被触碰。
func TestKU4BinaryFirstRouteSuccessSkipsRest(t *testing.T) {
	payload := []byte("via-first-route")
	var directHits atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		_, _ = w.Write([]byte("direct-body"))
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer proxy.Close()

	plan := download.NewStaticPlan([]download.Route{{Prefix: proxy.URL}, {}})
	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, direct.URL+"/gost-amd64", tmp, nil); err != nil {
		t.Fatalf("首线路成功必须整腿成功: %v", err)
	}
	if b, err := os.ReadFile(tmp); err != nil || string(b) != string(payload) {
		t.Fatalf("落盘内容应来自首线路: %v %q", err, b)
	}
	if directHits.Load() != 0 {
		t.Fatalf("首线路成功后直连不得被触碰, hits=%d", directHits.Load())
	}
}

// 全部线路失败 -> 聚合错误逐条点名线路与原因（403 + 500）。
func TestKU4BinaryAllRoutesFailNamesEveryRoute(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxy.Close()

	plan := download.NewStaticPlan([]download.Route{{Prefix: proxy.URL}, {}})
	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, direct.URL+"/gost-amd64", tmp, nil)
	if err == nil {
		t.Fatal("全部线路失败必须报错")
	}
	for _, want := range []string{"403", "500", "直连"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("聚合错误应包含 %q, got: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), proxy.URL) && !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("聚合错误应点名首条线路, got: %v", err)
	}
}

// 直连短路 + 探针准入：直连能拉到真 .sha256 时计划只有直连，
// 代理根本不被探测（零请求）；直连已死时，对 .sha256 回 200
// 落地页（HTML 垃圾）的假活代理必须被内容校验挡在准入之外。
func TestKU4ProbeRejectsLandingPageFakeAlive(t *testing.T) {
	payload := []byte("new-agent-binary-payload")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			_, _ = w.Write([]byte(hash + "  gost-amd64\n"))
			return
		}
		_, _ = w.Write(payload)
	}))
	defer direct.Close()
	var fakeHits atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeHits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><head><title>mirror</title></head><body>welcome</body></html>"))
	}))
	defer fake.Close()

	old := download.MirrorPrefixes
	download.MirrorPrefixes = []string{fake.URL}
	defer func() { download.MirrorPrefixes = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 短路：直连准入 -> 计划只有直连，假活代理零探测。
	plan := download.NewPlan(ctx, direct.URL+"/gost-amd64.sha256", probeUpgradeRoute(newUpgradeDownloadClient("")))
	routes := plan.Routes()
	if len(routes) != 1 || routes[0].Prefix != "" {
		t.Fatalf("直连准入时计划应只有直连, got %v", routes)
	}
	if fakeHits.Load() != 0 {
		t.Fatalf("直连短路后代理不得被探测, fake hits=%d", fakeHits.Load())
	}

	// 直连已死 -> 代理被探测，但落地页内容不合法 -> 零准入。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	plan2 := download.NewPlan(ctx, deadURL+"/gost-amd64.sha256", probeUpgradeRoute(newUpgradeDownloadClient("")))
	_ = plan2.Routes()
	if fakeHits.Load() == 0 {
		t.Fatal("直连已死时应转入代理探测")
	}
	if got := plan2.Probed(); len(got) != 0 {
		t.Fatalf("假活代理必须被内容校验挡下, 准入 = %v", got)
	}
}

// 直连已死时的代理排名按实测延迟：快代理排慢代理之前，两条都
// 准入（校验文件都可解析）。
func TestKU4ProbeRanksProxiesByLatencyWhenDirectDead(t *testing.T) {
	payload := []byte("new-agent-binary-payload")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	serve := func(delay time.Duration) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if delay > 0 {
				time.Sleep(delay)
			}
			if strings.Contains(r.URL.Path, ".sha256") {
				_, _ = w.Write([]byte(hash + "  gost-amd64\n"))
				return
			}
			_, _ = w.Write(payload)
		}))
	}
	dead := serve(0)
	deadURL := dead.URL
	dead.Close()
	fastProxy := serve(0)
	defer fastProxy.Close()
	slowProxy := serve(250 * time.Millisecond)
	defer slowProxy.Close()

	old := download.MirrorPrefixes
	download.MirrorPrefixes = []string{slowProxy.URL, fastProxy.URL}
	defer func() { download.MirrorPrefixes = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan := download.NewPlan(ctx, deadURL+"/gost-amd64.sha256", probeUpgradeRoute(newUpgradeDownloadClient("")))
	routes := plan.Routes()
	if len(routes) != 2 {
		t.Fatalf("两个代理都应准入, 排名 = %v", routes)
	}
	if routes[0].Prefix != fastProxy.URL || routes[1].Prefix != slowProxy.URL {
		t.Fatalf("排名应为 快代理 > 慢代理, got %v", routes)
	}
	if !routes[0].Probed || routes[0].Latency >= routes[1].Latency {
		t.Fatalf("延迟记录错误: %+v vs %+v", routes[0], routes[1])
	}
}
