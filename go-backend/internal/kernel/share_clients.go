package kernel

import (
	"fmt"
	"strings"
)

// Node-list formats for iOS clients. They are produced from the Mihomo
// schema (the panel's own inbounds go through ClashProxy first), like
// Sub-Store does; nodes a client cannot express are skipped.

const (
	FormatShadowrocket = "shadowrocket"
	FormatLoon         = "loon"
	FormatQX           = "qx"
)

// endpointProxy returns the endpoint as a Mihomo proxy, named after it.
func endpointProxy(e Endpoint) (Proxy, bool) {
	if e.Proxy != nil {
		return e.Proxy.forEndpoint(e), true
	}
	node, ok := ClashProxy(e)
	if !ok {
		return nil, false
	}
	return Proxy(yamlToMap(node)), true
}

func yamlToMap(n yamlNode) map[string]interface{} {
	out := make(map[string]interface{}, len(n))
	for _, f := range n {
		if sub, ok := f.Value.(yamlNode); ok {
			out[f.Key] = yamlToMap(sub)
		} else {
			out[f.Key] = f.Value
		}
	}
	return out
}

// ---- Shadowrocket ----

// shadowrocketProfile is a Clash proxy list, which Shadowrocket imports as
// a node subscription. Groups are not imported, so a chained landing node
// names its relay directly: one landing node per relay node.
func shadowrocketProfile(endpoints []Endpoint) string {
	var rendered []Endpoint
	var nodes []yamlNode
	for _, e := range endpoints {
		// 与 config 路径同口径：SR 无法表达的节点（VLESS 加密、WireGuard）不进
		// profile，避免用户拿到能显示却握手必失败的坏节点。
		if p, ok := endpointProxy(e); ok && shadowrocketSkips(p) {
			continue
		}
		if p, ok := ClashProxy(e); ok {
			rendered = append(rendered, e)
			nodes = append(nodes, p)
		}
	}
	_, relays, chain := chainGroups(rendered)
	proxies := []interface{}{}
	for i, e := range rendered {
		if !chain || e.ChainRole != ChainRoleLanding {
			proxies = append(proxies, nodes[i])
			continue
		}
		for _, relay := range relays {
			p := append(yamlNode{}, nodes[i]...)
			if len(relays) > 1 {
				p[0] = yamlField{"name", chainedName(e.Name, relay)}
			}
			proxies = append(proxies, append(p, yamlField{"dialer-proxy", relay}))
		}
	}
	return marshalYAML(yamlNode{{"proxies", proxies}})
}

// shadowrocketProxies renders Clash proxies for Shadowrocket, expanding
// chained landing nodes into one copy per relay. Names are returned in
// the same order. 移植自 kure29/flyx（供规则模式的 shadowrocketConfig 用）。
//
// A5-03：proxies 保留全部条目（中转条目是落地副本 dialer-proxy 的指向
// 目标，删之整链悬空，与 Clash 把中转保留在 proxies 同口径）；names
// （进普通策略组的那份）按 listed() 过滤，隐藏的中转不进任何组。
func shadowrocketProxies(endpoints []Endpoint, opts SubOptions) ([]interface{}, []string) {
	var rendered []Endpoint
	var nodes []yamlNode
	for _, e := range endpoints {
		if p, ok := ClashProxy(e); ok && !shadowrocketSkips(Proxy(yamlToMap(p))) {
			rendered = append(rendered, e)
			nodes = append(nodes, p)
		}
	}
	_, relays, chain := chainGroups(rendered)
	proxies := []interface{}{}
	names := []string{}
	for i, e := range rendered {
		if !chain || e.ChainRole != ChainRoleLanding {
			proxies = append(proxies, nodes[i])
			if listed(e, chain, opts) {
				names = append(names, e.Name)
			}
			continue
		}
		for _, relay := range relays {
			p := append(yamlNode{}, nodes[i]...)
			name := e.Name
			if len(relays) > 1 {
				name = chainedName(e.Name, relay)
				p[0] = yamlField{"name", name}
			}
			proxies = append(proxies, append(p, yamlField{"dialer-proxy", relay}))
			names = append(names, name)
		}
	}
	return proxies, names
}

// shadowrocketSkips reports nodes Shadowrocket cannot express.
// 移植自 kure29/flyx。
func shadowrocketSkips(p Proxy) bool {
	return hasVLESSEncryption(p) || p.Type() == "wireguard"
}

// hasVLESSEncryption reports whether a VLESS proxy sets a non-none
// encryption (which Shadowrocket/Loon cannot express).
// 移植自 kure29/flyx。
func hasVLESSEncryption(p Proxy) bool {
	enc := p.str("encryption")
	return enc != "" && enc != "none"
}

