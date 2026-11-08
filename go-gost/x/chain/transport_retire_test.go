package chain

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/metadata"
)

// F05: Transport.Retire used to skip dialers/connectors without a
// Retire method silently — the mechanism by which the ssh/sshd
// dialers' sessions leaked across chain reloads unnoticed. The
// skip must now be counted and observable.

type plainDialer struct{}

func (plainDialer) Init(metadata.Metadata) error { return nil }
func (plainDialer) Dial(context.Context, string, ...dialer.DialOption) (net.Conn, error) {
	return nil, errors.New("unused")
}

type plainConnector struct{}

func (plainConnector) Init(metadata.Metadata) error { return nil }
func (plainConnector) Connect(context.Context, net.Conn, string, string, ...connector.ConnectOption) (net.Conn, error) {
	return nil, errors.New("unused")
}

type retiringDialer struct {
	plainDialer
	retired *int
}

func (d retiringDialer) Retire() { *d.retired += 1 }

type retiringConnector struct {
	plainConnector
	retired *int
}

func (c retiringConnector) Retire() { *c.retired += 1 }

func TestTransportRetireCountsSkips(t *testing.T) {
	before := RetireSkipCount()

	tr := NewTransport(plainDialer{}, plainConnector{})
	tr.Retire()

	if got := RetireSkipCount() - before; got != 2 {
		t.Fatalf("RetireSkipCount delta = %d, want 2 (dialer + connector)", got)
	}
}

func TestTransportRetireReachesRetirersWithoutSkips(t *testing.T) {
	var dRetired, cRetired int
	tr := NewTransport(
		retiringDialer{retired: &dRetired},
		retiringConnector{retired: &cRetired},
	)

	before := RetireSkipCount()
	tr.Retire()

	if dRetired != 1 || cRetired != 1 {
		t.Fatalf("retired counts = %d/%d, want 1/1", dRetired, cRetired)
	}
	if got := RetireSkipCount() - before; got != 0 {
		t.Fatalf("RetireSkipCount delta = %d, want 0", got)
	}
}
