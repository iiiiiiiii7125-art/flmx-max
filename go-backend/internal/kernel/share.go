package kernel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Endpoint is one address a client can reach an inbound at: the landing node
// itself, or the entry of a relay forward in front of it.
type Endpoint struct {
	Name    string
	Host    string
	Port    int
	Inbound Inbound
	// Direct marks the landing node's own address; only direct endpoints
	// carry the Hysteria2 port-hopping range (relays forward one port).
	Direct bool
	// Proxy is set for imported nodes; it replaces Inbound when rendering.
	Proxy Proxy
	// Original is the name before ApplySubOptions renamed it.
	Original string
	// ChainRole is ChainRoleLanding / ChainRoleRelay when the subscription
	// chains proxies (set by ApplySubOptions).
	ChainRole string
	// SourceID is the remote source an imported node comes from (0 for
	// none).
	SourceID int64
	// RelayID identifies which relay this endpoint belongs to (0 = direct).
	RelayID int64
}

// hopRange is the port-hopping range clients may use for this endpoint.
func (e Endpoint) hopRange() (int, int) {
	if !e.Direct || e.Inbound.Protocol != ProtocolHysteria2 {
		return 0, 0
	}
	return e.Inbound.HopRange()
}

const (
	FormatBase64  = "base64"
	FormatClash   = "clash"
	FormatSingbox = "singbox"
	FormatSurge   = "surge"
)

func (e Endpoint) selfSigned() bool {
	return e.Inbound.Security.Type == SecurityTLS && e.Inbound.Security.CertMode == CertModeSelf
}

func hostPort(host string, port int) string {
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// ShareURI renders the de-facto standard share link (v2rayN / Shadowrocket /
// NekoBox style) for an endpoint.
func ShareURI(e Endpoint) (string, bool) {
	if e.Proxy != nil {
		return proxyURI(e)
	}
	in := e.Inbound
	switch in.Protocol {
	case ProtocolVLESS, ProtocolTrojan:
		q := url.Values{}
		q.Set("type", in.Transport.Type)
		q.Set("security", in.Security.Type)
		if in.Protocol == ProtocolVLESS {
			enc, err := in.Settings.EncryptionString()
			if err != nil {
				return "", false
			}
			q.Set("encryption", enc)
			if in.Settings.Flow != "" {
				q.Set("flow", in.Settings.Flow)
			}
		}
		addTransportQuery(q, in.Transport)
		addSecurityQuery(q, e)
		user := in.Settings.UUID
		if in.Protocol == ProtocolTrojan {
			user = url.PathEscape(in.Settings.Password)
		}
		return fmt.Sprintf("%s://%s@%s?%s#%s", in.Protocol, user, hostPort(e.Host, e.Port),
			q.Encode(), url.PathEscape(e.Name)), true
	case ProtocolVMess:
		v := map[string]interface{}{
			"v": "2", "ps": e.Name, "add": strings.Trim(e.Host, "[]"), "port": strconv.Itoa(e.Port),
			"id": in.Settings.UUID, "aid": "0", "scy": "auto", "net": in.Transport.Type,
			"type": "none", "host": in.Transport.Host, "path": in.Transport.Path, "tls": "",
		}
		if in.Transport.Type == TransportGRPC {
			v["path"] = in.Transport.ServiceName
			v["type"] = "gun"
		}
		if in.Security.Type == SecurityTLS {
			v["tls"] = "tls"
			v["sni"] = in.Security.SNI
			v["fp"] = in.Security.Fingerprint
			if len(in.Security.ALPN) > 0 {
				v["alpn"] = strings.Join(in.Security.ALPN, ",")
			}
			if e.selfSigned() {
				v["allowInsecure"] = "1"
			}
		}
		b, _ := json.Marshal(v)
		return "vmess://" + base64.StdEncoding.EncodeToString(b), true
	case ProtocolShadowsocks:
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(in.Settings.Method + ":" + in.Settings.Password))
		if SS2022KeyLen(in.Settings.Method) > 0 {
			// SIP022: 2022 ciphers use percent-encoded plain userinfo.
			userinfo = url.QueryEscape(in.Settings.Method) + ":" + url.QueryEscape(in.Settings.Password)
		}
		query := ""
		if in.Security.Type == SecurityShadowTLS {
			// SIP002 plugin parameter, as understood by Shadowrocket / NekoBox.
			query = "?plugin=" + url.QueryEscape(fmt.Sprintf("shadow-tls;host=%s;password=%s;version=3",
				in.Security.SNI, in.Security.ShadowTLSPassword))
		}
		return fmt.Sprintf("ss://%s@%s%s#%s", userinfo, hostPort(e.Host, e.Port), query, url.PathEscape(e.Name)), true
	case ProtocolHysteria2:
		q := url.Values{}
		q.Set("sni", in.Security.SNI)
		if e.selfSigned() {
			q.Set("insecure", "1")
		}
		if lo, hi := e.hopRange(); lo > 0 {
			q.Set("mport", fmt.Sprintf("%d-%d", lo, hi)) // v2rayN / NekoBox convention
		}
		return fmt.Sprintf("hysteria2://%s@%s/?%s#%s", url.PathEscape(in.Settings.Password), hostPort(e.Host, e.Port),
			q.Encode(), url.PathEscape(e.Name)), true
	case ProtocolTUIC:
		q := url.Values{}
		q.Set("congestion_control", "bbr")
		q.Set("alpn", "h3")
		q.Set("sni", in.Security.SNI)
		q.Set("udp_relay_mode", "native")
		if e.selfSigned() {
			q.Set("allow_insecure", "1")
		}
		return fmt.Sprintf("tuic://%s:%s@%s?%s#%s", in.Settings.UUID, url.PathEscape(in.Settings.Password),
			hostPort(e.Host, e.Port), q.Encode(), url.PathEscape(e.Name)), true
	case ProtocolNaive:
		// naive+https://user:pass@host:port (NekoBox / v2rayN); sni only when
		// connecting by address (e.g. through a relay).
		q := ""
		if strings.Trim(e.Host, "[]") != in.Security.SNI {
			q = "?sni=" + url.QueryEscape(in.Security.SNI)
		}
		return fmt.Sprintf("naive+https://%s:%s@%s%s#%s", url.PathEscape(in.Settings.Username), url.PathEscape(in.Settings.Password),
			hostPort(e.Host, e.Port), q, url.PathEscape(e.Name)), true
	case ProtocolAnyTLS:
		q := url.Values{}
		q.Set("sni", in.Security.SNI)
		if e.selfSigned() {
			q.Set("insecure", "1")
			q.Set("allowInsecure", "1")
		}
		return fmt.Sprintf("anytls://%s@%s/?%s#%s", url.PathEscape(in.Settings.Password), hostPort(e.Host, e.Port),
			q.Encode(), url.PathEscape(e.Name)), true
	}
	// Snell has no standard share link; it is only exported to Surge.
	return "", false
}

