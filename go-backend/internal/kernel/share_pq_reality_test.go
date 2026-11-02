package kernel

import (
	"encoding/json"
	"strings"
	"testing"
)

// Mihomo 抗量子标记渲染面：support-x25519mlkem768 只在
// VLESS + Reality + Endpoint.PostQuantumReality 三重条件齐备时
// 出现在 reality-opts 里；其余协议/安全类型/格式一律不带。

func pqRealityEndpoint(protocol string, pq bool) Endpoint {
	in := Inbound{
		Protocol:  protocol,
		Port:      443,
		Transport: Transport{Type: TransportTCP},
		Security: Security{Type: SecurityReality, SNI: "www.example.com",
			RealityPublicKey: "pubkey", RealityShortIDs: []string{"ab"}, Fingerprint: "chrome"},
	}
	switch protocol {
	case ProtocolVLESS:
		in.Settings = Settings{UUID: "22222222-2222-4222-8222-222222222222"}
	case ProtocolTrojan:
		in.Settings = Settings{Password: "pw"}
	}
	return Endpoint{Name: "pq-node", Host: "203.0.113.10", Port: 443, Inbound: in, Direct: true, PostQuantumReality: pq}
}

func TestClashProxyPostQuantumRealityMarker(t *testing.T) {
	node, ok := ClashProxy(pqRealityEndpoint(ProtocolVLESS, true))
	if !ok {
		t.Fatal("vless reality endpoint must render")
	}
	if got := marshalYAML(node); !strings.Contains(got, "support-x25519mlkem768: true") {
		t.Fatalf("flagged VLESS+Reality endpoint must carry the PQ marker:\n%s", got)
	}

	node, ok = ClashProxy(pqRealityEndpoint(ProtocolVLESS, false))
	if !ok {
		t.Fatal("vless reality endpoint must render")
	}
	if got := marshalYAML(node); strings.Contains(got, "support-x25519mlkem768") {
		t.Fatalf("unflagged endpoint must NOT carry the PQ marker:\n%s", got)
	}

	// 协议门：Trojan + Reality 即使标志为真也不加（标记是 VLESS 专属）。
	node, ok = ClashProxy(pqRealityEndpoint(ProtocolTrojan, true))
	if !ok {
		t.Fatal("trojan reality endpoint must render")
	}
	if got := marshalYAML(node); strings.Contains(got, "support-x25519mlkem768") {
		t.Fatalf("trojan endpoint must NOT carry the PQ marker:\n%s", got)
	}
}

func TestSingboxRenderNeverCarriesPQMarker(t *testing.T) {
	outs, ok := SingboxOutbounds(pqRealityEndpoint(ProtocolVLESS, true))
	if !ok || len(outs) == 0 {
		t.Fatal("vless reality endpoint must render for sing-box")
	}
	raw, err := json.Marshal(outs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "support-x25519mlkem768") {
		t.Fatalf("sing-box rendering must NOT carry the Mihomo PQ marker: %s", raw)
	}
}
