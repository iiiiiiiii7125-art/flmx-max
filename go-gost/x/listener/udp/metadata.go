package udp

import (
	"time"

	mdata "github.com/go-gost/core/metadata"
	"github.com/go-gost/x/internal/net/udp"
	mdutil "github.com/go-gost/x/metadata/util"
)

const (
	defaultTTL = 30 * time.Second
	// The read buffer must hold a whole maximum-size datagram: a
	// smaller buffer makes the kernel silently discard the tail of
	// oversized datagrams before any consumer can see them (F-UDP-5).
	// Buffers come from bufpool (size-classed sync.Pools), so the
	// cost is one pooled 64K buffer per in-flight datagram, recycled
	// on consumption.
	defaultReadBufferSize = udp.MaxDatagramSize
	defaultReadQueueSize  = 128
	defaultBacklog        = 128
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