func addTransportQuery(q url.Values, t Transport) {
	switch t.Type {
	case TransportWS:
		q.Set("path", t.Path)
		if t.Host != "" {
			q.Set("host", t.Host)
		}
	case TransportXHTTP:
		q.Set("path", t.Path)
		q.Set("mode", t.Mode)
		if t.Host != "" {
			q.Set("host", t.Host)
		}
	case TransportGRPC:
		q.Set("serviceName", t.ServiceName)
		q.Set("mode", "gun")
	}
}

func addSecurityQuery(q url.Values, e Endpoint) {
	sec := e.Inbound.Security
	switch sec.Type {
	case SecurityReality:
		q.Set("sni", sec.SNI)
		q.Set("fp", sec.Fingerprint)
		q.Set("pbk", sec.RealityPublicKey)
		if len(sec.RealityShortIDs) > 0 {
			q.Set("sid", sec.RealityShortIDs[0])
		}
	case SecurityTLS:
		q.Set("sni", sec.SNI)
		q.Set("fp", sec.Fingerprint)
		if len(sec.ALPN) > 0 {
			q.Set("alpn", strings.Join(sec.ALPN, ","))
		}
		if e.selfSigned() {
			q.Set("allowInsecure", "1")
		}
	}
}

// ClashProxy renders a Mihomo (Clash Meta) proxy entry. XHTTP is rendered
// with xhttp-opts (Mihomo ≥1.19 supports it; the same shape proxy_produce
// emits for imported nodes).
func ClashProxy(e Endpoint) (yamlNode, bool) {
	if e.Proxy != nil {
		return proxyClash(e)
	}
	in := e.Inbound
	if in.Protocol == ProtocolSnell || in.Protocol == ProtocolNaive {
		// Mihomo only speaks Snell v1-v3 and has no NaïveProxy client.
		return nil, false
	}
	switch in.Protocol {
	case ProtocolHysteria2, ProtocolTUIC, ProtocolAnyTLS:
		return clashQUICFamily(e), true
	}
	p := yamlNode{
		{"name", e.Name},
		{"type", map[string]string{ProtocolShadowsocks: "ss"}[in.Protocol]},
		{"server", strings.Trim(e.Host, "[]")},
		{"port", e.Port},
		{"udp", true},
	}
	if p[1].Value == "" {
		p[1].Value = in.Protocol
	}
	switch in.Protocol {
	case ProtocolVLESS:
		p = append(p, yamlField{"uuid", in.Settings.UUID})
		if in.Settings.Flow != "" {
			p = append(p, yamlField{"flow", in.Settings.Flow})
		}
		if in.Settings.EncryptionEnabled() {
			enc, err := in.Settings.EncryptionString()
			if err != nil {
				return nil, false
			}
			p = append(p, yamlField{"encryption", enc})
		}
	case ProtocolVMess:
		p = append(p, yamlField{"uuid", in.Settings.UUID}, yamlField{"alterId", 0}, yamlField{"cipher", "auto"})
	case ProtocolTrojan:
		p = append(p, yamlField{"password", in.Settings.Password})
	case ProtocolShadowsocks:
		p = append(p, yamlField{"cipher", in.Settings.Method}, yamlField{"password", in.Settings.Password})
		if in.Security.Type == SecurityShadowTLS {
			p[4].Value = false // UDP is not carried through ShadowTLS
			p = append(p,
				yamlField{"client-fingerprint", in.Security.Fingerprint},
				yamlField{"plugin", "shadow-tls"},
				yamlField{"plugin-opts", yamlNode{
					{"host", in.Security.SNI},
					{"password", in.Security.ShadowTLSPassword},
					{"version", 3},
				}})
		}
		return p, true
	default:
		return nil, false
	}

	p = append(p, yamlField{"network", in.Transport.Type})
	switch in.Transport.Type {
	case TransportWS:
		ws := yamlNode{{"path", in.Transport.Path}}
		if in.Transport.Host != "" {
			ws = append(ws, yamlField{"headers", yamlNode{{"Host", in.Transport.Host}}})
		}
		p = append(p, yamlField{"ws-opts", ws})
	case TransportGRPC:
		p = append(p, yamlField{"grpc-opts", yamlNode{{"grpc-service-name", in.Transport.ServiceName}}})
	case TransportXHTTP:
		xo := yamlNode{{"path", in.Transport.Path}}
		if in.Transport.Host != "" {
			xo = append(xo, yamlField{"host", in.Transport.Host})
		}
		if in.Transport.Mode != "" {
			xo = append(xo, yamlField{"mode", in.Transport.Mode})
		}
		p = append(p, yamlField{"xhttp-opts", xo})
	}

	sec := in.Security
	if sec.Type != SecurityNone {
		if in.Protocol != ProtocolTrojan {
			p = append(p, yamlField{"tls", true})
		}
		sniKey := "servername"
		if in.Protocol == ProtocolTrojan {
			sniKey = "sni"
		}
		p = append(p, yamlField{sniKey, sec.SNI}, yamlField{"client-fingerprint", sec.Fingerprint})
		if len(sec.ALPN) > 0 {
			p = append(p, yamlField{"alpn", sec.ALPN})
		}
		if sec.Type == SecurityReality {
			ro := yamlNode{{"public-key", sec.RealityPublicKey}}
			if len(sec.RealityShortIDs) > 0 {
				ro = append(ro, yamlField{"short-id", sec.RealityShortIDs[0]})
			}
			p = append(p, yamlField{"reality-opts", ro})
		}
		if e.selfSigned() {
			p = append(p, yamlField{"skip-cert-verify", true})
		}
	}
	return p, true
}

