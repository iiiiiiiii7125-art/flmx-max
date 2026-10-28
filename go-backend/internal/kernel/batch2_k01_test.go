// Batch 2 of the FLMX 1.7.1 post-release full audit (baseline main @
// c2f45a6): regression tests for K-01 — the import face accepted the
// transport x security combinations the self-built face forbids
// (Vision+WS/gRPC, Reality+WS), poisoning subscriptions. Written
// red first.
//
// Correction (batch2-correction, K-01 over-rejection): the batch-2
// predicate barred Reality on every non-TCP transport, but the
// capabilities matrix allows Reality on TCP/gRPC/XHTTP (Xray VLESS)
// and TCP/gRPC (sing-box VLESS, Trojan both kernels) — only WS is
// banned (no transport row lists Reality under WS), and Normalize
// enforces exactly that matrix. Reality+gRPC and Reality+XHTTP are
// therefore legal and were wrongly rejected; this file now pins the
// matrix-derived expectations.
package kernel

import (
	"strings"
	"testing"
)

func TestBatch2K01TransportSecurityCoupling(t *testing.T) {
	// The K probe groups that must be rejected with an explicit error:
	// Vision is TCP-only (visionOK: VLESS + TCP), Reality is banned
	// only on WS (capabilities matrix: no row lists Reality under WS).
	bad := []struct {
		name string
		link string
		want string
	}{
		{"vision+ws",
			"vless://" + manualUUID + "@1.2.3.4:443?security=tls&type=ws&path=%2Fws&flow=xtls-rprx-vision#ws-vision",
			"xtls-rprx-vision"},
		{"vision+grpc",
			"vless://" + manualUUID + "@1.2.3.4:443?security=tls&type=grpc&serviceName=svc&flow=xtls-rprx-vision#grpc-vision",
			"xtls-rprx-vision"},
		{"vision+xhttp",
			"vless://" + manualUUID + "@1.2.3.4:443?security=tls&type=xhttp&path=%2Fxhttp&mode=auto&flow=xtls-rprx-vision#xhttp-vision",
			"xtls-rprx-vision"},
		{"reality+ws",
			"vless://" + manualUUID + "@1.2.3.4:443?security=reality&sni=www.microsoft.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12&type=ws&path=%2Fws#ws-reality",
			"Reality"},
	}
	for _, c := range bad {
		nodes, errs := ParseImport(c.link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with an error", c.name, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], c.want) {
			t.Errorf("%s: err = %q, want it to name %q", c.name, errs[0], c.want)
		}
	}
	// Legal combinations must still import. Reality+gRPC and
	// Reality+XHTTP are legal per the capabilities matrix (Xray VLESS:
	// TCP/XHTTP/gRPC; sing-box VLESS and Trojan: TCP/gRPC) — the
	// batch-2 predicate wrongly rejected them.
	for _, link := range []string{
		"vless://" + manualUUID + "@1.2.3.4:443?security=tls&type=tcp&flow=xtls-rprx-vision#tcp-vision",
		"vless://" + manualUUID + "@1.2.3.4:443?security=reality&sni=www.microsoft.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12&type=tcp#tcp-reality",
		"vless://" + manualUUID + "@1.2.3.4:443?security=reality&sni=www.microsoft.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12&type=tcp&flow=xtls-rprx-vision#tcp-reality-vision",
		"vless://" + manualUUID + "@1.2.3.4:443?security=tls&type=ws&path=%2Fws#ws-tls",
		"vless://" + manualUUID + "@1.2.3.4:443?security=tls&type=grpc&serviceName=svc#grpc-tls",
		"vless://" + manualUUID + "@1.2.3.4:443?security=reality&sni=www.microsoft.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12&type=grpc&serviceName=svc#grpc-reality",
		"vless://" + manualUUID + "@1.2.3.4:443?security=reality&sni=www.microsoft.com&fp=chrome&pbk=" + manualFixPBK + "&sid=ab12&type=xhttp&path=%2Fxhttp&mode=auto#xhttp-reality",
		"trojan://pw@1.2.3.4:443?security=reality&sni=www.microsoft.com&pbk=" + manualFixPBK + "&type=grpc&serviceName=svc#trojan-grpc-reality",
	} {
		mustImportOne(t, link)
	}
	// VMess + Reality is TCP-legal but WS-illegal, same rule.
	mustImportOne(t, r2vmessLink(nil))
	nodes, errs := ParseImport(r2vmessLink(map[string]interface{}{"net": "ws", "host": "example.com", "path": "/ws"}))
	if len(nodes) != 0 || len(errs) == 0 {
		t.Fatalf("vmess reality+ws: nodes=%d errs=%v, want 0 nodes with an error", len(nodes), errs)
	}
	if !strings.Contains(errs[0], "Reality") {
		t.Errorf("vmess reality+ws: err = %q, want it to name Reality", errs[0])
	}
}

