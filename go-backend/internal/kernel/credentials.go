package kernel

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func NewUUID() string {
	return uuid.NewString()
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return b
}

// RandomPassword returns a URL-safe secret suitable for Trojan / legacy SS.
func RandomPassword() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(24))
}

// SS2022KeyLen returns the PSK length a Shadowsocks 2022 method requires, or 0
// for legacy AEAD methods that accept any password.
//
// R4X-001: the lookup is case-insensitive. This function is the single
// source of truth for every SS2022 guard (import validation, the
// sing-box renderer's skip guard, password normalization); when it
// matched only the lowercase literals, a case variant of a known
// cipher (2022-BLAKE3-AES-128-GCM) made the whole chain blind —
// validation passed any key and the renderer emitted the variant
// verbatim, which both sing-box and mihomo reject with
// "unknown method", poisoning the whole profile. Import still stores
// the canonical lowercase name (validateProxyCredentials lowercases
// the cipher) and rendering lowercases stock rows (Proxy.forEndpoint);
// this insensitivity is the predicate-level backstop so no present or
// future call site can re-open the bypass by forgetting to normalize.
func SS2022KeyLen(method string) int {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "2022-blake3-aes-128-gcm":
		return 16
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32
	default:
		return 0
	}
}

func ShadowsocksPassword(method string) string {
	if n := SS2022KeyLen(method); n > 0 {
		return base64.StdEncoding.EncodeToString(randomBytes(n))
	}
	return RandomPassword()
}

// decodeSS2022Segment reports whether seg decodes to exactly n bytes
// under any of the four base64 shapes an SS2022 key appears in in the
// wild: Std with padding, RawStd, URL with padding, RawURL. The loose
// decoder (decodeBase64Loose) only tries the two Raw variants and
// would reject padded standard keys, so it must not be reused here —
// mis-rejecting legitimate airport keys is this check's worst failure
// mode (R3X-008).
func decodeSS2022Segment(seg string, n int) bool {
	_, ok := decodeSS2022SegmentBytes(seg, n)
	return ok
}

// decodeSS2022SegmentBytes is decodeSS2022Segment returning the decoded
// key bytes, so renderers can re-encode them in canonical form.
func decodeSS2022SegmentBytes(seg string, n int) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(seg); err == nil && len(b) == n {
			return b, true
		}
	}
	return nil, false
}

// NormalizedSS2022Password returns the canonical rendering form of an
// SS2022 password: a single-segment key that decodes (in any of the
// four accepted base64 shapes) to the cipher's key length is re-encoded
// with padded StdEncoding — the only shape both sing-box and mihomo
// accept (release-gate item 3: non-canonical shapes rendered verbatim
// made both clients fail the whole profile). The decoded bytes are
// unchanged, so this is a wire-format normalization, never a key
// change, and the canonical shape is a fixed point.
//
// Everything else is returned verbatim, by design:
//   - legacy AEAD and unknown ciphers: no key format to normalize;
//   - SIP023 colon-separated multi-segment passwords: kept verbatim
//     here; the Clash / mihomo and sing-box faces normalize them,
//     segment by segment, via NormalizedSS2022PasswordSegments
//     (binary-verified decoder behavior on both faces);
//   - undecodable stock garbage: each renderer's existing policy
//     decides (the sing-box proxy renderer skips the node, the other
//     formats emit it verbatim), never a new behavior invented here.
func NormalizedSS2022Password(method, password string) string {
	n := SS2022KeyLen(method)
	if n == 0 {
		return password
	}
	pw := strings.TrimSpace(password)
	if strings.Contains(pw, ":") {
		return password
	}
	b, ok := decodeSS2022SegmentBytes(pw, n)
	if !ok {
		return password
	}
	return base64.StdEncoding.EncodeToString(b)
}

