// R4-B-01 (P2): h2/xhttp-opts nested headers.Host escapes import
// validation and the render predicate; Clash/Stash emit the bad label
// verbatim. The collector importTransportHosts read ws/http nested
// headers but only the direct host key for h2/xhttp, and the render
// predicate validImportedProxyFields reuses the same collector, so the
// same blind spot let the value through on both faces. Written red
// first: the import cells FAIL before the fix (1 node, no errors),
// the stored-row cells FAIL (Clash/Stash/URI emit the bad value),
// and the legal nested cells FAIL on transportFields (host == "").
package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const r4B01BadHost = "z..z"
const r4B01LegalHost = "r4.example.net"
const r4B01UUID = "44444444-4444-4444-4444-444444444444"

// r4B01ClashVMessNested builds a Clash VMess node whose transport Host
// is carried under the nested headers sub-map of the given carrier —
// the exact reproduction input from the r4 B-domain probe.
func r4B01ClashVMessNested(carrier, key, value string) string {
	var opts string
	switch carrier {
	case "h2":
		opts = "    network: h2\n    h2-opts:\n      path: /h2\n      headers:\n        " + key + ": " + value + "\n"
	case "xhttp":
		opts = "    network: xhttp\n    xhttp-opts:\n      path: /x\n      mode: auto\n      headers:\n        " + key + ": " + value + "\n"
	}
	return "proxies:\n" +
		"  - name: R4B01-" + carrier + "-" + key + "\n" +
		"    type: vmess\n" +
		"    server: 203.0.113.10\n" +
		"    port: 443\n" +
		"    uuid: " + r4B01UUID + "\n" +
		"    tls: true\n" +
		"    servername: example.com\n" +
		opts
}

func TestR4B01NestedHeadersRejectedAtImport(t *testing.T) {
	// h2/xhttp x {Host, host, HOST} nested, string value: every cell
	// must be rejected at import with the Host wording naming the value.
	for _, carrier := range []string{"h2", "xhttp"} {
		for _, key := range []string{"Host", "host", "HOST"} {
			nodes, errs := ParseImport(r4B01ClashVMessNested(carrier, key, r4B01BadHost))
			if len(nodes) != 0 || len(errs) == 0 {
				t.Errorf("carrier=%s key=%s: ParseImport = %d nodes, errs=%v, want 0 nodes with an error",
					carrier, key, len(nodes), errs)
				continue
			}
			if !strings.Contains(errs[0], "Host 格式错误") || !strings.Contains(errs[0], r4B01BadHost) {
				t.Errorf("carrier=%s key=%s: err = %q, want it to name the bad Host value",
					carrier, key, errs[0])
			}
		}
	}
	// List-form nested value (Clash YAML `Host: [z..z]`) must be
	// rejected too — headerHostValues expands list values element-wise.
	for _, carrier := range []string{"h2", "xhttp"} {
		nodes, errs := ParseImport(r4B01ClashVMessNested(carrier, "Host", "["+r4B01BadHost+"]"))
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("carrier=%s list-form: ParseImport = %d nodes, errs=%v, want 0 nodes with an error",
				carrier, len(nodes), errs)
		}
	}
}

func TestR4B01NestedHeadersLegalStillImports(t *testing.T) {
	// Legal nested Host must still import, and the render-side reader
	// must see it: transportFields used to return host == "" for this
	// form, so the same stored row was seen differently on each face
	// (Clash/Stash carried the value, URI/sing-box rendered empty host).
	for _, carrier := range []string{"h2", "xhttp"} {
		for _, key := range []string{"Host", "host"} {
			n := mustImportOne(t, r4B01ClashVMessNested(carrier, key, r4B01LegalHost))
			host, _, _, _ := transportFields(n.Proxy)
			if host != r4B01LegalHost {
				t.Errorf("carrier=%s key=%s: transportFields host = %q, want %q",
					carrier, key, host, r4B01LegalHost)
			}
		}
	}
}

// r4B01StoredProxy builds a stored VLESS row exactly as it sits in the
// DB after an import that escaped the old check: the bad Host under
// the nested headers sub-map of the given carrier.
func r4B01StoredProxy(network, optsKey string, opts Proxy) Proxy {
	p := Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443,
		"uuid": manualUUID, "network": network,
	}
	p[optsKey] = opts
	return p
}

