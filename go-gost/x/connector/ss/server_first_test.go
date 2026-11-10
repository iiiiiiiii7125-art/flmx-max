package ss_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/handler"
	xchain "github.com/go-gost/x/chain"
	ssconnector "github.com/go-gost/x/connector/ss"
	sshandler "github.com/go-gost/x/handler/ss"
	xlogger "github.com/go-gost/x/logger"
)

// F-212-B, ss face: in delayed mode (the default) the ss
// connector cached the address header inside the shadow conn
// until the first payload Write. That header is the only place
// the target address exists, so a server-first target behind an
// ss leg was unreachable for a read-first consumer — the exact
// relay-connector gate, one protocol over. This runs the
// production ss connector against the production ss handler over
// loopback TCP: the client connects and reads without writing.
func TestSSServerFirstDelayedConnector(t *testing.T) {
	init := bytes.Repeat([]byte("INIT"), 50)

	// server-first target: INIT on accept, echo afterwards.
	tln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tln.Close()
	go func() {
		for {
			conn, err := tln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				if _, err := conn.Write(init); err != nil {
					return
				}
				io.Copy(conn, conn)
			}(conn)
		}
	}()

	auth := url.UserPassword("aes-256-cfb", "test-password")

	h := sshandler.NewHandler(
		handler.AuthOption(auth),
		handler.RouterOption(xchain.NewRouter(chain.LoggerRouterOption(xlogger.Nop()))),
		handler.LoggerOption(xlogger.Nop()),
		handler.ServiceOption("test-ss-server-first"),
	)
	if err := h.Init(nil); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go h.Handle(context.Background(), conn)
		}
	}()

	c := ssconnector.NewConnector(
		connector.AuthOption(auth),
		connector.LoggerOption(xlogger.Nop()),
	)
	if err := c.Init(nil); err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	cc, err := c.Connect(context.Background(), raw, "tcp", tln.Addr().String())
	if err != nil {
		t.Fatalf("ss Connect: %v", err)
	}
	defer cc.Close()

	if err := cc.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer cc.SetReadDeadline(time.Time{})

	got := make([]byte, len(init))
	if _, err := io.ReadFull(cc, got); err != nil {
		t.Fatalf("server-first INIT not delivered to a silent client through the ss leg: %v", err)
	}
	if !bytes.Equal(got, init) {
		t.Fatalf("INIT corrupted: %q", got)
	}

	payload := []byte("after-init")
	if _, err := cc.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(cc, echo); err != nil {
		t.Fatalf("echo Read: %v", err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("echo corrupted: %q", echo)
	}
}