// NormalizedSS2022PasswordSegments is NormalizedSS2022Password for the
// Clash / mihomo and sing-box rendering faces, extended to SIP023
// multi-segment passwords segment by segment. GATE-F1 binary evidence
// (official mihomo v1.19.33 -t, sing-box 1.14.3 check): both clients
// split a multi-segment password on ':' and decode every segment with
// the padded Std decoder — Std segments pass, RawURL segments fail the
// whole profile. Re-encoding each segment as padded Std therefore
// preserves the decoded key bytes (the SIP023 semantics) exactly,
// while the verbatim non-canonical segments poison the profile.
// (R4-SIP023-R: the sing-box face previously skipped multi-segment
// nodes on the strength of this segment decoder being unverified;
// D §1.2 probes plus the R4 batch-2 retest on sing-box 1.11.x/1.12.x
// verified it, so proxySingbox now renders through this helper too.)
// A password with any undecodable segment is returned verbatim (the
// face's existing garbage policy), as is a single-segment password's
// handling via NormalizedSS2022Password. Other faces keep multi-
// segment passwords verbatim: their clients' segment decoders are
// not binary-verified.
func NormalizedSS2022PasswordSegments(method, password string) string {
	n := SS2022KeyLen(method)
	if n == 0 {
		return password
	}
	pw := strings.TrimSpace(password)
	if !strings.Contains(pw, ":") {
		return NormalizedSS2022Password(method, password)
	}
	segs := strings.Split(pw, ":")
	out := make([]string, len(segs))
	for i, seg := range segs {
		b, ok := decodeSS2022SegmentBytes(seg, n)
		if !ok {
			return password
		}
		out[i] = base64.StdEncoding.EncodeToString(b)
	}
	return strings.Join(out, ":")
}

// ValidateSS2022Password checks a Shadowsocks password against the
// SS2022 key format when method is an SS2022 cipher (required length
// from SS2022KeyLen, the same source of truth Normalize uses). Legacy
// AEAD methods and unknown cipher names accept any password and are
// never checked. SIP023 (EIH) passwords are colon-separated key
// segments; every segment must independently decode to the key
// length. This is the single validation helper shared by the import
// parser and the legacy external-inbound edit path.
func ValidateSS2022Password(method, password string) error {
	n := SS2022KeyLen(method)
	if n == 0 {
		return nil
	}
	for _, seg := range strings.Split(strings.TrimSpace(password), ":") {
		if !decodeSS2022Segment(seg, n) {
			return fmt.Errorf("SS2022 密钥必须是 %d 字节的 base64", n)
		}
	}
	return nil
}

// ValidSS2022SingleKey reports whether password is a single valid
// SS2022 key for method. SIP023 multi-segment passwords report false;
// this single-key predicate is deliberately NOT relaxed for them —
// faces that carry multi-segment passwords (Clash / sing-box) gate on
// the per-segment predicate ValidateSS2022Password instead
// (R4-SIP023-R). Non-SS2022 methods report true: nothing to check.
func ValidSS2022SingleKey(method, password string) bool {
	n := SS2022KeyLen(method)
	if n == 0 {
		return true
	}
	pw := strings.TrimSpace(password)
	if strings.Contains(pw, ":") {
		return false
	}
	return decodeSS2022Segment(pw, n)
}

// SSUserPSK derives a share subscription's Shadowsocks 2022 user PSK
// (uPSK) from its subscription password. The derivation is deterministic
// and storage-free: kernel configs, subscription rendering and client
// links all compute the same value from the same inputs, and rotating
// the subscription password rotates the uPSK with it. It is
// domain-separated per subscription and method and truncated to the
// method's key length, so the result is a well-formed 2022 PSK. A uPSK
// alone is not a usable credential — clients present it combined with
// the companion inbound's identity PSK (see ForUser).
func SSUserPSK(subPassword string, subID int64, method string) string {
	n := SS2022KeyLen(method)
	if n == 0 || subPassword == "" || subID <= 0 {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(subPassword))
	fmt.Fprintf(mac, "flmx-ss-upsk|%d|%s", subID, method)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)[:n])
}

