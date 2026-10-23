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
