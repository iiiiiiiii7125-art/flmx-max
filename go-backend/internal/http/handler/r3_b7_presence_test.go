package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R3X-012（批 7）：presence 回填全集表驱动。
//
// 行集 = Normalize（internal/kernel/model.go）中全部「空→生成/默认」且属
// 凭据/协议关键默认族的字段（Settings / Security / Transport 三组），
// 逐行清零断言更新后不变；含 R2X-030 已修六项（EncryptionSecret /
// RealityPrivateKey / RealityShortIDs / Listen / UUID / Password）防回退，
// 以及更早在位的 CertPEM/KeyPEM、ACME DNS 凭据与 Port。
//
// 不在本族（报告中登记，不作行）：EncryptionAuth/Mode（留空即关闭 ENC 的
// 模式切换语义）、BackendPort（R2 裁定排除）、RealityPublicKey（由私钥
// 派生，随 RealityPrivateKey 行一并断言）。

type b7PresenceRow struct {
	name       string
	kernelType string
	seed       kernel.Inbound
	clear      func(req *inboundRequest)
	get        func(in kernel.Inbound) string
	expect     string // 非空时断言种子值恰为此值（证明种子是非默认形态）
}

func b7Rows() []b7PresenceRow {
	return []b7PresenceRow{
		{
			name:       "settings.uuid",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityNone},
				Settings: kernel.Settings{UUID: "11111111-2222-3333-4444-555555555555"}},
			clear:  func(req *inboundRequest) { req.Settings.UUID = "" },
			get:    func(in kernel.Inbound) string { return in.Settings.UUID },
			expect: "11111111-2222-3333-4444-555555555555",
		},
		{
			name:       "settings.password",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "p.example.com"},
				Settings: kernel.Settings{Password: "trojan-pass-123"}},
			clear:  func(req *inboundRequest) { req.Settings.Password = "" },
			get:    func(in kernel.Inbound) string { return in.Settings.Password },
			expect: "trojan-pass-123",
		},
		{
			name:       "settings.encryptionSecret",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityNone},
				Settings: kernel.Settings{EncryptionAuth: kernel.EncryptionAuthX25519, EncryptionMode: kernel.EncryptionModeXorpub}},
			clear: func(req *inboundRequest) { req.Settings.EncryptionSecret = "" },
			get:   func(in kernel.Inbound) string { return in.Settings.EncryptionSecret },
		},
		{
			name:       "settings.method",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 24443,
				Settings: kernel.Settings{Method: "aes-256-gcm", Password: "ss-legacy-pass"}},
			clear:  func(req *inboundRequest) { req.Settings.Method = "" },
			get:    func(in kernel.Inbound) string { return in.Settings.Method },
			expect: "aes-256-gcm",
		},
		{
			name:       "settings.username",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolNaive, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "n.example.com", CertMode: kernel.CertModeFile, CertFile: "/tmp/c.pem", KeyFile: "/tmp/k.pem"},
				Settings: kernel.Settings{Username: "myuser123", Password: "naive-pass-123"}},
			clear:  func(req *inboundRequest) { req.Settings.Username = "" },
			get:    func(in kernel.Inbound) string { return in.Settings.Username },
			expect: "myuser123",
		},
		{
			name:       "settings.snellVersion",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolSnell, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityNone},
				Settings: kernel.Settings{Password: "psk-aaaaaaaa", SnellVersion: 4}},
			clear:  func(req *inboundRequest) { req.Settings.SnellVersion = 0 },
			get:    func(in kernel.Inbound) string { return fmt.Sprint(in.Settings.SnellVersion) },
			expect: "4",
		},
		{
			name:       "security.realityPrivateKey",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityReality}},
			clear: func(req *inboundRequest) {
				req.Security.RealityPrivateKey = ""
				req.Security.RealityPublicKey = ""
			},
			get: func(in kernel.Inbound) string {
				return in.Security.RealityPrivateKey + "/" + in.Security.RealityPublicKey
			},
		},
		{
			name:       "security.realityShortIDs",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityReality}},
			clear: func(req *inboundRequest) { req.Security.RealityShortIDs = nil },
			get:   func(in kernel.Inbound) string { return strings.Join(in.Security.RealityShortIDs, ",") },
		},
		{
			name:       "security.shadowTlsPassword",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "www.example.com", ShadowTLSPassword: "stls-pass-1234"},
				Settings: kernel.Settings{Method: "aes-256-gcm", Password: "ss-pass-12345"}},
			clear:  func(req *inboundRequest) { req.Security.ShadowTLSPassword = "" },
			get:    func(in kernel.Inbound) string { return in.Security.ShadowTLSPassword },
			expect: "stls-pass-1234",
		},
		{
			name:       "security.certPEM",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "c.example.com", CertMode: kernel.CertModeSelf},
				Settings: kernel.Settings{Password: "trojan-pass-123"}},
			clear: func(req *inboundRequest) {
				req.Security.CertPEM = ""
				req.Security.KeyPEM = ""
			},
			get: func(in kernel.Inbound) string { return in.Security.CertPEM },
		},
		{
			name:       "security.acmeDNSKey",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolAnyTLS, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "acme.example.com", CertMode: kernel.CertModeACME, ACMEDNS: kernel.ACMEDNSCloudflare, ACMEDNSKey: "cf-token-123"},
				Settings: kernel.Settings{Password: "anytls-pass-123"}},
			clear: func(req *inboundRequest) {
				req.Security.ACMEDNSKey = ""
				req.Security.ACMEDNSSecret = ""
			},
			get:    func(in kernel.Inbound) string { return in.Security.ACMEDNSKey },
			expect: "cf-token-123",
		},
		{
			name:       "inbound.listen",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Listen: "0.0.0.0", Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityNone}},
			clear:  func(req *inboundRequest) { req.Listen = "" },
			get:    func(in kernel.Inbound) string { return in.Listen },
			expect: "0.0.0.0",
		},
		{
			name:       "inbound.port",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityNone}},
			clear:  func(req *inboundRequest) { req.Port = 0 },
			get:    func(in kernel.Inbound) string { return fmt.Sprint(in.Port) },
			expect: "24443",
		},
		{
			name:       "transport.path.ws",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Transport: kernel.Transport{Type: kernel.TransportWS, Path: "/custom-path"},
				Security:  kernel.Security{Type: kernel.SecurityNone}},
			clear:  func(req *inboundRequest) { req.Transport.Path = "" },
			get:    func(in kernel.Inbound) string { return in.Transport.Path },
			expect: "/custom-path",
		},
		{
			name:       "transport.path.xhttp",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Transport: kernel.Transport{Type: kernel.TransportXHTTP, Path: "/xhttp-path", Mode: "packet-up"},
				Security:  kernel.Security{Type: kernel.SecurityNone}},
			clear:  func(req *inboundRequest) { req.Transport.Path = "" },
			get:    func(in kernel.Inbound) string { return in.Transport.Path },
			expect: "/xhttp-path",
		},
		{
			name:       "transport.serviceName.grpc",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
				Transport: kernel.Transport{Type: kernel.TransportGRPC, ServiceName: "myservice"},
				Security:  kernel.Security{Type: kernel.SecurityNone}},
			clear:  func(req *inboundRequest) { req.Transport.ServiceName = "" },
			get:    func(in kernel.Inbound) string { return in.Transport.ServiceName },
			expect: "myservice",
		},
	}
}