// RealityKeyPair returns an X25519 key pair in the raw-URL base64 form used by
// Xray (`xray x25519`) and sing-box.
func RealityKeyPair() (privateKey, publicKey string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.Bytes()),
		base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// RealityPublicKey derives the public key from a stored private key so a
// user-supplied private key still produces correct share links.
func RealityPublicKey(privateKey string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(privateKey)
	if err != nil {
		return "", fmt.Errorf("Reality 私钥格式错误")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("Reality 私钥格式错误")
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func RealityShortID() string {
	return hex.EncodeToString(randomBytes(8))
}

// SelfSignedCert issues a long-lived ECDSA certificate for serverName. Clients
// must skip verification (share links carry allowInsecure).
func SelfSignedCert(serverName string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: serverName},
		DNSNames:              []string{serverName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}

func certMatchesName(certPEM, name string) bool {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	return cert.VerifyHostname(name) == nil && time.Now().Before(cert.NotAfter)
}

// randomPort returns a port in [lo, hi).
func randomPort(lo, hi int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo)))
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return lo + int(n.Int64())
}

// ---- R5 批 1（R5X-001/002/003）枚举凭据谓词 ----
//
// 有限枚举 / 格式域字段的唯一真源：导入期 validateProxyCredentials 与
// 存量渲染守卫（proxySingbox / proxyClash）共用同一谓词，不得逐面另
// 写判定。合法集逐值经 sing-box 1.14.3 `check` 与 mihomo v1.19.33 `-t`
// 双二进制核对（批报告逐格矩阵）：
//   - vmess cipher：双客户端共同接受集 {auto, aes-128-gcm,
//     chacha20-poly1305, none, zero}（aes-128-ctr / chacha20 等双双
//     FATAL）；大小写变体按 ss cipher 先例（R4X-001）小写愈合。
//   - hy2 obfs：{"", salamander, gecko}——gecko 为 sing-box 1.14 与
//     mihomo 现行共同接受集成员（双二进制实测 exit 0），候选集原列
//     {"", salamander}，逐值核对后扩入，防误拒现行机场形态。
//   - vless flow：{"", xtls-rprx-vision}（sing-box 严口径；
//     xtls-rprx-vision-udp443 等 mihomo 容忍形态在 sing-box FATAL）。
//   - tuic congestion-controller：{"", bbr, cubic, new_reno}——下划线
//     形态经 sing-box 实测确认（new-reno 连字符形态 sing-box FATAL、
//     mihomo 容忍，以严口径为准）。udp-relay-mode 双二进制 check 均
//     不校验、拨号期未验证（R5 §3 第 6 点），不在校验集内。
//   - snell obfs mode：{"", http, tls, shadow-tls, restls, jls}——
//     mihomo 官方文档与 v1.19.33 实测口径；历史拼写 "shadowtls" 被
//     mihomo 本体拒绝（obfs mode error），作别名在导入期愈合为
//     "shadow-tls" 落库。
//   - hy2 ports：逗号分隔的端口 / 端口段清单，每项端口须在
//     1–65535 内。面板渲染产物实测：abc 双客户端 FATAL、80000-90000
//     在 sing-box FATAL、单端口 "443" 在 sing-box server_ports 形态
//     下 FATAL（渲染侧改按项拆分并 p→p:p 归一，见 proxySingbox）。

var vmessCiphers = map[string]bool{
	"auto": true, "aes-128-gcm": true, "chacha20-poly1305": true,
	"none": true, "zero": true,
}

// ValidVMessCipher reports whether cipher is in the dual-client VMess
// security set. Empty is allowed: every parser defaults it to "auto"
// and the sing-box renderer falls back to "auto" as well.
func ValidVMessCipher(cipher string) bool {
	if cipher == "" {
		return true
	}
	return vmessCiphers[strings.ToLower(cipher)]
}

