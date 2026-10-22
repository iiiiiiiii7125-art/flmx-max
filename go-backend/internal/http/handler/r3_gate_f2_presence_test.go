package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// GATE-F2（R3X-012 整对象 presence 限定）：入站更新载荷**整对象省略**
// settings / transport / security / extra 时应保留旧值（与字段级回填同
// 口径，R2X-030「更新留空无合法意图清空语义」）。批 7 只覆盖了对象在场
// 时的子字段清空；整对象缺席时旧实现把 security.Type="" 交给 Normalize
// 取默认首选安全层，ShadowTLS 被静默剥离为 none、凭据丢失（发版门项 8
// FAIL）。显式路径不受影响：security 对象在场且 type 显式传 "none" 仍是
// 合法关闭路径，显式改口令仍生效；创建路径不查 presence、行为不变。

type gf2Form struct {
	name       string
	kernelType string
	seed       kernel.Inbound
}

func gf2Forms() []gf2Form {
	return []gf2Form{
		{
			name:       "shadowtls-ss",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 24443,
				Security: kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "www.example.com", ShadowTLSPassword: "a4shadowpw"},
				Settings: kernel.Settings{Method: "aes-256-gcm", Password: "ss-pass-12345"}},
		},
		{
			name:       "shadowtls-snell",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolSnell, Port: 24444,
				Security: kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "st.example.com", ShadowTLSPassword: "snellstls-pw-1"},
				Settings: kernel.Settings{Password: "psk-bbbbbbbb", SnellVersion: 5}},
		},
		{
			name:       "reality-vless",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24445,
				Security: kernel.Security{Type: kernel.SecurityReality, SNI: "r.example.com", RealityDest: "r.example.com:8443", RealityShortIDs: []string{"abcd"}},
				Settings: kernel.Settings{Flow: kernel.FlowVision}},
		},
		{
			name:       "tls-trojan",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 24446,
				Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "t.example.com", CertMode: kernel.CertModeSelf},
				Settings: kernel.Settings{Password: "trojan-pass-123"}},
		},
		{
			// Naïve 真机创建受证书校验拦截（合法校验），单测以 file 证书
			// 种子验 presence（同批 7 naive 行口径）。
			name:       "tls-naive",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolNaive, Port: 24447,
				Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "n.example.com", CertMode: kernel.CertModeFile, CertFile: "/tmp/c.pem", KeyFile: "/tmp/k.pem"},
				Settings: kernel.Settings{Username: "myuser123", Password: "naive-pass-123"}},
		},
	}
}

func gf2Setup(t *testing.T, form gf2Form) (*Handler, *repo.Repository, *model.NodeInbound) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/gf2.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "gf2-node", "198.51.100.60")
	k := seedB8Kernel(t, r, nodeID, form.kernelType, "latest")
	entry := seedChainInbound(t, r, k, "gf2-"+form.name, form.seed, 0)
	return h, r, entry
}