func TestR4B01NestedHeadersSkippedAtRender(t *testing.T) {
	cells := []struct {
		name    string
		network string
		optsKey string
		opts    Proxy
	}{
		{"h2-nested-Host", "h2", "h2-opts",
			Proxy{"headers": Proxy{"Host": r4B01BadHost}, "path": "/h2"}},
		{"h2-nested-host-list", "h2", "h2-opts",
			Proxy{"headers": Proxy{"host": []interface{}{r4B01BadHost}}, "path": "/h2"}},
		{"xhttp-nested-Host", "xhttp", "xhttp-opts",
			Proxy{"headers": Proxy{"Host": r4B01BadHost}, "path": "/x", "mode": "auto"}},
		{"xhttp-nested-Host-list", "xhttp", "xhttp-opts",
			Proxy{"headers": Proxy{"Host": []interface{}{r4B01BadHost}}, "path": "/x", "mode": "auto"}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Name: "r4b-stored-" + c.name, Host: "1.2.3.4", Port: 443, Direct: true,
				Proxy: r4B01StoredProxy(c.network, c.optsKey, c.opts)}
			// The shared predicate must be false on every face
			// constant, so no face can emit the row.
			for _, face := range []string{"clash", "other", "singbox", "xray-chain"} {
				if validImportedProxyFields(e.Proxy, face) {
					t.Errorf("stored bad nested host: predicate(%s) = true, want false", face)
				}
			}
			uri, sb, cl, sg, ln, qx, chainErr := r3D01FaceResults(e)
			if uri || sb || cl || sg || ln || qx {
				t.Errorf("stored bad nested host: faces uri/singbox/clash/surge/loon/qx = %v/%v/%v/%v/%v/%v, want all false (skipped)",
					uri, sb, cl, sg, ln, qx)
			}
			if chainErr == nil {
				t.Errorf("stored bad nested host: chainTargetFromProxy must refuse the node")
			}
			// Stash shares the Clash render path: the body must not
			// carry the bad value or the node name.
			body, _, err := Subscription(FormatStash, []Endpoint{e}, SubOptions{})
			if err != nil {
				t.Fatalf("stash subscription: %v", err)
			}
			if strings.Contains(body, r4B01BadHost) || strings.Contains(body, e.Name) {
				t.Errorf("stored bad nested host: stash body must not contain the bad value or the node name:\n%s", body)
			}
		})
	}
}

