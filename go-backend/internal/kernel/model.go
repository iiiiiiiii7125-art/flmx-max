package kernel

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Settings struct {
	UUID     string `json:"uuid,omitempty"`
	Username string `json:"username,omitempty"` // NaïveProxy
	Flow     string `json:"flow,omitempty"`
	Password string `json:"password,omitempty"`
	Method   string `json:"method,omitempty"`

	// SnellVersion is the protocol version clients use (4, 5 or 6).
	SnellVersion int `json:"snellVersion,omitempty"`
	// SnellMode is Snell v6's traffic shaping: "" (default) or "unshaped";
	// clients must use the same.
	SnellMode string `json:"snellMode,omitempty"`
	// Masquerade is the site Hysteria2 serves to non-clients (optional URL).
	Masquerade string `json:"masquerade,omitempty"`
	// BackendPort is the loopback port a ShadowTLS-fronted Snell listens on.
	BackendPort int `json:"backendPort,omitempty"`
	// PortHopping is a UDP range ("20000-30000") the agent redirects to the
	// Hysteria2 port so clients can hop between ports.
	PortHopping string `json:"portHopping,omitempty"`

	// VLESS 加密（mlkem768x25519plus）：EncryptionAuth 为空表示禁用，
	// 取 x25519（服务端存 X25519 私钥）或 mlkem768（服务端存 ML-KEM seed）；
	// EncryptionSecret 是服务端认证秘密，客户端 encryption 串由它单向派生，不另存。
	EncryptionAuth   string `json:"encryptionAuth,omitempty"`
	EncryptionMode   string `json:"encryptionMode,omitempty"`
	EncryptionSecret string `json:"encryptionSecret,omitempty"`
}

// HopRange returns the port-hopping range, or 0, 0 when disabled.
func (in Inbound) HopRange() (int, int) {
	lo, hi, err := parseHopRange(in.Settings.PortHopping)
	if err != nil {
		return 0, 0
	}
	return lo, hi
}

func parseHopRange(spec string) (int, int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, nil
	}
	parts := strings.Split(strings.ReplaceAll(spec, ":", "-"), "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("端口跳跃格式应为 起始-结束，例如 20000-30000")
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || lo < 1024 || hi > 65535 || lo >= hi {
		return 0, 0, errors.New("端口跳跃范围需在 1024-65535 之间，且起始小于结束")
	}
	return lo, hi, nil
}

type Transport struct {
	Type        string `json:"type"`
	Path        string `json:"path,omitempty"`
	Host        string `json:"host,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

type Security struct {
	Type        string   `json:"type"`
	SNI         string   `json:"sni,omitempty"`
	ALPN        []string `json:"alpn,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`

	// TLS: "self" stores a generated certificate inline, "file" points at
	// paths that already exist on the node, "acme" lets sing-box obtain a
	// certificate for SNI (HTTP-01 / TLS-ALPN-01, or DNS-01 when configured).
	CertMode string `json:"certMode,omitempty"`
	CertPEM  string `json:"certPem,omitempty"`
	KeyPEM   string `json:"keyPem,omitempty"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`

	ACMEEmail string `json:"acmeEmail,omitempty"`
	// ACMEDNS selects DNS-01 ("cloudflare" / "alidns"); empty uses HTTP-01,
	// which needs TCP port 80 on the node reachable from the internet.
	ACMEDNS string `json:"acmeDns,omitempty"`
	// Cloudflare: API token. AliDNS: AccessKey ID.
	ACMEDNSKey string `json:"acmeDnsKey,omitempty"`
	// AliDNS: AccessKey secret.
	ACMEDNSSecret string `json:"acmeDnsSecret,omitempty"`

	RealityDest       string   `json:"realityDest,omitempty"`
	RealityPrivateKey string   `json:"realityPrivateKey,omitempty"`
	RealityPublicKey  string   `json:"realityPublicKey,omitempty"`
	RealityShortIDs   []string `json:"realityShortIds,omitempty"`
	// RealityMinClientVer is the oldest client Xray accepts. Xray 26.7.11+
	// defaults to 26.3.27 when unset, which locks out Mihomo/Clash (they
	// report 1.8.2) and older mobile clients, so the panel always sets it.
	RealityMinClientVer string `json:"realityMinClientVer,omitempty"`

	// ShadowTLS v3: SNI is the handshake site, this is the shared secret.
	ShadowTLSPassword string `json:"shadowTlsPassword,omitempty"`
}