// gf2Body builds a full update payload from the stored inbound, then lets
// mutate delete/replace top-level keys at the raw JSON level — this is the
// only way to express 整对象省略 (struct marshaling always emits the keys).
func gf2Body(t *testing.T, row *model.NodeInbound, mutate func(map[string]json.RawMessage)) string {
	t.Helper()
	old, err := inboundFromModel(*row)
	if err != nil {
		t.Fatal(err)
	}
	req := inboundRequest{
		ID: row.ID, Name: row.Name, Protocol: old.Protocol, Listen: old.Listen, Port: old.Port,
		Settings: old.Settings, Transport: old.Transport, Security: old.Security,
		Extra: string(old.Extra), OutboundID: row.OutboundID,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(m)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func gf2Row(t *testing.T, r *repo.Repository, id int64) *model.NodeInbound {
	t.Helper()
	row, err := r.GetNodeInbound(id)
	if err != nil || row == nil {
		t.Fatalf("reload inbound %d: %v", id, err)
	}
	return row
}

// ① 整对象省略 security：安全层逐字不变（全部带 security 的形态）。
func TestR3GateF2OmitSecurityObjectPreserves(t *testing.T) {
	for _, form := range gf2Forms() {
		t.Run(form.name, func(t *testing.T) {
			h, r, entry := gf2Setup(t, form)
			before := gf2Row(t, r, entry.ID)
			body := gf2Body(t, before, func(m map[string]json.RawMessage) {
				delete(m, "security")
				m["name"] = json.RawMessage(`"gf2-` + form.name + `-renamed"`)
			})
			out := callInboundSave(t, h, true, body)
			if out.Code != 0 {
				t.Fatalf("update omitting security object must succeed: %+v", out)
			}
			after := gf2Row(t, r, entry.ID)
			if after.Security != before.Security {
				t.Fatalf("omitted security must be preserved verbatim\nbefore %s\nafter  %s", before.Security, after.Security)
			}
			if after.Settings != before.Settings {
				t.Fatalf("settings must be untouched\nbefore %s\nafter  %s", before.Settings, after.Settings)
			}
			if after.Transport != before.Transport {
				t.Fatalf("transport must be untouched\nbefore %s\nafter  %s", before.Transport, after.Transport)
			}
		})
	}
}

// ①b 整对象省略 settings：非凭据字段（BackendPort/Masquerade/PortHopping/
// Flow）不在字段级回填集内，整对象省略必须整对象保留。
func TestR3GateF2OmitSettingsObjectPreserves(t *testing.T) {
	forms := []gf2Form{
		{
			name:       "snell-shadowtls-backendport",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolSnell, Port: 24453,
				Security: kernel.Security{Type: kernel.SecurityShadowTLS, SNI: "st.example.com", ShadowTLSPassword: "snellstls-pw-1"},
				Settings: kernel.Settings{Password: "psk-bbbbbbbb", SnellVersion: 5}},
		},
		{
			name:       "hy2-masquerade-hopping",
			kernelType: kernel.TypeSingbox,
			seed: kernel.Inbound{Protocol: kernel.ProtocolHysteria2, Port: 24454,
				Transport: kernel.Transport{Type: kernel.TransportQUIC},
				Security:  kernel.Security{Type: kernel.SecurityTLS, SNI: "h.example.com", CertMode: kernel.CertModeSelf},
				Settings:  kernel.Settings{Password: "hy2-pass-1234", Masquerade: "https://example.com/404", PortHopping: "20000-20100"}},
		},
		{
			name:       "vless-vision-flow",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24455,
				Security: kernel.Security{Type: kernel.SecurityReality},
				Settings: kernel.Settings{Flow: kernel.FlowVision}},
		},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			h, r, entry := gf2Setup(t, form)
			before := gf2Row(t, r, entry.ID)
			body := gf2Body(t, before, func(m map[string]json.RawMessage) { delete(m, "settings") })
			out := callInboundSave(t, h, true, body)
			if out.Code != 0 {
				t.Fatalf("update omitting settings object must succeed: %+v", out)
			}
			after := gf2Row(t, r, entry.ID)
			if after.Settings != before.Settings {
				t.Fatalf("omitted settings must be preserved verbatim\nbefore %s\nafter  %s", before.Settings, after.Settings)
			}
			if after.Security != before.Security {
				t.Fatalf("security must be untouched\nbefore %s\nafter  %s", before.Security, after.Security)
			}
		})
	}
}

