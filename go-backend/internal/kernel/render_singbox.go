package kernel

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func renderSingbox(inbounds []Inbound, opts RenderOptions) (string, error) {
	list := make([]interface{}, 0, len(inbounds))
	acme := &acmeProviders{}
	for _, in := range inbounds {
		items, err := singboxInbounds(in, acme)
		if err != nil {
			return "", fmt.Errorf("入站 %d: %v", in.ID, err)
		}
		list = append(list, items...)
	}
	cfg := obj{
		"log":       obj{"level": "warn"},
		"inbounds":  list,
		"outbounds": []interface{}{obj{"type": "direct", "tag": "direct"}},
	}
	if len(acme.list) > 0 {
		cfg["certificate_providers"] = acme.list
	}
	// Chained inbounds leave through their target; their rules come first
	// so their users do not match the direct per-user rules below.
	chained, rules, sets, err := singboxChainRoutesWith(inbounds, opts.SingboxSnell, opts.SingboxNaive)
	if err != nil {
		return "", err
	}
	outbounds := append(cfg["outbounds"].([]interface{}), chained...)
	// Each user leaves through a direct outbound whose sockets carry the
	// user's mark (see UserMark); the first outbound stays the default.
	for _, id := range markedUsers(inbounds) {
		tag := userOutboundTag(id)
		outbounds = append(outbounds, obj{"type": "direct", "tag": tag, "routing_mark": UserMark(id)})
		rules = append(rules, obj{"auth_user": []string{User{ID: id}.Name()}, "outbound": tag})
	}
	cfg["outbounds"] = outbounds
	// The route is always rendered: even an inbound with no chain, split
	// or counted user still needs the private-range guard below.
	route := obj{"rules": singboxGuardRules(rules), "final": "direct"}
	if defs := singboxRuleSets(sets, opts); len(defs) > 0 {
		// sing-box downloads the 分流 rule sets itself and keeps them in
		// the cache file across restarts.
		route["rule_set"] = defs
		cfg["experimental"] = obj{"cache_file": obj{"enabled": true, "path": "cache.db"}}
		if opts.SingboxHTTPClient {
			cfg["http_clients"] = []interface{}{obj{"tag": ruleSetHTTPClient}}
		}
	}
	cfg["route"] = route
	// preferGoDNS 传 false：我方无 GoDNS 特性（见 S4 方案 2.5 注，启用 IP
	// 策略的入站在无 systemd-resolved 的节点上解析走默认 local resolver）。
	if err := applyOutboundIPPolicies(TypeSingbox, cfg, inbounds, false); err != nil {
		return "", err
	}
	// The domain form of the guard runs last: it reads the final rule
	// list (the IP policy pass reorders and appends rules) and the
	// final outbound set.
	guardSingboxDomainExits(cfg)
	return marshalConfig(cfg)
}

// singboxPrivateRejectRule is the node-side private-range guard
// (R7-001): sing-box has no equivalent of Xray freedom's built-in
// defaultBlockPrivateRule, so without this rule every sing-box inbound
// could reach the node's private addresses and the cloud metadata
// address 169.254.169.254 directly. sing-box documents ip_is_private
// as matching non-public IPs (RFC1918, loopback, link-local including
// 169.254/16, CGNAT, ULA, ...), the same destination set the Xray rule
// blocks. The rule carries no inbound or user restriction, so it
// covers every inbound this render emits. This is the literal form;
// the same rule object also serves as the reject half of the domain
// form in guardSingboxDomainExits.
func singboxPrivateRejectRule() obj {
	return obj{"ip_is_private": true, "action": "reject"}
}

// isSingboxPrivateRejectRule identifies the guard rule above; the
// outbound IP policy pass (egress.go) uses it to keep the guard ahead
// of the rules it reorders.
func isSingboxPrivateRejectRule(rule obj) bool {
	private, _ := rule["ip_is_private"].(bool)
	return private && rule["action"] == "reject"
}

