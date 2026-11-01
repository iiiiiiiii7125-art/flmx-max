package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Test harness for the KU4 probe-ranked route selection: httptest
// servers stand in for mirror proxies (a "proxy" answers any path,
// the prefixed form is irrelevant to it) and for the direct origin.
// The production candidate list is substituted per test.

func setMirrorPrefixes(t *testing.T, prefixes ...string) {
	t.Helper()
	old := MirrorPrefixes
	MirrorPrefixes = prefixes
	t.Cleanup(func() { MirrorPrefixes = old })
}

func setProbeBudgets(t *testing.T, perRoute, total time.Duration) {
	t.Helper()
	oldR, oldB := ProbeRouteTimeout, ProbeBudget
	ProbeRouteTimeout, ProbeBudget = perRoute, total
	t.Cleanup(func() { ProbeRouteTimeout, ProbeBudget = oldR, oldB })
}

// delayedServer answers 200 "ok" after delay on every path.
func delayedServer(delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		_, _ = w.Write([]byte("ok"))
	}))
}

func okProbe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func routeNames(routes []Route) []string {
	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = r.Name()
	}
	return names
}

func TestRankRoutesOrdersByMeasuredLatency(t *testing.T) {
	slow := delayedServer(250 * time.Millisecond)
	defer slow.Close()
	fast := delayedServer(20 * time.Millisecond)
	defer fast.Close()
	direct := delayedServer(120 * time.Millisecond)
	defer direct.Close()
	setMirrorPrefixes(t, slow.URL, fast.URL)

	routes := RankRoutes(context.Background(), direct.URL+"/file.sha256", okProbe)
	want := []string{fast.URL, direct.URL, slow.URL}
	// Names are hosts; compare by prefix instead for exactness.
	if len(routes) != 3 {
		t.Fatalf("ranked routes = %v, want 3 admitted", routeNames(routes))
	}
	for i, w := range want {
		wantPrefix := w
		if w == direct.URL {
			wantPrefix = ""
		}
		if routes[i].Prefix != wantPrefix {
			t.Fatalf("rank[%d] = %q, want %q (full ranking: %v)", i, routes[i].Prefix, wantPrefix, routeNames(routes))
		}
		if !routes[i].Probed || routes[i].Latency <= 0 {
			t.Fatalf("rank[%d] must carry its measured latency, got %+v", i, routes[i])
		}
	}
}

func TestRankRoutesDirectWinsWhenFastest(t *testing.T) {
	p1 := delayedServer(200 * time.Millisecond)
	defer p1.Close()
	p2 := delayedServer(300 * time.Millisecond)
	defer p2.Close()
	direct := delayedServer(0)
	defer direct.Close()
	setMirrorPrefixes(t, p1.URL, p2.URL)

	routes := RankRoutes(context.Background(), direct.URL+"/file.sha256", okProbe)
	if len(routes) == 0 || routes[0].Prefix != "" {
		t.Fatalf("fastest direct must rank first, got %v", routeNames(routes))
	}
}

// A candidate that dies between probe and download is skipped at
// download time to the next ranked route; routes past the winner
// are never attempted.
func TestTryRoutesFallsToNextRankedAndStopsAtWinner(t *testing.T) {
	var hits1, hits2, hits3 atomic.Int32
	routes := []Route{{Prefix: "https://a.example"}, {Prefix: "https://b.example"}, {Prefix: "https://c.example"}}
	attempt := func(route Route, url string) error {
		switch route.Prefix {
		case "https://a.example":
			hits1.Add(1)
			return errors.New("boom-a")
		case "https://b.example":
			hits2.Add(1)
			return nil
		default:
			hits3.Add(1)
			return nil
		}
	}
	used, err := TryRoutes(routes, "https://github.com/x/y", attempt)
	if err != nil {
		t.Fatalf("second ranked route must win: %v", err)
	}
	if used.Prefix != "https://b.example" {
		t.Fatalf("winner = %q, want b", used.Prefix)
	}
	if hits1.Load() != 1 || hits2.Load() != 1 || hits3.Load() != 0 {
		t.Fatalf("attempts a/b/c = %d/%d/%d, want 1/1/0", hits1.Load(), hits2.Load(), hits3.Load())
	}
}

