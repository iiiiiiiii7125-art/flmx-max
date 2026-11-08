package pht

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// F02 regression: authorized connections were stored in the
// server table with no TTL and no cap, deleted only on some
// push/pull error paths, and Server.Close did not drain them —
// an authorize-then-abandon client (the entry is created before
// any upper-layer authentication) left its entry and pipe end
// behind forever. Entries must now be reaped after one TTL of
// inactivity, and Close must drain whatever is left.

func startTestServer(t *testing.T, ttl time.Duration) *Server {
	t.Helper()
	s := NewServer("127.0.0.1:0",
		PathServerOption("/authorize", "/push", "/pull"),
		ReadTimeoutServerOption(50*time.Millisecond),
		ConnTTLServerOption(ttl),
		LoggerServerOption(xlogger.Nop()),
	)
	go s.ListenAndServe()

	deadline := time.Now().Add(3 * time.Second)
	for s.addr == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.addr == nil {
		t.Fatal("server did not start listening")
	}
	return s
}

func authorize(t *testing.T, s *Server) string {
	t.Helper()
	resp, err := http.Get("http://" + s.addr.String() + "/authorize")
	if err != nil {
		t.Fatalf("authorize request: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("authorize body: %v", err)
	}
	token, ok := strings.CutPrefix(string(b), "token=")
	if !ok || token == "" {
		t.Fatalf("authorize body = %q, want token=<id>", b)
	}
	return token
}

func TestServerReapsAbandonedAuthorize(t *testing.T) {
	s := startTestServer(t, 300*time.Millisecond)
	defer s.Close()

	token := authorize(t, s)

	// The accepted side of the pipe is what the service reads the
	// tunnel from.
	accepted, err := s.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer accepted.Close()

	if _, ok := s.conns.Load(token); !ok {
		t.Fatal("authorized entry missing from the table")
	}

	// Nobody ever pushes or pulls: the entry must be reaped
	// within roughly one TTL (sweeper ticks at TTL/3).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.conns.Load(token); !ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := s.conns.Load(token); ok {
		t.Fatal("abandoned entry was not reaped after its TTL")
	}

	// Reaping closed the stored pipe end, so the accepted side
	// sees the tunnel end instead of blocking forever.
	accepted.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [8]byte
	if _, err := accepted.Read(b[:]); err != io.EOF {
		t.Fatalf("accepted Read error = %v, want io.EOF after reap", err)
	}
}

func TestServerCloseDrainsConns(t *testing.T) {
	// A long TTL proves the drain is done by Close, not by the
	// sweeper.
	s := startTestServer(t, 30*time.Second)

	token := authorize(t, s)
	accepted, err := s.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer accepted.Close()

	if _, ok := s.conns.Load(token); !ok {
		t.Fatal("authorized entry missing from the table")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, ok := s.conns.Load(token); ok {
		t.Fatal("entry still in the table after Close")
	}

	accepted.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [8]byte
	if _, err := accepted.Read(b[:]); err != io.EOF {
		t.Fatalf("accepted Read error = %v, want io.EOF after Close drain", err)
	}
}

func TestServerActiveEntrySurvivesTTL(t *testing.T) {
	s := startTestServer(t, 300*time.Millisecond)
	defer s.Close()

	token := authorize(t, s)
	accepted, err := s.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer accepted.Close()

	// Keep pulling: each pull request is activity, so the entry
	// must outlive several TTLs. (Pulls return after one
	// readTimeout with no data; the cadence still counts.)
	stop := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(stop) {
		resp, err := http.Get("http://" + s.addr.String() + "/pull?token=" + token)
		if err != nil {
			t.Fatalf("pull request: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("pull status = %d, want 200", resp.StatusCode)
		}
	}

	if _, ok := s.conns.Load(token); !ok {
		t.Fatal("entry with ongoing pull activity was reaped")
	}
}