// singboxGuardRules returns the route rules with the private-range
// guard in force. Xray blocks private destinations inside the freedom
// outbound, after routing, so no routing rule — chained, split or
// per-user — can steer a private destination anywhere; the sing-box
// equivalent is a reject rule ahead of every routing rule those
// features emit (a user split rule pointing at a private target loses
// to it). Sniff rules stay in front of the guard: sniffing is not a
// final action, it only enriches the connection for the rules that
// follow, mirroring the client configs in share.go where the sniff
// rule precedes the ip_is_private rule.
func singboxGuardRules(rules []interface{}) []interface{} {
	var sniff, rest []interface{}
	for _, r := range rules {
		if rule, ok := r.(obj); ok && rule["action"] == "sniff" {
			sniff = append(sniff, r)
		} else {
			rest = append(rest, r)
		}
	}
	out := make([]interface{}, 0, len(rules)+1)
	out = append(out, sniff...)
	out = append(out, singboxPrivateRejectRule())
	out = append(out, rest...)
	return out
}

// singboxPrivateResolveRule resolves a domain destination so the
// reject rule paired with it can match the resolved addresses.
// sing-box evaluates route rules against the literal destination: a
// domain destination carries no IP, so the head guard above only ever
// sees IP-literal destinations (RM8 S1(b): a Shadowsocks inbound
// reached private and metadata addresses through nip.io names while
// literal probes were rejected). The resolve action stores the
// addresses in the routing context for the rules that follow; it is
// not a final action and does not change the destination itself.
func singboxPrivateResolveRule() obj {
	return obj{"action": "resolve"}
}

// guardSingboxDomainExits extends the private-range guard to domain
// destinations on every local exit of the rendered config. A domain
// destination is only dangerous where this node resolves it: the
// direct outbounds (the default one, the per-user marked ones, the
// outbound-IP-policy clones) and the route final, which this render
// always pins to "direct". Chained and split-proxy outbounds receive
// the domain unchanged and resolve at the far end — the same split
// Xray makes, whose freedom block likewise only governs the local
// exit — so they must not be resolved here: a leading unconditional
// resolve rule would hand every chain outbound a locally resolved IP
// instead of the domain (verified against sing-box 1.14.4).
//
// For each rule that selects a direct outbound, an identically
// conditioned resolve + reject pair is inserted immediately ahead of
// it: the pair fires exactly for the connections that rule would take
// (any other connection fails the copied conditions and falls through
// with its domain intact), and the reject half matches when any
// resolved address is private — a mixed A/AAAA answer with one
// private address is rejected as a whole, as is an answer that mixes
// public and private addresses. One unconditional pair closes the
// list for the connections no rule matched, which leave through the
// final. The reject half is the head guard's own rule, so the literal
// and domain forms stay a single predicate: ip_is_private -> reject.
func guardSingboxDomainExits(cfg obj) {
	route, _ := cfg["route"].(obj)
	if route == nil {
		return
	}
	rules, _ := route["rules"].([]interface{})
	outbounds, _ := cfg["outbounds"].([]interface{})
	direct := map[string]bool{}
	for _, v := range outbounds {
		if ob, ok := v.(obj); ok && ob["type"] == "direct" {
			if tag, ok := ob["tag"].(string); ok && tag != "" {
				direct[tag] = true
			}
		}
	}
	guarded := make([]interface{}, 0, len(rules)*3+2)
	for _, v := range rules {
		if rule, ok := v.(obj); ok {
			if tag, ok := rule["outbound"].(string); ok && direct[tag] {
				guarded = append(guarded, singboxExitGuardPair(rule)...)
			}
		}
		guarded = append(guarded, v)
	}
	guarded = append(guarded, singboxPrivateResolveRule(), singboxPrivateRejectRule())
	route["rules"] = guarded
}

// singboxExitGuardPair returns the resolve + reject pair guarding one
// direct-exit rule: both halves copy the rule's conditions (everything
// but its outbound and action), so they match exactly the connections
// the rule itself would match.
func singboxExitGuardPair(anchor obj) []interface{} {
	resolve := singboxPrivateResolveRule()
	reject := singboxPrivateRejectRule()
	for key, value := range anchor {
		if key == "outbound" || key == "action" {
			continue
		}
		resolve[key] = value
		reject[key] = value
	}
	return []interface{}{resolve, reject}
}

// acmeProviders shares one ACME certificate provider per domain + challenge
// setup, so inbounds on the same domain do not race for port 80 or the
// account. Requires sing-box 1.14+.
type acmeProviders struct {
	list []interface{}
	tags map[string]string
}

