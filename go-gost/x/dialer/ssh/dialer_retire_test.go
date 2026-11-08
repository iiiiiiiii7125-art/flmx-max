package ssh

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
	xlogger "github.com/go-gost/x/logger"
	"golang.org/x/crypto/ssh"
)

// F05 regression: the ssh dialer had no Retire, so chain reloads
// left the old generation's sessions (connections plus their
// Wait/WaitClose goroutines) alive forever when the peer died
// silently. Retire must detach the sessions, refuse new dials,
// let in-flight streams drain, and close the underlying
// connection when the last stream finishes.

type stdNetDialer struct{}

func (stdNetDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var nd net.Dialer
	return nd.DialContext(ctx, network, addr)
}

var _ xnet.Dialer = stdNetDialer{}

type sshTestServer struct {
	addr     string
	connDied chan struct{}
}

func startSSHTestServer(t *testing.T) *sshTestServer {
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

	s := &sshTestServer{addr: ln.Addr().String(), connDied: make(chan struct{})}
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

func echoRoundtrip(t *testing.T, conn net.Conn, msg string) {
	t.Helper()
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
	conn.SetReadDeadline(time.Time{})
}

func TestRetireDrainsSessionStreams(t *testing.T) {
	server := startSSHTestServer(t)

	d := NewDialer(coredialer.LoggerOption(xlogger.Nop())).(*sshDialer)
	conn, err := d.Dial(context.Background(), server.addr,
		coredialer.NetDialerDialOption(stdNetDialer{}))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	session := d.sessions[server.addr]
	if session == nil {
		t.Fatal("session not cached after Dial")
	}
	if got := session.NumStreams(); got != 1 {
		t.Fatalf("NumStreams = %d, want 1", got)
	}

	echoRoundtrip(t, conn, "ping")

	d.Retire()

	if got := len(d.sessions); got != 0 {
		t.Fatalf("sessions not detached: len = %d, want 0", got)
	}
	if _, err := d.Dial(context.Background(), server.addr,
		coredialer.NetDialerDialOption(stdNetDialer{})); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial after Retire err = %v, want net.ErrClosed", err)
	}

	// Drain semantics: the in-flight stream keeps working and the
	// underlying connection stays up while it is open.
	echoRoundtrip(t, conn, "drain")
	select {
	case <-server.connDied:
		t.Fatal("server connection died while a stream was still open on a retired session")
	case <-time.After(200 * time.Millisecond):
	}

	// Closing the last stream reaps the session: the server sees
	// its connection die, which also ends the client's
	// Wait/WaitClose goroutines.
	conn.Close()
	select {
	case <-server.connDied:
	case <-time.After(3 * time.Second):
		t.Fatal("server connection still alive after the last stream of a retired session closed")
	}
	if got := session.NumStreams(); got != 0 {
		t.Fatalf("NumStreams = %d after stream close, want 0", got)
	}
}

func TestCloseClosesSessionsImmediately(t *testing.T) {
	server := startSSHTestServer(t)

	d := NewDialer(coredialer.LoggerOption(xlogger.Nop())).(*sshDialer)
	conn, err := d.Dial(context.Background(), server.addr,
		coredialer.NetDialerDialOption(stdNetDialer{}))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	echoRoundtrip(t, conn, "ping")

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