func TestR4B01NestedHeadersLegalRenders(t *testing.T) {
	// Legal controls: the same nested cells with a valid Host must
	// render exactly like the legal direct-key controls in
	// TestR3D01StoredLegalHostRenders — every face consistent.
	// (Surge has no VLESS client; Loon/QX support no h2/xhttp carrier;
	// sing-box has no xhttp transport; the Xray chain refuses h2
	// networks by design.)
	cells := []struct {
		name                    string
		network                 string
		optsKey                 string
		opts                    Proxy
		uri, sb, cl, sg, ln, qx bool
		chainOK                 bool
	}{
		{"h2-nested-Host", "h2", "h2-opts",
			Proxy{"headers": Proxy{"Host": r4B01LegalHost}, "path": "/h2"},
			true, true, true, false, false, false, false},
		{"h2-nested-host-list", "h2", "h2-opts",
			Proxy{"headers": Proxy{"host": []interface{}{r4B01LegalHost}}, "path": "/h2"},
			true, true, true, false, false, false, false},
		{"xhttp-nested-Host", "xhttp", "xhttp-opts",
			Proxy{"headers": Proxy{"Host": r4B01LegalHost}, "path": "/x", "mode": "auto"},
			true, false, true, false, false, false, true},
		{"xhttp-nested-host-list", "xhttp", "xhttp-opts",
			Proxy{"headers": Proxy{"host": []interface{}{r4B01LegalHost}}, "path": "/x", "mode": "auto"},
			true, false, true, false, false, false, true},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Name: "r4b-legal-" + c.name, Host: "1.2.3.4", Port: 443, Direct: true,
				Proxy: r4B01StoredProxy(c.network, c.optsKey, c.opts)}
			host, _, _, _ := transportFields(e.Proxy)
			if host != r4B01LegalHost {
				t.Fatalf("legal nested host: transportFields host = %q, want %q", host, r4B01LegalHost)
			}
			uri, sb, cl, sg, ln, qx, chainErr := r3D01FaceResults(e)
			if uri != c.uri || sb != c.sb || cl != c.cl || sg != c.sg || ln != c.ln || qx != c.qx {
				t.Errorf("legal nested host: faces uri/singbox/clash/surge/loon/qx = %v/%v/%v/%v/%v/%v, want %v/%v/%v/%v/%v/%v",
					uri, sb, cl, sg, ln, qx, c.uri, c.sb, c.cl, c.sg, c.ln, c.qx)
			}
			if (chainErr == nil) != c.chainOK {
				t.Errorf("legal nested host: chain err = %v, want chainOK=%v", chainErr, c.chainOK)
			}
			// URI face must carry the host (not the pre-fix empty host).
			if link, ok := proxyURI(e); ok {
				if !strings.Contains(link, "host="+r4B01LegalHost) {
					t.Errorf("legal nested host: URI link lacks host=%s: %s", r4B01LegalHost, link)
				}
			} else if c.uri {
				t.Errorf("legal nested host: proxyURI must render the node")
			}
			// Clash face must carry the nested headers map verbatim.
			if node, ok := proxyClash(e); ok {
				found := false
				for _, f := range node {
					if f.Key == c.optsKey {
						if m, ok := f.Value.(yamlNode); ok {
							for _, sf := range m {
								if sf.Key == "headers" {
									if hm, ok := sf.Value.(yamlNode); ok {
										for _, hf := range hm {
											if strings.EqualFold(hf.Key, "host") {
												found = true
											}
										}
									}
								}
							}
						}
						// Clash YAML nodes may also carry the opts as a
						// plain map; accept either shape.
						if m, ok := f.Value.(map[string]interface{}); ok {
							if _, ok := m["headers"]; ok {
								found = true
							}
						}
					}
				}
				if !found {
					t.Errorf("legal nested host: clash node lacks nested headers: %v", node)
				}
			} else if c.cl {
				t.Errorf("legal nested host: proxyClash must render the node")
			}
			// Stash body must carry the node and the host.
			body, _, err := Subscription(FormatStash, []Endpoint{e}, SubOptions{})
			if err != nil {
				t.Fatalf("stash subscription: %v", err)
			}
			if !strings.Contains(body, e.Name) || !strings.Contains(body, r4B01LegalHost) {
				t.Errorf("legal nested host: stash body must contain the node name and host:\n%s", body)
			}
		})
	}
}

func TestR4B01DirectKeyPrecedenceUnchanged(t *testing.T) {
	// Direct key wins over nested headers when both are present, so
	// no existing direct-key row can change its rendering: the nested
	// read is a fallback only.
	p := Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443,
		"uuid": manualUUID, "network": "h2",
		"h2-opts": Proxy{
			"host":    []interface{}{"direct.example.net"},
			"headers": Proxy{"Host": r4B01LegalHost},
			"path":    "/h2",
		},
	}
	host, _, _, _ := transportFields(p)
	if host != "direct.example.net" {
		t.Errorf("direct+nested: transportFields host = %q, want direct key to win", host)
	}
	// And the collector sees both values: a bad nested value is still
	// caught even when the direct key is legal.
	p["h2-opts"] = Proxy{
		"host":    []interface{}{"direct.example.net"},
		"headers": Proxy{"Host": r4B01BadHost},
		"path":    "/h2",
	}
	if err := validateImportHostnames(p); err == nil {
		t.Errorf("direct legal + nested bad: validateImportHostnames must reject the bad nested value")
	} else if !strings.Contains(err.Error(), r4B01BadHost) {
		t.Errorf("direct legal + nested bad: err = %q, want it to name the bad value", err)
	}
}

func TestR4B01VMessNestedHostDecodes(t *testing.T) {
	// Cross-check on the VMess shape the B probe used: the imported
	// node's inner JSON must carry the legal nested host after the fix
	// (pre-fix it decoded to host == "").
	n := mustImportOne(t, r4B01ClashVMessNested("h2", "Host", r4B01LegalHost))
	link, ok := proxyURI(Endpoint{Name: "r4b-vmess", Host: "203.0.113.10", Port: 443, Direct: true, Proxy: n.Proxy})
	if !ok {
		t.Fatalf("vmess nested legal: proxyURI must render the node")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		t.Fatalf("vmess nested legal: base64 decode: %v", err)
	}
	var inner map[string]interface{}
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatalf("vmess nested legal: json decode: %v", err)
	}
	if inner["host"] != r4B01LegalHost {
		t.Errorf("vmess nested legal: inner host = %v, want %q", inner["host"], r4B01LegalHost)
	}
}