// SingboxOutbounds renders the sing-box client outbounds (1.11+ schema) for
// an endpoint. The first one carries the endpoint's tag; ShadowTLS adds a
// second outbound the first detours through. Snell is left out: only
// sing-box 1.14+ has it (see singboxOutbounds).
func SingboxOutbounds(e Endpoint) ([]map[string]interface{}, bool) {
	return singboxOutbounds(e, false)
}

// singboxOutbounds is SingboxOutbounds; snell includes built Snell inbounds
// for sing-box 1.14+.
func singboxOutbounds(e Endpoint, snell bool) ([]map[string]interface{}, bool) {
	if e.Proxy != nil {
		return proxySingbox(e)
	}
	in := e.Inbound
	if in.Protocol == ProtocolSnell && !snell {
		return nil, false
	}
	if in.Settings.EncryptionEnabled() {
		// sing-box 不支持 VLESS 加密（官方 changelog 与字段文档均无），跳过而非
		// 渲染出缺 encryption 的坏节点。
		return nil, false
	}
	o, ok := singboxOutbound(e)
	if !ok {
		return nil, false
	}
	if (in.Protocol == ProtocolShadowsocks || in.Protocol == ProtocolSnell) && in.Security.Type == SecurityShadowTLS {
		stlsTag := e.Name + " (shadowtls)"
		o["detour"] = stlsTag
		return []map[string]interface{}{o, {
			"type":        "shadowtls",
			"tag":         stlsTag,
			"server":      strings.Trim(e.Host, "[]"),
			"server_port": e.Port,
			"version":     3,
			"password":    in.Security.ShadowTLSPassword,
			"tls": map[string]interface{}{
				"enabled":     true,
				"server_name": in.Security.SNI,
				"utls":        map[string]interface{}{"enabled": true, "fingerprint": in.Security.Fingerprint},
			},
		}}, true
	}
	return []map[string]interface{}{o}, true
}

