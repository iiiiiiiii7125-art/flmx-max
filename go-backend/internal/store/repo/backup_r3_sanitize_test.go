package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3-A-01（备份面）：可规范化形态（带括号 IPv6 + 尾点、带首尾空格）
// 改走规范化恢复，与保存/拉取面一致；真坏值仍清空 + 警告。
func TestR3SanitizeBackupShareHostCanonicalForms(t *testing.T) {
	for host, want := range map[string]string{
		"[2001:db8::9].":      "2001:db8::9",
		"[2001:db8::9.]":      "2001:db8::9",
		" entry.example.com ": "entry.example.com",
	} {
		got, ok := sanitizeBackupShareHost(host)
		if !ok || got != want {
			t.Fatalf("sanitize(%q) = %q, %v; want normalized %q restored", host, got, ok, want)
		}
	}
	// 逐字恢复口径不破：已规范的值原样带回。
	if got, ok := sanitizeBackupShareHost("[2001:db8::9]"); !ok || got != "[2001:db8::9]" {
		t.Fatalf("verbatim valid value must round-trip, got %q ok=%v", got, ok)
	}
	// 双尾点仍判坏值清空（与保存面 B2-01 钉版一致）。
	if got, ok := sanitizeBackupShareHost("example.com.."); ok || got != "" {
		t.Fatalf("double trailing dot must stay cleared, got %q ok=%v", got, ok)
	}
}

// R3-E-02（导入面）：ShareAddressMode 走白名单——非法值归 auto 并计
// ImportWarnings，合法值逐字恢复。
func TestR3ImportNormalizesShareAddressMode(t *testing.T) {
	r, err := Open(t.TempDir() + "/share-mode.db")
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
		ShareAddressMode: "ipv6", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(backup.Inbounds.Inbounds) != 1 {
		t.Fatalf("exported inbounds: %d", len(backup.Inbounds.Inbounds))
	}
	backup.Inbounds.Inbounds[0].ShareAddressMode = "evil\nmode"
	res, err := r.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetNodeInbound(in.ID)
	if err != nil || got == nil {
		t.Fatalf("inbound after import: %v", err)
	}
	if got.ShareAddressMode != "auto" {
		t.Fatalf("illegal mode must normalize to auto on import, got %q", got.ShareAddressMode)
	}
	warned := false
	for _, w := range res.ImportWarnings {
		if strings.Contains(w, "分享地址模式") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("import must disclose the normalized mode, warnings=%v", res.ImportWarnings)
	}

	// 合法 mode 逐字恢复、无警告。
	backup.Inbounds.Inbounds[0].ShareAddressMode = "ipv6"
	res, err = r.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.GetNodeInbound(in.ID)
	if err != nil || got == nil || got.ShareAddressMode != "ipv6" {
		t.Fatalf("valid mode must round-trip: %+v %v", got, err)
	}
	for _, w := range res.ImportWarnings {
		if strings.Contains(w, "分享地址模式") {
			t.Fatalf("valid mode must not warn, warnings=%v", res.ImportWarnings)
		}
	}
}

// R3-E-01（备份恢复面）：节点 ServerIP 含结构字符时清空 + 警告（与
// ShareHost 的 sanitize 同批口径），合法值逐字恢复；判定只作用于
// 恢复落库值，不影响节点其余字段。
func TestR3ImportSanitizesNodeServerIP(t *testing.T) {
	r, err := Open(t.TempDir() + "/node-serverip.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	node := model.Node{Name: "n", Secret: "n", ServerIP: "192.0.2.1"}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(backup.Nodes) != 1 {
		t.Fatalf("exported nodes: %d", len(backup.Nodes))
	}
	backup.Nodes[0].ServerIP = "good.com@evil.com"
	res, err := r.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetNodeByID(node.ID)
	if err != nil || got == nil {
		t.Fatalf("node after import: %v", err)
	}
	if got.ServerIP != "" {
		t.Fatalf("structural ServerIP must be cleared on import, got %q", got.ServerIP)
	}
	warned := false
	for _, w := range res.ImportWarnings {
		if strings.Contains(w, "节点地址") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("import must disclose the cleared node address, warnings=%v", res.ImportWarnings)
	}

	// 合法值（主机名、回环、RFC1918）逐字恢复——备份面不比保存面更严。
	for _, ip := range []string{"node.example.com", "127.0.0.1", "10.0.0.8"} {
		backup.Nodes[0].ServerIP = ip
		if _, err := r.Import(backup, []string{"nodes"}); err != nil {
			t.Fatal(err)
		}
		got, err = r.GetNodeByID(node.ID)
		if err != nil || got == nil || got.ServerIP != ip {
			t.Fatalf("valid ServerIP %q must round-trip: %+v %v", ip, got, err)
		}
	}
}