func TestRankRoutesExcludesDeadCandidates(t *testing.T) {
	dead := delayedServer(0)
	deadURL := dead.URL
	dead.Close() // listener gone: connection refused
	alive := delayedServer(0)
	defer alive.Close()
	direct := delayedServer(0)
	defer direct.Close()
	setMirrorPrefixes(t, deadURL, alive.URL)

	routes := RankRoutes(context.Background(), direct.URL+"/file.sha256", okProbe)
	for _, r := range routes {
		if r.Prefix == deadURL {
			t.Fatalf("dead candidate must not enter the ranking: %v", routeNames(routes))
		}
	}
	if len(routes) != 2 {
		t.Fatalf("ranking = %v, want alive proxy + direct", routeNames(routes))
	}
}

// Every candidate dead at probe time: RankRoutes degrades to the
// static full order (try-everything semantics, never worse than
// the pre-probe behavior), and TryRoutes over it fails with an
// error naming every route.
func TestRankRoutesAllDeadFallsBackToStaticOrder(t *testing.T) {
	dead := delayedServer(0)
	deadURL := dead.URL
	dead.Close()
	setMirrorPrefixes(t, deadURL, "https://mirror.invalid")

	routes := RankRoutes(context.Background(), "http://127.0.0.1:1/file.sha256", okProbe)
	all := AllRoutes()
	if len(routes) != len(all) {
		t.Fatalf("all-dead ranking = %v, want static order %v", routeNames(routes), routeNames(all))
	}
	for i := range all {
		if routes[i].Prefix != all[i].Prefix {
			t.Fatalf("all-dead rank[%d] = %q, want static %q", i, routes[i].Prefix, all[i].Prefix)
		}
	}
	_, err := TryRoutes(routes, "https://github.com/x/y", func(route Route, url string) error {
		return fmt.Errorf("dial failed for %s", route.Name())
	})
	if err == nil {
		t.Fatal("all routes failing must error")
	}
	for _, r := range all {
		if !strings.Contains(err.Error(), r.Name()) {
			t.Fatalf("aggregated error must name route %q, got: %v", r.Name(), err)
		}
	}
}

// The probe phase is time-bounded even when a candidate accepts
// and then hangs: per-route cap ~3s / phase cap ~4s in production,
// shrunk here.
func TestRankRoutesProbePhaseIsBounded(t *testing.T) {
	setProbeBudgets(t, 200*time.Millisecond, 400*time.Millisecond)
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer hung.Close()
	alive := delayedServer(0)
	defer alive.Close()
	direct := delayedServer(0)
	defer direct.Close()
	setMirrorPrefixes(t, hung.URL, alive.URL)

	start := time.Now()
	routes := RankRoutes(context.Background(), direct.URL+"/file.sha256", okProbe)
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("probe phase took %v, must stay bounded (~400ms budget)", elapsed)
	}
	for _, r := range routes {
		if r.Prefix == hung.URL {
			t.Fatalf("hung candidate must not be admitted: %v", routeNames(routes))
		}
	}
	if len(routes) == 0 {
		t.Fatal("alive routes must still be ranked")
	}
}

func TestRouteURLAndName(t *testing.T) {
	direct := Route{}
	if got := direct.URL("https://github.com/a/b"); got != "https://github.com/a/b" {
		t.Fatalf("direct URL = %q", got)
	}
	if direct.Name() != "直连" {
		t.Fatalf("direct name = %q", direct.Name())
	}
	mirror := Route{Prefix: "https://ghfast.top"}
	if got := mirror.URL("https://github.com/a/b"); got != "https://ghfast.top/https://github.com/a/b" {
		t.Fatalf("mirror URL = %q", got)
	}
	if mirror.Name() != "ghfast.top" {
		t.Fatalf("mirror name = %q", mirror.Name())
	}
	hosts := MirrorHosts()
	if len(hosts) != len(MirrorPrefixes) {
		t.Fatalf("MirrorHosts = %v, want one per candidate", hosts)
	}
}