func (a *acmeProviders) tag(sec Security) string {
	key := strings.Join([]string{sec.SNI, sec.ACMEEmail, sec.ACMEDNS, sec.ACMEDNSKey, sec.ACMEDNSSecret}, "\x00")
	if t, ok := a.tags[key]; ok {
		return t
	}
	if a.tags == nil {
		a.tags = map[string]string{}
	}
	t := fmt.Sprintf("acme-%d", len(a.list)+1)
	p := obj{
		"type":           "acme",
		"tag":            t,
		"domain":         []string{sec.SNI},
		"data_directory": "acme", // relative to the run dir, kept across applies
	}
	if sec.ACMEEmail != "" {
		p["email"] = sec.ACMEEmail
	}
	switch sec.ACMEDNS {
	case ACMEDNSCloudflare:
		p["dns01_challenge"] = obj{"provider": "cloudflare", "api_token": sec.ACMEDNSKey}
	case ACMEDNSAliDNS:
		p["dns01_challenge"] = obj{"provider": "alidns", "access_key_id": sec.ACMEDNSKey, "access_key_secret": sec.ACMEDNSSecret}
	}
	a.tags[key] = t
	a.list = append(a.list, p)
	return t
}

// singboxInbounds returns the inbound objects for one panel inbound. A
// ShadowTLS-fronted Shadowsocks or Snell becomes two: the public shadowtls
// inbound and a loopback backend it detours to.
func singboxInbounds(in Inbound, acme *acmeProviders) ([]interface{}, error) {
	item := obj{
		"tag":         in.Tag(),
		"listen":      in.Listen,
		"listen_port": in.Port,
	}
	s := in.Settings
	switch in.Protocol {
	case ProtocolVLESS:
		user := obj{"uuid": s.UUID}
		if s.Flow != "" {
			user["flow"] = s.Flow
		}
		item["type"] = "vless"
		item["users"] = []interface{}{user}
	case ProtocolVMess:
		item["type"] = "vmess"
		item["users"] = []interface{}{obj{"uuid": s.UUID, "alterId": 0}}
	case ProtocolTrojan:
		item["type"] = "trojan"
		item["users"] = []interface{}{obj{"password": s.Password}}
	case ProtocolShadowsocks:
		// GATE-F1：服务端入站与客户端订阅同 key 字节——合法单段
		// SS2022 key 统一 Std 形态输出（Normalize 保存面已把关，
		// 此处兜底直写存量），客户端规范化后两端仍同字节。
		if in.Security.Type == SecurityShadowTLS {
			return singboxShadowTLSFront(in, obj{
				"type":     "shadowsocks",
				"method":   s.Method,
				"password": NormalizedSS2022Password(s.Method, s.Password),
			})
		}
		item["type"] = "shadowsocks"
		item["method"] = s.Method
		item["password"] = NormalizedSS2022Password(s.Method, s.Password)
	case ProtocolSnell:
		// Server version 5 also serves v4 clients (the same wire protocol
		// without QUIC proxy mode, which sing-box does not offer).
		snell := obj{"type": "snell", "version": 5, "psk": s.Password}
		if s.SnellVersion == 6 {
			snell["version"] = 6
			if s.SnellMode != "" {
				snell["mode"] = s.SnellMode
			}
		}
		if in.Security.Type == SecurityShadowTLS {
			return singboxShadowTLSFront(in, snell)
		}
		for k, v := range snell {
			item[k] = v
		}
	case ProtocolHysteria2:
		item["type"] = "hysteria2"
		item["users"] = []interface{}{obj{"password": s.Password}}
		item["ignore_client_bandwidth"] = true
		if s.Masquerade != "" {
			item["masquerade"] = s.Masquerade
		}
	case ProtocolTUIC:
		item["type"] = "tuic"
		item["users"] = []interface{}{obj{"uuid": s.UUID, "password": s.Password}}
		item["congestion_control"] = "bbr"
	case ProtocolAnyTLS:
		item["type"] = "anytls"
		item["users"] = []interface{}{obj{"password": s.Password}}
	case ProtocolNaive:
		item["type"] = "naive"
		item["users"] = []interface{}{obj{"username": s.Username, "password": s.Password}}
	default:
		return nil, fmt.Errorf("sing-box 不支持协议 %s", in.Protocol)
	}
	if extra := singboxUsers(in); len(extra) > 0 {
		// The append must be nil-safe: branches that never create a
		// "users" key (Shadowsocks) previously relied on singboxUsers
		// returning nil for them; an SS companion's users land here and
		// the old hard assertion would panic on the missing key.
		existing, _ := item["users"].([]interface{})
		item["users"] = append(existing, extra...)
	}

	if tls, err := singboxTLS(in.Security, acme); err != nil {
		return nil, err
	} else if tls != nil {
		item["tls"] = tls
	}
	switch in.Transport.Type {
	case TransportWS:
		t := obj{"type": "ws", "path": in.Transport.Path}
		if in.Transport.Host != "" {
			t["headers"] = obj{"Host": in.Transport.Host}
		}
		item["transport"] = t
	case TransportGRPC:
		item["transport"] = obj{"type": "grpc", "service_name": in.Transport.ServiceName}
	}
	if err := mergeExtra(item, in.Extra); err != nil {
		return nil, err
	}
	return []interface{}{item}, nil
}