// TestBatch2K01StoredPoisonedSkippedAtRender pins the stored-row
// disposition: rows imported before the fix (illegal transport x
// security) are isolated at render — every face skips them, exactly
// like the other predicate failures in validImportedProxyFields —
// instead of being served verbatim into subscriptions.
func TestBatch2K01StoredPoisonedSkippedAtRender(t *testing.T) {
	poisoned := []Proxy{
		{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID,
			"tls": true, "servername": "example.com", "flow": "xtls-rprx-vision",
			"network": "ws", "ws-opts": map[string]interface{}{"path": "/ws"}},
		{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID,
			"tls": true, "servername": "www.microsoft.com", "client-fingerprint": "chrome",
			"reality-opts": map[string]interface{}{"public-key": manualFixPBK, "short-id": "ab12"},
			"network": "ws", "ws-opts": map[string]interface{}{"path": "/ws"}},
	}
	for i, p := range poisoned {
		e := Endpoint{Name: "k01-poisoned", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: p}
		sb, sg, ln, qx, uri := b2FaceResults(e)
		if sb || sg || ln || qx || uri {
			t.Errorf("poisoned[%d]: faces singbox/surge/loon/qx/uri = %v/%v/%v/%v/%v, want all false (skipped)",
				i, sb, sg, ln, qx, uri)
		}
		if _, ok := proxyClash(e); ok {
			t.Errorf("poisoned[%d]: proxyClash must skip the node", i)
		}
		if _, err := chainTargetFromProxy(e); err == nil {
			t.Errorf("poisoned[%d]: chainTargetFromProxy must refuse the node", i)
		}
	}
	// Legal stored rows still render on every face that supports
	// VLESS (Surge has no VLESS client by design, N-10).
	legal := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID,
		"tls": true, "servername": "www.microsoft.com", "client-fingerprint": "chrome",
		"reality-opts": map[string]interface{}{"public-key": manualFixPBK, "short-id": "ab12"},
		"flow":         "xtls-rprx-vision"}
	e := Endpoint{Name: "k01-legal", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: legal}
	sb, sg, ln, qx, uri := b2FaceResults(e)
	if !(sb && !sg && ln && qx && uri) {
		t.Errorf("legal reality+tcp+vision faces singbox/surge/loon/qx/uri = %v/%v/%v/%v/%v, want true/false/true/true/true", sb, sg, ln, qx, uri)
	}
	if _, ok := proxyClash(e); !ok {
		t.Error("legal reality+tcp+vision must render on the Clash face")
	}
	if _, err := chainTargetFromProxy(e); err != nil {
		t.Errorf("legal reality+tcp+vision chainTargetFromProxy: %v", err)
	}
	// Correction: Reality+gRPC and Reality+XHTTP stored rows are legal
	// per the matrix and must render on every face that supports the
	// transport — the batch-2 predicate wrongly isolated them.
	// Reality+gRPC: sing-box renders (grpc transport + reality TLS),
	// Clash passes through, URI round-trips type=grpc + security=
	// reality; Surge has no VLESS client, Loon/QX support tcp/ws
	// transports only. Xray chain accepts grpc + reality.
	grpcLegal := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID,
		"tls": true, "servername": "www.microsoft.com", "client-fingerprint": "chrome",
		"reality-opts": map[string]interface{}{"public-key": manualFixPBK, "short-id": "ab12"},
		"network": "grpc", "grpc-opts": map[string]interface{}{"grpc-service-name": "svc"}}
	ge := Endpoint{Name: "k01-legal-grpc", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: grpcLegal}
	sb, sg, ln, qx, uri = b2FaceResults(ge)
	if !(sb && !sg && !ln && !qx && uri) {
		t.Errorf("legal reality+grpc faces singbox/surge/loon/qx/uri = %v/%v/%v/%v/%v, want true/false/false/false/true", sb, sg, ln, qx, uri)
	}
	if _, ok := proxyClash(ge); !ok {
		t.Error("legal reality+grpc must render on the Clash face")
	}
	if _, err := chainTargetFromProxy(ge); err != nil {
		t.Errorf("legal reality+grpc chainTargetFromProxy: %v", err)
	}
	// Reality+XHTTP: sing-box has no xhttp transport (always skipped,
	// OBS-D-01), Loon/QX/Surge exclude XHTTP by client capability;
	// Clash renders xhttp-opts (Mihomo ≥1.19), URI round-trips
	// type=xhttp + security=reality, Xray chain emits xhttpSettings
	// with realitySettings.
	xhttpLegal := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": manualUUID,
		"tls": true, "servername": "www.microsoft.com", "client-fingerprint": "chrome",
		"reality-opts": map[string]interface{}{"public-key": manualFixPBK, "short-id": "ab12"},
		"network": "xhttp", "xhttp-opts": map[string]interface{}{"path": "/xhttp", "mode": "auto"}}
	xe := Endpoint{Name: "k01-legal-xhttp", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: xhttpLegal}
	sb, sg, ln, qx, uri = b2FaceResults(xe)
	if !(!sb && !sg && !ln && !qx && uri) {
		t.Errorf("legal reality+xhttp faces singbox/surge/loon/qx/uri = %v/%v/%v/%v/%v, want false/false/false/false/true", sb, sg, ln, qx, uri)
	}
	if _, ok := proxyClash(xe); !ok {
		t.Error("legal reality+xhttp must render on the Clash face")
	}
	if _, err := chainTargetFromProxy(xe); err != nil {
		t.Errorf("legal reality+xhttp chainTargetFromProxy: %v", err)
	}
}