func singboxOutbound(e Endpoint) (map[string]interface{}, bool) {
	in := e.Inbound
	if in.Transport.Type == TransportXHTTP {
		return nil, false
	}
	o := map[string]interface{}{
		"tag":         e.Name,
		"server":      strings.Trim(e.Host, "[]"),
		"server_port": e.Port,
	}
	switch in.Protocol {
	case ProtocolVLESS:
		o["type"] = "vless"
		o["uuid"] = in.Settings.UUID
		if in.Settings.Flow != "" {
			o["flow"] = in.Settings.Flow
		}
	case ProtocolVMess:
		o["type"] = "vmess"
		o["uuid"] = in.Settings.UUID
		o["security"] = "auto"
	case ProtocolTrojan:
		o["type"] = "trojan"
		o["password"] = in.Settings.Password
	case ProtocolSnell:
		// The v4 client speaks to v4 and v5 servers (sing-box has no v5
		// client: without QUIC proxy mode v5 is v4).
		o["type"] = "snell"
		o["version"] = 4
		if in.Settings.SnellVersion == 6 {
			o["version"] = 6
			if in.Settings.SnellMode != "" {
				o["mode"] = in.Settings.SnellMode
			}
		}
		o["psk"] = in.Settings.Password
		if in.Security.Type == SecurityShadowTLS {
			delete(o, "server")
			delete(o, "server_port")
		}
		return o, true
	case ProtocolShadowsocks:
		o["type"] = "shadowsocks"
		o["method"] = in.Settings.Method
		o["password"] = in.Settings.Password
		if in.Security.Type == SecurityShadowTLS {
			// The shadowtls outbound dials the server; this one has no address.
			delete(o, "server")
			delete(o, "server_port")
		}
		return o, true
	case ProtocolHysteria2:
		o["type"] = "hysteria2"
		o["password"] = in.Settings.Password
		if lo, hi := e.hopRange(); lo > 0 {
			// server_ports replaces server_port; every port in it reaches the inbound.
			delete(o, "server_port")
			o["server_ports"] = []string{fmt.Sprintf("%d:%d", lo, hi)}
			o["hop_interval"] = "30s"
		}
	case ProtocolTUIC:
		o["type"] = "tuic"
		o["uuid"] = in.Settings.UUID
		o["password"] = in.Settings.Password
		o["congestion_control"] = "bbr"
		o["udp_relay_mode"] = "native"
	case ProtocolAnyTLS:
		o["type"] = "anytls"
		o["password"] = in.Settings.Password
	case ProtocolNaive:
		// sing-box 1.13+ (builds with the cronet-based naive outbound).
		o["type"] = "naive"
		o["username"] = in.Settings.Username
		o["password"] = in.Settings.Password
	default:
		return nil, false
	}

	switch in.Transport.Type {
	case TransportWS:
		t := map[string]interface{}{"type": "ws", "path": in.Transport.Path}
		if in.Transport.Host != "" {
			t["headers"] = map[string]interface{}{"Host": in.Transport.Host}
		}
		o["transport"] = t
	case TransportGRPC:
		o["transport"] = map[string]interface{}{"type": "grpc", "service_name": in.Transport.ServiceName}
	}

	sec := in.Security
	if sec.Type != SecurityNone {
		tls := map[string]interface{}{
			"enabled":     true,
			"server_name": sec.SNI,
		}
		// uTLS only applies to TCP; sing-box rejects it on QUIC outbounds,
		// and naive uses Chromium's own TLS stack.
		if in.Network() != "udp" && in.Protocol != ProtocolNaive && sec.Fingerprint != "" {
			tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": sec.Fingerprint}
		}
		if len(sec.ALPN) > 0 {
			tls["alpn"] = sec.ALPN
		}
		if sec.Type == SecurityReality {
			r := map[string]interface{}{"enabled": true, "public_key": sec.RealityPublicKey}
			if len(sec.RealityShortIDs) > 0 {
				r["short_id"] = sec.RealityShortIDs[0]
			}
			tls["reality"] = r
		}
		if e.selfSigned() {
			tls["insecure"] = true
		}
		o["tls"] = tls
	}
	return o, true
}

