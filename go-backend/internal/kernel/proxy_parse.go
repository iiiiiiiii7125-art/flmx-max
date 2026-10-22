package kernel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseImport parses nodes from any supported input: share links (one per
// line), a base64 subscription, a Clash / Mihomo profile, a sing-box
// profile, or Surge / Loon proxy lines. Bad entries are reported without
// stopping the rest.
func ParseImport(text string) ([]ImportedNode, []string) {
	text = strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	if text == "" {
		return nil, []string{"内容为空"}
	}
	if decoded, ok := decodeSubscriptionBase64(text); ok {
		text = decoded
	}
	switch {
	case strings.HasPrefix(text, "{"):
		return parseSingboxProfile(text)
	case looksLikeClash(text):
		return parseClashProfile(text)
	}
	return parseLines(text)
}

func decodeSubscriptionBase64(text string) (string, bool) {
	if strings.Contains(text, "://") || strings.ContainsAny(text, " :{") {
		return "", false
	}
	compact := strings.Join(strings.Fields(text), "")
	b, err := decodeBase64Loose(compact)
	if err != nil || !strings.Contains(string(b), "://") {
		return "", false
	}
	return string(b), true
}

func looksLikeClash(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "proxies:") {
			return true
		}
	}
	return false
}

func parseLines(text string) ([]ImportedNode, []string) {
	var nodes []ImportedNode
	var errs []string
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") ||
			(strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")) {
			continue
		}
		var p Proxy
		var err error
		if strings.Contains(line, "://") {
			p, err = parseURI(line)
		} else if strings.Contains(line, "=") {
			p, err = parseSurgeLine(line)
		} else {
			err = errors.New("无法识别的格式")
		}
		if err == nil {
			var node ImportedNode
			node, err = finishProxy(p)
			if err == nil {
				nodes = append(nodes, node)
				continue
			}
		}
		errs = append(errs, fmt.Sprintf("第 %d 行：%v", i+1, err))
	}
	return nodes, errs
}

// finishProxy validates a parsed proxy and fills the node fields.
func finishProxy(p Proxy) (ImportedNode, error) {
	t := p.Type()
	if _, ok := importTypes[t]; !ok {
		if t == "" {
			return ImportedNode{}, errors.New("缺少节点类型")
		}
		return ImportedNode{}, fmt.Errorf("暂不支持 %s 类型的节点", t)
	}
	host := strings.Trim(p.str("server"), "[]")
	if err := ValidateImportHost(host); err != nil {
		return ImportedNode{}, err
	}
	// D4-08：数值不确定即拒绝——小数端口被 num() 截断、溢出值被包成
	// 垃圾 int 都是「误解析成功」，必须在落库前拦下。
	port := p.num("port")
	if n, present, exact := p.exactInt("port"); present {
		if !exact {
			return ImportedNode{}, errors.New("端口不正确")
		}
		port = n
	}
	if port < 1 || port > 65535 {
		return ImportedNode{}, errors.New("端口不正确")
	}
	if t == "vmess" {
		if n, present, exact := p.exactInt("alterId"); present && (!exact || n < 0 || n > 65535) {
			return ImportedNode{}, errors.New("alterId 不正确")
		}
	}
	if t == "snell" {
		// A5-07：与行格式同口径，缺省/为 0 时补默认版本 4，否则再导出
		// 时整键缺失、客户端缺省语义不明（clashSnell 对自建入站亦有
		// version==0 兜底）。
		// R2X-002：域外版本（<0 或 >6）拒绝导入作防御纵深——保留
		// 0→4 缺省与 1–6 落库（v1–v3 在 Clash/Surge 合法可用，不得在
		// 导入期拒；sing-box 渲染侧另按白名单略过，见 proxySingbox）。
		if v := p.num("version"); v < 0 || v > 6 {
			return ImportedNode{}, errors.New("Snell 版本不正确（只支持 1–6）")
		}
		if p.num("version") == 0 {
			p["version"] = 4
		}
	}
	if err := validateProxyCredentials(p); err != nil {
		return ImportedNode{}, err
	}
	name := p.str("name")
	if name == "" {
		name = net.JoinHostPort(host, strconv.Itoa(port))
	}
	p["server"] = host
	p["port"] = port
	delete(p, "name")
	return ImportedNode{Name: name, Host: host, Port: port, Proxy: p}, nil
}