// Inbound is the kernel-independent description of one listening service.
type Inbound struct {
	ID       int64  `json:"id"`
	Protocol string `json:"protocol"`
	Listen   string `json:"listen"`
	// OutboundIPStrategy scopes this inbound's outbound connections to an
	// IP family ("" / "auto" = no restriction; see egress.go).
	OutboundIPStrategy string          `json:"outboundIpStrategy,omitempty"`
	Port               int             `json:"port"`
	Settings           Settings        `json:"settings"`
	Transport          Transport       `json:"transport"`
	Security           Security        `json:"security"`
	Extra              json.RawMessage `json:"extra,omitempty"`
	// AcceptProxyProtocol makes the inbound require a PROXY protocol header
	// (sent by relay forwards) so the kernel sees the real client address.
	AcceptProxyProtocol bool `json:"acceptProxyProtocol,omitempty"`
	// ConnIdleSeconds is the per-inbound idle timeout (seconds) for Xray
	// (policy.levels[].connIdle). 0 = unset/clear: no policy segment is
	// emitted and Xray's level-0 default (300s, see
	// DefaultConnIdleSeconds) applies. 0 is accepted at Normalize (and
	// by the frontend as "clear"); it is only ever *rendered* as
	// "no segment", never written as an explicit 0 — Xray's official
	// policy doc gives no semantics for an explicit 0.
	// -1 = unlimited (UnlimitedConnIdleSentinel): rendered as
	// UnlimitedConnIdleSeconds (31536000 = 365d); with Xray's (N, 2N]
	// window the session then closes only after 1–2 years idle, which
	// is "never" for any real deployment (panel upgrades and node
	// restarts end sessions sooner). Range is -1, 0..MaxConnIdleSeconds;
	// Xray itself documents no upper bound (int seconds), the panel
	// caps at 86400 (24h) so a typo cannot pin dead connections for
	// days.
	// Non-Xray kernels have no equivalent and are refused at Normalize.
	ConnIdleSeconds int `json:"connIdleSeconds,omitempty"`
	// Users are extra clients (subscription users) rendered next to the
	// inbound's own credentials. Filled in when building a kernel config.
	Users []User `json:"-"`
	// Companion marks an SS metering companion inbound: a derived
	// listener that carries its origin inbound's share subscriptions as
	// Shadowsocks 2022 multi-user clients so their traffic can be counted
	// per subscription. Render-time only, filled from the row marker
	// (NodeInbound.CompanionOf) when building a kernel config.
	Companion bool `json:"-"`
	// Via is the proxy the inbound's traffic leaves through (a chained
	// outbound); nil leaves from the node itself. A Via without a host (its
	// target was deleted) drops the traffic. Filled in when building a
	// kernel config.
	Via *Endpoint `json:"-"`
	// Split are the inbound's 分流 rules, tried before its default exit;
	// targets resolved. Filled in when building a kernel config.
	Split []SplitRule `json:"-"`
}

func (in Inbound) Tag() string {
	return fmt.Sprintf("in-%d", in.ID)
}

// NetworkOn is Network for the kernel serving the inbound: sing-box's Snell
// has no QUIC proxy mode, so it only listens on TCP.
func (in Inbound) NetworkOn(kernelType string) string {
	if in.Protocol == ProtocolSnell && kernelType == TypeSingbox {
		return "tcp"
	}
	return in.Network()
}

// Network reports which L4 protocols the inbound listens on, used for nft
// traffic counters.
func (in Inbound) Network() string {
	switch in.Protocol {
	case ProtocolHysteria2, ProtocolTUIC:
		return "udp"
	case ProtocolShadowsocks:
		if in.Security.Type == SecurityShadowTLS {
			return "tcp"
		}
		return "tcp,udp"
	case ProtocolNaive:
		// HTTP/2 over TCP, plus HTTP/3 for clients that enable QUIC.
		return "tcp,udp"
	case ProtocolSnell:
		// v5 adds UDP (QUIC proxy mode); behind ShadowTLS only TCP is exposed.
		// v6 carries UDP over TCP.
		if in.Security.Type == SecurityShadowTLS || in.Settings.SnellVersion == 4 || in.Settings.SnellVersion == 6 {
			return "tcp"
		}
		return "tcp,udp"
	}
	return "tcp"
}

