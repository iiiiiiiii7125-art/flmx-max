package rudp

import (
	"testing"
	"time"

	xmetadata "github.com/go-gost/x/metadata"
)

// UDP session idle TTL alignment: rudp forwards its ttl via chain
// Bind to the relay/socks bindUDP listener pool, so its default
// must match the entry TTL (5m) and internal/net/udp DefaultTTL,
// or rudp-routed sessions get cut first. See listener/udp
// TestDefaultTTLIsFiveMinutes for the rationale.
func TestDefaultTTLIsFiveMinutes(t *testing.T) {
	l := &rudpListener{}
	if err := l.parseMetadata(xmetadata.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if l.md.ttl != 5*time.Minute {
		t.Fatalf("default ttl = %v, want 5m", l.md.ttl)
	}
}
