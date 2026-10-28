package kernel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ---- share link ----

func proxyURI(e Endpoint) (string, bool) {
	p := e.Proxy.forEndpoint(e)
	hp := hostPort(e.Host, e.Port)
	frag := "#" + url.PathEscape(e.Name)
	switch p.Type() {
	case "ss":
		cipher, pw := p.str("cipher"), p.str("password")
		// R6 批 1（未验证面同批设门，恒标未验证）：URI/base64 面以同一
		// 谓词设门——双拒 cipher 与 SS2022 坏 key 整节点略过，不再原样
		// 下发；单面集方法（chacha20 等 6 值）在本面保留（通用链接面、无整表毒化）。
		// R11 批 2（R11X-002）：ss 支改经统一谓词——plugin-opts.mode 格
		// 随谓词并入（此前 ssPluginString 只小写愈合、垃圾 mode 原样
		// 发链）。面参数取 ssFaceClash：本面按 R6 口径保留单面集
		// （chacha20 等 6 值），与 ssFaceOther 的单面集略过口径不符，
		// 以 other 设门会误杀单面集合法格；谓词其余格（cipher 合法集、
		// SS2022 key、横切 fp/xhttp、Reality）与原门同口径。
		if !validImportedProxyFields(p, ssFaceClash) {
			return "", false
		}
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(cipher + ":" + pw))
		if SS2022KeyLen(cipher) > 0 {
			userinfo = url.QueryEscape(cipher) + ":" + url.QueryEscape(pw)
		}
		query := ""
		if plugin := ssPluginString(p); plugin != "" {
			query = "/?plugin=" + url.QueryEscape(plugin)
		}
		return "ss://" + userinfo + "@" + hp + query + frag, true
	case "vmess":
		// R11 批 2（R11X-002）：vmess 支补统一谓词门（ssFaceOther）——
		// cipher 与横切 fp 此前无门、存量垃圾值原样发链。
		if !validImportedProxyFields(p, ssFaceOther) {
			return "", false
		}
		v := map[string]interface{}{"v": "2", "ps": e.Name, "add": strings.Trim(e.Host, "[]"), "port": strconv.Itoa(e.Port),
			"id": p.str("uuid"), "aid": strconv.Itoa(p.num("alterId")), "scy": firstNonEmptyString(p.str("cipher"), "auto"),
			"net": "tcp", "type": "none", "host": "", "path": "", "tls": ""}
		host, path, service, _ := transportFields(p)
		switch n := p.network(); n {
		case "ws", "grpc", "h2":
			v["net"] = n
			v["host"], v["path"] = host, path
			if n == "grpc" {
				v["path"], v["type"] = service, "gun"
			}
		case "http":
			v["type"], v["host"], v["path"] = "http", host, path
		}
		if ro := p.sub("reality-opts"); len(ro) > 0 {
			// R2-L3-F-01：Reality 节点回放 tls=reality + pbk/sid/
			// spx/realityDest，否则再导入一轮即丢 Reality（与
			// VLESS 支 L5-F-01 的 realityDest/spx 回放同口径；
			// 本支入口的 validImportedProxyFields 已过
			// validateRealityOpts，坏值到不了这里）。
			v["tls"] = "reality"
			v["sni"] = p.serverName()
			v["fp"] = p.str("client-fingerprint")
			if alpn := p.strs("alpn"); len(alpn) > 0 {
				v["alpn"] = strings.Join(alpn, ",")
			}
			if p.flag("skip-cert-verify") {
				v["allowInsecure"] = "1"
			}
			v["pbk"] = ro.str("public-key")
			if sid := ro.str("short-id"); sid != "" {
				v["sid"] = sid
			}
			if spx := ro.str("spider-x"); spx != "" {
				v["spx"] = spx
			}
			if dest := ro.str("dest"); dest != "" {
				v["realityDest"] = dest
			}
		} else if p.usesTLS() {
			v["tls"] = "tls"
			v["sni"] = p.serverName()
			v["fp"] = p.str("client-fingerprint")
			if alpn := p.strs("alpn"); len(alpn) > 0 {
				v["alpn"] = strings.Join(alpn, ",")
			}
			if p.flag("skip-cert-verify") {
				v["allowInsecure"] = "1"
			}
		}
		b, _ := json.Marshal(v)
		return "vmess://" + base64.StdEncoding.EncodeToString(b), true
	case "vless", "trojan":
		// R10X-010（D-05）：vless 支入口加与 ss 支同形的谓词门——
		// 存量坏 ENC / 坏 Reality 节点在其余六面均整节点略过/报错，
		// URI 面不得独家发链；合法节点逐字不变（谓词只读不改）。
		// R11 批 2（R11X-002）订正：旧注「trojan 支无枚举域」的前提
		// 不实——trojan 支同样下发横切枚举/形态字段（fp、xhttp
		// mode、Reality pbk/sid），forEndpoint 只愈合大小写、不拒
		// 垃圾值；故本门扩至 trojan 支，两支同过 ssFaceOther。
		if !validImportedProxyFields(p, ssFaceOther) {
			return "", false
		}
		if p.network() == "http" {
			return "", false
		}
		q := url.Values{}
		user := url.PathEscape(p.str("password"))
		if p.Type() == "vless" {
			user = p.str("uuid")
			if enc := p.str("encryption"); enc != "" {
				q.Set("encryption", enc)
			} else {
				q.Set("encryption", "none")
			}
			if f := p.str("flow"); f != "" {
				q.Set("flow", f)
			}
		}
		netType := p.network()
		if p.sub("ws-opts").flag("v2ray-http-upgrade") {
			netType = "httpupgrade"
		}
		q.Set("type", netType)
		host, path, service, mode := transportFields(p)
		switch p.network() {
		case "ws", "h2", "xhttp":
			setIf(q, "path", path)
			setIf(q, "host", host)
			setIf(q, "mode", mode)
		case "grpc":
			q.Set("serviceName", service)
			q.Set("mode", "gun")
		}
		switch {
		case len(p.sub("reality-opts")) > 0:
			q.Set("security", "reality")
			ro := p.sub("reality-opts")
			q.Set("pbk", ro.str("public-key"))
			setIf(q, "sid", ro.str("short-id"))
			// L5-F-01：dest/spx 回放——导入时保留的值须能原样回到
			// 链接里，否则仍是单向丢弃（本支入口的
			// validImportedProxyFields 已过 validateRealityOpts，
			// 坏值到不了这里）。
			setIf(q, "realityDest", ro.str("dest"))
			setIf(q, "spx", ro.str("spider-x"))
		case p.usesTLS():
			q.Set("security", "tls")
		default:
			q.Set("security", "none")
		}
		if q.Get("security") != "none" {
			setIf(q, "sni", p.serverName())
			setIf(q, "fp", p.str("client-fingerprint"))
			if alpn := p.strs("alpn"); len(alpn) > 0 {
				q.Set("alpn", strings.Join(alpn, ","))
			}
			if p.flag("skip-cert-verify") {
				q.Set("allowInsecure", "1")
			}
		}
		return p.Type() + "://" + user + "@" + hp + "?" + q.Encode() + frag, true
	case "hysteria2":
		// R11 批 2（R11X-002）：hysteria2 支补统一谓词门
		// （ssFaceOther）——obfs/ports 与横切 fp 存量垃圾此前原样发链。
		if !validImportedProxyFields(p, ssFaceOther) {
			return "", false
		}
		q := url.Values{}
		setIf(q, "sni", p.serverName())
		if p.flag("skip-cert-verify") {
			q.Set("insecure", "1")
		}
		setIf(q, "obfs", p.str("obfs"))
		setIf(q, "obfs-password", p.str("obfs-password"))
		setIf(q, "mport", p.str("ports"))
		setIf(q, "pinSHA256", p.str("fingerprint"))
		if alpn := p.strs("alpn"); len(alpn) > 0 {
			q.Set("alpn", strings.Join(alpn, ","))
		}
		return "hysteria2://" + url.PathEscape(p.str("password")) + "@" + hp + "/?" + q.Encode() + frag, true
	case "tuic":
		// R11 批 2（R11X-002）：tuic 支补统一谓词门（ssFaceOther）——
		// congestion-controller 与横切 fp 存量垃圾此前原样发链。
		if !validImportedProxyFields(p, ssFaceOther) {
			return "", false
		}
		q := url.Values{}
		setIf(q, "sni", p.serverName())
		setIf(q, "congestion_control", p.str("congestion-controller"))
		setIf(q, "udp_relay_mode", p.str("udp-relay-mode"))
		if alpn := p.strs("alpn"); len(alpn) > 0 {
			q.Set("alpn", strings.Join(alpn, ","))
		}
		if p.flag("skip-cert-verify") {
			q.Set("allow_insecure", "1")
		}
		if p.flag("disable-sni") {
			q.Set("disable_sni", "1")
		}
		return "tuic://" + p.str("uuid") + ":" + url.PathEscape(p.str("password")) + "@" + hp + "?" + q.Encode() + frag, true
	case "anytls":
		// R11 批 2（R11X-002）：anytls 支补统一谓词门
		// （ssFaceOther）——横切 fp 存量垃圾此前原样发链。
		if !validImportedProxyFields(p, ssFaceOther) {
			return "", false
		}
		q := url.Values{}
		setIf(q, "sni", p.serverName())
		setIf(q, "fp", p.str("client-fingerprint"))
		if p.flag("skip-cert-verify") {
			q.Set("insecure", "1")
		}
		return "anytls://" + url.PathEscape(p.str("password")) + "@" + hp + "/?" + q.Encode() + frag, true
	case "socks5":
		auth := ""
		if u := p.str("username"); u != "" {
			auth = url.PathEscape(u) + ":" + url.PathEscape(p.str("password")) + "@"
		}
		return "socks5://" + auth + hp + frag, true
	}
	// Snell and HTTP proxies have no common share link.
	return "", false
}