const (
	FlowVision                 = "xtls-rprx-vision"
	DefaultRealitySNI          = "www.amd.com"
	DefaultSSMethod            = "2022-blake3-aes-128-gcm"
	DefaultFingerprint         = "chrome"
	DefaultTLSCertMode         = CertModeSelf
	CertModeSelf               = "self"
	CertModeFile               = "file"
	CertModeACME               = "acme"
	ACMEDNSCloudflare          = "cloudflare"
	ACMEDNSAliDNS              = "alidns"
	DefaultQUICSNI             = "www.bing.com"
	DefaultRealityMinClientVer = "1.0.0"
	DefaultSnellVersion        = 5
	// DefaultConnIdleSeconds is Xray's level-0 connIdle default (official
	// policy doc: "connIdle: Connection idle time limit. Unit is seconds.
	// Default value is 300"). The panel never writes it explicitly: an
	// unset field means "Xray default", not "300 written by us".
	DefaultConnIdleSeconds = 300
	// MaxConnIdleSeconds is the panel-side cap for the per-inbound idle
	// timeout. Xray documents no upper bound; 86400 (24h) is the guard
	// against a mistyped huge value holding dead connections.
	MaxConnIdleSeconds = 86400
	// UnlimitedConnIdleSentinel is the stored value meaning "no idle
	// timeout" (-1). It renders as UnlimitedConnIdleSeconds below.
	UnlimitedConnIdleSentinel = -1
	// UnlimitedConnIdleSeconds is the rendered connIdle for an
	// unlimited inbound: 31536000 seconds (365 days). Xray's idle
	// close window is (N, 2N], so a session closes only after 1–2
	// years idle — equivalent to never disconnecting for idle. A
	// larger value is pointless (panel upgrades and node restarts end
	// sessions sooner) and unsafe: 2N must stay far from the int32
	// ceiling (2^31) the value is stored and compared against.
	UnlimitedConnIdleSeconds = 31536000
	// maxConnIdleLevel is the highest policy level the panel allocates.
	// Levels are 1..N per render; the binding constraint is Xray's SS
	// server config, whose level field is a byte (0-255).
	maxConnIdleLevel    = 255
	defaultGRPCService  = "grpc"
	defaultXHTTPMode    = "auto"
	maxExtraConfigBytes = 64 * 1024
)

var ssMethods = []string{
	"2022-blake3-aes-128-gcm",
	"2022-blake3-aes-256-gcm",
	"2022-blake3-chacha20-poly1305",
	"aes-128-gcm",
	"aes-256-gcm",
	"chacha20-ietf-poly1305",
}

var (
	shortIDPattern = regexp.MustCompile(`^[0-9a-f]{0,16}$`)
	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
)

// HasEmptyLabel reports whether host contains an empty DNS label
// (leading, trailing, or consecutive dots). T-01: `a..b` matches
// hostPattern yet can never resolve (a DNS zone cannot hold an empty
// label), so it must be rejected wherever a hostname is validated —
// the import host, the transport Host, and the SNI. Empty string is
// not an empty label here; callers handle emptiness separately.
func HasEmptyLabel(host string) bool {
	if host == "" {
		return false
	}
	for _, seg := range strings.Split(host, ".") {
		if seg == "" {
			return true
		}
	}
	return false
}

// SSMethods lists the Shadowsocks ciphers the panel offers.
func SSMethods() []string {
	out := make([]string, len(ssMethods))
	copy(out, ssMethods)
	return out
}

// Normalize validates an inbound against a kernel's capabilities, fills
// defaults and generates any missing credentials. It mutates in place so the
// caller persists exactly what will be rendered.
func Normalize(kernelType string, in *Inbound) error {
	if in == nil {
		return errors.New("入站不能为空")
	}
	info, ok := Lookup(kernelType)
	if !ok || !info.Implemented {
		return fmt.Errorf("暂不支持内核 %s", kernelType)
	}
	strategy, err := NormalizeOutboundIPStrategy(kernelType, in.OutboundIPStrategy)
	if err != nil {
		return err
	}
	in.OutboundIPStrategy = strategy

	in.Protocol = strings.ToLower(strings.TrimSpace(in.Protocol))
	proto, ok := info.protocol(in.Protocol)
	if !ok {
		return fmt.Errorf("%s 不支持协议 %s", info.Name, in.Protocol)
	}

	in.Transport.Type = strings.ToLower(strings.TrimSpace(in.Transport.Type))
	if in.Transport.Type == "" {
		in.Transport.Type = TransportTCP
	}
	tcap, ok := proto.transport(in.Transport.Type)
	if !ok {
		return fmt.Errorf("%s 协议不支持传输方式 %s", in.Protocol, in.Transport.Type)
	}

	in.Security.Type = strings.ToLower(strings.TrimSpace(in.Security.Type))
	if in.Security.Type == "" {
		in.Security.Type = tcap.Securities[0]
	}
	if !containsString(tcap.Securities, in.Security.Type) {
		return fmt.Errorf("%s + %s 不支持安全层 %s", in.Protocol, in.Transport.Type, in.Security.Type)
	}

	if in.Port < 1 || in.Port > 65535 {
		return errors.New("端口必须在 1-65535 之间")
	}
	in.Listen = strings.TrimSpace(in.Listen)
	if in.Listen == "" {
		in.Listen = "::"
	}
	if net.ParseIP(in.Listen) == nil {
		return errors.New("监听地址必须是 IP")
	}
	if in.AcceptProxyProtocol && kernelType != TypeXray {
		return errors.New("获取真实 IP（PROXY protocol）目前只支持 Xray 内核")
	}
	if in.ConnIdleSeconds < UnlimitedConnIdleSentinel || in.ConnIdleSeconds > MaxConnIdleSeconds {
		return fmt.Errorf("空闲超时须为 -1（不限）或 0（默认 %d 秒）或 1-%d 秒的整数", DefaultConnIdleSeconds, MaxConnIdleSeconds)
	}
	if in.ConnIdleSeconds != 0 && kernelType != TypeXray {
		return errors.New("空闲超时（connIdle）只支持 Xray 内核：sing-box 没有 Xray 式的空闲超时（TCP 靠 keepalive 保活、UDP 会话默认 5 分钟清理、仅 TUN 有 udp_timeout），请先切到 Xray 内核")
	}

	if err := normalizeExtra(kernelType, in); err != nil {
		return err
	}
	if err := normalizeTransport(&in.Transport); err != nil {
		return err
	}
	if err := normalizeSettings(in); err != nil {
		return err
	}
	if err := normalizeSecurity(in); err != nil {
		return err
	}
	if in.Protocol == ProtocolSnell && kernelType == TypeSingbox {
		// sing-box chains ShadowTLS to Snell internally; no loopback port.
		in.Settings.BackendPort = 0
	}
	if in.Security.CertMode == CertModeACME && kernelType != TypeSingbox {
		return errors.New("自动申请证书（ACME）目前只支持 sing-box 内核")
	}
	if in.Settings.EncryptionEnabled() && (kernelType != TypeXray || in.Protocol != ProtocolVLESS) {
		return errors.New("VLESS 加密仅支持 Xray 内核的 VLESS 入站")
	}
	if in.Protocol == ProtocolNaive && in.Security.CertMode == CertModeSelf {
		// Naive clients verify certificates and self-signed ones are
		// trivially fingerprinted, which defeats NaïveProxy.
		return errors.New("NaïveProxy 需要真实证书：请选择自动申请证书或节点上的证书文件")
	}
	return nil
}

