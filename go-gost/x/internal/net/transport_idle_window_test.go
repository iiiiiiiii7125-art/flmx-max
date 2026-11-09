package net

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/go-gost/x/internal/idle"
)

// 2.1.2 hotfix regression (user ruling 2026-11-09): the FIX-1
// no-progress teardown is OFF unless the panel configured a
// per-node window. With the window disabled a fully silent session
// must survive indefinitely — the pre-FIX-1 behaviour — at both the
// internal entry (explicit 0) and the public Transport entry
// (which reads the internal/idle knob).
func TestTransportWindowDisabledSilentSessionSurvives(t *testing.T) {
	old := idle.Get()
	idle.Set(0)
	defer idle.Set(old)

	for name, run := range map[string]func(rw1, rw2 io.ReadWriter) error{
		"internal zero window": func(rw1, rw2 io.ReadWriter) error { return transport(rw1, rw2, 0) },
		"public knob off":      func(rw1, rw2 io.ReadWriter) error { return Transport(rw1, rw2) },
	} {
		t.Run(name, func(t *testing.T) {
			clientSide, transportToClient := tcpPair(t)
			defer clientSide.Close()
			backendSide, transportToBackend := tcpPair(t)
			defer backendSide.Close()

			done := make(chan error, 1)
			go func() { done <- run(transportToClient, transportToBackend) }()

			// Silence for well over a second: with the fuse off
			// nothing may be torn down.
			select {
			case err := <-done:
				t.Fatalf("Transport returned with the window disabled: %v", err)
			case <-time.After(time.Second):
			}

			// And the session must still carry traffic afterwards.
			if _, err := clientSide.Write([]byte("ping")); err != nil {
				t.Fatalf("write after silence: %v", err)
			}
			buf := make([]byte, 4)
			backendSide.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := backendSide.Read(buf); err != nil {
				t.Fatalf("backend read after silence: %v", err)
			}
			if string(buf) != "ping" {
				t.Fatalf("backend read %q, want ping", buf)
			}

			clientSide.Close()
			backendSide.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Transport err = %v, want nil after clean close", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Transport did not return after clean close")
			}
			transportToClient.Close()
			transportToBackend.Close()
		})
	}
}

// The public Transport entry takes its window from internal/idle:
// once the panel's window is published, a fully silent session is
// torn down with ErrNoProgress again (the fuse, opt-in).
func TestTransportPublicEntryHonoursConfiguredWindow(t *testing.T) {
	old := idle.Get()
	idle.Set(300 * time.Millisecond)
	defer idle.Set(old)

	clientSide, transportToClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportToBackend := tcpPair(t)
	defer backendSide.Close()
	go io.Copy(io.Discard, backendSide)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Transport(transportToClient, transportToBackend) }()

	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("Transport err = %v, want ErrNoProgress", err)
		}
		if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
			t.Fatalf("torn down after %v, before the configured window", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silent session not torn down within the configured window")
	}
}

// The incident report's buffered-path gap: one-direction trickle
// survival was previously only proven on the raw-TCP splice path.
// On wrapped (non-splice) endpoints the same criterion must hold —
// bytes in ONE direction only, spaced inside the window, keep the
// session alive across several windows.
func TestTransportBufferedOneWayTrickleNotKilled(t *testing.T) {
	const window = 300 * time.Millisecond

	clientSide, transportToClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportToBackend := tcpPair(t)

	received := make(chan struct{})
	go func() {
		buf := make([]byte, 64)
		total := 0
		for total < 8*16 {
			n, err := clientSide.Read(buf)
			if err != nil {
				return
			}
			total += n
		}
		close(received)
	}()

	done := make(chan error, 1)
	go func() {
		done <- transport(hideTCP{transportToClient}, hideTCP{transportToBackend}, window)
	}()

	// Downstream-only trickle: the backend sends, the client never
	// sends a byte. 8 chunks, one every window/3 (≈2.7 windows).
	for i := 0; i < 8; i++ {
		if _, err := backendSide.Write(bytes.Repeat([]byte("y"), 16)); err != nil {
			t.Fatalf("backend write %d: %v", i, err)
		}
		time.Sleep(window / 3)
	}

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not receive the full one-way trickle")
	}
	select {
	case err := <-done:
		t.Fatalf("Transport returned during a one-way trickling session: %v", err)
	default:
	}

	clientSide.Close()
	backendSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Transport err = %v, want nil after clean close", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return after clean close")
	}
	transportToClient.Close()
	transportToBackend.Close()
}
