package kernel

// SubOptions is a subscription's render-time options. The stored
// pipeline (handler subPipeline) maps into it; only the relay scope
// (Lines / RelayIDs) still filters endpoints — the former processing pipeline
// (region/protocol/name filters, rename, flag, dedupe, prefix, sort),
// the subscription chain options, the rule engine and the JavaScript
// hook were removed in 2.1.2, and subscriptions now render in the
// panel's original format. The remaining fields are render-time flags
// the handler fills per fetch (client capabilities, notices, template
// bodies); they are not stored options.
type SubOptions struct {
	// Lines is the subscription's relay scope: "direct" keeps direct
	// endpoints only, "relay" keeps relayed endpoints only, "" keeps
	// both. It is a stored option (the 中转范围 three-state scope).
	Lines string
	// RelayIDs is the subscription's exact endpoint whitelist
	// (0 = direct, >0 = relay id): when non-empty it is the operative
	// scope filter and takes precedence over Lines — an endpoint is
	// kept iff its RelayID is in the list. Lines applies only when no
	// whitelist is present. It is a stored option (the pre-2.1.2
	// per-endpoint picker; 2.1.2 folded it into Lines at parse time
	// and widened partial whitelists to the whole tier — that fold is
	// removed, the whitelist again means exactly the listed set).
	RelayIDs []int64
	// ChainHideRelays is a render-time flag consulted by the template
	// renderer's chain vocabulary (listed): with a chain, relay nodes
	// stay out of the node lists. No stored option maps into it since
	// the subscription chain options were removed in 2.1.2 — nothing
	// assigns endpoint chain roles anymore, so it stays false; the
	// field remains because the template renderer still threads it.
	ChainHideRelays bool
	// SingboxSnell puts imported Snell nodes in sing-box profiles: the
	// client is sing-box 1.14+ (see SingboxClientHasSnell).
	SingboxSnell bool
	// SingboxNaive puts imported Naive nodes in sing-box profiles: the
	// client is sing-box 1.13+ (see SingboxClientHasNaive). R9X-005 连带格：低于
	// 1.13 的客户端略过 Naive 节点（整表 FATAL 的替代处置，与导入
	// Snell 节点的 UA 略过同型），其余节点照常下发。
	SingboxNaive bool
	// Notices are placeholder nodes put first (see sub_notice.go). The
	// handler fills them: the reason a stopped subscription no longer
	// works. They are not part of the stored pipeline JSON.
	Notices []string
	// Templates 是管理员自备的客户端配置骨架，按槽位存模板正文
	// （TemplateClash / TemplateSingbox / TemplateSurge / TemplateStash /
	// TemplateShadowrocket，见 sub_template.go）。某格式选了模板就渲染
	// 进骨架，而不是内置配置；没选模板的格式与现状逐字一致。
	Templates map[string]string
}

// EndpointProtocol is the protocol clients use for the endpoint.
func EndpointProtocol(e Endpoint) string {
	if e.Proxy != nil {
		if p := ProxyProtocol(e.Proxy); p != "" {
			return p
		}
		return e.Proxy.Type()
	}
	return e.Inbound.Protocol
}

// Validate checks the options. The expression-bearing options it used
// to check (rename / include / exclude / chain / script) were removed
// in 2.1.2; nothing stored can be invalid now, so this always passes.
// It stays as the save path's single validation hook.
func (o SubOptions) Validate() error { return nil }

// FilterEndpoints applies the relay-scope filter: a non-empty
// RelayIDs whitelist is an exact endpoint-set filter (endpoint kept
// iff its RelayID is listed; 0 = direct) and wins over Lines; without
// a whitelist, "direct" keeps direct endpoints, "relay" keeps relayed
// ones, anything else keeps both.
func FilterEndpoints(endpoints []Endpoint, opts SubOptions) []Endpoint {
	if len(opts.RelayIDs) > 0 {
		allowed := make(map[int64]bool, len(opts.RelayIDs))
		for _, id := range opts.RelayIDs {
			allowed[id] = true
		}
		out := make([]Endpoint, 0, len(endpoints))
		for _, e := range endpoints {
			if allowed[e.RelayID] {
				out = append(out, e)
			}
		}
		return out
	}
	if opts.Lines != "relay" && opts.Lines != "direct" {
		return endpoints
	}
	out := make([]Endpoint, 0, len(endpoints))
	for _, e := range endpoints {
		if opts.Lines == "relay" && e.Direct || opts.Lines == "direct" && !e.Direct {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ApplySubOptions runs what is left of the pipeline: the relay-scope
// filter, and stamping each endpoint's Original name (the preview and
// the template renderer read it).
func ApplySubOptions(endpoints []Endpoint, opts SubOptions) []Endpoint {
	endpoints = FilterEndpoints(endpoints, opts)
	out := make([]Endpoint, 0, len(endpoints))
	for _, e := range endpoints {
		if e.Original == "" {
			e.Original = e.Name
		}
		out = append(out, e)
	}
	return out
}

func toSet(list []string) map[string]bool {
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]bool, len(list))
	for _, v := range list {
		m[v] = true
	}
	return m
}