func TestR3B7PresenceFullSet(t *testing.T) {
	for _, row := range b7Rows() {
		t.Run(row.name, func(t *testing.T) {
			r, err := repo.Open(t.TempDir() + "/b7-pres.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			h := New(r, "unit-test-secret")
			nodeID := seedRelayTargetNode(t, r, "b7-node", "198.51.100.50")
			k := seedB8Kernel(t, r, nodeID, row.kernelType, "latest")
			entry := seedChainInbound(t, r, k, "b7-"+strings.ReplaceAll(row.name, ".", "-"), row.seed, 0)

			before := b6ReloadInbound(t, r, entry.ID)
			want := row.get(before)
			if want == "" {
				t.Fatalf("seed must carry a value for %s", row.name)
			}
			if row.expect != "" && want != row.expect {
				t.Fatalf("seed %s = %q, want custom %q", row.name, want, row.expect)
			}
			out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) {
				req.Name = req.Name + "-renamed"
				row.clear(req)
			})
			if out.Code != 0 {
				t.Fatalf("update clearing %s must succeed: %+v", row.name, out)
			}
			if after := b6ReloadInbound(t, r, entry.ID); row.get(after) != want {
				t.Fatalf("cleared %s must be preserved, before %q after %q", row.name, want, row.get(after))
			}
		})
	}
}

