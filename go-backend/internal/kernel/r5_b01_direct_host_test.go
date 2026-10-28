// R5-B-01 (P2): ws/http-opts direct host key escapes import validation
// and the render predicate; Clash/Stash emit the bad label verbatim.
// The collector importTransportHosts read only the nested headers
// sub-map for ws/http (h2/xhttp read direct key + nested), and
// transportFields did the same, so the direct-key form was neither
// validated nor consumed on URI/sing-box faces — yet Clash/Stash pass
// the opts map through verbatim. Written red first: the import cells
// FAIL before the fix (1 node, no errors), the legal-render cells FAIL
// on transportFields (host == ""), and the stored-row cells FAIL on
// the predicate (true on every face).
package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

const r5B01BadHost = "w..w"
const r5B01LegalHost = "r5.example.net"
const r5B01UUID = "55555555-5555-5555-5555-555555555555"

// r5B01ClashVMessDirect builds a Clash VMess node whose transport Host
// is carried under the DIRECT host key of the given carrier — the exact
// reproduction input from the r5 B-domain probe.
func r5B01ClashVMessDirect(carrier, key, value string) string {
	var opts string
	switch carrier {
	case "ws":
		opts = "    network: ws\n    ws-opts:\n      path: /ws\n      " + key + ": " + value + "\n"
	case "http":
		opts = "    network: http\n    http-opts:\n      path: /http\n      " + key + ": " + value + "\n"
	}
	return "proxies:\n" +
		"  - name: R5B01-" + carrier + "-" + key + "\n" +
		"    type: vmess\n" +
		"    server: 203.0.113.10\n" +
		"    port: 443\n" +
		"    uuid: " + r5B01UUID + "\n" +
		"    tls: true\n" +
		"    servername: example.com\n" +
		opts
}

func TestR5B01DirectHostRejectedAtImport(t *testing.T) {
	// ws/http x {Host, host, HOST} direct, string value: every cell
	// must be rejected at import with the Host wording naming the value.
	for _, carrier := range []string{"ws", "http"} {
		for _, key := range []string{"Host", "host", "HOST"} {
			nodes, errs := ParseImport(r5B01ClashVMessDirect(carrier, key, r5B01BadHost))
			if len(nodes) != 0 || len(errs) == 0 {
				t.Errorf("carrier=%s key=%s: ParseImport = %d nodes, errs=%v, want 0 nodes with an error",
					carrier, key, len(nodes), errs)
				continue
			}
			if !strings.Contains(errs[0], "Host 格式错误") || !strings.Contains(errs[0], r5B01BadHost) {
				t.Errorf("carrier=%s key=%s: err = %q, want it to name the bad Host value",
					carrier, key, errs[0])
			}
		}
	}
	// List-form direct value (Clash YAML `host: [w..w]`) must be
	// rejected too — headerHostValues expands list values element-wise.
	for _, carrier := range []string{"ws", "http"} {
		nodes, errs := ParseImport(r5B01ClashVMessDirect(carrier, "host", "["+r5B01BadHost+"]"))
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("carrier=%s list-form: ParseImport = %d nodes, errs=%v, want 0 nodes with an error",
				carrier, len(nodes), errs)
		}
	}
}

