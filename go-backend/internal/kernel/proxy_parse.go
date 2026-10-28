package kernel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
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

// validateImportHostnames validates the TLS SNI and transport Host fields
// of an imported proxy. The import path never validated these hostname
// fields (sweep §3): an empty-label SNI/Host (e.g. `a..b`) sailed
// through ParseImport while the self-built face rejects the same values
// (normalizeSecurity / normalizeTransport). Reuse the T-01 HasEmptyLabel
// predicate so both faces share one verdict; messages reuse the
// self-built face's wording.
func validateImportHostnames(p Proxy) error {
	// F-LIVE-01：错误附引坏值原文——与自建面同口径，备份恢复入站段
	// 汇总 warning 才能与转发/订阅源/分享/配置四段一样引用坏值。
	if sni := p.serverName(); sni != "" && (!hostPattern.MatchString(sni) || HasEmptyLabel(sni)) {
		return fmt.Errorf("SNI 格式错误：%q", sni)
	}
	for _, h := range importTransportHosts(p) {
		if h != "" && (!hostPattern.MatchString(h) || HasEmptyLabel(h)) {
			return fmt.Errorf("Host 格式错误：%q", h)
		}
	}
	for _, h := range importPluginHosts(p) {
		if h != "" && (!hostPattern.MatchString(h) || HasEmptyLabel(h)) {
			return fmt.Errorf("Host 格式错误：%q", h)
		}
	}
	return nil
}

// importTransportHosts collects the transport Host header from every
// network option that carries one (ws-opts / http-opts / h2-opts /
// xhttp-opts). One field, four carriers — validate them all so the same
// gap cannot recur in another network type.
//
// B-01: the key lookup is case-insensitive (EqualFold over the map
// keys) instead of enumerating spellings by hand. HTTP header names
// are case-insensitive, and the render faces read every spelling
// (transportFields reads Host/host case-insensitively for every carrier
// via firstHeaderHostValue, which shares the collector below; forEndpoint
// fills either) or pass the map through verbatim (Clash), so the previous
// exact-spelling enumeration ("Host" for ws/http-opts, "host" for
// h2/xhttp-opts) let the opposite spelling escape validation and land
// verbatim in subscriptions. R2-L-01: the render side used to dual-read
// only ws — http/h2/xhttp each read one spelling — so the premise above
// was false for the render faces until firstHeaderHostValue closed it;
// the "store keys unchanged" decision (CLOSED-LIST §2e) stands on the
// render side now reading every spelling. R3-D-01: the premise's other
// half — "values the render side reads out are legal" — never held
// for stored rows: rows that escaped the pre-B-01 import check kept
// rendering verbatim, and R2-L-01 widened their emission from 1
// carrier to 4. The render predicate (validImportedProxyFields) now
// re-validates hostname labels at render with this same collector,
// so stored bad-label rows are skipped instead of emitted; storage
// keys still unchanged. Values are collected without
// splitting on commas: a Host header is a single host, and a comma-joined
// value fails hostPattern as a whole (fail-closed).
//
// R4-B-01: the collector above missed one more form — the nested
// `headers` sub-map under h2-opts/xhttp-opts (the non-canonical Clash
// YAML shape, `h2-opts: {headers: {Host: ...}}`). ws/http read their
// nested headers from the start; h2/xhttp read only the direct key,
// so a bad label under the nested form escaped both import validation
// and the render predicate (which shares this collector) and was
// emitted verbatim on the Clash/Stash faces.
//
// R5-B-01: the "two forms per carrier" claim above was false for
// ws/http — those two carriers read ONLY the nested headers map, so
// a bad label under the direct `host` key escaped the same two gates
// and was emitted verbatim on Clash/Stash. Read lines, per carrier
// (direct key first, nested headers second; every spelling via
// headerHostValues, string and list values alike):
//
//	ws-opts:    direct `host` key + `headers` sub-map
//	http-opts:  direct `host` key + `headers` sub-map
//	h2-opts:    direct `host` key + `headers` sub-map
//	xhttp-opts: direct `host` key + `headers` sub-map
//
// Enumeration boundary: `headers` must be a map
// (Proxy.sub returns empty for non-maps); Clash YAML headers is a
// map, and no other Host-bearing shape exists under these opts.
func importTransportHosts(p Proxy) []string {
	var out []string
	out = append(out, headerHostValues(p.sub("ws-opts"))...)
	out = append(out, headerHostValues(p.sub("ws-opts").sub("headers"))...)
	out = append(out, headerHostValues(p.sub("http-opts"))...)
	out = append(out, headerHostValues(p.sub("http-opts").sub("headers"))...)
	out = append(out, headerHostValues(p.sub("h2-opts"))...)
	out = append(out, headerHostValues(p.sub("h2-opts").sub("headers"))...)
	out = append(out, headerHostValues(p.sub("xhttp-opts"))...)
	out = append(out, headerHostValues(p.sub("xhttp-opts").sub("headers"))...)
	return out
}