// ①c 整对象省略 transport：type 不得被 Normalize 默认回 tcp。
func TestR3GateF2OmitTransportObjectPreserves(t *testing.T) {
	forms := []gf2Form{
		{
			name:       "ws",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24463,
				Transport: kernel.Transport{Type: kernel.TransportWS, Path: "/custom-path", Host: "ws.example.com"},
				Security:  kernel.Security{Type: kernel.SecurityNone}},
		},
		{
			name:       "grpc",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24464,
				Transport: kernel.Transport{Type: kernel.TransportGRPC, ServiceName: "myservice"},
				Security:  kernel.Security{Type: kernel.SecurityNone}},
		},
		{
			name:       "xhttp",
			kernelType: kernel.TypeXray,
			seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24465,
				Transport: kernel.Transport{Type: kernel.TransportXHTTP, Path: "/xhttp-path", Mode: "packet-up"},
				Security:  kernel.Security{Type: kernel.SecurityNone}},
		},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			h, r, entry := gf2Setup(t, form)
			before := gf2Row(t, r, entry.ID)
			body := gf2Body(t, before, func(m map[string]json.RawMessage) { delete(m, "transport") })
			out := callInboundSave(t, h, true, body)
			if out.Code != 0 {
				t.Fatalf("update omitting transport object must succeed: %+v", out)
			}
			after := gf2Row(t, r, entry.ID)
			if after.Transport != before.Transport {
				t.Fatalf("omitted transport must be preserved verbatim\nbefore %s\nafter  %s", before.Transport, after.Transport)
			}
		})
	}
}

// ①d 整对象省略 extra：保留旧值；显式传空串仍是合法清空路径。
func TestR3GateF2OmitExtraPreservesExplicitEmptyClears(t *testing.T) {
	h, r, entry := gf2Setup(t, gf2Form{
		name:       "extra",
		kernelType: kernel.TypeXray,
		seed: kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24473,
			Security: kernel.Security{Type: kernel.SecurityNone},
			Extra:    json.RawMessage(`{"customFlag":true}`)},
	})
	before := gf2Row(t, r, entry.ID)
	if !strings.Contains(before.Extra, "customFlag") {
		t.Fatalf("seed extra missing: %q", before.Extra)
	}
	body := gf2Body(t, before, func(m map[string]json.RawMessage) { delete(m, "extra") })
	if out := callInboundSave(t, h, true, body); out.Code != 0 {
		t.Fatalf("update omitting extra must succeed: %+v", out)
	}
	if after := gf2Row(t, r, entry.ID); after.Extra != before.Extra {
		t.Fatalf("omitted extra must be preserved, before %q after %q", before.Extra, after.Extra)
	}
	// 显式清空：extra 在场且为空串 → 清空生效（合法意图清空语义保留）。
	body = gf2Body(t, before, func(m map[string]json.RawMessage) { m["extra"] = json.RawMessage(`""`) })
	if out := callInboundSave(t, h, true, body); out.Code != 0 {
		t.Fatalf("update with explicit empty extra must succeed: %+v", out)
	}
	if after := gf2Row(t, r, entry.ID); strings.TrimSpace(after.Extra) != "" {
		t.Fatalf("explicit empty extra must clear, got %q", after.Extra)
	}
}

// ② 显式修改 security 正常生效 + 显式 type="none" 是合法关闭路径（不可误伤）。
func TestR3GateF2ExplicitSecurityChangesStillApply(t *testing.T) {
	form := gf2Forms()[0] // shadowtls-ss

	t.Run("change-password", func(t *testing.T) {
		h, r, entry := gf2Setup(t, form)
		before := gf2Row(t, r, entry.ID)
		body := gf2Body(t, before, func(m map[string]json.RawMessage) {
			var sec map[string]interface{}
			if err := json.Unmarshal(m["security"], &sec); err != nil {
				t.Fatal(err)
			}
			sec["shadowTlsPassword"] = "newpass-9876"
			raw, err := json.Marshal(sec)
			if err != nil {
				t.Fatal(err)
			}
			m["security"] = raw
		})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("explicit security change must succeed: %+v", out)
		}
		after := b6ReloadInbound(t, r, entry.ID)
		if after.Security.Type != kernel.SecurityShadowTLS || after.Security.ShadowTLSPassword != "newpass-9876" {
			t.Fatalf("explicit new shadowTlsPassword must apply, got %+v", after.Security)
		}
	})

	t.Run("explicit-type-none-closes", func(t *testing.T) {
		h, r, entry := gf2Setup(t, form)
		before := gf2Row(t, r, entry.ID)
		body := gf2Body(t, before, func(m map[string]json.RawMessage) {
			m["security"] = json.RawMessage(`{"type":"none"}`)
		})
		if out := callInboundSave(t, h, true, body); out.Code != 0 {
			t.Fatalf("explicit type=none must succeed (legit close path): %+v", out)
		}
		after := b6ReloadInbound(t, r, entry.ID)
		if after.Security.Type != kernel.SecurityNone || after.Security.ShadowTLSPassword != "" {
			t.Fatalf("explicit type=none must close the security layer, got %+v", after.Security)
		}
	})
}