// SurgeLine renders a Surge [Proxy] line. Surge has no VLESS, Reality,
// gRPC or XHTTP support, so those endpoints are skipped.
func SurgeLine(e Endpoint) (string, bool) {
	if e.Proxy != nil {
		return proxySurge(e)
	}
	in := e.Inbound
	if in.Security.Type == SecurityReality || in.Transport.Type == TransportGRPC || in.Transport.Type == TransportXHTTP {
		return "", false
	}
	// F-081: 行式格式（逗号分字段、首个等号分键值）里全部用户可控值与
	// proxySurge 同口径过 surgeEscape，否则凭据含逗号时字段错位。
	// 名称仍用含 = 的旧替换器（名称出现在 " = " 分隔符之前，口径不同）。
	name := strings.NewReplacer(",", " ", "=", " ", "\n", " ").Replace(e.Name)
	host := strings.Trim(e.Host, "[]")
	parts := []string{}
	shadowTLS := func() []string {
		if in.Security.Type != SecurityShadowTLS {
			return nil
		}
		return []string{"shadow-tls-password=" + surgeEscape(in.Security.ShadowTLSPassword),
			"shadow-tls-sni=" + surgeEscape(in.Security.SNI), "shadow-tls-version=3"}
	}
	tlsTail := func() []string {
		t := []string{"sni=" + surgeEscape(in.Security.SNI)}
		if e.selfSigned() {
			t = append(t, "skip-cert-verify=true")
		}
		return t
	}
	switch in.Protocol {
	case ProtocolShadowsocks:
		parts = append(parts, "ss", host, strconv.Itoa(e.Port),
			"encrypt-method="+in.Settings.Method, "password="+surgeEscape(in.Settings.Password))
		if in.Security.Type == SecurityShadowTLS {
			parts = append(parts, shadowTLS()...)
		} else {
			parts = append(parts, "udp-relay=true")
		}
		return name + " = " + strings.Join(parts, ", "), true
	case ProtocolSnell:
		parts = append(parts, "snell", host, strconv.Itoa(e.Port), "psk="+surgeEscape(in.Settings.Password),
			fmt.Sprintf("version=%d", in.Settings.SnellVersion), "reuse=true", "tfo=true")
		if in.Settings.SnellMode != "" {
			parts = append(parts, "mode="+in.Settings.SnellMode)
		}
		parts = append(parts, shadowTLS()...)
		return name + " = " + strings.Join(parts, ", "), true
	case ProtocolHysteria2:
		parts = append(parts, "hysteria2", host, strconv.Itoa(e.Port), "password="+surgeEscape(in.Settings.Password))
		if lo, hi := e.hopRange(); lo > 0 {
			parts = append(parts, fmt.Sprintf("port-hopping=\"%d-%d\"", lo, hi), "port-hopping-interval=30")
		}
		return name + " = " + strings.Join(append(parts, tlsTail()...), ", "), true
	case ProtocolTUIC:
		parts = append(parts, "tuic-v5", host, strconv.Itoa(e.Port), "password="+surgeEscape(in.Settings.Password),
			"uuid="+surgeEscape(in.Settings.UUID), "alpn=h3")
		return name + " = " + strings.Join(append(parts, tlsTail()...), ", "), true
	case ProtocolAnyTLS:
		parts = append(parts, "anytls", host, strconv.Itoa(e.Port), "password="+surgeEscape(in.Settings.Password))
		return name + " = " + strings.Join(append(parts, tlsTail()...), ", "), true
	case ProtocolVMess:
		parts = append(parts, "vmess", host, strconv.Itoa(e.Port), "username="+surgeEscape(in.Settings.UUID), "vmess-aead=true")
	case ProtocolTrojan:
		parts = append(parts, "trojan", host, strconv.Itoa(e.Port), "password="+surgeEscape(in.Settings.Password))
	default:
		return "", false
	}
	if in.Transport.Type == TransportWS {
		parts = append(parts, "ws=true", "ws-path="+surgeEscape(in.Transport.Path))
		if in.Transport.Host != "" {
			parts = append(parts, "ws-headers=Host:"+surgeEscape(in.Transport.Host))
		}
	}
	if in.Security.Type == SecurityTLS {
		if in.Protocol == ProtocolVMess {
			parts = append(parts, "tls=true")
		}
		parts = append(parts, "sni="+surgeEscape(in.Security.SNI))
		if e.selfSigned() {
			parts = append(parts, "skip-cert-verify=true")
		}
	}
	return name + " = " + strings.Join(parts, ", "), true
}

var singboxUAVersion = regexp.MustCompile(`sing-box[ /]v?(\d+)\.(\d+)`)

// SingboxClientHasSnell reports whether a client UA names sing-box 1.14 or
// later, e.g. "SFA (sing-box 1.14.3; language zh_CN)". An unparseable UA
// returns false (fail-closed: old clients never receive Snell nodes).
func SingboxClientHasSnell(ua string) bool {
	m := singboxUAVersion.FindStringSubmatch(strings.ToLower(ua))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 1 || (major == 1 && minor >= 14)
}

// FormatForUserAgent picks a subscription format from the client's UA.
func FormatForUserAgent(ua string) string {
	ua = strings.ToLower(ua)
	switch {
	case strings.Contains(ua, "shadowrocket"):
		return FormatShadowrocket
	case strings.Contains(ua, "loon"):
		return FormatLoon
	case strings.Contains(ua, "quantumult"):
		return FormatQX
	case strings.Contains(ua, "sing-box") || strings.Contains(ua, "singbox") || strings.Contains(ua, "sfa") || strings.Contains(ua, "sfi") || strings.Contains(ua, "sfm"):
		return FormatSingbox
	case strings.Contains(ua, "clash") || strings.Contains(ua, "mihomo") || strings.Contains(ua, "stash") || strings.Contains(ua, "meta"):
		return FormatClash
	case strings.Contains(ua, "surge") || strings.Contains(ua, "surfboard") || strings.Contains(ua, "egern"):
		// Surfboard 与 Egern 消费 Surge 格式配置。
		return FormatSurge
	}
	return FormatBase64
}

