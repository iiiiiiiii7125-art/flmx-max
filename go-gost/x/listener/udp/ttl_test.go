package udp

import (
	"testing"
	"time"

	xmetadata "github.com/go-gost/x/metadata"
)

// UDP session idle TTL alignment: the listener default, the pool
// DefaultTTL and the panel-generated "ttl" must all be 5m, so no
// layer re-cuts a session the entry keeps alive. The effective
// reap window is (TTL, 2*TTL] via the pool's two-tick idle rule;
// 30s reaped sessions after 30-60s of bidirectional silence.
func TestDefaultTTLIsFiveMinutes(t *testing.T) {
	l := &udpListener{}
	if err := l.parseMetadata(xmetadata.NewMetadata(map[string]any{})); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if l.md.ttl != 5*time.Minute {
		t.Fatalf("default ttl = %v, want 5m", l.md.ttl)
	}
}

func TestExplicitTTLKept(t *testing.T) {
	l := &udpListener{}
	if err := l.parseMetadata(xmetadata.NewMetadata(map[string]any{"ttl": "300s"})); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if l.md.ttl != 5*time.Minute {
		t.Fatalf("explicit ttl = %v, want 5m", l.md.ttl)
	}
}
