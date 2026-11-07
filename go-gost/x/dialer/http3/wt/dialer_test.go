package wt

import (
	"context"
	"errors"
	"net"
	"testing"
)

// TestRetireDetachesClients verifies P1-08 for the WebTransport
// variant: Retire detaches every cached client (session-less clients
// close as no-ops) and the retired dialer refuses new dials.
func TestRetireDetachesClients(t *testing.T) {
	d := NewDialer().(*wtDialer)
	for _, addr := range []string{"a:443", "b:443"} {
		d.clients[addr] = &Client{}
	}

	d.Retire()

	if got := len(d.clients); got != 0 {
		t.Fatalf("clients not detached: len = %d, want 0", got)
	}
	if _, err := d.Dial(context.Background(), "a:443"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial after Retire err = %v, want net.ErrClosed", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close after Retire: %v", err)
	}
}