func validateProxyCredentials(p Proxy) error {
	switch p.Type() {
	case "ss":
		if p.str("cipher") == "" || p.str("password") == "" {
			return errors.New("缺少加密方式或密码")
		}
		// R3X-008：SS2022 密钥格式在导入期一处收口（ss 链接 / Clash /
		// sing-box JSON / Surge 四格式与订阅同步、换链接编辑全部经
		// finishProxy 到此）。坏 key 会毒化整份 sing-box 订阅，必须
		// 逐条报错（调用方按条收集、不连坐其余节点）；legacy AEAD
		// 与未知 cipher 不查，口径见 ValidateSS2022Password。
		if err := ValidateSS2022Password(p.str("cipher"), p.str("password")); err != nil {
			return err
		}
	case "vmess", "vless":
		if p.str("uuid") == "" {
			return errors.New("缺少 UUID")
		}
		// BUG-A4-2: 与自建入站同口径校验 UUID 格式（model.go 的
		// uuidPattern），此前只查非空，vless://not-a-uuid 会静默入库、
		// 以非法 uuid 渲染进订阅毒化整份配置。
		if !uuidPattern.MatchString(strings.ToLower(p.str("uuid"))) {
			return errors.New("UUID 格式错误")
		}
	case "trojan", "hysteria2", "anytls":
		if p.str("password") == "" {
			return errors.New("缺少密码")
		}
	case "tuic":
		if p.str("uuid") == "" || p.str("password") == "" {
			return errors.New("缺少 UUID 或密码")
		}
		if !uuidPattern.MatchString(strings.ToLower(p.str("uuid"))) {
			return errors.New("UUID 格式错误")
		}
	case "snell":
		if p.str("psk") == "" {
			return errors.New("缺少 PSK")
		}
	case "naive":
		if p.str("username") == "" || p.str("password") == "" {
			return errors.New("缺少用户名或密码")
		}
	}
	return nil
}

// ---- share links ----

func parseURI(line string) (Proxy, error) {
	scheme, rest, _ := strings.Cut(line, "://")
	switch strings.ToLower(scheme) {
	case "ss":
		return parseSSURI(rest)
	case "vmess":
		return parseVMessURI(rest)
	case "vless":
		return parseUserURI("vless", rest)
	case "trojan":
		return parseUserURI("trojan", rest)
	case "hysteria2", "hy2":
		return parseUserURI("hysteria2", rest)
	case "tuic":
		return parseUserURI("tuic", rest)
	case "anytls":
		return parseUserURI("anytls", rest)
	case "socks", "socks5":
		return parseSocksURI(rest)
	case "http", "https":
		return nil, errors.New("http(s):// 是订阅地址，请单独粘贴在输入框里")
	}
	return nil, fmt.Errorf("暂不支持 %s:// 链接", scheme)
}

// splitURI splits "userinfo@host:port/path?query#name".
func splitURI(rest string) (user, host string, port int, query url.Values, name string, err error) {
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		name = rest[i+1:]
		if n, e := url.PathUnescape(name); e == nil {
			name = n
		}
		rest = rest[:i]
	}
	query = url.Values{}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		query, _ = url.ParseQuery(rest[i+1:])
		rest = rest[:i]
	}
	rest = strings.TrimSuffix(rest, "/")
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		user, rest = rest[:at], rest[at+1:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	host, port, err = ParseImportHostPort(rest)
	return user, host, port, query, strings.TrimSpace(name), err
}

