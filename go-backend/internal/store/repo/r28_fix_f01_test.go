package repo

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R28-L3-E01 / R28-FIX-F01（产品 P2）面 2：resolveForwardIngress /
// ForwardEntries 在 ServerIP 含端口时须产出干净 host:port 对。
// formatForwardIngressAddress 是三处调用面的唯一汇合点
// （forward 列表/详情 inIp 面 + ForwardEntries），同口径修复。

func TestR28FixF01FormatForwardIngressAddressStripsPort(t *testing.T) {
	cases := []struct {
		name string
		host string
		port int64
		want string
	}{
		{"ipv4 with :port", "203.0.113.10:6366", 45001, "203.0.113.10:45001"},
		{"bracketed v6 with :port", "[2001:db8::1]:6366", 45001, "[2001:db8::1]:45001"},
		{"bare ipv4", "203.0.113.10", 45001, "203.0.113.10:45001"},
		{"bare v6", "2001:db8::1", 45001, "[2001:db8::1]:45001"},
		{"hostname with :port", "node.example.com:6366", 45001, "node.example.com:45001"},
		{"bracketed v6 no port", "[2001:db8::1]", 45001, "[2001:db8::1]:45001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatForwardIngressAddress(tc.host, tc.port); got != tc.want {
				t.Fatalf("formatForwardIngressAddress(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func seedR28F01ForwardEntry(t *testing.T, r *Repository, serverIP, inIP string, port int) (int64, int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	node := model.Node{
		Name: "r28-entry", Secret: "r28-secret", ServerIP: serverIP,
		Port: "30000-30010", TCPListenAddr: "[::]", UDPListenAddr: "[::]",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true},
	}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	tunnel := model.Tunnel{Name: "r28-tunnel", Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "r28-fwd", TunnelID: tunnel.ID, RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	fp := model.ForwardPort{ForwardID: fwd.ID, NodeID: node.ID, Port: port}
	if inIP != "" {
		fp.InIP = sql.NullString{String: inIP, Valid: true}
	}
	if err := r.DB().Create(&fp).Error; err != nil {
		t.Fatal(err)
	}
	return fwd.ID, tunnel.ID
}

// TestR28FixF01ForwardEntriesStripsServerIPPort：入口节点 ServerIP
// 含端口且 forward_port 无 InIP 时，ForwardEntries 的 host 须为裸
// host（不含 :port 后缀）；InIP 含端口时同口径剥离。
func TestR28FixF01ForwardEntriesStripsServerIPPort(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	fwdID, tunnelID := seedR28F01ForwardEntry(t, r, "203.0.113.10:6366", "", 45001)
	entries, err := r.ForwardEntries(fwdID, tunnelID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Host != "203.0.113.10" || entries[0].Port != 45001 {
		t.Fatalf("ForwardEntries with :port ServerIP = %+v, want [{203.0.113.10 45001}]", entries)
	}

	fwdID2, tunnelID2 := seedR28F01ForwardEntry(t, r, "203.0.113.10:6366", "198.51.100.9:6366", 45002)
	entries2, err := r.ForwardEntries(fwdID2, tunnelID2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries2) != 1 || entries2[0].Host != "198.51.100.9" || entries2[0].Port != 45002 {
		t.Fatalf("ForwardEntries with :port InIP = %+v, want [{198.51.100.9 45002}]", entries2)
	}
}

// TestR28FixF01ListTunnelsInIPStripsPort：sweep —— ListTunnels 在
// tunnel.in_ip 为空时以入口节点 ServerIP 回退填充 inIp 展示值；
// 含端口的原值会让前端 formatInAddress 把面板端口当成入口端口。
func TestR28FixF01ListTunnelsInIPStripsPort(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	node := model.Node{
		Name: "r28-tun-entry", Secret: "r28-secret", ServerIP: "203.0.113.10:6366",
		Port: "30000-30010", TCPListenAddr: "[::]", UDPListenAddr: "[::]",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true},
	}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	tunnel := model.Tunnel{Name: "r28-tun", Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Create(&model.ChainTunnel{TunnelID: tunnel.ID, ChainType: "1", NodeID: node.ID, Strategy: sql.NullString{String: "round", Valid: true}, Inx: sql.NullInt64{Int64: 1, Valid: true}, Protocol: sql.NullString{String: "tcp", Valid: true}}).Error; err != nil {
		t.Fatal(err)
	}
	// ListTunnels 的 inIp 回退只在 chainBucket 非空（存在 type-2
	// 链节点）时执行，补一行 type-2 使回退路径可达。
	if err := r.DB().Create(&model.ChainTunnel{TunnelID: tunnel.ID, ChainType: "2", NodeID: node.ID, Strategy: sql.NullString{String: "round", Valid: true}, Inx: sql.NullInt64{Int64: 1, Valid: true}, Protocol: sql.NullString{String: "tcp", Valid: true}}).Error; err != nil {
		t.Fatal(err)
	}
	items, err := r.ListTunnels()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if id, _ := item["id"].(int64); id == tunnel.ID {
			if inIP, _ := item["inIp"].(string); inIP != "203.0.113.10" {
				t.Fatalf("ListTunnels inIp fallback = %q, want %q", inIP, "203.0.113.10")
			}
			return
		}
	}
	t.Fatalf("tunnel %d not found in ListTunnels output %+v", tunnel.ID, items)
}
