package v5

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/connector"
	"github.com/go-gost/x/internal/util/socks"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
)

// F1-01 regression: with no TLS config supplied, the SOCKS5 TLS method
// must verify the server's certificate. Previously the connector
// silently substituted InsecureSkipVerify:true, so any certificate —
// including a MITM's self-signed one — was accepted.

func testServerCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return cert, pool
}

func serveTLSOnce(t *testing.T, cert tls.Certificate) (addr string, result chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	result = make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		srv := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
		srv.SetDeadline(time.Now().Add(10 * time.Second))
		result <- srv.Handshake()
	}()
	return ln.Addr().String(), result
}

func handshakeViaSelector(t *testing.T, c connector.Connector, addr string) error {
	t.Helper()
	sel := c.(*socks5Connector).selector.(*clientSelector)
	raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer raw.Close()
	_, wrapped, err := sel.OnSelected(socks.MethodTLS, raw)
	if err != nil {
		return err
	}
	tc, ok := wrapped.(*tls.Conn)
	if !ok {
		t.Fatalf("OnSelected returned %T, want *tls.Conn", wrapped)
	}
	tc.SetDeadline(time.Now().Add(10 * time.Second))
	return tc.Handshake()
}

func TestSocksTLSDefaultConfigRejectsSelfSigned(t *testing.T) {
	cert, _ := testServerCert(t)
	addr, _ := serveTLSOnce(t, cert)

	c := NewConnector(connector.LoggerOption(xlogger.Nop()))
	if err := c.Init(mdx.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sel := c.(*socks5Connector).selector.(*clientSelector)
	if sel.TLSConfig == nil {
		t.Fatal("selector has no TLS config")
	}
	if sel.TLSConfig.InsecureSkipVerify {
		t.Fatal("default TLS config has InsecureSkipVerify=true")
	}

	if err := handshakeViaSelector(t, c, addr); err == nil {
		t.Fatal("handshake with a self-signed server certificate succeeded under the default TLS config; want verification failure")
	}
}

func TestSocksTLSExplicitRootCAsAccepted(t *testing.T) {
	cert, pool := testServerCert(t)
	addr, _ := serveTLSOnce(t, cert)

	c := NewConnector(
		connector.LoggerOption(xlogger.Nop()),
		connector.TLSConfigOption(&tls.Config{RootCAs: pool}),
	)
	if err := c.Init(mdx.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := handshakeViaSelector(t, c, addr); err != nil {
		t.Fatalf("handshake with explicit RootCAs failed: %v", err)
	}
}