var hy2ObfsTypes = map[string]bool{"salamander": true, "gecko": true}

// ValidHy2Obfs reports whether obfs is a Hysteria2 obfs type both
// clients accept ("" = no obfs).
func ValidHy2Obfs(obfs string) bool {
	if obfs == "" {
		return true
	}
	return hy2ObfsTypes[strings.ToLower(obfs)]
}

// ValidVLESSFlow reports whether flow is in the sing-box-accepted
// VLESS flow set ("" = no flow).
func ValidVLESSFlow(flow string) bool {
	switch strings.ToLower(flow) {
	case "", "xtls-rprx-vision":
		return true
	}
	return false
}

var tuicCongestionControllers = map[string]bool{
	"bbr": true, "cubic": true, "new_reno": true,
}

// ValidTUICCongestionController reports whether cc is in the
// sing-box-accepted TUIC congestion-controller set ("" = client
// default).
func ValidTUICCongestionController(cc string) bool {
	if cc == "" {
		return true
	}
	return tuicCongestionControllers[strings.ToLower(cc)]
}

// CanonicalSnellObfsMode maps the historical "shadowtls" spelling to
// mihomo's canonical "shadow-tls" (the only spelling mihomo accepts).
func CanonicalSnellObfsMode(mode string) string {
	if strings.EqualFold(mode, "shadowtls") {
		return "shadow-tls"
	}
	return strings.ToLower(mode)
}

var snellObfsModes = map[string]bool{
	"http": true, "tls": true, "shadow-tls": true, "restls": true, "jls": true,
}

// ValidSnellObfsMode reports whether mode is a mihomo-accepted Snell
// obfs mode ("" = no obfs). The "shadowtls" alias is accepted here
// because import/render canonicalize it before it reaches a client.
func ValidSnellObfsMode(mode string) bool {
	if mode == "" {
		return true
	}
	return snellObfsModes[CanonicalSnellObfsMode(mode)]
}

// ValidHy2Ports reports whether ports is a well-formed Hysteria2
// port-hopping list: comma-separated ports or port ranges, every
// port within 1–65535. "" means no hopping.
func ValidHy2Ports(ports string) bool {
	if strings.TrimSpace(ports) == "" {
		return true
	}
	for _, item := range strings.Split(ports, ",") {
		item = strings.TrimSpace(item)
		lo, hi, found := strings.Cut(item, "-")
		if !found {
			hi = lo
		}
		for _, s := range []string{lo, hi} {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n < 1 || n > 65535 {
				return false
			}
		}
	}
	return true
}

