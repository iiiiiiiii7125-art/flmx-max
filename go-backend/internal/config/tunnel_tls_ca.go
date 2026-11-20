package config

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// R18-F1-01 (D-3): the panel's per-install tunnel TLS certificate
// authority.
//
// The panel is the trust root for gost tunnel hops: it owns one CA per
// installation and issues one certificate + private key per node. The
// CA lives in two files next to the panel database (same <DBPath
// 同级> convention as jwt_secret, same atomic tmp+rename publish):
//
//   - tunnel_tls_ca.crt (0644, public)
//   - tunnel_tls_ca.key (0600, private — never logged, never shipped
//     anywhere except into per-node signatures)
//
// Lifecycle rules (fail-closed, never silently regenerate):
//   - Both files present → load and reuse them. A restart never mints
//     a new CA: regenerating would orphan every issued node
//     certificate and flip all verified tunnels back to legacy (or
//     break them outright).
//   - Neither present → create once (ECDSA P-256, 10 years).
//   - Key present, cert missing → re-derive the self-signed cert from
//     the existing key (same public key, so issued node certificates
//     keep verifying).
//   - Cert present, key missing → hard error. The panel cannot sign
//     without the key, and minting a fresh CA would silently orphan
//     issued node certificates. The operator must restore the key
//     from backup; the panel keeps running with legacy (unverified)
//     tunnel rendering in the meantime — it never breaks a running
//     chain to force the issue.

const (
	tunnelTLSCACertFileName = "tunnel_tls_ca.crt"
	tunnelTLSKeyFileName    = "tunnel_tls_ca.key"
)

// TunnelTLSCA is a loaded (or freshly created) per-install tunnel CA.
type TunnelTLSCA struct {
	CertPEM []byte
	KeyPEM  []byte
	Cert    *x509.Certificate
	Key     crypto.Signer
}

// TunnelTLSNodeServerName returns the stable node identity a tunnel
// certificate is bound to: node-<id>.flmx.internal. The panel renders
// this as the dialer's TLS serverName and the agent verifies the
// peer certificate against it, so verification does not depend on
// node IP addresses (which change across re-deploys and NAT).
func TunnelTLSNodeServerName(nodeID int64) string {
	return fmt.Sprintf("node-%d.flmx.internal", nodeID)
}

func tunnelTLSCAFilePaths(dir string) (certPath, keyPath string) {
	return filepath.Join(dir, tunnelTLSCACertFileName), filepath.Join(dir, tunnelTLSKeyFileName)
}

func parseTunnelTLSCA(certPEM, keyPEM []byte) (*TunnelTLSCA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("CA 证书 PEM 无法解析")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA 证书解析失败: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("CA 证书不是 CA 证书")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("CA 私钥 PEM 无法解析")
	}
	priv, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA 私钥解析失败: %w", err)
	}
	if !publicKeysEqual(cert.PublicKey, &priv.PublicKey) {
		return nil, errors.New("CA 证书与私钥不匹配")
	}
	if time.Now().Add(24 * time.Hour).After(cert.NotAfter) {
		return nil, errors.New("CA 证书已过期或即将过期")
	}
	return &TunnelTLSCA{CertPEM: certPEM, KeyPEM: keyPEM, Cert: cert, Key: priv}, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	ak, ok := a.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	bk, ok := b.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	return ak.Curve == bk.Curve && ak.X.Cmp(bk.X) == 0 && ak.Y.Cmp(bk.Y) == 0
}