// Subscription renders every supported endpoint in the requested format and
// returns the body with its content type.
func Subscription(format string, endpoints []Endpoint, opts SubOptions) (string, string, error) {
	endpoints = uniqueNames(ApplySubOptions(endpoints, opts), opts.Rules)
	groups := opts.RegionGroups || opts.Rules
	switch format {
	case FormatBase64:
		lines := make([]string, 0, len(endpoints))
		for _, e := range endpoints {
			if uri, ok := ShareURI(e); ok {
				lines = append(lines, uri)
			}
		}
		return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n"))), "text/plain; charset=utf-8", nil
	case FormatClash:
		return clashProfile(endpoints, groups, opts), "text/yaml; charset=utf-8", nil
	case FormatShadowrocket:
		if opts.Rules {
			return shadowrocketConfig(endpoints, opts), "text/yaml; charset=utf-8", nil
		}
		return shadowrocketProfile(endpoints), "text/yaml; charset=utf-8", nil
	case FormatLoon:
		if opts.Rules {
			return loonConfig(endpoints, opts), "text/plain; charset=utf-8", nil
		}
		return loonList(endpoints), "text/plain; charset=utf-8", nil
	case FormatQX:
		if opts.Rules {
			return qxConfig(endpoints, opts), "text/plain; charset=utf-8", nil
		}
		return qxList(endpoints), "text/plain; charset=utf-8", nil
	case FormatSingbox:
		body, err := singboxProfile(endpoints, groups, opts)
		return body, "application/json; charset=utf-8", err
	case FormatSurge:
		lines := []string{"[Proxy]"}
		names := []string{}
		var rendered []Endpoint
		var proxyLines []string
		for _, e := range endpoints {
			if line, ok := SurgeLine(e); ok {
				proxyLines = append(proxyLines, line)
				e.Name = strings.SplitN(line, " = ", 2)[0]
				rendered = append(rendered, e)
			}
		}
		landing, relay, chain := chainGroups(rendered)
		for i, line := range proxyLines {
			if chain && rendered[i].ChainRole == ChainRoleLanding {
				line += ", underlying-proxy=" + ChainRelayGroup
			}
			lines = append(lines, line)
			if listed(rendered[i], chain, opts) {
				names = append(names, rendered[i].Name)
			}
		}
		lines = append(lines, "", "[Proxy Group]")
		var regionLines []string
		var regionNames []string
		choices := names
		if groups {
			for _, g := range regionGroups(names) {
				regionNames = append(regionNames, g.name)
				regionLines = append(regionLines, g.name+" = url-test, "+strings.Join(g.members, ", ")+
					", url=http://www.gstatic.com/generate_204, interval=300")
			}
			choices = append(regionNames, names...)
		}
		if chain {
			choices = append([]string{ChainLandingGroup}, choices...)
		}
		if len(choices) > 0 {
			lines = append(lines, "节点选择 = select, "+strings.Join(choices, ", "))
		} else {
			lines = append(lines, "节点选择 = select, DIRECT")
		}
		if chain {
			lines = append(lines, ChainLandingGroup+" = select, "+strings.Join(landing, ", "),
				ChainRelayGroup+" = select, "+strings.Join(relay, ", "))
		}
		// 移植自 kure29/flyx：规则模式时追加规则策略组并替换规则。
		rules := []string{"GEOIP,LAN,DIRECT", "FINAL,节点选择"}
		if opts.Rules {
			// rulePolicyGroups 的成员模板引用「自动选择」，Surge 分支必须
			// 同步生成同名组（其余渲染器都有），否则组引用悬空。形态对齐
			// clientGroups：url-test、成员为全部节点；无节点时回退 select DIRECT。
			if len(names) > 0 {
				lines = append(lines, "自动选择 = url-test, "+strings.Join(names, ", ")+
					", url=http://www.gstatic.com/generate_204, interval=300")
			} else {
				lines = append(lines, "自动选择 = select, DIRECT")
			}
			for _, g := range rulePolicyGroups(opts, regionNames) {
				lines = append(lines, g.name+" = select, "+strings.Join(g.members, ", "))
			}
			rules = surgeRules(opts)
		}
		lines = append(lines, regionLines...)
		lines = append(append(append(lines, "", "[Rule]"), rules...), "")
		return strings.Join(lines, "\n"), "text/plain; charset=utf-8", nil
	}
	return "", "", fmt.Errorf("不支持的订阅格式 %s", format)
}

