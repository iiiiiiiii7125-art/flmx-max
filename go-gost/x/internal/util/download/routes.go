package download

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// --- Probe-selected download routes (KU4, 1.7.7) ---
//
// Every agent GitHub download (self-upgrade, kernel installs,
// geodata) used to ride ONE proxy chosen out-of-band: the panel's
// GitHub-acceleration setting for the self-upgrade, a panel-pushed
// mirror prefix for kernels. When that single route sickened
// (ghfast's recurring stalls, per-repo blacklists, no API
// forwarding), the download burned its whole budget on it before
// any fallback could run. The panel setting is retired
// product-wide; routing is now decided where the download happens,
// from a built-in candidate set (MirrorPrefixes + direct —
// compile-time data, never user input, so no new SSRF surface),
// with a direct-first short-circuit:
//
//  1. Probe DIRECT alone against the real small target (the
//     release's .sha256 file). Admitted (file delivered, content
//     parseable) within ProbeRouteTimeout AND fast enough
//     (DirectShortCircuitMaxLatency) → download goes direct
//     and no proxy is ever probed. Overseas nodes with working
//     direct GitHub access pay zero proxy overhead. A direct
//     route that is admitted but SLOW skips the short-circuit
//     and competes in the stage-2 ranking with its measured
//     latency (see DirectShortCircuitMaxLatency).
//  2. Direct not admitted (or admitted but slow) → probe the
//     mirrors in parallel and rank the admitted ones by measured
//     latency; downloads try them in rank order, switching on
//     any attempt failure.
//  3. If the admitted direct route fails mid-download, the plan
//     advances: mirrors are probed then (if not yet) and the
//     download continues down the ranking. If no probe admits
//     anything, the plan degrades to trying every candidate in
//     static order — never worse than the pre-probe behavior.
//
// Admission is never a bare HTTP 200: the caller-supplied probe
// must fetch the real small target and validate its content (a
// mirror answering its landing page is fake-alive and stays out).

// MirrorPrefixes is the built-in GitHub mirror candidate list, in
// static fallback order. Product-wide caliber (9 mirrors +
// direct): the panel and the installer carry the same list. A var
// only so tests can substitute local servers — production code
// never mutates it.
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

// Probe budgets: one candidate gets ProbeRouteTimeout (~3s), a
// parallel mirror-probe phase gets ProbeBudget (~4s). Vars so
// tests can shrink them.
var (
	ProbeRouteTimeout = 3 * time.Second
	ProbeBudget       = 4 * time.Second
)

// DirectShortCircuitMaxLatency bounds the stage-1 short-circuit:
// an admitted direct route whose probe — a fetch of the tiny
// checksum file — already took longer than this does NOT get the
// download to itself. The caliber behind the bound (production
// report 2026-11-01): a route needing >800ms for the small probe
// file moves the ~10MB body at a trickle relative to the shared
// 240s whole-operation budget; short-circuiting on it let the
// direct leg eat the budget while every mirror leg died on the
// expired context. A slow direct route is not discarded either:
// it keeps its measured latency and joins the stage-2 parallel
// ranking, so it still wins exactly when it is genuinely the
// fastest admitted route. Below the bound, behavior is unchanged
// (overseas nodes with healthy direct access keep paying zero
// proxy overhead). A var so tests can adjust it.
var DirectShortCircuitMaxLatency = 800 * time.Millisecond

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

// AllRoutes returns every route in static fallback order: direct
// first (the product caliber is direct-first), then the built-in
// mirrors in listed order.
func AllRoutes() []Route {
	routes := make([]Route, 0, len(MirrorPrefixes)+1)
	routes = append(routes, Route{})
	for _, p := range MirrorPrefixes {
		routes = append(routes, Route{Prefix: p})
	}
	return routes
}

func mirrorRoutes() []Route {
	routes := make([]Route, 0, len(MirrorPrefixes))
	for _, p := range MirrorPrefixes {
		routes = append(routes, Route{Prefix: p})
	}
	return routes
}

// ProbeFunc performs ONE admission probe of a fully built URL. A
// nil error admits the route: the caller defines what "alive"
// means, and for GitHub downloads it must mean the real small
// target (the .sha256 file) arrived with parseable content —
// never a bare HTTP 200. Latency is measured by the caller around
// the call.
type ProbeFunc func(ctx context.Context, url string) error

// probeOne probes a single route against target (the direct-form
// probe URL), bounded by ProbeRouteTimeout.
func probeOne(ctx context.Context, target string, route Route, probe ProbeFunc) (Route, bool) {
	rctx, cancel := context.WithTimeout(ctx, ProbeRouteTimeout)
	defer cancel()
	start := time.Now()
	if err := probe(rctx, route.URL(target)); err != nil {
		return Route{}, false
	}
	route.Latency = time.Since(start)
	route.Probed = true
	return route, true
}