func setIf(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}

// transportFields extracts host / path / gRPC service / XHTTP mode.
func transportFields(p Proxy) (host, path, service, mode string) {
	switch p.network() {
	case "ws":
		wo := p.sub("ws-opts")
		path = wo.str("path")
		host = firstNonEmptyString(wo.sub("headers").str("Host"), wo.sub("headers").str("host"))
	case "grpc":
		service = p.sub("grpc-opts").str("grpc-service-name")
	case "h2":
		ho := p.sub("h2-opts")
		path = ho.str("path")
		if hosts := ho.strs("host"); len(hosts) > 0 {
			host = hosts[0]
		}
	case "http":
		ho := p.sub("http-opts")
		if paths := ho.strs("path"); len(paths) > 0 {
			path = paths[0]
		}
		if hosts := ho.sub("headers").strs("Host"); len(hosts) > 0 {
			host = hosts[0]
		}
	case "xhttp":
		xo := p.sub("xhttp-opts")
		path, host, mode = xo.str("path"), xo.str("host"), xo.str("mode")
	}
	return
}

func ssPluginString(p Proxy) string {
	po := p.sub("plugin-opts")
	// R10X-003 附带格：plugin 名大小写不敏感匹配——存量变体名
	// （Shadow-TLS / OBFS / V2ray-Plugin）此前在 URI 面静默丢
	// plugin（节点降级、直连前置层消失）；mihomo 对变体名容忍、
	// 无整表毒化，故只在此消费点收口，产物恒用规范小写名。
	// R10X-003 本体格：obfs 支 mode 消费前同进口口径小写愈合
	// （合法集校验在进口与 validImportedProxyFields 谓词，本函
	// 数只产出字符串、无从拒绝，垃圾值由调用面谓词先行略过）。
	switch strings.ToLower(strings.TrimSpace(p.str("plugin"))) {
	case "obfs":
		s := "obfs-local;obfs=" + strings.ToLower(po.str("mode"))
		if h := po.str("host"); h != "" {
			s += ";obfs-host=" + h
		}
		return s
	case "v2ray-plugin":
		s := "v2ray-plugin;mode=websocket"
		if h := po.str("host"); h != "" {
			s += ";host=" + h
		}
		if path := po.str("path"); path != "" {
			s += ";path=" + path
		}
		if po.flag("tls") {
			s += ";tls"
		}
		return s
	case "shadow-tls":
		version := po.num("version")
		if version == 0 {
			version = 3
		}
		return fmt.Sprintf("shadow-tls;host=%s;password=%s;version=%d", po.str("host"), po.str("password"), version)
	}
	return ""
}

