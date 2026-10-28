// R3-D-01 (P2): stored bad-label transport Host rows render verbatim
// into subscriptions. The pre-B-01 import check only read one spelling
// per carrier (ws/http-opts.headers "Host", h2/xhttp-opts "host"), so a
// bad label under the opposite spelling escaped validation and landed
// in storage; B-01 closed the import gate but left the stored rows,
// and R2-L-01 (render-side dual read) widened their emission from 1
// carrier (ws) to 4 (ws/http/h2/xhttp). The render faces must give the
// same verdict as the import face (K-01 stored-poison precedent: whole
// stock nodes failing the shared predicate are skipped at render).
// Written red first: the four cells below FAIL before the fix.
package kernel

import (
	"strings"
	"testing"
)

const r3D01BadHost = "a..b"

// r3D01StoredProxy builds a stored VLESS row exactly as it sits in the
// DB after a pre-B-01 import: the bad Host under the spelling the old
// single-spelling import check did not read. Value shapes mirror the
// R2-L-01 regression test (Clash YAML-decoded forms).
func r3D01StoredProxy(network, optsKey string, opts Proxy) Proxy {
	p := Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443,
		"uuid": manualUUID, "network": network,
	}
	p[optsKey] = opts
	return p
}

// r3D01FaceResults reports whether each of the seven render faces emits
// the node (chain returns the error instead of a bool: a refusal counts
// as skipped).
func r3D01FaceResults(e Endpoint) (uri, singbox, clash, surge, loon, qx bool, chainErr error) {
	if _, ok := proxyURI(e); ok {
		uri = true
	}
	if _, ok := proxySingbox(e); ok {
		singbox = true
	}
	if _, ok := proxyClash(e); ok {
		clash = true
	}
	if _, ok := proxySurge(e); ok {
		surge = true
	}
	if _, ok := loonLine(e.Proxy.forEndpoint(e)); ok {
		loon = true
	}
	if _, ok := qxLine(e.Proxy.forEndpoint(e)); ok {
		qx = true
	}
	_, chainErr = chainTargetFromProxy(e)
	return
}

func TestR3D01StoredBadHostSkippedAtRender(t *testing.T) {
	cells := []struct {
		name    string
		network string
		optsKey string
		opts    Proxy
	}{
		{"ws-lowercase-host", "ws", "ws-opts",
			Proxy{"headers": Proxy{"host": r3D01BadHost}, "path": "/ws"}},
		{"http-lowercase-host", "http", "http-opts",
			Proxy{"headers": Proxy{"host": []interface{}{r3D01BadHost}}, "path": "/h"}},
		{"h2-capital-Host", "h2", "h2-opts",
			Proxy{"Host": []interface{}{r3D01BadHost}, "path": "/h2"}},
		{"xhttp-capital-Host", "xhttp", "xhttp-opts",
			Proxy{"Host": r3D01BadHost, "path": "/xhttp", "mode": "auto"}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Name: "r3d-stored-" + c.name, Host: "1.2.3.4", Port: 443, Direct: true,
				Proxy: r3D01StoredProxy(c.network, c.optsKey, c.opts)}
			uri, sb, cl, sg, ln, qx, chainErr := r3D01FaceResults(e)
			if uri || sb || cl || sg || ln || qx {
				t.Errorf("stored bad host: faces uri/singbox/clash/surge/loon/qx = %v/%v/%v/%v/%v/%v, want all false (skipped)",
					uri, sb, cl, sg, ln, qx)
			}
			if chainErr == nil {
				t.Errorf("stored bad host: chainTargetFromProxy must refuse the node")
			}
		})
	}
}

