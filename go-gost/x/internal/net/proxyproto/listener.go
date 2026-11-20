package proxyproto

import (
	"net"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
)

func WrapListener(ppv int, ln net.Listener, readHeaderTimeout time.Duration) net.Listener {
	if ppv <= 0 {
		return ln
	}

	return &proxyproto.Listener{
		Listener:          ln,
		ReadHeaderTimeout: readHeaderTimeout,
	}
}

// WrapListenerWithAllowlist wraps ln with PROXY protocol parsing
// only when the caller supplies a non-empty allowlist of trusted
// PROXY senders (R18-C5-01). The allowlist is enforced with a Lax
// whitelist policy: a PROXY header from a non-allowlisted peer is
// ignored (the connection still works, but its address stays the
// real peer — a forger can no longer fake the "real client IP" in
// the connection log), while an allowlisted sender's header is
// honoured. With ppv <= 0 or an empty allowlist the bare listener
// is returned (fail-closed: no PROXY header is parsed at all); a
// policy-construction error also returns the bare listener plus
// the error, so the caller can log it and keep serving.
func WrapListenerWithAllowlist(ppv int, allowlist []string, ln net.Listener, readHeaderTimeout time.Duration) (net.Listener, error) {
	if ppv <= 0 || len(allowlist) == 0 {
		return ln, nil
	}
	policy, err := proxyproto.LaxWhiteListPolicy(allowlist)
	if err != nil {
		return ln, err
	}
	return &proxyproto.Listener{
		Listener:          ln,
		Policy:            policy,
		ReadHeaderTimeout: readHeaderTimeout,
	}, nil
}