// chainedName names the copy of a landing node dialed through one relay.
func chainedName(landing, relay string) string {
	return landing + "（经 " + relay + "）"
}

// ---- Loon ----

// loonList renders Loon node lines. Loon nodes cannot name a front proxy,
// so landing nodes connect directly.
func loonList(endpoints []Endpoint, opts SubOptions) string {
	_, _, chain := chainGroups(endpoints)
	var lines []string
	for _, e := range endpoints {
		// A5-03：列表无组概念，ChainHideRelays 的隐藏即不下发该行
		// （落地直连、无引用悬空），与三主格式的 listed() 语义对齐。
		if !listed(e, chain, opts) {
			continue
		}
		if p, ok := endpointProxy(e); ok {
			if line, ok := loonLine(p); ok {
				lines = append(lines, line)
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func loonName(p Proxy) string {
	return strings.NewReplacer(",", " ", "=", " ", "\n", " ").Replace(p.str("name"))
}

var loonSSCiphers = toSet([]string{"aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305",
	"xchacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305", "aes-128-cfb", "aes-256-cfb",
	"chacha20", "chacha20-ietf", "rc4-md5"})

func loonLine(p Proxy) (string, bool) {
	// R11 批 2（R11X-003）：Loon 面入口补统一谓词门——此前仅 SS
	// cipher 白名单/key 与 VLESS flow/ENC 有手工门，SS plugin-opts.
	// mode、VMess cipher、Reality 等存量垃圾原样下发。面参数取
	// ssFaceClash：Loon 白名单含 SS 单面集 chacha20（本面合法），
	// 以 ssFaceOther/ssFaceSingbox 设门会误杀单面集；其余单面集值
	// （aes-*-gcm-siv 等 5 值）不在 Loon 白名单内、仍由下方白名单
	// 略过，处置不变。谓词面参数只判别 SS 单面集一格。既有手工门
	// （SS 白名单、VLESS flow 仅 vision、ENC 略过、Hy2 obfs 仅
	// salamander）保留、与本门叠加。
	if !validImportedProxyFields(p, ssFaceClash) {
		return "", false
	}
	if enc := p.str("encryption"); enc != "" && enc != "none" {
		return "", false // Loon 无 VLESS 加密的已证实支持，跳过而非渲染坏节点
	}
	server, port := p.str("server"), p.num("port")
	parts := []string{}
	add := func(s ...string) { parts = append(parts, s...) }
	// R5-B5 订阅转义：Loon 行式格式用逗号分隔字段、首个等号分隔键值。
	// F-081: 与 surgeEscape 同口径——只处理逗号/换行，值内的 = 必须保留
	// （SS 2022 base64 密钥自带 == 填充）。
	loonEscape := func(s string) string {
		return strings.NewReplacer(",", " ", "\n", " ", "\r", " ").Replace(s)
	}
	addIf := func(key, v string) {
		if v != "" {
			add(key + "=" + loonEscape(v))
		}
	}
	tlsTail := func() {
		if p.flag("skip-cert-verify") {
			add("skip-cert-verify=true")
		}
		if ro := p.sub("reality-opts"); len(ro) > 0 {
			addIf("sni", p.serverName())
			add(fmt.Sprintf("public-key=%q", ro.str("public-key")))
			addIf("short-id", ro.str("short-id"))
		} else {
			addIf("tls-name", p.serverName())
		}
	}
	transport := func() bool {
		host, path, _, _ := transportFields(p)
		switch p.network() {
		case "tcp":
			add("transport=tcp")
		case "ws":
			if p.sub("ws-opts").flag("v2ray-http-upgrade") {
				return false
			}
			add("transport=ws")
			addIf("path", path)
			addIf("host", host)
		default:
			return false
		}
		return true
	}
	udp := func() {
		if p.flag("udp") {
			add("udp=true")
		}
	}

	switch p.Type() {
	case "ss":
		if !loonSSCiphers[p.str("cipher")] {
			return "", false
		}
		// R6 批 1（未验证面同批设门，恒标未验证）：cipher 白名单自守
		// 维持不改接；补 SS2022 key 逐段谓词门（与 sing-box/Clash 同
		// 谓词），坏 key 整节点略过。
		if ValidateSS2022Password(p.str("cipher"), p.str("password")) != nil {
			return "", false
		}
		add("shadowsocks", server, fmt.Sprint(port), p.str("cipher"), fmt.Sprintf("%q", p.str("password")))
		po := p.sub("plugin-opts")
		switch p.str("plugin") {
		case "":
		case "obfs":
			add("obfs-name=" + po.str("mode"))
			addIf("obfs-host", po.str("host"))
			addIf("obfs-uri", po.str("path"))
		case "shadow-tls":
			add("shadow-tls-password=" + loonEscape(po.str("password")))
			addIf("shadow-tls-sni", po.str("host"))
			if v := po.num("version"); v > 0 {
				add(fmt.Sprintf("shadow-tls-version=%d", v))
			}
		default:
			return "", false
		}
		udp()
	case "vmess":
		add("vmess", server, fmt.Sprint(port), firstNonEmptyString(p.str("cipher"), "auto"), fmt.Sprintf("%q", p.str("uuid")))
		if !transport() {
			return "", false
		}
		if p.usesTLS() {
			add("over-tls=true")
			tlsTail()
		}
		add(fmt.Sprintf("alterId=%d", p.num("alterId")))
		udp()
	case "vless":
		if f := p.str("flow"); f != "" && f != FlowVision {
			return "", false
		}
		add("vless", server, fmt.Sprint(port), fmt.Sprintf("%q", p.str("uuid")))
		if !transport() {
			return "", false
		}
		if p.usesTLS() {
			add("over-tls=true")
			addIf("flow", p.str("flow"))
			tlsTail()
		}
		udp()
	case "trojan":
		add("trojan", server, fmt.Sprint(port), fmt.Sprintf("%q", p.str("password")))
		if p.network() != "tcp" && !transport() {
			return "", false
		}
		tlsTail()
		udp()
	case "hysteria2":
		if ob := p.str("obfs"); ob != "" && ob != "salamander" {
			return "", false
		}
		add("Hysteria2", server, fmt.Sprint(port), fmt.Sprintf("%q", p.str("password")))
		if ports := p.str("ports"); ports != "" {
			add(fmt.Sprintf("server-ports=%q", ports))
		}
		tlsTail()
		addIf("salamander-password", p.str("obfs-password"))
		add("udp=true")
	case "anytls":
		add("anytls", server, fmt.Sprint(port), fmt.Sprintf("%q", p.str("password")))
		tlsTail()
		add("udp=true")
	case "http":
		kind := "http"
		if p.flag("tls") {
			kind = "https"
		}
		add(kind, server, fmt.Sprint(port))
		if u := p.str("username"); u != "" {
			add(u, fmt.Sprintf("%q", p.str("password")))
		}
		if kind == "https" {
			tlsTail()
		}
	case "socks5":
		add("socks5", server, fmt.Sprint(port))
		if u := p.str("username"); u != "" {
			add(u, fmt.Sprintf("%q", p.str("password")))
		}
		if p.flag("tls") {
			add("over-tls=true")
			tlsTail()
		}
		udp()
	default:
		return "", false
	}
	return loonName(p) + "=" + strings.Join(parts, ","), true
}

// ---- Quantumult X ----

// qxList renders Quantumult X server lines ([server_remote] resource). QX
// has no front-proxy option, so landing nodes connect directly.
func qxList(endpoints []Endpoint, opts SubOptions) string {
	_, _, chain := chainGroups(endpoints)
	var lines []string
	for _, e := range endpoints {
		if !listed(e, chain, opts) { // A5-03：同 loonList
			continue
		}
		if p, ok := endpointProxy(e); ok {
			if line, ok := qxLine(p); ok {
				lines = append(lines, line)
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

var qxSSCiphers = toSet([]string{"aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305",
	"xchacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305", "aes-128-cfb", "aes-256-cfb",
	"chacha20", "chacha20-ietf", "rc4-md5", "none"})

func qxLine(p Proxy) (string, bool) {
	// R11 批 2（R11X-003）：QX 面入口补统一谓词门——理由与面参数
	// 同 loonLine 注：QX 白名单含 SS 单面集 chacha20（本面合法），
	// 面参数取 ssFaceClash，不得以 ssFaceOther/ssFaceSingbox 误杀
	// 单面集；其余单面集值不在 QX 白名单内、仍由下方白名单略过。
	// 既有手工门（SS 白名单、VLESS flow 仅 vision、ENC 略过）
	// 保留、与本门叠加。
	if !validImportedProxyFields(p, ssFaceClash) {
		return "", false
	}
	if enc := p.str("encryption"); enc != "" && enc != "none" {
		return "", false // QX 无 VLESS 加密的已证实支持，跳过而非渲染坏节点
	}
	parts := []string{fmt.Sprintf("%s:%d", p.str("server"), p.num("port"))}
	add := func(s ...string) { parts = append(parts, s...) }
	// R5-B5 订阅转义：QX 行式格式用逗号分隔字段、首个等号分隔键值。
	// F-081: 与 surgeEscape 同口径——只处理逗号/换行，值内的 = 必须保留
	// （SS 2022 base64 密钥自带 == 填充）。
	qxEscape := func(s string) string {
		return strings.NewReplacer(",", " ", "\n", " ", "\r", " ").Replace(s)
	}
	addIf := func(key, v string) {
		if v != "" {
			add(key + "=" + qxEscape(v))
		}
	}
	reality := func() {
		if ro := p.sub("reality-opts"); len(ro) > 0 {
			add("reality-base64-pubkey=" + ro.str("public-key"))
			addIf("reality-hex-shortid", ro.str("short-id"))
		}
	}
	verify := func() {
		if p.flag("skip-cert-verify") {
			add("tls-verification=false")
		} else {
			add("tls-verification=true")
		}
	}
	// obfs carries TLS and WebSocket for VMess / VLESS / Trojan(ws); its
	// host is the WebSocket Host, else the TLS server name.
	obfs := func() bool {
		host, path, _, _ := transportFields(p)
		tls := p.usesTLS()
		switch p.network() {
		case "tcp":
			if !tls {
				return true
			}
			add("obfs=over-tls")
		case "ws":
			if p.sub("ws-opts").flag("v2ray-http-upgrade") {
				return false
			}
			if tls {
				add("obfs=wss")
			} else {
				add("obfs=ws")
			}
			addIf("obfs-uri", path)
		default:
			return false
		}
		addIf("obfs-host", firstNonEmptyString(host, p.serverName()))
		if tls {
			verify()
			reality()
		}
		return true
	}

	var kind string
	switch p.Type() {
	case "ss":
		if !qxSSCiphers[p.str("cipher")] {
			return "", false
		}
		// R6 批 1（未验证面同批设门，恒标未验证）：同 loonLine——cipher
		// 白名单自守维持，补 SS2022 key 逐段谓词门，坏 key 整节点略过。
		if ValidateSS2022Password(p.str("cipher"), p.str("password")) != nil {
			return "", false
		}
		kind = "shadowsocks"
		add("method="+p.str("cipher"), "password="+qxEscape(p.str("password")))
		po := p.sub("plugin-opts")
		switch p.str("plugin") {
		case "":
		case "obfs":
			add("obfs=" + po.str("mode"))
			addIf("obfs-host", po.str("host"))
			addIf("obfs-uri", po.str("path"))
		default:
			return "", false
		}
		if p.flag("udp") {
			add("udp-relay=true")
		}
	case "vmess":
		kind = "vmess"
		method := p.str("cipher")
		if method == "" || method == "auto" {
			method = "chacha20-poly1305"
		}
		add("method="+method, "password="+qxEscape(p.str("uuid")))
		if !obfs() {
			return "", false
		}
		add(fmt.Sprintf("aead=%t", p.num("alterId") == 0))
	case "vless":
		if f := p.str("flow"); f != "" && f != FlowVision {
			return "", false
		}
		kind = "vless"
		add("method=none", "password="+qxEscape(p.str("uuid")))
		if !obfs() {
			return "", false
		}
		addIf("vless-flow", p.str("flow"))
		if p.flag("udp") {
			add("udp-relay=true")
		}
	case "trojan":
		kind = "trojan"
		add("password=" + qxEscape(p.str("password")))
		switch p.network() {
		case "ws":
			if !obfs() {
				return "", false
			}
		case "tcp":
			add("over-tls=true")
			addIf("tls-host", p.serverName())
			verify()
			reality()
		default:
			return "", false
		}
	case "anytls":
		kind = "anytls"
		add("password="+qxEscape(p.str("password")), "over-tls=true")
		addIf("tls-host", p.serverName())
		verify()
		reality()
		add("udp-relay=true")
	case "http", "socks5":
		kind = p.Type()
		if u := p.str("username"); u != "" {
			add("username="+qxEscape(u), "password="+qxEscape(p.str("password")))
		}
		if p.flag("tls") {
			add("over-tls=true")
			addIf("tls-host", p.serverName())
			verify()
		}
	default:
		return "", false
	}
	tag := strings.NewReplacer(",", " ", "\n", " ").Replace(p.str("name"))
	return kind + "=" + strings.Join(append(parts, "tag="+tag), ", "), true
}

// qxName returns the Quantumult X-safe node name.
// 移植自 kure29/flyx（供规则模式的 qxConfig 用）。
func qxName(p Proxy) string {
	return strings.NewReplacer(",", " ", "\n", " ").Replace(p.str("name"))
}