// 防过修：创建留空仍自动生成/取默认（本批新增回填字段全数对照）。
func TestR3B7CreateStillGenerates(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b7-create.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "b7-node", "198.51.100.51")
	seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "latest")
	seedB8Kernel(t, r, nodeID, kernel.TypeXray, "latest")

	create := func(name string, req inboundRequest) kernel.Inbound {
		t.Helper()
		req.Name = name
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if out := callInboundSave(t, h, false, string(raw)); out.Code != 0 {
			t.Fatalf("create %s: %+v", name, out)
		}
		rows, err := r.ListNodeInbounds()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name == name {
				return b6ReloadInbound(t, r, row.ID)
			}
		}
		t.Fatalf("created inbound %s not found", name)
		return kernel.Inbound{}
	}

	naive := create("b7-c-naive", inboundRequest{NodeID: nodeID, KernelType: kernel.TypeSingbox,
		Protocol: kernel.ProtocolNaive, Port: 25443,
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "n.example.com", CertMode: kernel.CertModeFile, CertFile: "/tmp/c.pem", KeyFile: "/tmp/k.pem"},
		Settings: kernel.Settings{Password: "naive-pass-123"}})
	if naive.Settings.Username == "" {
		t.Fatal("create naive with empty username must still auto-generate")
	}

	ss := create("b7-c-ss", inboundRequest{NodeID: nodeID, KernelType: kernel.TypeSingbox,
		Protocol: kernel.ProtocolShadowsocks, Port: 25444})
	if ss.Settings.Method != kernel.DefaultSSMethod {
		t.Fatalf("create SS with empty method must still default, got %q", ss.Settings.Method)
	}

	snell := create("b7-c-snell", inboundRequest{NodeID: nodeID, KernelType: kernel.TypeSingbox,
		Protocol: kernel.ProtocolSnell, Port: 25445,
		Security: kernel.Security{Type: kernel.SecurityNone},
		Settings: kernel.Settings{Password: "psk-aaaaaaaa"}})
	if snell.Settings.SnellVersion != kernel.DefaultSnellVersion {
		t.Fatalf("create snell with version 0 must still default, got %d", snell.Settings.SnellVersion)
	}

	stls := create("b7-c-stls", inboundRequest{NodeID: nodeID, KernelType: kernel.TypeSingbox,
		Protocol: kernel.ProtocolShadowsocks, Port: 25446,
		Security: kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "www.example.com"},
		Settings: kernel.Settings{Method: "aes-256-gcm", Password: "ss-pass-12345"}})
	if stls.Security.ShadowTLSPassword == "" {
		t.Fatal("create shadowtls with empty password must still auto-generate")
	}

	ws := create("b7-c-ws", inboundRequest{NodeID: nodeID, KernelType: kernel.TypeXray,
		Protocol: kernel.ProtocolVLESS, Port: 25447,
		Transport: kernel.Transport{Type: kernel.TransportWS},
		Security:  kernel.Security{Type: kernel.SecurityNone}})
	if ws.Transport.Path == "" || !strings.HasPrefix(ws.Transport.Path, "/") {
		t.Fatalf("create WS with empty path must still auto-generate, got %q", ws.Transport.Path)
	}

	grpc := create("b7-c-grpc", inboundRequest{NodeID: nodeID, KernelType: kernel.TypeXray,
		Protocol: kernel.ProtocolVLESS, Port: 25448,
		Transport: kernel.Transport{Type: kernel.TransportGRPC},
		Security:  kernel.Security{Type: kernel.SecurityNone}})
	if grpc.Transport.ServiceName != "grpc" {
		t.Fatalf("create gRPC with empty serviceName must still default to grpc, got %q", grpc.Transport.ServiceName)
	}
}

// 防过修：ShadowTLSPassword 在 Regenerate 主动轮换集内，回填不得妨碍轮换。
func TestR3B7RegenerateStillRotatesShadowTLSPassword(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b7-regen.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "b7-node", "198.51.100.52")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "latest")
	entry := seedChainInbound(t, r, k, "b7-regen-stls", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 24443,
		Security: kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "www.example.com", ShadowTLSPassword: "stls-pass-1234"},
		Settings: kernel.Settings{Method: "aes-256-gcm", Password: "ss-pass-12345"}}, 0)
	before := b6ReloadInbound(t, r, entry.ID)

	rec := httptest.NewRecorder()
	h.inboundRegenerate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/inbound/regenerate", strings.NewReader(`{"id":`+jsonID(entry.ID)+`}`)))
	var out response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != 0 {
		t.Fatalf("regenerate: %+v", out)
	}
	after := b6ReloadInbound(t, r, entry.ID)
	if after.Security.ShadowTLSPassword == "" || after.Security.ShadowTLSPassword == before.Security.ShadowTLSPassword {
		t.Fatalf("regenerate must still rotate shadowTlsPassword, before %q after %q", before.Security.ShadowTLSPassword, after.Security.ShadowTLSPassword)
	}
	if after.Settings.Password == before.Settings.Password {
		t.Fatal("regenerate must still rotate the SS password")
	}
	// 主动轮换集不含本批其余字段：method 不随 regenerate 变更。
	if after.Settings.Method != before.Settings.Method {
		t.Fatalf("regenerate must not change method, before %q after %q", before.Settings.Method, after.Settings.Method)
	}
}