// ---- Clash / Mihomo ----

func proxyClash(e Endpoint) (yamlNode, bool) {
	p := e.Proxy.forEndpoint(e)
	// R5X-001/002/003 存量纵深：导入期校验集字段（枚举格 + Reality）
	// 的存量坏值整节点略过，与导入期同一谓词（validImportedProxyFields，
	// Reality 即 validateRealityOpts 本体）——BC-R4-01 宣称的 Reality
	// 渲染略过口径自此实际落地。SubscriptionNodeCount 经本函数同口径。
	// R6 批 1：谓词补 SS 格（cipher 合法集 + SS2022 key 逐段谓词，
	// R6X-001/002）——坏 key 存量自此在 Clash 面整节点略过，与
	// sing-box 面同口径；单面集方法（chacha20 等 6 值）在本面（mihomo 面）保留。
	if !validImportedProxyFields(p, ssFaceClash) {
		return nil, false
	}
	if p.Type() == "ss" {
		// GATE-F1：单段已在 forEndpoint 归一；SIP023 多段在 Clash 面
		// 按段归一（mihomo 按段以带填充 Std 解码、二进制实测非规范
		// 段整表失败；逐段归一不改变解码字节）。其余格式多段原样。
		p["password"] = NormalizedSS2022PasswordSegments(p.str("cipher"), p.str("password"))
	}
	if p.Type() == "naive" {
		// Mihomo has no NaïveProxy client; emitting type: naive would
		// poison the whole profile (same rule as built inbounds).
		return nil, false
	}
	if p.Type() == "snell" {
		// Mihomo's snell client (v1.19.32, NewSnell) accepts versions
		// 1-4, normalizes 5 to 4 (v5 servers accept v4 clients) and
		// rejects 6 outright — keep 6 out, render the rest. 此前按
		// v1-v3 的旧口径把 v4/v5 一并丢弃，与自建入站的 Clash 渲染
		// (clashSnell) 同批订正。
		srcVersion := p.num("version")
		switch {
		case srcVersion < 0:
			// R2X-002：负数版本是脏数据，略过而非原样下发。
			return nil, false
		case srcVersion == 5:
			p["version"] = 4
		case srcVersion > 5:
			return nil, false
		}
		// Snell over ShadowTLS is stored panel-side under "shadow-tls";
		// Mihomo expresses it as a snell obfs mode. An existing obfs
		// mode wins: obfs-opts holds a single mode.
		if st := p.sub("shadow-tls"); st.str("password") != "" && p.sub("obfs-opts").str("mode") == "" {
			version := st.num("version")
			if version == 0 {
				version = 3
			}
			// R5 批 1：mode 用 mihomo 规范拼写 shadow-tls——历史拼写
			// shadowtls 经 mihomo v1.19.33 实测被本体拒绝（obfs mode
			// error），自产形态此前即毒化 mihomo 整表。
			p["obfs-opts"] = map[string]interface{}{"mode": "shadow-tls", "host": st.str("host"),
				"password": st.str("password"), "version": version}
		}
		if oo := p.sub("obfs-opts"); oo.str("mode") != "" {
			// 存量别名拼写愈合（与导入期同一规范化）。
			cp := make(Proxy, len(oo))
			for k, v := range oo {
				cp[k] = v
			}
			cp["mode"] = CanonicalSnellObfsMode(oo.str("mode"))
			oo = cp
			p["obfs-opts"] = map[string]interface{}(oo)
		}
		// X-P3-11（L-02-1）：udp 不透传源值（sing-box 来源恒 true、
		// Clash 来源可缺键），按版本/ShadowTLS 语义合成，与自建入站
		// clashSnell 的 Network() 口径一致：ShadowTLS 前置只暴露 TCP、
		// v4 无 UDP（缺省版本按导入口径为 4），其余（v5 等）带 UDP；
		// 恒显式输出布尔。合成须用归一前的源版本（v5 渲染成 4 但有 UDP）。
		effVersion := srcVersion
		if effVersion == 0 {
			effVersion = 4
		}
		shadowTLS := p.sub("shadow-tls").str("password") != "" ||
			p.sub("obfs-opts").str("mode") == "shadow-tls"
		p["udp"] = !shadowTLS && effVersion != 4
	}
	delete(p, "shadow-tls") // panel-only key for Snell over ShadowTLS (converted above)
	node := yamlNode{{"name", e.Name}, {"type", p.Type()}, {"server", p.str("server")}, {"port", e.Port}}
	keys := make([]string, 0, len(p))
	for k := range p {
		if k != "name" && k != "type" && k != "server" && k != "port" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := p[k]
		if k == "encryption" {
			// R11X-004（下发侧归一，FIX-PLAN 批 3 定版）：校验与
			// URI / 链式两面均经 p.str("encryption") 取修剪形，唯独
			// 本面此前透传 map 原值——首尾空白包装的 ENC 串因此
			// 原样下发、mihomo 整表 exit 1。下发时同口径取修剪形；
			// 落库原值不动（p 为 forEndpoint 克隆、且本处只改下发
			// 值），合法无空白串逐字不变。
			v = p.str("encryption")
		}
		node = append(node, yamlField{k, v})
	}
	return node, true
}