func normalizeExtra(kernelType string, in *Inbound) error {
	raw := strings.TrimSpace(string(in.Extra))
	if raw == "" || raw == "null" || raw == "{}" {
		in.Extra = nil
		return nil
	}
	if len(raw) > maxExtraConfigBytes {
		return errors.New("高级配置过大")
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return errors.New("高级配置必须是 JSON 对象")
	}
	// F-022: Snell 的 INI 渲染根本不消费 Extra，填了只会静默无效；
	// 保存期明确拒绝，提示清空，而不是让用户以为已生效。
	if kernelType == TypeSnell {
		return errors.New("Snell 内核不支持高级配置（Extra 会被忽略），请清空高级配置")
	}
	// F-022: 受保护键在保存期就拒绝，不要等到下发渲染才暴露。
	if err := validateExtraProtectedKeys(obj); err != nil {
		return err
	}
	in.Extra = json.RawMessage(raw)
	// S4: 指定出站 IP 策略时 Extra 不得覆盖 tag/detour（会绕过策略作用域）。
	return ValidateOutboundIPExtra(kernelType, in.OutboundIPStrategy, in.Extra)
}

func normalizeTransport(t *Transport) error {
	t.Path = strings.TrimSpace(t.Path)
	t.Host = strings.TrimSpace(t.Host)
	t.ServiceName = strings.TrimSpace(t.ServiceName)
	t.Mode = strings.TrimSpace(t.Mode)
	if t.Host != "" && (!hostPattern.MatchString(t.Host) || HasEmptyLabel(t.Host)) {
		return errors.New("Host 格式错误")
	}
	switch t.Type {
	case TransportTCP, TransportQUIC:
		t.Path, t.Host, t.ServiceName, t.Mode = "", "", "", ""
	case TransportWS, TransportXHTTP:
		if t.Path == "" {
			t.Path = "/" + RealityShortID()[:8]
		}
		if !strings.HasPrefix(t.Path, "/") {
			t.Path = "/" + t.Path
		}
		t.ServiceName = ""
		if t.Type == TransportWS {
			t.Mode = ""
		} else if t.Mode == "" {
			t.Mode = defaultXHTTPMode
		} else {
			// R11X-001（R10X-002 的自建载体邻格）：XHTTP Mode 此前
			// 仅 TrimSpace——API 直写与备份恢复的大小写变体/集外值
			// 原样落库，逐字进自建 Clash 面、ShareURI 与 Xray 内
			// 核/链式配置，毒化整表/整单。与指纹格同形处置、与进
			// 口面同一合法集（ValidXHTTPMode）：小写愈合落库、集
			// 外拒绝。空值补 auto 的既有顺序与默认不变（前支）。
			t.Mode = strings.ToLower(t.Mode)
			if !ValidXHTTPMode(t.Mode) {
				return errors.New("xhttp mode 不受支持（仅 auto / packet-up / stream-up / stream-one）")
			}
		}
	case TransportGRPC:
		if t.ServiceName == "" {
			t.ServiceName = defaultGRPCService
		}
		t.Path, t.Mode = "", ""
	}
	return nil
}