// headerHostValues collects every value stored under any case spelling
// of the "host" key in m (Host / host / HOST / ...), expanding list
// values element-wise. Non-string, non-list values are ignored.
//
// R6-L-01: keys are visited in ascending byte order, not map iteration
// order, so the collected order is deterministic: firstHeaderHostValue
// takes vals[0], which is then always the first element of the
// byte-order smallest spelling, and validators that consume the whole
// list still see every value.
func headerHostValues(m Proxy) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.EqualFold(k, "host") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		v := m[k]
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				out = append(out, s)
			}
		case []string:
			for _, s := range t {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		case []interface{}:
			for _, x := range t {
				if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// importPluginHosts collects the plugin/obfs Host from the
// Shadowsocks/Snell plugin carriers (plugin-opts / obfs-opts /
// shadow-tls). L1-F-02: validateImportHostnames enumerated only the
// four transport carriers, so a plugin Host (`obfs-host=a..b`,
// `v2ray-plugin host=a..b`, `shadow-tls host=a..b`, Surge obfs-host,
// Clash plugin-opts host, sing-box SS/Snell shadowtls detour
// server_name — all normalized into these three keys by the time
// finishProxy runs) sailed through import and rendered verbatim into
// subscriptions. Same verdict and wording as the transport carriers.
// B-01 (same mechanism): the keys are read case-insensitively via
// headerHostValues — a capitalised `Host` key in a Clash profile
// passes the map through to Mihomo exactly like the transport
// carriers did, so it must be validated under every spelling too.
func importPluginHosts(p Proxy) []string {
	var out []string
	out = append(out, headerHostValues(p.sub("plugin-opts"))...)
	out = append(out, headerHostValues(p.sub("obfs-opts"))...)
	out = append(out, headerHostValues(p.sub("shadow-tls"))...)
	return out
}

// winnerHostKey returns the spelling key that firstHeaderHostValue
// reads from m, or "" when m carries no host value. It mirrors
// firstHeaderHostValue step for step: exact "Host" first, exact
// "host" second (both via strs, which drops empty elements), then the
// byte-order smallest remaining EqualFold spelling that contributes a
// value under headerHostValues expansion (checked per key through
// headerHostValues itself, so the expansion rules cannot drift apart).
func winnerHostKey(m Proxy) string {
	if vals := m.strs("Host"); len(vals) > 0 {
		return "Host"
	}
	if vals := m.strs("host"); len(vals) > 0 {
		return "host"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.EqualFold(k, "host") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(headerHostValues(Proxy{k: m[k]})) > 0 {
			return k
		}
	}
	return ""
}

// normalizeHostKeySpelling collapses every host-spelling key in m to
// the single winner spelling (R-STORE-01). Only the key names change:
// the winner key keeps its value verbatim (string stays string, list
// stays list, list-first-element semantics unchanged), so every render
// face still reads the same winner value through firstHeaderHostValue.
// With no winner (no host value at all) the map is left untouched.
// Callers must run this only after validation has passed: validation
// reads every spelling's values, while this drops the loser values.
func normalizeHostKeySpelling(m Proxy) {
	winner := winnerHostKey(m)
	if winner == "" {
		return
	}
	for k := range m {
		if k != winner && strings.EqualFold(k, "host") {
			delete(m, k)
		}
	}
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
	host, port, err := validateImportedProxyAddress(p)
	if err != nil {
		return ImportedNode{}, err
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
	// R31-L4-E01：SS plugin 别名进口归一——其余三转换器均在进口把
	// obfs-local / simple-obfs 归一为面板内规范名 obfs（applySSPlugin，
	// 下方 :543；Surge 直写 obfs），唯独 Clash 透传不过归一，而全部
	// 下游消费点只认 obfs（proxySingbox / proxySurge / ssPluginString /
	// 三处 A2-03 门），致 sing-box 与 Surge 面整节点丢弃、URI 面静默
	// 丢插件参数。finishProxy 为四格式的共同收口（与 R4X-001 cipher
	// 小写化同形），在此一处把两别名拼写归一为 obfs；其余三格式进口
	// 已归一，本处对它们是空操作。plugin-opts 的 mode 键 Clash 原生
	// 即与面板内形态一致，无需转换（与 applySSPlugin 的 obfs/obfs-host
	// 键映射不同，彼为 SIP003 串形态）；但 host 键不在此列——R3-L-01：
	// Clash 用户常写大写 `Host`，渲染面须经 firstHeaderHostValue 大小
	// 写不敏感读取，不得精确读小写 `host`。R-STORE-01：新导入行
	// 的 host 键在 finishProxy 末尾归一为胜者拼写（原 §2e“存储键
	// 不改写”设计变更为新导入行归一；存量行不迁移，仍经 helper
	// 读取）。归一后 A2-03
	// 门（mode 合法集 {http, tls} + 小写愈合）对别名拼写同样生效。
	// 大小写不敏感判定与 A2-03 门 / ssPluginString 同口径。
	// B-03：先把 plugin 名本身小写愈合落库（OBFS / V2Ray-Plugin /
	// Shadow-TLS 等变体名原样落库时，四面渲染开关的精确匹配整节点
	// 丢弃），再走别名归一；未知插件名小写化后仍为未知名，仍由各面
	// 谓词/开关处置，接受集不扩大。
	if t == "ss" {
		if pl := strings.ToLower(strings.TrimSpace(p.str("plugin"))); pl != "" {
			p["plugin"] = pl
		}
		switch p.str("plugin") {
		case "obfs-local", "simple-obfs":
			p["plugin"] = "obfs"
		}
	}
	if err := validateProxyCredentials(p); err != nil {
		return ImportedNode{}, err
	}
	if err := validateImportHostnames(p); err != nil {
		return ImportedNode{}, err
	}
	// R-STORE-01：校验通过后，在 finishProxy 这一处把各载体 host 键
	// 归一为 firstHeaderHostValue 的胜者拼写（新导入行；存量行不迁
	// 移，仍经 helper 确定性读取）。归一范围与校验侧收集器严格同
	// 构：四载体直接键 + 嵌套 headers + 三插件子 map；只改键名，
	// 不改值形，渲染三面所见胜者值不变。
	normalizeHostKeySpelling(p.sub("ws-opts"))
	normalizeHostKeySpelling(p.sub("ws-opts").sub("headers"))
	normalizeHostKeySpelling(p.sub("http-opts"))
	normalizeHostKeySpelling(p.sub("http-opts").sub("headers"))
	normalizeHostKeySpelling(p.sub("h2-opts"))
	normalizeHostKeySpelling(p.sub("h2-opts").sub("headers"))
	normalizeHostKeySpelling(p.sub("xhttp-opts"))
	normalizeHostKeySpelling(p.sub("xhttp-opts").sub("headers"))
	normalizeHostKeySpelling(p.sub("plugin-opts"))
	normalizeHostKeySpelling(p.sub("obfs-opts"))
	normalizeHostKeySpelling(p.sub("shadow-tls"))
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
	// R4X-002: Reality 凭据横切校验——任何协议只要带 reality-opts
	// 就查（trojan 同样可携带，不得只绑 vless 分支）。
	if err := validateRealityOpts(p); err != nil {
		return err
	}
	// R10X-001（横切格）：client-fingerprint 是 sing-box uTLS 有限
	// 枚举——任一带 TLS 的协议都可携带，在 finishProxy 一处按合法
	// 集校验 + 小写愈合落库（与 cipher 同形）。大小写变体（Chrome /
	// FIREFOX 是异源导出与手写配置的常见形态）原样落库会毒化
	// sing-box 订阅整表、垃圾值兼毒 Xray 链式整单。此前进口/谓词/
	// 渲染三层全缺（R10-D-01）。
	if fp := p.str("client-fingerprint"); fp != "" {
		p["client-fingerprint"] = strings.ToLower(fp)
	}
	if !ValidClientFingerprint(p.str("client-fingerprint")) {
		return errors.New("client-fingerprint 不受支持（须为 sing-box uTLS 指纹：chrome / firefox / edge / safari / 360 / qq / ios / android / random / randomized / chrome_psk）")
	}
	// R10X-002（横切格）：xhttp-opts.mode 有限枚举 {auto, packet-up,
	// stream-up, stream-one}——进口一处校验 + 小写愈合，集外逐条
	// 拒。AUTO / Packet-Up 原样落库毒化 Clash 整表与 Xray 链式整
	// 单（R10-D-02）。
	if xo := p.sub("xhttp-opts"); xo.str("mode") != "" {
		cp := make(Proxy, len(xo))
		for k, v := range xo {
			cp[k] = v
		}
		cp["mode"] = strings.ToLower(xo.str("mode"))
		p["xhttp-opts"] = map[string]interface{}(cp)
	}
	if !ValidXHTTPMode(p.sub("xhttp-opts").str("mode")) {
		return errors.New("xhttp mode 不受支持（仅 auto / packet-up / stream-up / stream-one）")
	}
	switch p.Type() {
	case "ss":
		// R4X-001: cipher 名一处 ToLower 收口（四格式三入口全经
		// finishProxy 到此，与 ss 链接 parseSSURI 既有口径对齐），
		// 落库即规范小写名。此前仅 ss 链接一处小写化，Clash /
		// sing-box JSON / Surge 三格式直传变体可整体绕过下方
		// SS2022 校验并原样渲染毒化整表。
		if c := p.str("cipher"); c != "" {
			p["cipher"] = strings.ToLower(c)
		}
		if p.str("cipher") == "" || p.str("password") == "" {
			return errors.New("缺少加密方式或密码")
		}
		// R6X-001：cipher 名本身是有限枚举域——双拒格（双客户端均
		// unknown method）导入期逐条拒绝，合法集 = ValidSSCipher
		// （共同集 18 ∪ 单面集 6 值，逐名见 credentials.go
		// ssCiphersMihomoOnly，逐值口径见 credentials.go 谓词注与
		// R6-FIX-B1 逐值探针存档）。单面集方法进口不拒，渲染面分面
		// 处置（sing-box 面略过、Clash 面保留）。
		if !ValidSSCipher(p.str("cipher")) {
			return errors.New("Shadowsocks 加密方式 cipher 不受支持")
		}
		// R3X-008：SS2022 密钥格式在导入期一处收口（ss 链接 / Clash /
		// sing-box JSON / Surge 四格式与订阅同步、换链接编辑全部经
		// finishProxy 到此）。坏 key 会毒化整份 sing-box 订阅，必须
		// 逐条报错（调用方按条收集、不连坐其余节点）；legacy AEAD
		// 与未知 cipher 不查，口径见 ValidateSS2022Password。
		if err := ValidateSS2022Password(p.str("cipher"), p.str("password")); err != nil {
			return err
		}
		// R10X-003：plugin=obfs 时 plugin-opts.mode 是有限枚举
		// {http, tls}——小写愈合落库 + 合法集校验（与 ssPluginString
		// 消费前同口径，见 proxy_produce.go）。HTTP 大写原样落库同
		// 时毒化 sing-box 与 Clash 两订阅整表（R10-D-03）。plugin 名
		// 大小写不敏感判定；v2ray-plugin 的 mode 不在此集内、不查。
		if strings.EqualFold(p.str("plugin"), "obfs") {
			if oo := p.sub("plugin-opts"); oo.str("mode") != "" {
				cp := make(Proxy, len(oo))
				for k, v := range oo {
					cp[k] = v
				}
				cp["mode"] = strings.ToLower(oo.str("mode"))
				p["plugin-opts"] = map[string]interface{}(cp)
			}
			if !ValidSSObfsMode(p.sub("plugin-opts").str("mode")) {
				return errors.New("Shadowsocks obfs mode 不受支持（仅 http / tls）")
			}
		}
	case "vmess":
		if p.str("uuid") == "" {
			return errors.New("缺少 UUID")
		}
		// BUG-A4-2: 与自建入站同口径校验 UUID 格式（model.go 的
		// uuidPattern），此前只查非空，vless://not-a-uuid 会静默入库、
		// 以非法 uuid 渲染进订阅毒化整份配置。
		if !uuidPattern.MatchString(strings.ToLower(p.str("uuid"))) {
			return errors.New("UUID 格式错误")
		}
		// R5X-001(a)：cipher 是有限枚举域——垃圾值双客户端整表毒化
		// （sing-box / mihomo 均 FATAL），导入期逐条拒绝。合法集与
		// 大小写愈合口径见 credentials.go 谓词注。
		if c := p.str("cipher"); c != "" {
			p["cipher"] = strings.ToLower(c)
		}
		if !ValidVMessCipher(p.str("cipher")) {
			return errors.New("VMess 加密方式 cipher 不受支持（仅 auto / aes-128-gcm / chacha20-poly1305 / none / zero）")
		}
	case "vless":
		if p.str("uuid") == "" {
			return errors.New("缺少 UUID")
		}
		if !uuidPattern.MatchString(strings.ToLower(p.str("uuid"))) {
			return errors.New("UUID 格式错误")
		}
		// R5X-003(a)(c)：flow 与 encryption 均为有限域——flow 垃圾值
		// 毒化 sing-box 整表、encryption 垃圾值毒化 mihomo 整表（其
		// 法定下发面），合法集见 credentials.go 谓词注；encryption
		// 按 Xray ENC 文法校验，真实 EncryptionString 产物恒通过。
		if f := p.str("flow"); f != "" {
			p["flow"] = strings.ToLower(f)
		}
		if !ValidVLESSFlow(p.str("flow")) {
			return errors.New("VLESS flow 不受支持（仅 xtls-rprx-vision 或空）")
		}
		if !ValidVLESSEncryption(p.str("encryption")) {
			return errors.New("VLESS encryption 格式错误（须为 none 或 mlkem768x25519plus 客户端加密串）")
		}
		// 手动填写节点修复（F-01）：flow 与 TLS/ENC 的耦合校验——
		// xtls-rprx-vision 是 TLS 之上的 XTLS 优化，无 TLS 且无真实
		// 加密时无意义（面板自建面 visionAllowed 同口径：VLESS +
		// TCP +（安全≠无 或 开了加密））。只查 TLS/ENC 耦合，不查
		// network（Vision+WS 的网络维由前端设防）。
		if p.str("flow") != "" && !p.flag("tls") {
			if enc := p.str("encryption"); enc == "" || enc == "none" {
				return errors.New("VLESS flow 需要 TLS 或加密（xtls-rprx-vision 不能用于无 TLS 且无加密的节点）")
			}
		}
	case "trojan", "anytls":
		if p.str("password") == "" {
			return errors.New("缺少密码")
		}
	case "hysteria2":
		if p.str("password") == "" {
			return errors.New("缺少密码")
		}
		// R5X-001(b)：obfs 类型是有限枚举域，垃圾值双客户端整表
		// 毒化，导入期逐条拒绝。
		if o := p.str("obfs"); o != "" {
			p["obfs"] = strings.ToLower(o)
		}
		if !ValidHy2Obfs(p.str("obfs")) {
			return errors.New("Hysteria2 obfs 类型不受支持（仅 salamander / gecko 或空）")
		}
		// R5 前置探针定生死：ports 垃圾形态经面板渲染产物 + 双二进
		// 制坐实整表毒化（abc 双客户端 FATAL、越界段 sing-box
		// FATAL），入校验集逐条拒绝。
		if !ValidHy2Ports(p.str("ports")) {
			return errors.New("Hysteria2 ports 格式错误（须为逗号分隔的端口或端口段，端口在 1–65535 内）")
		}
	case "tuic":
		if p.str("uuid") == "" || p.str("password") == "" {
			return errors.New("缺少 UUID 或密码")
		}
		if !uuidPattern.MatchString(strings.ToLower(p.str("uuid"))) {
			return errors.New("UUID 格式错误")
		}
		// R5X-003(b)：congestion-controller 垃圾值毒化 sing-box 整
		// 表，合法集 {bbr, cubic, new_reno} 见谓词注。udp-relay-mode
		// 未验证（双二进制 check 均不校验、拨号期未验），不在此拒。
		if cc := p.str("congestion-controller"); cc != "" {
			p["congestion-controller"] = strings.ToLower(cc)
		}
		if !ValidTUICCongestionController(p.str("congestion-controller")) {
			return errors.New("TUIC congestion-controller 不受支持（仅 bbr / cubic / new_reno）")
		}
	case "snell":
		if p.str("psk") == "" {
			return errors.New("缺少 PSK")
		}
		// R11X-006：顶层 mode（v6 流量整形）一处收口——与渲染面
		// forEndpoint 同口径，version==6 且 EqualFold(unshaped) 时愈合
		// 落库规范值 SnellModeUnshaped；其余值（bogus、非 v6 的 mode）
		// 不落 mode（沿两转换器既有丢弃口径、节点本身不拒）。执法只
		// 此一处：两转换器（sing-box / Surge）只搬运原始 mode、Clash
		// 透传亦经 finishProxy 到此。落点选 validateProxyCredentials
		// 而非 finishProxy 本体——其余枚举字段的进口愈合（cipher /
		// flow / congestion-controller 等）全在此函数同形收口，且
		// finishProxy 已先行完成 version 域校验与缺省补 4，此处读到
		// 的 version 即最终落库版本。备份恢复面经 ValidateImportedProxy
		// 传副本调用本函数、不回写，存储逐字恢复口径不受影响。
		if p.num("version") == 6 && strings.EqualFold(p.str("mode"), SnellModeUnshaped) {
			p["mode"] = SnellModeUnshaped
		} else {
			delete(p, "mode")
		}
		// R5X-003(d)：obfs-opts.mode 垃圾值毒化 mihomo 整表（法定主
		// 面无守卫、sing-box 面既有略过）。历史拼写 shadowtls 在此
		// 愈合为 mihomo 规范拼写 shadow-tls 落库（mihomo 本体拒绝
		// 无连字符形态）。
		if oo := p.sub("obfs-opts"); oo.str("mode") != "" {
			cp := make(Proxy, len(oo))
			for k, v := range oo {
				cp[k] = v
			}
			cp["mode"] = CanonicalSnellObfsMode(oo.str("mode"))
			oo = cp
			p["obfs-opts"] = map[string]interface{}(oo)
		}
		if !ValidSnellObfsMode(p.sub("obfs-opts").str("mode")) {
			return errors.New("Snell obfs mode 不受支持（仅 http / tls / shadow-tls / restls / jls 或空）")
		}
	case "naive":
		if p.str("username") == "" || p.str("password") == "" {
			return errors.New("缺少用户名或密码")
		}
	}
	// K-01: transport x security coupling — Vision only on TCP,
	// Reality banned only on WS (TCP/gRPC/XHTTP legal per the
	// capabilities matrix, see transportSecurityViolation). Checked
	// here, after the per-type columns, because Reality is
	// cross-protocol (vless / vmess / trojan) and flow is validated
	// for vless above. The backup-restore face inherits the same
	// rejection through ValidateImportedProxy.
	if v := transportSecurityViolation(p); v != "" {
		return errors.New(v)
	}
	return nil
}

// validateImportedProxyAddress is the address column (server host +
// port) of the import predicate, extracted from finishProxy so the
// ParseImport face and the backup-restore face validate addresses
// through this one implementation (R7X-003 — R6X-008 shared only the
// credential column, leaving the restore face's Extra server/port
// unchecked: loopback / cloud-metadata hosts and out-of-range ports
// restored verbatim while ParseImport rejected the same rows).
func validateImportedProxyAddress(p Proxy) (string, int, error) {
	host := strings.Trim(p.str("server"), "[]")
	if err := ValidateImportHost(host); err != nil {
		return "", 0, err
	}
	// D4-08：数值不确定即拒绝——小数端口被 num() 截断、溢出值被包成
	// 垃圾 int 都是「误解析成功」，必须在落库前拦下。
	port := p.num("port")
	if n, present, exact := p.exactInt("port"); present {
		if !exact {
			return "", 0, errors.New("端口不正确")
		}
		port = n
	}
	if port < 1 || port > 65535 {
		return "", 0, errors.New("端口不正确")
	}
	return host, port, nil
}

// ValidateImportedProxyRowAddress is the row-carrier form of the
// address column: a backup-restored imported-type inbound carries the
// same address a second time in its row's Listen/Port columns (the
// externalRow invariant writes Listen=Host, Port=Port from the
// finishProxy-validated pair), so the restore face validates the row
// carrier through this wrapper — which calls the one
// validateImportedProxyAddress implementation above, never a second
// predicate (R8X-004 facet A, along the R7X-003 shared-implementation
// rule; R7X-003 itself closed only the Extra carrier of this column).
func ValidateImportedProxyRowAddress(host string, port int) error {
	_, _, err := validateImportedProxyAddress(Proxy{"server": host, "port": port})
	return err
}

// ValidateImportedProxy is the exported form of the import predicate:
// ParseImport (finishProxy) and the backup-restore face for
// imported-type inbounds (kernel_id=0, whose credentials live in the
// Extra proxy object) validate through this one implementation, so
// the two faces can never drift apart (R6X-008 — the R5X-024 restore
// gate covered only self-built inbounds because the restore face had
// no shared predicate to call). The predicate covers all three columns
// of the import gate: the address column (validateImportedProxyAddress,
// R7X-003), the credential column (validateProxyCredentials), and the
// hostname column (validateImportHostnames, L1-F-03 — the sweep added
// it to finishProxy only, so a bad SNI/Host in an old backup row
// restored verbatim while ParseImport rejected the same row). The
// call order mirrors finishProxy (address → credentials → hostnames).
// The predicate may normalize fields on the passed Proxy (case healing,
// mirroring import); callers checking stored content must pass a
// throwaway copy and must not write the copy back — restore keeps the
// backup bytes verbatim.
func ValidateImportedProxy(p Proxy) error {
	if _, _, err := validateImportedProxyAddress(p); err != nil {
		return err
	}
	if err := validateProxyCredentials(p); err != nil {
		return err
	}
	return validateImportHostnames(p)
}

// validateRealityOpts checks the Reality fields of any proxy carrying
// reality-opts (R4X-002). Reality without a usable public key is not a
// working node: the key is rendered verbatim into sing-box / mihomo
// profiles, where a malformed or empty value fails the whole profile
// and takes every healthy node with it (the same poisoning mechanism
// as R3X-008's bad SS2022 keys), so bad values are rejected per node
// at import instead. The public-key predicate mirrors the decode in
// RealityPublicKey: RawURL base64 decoding to exactly 32 bytes — the
// form check deliberately accepts nothing wider (no Std/padded
// variants). short-id is checked alongside but is not a closing
// condition on its own: empty is allowed; a non-empty value must be
// lowercase hex, even length, at most 16 chars (the self-built face's
// shortIDPattern plus its even-length rule in normalizeSecurity).
// dest (L5-F-01) reuses the self-built face's L1-F-01 predicate and
// wording verbatim; spider-x must not contain whitespace/control
// characters (it is a URL path segment).
func validateRealityOpts(p Proxy) error {
	ro := p.sub("reality-opts")
	if len(ro) == 0 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(ro.str("public-key"))
	if err != nil || len(raw) != 32 {
		return errors.New("Reality 公钥 pbk 格式错误（须为 32 字节的 RawURL base64）")
	}
	if sid := ro.str("short-id"); sid != "" {
		if !shortIDPattern.MatchString(sid) || len(sid)%2 != 0 {
			return errors.New("Reality short-id 格式错误（须为偶数位小写十六进制，最多 16 位）")
		}
	}
	// L5-F-01：Reality dest 与自建面 normalizeSecurity 的 L1-F-01
	// 校验同谓词、同文案——SplitHostPort + 空标签拒 + 端口 1-65535。
	// 导入面此前根本不读该字段，`a..b:443` 无声丢弃而创建面拒收。
	if dest := ro.str("dest"); dest != "" {
		if host, port, err := net.SplitHostPort(dest); err != nil || host == "" {
			return errors.New("Reality 目标地址格式应为 host:port")
		} else if HasEmptyLabel(host) {
			return fmt.Errorf("Reality 目标地址不正确：%q", dest)
		} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return errors.New("Reality 目标端口错误")
		}
	}
	// L5-F-01 同型残留 spx：标准链接参数，对应 Xray 客户端 spiderX。
	// spiderX 是 URL 路径段，不可能含空白或控制字符，含则拒收。
	if spx := ro.str("spider-x"); spx != "" {
		for _, r := range spx {
			if r <= 0x20 || r == 0x7f {
				return errors.New("Reality spiderX 格式错误（不能含空白或控制字符）")
			}
		}
	}
	// 手动填写节点修复（F-02）：Reality 须填 SNI——serverName 是
	// 伪装站域名，空 SNI 的 Reality 节点在直连订阅路径下恒缺
	// server_name，地址为 IP 时握手必然失败。横切校验：vless /
	// vmess 存 servername，trojan 存 sni，serverName() 两键都认。
	if p.serverName() == "" {
		return errors.New("Reality 须填写 SNI（伪装站域名）")
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
	case "naive":
		// Supplementary (B-06 round-trip): the panel renders naive://
		// links but could not import them back — "暂不支持". Userinfo
		// is user:pass, PathEscape'd on emit, decoded like trojan's.
		return parseUserURI("naive", rest)
	case "socks", "socks5":
		return parseSocksURI(rest)
	case "http", "https":
		return nil, errors.New("http(s):// 是订阅地址，请单独粘贴在输入框里")
	}
	return nil, fmt.Errorf("暂不支持 %s:// 链接", scheme)
}

// errBadQuery marks a splitURI failure to parse the link query
// (B-04): callers with a legacy fallback (parseSSURI / parseSocksURI)
// must not treat it as a host-parse failure and silently retry
// without the query.
var errBadQuery = errors.New("链接参数解析失败")

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
		// B-04：不再丢弃 url.ParseQuery 错误——单对编码非法时
		// ParseQuery 只丢该对、其余照常返回，于是 security=%zz 的
		// security 对被无声剔除、节点以明文入库（sni=%zz 同理丢
		// SNI）。解析失败即整条拒收，与 security=bogus 明拒同口径。
		q, qerr := url.ParseQuery(rest[i+1:])
		if qerr != nil {
			return "", "", 0, nil, "", fmt.Errorf("%w：%v", errBadQuery, qerr)
		}
		query = q
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
	if err != nil && user == "" && !errors.Is(err, errBadQuery) {
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
//
// R4-C-01: fold the plugin name and the option keys to lowercase before
// matching (same caliber as the B-03 healing in finishProxy and the
// case-insensitive switch in ssPluginString). Before this, an uppercase
// name ("OBFS-LOCAL") was rejected at the door on the URI and sing-box
// faces, and uppercase option keys ("OBFS", "OBFS-HOST") were silently
// dropped while the node still imported — the finishProxy healing runs
// after parsing and is unreachable for these two call sites. Option
// values are NOT folded here: mode-value healing stays with the A2-03
// gate in finishProxy. Unknown names stay rejected after folding, so
// the acceptance set does not expand.
func applySSPlugin(p Proxy, spec string) error {
	parts := strings.Split(spec, ";")
	opts := map[string]string{}
	for _, kv := range parts[1:] {
		k, v, _ := strings.Cut(kv, "=")
		opts[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
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
	switch tls {
	case "tls", "1", "true":
		p["tls"] = true
		p.set("servername", j.str("sni"))
		p.set("client-fingerprint", j.str("fp"))
		p.set("alpn", j.strs("alpn"))
		if Proxy(v).flag("allowInsecure") || j.flag("skip-cert-verify") {
			p["skip-cert-verify"] = true
		}
	case "reality":
		// R2-L3-F-01：接入 tls=reality，与 sing-box 入口同口径
		// （sing-box VMess 出站 tls.reality.enabled 早已接纳并透传
		// reality-opts，共享谓词 validateRealityOpts 亦接纳
		// VMess+Reality）。此前本分支只认 tls∈{tls,1,true}，
		// tls=reality 无声落到明文，sni/fp/alpn 一并丢弃。pbk/sid/
		// spx 为 v2rayN VMess JSON 的 Reality 字段，realityDest
		// 与 VLESS 链接面同名（本面板分享链接回放键，见
		// proxyURI vmess 支）；缺 pbk 解析期即明拒，其余坏值
		// （坏 pbk/sid/dest/spx、缺 SNI、坏指纹）由 finishProxy
		// 的 validateRealityOpts / 横切校验收口，与 L5-F-01 同口径。
		p["tls"] = true
		p.set("servername", j.str("sni"))
		p.set("client-fingerprint", j.str("fp"))
		p.set("alpn", j.strs("alpn"))
		if Proxy(v).flag("allowInsecure") || j.flag("skip-cert-verify") {
			p["skip-cert-verify"] = true
		}
		ro := Proxy{}
		ro.set("public-key", j.str("pbk"))
		ro.set("short-id", j.str("sid"))
		ro.set("dest", j.str("realityDest"))
		ro.set("spider-x", j.str("spx"))
		if ro.str("public-key") == "" {
			return nil, errors.New("VMess Reality 链接缺少公钥 pbk")
		}
		p["reality-opts"] = map[string]interface{}(ro)
	case "", "none", "0", "false":
		// 明文：无 TLS 参数可读。
	default:
		// R2-L3-F-01 同机制：未识别的 tls 值不得静默降级为明文
		//（旧代码白名单 if 无 else 拒，xtls 等值无声消失）。
		return nil, fmt.Errorf("VMess tls 值不受支持：%q（仅支持 tls / reality / 留空表示明文）", j.str("tls"))
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
		case "", "none":
			// 明文：无 TLS 参数可读。
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
				// L5-F-01：链接上的 realityDest（目标地址）与 spx
				// （spiderX）此前根本不读，合法值也静默丢弃。
				// 与 pbk/sid 同进 reality-opts，校验收口于
				// validateRealityOpts（finishProxy / 恢复谓词 /
				// 渲染面谓词共用），坏值整条拒收。
				ro.set("dest", q.Get("realityDest"))
				ro.set("spider-x", q.Get("spx"))
				if ro.str("public-key") == "" {
					return nil, errors.New("Reality 链接缺少公钥 pbk")
				}
				p["reality-opts"] = map[string]interface{}(ro)
				if p.str("client-fingerprint") == "" {
					p["client-fingerprint"] = "chrome"
				}
			}
		default:
			// R2-L3-F-01 复扫同族残留：未识别的 security 值此前无声
			// 落到明文（sni/fp 一并丢弃），与 VMess JSON tls 白名单
			// if 同机制。仅放行已知值，其余明拒并点名该值。
			return nil, fmt.Errorf("security 值不受支持：%q（仅支持 tls / xtls / reality / none）", q.Get("security"))
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
	case "naive":
		// naive://user:pass@host:port — the B-06 render shape.
		// userinfo is already PathUnescaped above, like trojan's;
		// split on the first ":" so an escaped ":" in the password
		// survives. A missing half is rejected by finishProxy's
		// naive credential check, not here. SNI and other cross-cut
		// fields are deliberately not wired yet.
		u, pw, _ := strings.Cut(user, ":")
		p.set("username", u)
		p.set("password", pw)
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
		// B-04：query 解析失败不得走 legacy base64 回退（回退体会
		// 丢掉整个 query、把坏链接当好链接导入）。
		if errors.Is(err, errBadQuery) {
			return nil, err
		}
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
		// R2-L3-F-02：Clash 原生 `fingerprint`（uTLS）归一为面板内规范键
		// `client-fingerprint`（见 normalizeClashFingerprint），再进
		// finishProxy 的 R10X-001 合法集校验。
		rawMap := normalizeYAML(raw).(map[string]interface{})
		normalizeClashFingerprint(Proxy(rawMap))
		node, err := finishProxy(rawMap)
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

// normalizeClashFingerprint 把 Clash 原生键 `fingerprint` 归一为面板内
// 规范键 `client-fingerprint`（R2-L3-F-02）。parseClashProfile 此前整条
// 透传、无键映射：合法 uTLS 值只存于 `fingerprint` 键，sing-box 面回落
// chrome、分享链接丢 `fp` 参数、Xray 链式面回落默认指纹，仅 Clash 面靠
// 原 map 透传存活；垃圾值更绕过 finishProxy 的 R10X-001 合法集校验。
//
// 只归一 vless / vmess / trojan / anytls 四类：这四类在 Mihomo 里
// `fingerprint` 即 uTLS 指纹，且面板三面（sing-box utls、分享链接 fp、
// Xray 链式 Fingerprint）只读 `client-fingerprint`。hysteria2 的
// `fingerprint` 是证书 pin（SHA-256，与内部 `fingerprint` 键同义，
// proxyURI 的 pinSHA256 与 chain.go:443 的 PinSHA256 消费），须原样
// 保留；tuic / ss 等其余类型在 Mihomo 里无 uTLS fingerprint 键，同样不动。
//
// `fingerprint` 本身是双义键：uTLS 指纹（11 值短 token）与证书 pin
// （64 位十六进制 SHA-256）无交集——按值形态区分：pin 形态原样保留
// （此时 `fingerprint` 键仍为 pin 的唯一载体，删不得）；其余形态按
// 链接面 `fp` 的口径处理（`p.set` 只写非空、已有规范值不被覆盖）：
// 优先级 client-fingerprint（面板规范键，已在位者为准）> fingerprint
// （Clash 原生）> fp（非标准别名，末位兜底）。归一后删除原键：
// 映射掉的值若残留，Clash 面透传时新旧双键并存、面板内成双源头；
// 落入 `client-fingerprint` 的垃圾值由 finishProxy 的 R10X-001 拒收
// （0 落库），不由本函数另设校验。
func normalizeClashFingerprint(p Proxy) {
	switch p.Type() {
	case "vless", "vmess", "trojan", "anytls":
	default:
		return
	}
	if isSHA256HexPin(p.str("fingerprint")) {
		delete(p, "fp")
		return
	}
	if p.str("client-fingerprint") == "" {
		p.set("client-fingerprint", firstNonEmptyString(p.str("fingerprint"), p.str("fp")))
	}
	delete(p, "fingerprint")
	delete(p, "fp")
}

// isSHA256HexPin reports whether s is a 64-hex-digit SHA-256 value — the
// certificate-pin shape of the `fingerprint` key (vs. the short uTLS
// tokens in ValidClientFingerprint's set).
func isSHA256HexPin(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
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
		// R9X-002：encryption 是凭据列在格式转换器维度的漏搬字段——
		// 不搬则 finishProxy 的 ENC 收口校验够不到、节点静默降级为
		// 普通 vless。此处只补搬运（set 空值不写），校验/回执/分面
		// 全复用 finishProxy 既有实现，禁在转换层另设校验。
		p.set("encryption", o.str("encryption"))
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
		// R2X-023 / R11X-006：mode 是 v6 的流量整形字段。此处只搬运
		// 原始值（set 空值不写），保留/愈合执法一处收口于
		// validateProxyCredentials 的 snell 支——此前以精确匹配
		// == SnellModeUnshaped 决定保留，大小写变体在落库前即被静默
		// 丢弃、中央愈合够不到（禁在此与 Surge 面各写一份 EqualFold）。
		p.set("mode", o.str("mode"))
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
			// R5 批 1：全部端口段以逗号拼回面板 ports 形态（此前只取
			// 第一段、多段往返静默丢失），与 proxySingbox 的按项拆分
			// 渲染互为逆操作。
			items := make([]string, len(ports))
			for i, sp := range ports {
				items[i] = strings.ReplaceAll(sp, ":", "-")
			}
			p["ports"] = strings.Join(items, ",")
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
		// R5-D-01: headers keys are HTTP header names (case-insensitive,
		// RFC 9110 §5.1) and list-valued in sing-box, so read them with
		// the same list-aware, spelling-folding helper the render faces
		// use. The previous exact-spelling string read silently dropped
		// non-canonical spellings and fmt.Sprint-stringified list values
		// into a malformed hostname.
		host := firstHeaderHostValue(t.sub("headers"))
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
		p["cipher"] = firstNonEmptyString(kv.str("encrypt-method"), "auto")
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
		// R2X-023 / R11X-006：同 singboxToProxy 口径——此处只搬运原始
		// mode，保留/愈合执法一处收口于 validateProxyCredentials 的
		// snell 支（禁在此与 sing-box 面各写一份 EqualFold）。
		p.set("mode", kv.str("mode"))
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