func parseSSURI(rest string) (Proxy, error) {
	user, host, port, q, name, err := splitURI(rest)
	if err != nil && user == "" {
		// Legacy: ss://base64(method:password@host:port)#name
		body := rest
		if i := strings.IndexAny(body, "#?"); i >= 0 {
			body = body[:i]
		}
		decoded, derr := decodeBase64Loose(strings.TrimSuffix(body, "/"))
		if derr != nil {
			return nil, errors.New("ss 链接格式不正确")
		}
		at := strings.LastIndexByte(string(decoded), '@')
		if at < 0 {
			return nil, errors.New("ss 链接格式不正确")
		}
		user = string(decoded[:at])
		if host, port, err = ParseImportHostPort(string(decoded[at+1:])); err != nil {
			return nil, err
		}
		if i := strings.IndexByte(rest, '#'); i >= 0 {
			name, _ = url.PathUnescape(rest[i+1:])
		}
	} else if err != nil {
		return nil, err
	} else if decoded, derr := decodeBase64Loose(user); derr == nil && strings.Contains(string(decoded), ":") {
		user = string(decoded)
	} else if plain, perr := url.PathUnescape(user); perr == nil {
		user = plain
	}
	method, password, ok := strings.Cut(user, ":")
	if !ok {
		return nil, errors.New("ss 链接缺少加密方式或密码")
	}
	p := Proxy{"type": "ss", "name": name, "server": host, "port": port,
		"cipher": strings.ToLower(method), "password": password, "udp": true}
	if plugin := q.Get("plugin"); plugin != "" {
		if err := applySSPlugin(p, plugin); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// applySSPlugin converts a SIP003 plugin string ("obfs-local;obfs=http;...")
// to Mihomo plugin / plugin-opts.
func applySSPlugin(p Proxy, spec string) error {
	parts := strings.Split(spec, ";")
	opts := map[string]string{}
	for _, kv := range parts[1:] {
		k, v, _ := strings.Cut(kv, "=")
		opts[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	switch strings.TrimSpace(parts[0]) {
	case "obfs-local", "simple-obfs", "obfs":
		po := Proxy{}
		po.set("mode", opts["obfs"])
		po.set("host", opts["obfs-host"])
		p["plugin"] = "obfs"
		p["plugin-opts"] = map[string]interface{}(po)
	case "v2ray-plugin":
		po := Proxy{"mode": "websocket"}
		po.set("host", opts["host"])
		po.set("path", opts["path"])
		if _, ok := opts["tls"]; ok {
			po["tls"] = true
		}
		p["plugin"] = "v2ray-plugin"
		p["plugin-opts"] = map[string]interface{}(po)
	case "shadow-tls":
		po := Proxy{"version": 3}
		po.set("host", opts["host"])
		po.set("password", opts["password"])
		if v, err := strconv.Atoi(opts["version"]); err == nil && v > 0 {
			po["version"] = v
		}
		p["plugin"] = "shadow-tls"
		p["plugin-opts"] = map[string]interface{}(po)
		p["client-fingerprint"] = "chrome"
	default:
		return fmt.Errorf("暂不支持 ss 插件 %s", parts[0])
	}
	return nil
}

func parseVMessURI(rest string) (Proxy, error) {
	body := rest
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	raw, err := decodeBase64Loose(body)
	if err != nil {
		return nil, errors.New("vmess 链接格式不正确（只支持 v2rayN 格式）")
	}
	var v map[string]interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errors.New("vmess 链接格式不正确（只支持 v2rayN 格式）")
	}
	j := Proxy(v)
	// D4-08：port/aid 在原始 JSON 里就必须是不溢出的整数，num() 的
	// 截断/回绕结果不能带进 finishProxy。
	for _, k := range []string{"port", "aid"} {
		if _, present, exact := j.exactInt(k); present && !exact {
			return nil, errors.New("vmess 链接中的 " + k + " 不正确")
		}
	}
	p := Proxy{"type": "vmess", "name": j.str("ps"), "server": j.str("add"), "port": j.num("port"),
		"uuid": j.str("id"), "alterId": j.num("aid"), "cipher": "auto", "udp": true}
	if c := j.str("scy"); c != "" {
		p["cipher"] = c
	}
	tls := strings.ToLower(j.str("tls"))
	if tls == "tls" || tls == "1" || tls == "true" {
		p["tls"] = true
		p.set("servername", j.str("sni"))
		p.set("client-fingerprint", j.str("fp"))
		p.set("alpn", j.strs("alpn"))
		if Proxy(v).flag("allowInsecure") || j.flag("skip-cert-verify") {
			p["skip-cert-verify"] = true
		}
	}
	applyTransport(p, strings.ToLower(j.str("net")), j.str("type"), j.str("host"), j.str("path"), j.str("path"), "")
	return p, nil
}

// parseUserURI handles vless / trojan / hysteria2 / tuic / anytls links,
// which share the user@host:port?query#name shape.
func parseUserURI(typ, rest string) (Proxy, error) {
	user, host, port, q, name, err := splitURI(rest)
	if err != nil {
		return nil, err
	}
	user, _ = url.PathUnescape(user)
	p := Proxy{"type": typ, "name": name, "server": host, "port": port, "udp": true}
	insecure := q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" || q.Get("allow_insecure") == "1" ||
		strings.EqualFold(q.Get("allowInsecure"), "true") || strings.EqualFold(q.Get("insecure"), "true")
	sni := firstNonEmptyString(q.Get("sni"), q.Get("peer"), q.Get("servername"))
	alpn := strings.Split(q.Get("alpn"), ",")
	if q.Get("alpn") == "" {
		alpn = nil
	}

	switch typ {
	case "vless", "trojan":
		if typ == "vless" {
			p["uuid"] = user
			p.set("flow", q.Get("flow"))
			p.set("encryption", q.Get("encryption"))
		} else {
			p["password"] = user
		}
		sec := strings.ToLower(q.Get("security"))
		if typ == "trojan" && sec == "" {
			sec = "tls"
		}
		switch sec {
		case "tls", "xtls", "reality":
			if typ == "vless" {
				p["tls"] = true
				p.set("servername", sni)
			} else {
				p.set("sni", sni)
			}
			p.set("client-fingerprint", q.Get("fp"))
			p.set("alpn", alpn)
			p.set("skip-cert-verify", insecure)
			if sec == "reality" {
				ro := Proxy{}
				ro.set("public-key", q.Get("pbk"))
				ro.set("short-id", q.Get("sid"))
				if ro.str("public-key") == "" {
					return nil, errors.New("Reality 链接缺少公钥 pbk")
				}
				p["reality-opts"] = map[string]interface{}(ro)
				if p.str("client-fingerprint") == "" {
					p["client-fingerprint"] = "chrome"
				}
			}
		}
		netType := strings.ToLower(firstNonEmptyString(q.Get("type"), "tcp"))
		applyTransport(p, netType, q.Get("headerType"), q.Get("host"), q.Get("path"), q.Get("serviceName"), q.Get("mode"))
	case "hysteria2":
		p["password"] = user
		p.set("sni", sni)
		p.set("skip-cert-verify", insecure)
		p.set("alpn", alpn)
		p.set("obfs", q.Get("obfs"))
		p.set("obfs-password", q.Get("obfs-password"))
		p.set("ports", firstNonEmptyString(q.Get("mport"), q.Get("ports")))
		p.set("fingerprint", q.Get("pinSHA256"))
	case "tuic":
		uuid, password, _ := strings.Cut(user, ":")
		p["uuid"] = uuid
		p["password"] = password
		p.set("sni", sni)
		p.set("skip-cert-verify", insecure)
		if alpn == nil {
			alpn = []string{"h3"}
		}
		p["alpn"] = alpn
		p.set("congestion-controller", q.Get("congestion_control"))
		p.set("udp-relay-mode", q.Get("udp_relay_mode"))
		if q.Get("disable_sni") == "1" {
			p["disable-sni"] = true
		}
	case "anytls":
		p["password"] = user
		p.set("sni", sni)
		p.set("skip-cert-verify", insecure)
		p.set("alpn", alpn)
		p.set("client-fingerprint", q.Get("fp"))
	}
	return p, nil
}

// applyTransport sets Mihomo network options from share-link fields.
func applyTransport(p Proxy, netType, headerType, host, path, serviceName, mode string) {
	switch netType {
	case "", "tcp", "raw":
		if headerType == "http" {
			p["network"] = "http"
			ho := Proxy{}
			if path != "" {
				ho["path"] = []string{path}
			}
			if host != "" {
				ho["headers"] = map[string]interface{}{"Host": []string{host}}
			}
			p.set("http-opts", ho)
		}
	case "ws", "httpupgrade":
		p["network"] = "ws"
		wo := Proxy{}
		wo.set("path", path)
		if host != "" {
			wo["headers"] = map[string]interface{}{"Host": host}
		}
		if netType == "httpupgrade" {
			wo["v2ray-http-upgrade"] = true
		}
		p.set("ws-opts", wo)
	case "grpc":
		p["network"] = "grpc"
		p["grpc-opts"] = map[string]interface{}{"grpc-service-name": serviceName}
	case "h2", "http":
		p["network"] = "h2"
		ho := Proxy{}
		ho.set("path", path)
		if host != "" {
			ho["host"] = []string{host}
		}
		p.set("h2-opts", ho)
	case "xhttp", "splithttp":
		p["network"] = "xhttp"
		xo := Proxy{}
		xo.set("path", path)
		xo.set("host", host)
		xo.set("mode", mode)
		p.set("xhttp-opts", xo)
	default:
		p["network"] = netType
	}
}

func parseSocksURI(rest string) (Proxy, error) {
	user, host, port, _, name, err := splitURI(rest)
	if err != nil {
		// v2rayN: socks://base64(user:pass@host:port)#name
		body := rest
		if i := strings.IndexByte(body, '#'); i >= 0 {
			name, _ = url.PathUnescape(body[i+1:])
			body = body[:i]
		}
		decoded, derr := decodeBase64Loose(body)
		if derr != nil {
			return nil, err
		}
		return parseSocksURI(string(decoded) + "#" + url.PathEscape(name))
	}
	p := Proxy{"type": "socks5", "name": name, "server": host, "port": port, "udp": true}
	if user != "" {
		if decoded, derr := decodeBase64Loose(user); derr == nil && strings.Contains(string(decoded), ":") {
			user = string(decoded)
		}
		u, pw, _ := strings.Cut(user, ":")
		u, _ = url.PathUnescape(u)
		pw, _ = url.PathUnescape(pw)
		p.set("username", u)
		p.set("password", pw)
	}
	return p, nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---- Clash / Mihomo ----

func parseClashProfile(text string) ([]ImportedNode, []string) {
	// IMP-010：yaml.v3 默认支持锚点/别名（anchor/alias），理论上有别名放大
	// （billion laughs）风险。已实测评估：9 级倍增展开仅约 0.3ms，且本函数只经
	// 管理员接口调用、请求体有 10MB 上限、订阅体有 5MB 上限，风险可接受，
	// 因此不引入 KnownFields/别名禁用等额外限制（会误伤合法 Clash 配置）。
	//
	// D4-07：先解到 yaml.Node 再逐条 Decode——整份强类型 Unmarshal 会被
	// 单个条目内的重复键 / 标量条目短路成整单 0 节点，与 sing-box、行格式
	// 的条目级容错口径不一致。文档级语法错误仍整单拒绝，条目级错误只丢
	// 该条并计入回执。
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return nil, []string{"Clash 配置解析失败：" + err.Error()}
	}
	var seq *yaml.Node
	if len(root.Content) > 0 {
		doc := root.Content[0]
		for i := 0; i+1 < len(doc.Content); i += 2 {
			if doc.Content[i].Value == "proxies" {
				seq = doc.Content[i+1]
				break
			}
		}
	}
	if seq != nil && seq.Kind == yaml.AliasNode && seq.Alias != nil {
		seq = seq.Alias
	}
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil, []string{"Clash 配置里没有 proxies"}
	}
	var nodes []ImportedNode
	var errs []string
	for i, item := range seq.Content {
		var raw map[string]interface{}
		if err := item.Decode(&raw); err != nil {
			errs = append(errs, fmt.Sprintf("第 %d 个节点：%v", i+1, err))
			continue
		}
		node, err := finishProxy(normalizeYAML(raw).(map[string]interface{}))
		if err != nil {
			name := Proxy(raw).str("name")
			if name == "" {
				name = fmt.Sprintf("第 %d 个节点", i+1)
			}
			errs = append(errs, fmt.Sprintf("%s：%v", name, err))
			continue
		}
		nodes = append(nodes, node)
	}
	if len(seq.Content) == 0 {
		errs = append(errs, "Clash 配置里没有 proxies")
	}
	return nodes, errs
}

// normalizeYAML turns yaml.v3 values into JSON-compatible ones.
func normalizeYAML(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, x := range t {
			out[k] = normalizeYAML(x)
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, x := range t {
			out[fmt.Sprint(k)] = normalizeYAML(x)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, x := range t {
			out[i] = normalizeYAML(x)
		}
		return out
	}
	return v
}

// ---- sing-box ----

func parseSingboxProfile(text string) ([]ImportedNode, []string) {
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, []string{"sing-box 配置解析失败：" + err.Error()}
	}
	byTag := map[string]Proxy{}
	for _, o := range doc.Outbounds {
		byTag[Proxy(o).str("tag")] = Proxy(o)
	}
	var nodes []ImportedNode
	var errs []string
	for _, raw := range doc.Outbounds {
		o := Proxy(raw)
		switch o.str("type") {
		case "selector", "urltest", "direct", "block", "dns", "shadowtls":
			continue
		}
		p, err := singboxToProxy(o, byTag)
		if err == nil {
			var node ImportedNode
			if node, err = finishProxy(p); err == nil {
				nodes = append(nodes, node)
				continue
			}
		}
		errs = append(errs, fmt.Sprintf("%s：%v", o.str("tag"), err))
	}
	if len(doc.Outbounds) == 0 {
		errs = append(errs, "sing-box 配置里没有 outbounds")
	}
	return nodes, errs
}

func singboxToProxy(o Proxy, byTag map[string]Proxy) (Proxy, error) {
	types := map[string]string{"shadowsocks": "ss", "vmess": "vmess", "vless": "vless", "trojan": "trojan",
		"hysteria2": "hysteria2", "tuic": "tuic", "anytls": "anytls", "snell": "snell", "naive": "naive",
		"socks": "socks5", "http": "http"}
	typ, ok := types[o.str("type")]
	if !ok {
		return nil, fmt.Errorf("暂不支持 %s 类型的节点", o.str("type"))
	}
	// D4-08：数值/嵌套块不确定即拒绝该条目——小数端口截断、alter_id
	// 溢出、tls/transport/obfs 块类型错位都不许静默降级成「解析成功」。
	if _, present, exact := o.exactInt("server_port"); present && !exact {
		return nil, errors.New("server_port 不是整数")
	}
	for _, k := range []string{"tls", "transport", "obfs"} {
		if v, present := o[k]; present && v != nil {
			if _, isObj := v.(map[string]interface{}); !isObj {
				return nil, fmt.Errorf("%s 字段格式不正确", k)
			}
		}
	}
	p := Proxy{"type": typ, "name": o.str("tag"), "server": o.str("server"), "port": o.num("server_port"), "udp": true}
	switch typ {
	case "ss":
		p["cipher"] = o.str("method")
		p["password"] = o.str("password")
		if plugin := o.str("plugin"); plugin != "" {
			if err := applySSPlugin(p, plugin+";"+o.str("plugin_opts")); err != nil {
				return nil, err
			}
		}
		if detour := byTag[o.str("detour")]; detour.str("type") == "shadowtls" {
			if _, present, exact := detour.exactInt("server_port"); present && !exact {
				return nil, errors.New("server_port 不是整数")
			}
			// ss over ShadowTLS: the shadowtls outbound holds the address.
			p["server"], p["port"] = detour.str("server"), detour.num("server_port")
			po := Proxy{"version": detour.num("version"), "password": detour.str("password"),
				"host": detour.sub("tls").str("server_name")}
			p["plugin"] = "shadow-tls"
			p["plugin-opts"] = map[string]interface{}(po)
			p["client-fingerprint"] = "chrome"
		}
	case "vmess":
		if _, present, exact := o.exactInt("alter_id"); present && !exact {
			return nil, errors.New("alter_id 不正确")
		}
		p["uuid"] = o.str("uuid")
		p["alterId"] = o.num("alter_id")
		p["cipher"] = firstNonEmptyString(o.str("security"), "auto")
	case "vless":
		p["uuid"] = o.str("uuid")
		p.set("flow", o.str("flow"))
	case "trojan", "hysteria2", "anytls":
		p["password"] = o.str("password")
	case "tuic":
		p["uuid"] = o.str("uuid")
		p["password"] = o.str("password")
		p.set("congestion-controller", o.str("congestion_control"))
		p.set("udp-relay-mode", o.str("udp_relay_mode"))
	case "snell":
		// A5-01：与 ss 分支同型——snell 本体字段 + detour 到 shadowtls
		// 时地址在 detour 出站上（面板自产的 snell+ShadowTLS 订阅正是
		// 此形态，本体不带 server/server_port）。
		p["psk"] = o.str("psk")
		if v := o.num("version"); v != 0 {
			p["version"] = v
		}
		// R2X-023：mode 是 v6 的流量整形字段，仅 v6 且值为 unshaped
		// 时保留（与 model 校验同域），否则往返中静默丢失。
		if o.num("version") == 6 && o.str("mode") == SnellModeUnshaped {
			p["mode"] = SnellModeUnshaped
		}
		if detour := byTag[o.str("detour")]; detour.str("type") == "shadowtls" {
			if _, present, exact := detour.exactInt("server_port"); present && !exact {
				return nil, errors.New("server_port 不是整数")
			}
			p["server"], p["port"] = detour.str("server"), detour.num("server_port")
			p["shadow-tls"] = map[string]interface{}{"version": detour.num("version"), "password": detour.str("password"),
				"host": detour.sub("tls").str("server_name")}
			// X-P3-12（L-02-2）：与同文件 ss detour 分支同口径补
			// client-fingerprint——sing-box 的 snell 出站无 utls 字段可
			// 读，缺省 chrome 与自建入站 clashSnell 的输出对齐。
			p["client-fingerprint"] = "chrome"
		}
	case "naive":
		// A5-01：NaïveProxy 恒为 HTTPS，tls 块字段由下方通用段落读取。
		p.set("username", o.str("username"))
		p["password"] = o.str("password")
		p["tls"] = true
	case "socks5", "http":
		p.set("username", o.str("username"))
		p.set("password", o.str("password"))
	}
	if typ == "hysteria2" {
		obfs := o.sub("obfs")
		p.set("obfs", obfs.str("type"))
		p.set("obfs-password", obfs.str("password"))
		if ports := o.strs("server_ports"); len(ports) > 0 {
			p["ports"] = strings.ReplaceAll(ports[0], ":", "-")
		}
	}
	if tls := o.sub("tls"); tls.flag("enabled") {
		sniKey := "sni"
		if typ == "vmess" || typ == "vless" || typ == "socks5" || typ == "http" || typ == "naive" {
			p["tls"] = true
			sniKey = "servername"
		}
		p.set(sniKey, tls.str("server_name"))
		p.set("skip-cert-verify", tls.flag("insecure"))
		p.set("alpn", tls.strs("alpn"))
		p.set("client-fingerprint", tls.sub("utls").str("fingerprint"))
		if r := tls.sub("reality"); r.flag("enabled") {
			p["reality-opts"] = map[string]interface{}{"public-key": r.str("public_key"), "short-id": r.str("short_id")}
		}
	}
	if t := o.sub("transport"); len(t) > 0 {
		host := t.sub("headers").str("Host")
		if hosts := t.strs("host"); host == "" && len(hosts) > 0 {
			host = hosts[0]
		}
		netType := t.str("type")
		if netType == "http" {
			netType = "h2"
		}
		applyTransport(p, netType, "", host, t.str("path"), t.str("service_name"), "")
	}
	return p, nil
}

// ---- Surge / Loon ----

// parseSurgeLine parses "name = type, server, port, key=value, ...".
func parseSurgeLine(line string) (Proxy, error) {
	name, body, ok := strings.Cut(line, "=")
	if !ok {
		return nil, errors.New("无法识别的格式")
	}
	fields := splitSurgeFields(body)
	if len(fields) < 3 {
		return nil, errors.New("Surge 节点格式不正确")
	}
	typ := strings.ToLower(fields[0])
	port, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, errors.New("端口不正确")
	}
	kv := Proxy{}
	var positional []string
	for _, f := range fields[3:] {
		if k, v, ok := strings.Cut(f, "="); ok {
			kv[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		} else {
			positional = append(positional, f)
		}
	}
	p := Proxy{"name": strings.TrimSpace(name), "server": fields[1], "port": port}
	tlsCommon := func(sniKey string) {
		p.set(sniKey, kv.str("sni"))
		p.set("skip-cert-verify", kv.flag("skip-cert-verify"))
	}
	switch typ {
	case "ss", "shadowsocks":
		p["type"] = "ss"
		p["cipher"] = firstNonEmptyString(kv.str("encrypt-method"), kv.str("method"))
		p["password"] = kv.str("password")
		if len(positional) >= 2 && p.str("cipher") == "" { // Loon: ss, host, port, method, "password"
			p["cipher"], p["password"] = positional[0], strings.Trim(positional[1], `"`)
		}
		p["udp"] = kv.flag("udp-relay") || kv.flag("udp")
		if obfs := kv.str("obfs"); obfs != "" {
			p["plugin"] = "obfs"
			po := Proxy{"mode": obfs}
			po.set("host", kv.str("obfs-host"))
			p["plugin-opts"] = map[string]interface{}(po)
		}
		applySurgeShadowTLS(p, kv)
	case "vmess":
		p["type"] = "vmess"
		p["uuid"] = kv.str("username")
		p["cipher"] = "auto"
		p["alterId"] = 0
		if kv.str("vmess-aead") == "false" {
			p["alterId"] = 1
		}
		if kv.flag("tls") {
			p["tls"] = true
			tlsCommon("servername")
		}
		applySurgeWS(p, kv)
	case "trojan":
		p["type"] = "trojan"
		p["password"] = kv.str("password")
		tlsCommon("sni")
		applySurgeWS(p, kv)
	case "snell":
		p["type"] = "snell"
		p["psk"] = kv.str("psk")
		p["version"] = firstNonEmptyString(kv.str("version"), "4")
		p["version"] = Proxy{"v": p["version"]}.num("v")
		// R2X-023：同 singboxToProxy 口径，v6 + unshaped 才保留 mode。
		if p.num("version") == 6 && kv.str("mode") == SnellModeUnshaped {
			p["mode"] = SnellModeUnshaped
		}
		if obfs := kv.str("obfs"); obfs != "" {
			oo := Proxy{"mode": obfs}
			oo.set("host", kv.str("obfs-host"))
			p["obfs-opts"] = map[string]interface{}(oo)
		}
		applySurgeShadowTLS(p, kv)
	case "hysteria2":
		p["type"] = "hysteria2"
		p["password"] = kv.str("password")
		tlsCommon("sni")
		p.set("ports", kv.str("port-hopping"))
		p.set("obfs-password", kv.str("salamander-password"))
		if p.str("obfs-password") != "" {
			p["obfs"] = "salamander"
		}
	case "tuic", "tuic-v5":
		p["type"] = "tuic"
		p["uuid"] = kv.str("uuid")
		p["password"] = kv.str("password")
		p["alpn"] = firstNonEmptySlice(kv.strs("alpn"), []string{"h3"})
		tlsCommon("sni")
	case "anytls":
		p["type"] = "anytls"
		p["password"] = kv.str("password")
		tlsCommon("sni")
	case "socks5", "socks5-tls":
		p["type"] = "socks5"
		applySurgeAuth(p, kv, positional)
		if typ == "socks5-tls" {
			p["tls"] = true
			tlsCommon("servername")
		}
	case "http", "https":
		p["type"] = "http"
		applySurgeAuth(p, kv, positional)
		if typ == "https" {
			p["tls"] = true
			tlsCommon("servername")
		}
	default:
		return nil, fmt.Errorf("暂不支持 %s 类型的节点", typ)
	}
	return p, nil
}

func firstNonEmptySlice(values ...[]string) []string {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

func applySurgeAuth(p Proxy, kv Proxy, positional []string) {
	p.set("username", kv.str("username"))
	p.set("password", kv.str("password"))
	if p.str("username") == "" && len(positional) >= 2 {
		p["username"], p["password"] = positional[0], positional[1]
	}
}

func applySurgeWS(p Proxy, kv Proxy) {
	if !kv.flag("ws") && kv.str("transport") != "ws" {
		return
	}
	p["network"] = "ws"
	wo := Proxy{}
	wo.set("path", firstNonEmptyString(kv.str("ws-path"), kv.str("path")))
	for _, h := range strings.Split(kv.str("ws-headers"), "|") {
		if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			wo["headers"] = map[string]interface{}{"Host": strings.TrimSpace(v)}
		}
	}
	p.set("ws-opts", wo)
}

func applySurgeShadowTLS(p Proxy, kv Proxy) {
	pw := kv.str("shadow-tls-password")
	if pw == "" {
		return
	}
	version := kv.num("shadow-tls-version")
	if version == 0 {
		version = 3
	}
	if p.Type() == "ss" {
		p["plugin"] = "shadow-tls"
		p["plugin-opts"] = map[string]interface{}{"host": kv.str("shadow-tls-sni"), "password": pw, "version": version}
		p["client-fingerprint"] = "chrome"
		return
	}
	p["shadow-tls"] = map[string]interface{}{"host": kv.str("shadow-tls-sni"), "password": pw, "version": version}
}

// splitSurgeFields splits on commas outside double quotes.
func splitSurgeFields(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ',' && !quoted:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}
