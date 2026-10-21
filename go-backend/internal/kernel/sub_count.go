package kernel

// SubscriptionNodeCount returns how many real node entries Subscription
// would deliver for one format, policy/selector groups excluded. It runs
// the same pipeline and the same per-format renderability checks as
// Subscription itself, so the number a list shows for a format is the
// number a client of that format actually receives (A5-02/BC-11: the
// old count was taken before format filtering and matched no client).
func SubscriptionNodeCount(format string, endpoints []Endpoint, opts SubOptions) int {
	endpoints = uniqueNames(ApplySubOptions(endpoints, opts), opts.Rules)
	count := func(ok func(Endpoint) bool) int {
		n := 0
		for _, e := range endpoints {
			if ok(e) {
				n++
			}
		}
		return n
	}
	switch format {
	case FormatBase64:
		return count(func(e Endpoint) bool { _, ok := ShareURI(e); return ok })
	case FormatClash:
		return count(func(e Endpoint) bool { _, ok := ClashProxy(e); return ok })
	case FormatSingbox:
		return count(func(e Endpoint) bool { _, ok := singboxOutbounds(e, opts.SingboxSnell); return ok })
	case FormatSurge:
		return count(func(e Endpoint) bool { _, ok := SurgeLine(e); return ok })
	case FormatLoon:
		_, _, chain := chainGroups(endpoints)
		return count(func(e Endpoint) bool {
			if !listed(e, chain, opts) {
				return false
			}
			p, ok := endpointProxy(e)
			if !ok {
				return false
			}
			_, ok = loonLine(p)
			return ok
		})
	case FormatQX:
		_, _, chain := chainGroups(endpoints)
		return count(func(e Endpoint) bool {
			if !listed(e, chain, opts) {
				return false
			}
			p, ok := endpointProxy(e)
			if !ok {
				return false
			}
			_, ok = qxLine(p)
			return ok
		})
	case FormatShadowrocket:
		// Entries of the profile: every rendered Clash proxy, with a
		// chained landing expanded to one copy per relay (see
		// shadowrocketProfile).
		var rendered []Endpoint
		for _, e := range endpoints {
			if p, ok := endpointProxy(e); ok && shadowrocketSkips(p) {
				continue
			}
			if _, ok := ClashProxy(e); ok {
				rendered = append(rendered, e)
			}
		}
		_, relays, chain := chainGroups(rendered)
		n := 0
		for _, e := range rendered {
			if chain && e.ChainRole == ChainRoleLanding {
				n += len(relays)
			} else {
				n++
			}
		}
		return n
	}
	return 0
}
