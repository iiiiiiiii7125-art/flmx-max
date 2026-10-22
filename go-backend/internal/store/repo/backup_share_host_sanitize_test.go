package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestSanitizeBackupShareHost(t *testing.T) {
	for host, wantOK := range map[string]bool{
		"":                  true,
		"ok.example.com":    true,
		"192.0.2.55":        true,
		"[2001:db8::9]":     true,
		"good.com@evil.com": false,
		"bad host/x":        false,
		"a,b.com":           false,
		"127.0.0.1":         false,
	} {
		got, ok := sanitizeBackupShareHost(host)
		if ok != wantOK {
			t.Fatalf("sanitize(%q) ok=%v, want %v", host, ok, wantOK)
		}
		if wantOK && got != host {
			t.Fatalf("sanitize(%q) = %q, valid values must pass verbatim", host, got)
		}
		if !wantOK && got != "" {
			t.Fatalf("sanitize(%q) = %q, invalid values must be cleared", host, got)
		}
	}
}

// F-A-01/C-01（R1 修复批）：备份恢复与 ShareHost 写入面同标准——
// 旧备份里的坏分享地址（校验上线前落库的值）恢复时清空并计入
// ImportWarnings，不原样带回库里；合法值逐字恢复。
func TestImportClearsInvalidShareHost(t *testing.T) {
	r, err := Open(t.TempDir() + "/share-host.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	node := model.Node{Name: "n", Secret: "n", ServerIP: "192.0.2.1"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	in := model.NodeInbound{NodeID: node.ID, Name: "in", Protocol: "vless", Listen: "::", Port: 443,
		ShareHost: "ok.example.com", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "f", RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	rel := model.NodeInboundRelay{InboundID: in.ID, ForwardID: fwd.ID, Name: "rel", ShareHost: "relay.example.com", Managed: 1, CreatedTime: now}
	if err := r.DB().Create(&rel).Error; err != nil {
		t.Fatal(err)
	}
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	for i := range backup.Inbounds.Inbounds {
		backup.Inbounds.Inbounds[i].ShareHost = "good.com@evil.com"
	}
	for i := range backup.Inbounds.Relays {
		backup.Inbounds.Relays[i].ShareHost = "bad host/x"
	}
	res, err := r.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetNodeInbound(in.ID)
	if err != nil || got == nil {
		t.Fatalf("inbound after import: %v", err)
	}
	if got.ShareHost != "" {
		t.Fatalf("invalid inbound ShareHost must be cleared on import, got %q", got.ShareHost)
	}
	relays, err := r.ListInboundRelays(0)
	if err != nil || len(relays) != 1 {
		t.Fatalf("relays after import: %v %v", relays, err)
	}
	if relays[0].ShareHost != "" {
		t.Fatalf("invalid relay ShareHost must be cleared on import, got %q", relays[0].ShareHost)
	}
	warned := false
	for _, w := range res.ImportWarnings {
		if strings.Contains(w, "分享地址") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("import must disclose the cleared share hosts, warnings=%v", res.ImportWarnings)
	}

	// 合法值逐字恢复，不被误清。
	for i := range backup.Inbounds.Inbounds {
		backup.Inbounds.Inbounds[i].ShareHost = "ok.example.com"
	}
	for i := range backup.Inbounds.Relays {
		backup.Inbounds.Relays[i].ShareHost = "relay.example.com"
	}
	if _, err := r.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatal(err)
	}
	got, err = r.GetNodeInbound(in.ID)
	if err != nil || got == nil || got.ShareHost != "ok.example.com" {
		t.Fatalf("valid inbound ShareHost must round-trip: %+v %v", got, err)
	}
	relays, err = r.ListInboundRelays(0)
	if err != nil || len(relays) != 1 || relays[0].ShareHost != "relay.example.com" {
		t.Fatalf("valid relay ShareHost must round-trip: %+v %v", relays, err)
	}
}
