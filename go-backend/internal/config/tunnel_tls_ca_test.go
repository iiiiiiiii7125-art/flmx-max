package config

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadOrCreateTunnelTLSCACreatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateTunnelTLSCA(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateTunnelTLSCA: %v", err)
	}
	if ca.Cert == nil || !ca.Cert.IsCA {
		t.Fatal("created CA must parse as a CA certificate")
	}
	if got := ca.Cert.NotAfter.Sub(time.Now()); got < 9*365*24*time.Hour {
		t.Fatalf("CA validity = %v, want ~10 years", got)
	}
	keyPath := filepath.Join(dir, tunnelTLSKeyFileName)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat CA key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("CA key perm = %o, want 0600", info.Mode().Perm())
	}
	// A restart must reuse the same CA, never mint a new one.
	ca2, err := LoadOrCreateTunnelTLSCA(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreateTunnelTLSCA: %v", err)
	}
	if ca2.CASha256() != ca.CASha256() {
		t.Fatal("restart minted a different CA; the CA must persist across restarts")
	}
}

func TestLoadOrCreateTunnelTLSCACertWithoutKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateTunnelTLSCA(dir); err != nil {
		t.Fatalf("create CA: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, tunnelTLSKeyFileName)); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	if _, err := LoadOrCreateTunnelTLSCA(dir); err == nil {
		t.Fatal("CA cert without key must fail closed, not silently mint a new CA")
	} else if !strings.Contains(err.Error(), "私钥缺失") {
		t.Fatalf("error must name the missing private key, got: %v", err)
	}
	// No new key must have appeared as a side effect.
	if _, err := os.Stat(filepath.Join(dir, tunnelTLSKeyFileName)); !os.IsNotExist(err) {
		t.Fatalf("failed load must not create a replacement key, stat: %v", err)
	}
}

func TestLoadOrCreateTunnelTLSAKeyWithoutCertReDerives(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateTunnelTLSCA(dir)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	oldPub := ca.Cert.PublicKey
	if err := os.Remove(filepath.Join(dir, tunnelTLSCACertFileName)); err != nil {
		t.Fatalf("remove cert: %v", err)
	}
	ca2, err := LoadOrCreateTunnelTLSCA(dir)
	if err != nil {
		t.Fatalf("key without cert must re-derive the cert: %v", err)
	}
	if !publicKeysEqual(oldPub, ca2.Cert.PublicKey) {
		t.Fatal("re-derived CA cert must carry the same public key, so issued node certs keep verifying")
	}
}

func TestIssueNodeCertificateVerifiesAgainstCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateTunnelTLSCA(dir)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	certPEM, keyPEM, certSHA, err := ca.IssueNodeCertificate(7)
	if err != nil {
		t.Fatalf("IssueNodeCertificate: %v", err)
	}
	if certSHA == "" {
		t.Fatal("issue must return a certificate fingerprint")
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("node cert PEM does not decode")
	}
	nodeCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse node cert: %v", err)
	}
	if len(nodeCert.DNSNames) != 1 || nodeCert.DNSNames[0] != "node-7.flmx.internal" {
		t.Fatalf("node cert SAN = %v, want [node-7.flmx.internal]", nodeCert.DNSNames)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := nodeCert.Verify(x509.VerifyOptions{
		Roots:       pool,
		DNSName:     "node-7.flmx.internal",
		CurrentTime: time.Now(),
	}); err != nil {
		t.Fatalf("issued node cert must verify against the CA for its server name: %v", err)
	}
	// The issued key must match the certificate.
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("node key PEM does not decode")
	}
	priv, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("parse node key: %v", err)
	}
	if !publicKeysEqual(nodeCert.PublicKey, &priv.PublicKey) {
		t.Fatal("issued node key does not match the certificate")
	}
	// ExtKeyUsage must cover both server and client auth: the same
	// material serves the listener and the dialer.
	hasServer, hasClient := false, false
	for _, u := range nodeCert.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
		if u == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasServer || !hasClient {
		t.Fatalf("node cert ExtKeyUsage = %v, want server+client auth", nodeCert.ExtKeyUsage)
	}
	if _, _, _, err := ca.IssueNodeCertificate(0); err == nil {
		t.Fatal("issuing for node 0 must fail")
	}
}
