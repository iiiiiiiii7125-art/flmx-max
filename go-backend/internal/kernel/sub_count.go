package kernel

// SubscriptionNodeCount returns how many real node entries Subscription
// would deliver for one format, policy/selector groups excluded. It runs
// the same pipeline and the same per-format renderability checks as
// Subscription itself, so the number a list shows for a format is the
// number a client of that format actually receives (A5-02/BC-11: the
// old count was taken before format filtering and matched no client).
func SubscriptionNodeCount(format string, endpoints []Endpoint, opts SubOptions) int {
	endpoints = uniqueNames(ApplySubOptions(endpoints, opts))
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
		// The clash format renders through clashProxyMihomo; Stash
		// renders through ClashProxy. The two differ on the imported
		// nodes proxyClashFor gates per format, so counts are taken
		// through the same renderer each format uses.
		return count(func(e Endpoint) bool { _, ok := clashProxyMihomo(e); return ok })
	case FormatStash:
		return count(func(e Endpoint) bool { _, ok := ClashProxy(e); return ok })
	case FormatSingbox:
		return count(func(e Endpoint) bool {
			_, ok := singboxOutboundsWith(e, opts.SingboxSnell, opts.SingboxNaive)
			return ok
		})
	case FormatSurge:
		return count(func(e Endpoint) bool { _, ok := SurgeLine(e); return ok })
	case FormatLoon:
		return count(func(e Endpoint) bool {
			p, ok := endpointProxy(e)
			if !ok {
				return false
			}
			_, ok = loonLine(p)
			return ok
		})
	case FormatQX:
		return count(func(e Endpoint) bool {
			p, ok := endpointProxy(e)
			if !ok {
				return false
			}
			_, ok = qxLine(p)
			return ok
		})
	case FormatShadowrocket:
		// Entries of the profile: every rendered Clash proxy (see
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
		return len(rendered)
	}
	return 0
}