func normalizeSettings(in *Inbound) error {
	s := &in.Settings
	s.UUID = strings.TrimSpace(s.UUID)
	s.Password = strings.TrimSpace(s.Password)
	s.Method = strings.TrimSpace(s.Method)
	s.Flow = strings.TrimSpace(s.Flow)
	s.EncryptionAuth = strings.TrimSpace(s.EncryptionAuth)
	s.EncryptionMode = strings.TrimSpace(s.EncryptionMode)
	if s.EncryptionEnabled() {
		if s.EncryptionAuth != EncryptionAuthX25519 && s.EncryptionAuth != EncryptionAuthMLKEM {
			return fmt.Errorf("不支持的 VLESS 加密认证方式 %s", s.EncryptionAuth)
		}
		switch s.EncryptionMode {
		case "", EncryptionModeNative, EncryptionModeXorpub, EncryptionModeRandom:
		default:
			return fmt.Errorf("不支持的 VLESS 加密外观模式 %s", s.EncryptionMode)
		}
		if s.EncryptionMode == "" {
			s.EncryptionMode = EncryptionModeNative
		}
		if s.EncryptionSecret == "" {
			secret, err := GenerateEncryptionSecret(s.EncryptionAuth)
			if err != nil {
				return err
			}
			s.EncryptionSecret = secret
		} else if err := ValidateEncryptionSecret(s.EncryptionAuth, s.EncryptionSecret); err != nil {
			return err
		}
	} else {
		s.EncryptionMode, s.EncryptionSecret = "", ""
	}
	if in.Protocol != ProtocolNaive {
		s.Username = ""
	}

	switch in.Protocol {
	case ProtocolVLESS, ProtocolVMess:
		if s.UUID == "" {
			s.UUID = NewUUID()
		}
		if !uuidPattern.MatchString(strings.ToLower(s.UUID)) {
			return errors.New("UUID 格式错误")
		}
		s.Password, s.Method = "", ""
		visionOK := in.Protocol == ProtocolVLESS && in.Transport.Type == TransportTCP && (in.Security.Type != SecurityNone || s.EncryptionEnabled())
		if s.Flow != "" && (s.Flow != FlowVision || !visionOK) {
			s.Flow = ""
		}
	case ProtocolTrojan:
		if s.Password == "" {
			s.Password = RandomPassword()
		}
		s.UUID, s.Method, s.Flow = "", "", ""
	case ProtocolShadowsocks:
		if s.Method == "" {
			s.Method = DefaultSSMethod
		}
		if !containsString(ssMethods, s.Method) {
			return fmt.Errorf("不支持的加密方式 %s", s.Method)
		}
		if s.Password == "" {
			s.Password = ShadowsocksPassword(s.Method)
		}
		if n := SS2022KeyLen(s.Method); n > 0 {
			// R4X-013: 与导入面同一四形态解码核（Std / RawStd /
			// URL / RawURL 任一解出恰 N 字节即合法）——此前这里只
			// 认 Std 单形态，误拒其余三种合法密钥。复用
			// ValidSS2022SingleKey 同时带来单段限定：自建入站密码
			// 是服务端单密钥，SIP023 冒号多段在此仍拒，不得把
			// 导入面的多段接受性带入自建面。落库保留原样，线格式
			// 由渲染侧 NormalizedSS2022Password 归一。
			if !ValidSS2022SingleKey(s.Method, s.Password) {
				return fmt.Errorf("%s 的密钥必须是 %d 字节的 base64", s.Method, n)
			}
		}
		s.UUID, s.Flow = "", ""
	case ProtocolHysteria2, ProtocolAnyTLS:
		if s.Password == "" {
			s.Password = RandomPassword()
		}
		s.UUID, s.Method, s.Flow = "", "", ""
		s.Masquerade = strings.TrimSpace(s.Masquerade)
		if in.Protocol != ProtocolHysteria2 {
			s.Masquerade = ""
		}
		if s.Masquerade != "" {
			u, err := url.Parse(s.Masquerade)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("伪装地址必须是 http(s):// 开头的网址")
			}
		}
	case ProtocolNaive:
		s.Username = strings.TrimSpace(s.Username)
		if s.Username == "" {
			s.Username = "u" + RealityShortID()[:8]
		}
		if !naiveUserPattern.MatchString(s.Username) {
			return errors.New("用户名只能包含字母、数字和 -_，长度 1-32")
		}
		if s.Password == "" {
			s.Password = RandomPassword()
		}
		if !snellPSKPattern.MatchString(s.Password) {
			return errors.New("密码只能包含字母、数字和 -_，长度 8-64")
		}
		s.UUID, s.Method, s.Flow = "", "", ""
	case ProtocolTUIC:
		if s.UUID == "" {
			s.UUID = NewUUID()
		}
		if !uuidPattern.MatchString(strings.ToLower(s.UUID)) {
			return errors.New("UUID 格式错误")
		}
		if s.Password == "" {
			s.Password = RandomPassword()
		}
		s.Method, s.Flow = "", ""
	case ProtocolSnell:
		if s.Password == "" {
			s.Password = RandomPassword()
		}
		if !snellPSKPattern.MatchString(s.Password) {
			return errors.New("Snell PSK 只能包含字母、数字和 -_，长度 8-64")
		}
		if s.SnellVersion == 0 {
			s.SnellVersion = DefaultSnellVersion
		}
		if s.SnellVersion != 4 && s.SnellVersion != 5 && s.SnellVersion != 6 {
			return errors.New("Snell 版本只支持 4、5 或 6")
		}
		if s.SnellVersion == 6 && len(s.Password) < snellV6MinPSK {
			return fmt.Errorf("Snell v6 的 PSK 至少 %d 位", snellV6MinPSK)
		}
		s.SnellMode = strings.TrimSpace(s.SnellMode)
		if s.SnellMode == "default" || s.SnellVersion != 6 {
			s.SnellMode = ""
		}
		// unsafe-raw turns encryption off; not offered.
		if s.SnellMode != "" && s.SnellMode != SnellModeUnshaped {
			return errors.New("Snell v6 流量整形只支持默认或 unshaped")
		}
		s.UUID, s.Method, s.Flow = "", "", ""
	}
	if in.Protocol == ProtocolHysteria2 {
		lo, hi, err := parseHopRange(s.PortHopping)
		if err != nil {
			return err
		}
		s.PortHopping = ""
		if lo > 0 {
			if in.Port >= lo && in.Port <= hi {
				return errors.New("端口跳跃范围不能包含入站端口本身")
			}
			s.PortHopping = fmt.Sprintf("%d-%d", lo, hi)
		}
	} else {
		s.PortHopping = ""
	}
	if in.Protocol == ProtocolSnell && in.Security.Type == SecurityShadowTLS {
		if s.BackendPort == 0 {
			s.BackendPort = randomPort(20000, 60000)
		}
		if s.BackendPort < 1024 || s.BackendPort > 65535 || s.BackendPort == in.Port {
			return errors.New("ShadowTLS 后端端口无效")
		}
	} else {
		s.BackendPort = 0
	}
	return nil
}

var naiveUserPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

var semverPattern = regexp.MustCompile(`^\d{1,4}\.\d{1,4}\.\d{1,4}$`)

var snellPSKPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// snellV6MinPSK is the shortest PSK Snell v6 accepts.
const snellV6MinPSK = 12

// SnellModeUnshaped keeps Snell v6's encryption but turns traffic shaping
// off; the zero value ("") is Snell's default shaping.
const SnellModeUnshaped = "unshaped"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func normalizeSecurity(in *Inbound) error {
	sec := &in.Security
	sec.SNI = strings.TrimSpace(sec.SNI)
	sec.Fingerprint = strings.TrimSpace(sec.Fingerprint)
	// R10X-001 自建面同根格：fingerprint 此前仅 TrimSpace——API 直
	// 写大写值时自建端点的 sing-box 面同毒（utls 原样下发）。与进
	// 口面同一合法集（ValidClientFingerprint）：小写愈合落库、集
	// 外拒绝。SecurityNone 分支整体丢弃 Security、本格不涉。
	if sec.Type != SecurityNone && sec.Fingerprint != "" {
		sec.Fingerprint = strings.ToLower(sec.Fingerprint)
		if !ValidClientFingerprint(sec.Fingerprint) {
			return errors.New("client-fingerprint 不受支持（须为 sing-box uTLS 指纹：chrome / firefox / edge / safari / 360 / qq / ios / android / random / randomized / chrome_psk）")
		}
	}
	if sec.SNI != "" && (!hostPattern.MatchString(sec.SNI) || HasEmptyLabel(sec.SNI)) {
		return errors.New("SNI 格式错误")
	}

	switch sec.Type {
	case SecurityNone:
		*sec = Security{Type: SecurityNone}
		return nil
	case SecurityShadowTLS:
		*sec = Security{Type: SecurityShadowTLS, SNI: sec.SNI, Fingerprint: sec.Fingerprint, ShadowTLSPassword: strings.TrimSpace(sec.ShadowTLSPassword)}
		if sec.SNI == "" {
			sec.SNI = DefaultRealitySNI
		}
		if sec.Fingerprint == "" {
			sec.Fingerprint = DefaultFingerprint
		}
		if sec.ShadowTLSPassword == "" {
			sec.ShadowTLSPassword = RandomPassword()
		}
		if !snellPSKPattern.MatchString(sec.ShadowTLSPassword) {
			return errors.New("ShadowTLS 密码只能包含字母、数字和 -_，长度 8-64")
		}
		return nil
	case SecurityTLS:
		sec.RealityDest, sec.RealityPrivateKey, sec.RealityPublicKey, sec.RealityShortIDs = "", "", "", nil
		sec.RealityMinClientVer = ""
		sec.ShadowTLSPassword = ""
		quic := in.Protocol == ProtocolHysteria2 || in.Protocol == ProtocolTUIC
		if quic {
			sec.ALPN = []string{"h3"}
		}
		if sec.SNI == "" && (quic || in.Protocol == ProtocolAnyTLS) {
			sec.SNI = DefaultQUICSNI
		}
		if sec.Fingerprint == "" {
			sec.Fingerprint = DefaultFingerprint
		}
		if sec.CertMode == "" {
			sec.CertMode = DefaultTLSCertMode
		}
		if sec.CertMode != CertModeACME {
			sec.ACMEEmail, sec.ACMEDNS, sec.ACMEDNSKey, sec.ACMEDNSSecret = "", "", "", ""
		}
		switch sec.CertMode {
		case CertModeSelf:
			if sec.SNI == "" {
				return errors.New("TLS 需要填写域名 (SNI)")
			}
			sec.CertFile, sec.KeyFile = "", ""
			if sec.CertPEM == "" || sec.KeyPEM == "" || !certMatchesName(sec.CertPEM, sec.SNI) {
				cert, key, err := SelfSignedCert(sec.SNI)
				if err != nil {
					return fmt.Errorf("生成自签证书失败: %v", err)
				}
				sec.CertPEM, sec.KeyPEM = cert, key
			} else if _, err := tls.X509KeyPair([]byte(sec.CertPEM), []byte(sec.KeyPEM)); err != nil {
				// F-078: certMatchesName 只保证证书域名/有效期对，不保证
				// 与私钥配对——错配对原样落库后要到节点内核启动才炸。
				// 保存期显式拦截（材料是用户主动粘贴的，不静默替换）。
				return errors.New("证书与私钥不匹配，请确认粘贴的证书与私钥为一对")
			}
		case CertModeFile:
			sec.CertPEM, sec.KeyPEM = "", ""
			sec.CertFile = strings.TrimSpace(sec.CertFile)
			sec.KeyFile = strings.TrimSpace(sec.KeyFile)
			if !strings.HasPrefix(sec.CertFile, "/") || !strings.HasPrefix(sec.KeyFile, "/") {
				return errors.New("证书和私钥需要填写节点上的绝对路径")
			}
		case CertModeACME:
			sec.CertPEM, sec.KeyPEM, sec.CertFile, sec.KeyFile = "", "", "", ""
			return normalizeACME(sec)
		default:
			return fmt.Errorf("未知证书模式 %s", sec.CertMode)
		}
		return nil
	case SecurityReality:
		sec.CertMode, sec.CertPEM, sec.KeyPEM, sec.CertFile, sec.KeyFile = "", "", "", "", ""
		sec.ACMEEmail, sec.ACMEDNS, sec.ACMEDNSKey, sec.ACMEDNSSecret = "", "", "", ""
		sec.ShadowTLSPassword = ""
		if sec.SNI == "" {
			sec.SNI = DefaultRealitySNI
		}
		if sec.Fingerprint == "" {
			sec.Fingerprint = DefaultFingerprint
		}
		sec.RealityDest = strings.TrimSpace(sec.RealityDest)
		if sec.RealityDest == "" {
			sec.RealityDest = sec.SNI + ":443"
		}
		if host, port, err := net.SplitHostPort(sec.RealityDest); err != nil || host == "" {
			return errors.New("Reality 目标地址格式应为 host:port")
		} else if HasEmptyLabel(host) {
			// L1-F-01：RealityDest host 段此前只走 SplitHostPort +
			// 端口检查，空标签（`a..b:443`）原值落库；与同函数 SNI
			//（:648）同口径拒。IP 字面量无空标签，不受影响。
			return errors.New("Reality 目标地址不正确")
		} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return errors.New("Reality 目标端口错误")
		}
		sec.RealityPrivateKey = strings.TrimSpace(sec.RealityPrivateKey)
		if sec.RealityPrivateKey == "" {
			priv, pub, err := RealityKeyPair()
			if err != nil {
				return fmt.Errorf("生成 Reality 密钥失败: %v", err)
			}
			sec.RealityPrivateKey, sec.RealityPublicKey = priv, pub
		} else {
			pub, err := RealityPublicKey(sec.RealityPrivateKey)
			if err != nil {
				return err
			}
			sec.RealityPublicKey = pub
		}
		ids := make([]string, 0, len(sec.RealityShortIDs))
		for _, id := range sec.RealityShortIDs {
			id = strings.ToLower(strings.TrimSpace(id))
			if !shortIDPattern.MatchString(id) || len(id)%2 != 0 {
				return errors.New("Reality shortId 必须是偶数位十六进制（最多 16 位）")
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			ids = []string{RealityShortID()}
		}
		sec.RealityShortIDs = ids
		sec.RealityMinClientVer = strings.TrimSpace(sec.RealityMinClientVer)
		if sec.RealityMinClientVer == "" {
			sec.RealityMinClientVer = DefaultRealityMinClientVer
		}
		if !semverPattern.MatchString(sec.RealityMinClientVer) {
			return errors.New("最低客户端版本格式应为 x.y.z，例如 1.0.0")
		}
		return nil
	}
	return fmt.Errorf("未知安全层 %s", sec.Type)
}