// createTunnelTLSCA mints a fresh CA key + self-signed certificate.
func createTunnelTLSCA(now time.Time) (*TunnelTLSCA, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 密钥: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "FLMX Tunnel CA", Organization: []string{"FLMX"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("签发 CA 证书: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 证书: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("编码 CA 私钥: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return &TunnelTLSCA{CertPEM: certPEM, KeyPEM: keyPEM, Cert: cert, Key: priv}, nil
}

// deriveTunnelTLSCACert re-issues the self-signed CA certificate for
// an existing CA key (cert file lost). The public key is unchanged, so
// node certificates issued under the old cert keep verifying.
func deriveTunnelTLSCACert(priv *ecdsa.PrivateKey, now time.Time) (*TunnelTLSCA, error) {
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "FLMX Tunnel CA", Organization: []string{"FLMX"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("重签 CA 证书: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 证书: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("编码 CA 私钥: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return &TunnelTLSCA{CertPEM: certPEM, KeyPEM: keyPEM, Cert: cert, Key: priv}, nil
}

// writeFileAtomic0600 publishes a file atomically (tmp + rename) with
// 0600 permissions, mirroring EnsureJWTSecret.
func writeFileAtomic0600(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建目录 %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入 %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("设置 %s 权限: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("发布 %s: %w", path, err)
	}
	return nil
}

// LoadOrCreateTunnelTLSCA loads the per-install tunnel CA from dir, or
// creates it once when neither file exists. See the lifecycle rules in
// the package header: it never silently regenerates a CA whose key is
// lost, and a restart always reuses the existing CA.
func LoadOrCreateTunnelTLSCA(dir string) (*TunnelTLSCA, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("隧道 TLS CA 目录为空")
	}
	certPath, keyPath := tunnelTLSCAFilePaths(dir)
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	certMissing := errors.Is(certErr, os.ErrNotExist)
	keyMissing := errors.Is(keyErr, os.ErrNotExist)
	if certErr != nil && !certMissing {
		return nil, fmt.Errorf("读取 %s: %w", certPath, certErr)
	}
	if keyErr != nil && !keyMissing {
		return nil, fmt.Errorf("读取 %s: %w", keyPath, keyErr)
	}
	switch {
	case !certMissing && !keyMissing:
		ca, err := parseTunnelTLSCA(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("加载隧道 TLS CA（%s）: %w", dir, err)
		}
		log.Printf("隧道 TLS CA 来源: 读到既有 CA（文件 %s）", certPath)
		return ca, nil
	case certMissing && keyMissing:
		ca, err := createTunnelTLSCA(time.Now())
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomic0600(keyPath, ca.KeyPEM); err != nil {
			return nil, err
		}
		if err := os.Chmod(keyPath, 0o600); err != nil {
			return nil, fmt.Errorf("设置 %s 权限: %w", keyPath, err)
		}
		// The certificate is public material; 0644 keeps it readable
		// by the panel's own tooling without weakening anything (the
		// key file carries the 0600 protection).
		if err := os.WriteFile(certPath+".tmp", ca.CertPEM, 0o644); err != nil {
			return nil, fmt.Errorf("写入 %s: %w", certPath+".tmp", err)
		}
		if err := os.Rename(certPath+".tmp", certPath); err != nil {
			_ = os.Remove(certPath + ".tmp")
			return nil, fmt.Errorf("发布 %s: %w", certPath, err)
		}
		log.Printf("隧道 TLS CA 来源: 新生成 CA（文件 %s）", certPath)
		return ca, nil
	case keyMissing:
		// Cert without key: fail closed. Minting a fresh CA here
		// would orphan every node certificate already issued under
		// the lost key. The operator restores the key from backup;
		// until then tunnels keep the legacy rendering.
		return nil, fmt.Errorf("隧道 TLS CA 私钥缺失（%s），证书在而私钥不在：无法签发节点证书，也绝不静默重建 CA（会作废已下发的节点证书）；请从备份恢复私钥后重启面板", keyPath)
	default: // cert missing, key present: re-derive the cert.
		keyBlock, _ := pem.Decode(keyPEM)
		if keyBlock == nil {
			return nil, fmt.Errorf("CA 私钥 PEM 无法解析（%s）", keyPath)
		}
		priv, err := x509.ParseECPrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CA 私钥解析失败（%s）: %w", keyPath, err)
		}
		ca, err := deriveTunnelTLSCACert(priv, time.Now())
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(certPath+".tmp", ca.CertPEM, 0o644); err != nil {
			return nil, fmt.Errorf("写入 %s: %w", certPath+".tmp", err)
		}
		if err := os.Rename(certPath+".tmp", certPath); err != nil {
			_ = os.Remove(certPath + ".tmp")
			return nil, fmt.Errorf("发布 %s: %w", certPath, err)
		}
		log.Printf("隧道 TLS CA 来源: 私钥在、证书缺失，已从既有私钥重签 CA 证书（文件 %s），已下发的节点证书继续有效", certPath)
		return ca, nil
	}
}

// CASha256 returns the SHA-256 fingerprint of the CA certificate DER.
func (ca *TunnelTLSCA) CASha256() string {
	if ca == nil || ca.Cert == nil {
		return ""
	}
	sum := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// IssueNodeCertificate signs a per-node certificate for nodeID: the
// SAN binds the stable node identity (node-<id>.flmx.internal) and
// the certificate is valid for both server and client auth, so the
// same material serves the node's listener (server side) and its
// dialer (client side, required by verified listeners). Validity is
// 2 years; rotation is a re-issue on the next distribution (the
// agent's status check reports expiry and the panel re-issues).
func (ca *TunnelTLSCA) IssueNodeCertificate(nodeID int64) (certPEM, keyPEM []byte, certSHA256 string, err error) {
	if ca == nil || ca.Cert == nil || ca.Key == nil {
		return nil, nil, "", errors.New("隧道 TLS CA 未加载")
	}
	if nodeID <= 0 {
		return nil, nil, "", errors.New("隧道 TLS 节点 ID 非法")
	}
	serverName := TunnelTLSNodeServerName(nodeID)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", fmt.Errorf("生成节点密钥: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: serverName, Organization: []string{"FLMX"}},
		DNSNames:     []string{serverName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(2 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &priv.PublicKey, ca.Key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("签发节点证书: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, "", fmt.Errorf("编码节点私钥: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	sum := sha256.Sum256(der)
	return certPEM, keyPEM, hex.EncodeToString(sum[:]), nil
}
