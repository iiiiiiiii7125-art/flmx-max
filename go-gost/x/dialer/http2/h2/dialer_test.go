package h2

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

// TestRetireDetachesClients verifies the P1-08 family fix for the h2
// client table: Retire detaches every cached client and the retired
// dialer refuses new dials.
func TestRetireDetachesClients(t *testing.T) {
	d := NewDialer().(*h2Dialer)
	for _, addr := range []string{"a:80", "b:80"} {
		d.clients[addr] = &http.Client{}
	}

	d.Retire()

	if got := len(d.clients); got != 0 {
		t.Fatalf("clients not detached: len = %d, want 0", got)
	}
	if _, err := d.Dial(context.Background(), "127.0.0.1:80"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial after Retire err = %v, want net.ErrClosed", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close after Retire: %v", err)
	}
}
