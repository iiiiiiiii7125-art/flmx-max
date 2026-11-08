package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	xnet "github.com/go-gost/core/common/net"
	coredialer "github.com/go-gost/core/dialer"
	ssh_util "github.com/go-gost/x/internal/util/ssh"
	xlogger "github.com/go-gost/x/logger"
	"golang.org/x/crypto/ssh"
)

// F05 regression: the sshd dialer had no Retire, so chain reloads
// left the old generation's sessions (connections plus their
// Wait/WaitClose goroutines) alive forever when the peer died
// silently. Retire must detach and close the sessions — outright,
// because the connector opens streams through the ssh.Client
// exposed on the returned ClientConn, which the dialer cannot
// count (see the Retire comment in dialer.go).

type stdNetDialer struct{}

func (stdNetDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, network, addr)
}

var _ xnet.Dialer = stdNetDialer{}

type sshdTestServer struct {
	addr     string
	connDied chan struct{}
}

func startSSHDTestServer(t *testing.T) *sshdTestServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	s := &sshdTestServer{addr: ln.Addr().String(), connDied: make(chan struct{})}
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		serverConn, chans, reqs, err := ssh.NewServerConn(nc, config)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			for ch := range chans {
				channel, _, err := ch.Accept()
				if err != nil {
					continue
				}
				go func() {
					io.Copy(channel, channel) // echo
					channel.Close()
				}()
			}
		}()
		serverConn.Wait()
		close(s.connDied)
	}()
	return s
}

func TestRetireClosesSessions(t *testing.T) {
	server := startSSHDTestServer(t)

	d := NewDialer(coredialer.LoggerOption(xlogger.Nop())).(*sshdDialer)
	conn, err := d.Dial(context.Background(), server.addr,
		coredialer.NetDialerDialOption(stdNetDialer{}))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// The returned conn must remain the concrete ClientConn the
	// sshd connector type-asserts.
	cc, ok := conn.(*ssh_util.ClientConn)
	if !ok {
		t.Fatalf("Dial returned %T, want *ssh_util.ClientConn", conn)
	}
	if cc.Client() == nil {
		t.Fatal("ClientConn.Client() is nil")
	}
	if got := len(d.sessions); got != 1 {
		t.Fatalf("sessions len = %d, want 1", got)
	}

	d.Retire()

	if got := len(d.sessions); got != 0 {
		t.Fatalf("sessions not detached: len = %d, want 0", got)
	}
	if _, err := d.Dial(context.Background(), server.addr,
		coredialer.NetDialerDialOption(stdNetDialer{})); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial after Retire err = %v, want net.ErrClosed", err)
	}

	// Outright close: the server sees its connection die, which
	// also ends the client's Wait/WaitClose goroutines.
	select {
	case <-server.connDied:
	case <-time.After(3 * time.Second):
		t.Fatal("server connection still alive after Retire")
	}
}

func TestCloseClosesSessions(t *testing.T) {
	server := startSSHDTestServer(t)

	d := NewDialer(coredialer.LoggerOption(xlogger.Nop())).(*sshdDialer)
	conn, err := d.Dial(context.Background(), server.addr,
		coredialer.NetDialerDialOption(stdNetDialer{}))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(d.sessions); got != 0 {
		t.Fatalf("sessions not detached: len = %d, want 0", got)
	}
	select {
	case <-server.connDied:
	case <-time.After(3 * time.Second):
		t.Fatal("server connection still alive after Close")
	}
}
