package kernel

// Chain vocabulary for subscription rendering. The subscription chain
// options themselves (marking landing/relay endpoints from stored
// options) were removed in 2.1.2: nothing assigns Endpoint.ChainRole
// anymore, so every consumer below always sees "no chain". The group
// names, role constants and the two helpers stay because the template
// renderer (sub_template.go) still threads them — its chain branches
// are unreachable in practice, and its behaviour for templates that
// mention the reserved group names is unchanged.

const (
	// ChainLandingGroup holds the exit nodes; it heads the node selection.
	ChainLandingGroup = "落地节点"
	// ChainRelayGroup holds the nodes landing nodes are dialed through.
	ChainRelayGroup = "中转节点"

	ChainRoleLanding = "landing"
	ChainRoleRelay   = "relay"
)

// listed reports whether a node goes into the node lists and groups: with
// ChainHideRelays a chain's relay nodes are only in the relay group.
func listed(e Endpoint, chain bool, opts SubOptions) bool {
	return !chain || !opts.ChainHideRelays || e.ChainRole != ChainRoleRelay
}

// chainGroups returns the landing and relay names among the endpoints a
// format could render; the chain applies only when both are non-empty.
func chainGroups(endpoints []Endpoint) (landing, relay []string, ok bool) {
	for _, e := range endpoints {
		switch e.ChainRole {
		case ChainRoleLanding:
			landing = append(landing, e.Name)
		case ChainRoleRelay:
			relay = append(relay, e.Name)
		}
	}
	return landing, relay, len(landing) > 0 && len(relay) > 0
}

func toInterfaces(names []string) []interface{} {
	out := make([]interface{}, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}
