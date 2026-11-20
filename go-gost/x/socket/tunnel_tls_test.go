package socket

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mintTunnelTLSTestCA mints a throwaway CA for tunnel-TLS tests.
func mintTunnelTLSTestCA(t *testing.T) (certPEM, keyPEM []byte, cert *x509.Certificate, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1001),
		Subject:               pkix.Name{CommonName: "FLMX Test Tunnel CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal CA key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, cert, key
}

// mintTunnelTLSTestNodeCert mints a node certificate signed by caCert.
// serverName is the DNS SAN; notAfter controls expiry testing.
func mintTunnelTLSTestNodeCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, serverName string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2002),
		Subject:      pkix.Name{CommonName: serverName},
		DNSNames:     []string{serverName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create node cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal node key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func useTempTunnelTLSBaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := tunnelTLSBaseDir
	tunnelTLSBaseDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { tunnelTLSBaseDir = old })
	return dir
}

func tunnelTLSIssuePayload(nodeID int64, serverName string, caPEM, certPEM, keyPEM []byte) map[string]interface{} {
	return map[string]interface{}{
		"nodeId":            nodeID,
		"serverName":        serverName,
		"caCertPem":         string(caPEM),
		"nodeCertPem":       string(certPEM),
		"nodePrivateKeyPem": string(keyPEM),
	}
}

func TestHandleTunnelTLSIssueStoresVerifiedMaterial(t *testing.T) {
	dir := useTempTunnelTLSBaseDir(t)
	caPEM, _, caCert, caKey := mintTunnelTLSTestCA(t)
	certPEM, keyPEM := mintTunnelTLSTestNodeCert(t, caCert, caKey, "node-7.flmx.internal", time.Now().Add(2*365*24*time.Hour))

	resp, err := handleTunnelTLSIssue(tunnelTLSIssuePayload(7, "node-7.flmx.internal", caPEM, certPEM, keyPEM))
	if err != nil {
		t.Fatalf("handleTunnelTLSIssue: %v", err)
	}
	issue, ok := resp.(tunnelTLSIssueResponse)
	if !ok {
		t.Fatalf("issue response type = %T, want tunnelTLSIssueResponse", resp)
	}
	if !issue.Ready || issue.ServerName != "node-7.flmx.internal" {
		t.Fatalf("issue response = %+v, want ready with serverName node-7.flmx.internal", issue)
	}
	if issue.CASha256 != sha256Hex(mustParseCertPEMBytes(t, caPEM)) || issue.CertSha256 != sha256Hex(mustParseCertPEMBytes(t, certPEM)) {
		t.Fatalf("issue fingerprints = %q/%q, want sha256 of issued certs", issue.CASha256, issue.CertSha256)
	}
	caPath, certPath, keyPath := tunnelTLSPaths(dir)
	for _, p := range []string{caPath, certPath, keyPath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("perm of %s = %o, want 0600", p, info.Mode().Perm())
		}
	}

	statusResp, err := handleTunnelTLSStatus(nil)
	if err != nil {
		t.Fatalf("handleTunnelTLSStatus: %v", err)
	}
	status, ok := statusResp.(tunnelTLSStatusResponse)
	if !ok {
		t.Fatalf("status response type = %T, want tunnelTLSStatusResponse", statusResp)
	}
	if !status.Ready || status.CASha256 != issue.CASha256 || status.CertSha256 != issue.CertSha256 {
		t.Fatalf("status after issue = %+v, want ready with matching fingerprints", status)
	}
}

func mustParseCertPEMBytes(t *testing.T, pemBytes []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("PEM decode failed")
	}
	return block.Bytes
}