func TestR5B01DirectHostLegalImportsAndRenders(t *testing.T) {
	// Legal direct Host must still import, and the render-side reader
	// must see it on every face: transportFields used to return
	// host == "" for this form, so the same stored row was seen
	// differently on each face (Clash/Stash carried the value,
	// URI/sing-box rendered empty host).
	for _, carrier := range []string{"ws", "http"} {
		for _, key := range []string{"Host", "host", "HOST"} {
			n := mustImportOne(t, r5B01ClashVMessDirect(carrier, key, r5B01LegalHost))
			host, _, _, _ := transportFields(n.Proxy)
			if host != r5B01LegalHost {
				t.Errorf("carrier=%s key=%s: transportFields host = %q, want %q",
					carrier, key, host, r5B01LegalHost)
			}
			e := Endpoint{Name: "r5b-legal-" + carrier + "-" + key, Host: "203.0.113.10", Port: 443, Direct: true, Proxy: n.Proxy}
			if link, ok := proxyURI(e); ok {
				// VMess links are base64 JSON, not query strings:
				// decode the payload and check the host field.
				raw := strings.TrimPrefix(link, "vmess://")
				payload, err := base64.StdEncoding.DecodeString(raw)
				if err != nil {
					payload, err = base64.RawStdEncoding.DecodeString(raw)
				}
				if err != nil {
					t.Fatalf("carrier=%s key=%s: vmess link not base64: %v", carrier, key, err)
				}
				var decoded map[string]interface{}
				if err := json.Unmarshal(payload, &decoded); err != nil {
					t.Fatalf("carrier=%s key=%s: vmess link not JSON: %v", carrier, key, err)
				}
				if decoded["host"] != r5B01LegalHost {
					t.Errorf("carrier=%s key=%s: URI link host = %v, want %s: %s",
						carrier, key, decoded["host"], r5B01LegalHost, link)
				}
			} else {
				t.Errorf("carrier=%s key=%s: proxyURI must render the legal node", carrier, key)
			}
			body, _, err := Subscription(FormatStash, []Endpoint{e}, SubOptions{})
			if err != nil {
				t.Fatalf("carrier=%s key=%s: stash subscription: %v", carrier, key, err)
			}
			if !strings.Contains(body, e.Name) || !strings.Contains(body, r5B01LegalHost) {
				t.Errorf("carrier=%s key=%s: stash body must contain the node name and host:\n%s",
					carrier, key, body)
			}
		}
	}
}

// r5B01StoredProxy builds a stored VMess row exactly as it sits in the
// DB after an import that escaped the old check: the bad Host under
// the direct host key of the given carrier.
func r5B01StoredProxy(network, optsKey string, opts Proxy) Proxy {
	p := Proxy{
		"type": "vmess", "server": "203.0.113.10", "port": 443,
		"uuid": r5B01UUID, "network": network,
	}
	p[optsKey] = opts
	return p
}

func TestR5B01DirectHostSkippedAtRender(t *testing.T) {
	cells := []struct {
		name    string
		network string
		optsKey string
		opts    Proxy
	}{
		{"ws-direct-Host", "ws", "ws-opts",
			Proxy{"Host": r5B01BadHost, "path": "/ws"}},
		{"ws-direct-host-list", "ws", "ws-opts",
			Proxy{"host": []interface{}{r5B01BadHost}, "path": "/ws"}},
		{"http-direct-Host", "http", "http-opts",
			Proxy{"Host": r5B01BadHost, "path": "/http"}},
		{"http-direct-host-list", "http", "http-opts",
			Proxy{"host": []interface{}{r5B01BadHost}, "path": "/http"}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Name: "r5b-stored-" + c.name, Host: "203.0.113.10", Port: 443, Direct: true,
				Proxy: r5B01StoredProxy(c.network, c.optsKey, c.opts)}
			// The shared predicate must be false on every face
			// constant, so no face can emit the row.
			for _, face := range []string{"clash", "other", "singbox", "xray-chain"} {
				if validImportedProxyFields(e.Proxy, face) {
					t.Errorf("stored bad direct host: predicate(%s) = true, want false", face)
				}
			}
			// The recovery face shares the same collector, so it
			// must refuse the row too.
			if err := ValidateImportedProxy(e.Proxy); err == nil {
				t.Errorf("stored bad direct host: ValidateImportedProxy = nil, want an error")
			}
			uri, sb, cl, sg, ln, qx, _ := r3D01FaceResults(e)
			if uri || sb || cl || sg || ln || qx {
				t.Errorf("stored bad direct host: faces uri/singbox/clash/surge/loon/qx = %v/%v/%v/%v/%v/%v, want all false (skipped)",
					uri, sb, cl, sg, ln, qx)
			}
			// Stash shares the Clash render path: the body must not
			// carry the bad value or the node name.
			body, _, err := Subscription(FormatStash, []Endpoint{e}, SubOptions{})
			if err != nil {
				t.Fatalf("stash subscription: %v", err)
			}
			if strings.Contains(body, r5B01BadHost) || strings.Contains(body, e.Name) {
				t.Errorf("stored bad direct host: stash body must not contain the bad value or the node name:\n%s", body)
			}
		})
	}
}