// probeParallel probes candidates concurrently against target and
// returns the admitted ones fastest-first. The phase is capped by
// ProbeBudget; candidates still in flight at the cap are treated
// as not admitted.
func probeParallel(ctx context.Context, target string, candidates []Route, probe ProbeFunc) []Route {
	pctx, cancel := context.WithTimeout(ctx, ProbeBudget)
	defer cancel()
	type outcome struct {
		route Route
		err   error
	}
	ch := make(chan outcome, len(candidates))
	for _, r := range candidates {
		go func(r Route) {
			rctx, rcancel := context.WithTimeout(pctx, ProbeRouteTimeout)
			defer rcancel()
			start := time.Now()
			err := probe(rctx, r.URL(target))
			if err == nil {
				r.Latency = time.Since(start)
				r.Probed = true
			}
			ch <- outcome{route: r, err: err}
		}(r)
	}
	var alive []Route
	pending := len(candidates)
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
	sort.SliceStable(alive, func(i, j int) bool { return alive[i].Latency < alive[j].Latency })
	return alive
}

// Plan is the lazy direct-first route sequence for one download
// operation (which may span several legs — binary + checksum —
// that all share the plan's probe results but iterate it
// independently via TryPlan). Stages resolve on demand:
//
//	stage 1: direct, admitted fast by its solo probe;
//	stage 2: mirrors, admitted by a parallel probe, latency-ranked
//	         (entered immediately when the direct probe fails or
//	         the admitted direct route is slow — in which case
//	         direct joins the ranking — or via Advance when an
//	         admitted direct route fails mid-download);
//	stage 3: static tail — every route not yet tried, in
//	         AllRoutes order (the all-probes-dead degradation).
//
// A Plan is not safe for concurrent use; one operation uses it
// from one goroutine.
type Plan struct {
	ctx    context.Context
	target string
	probe  ProbeFunc

	stage   int // 0 undecided, 1 direct, 2 mirrors, 3 static tail
	current []Route
	probed  []Route // admitted routes, in discovery order
	static  bool    // fixed route list (tests / no-probe callers)
}

// NewPlan builds a plan probing target (the direct-form URL of the
// small probe file, typically the release's .sha256) with probe.
func NewPlan(ctx context.Context, probeTarget string, probe ProbeFunc) *Plan {
	return &Plan{ctx: ctx, target: probeTarget, probe: probe}
}

// NewStaticPlan builds a plan over a fixed route list with no
// probing and no stage advancement.
func NewStaticPlan(routes []Route) *Plan {
	return &Plan{stage: 3, current: routes, static: true}
}

func (p *Plan) prepare() {
	if p.stage != 0 {
		return
	}
	if p.probe == nil || p.target == "" {
		p.stage = 3
		p.current = AllRoutes()
		return
	}
	// Stage 1: direct alone, short-circuit — unless the admitted
	// direct route is slow (DirectShortCircuitMaxLatency): then
	// it joins the stage-2 ranking with its measured latency
	// instead of monopolizing the download.
	if r, ok := probeOne(p.ctx, p.target, Route{}, p.probe); ok {
		if r.Latency <= DirectShortCircuitMaxLatency {
			p.stage = 1
			p.current = []Route{r}
			p.probed = []Route{r}
			return
		}
		p.probed = []Route{r}
		p.enterMirrors()
		p.current = append(p.current, r)
		sort.SliceStable(p.current, func(i, j int) bool { return p.current[i].Latency < p.current[j].Latency })
		return
	}
	p.enterMirrors()
}

func (p *Plan) enterMirrors() {
	p.stage = 2
	p.current = probeParallel(p.ctx, p.target, mirrorRoutes(), p.probe)
	p.probed = append(p.probed, p.current...)
}

// Routes returns the current stage's try-order, resolving stages
// lazily on first use. It may be empty (stage 2 admitted nothing)
// — callers driving attempts should use TryPlan, which advances
// through the static tail in that case.
func (p *Plan) Routes() []Route {
	p.prepare()
	return p.current
}

// Probed returns the routes admitted by probing so far, in
// discovery order (for reporting which line an operation picked).
func (p *Plan) Probed() []Route {
	return p.probed
}

// Advance moves the plan to its next stage after the current
// stage's routes are exhausted, returning false when no stage
// remains. From stage 1 (admitted direct) it probes the mirrors;
// from stage 2 it opens the static tail.
func (p *Plan) Advance() bool {
	if p.static {
		return false
	}
	p.prepare()
	switch p.stage {
	case 1:
		p.enterMirrors()
		return true
	case 2:
		p.stage = 3
		p.current = AllRoutes()
		return true
	}
	return false
}

// TryPlan runs attempt over the plan's routes — in stage order,
// skipping routes already tried within THIS TryPlan call — until
// one succeeds, and returns the winning route. When the admitted
// direct route fails, the plan advances and mirrors are probed
// before the next attempt (the mid-download supplementation).
// When every reachable route fails, the returned error names each
// tried route and its failure cause, so a dead fleet is
// diagnosable from the receipt alone.
func TryPlan(plan *Plan, directURL string, attempt func(route Route, url string) error) (Route, error) {
	if plan == nil {
		plan = NewStaticPlan(nil)
	}
	var failures []string
	tried := map[string]bool{}
	for {
		for _, r := range plan.Routes() {
			if tried[r.Prefix] {
				continue
			}
			tried[r.Prefix] = true
			if err := attempt(r, r.URL(directURL)); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", r.Name(), err))
				continue
			}
			return r, nil
		}
		if !plan.Advance() {
			break
		}
	}
	if len(failures) == 0 {
		return Route{}, fmt.Errorf("全部下载线路失败（无可用线路）")
	}
	return Route{}, fmt.Errorf("全部下载线路失败（%s）", strings.Join(failures, "；"))
}
