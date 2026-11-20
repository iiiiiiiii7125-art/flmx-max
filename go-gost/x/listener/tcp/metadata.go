package tcp

import (
	md "github.com/go-gost/core/metadata"
	mdutil "github.com/go-gost/x/metadata/util"
)

type metadata struct {
	mptcp                  bool
	proxyProtocolAllowlist []string
}

func (l *tcpListener) parseMetadata(md md.Metadata) (err error) {
	l.md.mptcp = mdutil.GetBool(md, "mptcp")
	// R18-C5-01: trusted PROXY senders for this entry, from the
	// panel (listener metadata "proxyProtocolAllowlist"). The
	// list is only honoured together with ProxyProtocol > 0 in
	// Init; an empty/absent list means receive stays off.
	l.md.proxyProtocolAllowlist = mdutil.GetStrings(md, "proxyProtocolAllowlist")

	return
}
