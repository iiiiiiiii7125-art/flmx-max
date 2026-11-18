package udp

import (
	"time"

	mdata "github.com/go-gost/core/metadata"
	"github.com/go-gost/x/internal/net/udp"
	mdutil "github.com/go-gost/x/metadata/util"
)

const (
	// 5m: UDP session idle TTL, aligned with DefaultTTL in
	// internal/net/udp (effective reap window (TTL, 2*TTL] via the
	// pool's two-tick idle rule) and with the downstream kernels'
	// UDP idle defaults (sing-box udp_timeout 5m; Xray connIdle
	// 300s). 30s reaped sessions after 30-60s of bidirectional
	// silence, cutting long idle gaps mid-game.
	defaultTTL = 5 * time.Minute
	// The read buffer must hold a whole maximum-size datagram: a
	// smaller buffer makes the kernel silently discard the tail of
	// oversized datagrams before any consumer can see them (F-UDP-5).
	// Buffers come from bufpool (size-classed sync.Pools), so the
	// cost is one pooled 64K buffer per in-flight datagram, recycled
	// on consumption.
	defaultReadBufferSize = udp.MaxDatagramSize
	// 1024 aligns with the rudp/ftcp listener defaults and with
	// udp.DefaultReadQueueSize: at 128 a 200-packet zero-gap burst
	// from one source lost ~4% of its datagrams to a full receive
	// queue at this listener (K2 live evidence, FIX-U).
	defaultReadQueueSize = 1024
	defaultBacklog       = 128
)

type metadata struct {
	readBufferSize int
	readQueueSize  int
	backlog        int
	keepalive      bool
	ttl            time.Duration
}

func (l *udpListener) parseMetadata(md mdata.Metadata) (err error) {
	const (
		readBufferSize = "readBufferSize"
		readQueueSize  = "readQueueSize"
		backlog        = "backlog"
		keepalive      = "keepalive"
		ttl            = "ttl"
	)

	l.md.ttl = mdutil.GetDuration(md, ttl)
	if l.md.ttl <= 0 {
		l.md.ttl = defaultTTL
	}
	l.md.readBufferSize = mdutil.GetInt(md, readBufferSize)
	if l.md.readBufferSize <= 0 {
		l.md.readBufferSize = defaultReadBufferSize
	}

	l.md.readQueueSize = mdutil.GetInt(md, readQueueSize)
	if l.md.readQueueSize <= 0 {
		l.md.readQueueSize = defaultReadQueueSize
	}

	l.md.backlog = mdutil.GetInt(md, backlog)
	if l.md.backlog <= 0 {
		l.md.backlog = defaultBacklog
	}
	l.md.keepalive = mdutil.GetBool(md, keepalive)

	return
}