func TestR5B01NestedPriorityUnchanged(t *testing.T) {
	// Nested headers remain canonical for ws/http: when both forms are
	// present, the nested value wins, so no existing nested-key row can
	// change its rendering. The direct key is a fallback only.
	for _, carrier := range []string{"ws", "http"} {
		optsKey := carrier + "-opts"
		p := Proxy{
			"type": "vmess", "server": "203.0.113.10", "port": 443,
			"uuid": r5B01UUID, "network": carrier,
			optsKey: Proxy{
				"host":    "direct.example.net",
				"headers": Proxy{"Host": "nested.example.net"},
				"path":    "/x",
			},
		}
		host, _, _, _ := transportFields(p)
		if host != "nested.example.net" {
			t.Errorf("carrier=%s both forms: transportFields host = %q, want nested value", carrier, host)
		}
		// Direct-only and nested-only rows keep rendering their value.
		p[optsKey] = Proxy{"host": "direct.example.net", "path": "/x"}
		if host, _, _, _ := transportFields(p); host != "direct.example.net" {
			t.Errorf("carrier=%s direct-only: transportFields host = %q, want direct value", carrier, host)
		}
		p[optsKey] = Proxy{"headers": Proxy{"Host": "nested.example.net"}, "path": "/x"}
		if host, _, _, _ := transportFields(p); host != "nested.example.net" {
			t.Errorf("carrier=%s nested-only: transportFields host = %q, want nested value", carrier, host)
		}
	}
}

// TestR5B01EnumerationRescan is the batch's full enumeration rescan:
// carrier x form x spelling x value-form. For bad values the collector
// must see the value (so validation can reject it); for legal values
// the render reader must return it (first element for lists). Any cell
// missing from either side is a rescan difference.
func TestR5B01EnumerationRescan(t *testing.T) {
	carriers := []struct {
		network string
		optsKey string
	}{
		{"ws", "ws-opts"},
		{"http", "http-opts"},
		{"h2", "h2-opts"},
		{"xhttp", "xhttp-opts"},
	}
	spellings := []string{"Host", "host", "HOST", "hOsT"}
	for _, c := range carriers {
		for _, form := range []string{"direct", "nested"} {
			for _, spelling := range spellings {
				for _, valueForm := range []string{"string", "list"} {
					// Bad cells: collector must contain the bad value.
					badVal := r5B01BadHost
					var badStored interface{}
					if valueForm == "list" {
						badStored = []interface{}{badVal}
					} else {
						badStored = badVal
					}
					var badOpts Proxy
					if form == "direct" {
						badOpts = Proxy{spelling: badStored, "path": "/x"}
					} else {
						badOpts = Proxy{"headers": Proxy{spelling: badStored}, "path": "/x"}
					}
					badProxy := Proxy{
						"type": "vmess", "server": "203.0.113.10", "port": 443,
						"uuid": r5B01UUID, "network": c.network, c.optsKey: badOpts,
					}
					found := false
					for _, v := range importTransportHosts(badProxy) {
						if v == badVal {
							found = true
						}
					}
					if !found {
						t.Errorf("carrier=%s form=%s spelling=%s valueForm=%s: collector lacks bad value %q (rescan difference)",
							c.network, form, spelling, valueForm, badVal)
					}
					// Legal cells: render reader must return the value
					// (first element for lists).
					var legalStored interface{}
					if valueForm == "list" {
						legalStored = []interface{}{r5B01LegalHost}
					} else {
						legalStored = r5B01LegalHost
					}
					var legalOpts Proxy
					if form == "direct" {
						legalOpts = Proxy{spelling: legalStored, "path": "/x"}
					} else {
						legalOpts = Proxy{"headers": Proxy{spelling: legalStored}, "path": "/x"}
					}
					legalProxy := Proxy{
						"type": "vmess", "server": "203.0.113.10", "port": 443,
						"uuid": r5B01UUID, "network": c.network, c.optsKey: legalOpts,
					}
					if host, _, _, _ := transportFields(legalProxy); host != r5B01LegalHost {
						t.Errorf("carrier=%s form=%s spelling=%s valueForm=%s: transportFields host = %q, want %q (rescan difference)",
							c.network, form, spelling, valueForm, host, r5B01LegalHost)
					}
				}
			}
		}
	}
}
