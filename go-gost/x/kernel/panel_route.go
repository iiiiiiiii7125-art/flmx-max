package kernel

import (
	"sync"

	"github.com/go-gost/x/internal/util/download"
)

// Panel-source route for kernel/geodata downloads (panel-source
// phase 3). The route's prefix points at the panel's GitHub mirror
// endpoint and is derived by the embedding process (the socket
// reporter) from the agent's OWN configured panel address — the
// same derivation the self-upgrade uses. It is registered once at
// startup via SetPanelRoute and consulted at every Plan
// construction in this package.
//
// Trust caliber: a kernel Download/GeoFile descriptor's TARGET
// URLs are panel-supplied (the pre-existing model — validated by
// ApplyRequest.validate and validateFetchEntryURL, and normalized
// by directForm, which strips any panel-attached mirror prefix);
// ROUTES, however, are decided locally by the probe plan over
// compile-time candidates. The panel route obeys the same split:
// the panel cannot inject it (it comes from local configuration,
// not from any command), and the mirror endpoint additionally
// enforces its own repository whitelist, so a descriptor target
// outside the mirrored set simply fails admission on this route
// and the plan falls back to the other routes.
var panelRouteState struct {
	sync.RWMutex
	route download.Route
	set   bool
}

// SetPanelRoute registers the panel-source download route for this
// process. Called by the embedding agent at startup; a later call
// replaces the previous registration.
func SetPanelRoute(r download.Route) {
	panelRouteState.Lock()
	defer panelRouteState.Unlock()
	panelRouteState.route = r
	panelRouteState.set = r.Prefix != ""
}

// panelExtraRoutes returns the registered panel route as Plan
// extra routes, or nil when none was registered (behavior is then
// exactly the pre-panel-source one).
func panelExtraRoutes() []download.Route {
	panelRouteState.RLock()
	defer panelRouteState.RUnlock()
	if !panelRouteState.set {
		return nil
	}
	return []download.Route{panelRouteState.route}
}
