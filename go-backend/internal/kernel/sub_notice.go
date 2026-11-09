package kernel

// Notices are placeholder nodes whose names tell the user something the
// client would not show otherwise: why a subscription no longer works.
// They point at 127.0.0.1:1, so selecting one connects nowhere. (The
// expiry/traffic info notices that rode the same mechanism left with
// the infoNodes option in 2.1.2; the reason notices for stopped
// subscriptions stay.)

// noticeEndpoints turns notice texts into placeholder Shadowsocks nodes.
// They are built directly: importing refuses a loopback address.
func noticeEndpoints(names []string) []Endpoint {
	out := make([]Endpoint, 0, len(names))
	for _, name := range names {
		p := Proxy{"name": name, "type": "ss", "server": "127.0.0.1", "port": 1, "cipher": "aes-128-gcm", "password": "notice"}
		out = append(out, Endpoint{Name: name, Original: name, Host: "127.0.0.1", Port: 1, Proxy: p, Direct: true})
	}
	return out
}

// withNotices puts the notices in front of the processed endpoints.
func withNotices(endpoints []Endpoint, opts SubOptions) []Endpoint {
	if len(opts.Notices) == 0 {
		return endpoints
	}
	return append(noticeEndpoints(opts.Notices), endpoints...)
}