func clashProfile(endpoints []Endpoint, withRegions bool, opts SubOptions) string {
	var rendered []Endpoint
	var nodes []yamlNode
	for _, e := range endpoints {
		if p, ok := ClashProxy(e); ok {
			rendered = append(rendered, e)
			nodes = append(nodes, p)
		}
	}
	landing, relay, chain := chainGroups(rendered)
	proxies := []interface{}{}
	names := []interface{}{}
	var plain []string
	for i, e := range rendered {
		p := nodes[i]
		if chain && e.ChainRole == ChainRoleLanding {
			p = append(p, yamlField{"dialer-proxy", ChainRelayGroup})
		}
		proxies = append(proxies, p)
		if listed(e, chain, opts) {
			names = append(names, e.Name)
			plain = append(plain, e.Name)
		}
	}
	var chainNodes []interface{}
	if chain {
		chainNodes = []interface{}{
			yamlNode{{"name", ChainLandingGroup}, {"type", "select"}, {"proxies", toInterfaces(landing)}},
			yamlNode{{"name", ChainRelayGroup}, {"type", "select"}, {"proxies", toInterfaces(relay)}},
		}
	}
	selectList := append([]interface{}{"自动选择"}, names...)
	autoList := names
	var regionNodes []interface{}
	if withRegions {
		var regionNames []interface{}
		for _, g := range regionGroups(plain) {
			regionNames = append(regionNames, g.name)
			members := make([]interface{}, len(g.members))
			for i, m := range g.members {
				members[i] = m
			}
			regionNodes = append(regionNodes, yamlNode{{"name", g.name}, {"type", "url-test"},
				{"url", "https://www.gstatic.com/generate_204"}, {"interval", 300}, {"proxies", members}})
		}
		selectList = append(append([]interface{}{"自动选择"}, regionNames...), names...)
	}
	if chain {
		selectList = append([]interface{}{ChainLandingGroup}, selectList...)
	}
	if len(names) == 0 {
		selectList = []interface{}{"DIRECT"}
		autoList = []interface{}{"DIRECT"}
	}
	// 移植自 kure29/flyx：规则模式时注入策略组、规则提供方与规则。
	ruleGroups := []interface{}{}
	rules := []interface{}{"GEOIP,LAN,DIRECT,no-resolve", "MATCH,节点选择"}
	var providers yamlNode
	if opts.Rules {
		var regionList []string
		for _, g := range regionGroups(plain) {
			regionList = append(regionList, g.name)
		}
		for _, g := range rulePolicyGroups(opts, regionList) {
			members := make([]interface{}, len(g.members))
			for i, m := range g.members {
				members[i] = m
			}
			ruleGroups = append(ruleGroups, yamlNode{{"name", g.name}, {"type", "select"}, {"proxies", members}})
		}
		providers, rules = clashRules(opts)
	}
	profile := yamlNode{
		{"mixed-port", 7890},
		{"allow-lan", false},
		{"mode", "rule"},
		{"log-level", "warning"},
	}
	if opts.Rules {
		profile = append(profile, clashRuleSettings()...)
	}
	profile = append(profile,
		yamlField{"proxies", proxies},
		yamlField{"proxy-groups", append(append([]interface{}{
			yamlNode{{"name", "节点选择"}, {"type", "select"}, {"proxies", selectList}},
			yamlNode{{"name", "自动选择"}, {"type", "url-test"}, {"url", "https://www.gstatic.com/generate_204"},
				{"interval", 300}, {"proxies", autoList}},
		}, ruleGroups...), append(chainNodes, regionNodes...)...)},
	)
	if opts.Rules {
		profile = append(profile, yamlField{"rule-providers", providers})
	}
	profile = append(profile, yamlField{"rules", rules})
	return marshalYAML(profile)
}