var acmeEmailPattern = regexp.MustCompile(`^[^@\s"]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)

func normalizeACME(sec *Security) error {
	if sec.SNI == "" || net.ParseIP(sec.SNI) != nil || !strings.Contains(sec.SNI, ".") || sec.SNI == DefaultQUICSNI {
		return errors.New("自动申请证书需要填写解析到该节点的域名")
	}
	sec.ACMEEmail = strings.TrimSpace(sec.ACMEEmail)
	if sec.ACMEEmail != "" {
		// T-01 同族：acmeEmailPattern 的域名段 `[A-Za-z0-9.-]+` 放行
		// 空标签（`x@a..b.com`），域名按 `.` 分段判空标签，与主机名
		// 同口径；文案复用既有邮箱校验。
		_, domain, _ := strings.Cut(sec.ACMEEmail, "@")
		if !acmeEmailPattern.MatchString(sec.ACMEEmail) || HasEmptyLabel(domain) {
			return errors.New("ACME 邮箱格式错误")
		}
	}
	sec.ACMEDNS = strings.ToLower(strings.TrimSpace(sec.ACMEDNS))
	sec.ACMEDNSKey = strings.TrimSpace(sec.ACMEDNSKey)
	sec.ACMEDNSSecret = strings.TrimSpace(sec.ACMEDNSSecret)
	switch sec.ACMEDNS {
	case "":
		sec.ACMEDNSKey, sec.ACMEDNSSecret = "", ""
	case ACMEDNSCloudflare:
		sec.ACMEDNSSecret = ""
		if sec.ACMEDNSKey == "" {
			return errors.New("请填写 Cloudflare API Token")
		}
	case ACMEDNSAliDNS:
		if sec.ACMEDNSKey == "" || sec.ACMEDNSSecret == "" {
			return errors.New("请填写阿里云 AccessKey ID 和 Secret")
		}
	default:
		return fmt.Errorf("不支持的 DNS 服务商 %s", sec.ACMEDNS)
	}
	return nil
}

// Regenerate replaces every secret of an inbound while keeping its shape.
func Regenerate(kernelType string, in *Inbound) error {
	in.Settings.UUID = ""
	in.Settings.Password = ""
	in.Settings.EncryptionSecret = ""
	in.Security.RealityPrivateKey = ""
	in.Security.RealityPublicKey = ""
	in.Security.RealityShortIDs = nil
	in.Security.CertPEM = ""
	in.Security.KeyPEM = ""
	in.Security.ShadowTLSPassword = ""
	return Normalize(kernelType, in)
}

const maxAllowEntries = 256

// ParseAllowList validates a comma / whitespace separated list of IPs and
// CIDRs and returns the normalized entries (bare IPs become /32 or /128).
func ParseAllowList(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})
	if len(fields) > maxAllowEntries {
		return nil, fmt.Errorf("白名单最多 %d 条", maxAllowEntries)
	}
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		cidr, err := NormalizeAllowEntry(f)
		if err != nil {
			return nil, err
		}
		if !seen[cidr] {
			seen[cidr] = true
			out = append(out, cidr)
		}
	}
	return out, nil
}

// NormalizeAllowEntry turns an IP or CIDR into canonical CIDR form.
func NormalizeAllowEntry(entry string) (string, error) {
	entry = strings.TrimSpace(entry)
	if ip := net.ParseIP(entry); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String() + "/32", nil
		}
		return ip.String() + "/128", nil
	}
	_, ipnet, err := net.ParseCIDR(entry)
	if err != nil {
		return "", fmt.Errorf("白名单条目 %q 不是合法的 IP 或网段", entry)
	}
	return ipnet.String(), nil
}
