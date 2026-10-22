package kernel

import (
	"testing"
)

// A5-02/BC-11：与 R3 真机同构的 9 节点混合订阅（Reality、Hy2、Snell
// v5、Snell v6 + ss2022/trojan/vmess-ws/tuic/anytls），四格式实得
// 8/9/8/7——计数必须按目标格式的实际渲染数，不再是渲染前的 9。
func mixEndpoints(t *testing.T) []Endpoint {
	t.Helper()
	uuid := "11111111-2222-3333-4444-555555555555"
	mk := func(name string, in Inbound) Endpoint {
		return Endpoint{Name: name, Host: "203.0.113.10", Port: in.Port, Inbound: in}
	}
	return []Endpoint{
		mk("reality", Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: uuid},
			Security: Security{Type: SecurityReality, SNI: "www.microsoft.com", RealityPublicKey: "pk", RealityShortIDs: []string{"ab"}}}),
		mk("hy2", Inbound{ID: 2, Protocol: ProtocolHysteria2, Port: 443, Settings: Settings{Password: "hy2-secret"},
			Security: Security{Type: SecurityTLS, SNI: "h.example.com"}}),
		mk("snell5", Inbound{ID: 3, Protocol: ProtocolSnell, Port: 6160, Settings: Settings{Password: "psk-cccccccc", SnellVersion: 5}}),
		mk("snell6", Inbound{ID: 4, Protocol: ProtocolSnell, Port: 6161, Settings: Settings{Password: "psk-dddddddd", SnellVersion: 6}}),
		mk("ss2022", Inbound{ID: 5, Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: "2022-blake3-aes-256-gcm", Password: "ss-secret"}}),
		mk("trojan", Inbound{ID: 6, Protocol: ProtocolTrojan, Port: 443, Settings: Settings{Password: "trojan-secret"},
			Security: Security{Type: SecurityTLS, SNI: "t.example.com"}}),
		mk("vmess-ws", Inbound{ID: 7, Protocol: ProtocolVMess, Port: 80, Settings: Settings{UUID: uuid},
			Transport: Transport{Type: TransportWS, Path: "/ws"}}),
		mk("tuic", Inbound{ID: 8, Protocol: ProtocolTUIC, Port: 8443, Settings: Settings{UUID: uuid, Password: "tuic-secret"},
			Security: Security{Type: SecurityTLS, SNI: "u.example.com"}}),
		mk("anytls", Inbound{ID: 9, Protocol: ProtocolAnyTLS, Port: 8444, Settings: Settings{Password: "anytls-secret"},
			Security: Security{Type: SecurityTLS, SNI: "a.example.com"}}),
	}
}

func TestSubscriptionNodeCountClash(t *testing.T) {
	if got := SubscriptionNodeCount(FormatClash, mixEndpoints(t), SubOptions{}); got != 8 {
		t.Fatalf("clash count = %d, want 8 (snell v6 dropped)", got)
	}
}

func TestSubscriptionNodeCountSingbox(t *testing.T) {
	if got := SubscriptionNodeCount(FormatSingbox, mixEndpoints(t), SubOptions{SingboxSnell: true}); got != 9 {
		t.Fatalf("sing-box count = %d, want 9 (all nodes for a 1.14+ client)", got)
	}
	if got := SubscriptionNodeCount(FormatSingbox, mixEndpoints(t), SubOptions{}); got != 7 {
		t.Fatalf("sing-box count without snell-capable client = %d, want 7", got)
	}
}

func TestSubscriptionNodeCountSurge(t *testing.T) {
	if got := SubscriptionNodeCount(FormatSurge, mixEndpoints(t), SubOptions{}); got != 8 {
		t.Fatalf("surge count = %d, want 8 (vless dropped)", got)
	}
}

func TestSubscriptionNodeCountBase64(t *testing.T) {
	if got := SubscriptionNodeCount(FormatBase64, mixEndpoints(t), SubOptions{}); got != 7 {
		t.Fatalf("base64 count = %d, want 7 (snell has no share link)", got)
	}
}