func TestHandleTunnelTLSIssueRejectsWrongCA(t *testing.T) {
	dir := useTempTunnelTLSBaseDir(t)
	caPEM, _, _, _ := mintTunnelTLSTestCA(t)
	_, _, otherCACert, otherCAKey := mintTunnelTLSTestCA(t)
	certPEM, keyPEM := mintTunnelTLSTestNodeCert(t, otherCACert, otherCAKey, "node-7.flmx.internal", time.Now().Add(2*365*24*time.Hour))

	if _, err := handleTunnelTLSIssue(tunnelTLSIssuePayload(7, "node-7.flmx.internal", caPEM, certPEM, keyPEM)); err == nil {
		t.Fatal("issue with a certificate from another CA must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, tunnelTLSRelDir)); !os.IsNotExist(err) {
		t.Fatalf("failed issue must store nothing, tls dir state: %v", err)
	}
}

func TestHandleTunnelTLSIssueRejectsMismatchedKey(t *testing.T) {
	dir := useTempTunnelTLSBaseDir(t)
	caPEM, _, caCert, caKey := mintTunnelTLSTestCA(t)
	certPEM, _ := mintTunnelTLSTestNodeCert(t, caCert, caKey, "node-7.flmx.internal", time.Now().Add(2*365*24*time.Hour))
	_, otherKeyPEM := mintTunnelTLSTestNodeCert(t, caCert, caKey, "node-8.flmx.internal", time.Now().Add(2*365*24*time.Hour))

	if _, err := handleTunnelTLSIssue(tunnelTLSIssuePayload(7, "node-7.flmx.internal", caPEM, certPEM, otherKeyPEM)); err == nil {
		t.Fatal("issue with a non-matching private key must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, tunnelTLSRelDir)); !os.IsNotExist(err) {
		t.Fatalf("failed issue must store nothing, tls dir state: %v", err)
	}
}

func TestHandleTunnelTLSIssueRejectsWrongSAN(t *testing.T) {
	useTempTunnelTLSBaseDir(t)
	caPEM, _, caCert, caKey := mintTunnelTLSTestCA(t)
	certPEM, keyPEM := mintTunnelTLSTestNodeCert(t, caCert, caKey, "node-8.flmx.internal", time.Now().Add(2*365*24*time.Hour))

	if _, err := handleTunnelTLSIssue(tunnelTLSIssuePayload(7, "node-7.flmx.internal", caPEM, certPEM, keyPEM)); err == nil {
		t.Fatal("issue whose certificate SAN is another node's identity must fail")
	}
}

func TestHandleTunnelTLSIssueRejectsExpiredCert(t *testing.T) {
	useTempTunnelTLSBaseDir(t)
	caPEM, _, caCert, caKey := mintTunnelTLSTestCA(t)
	certPEM, keyPEM := mintTunnelTLSTestNodeCert(t, caCert, caKey, "node-7.flmx.internal", time.Now().Add(-time.Hour))

	if _, err := handleTunnelTLSIssue(tunnelTLSIssuePayload(7, "node-7.flmx.internal", caPEM, certPEM, keyPEM)); err == nil {
		t.Fatal("issue with an expired node certificate must fail")
	}
}

func TestHandleTunnelTLSStatusNotReadyWhenEmpty(t *testing.T) {
	useTempTunnelTLSBaseDir(t)
	resp, err := handleTunnelTLSStatus(nil)
	if err != nil {
		t.Fatalf("handleTunnelTLSStatus: %v", err)
	}
	status, ok := resp.(tunnelTLSStatusResponse)
	if !ok {
		t.Fatalf("status response type = %T, want tunnelTLSStatusResponse", resp)
	}
	if status.Ready {
		t.Fatalf("status on empty dir = %+v, want ready=false", status)
	}
	if status.Error == "" {
		t.Fatal("status when not ready must name the missing material")
	}
}

func TestHandleTunnelTLSStatusDetectsDeletedKey(t *testing.T) {
	dir := useTempTunnelTLSBaseDir(t)
	caPEM, _, caCert, caKey := mintTunnelTLSTestCA(t)
	certPEM, keyPEM := mintTunnelTLSTestNodeCert(t, caCert, caKey, "node-7.flmx.internal", time.Now().Add(2*365*24*time.Hour))
	if _, err := handleTunnelTLSIssue(tunnelTLSIssuePayload(7, "node-7.flmx.internal", caPEM, certPEM, keyPEM)); err != nil {
		t.Fatalf("issue: %v", err)
	}
	_, _, keyPath := tunnelTLSPaths(dir)
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	resp, err := handleTunnelTLSStatus(nil)
	if err != nil {
		t.Fatalf("handleTunnelTLSStatus: %v", err)
	}
	status := resp.(tunnelTLSStatusResponse)
	if status.Ready {
		t.Fatalf("status after key deletion = %+v, want ready=false", status)
	}
	if !strings.Contains(status.Error, "私钥") {
		t.Fatalf("status error = %q, want it to name the missing private key", status.Error)
	}
}

func TestTunnelTLSIssuePayloadPrivateKeyIsRedactedFromLogs(t *testing.T) {
	_, _, caCert, caKey := mintTunnelTLSTestCA(t)
	certPEM, keyPEM := mintTunnelTLSTestNodeCert(t, caCert, caKey, "node-7.flmx.internal", time.Now().Add(2*365*24*time.Hour))
	caPEM, _, _, _ := mintTunnelTLSTestCA(t)
	payload := tunnelTLSIssuePayload(7, "node-7.flmx.internal", caPEM, certPEM, keyPEM)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	detail, ok := redactCommandForLog(raw)
	if !ok {
		t.Fatal("redactCommandForLog must render the issue command")
	}
	if strings.Contains(detail, string(keyPEM)) {
		t.Fatal("redacted command log still contains the node private key PEM")
	}
	if !strings.Contains(detail, "***") {
		t.Fatalf("redacted command log has no masked value: %s", detail)
	}
}