func singboxProfile(endpoints []Endpoint, withRegions bool, opts SubOptions) (string, error) {
	var rendered []Endpoint
	var obsList [][]map[string]interface{}
	for _, e := range endpoints {
		if obs, ok := singboxOutbounds(e, opts.SingboxSnell); ok {
			rendered = append(rendered, e)
			obsList = append(obsList, obs)
		}
	}
	landing, relay, chain := chainGroups(rendered)
	outbounds := []interface{}{}
	names := []interface{}{}
	var plain []string
	for i, e := range rendered {
		for _, o := range obsList[i] {
			// The outbound that opens the connection dials through the relay
			// group; a ShadowTLS pair's inner one already detours.
			if _, detour := o["detour"]; chain && e.ChainRole == ChainRoleLanding && !detour {
				o["detour"] = ChainRelayGroup
			}
			outbounds = append(outbounds, o)
		}
		if listed(e, chain, opts) {
			names = append(names, e.Name)
			plain = append(plain, e.Name)
		}
	}
	var chainOutbounds []interface{}
	if chain {
		chainOutbounds = []interface{}{
			map[string]interface{}{"type": "selector", "tag": ChainLandingGroup, "outbounds": landing},
			map[string]interface{}{"type": "selector", "tag": ChainRelayGroup, "outbounds": relay},
		}
	}
	selectList := append([]interface{}{"自动选择"}, names...)
	autoList := names
	var regionOutbounds []interface{}
	if withRegions {
		var regionNames []interface{}
		for _, g := range regionGroups(plain) {
			regionNames = append(regionNames, g.name)
			regionOutbounds = append(regionOutbounds, map[string]interface{}{"type": "urltest", "tag": g.name,
				"outbounds": g.members, "url": "https://www.gstatic.com/generate_204", "interval": "5m"})
		}
		selectList = append(append([]interface{}{"自动选择"}, regionNames...), names...)
	}
	if chain {
		selectList = append([]interface{}{ChainLandingGroup}, selectList...)
	}
	if len(names) == 0 {
		selectList = []interface{}{"direct"}
		autoList = []interface{}{"direct"}
	}
	groups := append([]interface{}{
		map[string]interface{}{"type": "selector", "tag": "节点选择", "outbounds": selectList},
		map[string]interface{}{"type": "urltest", "tag": "自动选择", "outbounds": autoList,
			"url": "https://www.gstatic.com/generate_204", "interval": "5m"},
	}, append(chainOutbounds, regionOutbounds...)...)
	// 移植自 kure29/flyx：规则模式时追加规则策略组。
	if opts.Rules {
		var regionList []string
		for _, g := range regionGroups(plain) {
			regionList = append(regionList, g.name)
		}
		for _, g := range rulePolicyGroups(opts, regionList) {
			if g.members[0] == "REJECT" {
				continue // sing-box 在路由里直接 reject 广告
			}
			members := make([]string, len(g.members))
			for i, m := range g.members {
				members[i] = ruleTarget(m, "direct")
			}
			groups = append(groups, map[string]interface{}{"type": "selector", "tag": g.name, "outbounds": members})
		}
	}
	cfg := map[string]interface{}{
		"log": map[string]interface{}{"level": "warn"},
		"inbounds": []interface{}{
			map[string]interface{}{"type": "tun", "tag": "tun-in", "address": []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
				"auto_route": true, "strict_route": true},
			map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		},
		"outbounds": append(append(groups, outbounds...), map[string]interface{}{"type": "direct", "tag": "direct"}),
		"route": map[string]interface{}{
			"auto_detect_interface": true,
			"final":                 "节点选择",
			"rules": []interface{}{
				map[string]interface{}{"action": "sniff"},
				map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
				map[string]interface{}{"ip_is_private": true, "outbound": "direct"},
			},
		},
	}
	// 移植自 kure29/flyx：规则模式时注入 sing-box 规则、规则集与 DNS。
	if opts.Rules {
		rules, sets, dns := singboxRules(opts)
		route := cfg["route"].(map[string]interface{})
		route["rules"] = append(route["rules"].([]interface{})[:2], rules...)
		route["rule_set"] = sets
		route["final"] = ruleFinalGroup
		route["default_domain_resolver"] = "dns-local"
		cfg["dns"] = dns
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// uniqueNames suffixes duplicate endpoint names; Clash proxy names and
// sing-box tags must be unique or the whole profile is rejected.
// 移植自 kure29/flyx：rules 为 true 时预留规则组名，避免节点重名冲突。
func uniqueNames(endpoints []Endpoint, rules bool) []Endpoint {
	out := make([]Endpoint, len(endpoints))
	seen := map[string]bool{"节点选择": true, "自动选择": true, "direct": true, "DIRECT": true, "REJECT": true,
		ChainLandingGroup: true, ChainRelayGroup: true}
	if rules {
		for _, n := range ruleGroupNames() {
			seen[n] = true
		}
	}
	for _, r := range regions {
		seen[r.name] = true
	}
	for i, e := range endpoints {
		base := strings.TrimSpace(e.Name)
		if base == "" {
			base = fmt.Sprintf("node-%d", i+1)
		}
		name := base
		for n := 2; seen[name]; n++ {
			name = fmt.Sprintf("%s #%d", base, n)
		}
		seen[name] = true
		e.Name = name
		out[i] = e
	}
	return out
}

func clashQUICFamily(e Endpoint) yamlNode {
	in := e.Inbound
	p := yamlNode{
		{"name", e.Name},
		{"type", in.Protocol},
		{"server", strings.Trim(e.Host, "[]")},
		{"port", e.Port},
		{"udp", true},
	}
	switch in.Protocol {
	case ProtocolHysteria2:
		p = append(p, yamlField{"password", in.Settings.Password}, yamlField{"alpn", []string{"h3"}})
		if lo, hi := e.hopRange(); lo > 0 {
			p = append(p, yamlField{"ports", fmt.Sprintf("%d-%d", lo, hi)}, yamlField{"hop-interval", 30})
		}
	case ProtocolTUIC:
		p = append(p,
			yamlField{"uuid", in.Settings.UUID},
			yamlField{"password", in.Settings.Password},
			yamlField{"alpn", []string{"h3"}},
			yamlField{"congestion-controller", "bbr"},
			yamlField{"udp-relay-mode", "native"})
	case ProtocolAnyTLS:
		p = append(p, yamlField{"password", in.Settings.Password}, yamlField{"client-fingerprint", in.Security.Fingerprint})
	}
	p = append(p, yamlField{"sni", in.Security.SNI})
	if e.selfSigned() {
		p = append(p, yamlField{"skip-cert-verify", true})
	}
	return p
}