// singboxShadowTLSFront returns the public shadowtls v3 inbound and the
// loopback backend (a Shadowsocks or Snell inbound) it detours to.
func singboxShadowTLSFront(in Inbound, backend obj) ([]interface{}, error) {
	backendTag := singboxShadowTLSBackendTag(in)
	front := obj{
		"type":        "shadowtls",
		"tag":         in.Tag(),
		"listen":      in.Listen,
		"listen_port": in.Port,
		"version":     3,
		"users":       []interface{}{obj{"password": in.Security.ShadowTLSPassword}},
		"handshake":   obj{"server": in.Security.SNI, "server_port": 443},
		"detour":      backendTag,
	}
	backend["tag"] = backendTag
	backend["listen"] = "127.0.0.1"
	if err := mergeExtra(front, in.Extra); err != nil {
		return nil, err
	}
	return []interface{}{front, backend}, nil
}

func singboxTLS(sec Security, acme *acmeProviders) (obj, error) {
	switch sec.Type {
	case SecurityTLS:
		tls := obj{"enabled": true, "server_name": sec.SNI}
		if len(sec.ALPN) > 0 {
			tls["alpn"] = sec.ALPN
		}
		switch sec.CertMode {
		case CertModeACME:
			tls["certificate_provider"] = acme.tag(sec)
		case CertModeFile:
			tls["certificate_path"] = sec.CertFile
			tls["key_path"] = sec.KeyFile
		default:
			tls["certificate"] = pemLines(sec.CertPEM)
			tls["key"] = pemLines(sec.KeyPEM)
		}
		return tls, nil
	case SecurityReality:
		host, portStr, err := net.SplitHostPort(sec.RealityDest)
		if err != nil {
			return nil, fmt.Errorf("Reality 目标地址错误")
		}
		port, _ := strconv.Atoi(portStr)
		return obj{
			"enabled":     true,
			"server_name": sec.SNI,
			"reality": obj{
				"enabled":     true,
				"handshake":   obj{"server": host, "server_port": port},
				"private_key": sec.RealityPrivateKey,
				"short_id":    sec.RealityShortIDs,
			},
		}, nil
	}
	return nil, nil
}

// ruleSetHTTPClient is the HTTP client rule sets are downloaded with: a
// direct connection.
const ruleSetHTTPClient = "rule-set"

// singboxRuleSets defines each rule set once, in first-use order.
func singboxRuleSets(tags []string, opts RenderOptions) []interface{} {
	var out []interface{}
	seen := map[string]bool{}
	for _, t := range tags {
		if seen[t] {
			continue
		}
		seen[t] = true
		// Downloaded directly (download_detour is deprecated in sing-box
		// 1.14; before it, the default outbound is the direct one).
		set := obj{"type": "remote", "tag": t, "format": "binary", "url": RuleSetURL(t, opts.RuleSetMirror)}
		if opts.SingboxHTTPClient {
			set["http_client"] = ruleSetHTTPClient
		}
		out = append(out, set)
	}
	return out
}
