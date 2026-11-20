package kernel

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// R18-C2-01 (D-2): sing-box 链式/订阅出站对自签目标静默渲染
// tls.insecure=true。裁决为警告 + 文档明示（fail-closed 列入下个大
// 版本），渲染行为本身一字不改：以下测试断言 insecure=true 仍照常
// 渲染（存量链路不断），同时断言每次渲染都发出点名目标的警告日志。

// r18c201CaptureLog redirects the std logger into a buffer for the
// duration of the test (same pattern as TestR14F03SplitFallbackWhenIdleOutboundMissing).
func r18c201CaptureLog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	return &buf, func() { log.SetOutput(old) }
}

func r18c201TLSBlock(t *testing.T, obs []map[string]interface{}) map[string]interface{} {
	t.Helper()
	if len(obs) == 0 {
		t.Fatal("no sing-box outbound rendered")
	}
	tls, ok := obs[0]["tls"].(map[string]interface{})
	if !ok {
		t.Fatalf("outbound has no tls block: %v", obs[0])
	}
	return tls
}

// TestR18C201SelfSignedSingboxWarnsButRendersInsecure is the core
// regression test: a self-signed target still renders tls.insecure=true
// byte-for-byte (existing chains keep working), and the render emits a
// warning naming the target address and the skipped verification.
func TestR18C201SelfSignedSingboxWarnsButRendersInsecure(t *testing.T) {
	eps := shareFixtures(t)
	e := eps[3] // Trojan, gRPC, TLS, self-signed (golden fixture id 4)
	if !e.selfSigned() {
		t.Fatal("fixture must be self-signed")
	}
	buf, restore := r18c201CaptureLog(t)
	defer restore()
	obs, ok := SingboxOutbounds(e)
	if !ok {
		t.Fatal("self-signed endpoint must still render")
	}
	if got := r18c201TLSBlock(t, obs)["insecure"]; got != true {
		t.Fatalf("tls.insecure = %v, want true (render behavior must not change)", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "203.0.113.10:2053") {
		t.Fatalf("warning must name the target address, got log: %q", logged)
	}
	if !strings.Contains(logged, "tls.insecure=true") || !strings.Contains(logged, "跳过") {
		t.Fatalf("warning must state tls.insecure=true and skipped verification, got log: %q", logged)
	}
}

// TestR18C201ChainOutboundWarns proves the same warning fires on the
// chain render path (singboxChainOutboundsWith -> singboxOutboundsWith
// -> singboxOutbound), which is how a node dials the next hop.
func TestR18C201ChainOutboundWarns(t *testing.T) {
	eps := shareFixtures(t)
	e := eps[3] // Trojan, gRPC, TLS, self-signed
	buf, restore := r18c201CaptureLog(t)
	defer restore()
	obs, err := singboxChainOutboundsWith(e, "chain-test", 0, false, true)
	if err != nil {
		t.Fatalf("chain render: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("no chain outbound rendered")
	}
	first, ok := obs[0].(map[string]interface{})
	if !ok {
		t.Fatalf("chain outbound is not an object: %T", obs[0])
	}
	tls, ok := first["tls"].(map[string]interface{})
	if !ok || tls["insecure"] != true {
		t.Fatalf("chain outbound tls.insecure = %v, want true: %v", first["tls"], first)
	}
	logged := buf.String()
	if !strings.Contains(logged, "203.0.113.10:2053") || !strings.Contains(logged, "chain-test") {
		t.Fatalf("chain warning must name the tag and target address, got log: %q", logged)
	}
}

// TestR18C201TrustedCertSingboxSilent is the negative control: a target
// with a file (trusted) certificate renders no insecure flag and logs no
// warning.
func TestR18C201TrustedCertSingboxSilent(t *testing.T) {
	eps := shareFixtures(t)
	e := eps[2] // VMess, WS, TLS, file certificate (golden fixture id 3)
	if e.selfSigned() {
		t.Fatal("fixture must not be self-signed")
	}
	buf, restore := r18c201CaptureLog(t)
	defer restore()
	obs, ok := SingboxOutbounds(e)
	if !ok {
		t.Fatal("trusted-cert endpoint must render")
	}
	if _, has := r18c201TLSBlock(t, obs)["insecure"]; has {
		t.Fatalf("trusted-cert target must not render tls.insecure: %v", obs[0]["tls"])
	}
	if got := buf.String(); strings.Contains(got, "insecure") {
		t.Fatalf("trusted-cert render must not warn, got log: %q", got)
	}
}

// TestR18C201ImportedSkipCertVerifyWarns covers the secondary point of
// the same finding (proxy_produce.go): an imported node carrying an
// explicit skip-cert-verify flag renders tls.insecure=true and warns.
func TestR18C201ImportedSkipCertVerifyWarns(t *testing.T) {
	n := mustImportOne(t, "trojan://pass%40word@t.example.com:443?security=tls&sni=t.example.com&type=ws&path=%2Ftr&host=t.example.com&allowInsecure=1#Trojan")
	if !n.Proxy.flag("skip-cert-verify") {
		t.Fatal("fixture must carry skip-cert-verify")
	}
	e := Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Proxy: n.Proxy, Direct: true}
	buf, restore := r18c201CaptureLog(t)
	defer restore()
	obs, ok := SingboxOutbounds(e)
	if !ok {
		t.Fatal("imported node must still render")
	}
	if got := r18c201TLSBlock(t, obs)["insecure"]; got != true {
		t.Fatalf("tls.insecure = %v, want true (render behavior must not change)", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "t.example.com:443") {
		t.Fatalf("warning must name the target address, got log: %q", logged)
	}
	if !strings.Contains(logged, "tls.insecure=true") || !strings.Contains(logged, "跳过") {
		t.Fatalf("warning must state tls.insecure=true and skipped verification, got log: %q", logged)
	}
}