// ---- sing-box ----

func proxySingbox(e Endpoint) ([]map[string]interface{}, bool) {
	p := e.Proxy.forEndpoint(e)
	// R5X-001/002/003 存量纵深：与 proxyClash 同一谓词整节点略过
	// （见 proxyClash 注）。sing-box 面既有的字段级守卫（SS2022 key、
	// VLESS ENC、Snell obfs）保留在各分支内，与本谓词叠加不冲突。
	// R6 批 1：面参数为 sing-box 面——单面集方法（chacha20 等 6 值）在本面略过。
	if !validImportedProxyFields(p, ssFaceSingbox) {
		return nil, false
	}
	o := map[string]interface{}{"tag": e.Name, "server": strings.Trim(e.Host, "[]"), "server_port": e.Port}
	tls := func(enabled bool) {
		if !enabled {
			return
		}
		t := map[string]interface{}{"enabled": true}
		if sni := p.serverName(); sni != "" {
			t["server_name"] = sni
		}
		if p.flag("skip-cert-verify") {
			t["insecure"] = true
		}
		if alpn := p.strs("alpn"); len(alpn) > 0 {
			t["alpn"] = alpn
		}
		if fp := p.str("client-fingerprint"); fp != "" {
			t["utls"] = map[string]interface{}{"enabled": true, "fingerprint": fp}
		}
		if ro := p.sub("reality-opts"); len(ro) > 0 {
			t["reality"] = map[string]interface{}{"enabled": true, "public_key": ro.str("public-key"), "short_id": ro.str("short-id")}
			if _, ok := t["utls"]; !ok {
				t["utls"] = map[string]interface{}{"enabled": true, "fingerprint": "chrome"}
			}
		}
		o["tls"] = t
	}
	transport := func() bool {
		host, path, service, _ := transportFields(p)
		switch p.network() {
		case "tcp":
		case "ws":
			t := map[string]interface{}{"type": "ws", "path": path}
			if p.sub("ws-opts").flag("v2ray-http-upgrade") {
				t["type"] = "httpupgrade"
				t["host"] = host
			} else if host != "" {
				t["headers"] = map[string]interface{}{"Host": host}
			}
			o["transport"] = t
		case "grpc":
			o["transport"] = map[string]interface{}{"type": "grpc", "service_name": service}
		case "h2", "http":
			// Clash 的 h2 与 http 均为 HTTP 系传输，映射为 sing-box 的 http 传输。
			t := map[string]interface{}{"type": "http", "path": path}
			if host != "" {
				t["host"] = []string{host}
			}
			o["transport"] = t
		case "quic":
			o["transport"] = map[string]interface{}{"type": "quic"}
		default:
			return false
		}
		return true
	}
	switch p.Type() {
	case "ss":
		// R3X-008 渲染纵深 + R4-SIP023-R 解除（R4-X-verdicts §4）：
		// 逐段以导入期同一谓词 ValidateSS2022Password 校验，任一段
		// 不合规整节点略过——存量坏 key 纵深兜底不变，不让一条脏行
		// 毒化整份 sing-box 订阅；存量不迁移、不自动清理，靠用户
		// 编辑/重导入时被导入期校验拦下收敛。通过后逐段归一为带
		// 填充 Std 渲染（NormalizedSS2022PasswordSegments，与 Clash
		// 面同 helper 语义）：sing-box 出站侧按段以带填充 Std 解码
		// （1.14.3 / 1.12.x / 1.11.x 官方二进制 check 实测，D §1.2 与
		// 批 2 补测），原样直出含 Raw 段会整份 check 失败，故严禁
		// 原样直出、严禁放宽 ValidSS2022SingleKey 一刀切实现。
		// 链式 sing-box 出站经 singboxOutbounds 同函数同步解除、
		// SubscriptionNodeCount 计数同口径；自建入站服务端面不动。
		if err := ValidateSS2022Password(p.str("cipher"), p.str("password")); err != nil {
			return nil, false
		}
		o["type"] = "shadowsocks"
		o["method"] = p.str("cipher")
		o["password"] = NormalizedSS2022PasswordSegments(p.str("cipher"), p.str("password"))
		po := p.sub("plugin-opts")
		switch p.str("plugin") {
		case "":
		case "obfs":
			o["plugin"] = "obfs-local"
			o["plugin_opts"] = strings.TrimPrefix(ssPluginString(p), "obfs-local;")
		case "v2ray-plugin":
			o["plugin"] = "v2ray-plugin"
			o["plugin_opts"] = strings.TrimPrefix(ssPluginString(p), "v2ray-plugin;")
		case "shadow-tls":
			tag := e.Name + " (shadowtls)"
			o["detour"] = tag
			version := po.num("version")
			if version == 0 {
				version = 3
			}
			return []map[string]interface{}{o, {
				"type": "shadowtls", "tag": tag, "server": strings.Trim(e.Host, "[]"), "server_port": e.Port,
				"version": version, "password": po.str("password"),
				"tls": map[string]interface{}{"enabled": true, "server_name": po.str("host"),
					"utls": map[string]interface{}{"enabled": true, "fingerprint": firstNonEmptyString(p.str("client-fingerprint"), "chrome")}},
			}}, true
		default:
			return nil, false
		}
	case "vmess":
		o["type"] = "vmess"
		o["uuid"] = p.str("uuid")
		o["alter_id"] = p.num("alterId")
		o["security"] = firstNonEmptyString(p.str("cipher"), "auto")
		if !transport() {
			return nil, false
		}
		tls(p.usesTLS())
	case "vless":
		if enc := p.str("encryption"); enc != "" && enc != "none" {
			// sing-box 不支持 VLESS 加密，跳过而非渲染坏节点。
			return nil, false
		}
		o["type"] = "vless"
		o["uuid"] = p.str("uuid")
		if f := p.str("flow"); f != "" {
			o["flow"] = f
		}
		if !transport() {
			return nil, false
		}
		tls(p.usesTLS())
	case "trojan":
		o["type"] = "trojan"
		o["password"] = p.str("password")
		if !transport() {
			return nil, false
		}
		tls(true)
	case "hysteria2":
		o["type"] = "hysteria2"
		o["password"] = p.str("password")
		if ob := p.str("obfs"); ob != "" {
			o["obfs"] = map[string]interface{}{"type": ob, "password": p.str("obfs-password")}
		}
		if ports := p.str("ports"); ports != "" {
			delete(o, "server_port")
			// R5 批 1：按逗号项拆分逐项归一——sing-box 的 server_ports
			// 条目必须是 "lo:hi" 段式，整串直塞（旧形态）在单端口
			// （"443"）与多项清单下均 FATAL（bad port range）。
			items := strings.Split(ports, ",")
			ranges := make([]string, 0, len(items))
			for _, item := range items {
				item = strings.TrimSpace(item)
				if !strings.Contains(item, "-") {
					item += "-" + item
				}
				ranges = append(ranges, strings.ReplaceAll(item, "-", ":"))
			}
			o["server_ports"] = ranges
		}
		tls(true)
	case "tuic":
		o["type"] = "tuic"
		o["uuid"] = p.str("uuid")
		o["password"] = p.str("password")
		if cc := p.str("congestion-controller"); cc != "" {
			o["congestion_control"] = cc
		}
		if m := p.str("udp-relay-mode"); m != "" {
			o["udp_relay_mode"] = m
		}
		tls(true)
	case "anytls":
		o["type"] = "anytls"
		o["password"] = p.str("password")
		tls(true)
	case "naive":
		o["type"] = "naive"
		o["username"] = p.str("username")
		o["password"] = p.str("password")
		tls(true)
	case "snell":
		if oo := p.sub("obfs-opts"); oo.str("mode") != "" {
			// sing-box's snell client has no obfs modes; skip rather
			// than emit a node stripped of its obfuscation layer.
			return nil, false
		}
		o["type"] = "snell"
		// R2X-002（verdicts §2.4）：sing-box 的 Snell 接受域经官方二进
		// 制实测只有 {4, 6}——改白名单：4/5→4（v4 线格式可与 v5 服务
		// 端互通）、6→6，其余（含 1/2/3、越界与缺失）整节点略过，绝不
		// 直出毒化整份配置。分客户端口径（N-08）：Clash/Surge 对
		// v1–v3 的处理各自独立，不追求三格式一致。
		switch v := p.num("version"); v {
		case 4, 5:
			o["version"] = 4
		case 6:
			o["version"] = 6
			// R2X-023：v6 的 mode 往返补出（导入侧仅保留 unshaped）。
			if p.str("mode") == SnellModeUnshaped {
				o["mode"] = SnellModeUnshaped
			}
		default:
			return nil, false
		}
		o["psk"] = p.str("psk")
		if st := p.sub("shadow-tls"); st.str("password") != "" {
			tag := e.Name + " (shadowtls)"
			delete(o, "server")
			delete(o, "server_port")
			o["detour"] = tag
			version := st.num("version")
			if version == 0 {
				version = 3
			}
			return []map[string]interface{}{o, {
				"type": "shadowtls", "tag": tag, "server": strings.Trim(e.Host, "[]"), "server_port": e.Port,
				"version": version, "password": st.str("password"),
				"tls": map[string]interface{}{"enabled": true, "server_name": st.str("host"),
					"utls": map[string]interface{}{"enabled": true, "fingerprint": firstNonEmptyString(p.str("client-fingerprint"), "chrome")}},
			}}, true
		}
	case "socks5":
		o["type"] = "socks"
		o["version"] = "5"
		if u := p.str("username"); u != "" {
			o["username"], o["password"] = u, p.str("password")
		}
	case "http":
		o["type"] = "http"
		if u := p.str("username"); u != "" {
			o["username"], o["password"] = u, p.str("password")
		}
		tls(p.flag("tls"))
	default:
		return nil, false
	}
	if t, ok := o["tls"].(map[string]interface{}); ok && (p.Type() == "hysteria2" || p.Type() == "tuic" || p.Type() == "naive") {
		delete(t, "utls") // sing-box rejects uTLS on QUIC outbounds; naive uses Chromium's own TLS stack
	}
	return []map[string]interface{}{o}, true
}

