package kernel

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
//	mlkem768x25519plus.<native|xorpub|random>.<0rtt|1rtt>.<填充链>.<认证段>
//
// The padding chain is one or more dot-separated groups of
// dash-separated decimal numbers (the built face emits
// "100-111-1111.75-0-111.50-0-3333"); the authentication segment must
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
	if len(parts) < 5 || parts[0] != "mlkem768x25519plus" {
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

// validImportedProxyFields is the shared stock/import predicate over
// every field that entered the R5 batch-1 validation set. It reports
// whether a Proxy's enum/format fields are all render-safe for the
// strict faces (sing-box / mihomo): import rejects per node with it,
// and proxySingbox / proxyClash skip whole stock nodes failing it.
// Reality is checked through validateRealityOpts (the R4X-002 import
// predicate itself, per verdicts §3.4) and SS2022 keys keep their
// existing per-face policies, so neither is duplicated here. TUIC
// udp-relay-mode is deliberately absent: unverified (see above).
func validImportedProxyFields(p Proxy) bool {
	if err := validateRealityOpts(p); err != nil {
		return false
	}
	switch p.Type() {
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
