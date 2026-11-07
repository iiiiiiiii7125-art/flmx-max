package http3

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	pht_util "github.com/go-gost/x/internal/util/pht"
)

// TestRetireDetachesClients verifies P1-08 for http3: Retire detaches
// every cached client and the retired dialer refuses new dials.
func TestRetireDetachesClients(t *testing.T) {
	d := NewDialer().(*http3Dialer)
	for _, addr := range []string{"a:443", "b:443"} {
		d.clients[addr] = &pht_util.Client{Client: &http.Client{}}
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