// 枚举探针（标量维度现状，非静默面）：protocol/name 省略为显式拒绝、
// port/listen 省略为既有回填保留——修复前后均应成立。
func TestR3GateF2ScalarPresenceProbe(t *testing.T) {
	form := gf2Forms()[0]
	h, r, entry := gf2Setup(t, form)
	before := gf2Row(t, r, entry.ID)

	body := gf2Body(t, before, func(m map[string]json.RawMessage) { delete(m, "protocol") })
	if out := callInboundSave(t, h, true, body); out.Code == 0 {
		t.Fatalf("update omitting protocol must be rejected, got %+v", out)
	}
	body = gf2Body(t, before, func(m map[string]json.RawMessage) { delete(m, "name") })
	if out := callInboundSave(t, h, true, body); out.Code == 0 {
		t.Fatalf("update omitting name must be rejected, got %+v", out)
	}
	body = gf2Body(t, before, func(m map[string]json.RawMessage) {
		delete(m, "port")
		delete(m, "listen")
	})
	if out := callInboundSave(t, h, true, body); out.Code != 0 {
		t.Fatalf("update omitting port/listen must succeed: %+v", out)
	}
	after := gf2Row(t, r, entry.ID)
	if after.Port != before.Port || after.Listen != before.Listen {
		t.Fatalf("port/listen must be preserved, before %d/%q after %d/%q", before.Port, before.Listen, after.Port, after.Listen)
	}
}

// 防过修：创建路径整对象省略 security 仍走默认（不查 presence、无旧值可回填）。
func TestR3GateF2CreateOmittingSecurityStillDefaults(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/gf2-create.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "gf2-node", "198.51.100.61")
	seedB8Kernel(t, r, nodeID, kernel.TypeXray, "latest")

	// 对照口径：创建省略 security 与创建显式传空对象 {} 必须同结果
	// （目录默认安全层），且凭据仍自动生成——presence 只作用于更新回填。
	port := 24483
	create := func(name, securityKey string) kernel.Inbound {
		t.Helper()
		port++
		body := `{"nodeId":` + jsonID(nodeID) + `,"kernelType":"xray","name":"` + name + `","protocol":"vless","port":` + jsonID(int64(port)) + `,"settings":{},"transport":{"type":"tcp"}`
		if securityKey != "" {
			body += `,"security":` + securityKey
		}
		body += `}`
		if out := callInboundSave(t, h, false, body); out.Code != 0 {
			t.Fatalf("create %s must succeed: %+v", name, out)
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
	omitted := create("gf2-create-omit", "")
	explicit := create("gf2-create-empty", `{}`)
	if omitted.Security.Type == "" || omitted.Security.Type != explicit.Security.Type {
		t.Fatalf("create omitting security must match explicit empty object: omitted %q explicit %q", omitted.Security.Type, explicit.Security.Type)
	}
	if omitted.Settings.UUID == "" {
		t.Fatal("create omitting security must still auto-generate credentials")
	}
}
