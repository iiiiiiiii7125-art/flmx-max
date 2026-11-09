package kernel

// SubscriptionNodeCount returns how many real node entries Subscription
// would deliver for one format, policy/selector groups excluded. It runs
// the same pipeline and the same per-format renderability checks as
// Subscription itself, so the number a list shows for a format is the
// number a client of that format actually receives (A5-02/BC-11: the
// old count was taken before format filtering and matched no client).
func SubscriptionNodeCount(format string, endpoints []Endpoint, opts SubOptions) int {
	ok := subscriptionFormatRenderable(format, opts)
	if ok == nil {
		return 0
	}
	endpoints = uniqueNames(ApplySubOptions(endpoints, opts))
	n := 0
	for _, e := range endpoints {
		if ok(e) {
			n++
		}
	}
	return n
}

// SubscriptionFormats lists every delivery format Subscription can
// render, in display order.
func SubscriptionFormats() []string {
	return []string{FormatBase64, FormatClash, FormatSingbox, FormatSurge,
		FormatStash, FormatShadowrocket, FormatLoon, FormatQX}
}

// SubscriptionFormatSkipped returns the names of the endpoints that
// Subscription would leave out of the given format's output
// (protocol/format combinations the format cannot express), in
// pipeline order. It runs the identical pipeline and renderability
// predicates as SubscriptionNodeCount, so the two can never disagree
// (W3/P-003: the per-format skip used to be invisible everywhere —
// list counts shrank silently and the preview gave no hint; callers
// now surface the skipped names at preview time).
func SubscriptionFormatSkipped(format string, endpoints []Endpoint, opts SubOptions) []string {
	ok := subscriptionFormatRenderable(format, opts)
	if ok == nil {
		return nil
	}
	endpoints = uniqueNames(ApplySubOptions(endpoints, opts))
	var names []string
	for _, e := range endpoints {
		if !ok(e) {
			names = append(names, e.Name)
		}
	}
	return names
}

// subscriptionFormatRenderable is the per-format renderability
// predicate Subscription's renderers apply, shared by the count and
// the skipped-names faces. Nil for an unknown format.
func subscriptionFormatRenderable(format string, opts SubOptions) func(Endpoint) bool {
	switch format {
	case FormatBase64:
		return func(e Endpoint) bool { _, ok := ShareURI(e); return ok }
	case FormatClash:
		// The clash format renders through clashProxyMihomo; Stash
		// renders through ClashProxy. The two differ on the imported
		// nodes proxyClashFor gates per format, so counts are taken
		// through the same renderer each format uses.
		return func(e Endpoint) bool { _, ok := clashProxyMihomo(e); return ok }
	case FormatStash:
		return func(e Endpoint) bool { _, ok := ClashProxy(e); return ok }
	case FormatSingbox:
		return func(e Endpoint) bool {
			_, ok := singboxOutboundsWith(e, opts.SingboxSnell, opts.SingboxNaive)
			return ok
		}
	case FormatSurge:
		return func(e Endpoint) bool { _, ok := SurgeLine(e); return ok }
	case FormatLoon:
		return func(e Endpoint) bool {
			p, ok := endpointProxy(e)
			if !ok {
				return false
			}
			_, ok = loonLine(p)
			return ok
		}
	case FormatQX:
		return func(e Endpoint) bool {
			p, ok := endpointProxy(e)
			if !ok {
				return false
			}
			_, ok = qxLine(p)
			return ok
		}
	case FormatShadowrocket:
		// Entries of the profile: every rendered Clash proxy (see
		// shadowrocketProfile); shadowrocketSkips entries are left
		// out before rendering.
		return func(e Endpoint) bool {
			if p, ok := endpointProxy(e); ok && shadowrocketSkips(p) {
				return false
			}
			_, ok := ClashProxy(e)
			return ok
		}
	}
	return nil
}
