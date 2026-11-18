package udp

import (
	"time"

	mdata "github.com/go-gost/core/metadata"
	mdutil "github.com/go-gost/x/metadata/util"
)

const (
	// 5m: same UDP session idle TTL as the rest of the family
	// (see internal/net/udp DefaultTTL). The redirect listener
	// currently parses but never consumes ttl (accept() dials
	// per datagram, no session pool), so this is a consistency
	// alignment only, with no behaviour change today.
	defaultTTL            = 5 * time.Minute
	defaultReadBufferSize = 4096
)

type metadata struct {
	ttl            time.Duration
	readBufferSize int
}

func (l *redirectListener) parseMetadata(md mdata.Metadata) (err error) {
	const (
		ttl            = "ttl"
		readBufferSize = "readBufferSize"
	)

	l.md.ttl = mdutil.GetDuration(md, ttl)
	if l.md.ttl <= 0 {
		l.md.ttl = defaultTTL
	}

	l.md.readBufferSize = mdutil.GetInt(md, readBufferSize)
	if l.md.readBufferSize <= 0 {
		l.md.readBufferSize = defaultReadBufferSize
	}

	return
}
