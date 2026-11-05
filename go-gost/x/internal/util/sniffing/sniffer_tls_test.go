package sniffing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	dissector "github.com/go-gost/tls-dissector"
	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// F1-03 regression: when the client sent no SNI, terminateTLS must
// still verify the upstream certificate chain. Previously it set
// InsecureSkipVerify=true in exactly that case, accepting any
// certificate (e.g. a MITM's self-signed one) on the upstream leg.
func TestTerminateTLSNoSNIRejectsSelfSignedUpstream(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	upstreamCert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				srv := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{upstreamCert}})
				srv.SetDeadline(time.Now().Add(10 * time.Second))
				_ = srv.Handshake()
			}(conn)
		}
	}()

	cc, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("Dial upstream: %v", err)
	}
	defer cc.Close()

	clientSide, peerSide := net.Pipe()
	defer clientSide.Close()
	defer peerSide.Close()

	h := &Sniffer{}
	ho := &HandleOptions{
		RecorderObject: &xrecorder.HandlerRecorderObject{TLS: &xrecorder.TLSRecorderObject{}},
		Log:            xlogger.Nop(),
	}
	hello := &dissector.ClientHelloInfo{ServerName: ""}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = h.terminateTLS(ctx, clientSide, cc, hello, ho)

	var cve *tls.CertificateVerificationError
	if !errors.As(err, &cve) {
		t.Fatalf("terminateTLS with no SNI and a self-signed upstream: err = %v; want a certificate verification error", err)
	}
}
