package nftables

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// C3-02 regression: the nftables SSH runner must verify the node host key
// against a pinned SHA256 fingerprint (fail closed when unpinned or
// mismatched), and the TOFU probe must capture the presented key without
// ever sending credentials to an unverified host.

func TestHostKeyCallbackPinStates(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	key := signer.PublicKey()
	fingerprint := ssh.FingerprintSHA256(key)

	if err := hostKeyCallback(fingerprint)("203.0.113.10:22", &net.TCPAddr{}, key); err != nil {
		t.Fatalf("matching pin must be accepted, got %v", err)
	}

	err = hostKeyCallback("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")("203.0.113.10:22", &net.TCPAddr{}, key)
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("mismatched pin must yield HostKeyMismatchError, got %v", err)
	}
	if mismatch.Actual != fingerprint {
		t.Fatalf("mismatch must carry the presented fingerprint %q, got %q", fingerprint, mismatch.Actual)
	}

	err = hostKeyCallback("")("203.0.113.10:22", &net.TCPAddr{}, key)
	if !errors.Is(err, ErrHostKeyUnpinned) {
		t.Fatalf("unpinned config must fail closed with ErrHostKeyUnpinned, got %v", err)
	}
}

type hostKeyTestServer struct {
	host        string
	port        int
	fingerprint string
	attempts    *int32
	listener    net.Listener
}

func startHostKeyTestServer(t *testing.T) *hostKeyTestServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	attempts := new(int32)
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			atomic.AddInt32(attempts, 1)
			if c.User() == "root" && string(password) == "secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("bad credentials")
		},
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveHostKeyTestConn(conn, config)
		}
	}()
	tcpAddr := listener.Addr().(*net.TCPAddr)
	return &hostKeyTestServer{
		host:        "127.0.0.1",
		port:        tcpAddr.Port,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		attempts:    attempts,
		listener:    listener,
	}
}

func serveHostKeyTestConn(nc net.Conn, config *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)
					_, _ = channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
					_ = channel.Close()
					return
				}
				_ = req.Reply(false, nil)
			}
		}()
	}
}

func (s *hostKeyTestServer) cfg(pin string) SSHConfig {
	return SSHConfig{
		Host:               s.host,
		Port:               s.port,
		Username:           "root",
		AuthType:           "password",
		Password:           "secret",
		HostKeyFingerprint: pin,
	}
}

func TestSSHRunnerVerifiesPinnedHostKey(t *testing.T) {
	server := startHostKeyTestServer(t)
	runner := NewSSHRunner()
	if err := runner.Test(context.Background(), server.cfg(server.fingerprint)); err != nil {
		t.Fatalf("test with correct pin: %v", err)
	}
	if got := atomic.LoadInt32(server.attempts); got != 1 {
		t.Fatalf("expected exactly one password auth after key verification, got %d", got)
	}
}

func TestSSHRunnerRejectsMismatchedHostKeyBeforeAuth(t *testing.T) {
	server := startHostKeyTestServer(t)
	runner := NewSSHRunner()
	err := runner.Test(context.Background(), server.cfg("SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"))
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected HostKeyMismatchError, got %v", err)
	}
	if got := atomic.LoadInt32(server.attempts); got != 0 {
		t.Fatalf("no credential may reach a host whose key mismatches the pin, got %d auth attempts", got)
	}
}

func TestSSHRunnerRejectsUnpinnedHostKeyBeforeAuth(t *testing.T) {
	server := startHostKeyTestServer(t)
	runner := NewSSHRunner()
	err := runner.Test(context.Background(), server.cfg(""))
	if !errors.Is(err, ErrHostKeyUnpinned) {
		t.Fatalf("expected ErrHostKeyUnpinned, got %v", err)
	}
	if got := atomic.LoadInt32(server.attempts); got != 0 {
		t.Fatalf("unpinned connections must fail before auth, got %d auth attempts", got)
	}
}

func TestProbeHostKeyFingerprintCapturesKeyWithoutAuth(t *testing.T) {
	server := startHostKeyTestServer(t)
	fingerprint, err := ProbeHostKeyFingerprint(context.Background(), server.cfg(""))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if fingerprint != server.fingerprint {
		t.Fatalf("probe returned %q, want %q", fingerprint, server.fingerprint)
	}
	if got := atomic.LoadInt32(server.attempts); got != 0 {
		t.Fatalf("probe must never authenticate, got %d auth attempts", got)
	}
}