// ValidVLESSEncryption checks a client-side VLESS encryption string
// against the Xray ENC grammar (vless_encryption.go):
//
//	mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>[.<填充链>].<认证段>
//
// The padding chain is zero or more dot-separated groups of
// dash-separated decimal numbers: the built face emits
// "100-111-1111.75-0-111.50-0-3333", while the Xray `vlessenc`
// default emits no padding chain at all (a 4-segment string whose
// last segment is the authentication segment — R8X-002); the
// authentication segment must
// be RawURL base64 decoding to 32 bytes (x25519 public key) or 1184
// bytes (ML-KEM-768 encapsulation key) — exactly what
// Settings.EncryptionString produces. "" and "none" are valid (no
// encryption). The server-side decryption form (ticket segment such
// as "600s", secret tail) is NOT a client string and is rejected:
// mihomo rejects it the same way.
func ValidVLESSEncryption(enc string) bool {
	if enc == "" || enc == "none" {
		return true
	}
	parts := strings.Split(enc, ".")
	if len(parts) < 4 || parts[0] != "mlkem768x25519plus" {
		return false
	}
	switch parts[1] {
	case EncryptionModeNative, EncryptionModeXorpub, EncryptionModeRandom:
	default:
		return false
	}
	if parts[2] != "0rtt" && parts[2] != "1rtt" {
		return false
	}
	for _, group := range parts[3 : len(parts)-1] {
		if group == "" {
			return false
		}
		for _, num := range strings.Split(group, "-") {
			if _, err := strconv.Atoi(num); err != nil {
				return false
			}
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[len(parts)-1])
	if err != nil {
		return false
	}
	return len(raw) == 32 || len(raw) == 1184
}

// ---- R6 批 1（R6X-001/002/003）SS cipher 合法集 ----
//
// 合法集唯一口径 = 本批施工前重跑的 30 方法名逐值探针（面板渲染产物 ×
// sing-box 1.14.3 `check` / mihomo v1.19.33 `-t`，全表 + 原始日志存档
// audit-out-132/r6/R6-FIX-B1-ss-method-probe.log，R6X-024 关闭前置）：
// 双客户端共同接受 18、仅 mihomo 接受 1（chacha20）（R6 时点）、双双拒绝 11。
// R5-FIX-B1 格 2 的旧摘要（25/4）已经 verdicts §1.4 作废，不得引用。
// R8X-003 扩围：候选池外 5 值经三二进制复核同为 mihomo-only，并入
// 单面集（单面集共 6 值），见 ssCiphersMihomoOnly 注。

var ssCiphersCommon = map[string]bool{
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true,
	"2022-blake3-chacha20-poly1305": true,
	"aes-128-gcm":                   true, "aes-192-gcm": true, "aes-256-gcm": true,
	"chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true,
	"aes-128-cfb": true, "aes-192-cfb": true, "aes-256-cfb": true,
	"aes-128-ctr": true, "aes-192-ctr": true, "aes-256-ctr": true,
	"chacha20-ietf": true, "rc4-md5": true, "none": true, "xchacha20": true,
}

// ssCiphersMihomoOnly 是单面集：仅 mihomo 系（Clash/Stash/Shadowrocket）
// 接受，sing-box 以 unknown method FATAL。进口不拒（mihomo 系合法存量
// 方法）；渲染面分发见 validImportedProxyFields 的面参数。
// R8X-003 补收 5 值（aes-128-gcm-siv、aes-256-gcm-siv、
// chacha8-ietf-poly1305、aes-128-ccm、aes-256-ccm）：三二进制签名逐值
// 与 chacha20 逐字同型（sing-box exit 1 / mihomo exit 0 / Xray
// exit 23，R8-D-sscand-probe.log + 批 2 复跑
// R8-FIX-B2-binary-probe-before.log），R6 候选池 30 名未含此 5 值、
// 属合法集枚举漏收，与 chacha20 同款分面待遇。
var ssCiphersMihomoOnly = map[string]bool{
	"chacha20":        true,
	"aes-128-gcm-siv": true, "aes-256-gcm-siv": true,
	"chacha8-ietf-poly1305": true,
	"aes-128-ccm":           true, "aes-256-ccm": true,
}

// ssCiphersXrayChain 是 Xray 链式面的子集（R7X-001）：共同集 18 值中
// 只有这 8 值被 Xray 本体接受，其余 10 值（aes-192-gcm、aes-128/192/
// 256-cfb、aes-128/192/256-ctr、chacha20-ietf、rc4-md5、xchacha20）
// 以 unknown cipher method exit 23 整单拒绝。子集写死依据 = 本批施工
// 前置硬门重测（Xray 25.6.8 fbae89d 逐值 `run -test`，原始日志
// audit-out-132/r7/R7-FIX-B1-xray-ss-reprobe.log，与 R7 审计轮 D/B/L
// 三域探针的 8/10 分界逐值一致、无漂移）。本子集只约束 Xray 链式面
// （validImportedProxyFields 的 ssFaceXrayChain 档）：订阅面不存在
// Xray 客户端格式、口径仍由双客户端共同集决定，sing-box 链式面接受
// 全 18，均不得以本子集设门（防过修）。
var ssCiphersXrayChain = map[string]bool{
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true,
	"2022-blake3-chacha20-poly1305": true,
	"aes-128-gcm":                   true, "aes-256-gcm": true,
	"chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true,
	"none": true,
}

// ValidSSCipherForXrayChain reports whether cipher is in the Xray
// chain-face subset above, case-insensitively (same normalization
// backstop as ValidSSCipher).
func ValidSSCipherForXrayChain(cipher string) bool {
	return ssCiphersXrayChain[strings.ToLower(strings.TrimSpace(cipher))]
}

// ValidSSCipher reports whether cipher is in the SS legal set（共同集
// 18 ∪ 单面集 6 值），大小写不敏感（与 SS2022KeyLen 同口径：
// 进口落库已小写愈合、存量渲染经 forEndpoint 小写愈合，本谓词的不
// 敏感是防调用点漏规范化的兜底）。集外一律 false（含空串与
// chacha20-poly1305 等近名——双二进制逐值实测双拒）。
func ValidSSCipher(cipher string) bool {
	c := strings.ToLower(strings.TrimSpace(cipher))
	return ssCiphersCommon[c] || ssCiphersMihomoOnly[c]
}

// ssCipherMihomoOnly reports whether cipher is in the single-face
// (mihomo-only) set, case-insensitively.
func ssCipherMihomoOnly(cipher string) bool {
	return ssCiphersMihomoOnly[strings.ToLower(strings.TrimSpace(cipher))]
}

// 渲染面标识（validImportedProxyFields 的面参数）：单面集方法
// 只在 mihomo 面（Clash 系，经 proxyClash 同门含 Stash/Shadowrocket）
// 放行，其余面（sing-box、Xray 链式）整节点略过/拒绝——Xray 25.6.8
// 实测 unknown cipher method exit 23，非 mihomo 面不得放行。
const (
	ssFaceSingbox = "singbox"
	ssFaceClash   = "clash"
	ssFaceOther   = "other"
	// ssFaceXrayChain 是 Xray 链式面档（R7X-001）：在 ssFaceOther 的
	// 全部口径之上再加 Xray 本体 SS 子集门（ssCiphersXrayChain）。
	ssFaceXrayChain = "xray-chain"
)

// validImportedProxyFields is the shared stock/import predicate over
// every field that entered the R5 batch-1 validation set, extended by
// R6 batch 1 with the SS格 (cipher 合法集 + SS2022 key 逐段谓词——
// R6X-001/002 收口：SS2022 key 自此入统一谓词，Clash 面不再分裂）。
// It reports whether a Proxy's enum/format fields are all render-safe
// for the given face: import rejects per node with it, and the render
// faces skip whole stock nodes failing it. Reality is checked through
// validateRealityOpts (the R4X-002 import predicate itself, per
// verdicts §3.4). TUIC udp-relay-mode is deliberately absent:
// unverified (see above).
func validImportedProxyFields(p Proxy, face string) bool {
	if err := validateRealityOpts(p); err != nil {
		return false
	}
	switch p.Type() {
	case "ss":
		if !ValidSSCipher(p.str("cipher")) {
			return false
		}
		if face != ssFaceClash && ssCipherMihomoOnly(p.str("cipher")) {
			return false
		}
		if face == ssFaceXrayChain && !ValidSSCipherForXrayChain(p.str("cipher")) {
			return false
		}
		return ValidateSS2022Password(p.str("cipher"), p.str("password")) == nil
	case "vmess":
		return ValidVMessCipher(p.str("cipher"))
	case "vless":
		return ValidVLESSFlow(p.str("flow")) && ValidVLESSEncryption(p.str("encryption"))
	case "hysteria2":
		return ValidHy2Obfs(p.str("obfs")) && ValidHy2Ports(p.str("ports"))
	case "tuic":
		return ValidTUICCongestionController(p.str("congestion-controller"))
	case "snell":
		return ValidSnellObfsMode(p.sub("obfs-opts").str("mode"))
	}
	return true
}