// ---- Surge ----

// surgeEscape 转义 Surge 行式格式中的值（R5-B5 订阅转义，F-081 修正）。
// 真正劈字段的是逗号（字段分隔符）与换行；键值按首个 = 切分，值内的 =
// 合法且必须保留——SS 2022 密钥是 base64、自带 == 填充，此前把 = 一并
// 替换成空格会把导入节点的 2022 密码改坏（金快照 p2 系列为据）。
func surgeEscape(s string) string {
	return strings.NewReplacer(
		",", " ",
		"\n", " ",
		"\r", " ",
	).Replace(s)
}

func proxySurge(e Endpoint) (string, bool) {
	p := e.Proxy.forEndpoint(e)
	// R11 批 2（R11X-003）：Surge 面入口补统一谓词门——此前仅 ss 支
	// 有 cipher/key 门，SS plugin-opts.mode、Snell obfs-opts.mode、
	// VMess cipher、Reality 等存量垃圾原样下发。面参数取
	// ssFaceClash：Surge 的 SS 分发与 mihomo 面同口径保留单面集
	// （chacha20 等 6 值经 ValidSSCipher 在本面 EMIT，见 R8 批 2
	// 对账），以 ssFaceOther/ssFaceSingbox 设门会误杀单面集合法格；
	// 谓词面参数只判别 SS 单面集一格，其余协议格与面参数无关。
	// 既有手工门（Hy2 obfs 非 salamander 略过、snell 版本域等）
	// 保留在各支内、与本门叠加。
	if !validImportedProxyFields(p, ssFaceClash) {
		return "", false
	}
	name := strings.NewReplacer(",", " ", "=", " ", "\n", " ").Replace(e.Name)
	// host 与 SurgeLine 同口径过 surgeEscape（F-A-01/C-01 纵深防御）。
	host := surgeEscape(strings.Trim(e.Host, "[]"))
	port := strconv.Itoa(e.Port)
	var parts []string
	tlsTail := func() {
		if sni := p.serverName(); sni != "" {
			parts = append(parts, "sni="+surgeEscape(sni))
		}
		if p.flag("skip-cert-verify") {
			parts = append(parts, "skip-cert-verify=true")
		}
	}
	ws := func() bool {
		switch p.network() {
		case "tcp":
			return true
		case "ws":
			if p.sub("ws-opts").flag("v2ray-http-upgrade") {
				return false
			}
			h, path, _, _ := transportFields(p)
			parts = append(parts, "ws=true", "ws-path="+surgeEscape(firstNonEmptyString(path, "/")))
			if h != "" {
				parts = append(parts, "ws-headers=Host:"+surgeEscape(h))
			}
			return true
		}
		return false
	}
	shadowTLS := func(po Proxy) {
		if po.str("password") == "" {
			return
		}
		version := po.num("version")
		if version == 0 {
			version = 3
		}
		parts = append(parts, "shadow-tls-password="+surgeEscape(po.str("password")), "shadow-tls-sni="+surgeEscape(po.str("host")),
			fmt.Sprintf("shadow-tls-version=%d", version))
	}
	switch p.Type() {
	case "ss":
		// R6 批 1（未验证面同批设门，恒标未验证）：Surge 面以同一谓词
		// 设门——双拒 cipher 与 SS2022 坏 key 整节点略过。
		if !ValidSSCipher(p.str("cipher")) || ValidateSS2022Password(p.str("cipher"), p.str("password")) != nil {
			return "", false
		}
		parts = append(parts, "ss", host, port, "encrypt-method="+p.str("cipher"), "password="+surgeEscape(p.str("password")))
		po := p.sub("plugin-opts")
		switch p.str("plugin") {
		case "":
			parts = append(parts, "udp-relay=true")
		case "obfs":
			parts = append(parts, "obfs="+po.str("mode"))
			if h := po.str("host"); h != "" {
				parts = append(parts, "obfs-host="+h)
			}
		case "shadow-tls":
			shadowTLS(po)
		default:
			return "", false
		}
	case "vmess":
		parts = append(parts, "vmess", host, port, "username="+p.str("uuid"))
		if p.num("alterId") == 0 {
			parts = append(parts, "vmess-aead=true")
		}
		if !ws() {
			return "", false
		}
		if p.usesTLS() {
			parts = append(parts, "tls=true")
			tlsTail()
		}
	case "trojan":
		parts = append(parts, "trojan", host, port, "password="+surgeEscape(p.str("password")))
		if !ws() {
			return "", false
		}
		tlsTail()
	case "snell":
		// R2X-002：Surge 原生支持 1–6；>6（及负数脏数据）整节点略过。
		if v := p.num("version"); v < 0 || v > 6 {
			return "", false
		}
		parts = append(parts, "snell", host, port, "psk="+surgeEscape(p.str("psk")), fmt.Sprintf("version=%d", maxInt(p.num("version"), 1)))
		// R2X-023：v6 的 mode 往返补出（导入侧仅保留 unshaped）。
		if p.num("version") == 6 && p.str("mode") == SnellModeUnshaped {
			parts = append(parts, "mode="+SnellModeUnshaped)
		}
		if oo := p.sub("obfs-opts"); oo.str("mode") != "" {
			parts = append(parts, "obfs="+oo.str("mode"))
			if h := oo.str("host"); h != "" {
				parts = append(parts, "obfs-host="+h)
			}
		}
		shadowTLS(p.sub("shadow-tls"))
	case "hysteria2":
		if p.str("obfs") != "" && p.str("obfs") != "salamander" {
			return "", false
		}
		parts = append(parts, "hysteria2", host, port, "password="+surgeEscape(p.str("password")))
		if pw := p.str("obfs-password"); pw != "" {
			parts = append(parts, "salamander-password="+pw)
		}
		if ports := p.str("ports"); ports != "" {
			parts = append(parts, fmt.Sprintf("port-hopping=%q", ports), "port-hopping-interval=30")
		}
		tlsTail()
	case "tuic":
		parts = append(parts, "tuic-v5", host, port, "password="+surgeEscape(p.str("password")), "uuid="+surgeEscape(p.str("uuid")),
			"alpn="+firstNonEmptyString(strings.Join(p.strs("alpn"), ","), "h3"))
		tlsTail()
	case "anytls":
		parts = append(parts, "anytls", host, port, "password="+surgeEscape(p.str("password")))
		tlsTail()
	case "socks5", "http":
		kind := p.Type()
		if p.flag("tls") {
			kind = map[string]string{"socks5": "socks5-tls", "http": "https"}[kind]
		}
		parts = append(parts, kind, host, port)
		if u := p.str("username"); u != "" {
			parts = append(parts, "username="+u, "password="+surgeEscape(p.str("password")))
		}
		if p.flag("tls") {
			tlsTail()
		}
	default:
		return "", false
	}
	return name + " = " + strings.Join(parts, ", "), true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