func TestR3D01StoredLegalHostRenders(t *testing.T) {
	// Legal controls: the same four cells with a valid Host must keep
	// rendering on every face that supports the carrier — the fix must
	// not over-skip. Expectations mirror the R2-L-01 regression test
	// and the D probe's per-face carrier support (Surge has no VLESS
	// client; Loon/QX support no http/h2/xhttp carrier; sing-box has
	// no xhttp transport; the Xray chain refuses http/h2 networks by
	// design).
	cells := []struct {
		name                    string
		network                 string
		optsKey                 string
		opts                    Proxy
		uri, sb, cl, sg, ln, qx bool
		chainOK                 bool
	}{
		{"ws-lowercase-host", "ws", "ws-opts",
			Proxy{"headers": Proxy{"host": "example.com"}, "path": "/ws"},
			true, true, true, false, true, true, true},
		{"http-lowercase-host", "http", "http-opts",
			Proxy{"headers": Proxy{"host": []interface{}{"example.com"}}, "path": "/h"},
			true, true, true, false, false, false, false},
		{"h2-capital-Host", "h2", "h2-opts",
			Proxy{"Host": []interface{}{"example.com"}, "path": "/h2"},
			true, true, true, false, false, false, false},
		{"xhttp-capital-Host", "xhttp", "xhttp-opts",
			Proxy{"Host": "example.com", "path": "/xhttp", "mode": "auto"},
			true, false, true, false, false, false, true},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Name: "r3d-legal-" + c.name, Host: "1.2.3.4", Port: 443, Direct: true,
				Proxy: r3D01StoredProxy(c.network, c.optsKey, c.opts)}
			uri, sb, cl, sg, ln, qx, chainErr := r3D01FaceResults(e)
			if uri != c.uri || sb != c.sb || cl != c.cl || sg != c.sg || ln != c.ln || qx != c.qx {
				t.Errorf("legal host: faces uri/singbox/clash/surge/loon/qx = %v/%v/%v/%v/%v/%v, want %v/%v/%v/%v/%v/%v",
					uri, sb, cl, sg, ln, qx, c.uri, c.sb, c.cl, c.sg, c.ln, c.qx)
			}
			if (chainErr == nil) != c.chainOK {
				t.Errorf("legal host: chain err = %v, want chainOK=%v", chainErr, c.chainOK)
			}
			if c.uri {
				u, _ := proxyURI(e)
				if !strings.Contains(u, "host=example.com") {
					t.Errorf("legal host: URI face must carry host=example.com, got %q", u)
				}
				if strings.Contains(u, "a..b") {
					t.Errorf("legal host: URI face must not contain the bad value, got %q", u)
				}
			}
		})
	}
}

// TestR3D01StoredBadSNIAndPluginHostSkipped pins the mechanism full
// set: the render predicate must give the import face's verdict on
// every hostname field it validates — SNI and plugin Hosts as well as
// transport Hosts — so no neighbouring column can recur as the next
// residual. (Plugin carrier read points are R3-L-01's batch; this
// test only pins that a bad label is skipped, never that a legal
// value is read.)
func TestR3D01StoredBadSNIAndPluginHostSkipped(t *testing.T) {
	faces := []string{ssFaceClash, ssFaceOther, ssFaceSingbox, ssFaceXrayChain}

	// Bad SNI stored row.
	badSNI := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID,
		"tls": true, "servername": r3D01BadHost}
	for _, f := range faces {
		if validImportedProxyFields(badSNI, f) {
			t.Errorf("bad SNI stored row: predicate must be false on %s face", f)
		}
	}
	legalSNI := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID,
		"tls": true, "servername": "example.com"}
	for _, f := range faces {
		if !validImportedProxyFields(legalSNI, f) {
			t.Errorf("legal SNI stored row: predicate must be true on %s face", f)
		}
	}
	e := Endpoint{Name: "r3d-sni", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: badSNI}
	if _, ok := proxyURI(e); ok {
		t.Errorf("bad SNI stored row: URI face must skip the node")
	}
	e.Proxy = legalSNI
	if _, ok := proxyURI(e); !ok {
		t.Errorf("legal SNI stored row: URI face must render the node")
	}

	// Bad plugin Host stored row (SS v2ray-plugin).
	badPlugin := Proxy{"type": "ss", "server": "1.2.3.4", "port": 443,
		"cipher": "aes-256-gcm", "password": "password12345678",
		"plugin":      "v2ray-plugin",
		"plugin-opts": Proxy{"mode": "websocket", "host": r3D01BadHost}}
	for _, f := range faces {
		if validImportedProxyFields(badPlugin, f) {
			t.Errorf("bad plugin Host stored row: predicate must be false on %s face", f)
		}
	}
	legalPlugin := Proxy{"type": "ss", "server": "1.2.3.4", "port": 443,
		"cipher": "aes-256-gcm", "password": "password12345678",
		"plugin":      "v2ray-plugin",
		"plugin-opts": Proxy{"mode": "websocket", "host": "example.com"}}
	for _, f := range faces {
		if !validImportedProxyFields(legalPlugin, f) {
			t.Errorf("legal plugin Host stored row: predicate must be true on %s face", f)
		}
	}
	pe := Endpoint{Name: "r3d-plugin", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: badPlugin}
	if _, ok := proxyURI(pe); ok {
		t.Errorf("bad plugin Host stored row: URI face must skip the node")
	}
	pe.Proxy = legalPlugin
	if _, ok := proxyURI(pe); !ok {
		t.Errorf("legal plugin Host stored row: URI face must render the node")
	}
}

// TestR3D01ImportStillRejectsSameBadValue is the negative control:
// the same bad value must still be rejected (and named) at import,
// so import and render give one verdict on the same row.
func TestR3D01ImportStillRejectsSameBadValue(t *testing.T) {
	p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID, "network": "http",
		"http-opts": Proxy{"headers": Proxy{"host": r3D01BadHost}}}
	if _, err := finishProxy(p); err == nil || !strings.Contains(err.Error(), "Host 格式错误") {
		t.Fatalf("bad label import must be rejected naming Host 格式错误，got err=%v", err)
	}
}
