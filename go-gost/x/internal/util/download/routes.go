package download

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// --- Probe-ranked route selection (KU4, 1.7.7) ---
//
// Every agent GitHub download (self-upgrade, kernel installs,
// geodata) used to ride ONE proxy chosen out-of-band: the panel's
// GitHub-acceleration setting for the self-upgrade, a panel-pushed
// mirror prefix for kernels. When that single route sickened
// (ghfast's recurring stalls, per-repo blacklists, no API
// forwarding), the download burned its whole budget on it before
// any fallback could run. The panel setting is retired
// product-wide; routing is now decided where the download happens:
//
//   - The candidate set is built in (MirrorPrefixes + direct) and
//     identical across the product. It is compile-time data, never
//     user input, so it opens no new SSRF surface.
//   - Before downloading, the caller probes every candidate in
//     parallel against the REAL small target (the release's
//     .sha256 file), and a candidate is admitted only when the
//     probe delivers that file with parseable content — a mirror
//     answering its landing page with HTTP 200 does not count as
//     alive. Admitted routes are ranked by measured latency.
//   - Downloads then try the ranked routes in order; each attempt
//     keeps the caller's existing failure semantics (watchdog,
//     shared budget), and when every route fails the error names
//     each route and its cause.

// MirrorPrefixes is the built-in GitHub mirror candidate list, in
// static fallback order (used when no probe admits any route).
// Product-wide caliber (9 mirrors + direct): the panel and the
// installer carry the same list. A var only so tests can
// substitute local servers — production code never mutates it.
var MirrorPrefixes = []string{
	"https://ghfast.top",
	"https://gcode.hostcentral.cc",
	"https://gh-proxy.com",
	"https://ghproxy.net",
	"https://hub.gitmirror.com",
	"https://ghps.cc",
	"https://gh.llkk.cc",
	"https://ghp.ci",
	"https://ghproxy.cc",
}

// Probe budgets for RankRoutes: one candidate gets ProbeRouteTimeout,
// the whole parallel phase gets ProbeBudget. With the candidates
// probed in parallel the total stays ~4s regardless of candidate
// count. Vars so tests can shrink them.
var (
	ProbeRouteTimeout = 3 * time.Second
	ProbeBudget       = 4 * time.Second
)

// Route is one download route: a mirror prefix, or direct when
// Prefix is "". Latency/Probed are set when the route was admitted
// by a live probe with that measured round trip.
type Route struct {
	Prefix  string
	Latency time.Duration
	Probed  bool
}

// Name is the route's display name: the mirror host, or 直连.
func (r Route) Name() string {
	if r.Prefix == "" {
		return "直连"
	}
	if u, err := url.Parse(r.Prefix); err == nil && u.Host != "" {
		return u.Host
	}
	return r.Prefix
}

// URL builds the full download URL of directURL over this route.
func (r Route) URL(directURL string) string {
	if r.Prefix == "" {
		return directURL
	}
	return r.Prefix + "/" + directURL
}

// MirrorHosts returns the host of every built-in mirror candidate
// (for redirect allowlists that must admit the mirrors' own hosts).
func MirrorHosts() []string {
	hosts := make([]string, 0, len(MirrorPrefixes))
	for _, p := range MirrorPrefixes {
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			hosts = append(hosts, strings.ToLower(u.Host))
		}
	}
	return hosts
}

// AllRoutes returns every route in static fallback order: the
// built-in mirrors in listed order, then direct.
func AllRoutes() []Route {
	routes := make([]Route, 0, len(MirrorPrefixes)+1)
	for _, p := range MirrorPrefixes {
		routes = append(routes, Route{Prefix: p})
	}
	return append(routes, Route{})
}

// ProbeFunc performs ONE admission probe of a fully built URL. A
// nil error admits the route: the caller defines what "alive"
// means, and for GitHub downloads it must mean the real small
// target (the .sha256 file) arrived with parseable content —
// never a bare HTTP 200. Latency is measured by RankRoutes around
// the call.
type ProbeFunc func(ctx context.Context, url string) error

// RankRoutes probes every route in parallel against directURL
// (the small probe target, typically the release's checksum file)
// and returns the admitted routes fastest-first. Probing is capped
// per route (ProbeRouteTimeout) and as a phase (ProbeBudget). If
// no route is admitted — all dead, or the phase budget expired
// first — it returns AllRoutes() in static order, so the caller
// degrades to trying every candidate sequentially: never worse
// than the pre-probe behavior.
func RankRoutes(ctx context.Context, directURL string, probe ProbeFunc) []Route {
	all := AllRoutes()
	if probe == nil || directURL == "" {
		return all
	}
	pctx, cancel := context.WithTimeout(ctx, ProbeBudget)
	defer cancel()
	type outcome struct {
		route Route
		err   error
	}
	ch := make(chan outcome, len(all))
	for _, r := range all {
		go func(r Route) {
			rctx, rcancel := context.WithTimeout(pctx, ProbeRouteTimeout)
			defer rcancel()
			start := time.Now()
			err := probe(rctx, r.URL(directURL))
			if err == nil {
				r.Latency = time.Since(start)
				r.Probed = true
			}
			ch <- outcome{route: r, err: err}
		}(r)
	}
	var alive []Route
	pending := len(all)
collect:
	for pending > 0 {
		select {
		case o := <-ch:
			pending--
			if o.err == nil {
				alive = append(alive, o.route)
			}
		case <-pctx.Done():
			break collect
		}
	}
	if len(alive) == 0 {
		return all
	}
	sort.SliceStable(alive, func(i, j int) bool { return alive[i].Latency < alive[j].Latency })
	return alive
}

// TryRoutes runs attempt once per route, in the given order, until
// one succeeds, and returns the winning route. attempt receives
// the route and its fully built URL. When every route fails, the
// returned error names each route and its failure cause, so a
// dead fleet is diagnosable from the receipt alone.
func TryRoutes(routes []Route, directURL string, attempt func(route Route, url string) error) (Route, error) {
	if len(routes) == 0 {
		routes = AllRoutes()
	}
	var failures []string
	for _, r := range routes {
		if err := attempt(r, r.URL(directURL)); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", r.Name(), err))
			continue
		}
		return r, nil
	}
	return Route{}, fmt.Errorf("全部下载线路失败（%s）", strings.Join(failures, "；"))
}
