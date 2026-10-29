package repo

import (
	"strings"
	"testing"
	"time"

	"database/sql"

	"go-backend/internal/store/model"
)

// R7-H-01 (P3): the restore-face 1-65535 port gate covered only inbound
// rows and service-monitor targets. Six more port carriers restored
// out-of-range values verbatim with zero skips and zero warnings:
// forward_port.port, chain_tunnel.port, tunnel.probe_target_port,
// node_ssh_config.port, peer_share port ranges and node.port range
// strings. These tests are the permanent parameterized gate: one case
// per carrier per bad-value shape from the r7 lane H fuzz, each bad row
// must be skipped with a named warning (same disposition as inbound
// rows), and valid rows — including the carriers' unset forms (chain
// port 0 / NULL, unconfigured probe target, peer-share 0-0, empty node
// port spec) — must restore untouched.

func r7h01WarningsContain(result *model.ImportResult, substr string) bool {
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func r7h01Count(t *testing.T, r *Repository, query string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := r.DB().Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// Carrier 1: forward_port.port — bad port rows are skipped one by one;
// the forward itself and its valid port rows restore.
func TestR7H01ForwardPortGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7h01-fw.db")
	createRow(t, dest, &model.User{ID: 11, User: "r7h01-user", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 21, Name: "r7h01-tunnel", Type: 1, Flow: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 41, UserID: 11, TunnelID: 21, Num: 1, Flow: 0, Status: 1})

	cases := []struct {
		name     string
		nodeID   int64
		port     int
		wantLand bool
	}{
		{"zero", 71, 0, false},
		{"negative", 72, -5, false},
		{"above-70000", 73, 70000, false},
		{"boundary-65536", 74, 65536, false},
		{"valid", 75, 23001, true},
	}
	ports := make([]model.ForwardPortBackup, 0, len(cases))
	for _, c := range cases {
		createRow(t, dest, &model.Node{ID: c.nodeID, Name: "r7h01-node-" + c.name, Secret: "s", ServerIP: "203.0.113.10", Port: "1000-65535", CreatedTime: now, Status: 1})
		ports = append(ports, model.ForwardPortBackup{NodeID: c.nodeID, Port: c.port})
	}
	backup := &model.BackupData{Version: "1.0", Forwards: []model.ForwardBackup{
		{ID: 31, UserID: 11, UserName: "r7h01-user", Name: "r7h01-fwd", TunnelID: 21, RemoteAddr: "203.0.113.20:443", Status: 1, CreatedTime: now, ForwardPorts: &ports},
	}}
	result, err := dest.Import(backup, []string{"forwards"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["forwardPorts"]; got != 4 {
		t.Fatalf("R7-H-01 forward_port: SkippedRows[forwardPorts] = %d, want 4, warnings=%v", got, result.ImportWarnings)
	}
	for _, c := range cases {
		n := r7h01Count(t, dest, `SELECT COUNT(1) FROM forward_port WHERE forward_id = 31 AND node_id = ? AND port = ?`, c.nodeID, c.port)
		if c.wantLand && n != 1 {
			t.Fatalf("R7-H-01 forward_port %s: valid port %d must restore, got %d rows", c.name, c.port, n)
		}
		if !c.wantLand && n != 0 {
			t.Fatalf("R7-H-01 forward_port %s: bad port %d must be skipped, got %d rows", c.name, c.port, n)
		}
	}
	if !r7h01WarningsContain(result, "70000") || !r7h01WarningsContain(result, "65536") {
		t.Fatalf("R7-H-01 forward_port: warnings must name the bad ports, got %v", result.ImportWarnings)
	}
}

// Carrier 2: chain_tunnel.port — negative and >65535 explicit ports are
// skipped per chain row; 0 and NULL are the legitimate "undetermined"
// form (CLOSED-LIST R-02/A3-S1) and must restore untouched.
func TestR7H01ChainTunnelPortGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7h01-chain.db")
	createRow(t, dest, &model.Node{ID: 71, Name: "r7h01-node", Secret: "s", ServerIP: "203.0.113.10", Port: "1000-65535", CreatedTime: now, Status: 1})
	intPtr := func(v int) *int { return &v }

	cases := []struct {
		name     string
		tunnelID int64
		chainID  int64
		port     *int
		wantLand bool
	}{
		{"negative", 21, 51, intPtr(-5), false},
		{"above-70001", 22, 52, intPtr(70001), false},
		{"boundary-65536", 23, 53, intPtr(65536), false},
		{"zero-undetermined", 24, 54, intPtr(0), true},
		{"null-undetermined", 25, 55, nil, true},
		{"valid", 26, 56, intPtr(23456), true},
	}
	tunnels := make([]model.TunnelBackup, 0, len(cases))
	for _, c := range cases {
		tunnels = append(tunnels, model.TunnelBackup{
			ID: c.tunnelID, Name: "r7h01-t-" + c.name, Type: 2, Flow: 1, TrafficRatio: 1, Status: 1, CreatedTime: now,
			ChainTunnels: []model.ChainTunnelBackup{{ID: c.chainID, TunnelID: c.tunnelID, ChainType: "1", NodeID: 71, Port: c.port}},
		})
	}
	backup := &model.BackupData{Version: "1.0", Tunnels: tunnels}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["chainTunnels"]; got != 3 {
		t.Fatalf("R7-H-01 chain_tunnel: SkippedRows[chainTunnels] = %d, want 3, warnings=%v", got, result.ImportWarnings)
	}
	for _, c := range cases {
		n := r7h01Count(t, dest, `SELECT COUNT(1) FROM chain_tunnel WHERE id = ?`, c.chainID)
		if c.wantLand && n != 1 {
			t.Fatalf("R7-H-01 chain_tunnel %s: chain row must restore, got %d rows", c.name, n)
		}
		if !c.wantLand && n != 0 {
			t.Fatalf("R7-H-01 chain_tunnel %s: chain row must be skipped, got %d rows", c.name, n)
		}
		// The tunnel row itself is not the bad carrier and must restore.
		if n := r7h01Count(t, dest, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, c.tunnelID); n != 1 {
			t.Fatalf("R7-H-01 chain_tunnel %s: tunnel row must restore, got %d rows", c.name, n)
		}
	}
	// The restored 0 must stay 0 (undetermined), not be rewritten.
	var zeroPort sql.NullInt64
	if err := dest.DB().Raw(`SELECT port FROM chain_tunnel WHERE id = 54`).Scan(&zeroPort).Error; err != nil {
		t.Fatalf("read chain 54 port: %v", err)
	}
	if !zeroPort.Valid || zeroPort.Int64 != 0 {
		t.Fatalf("R7-H-01 chain_tunnel: restored undetermined port = %+v, want valid 0", zeroPort)
	}
}

// Carrier 3: tunnel.probe_target_port — a configured probe target must
// carry a 1-65535 port; the whole tunnel row is skipped otherwise. The
// unconfigured form (empty host + port 0) restores untouched.
func TestR7H01TunnelProbeTargetPortGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7h01-probe.db")
	cases := []struct {
		name     string
		id       int64
		host     string
		port     int
		wantLand bool
	}{
		{"above-70000", 21, "www.bing.com", 70000, false},
		{"negative", 22, "www.bing.com", -1, false},
		{"boundary-65536", 23, "www.bing.com", 65536, false},
		{"zero-with-host", 24, "www.bing.com", 0, false},
		{"valid", 25, "www.bing.com", 443, true},
		{"unconfigured", 26, "", 0, true},
	}
	tunnels := make([]model.TunnelBackup, 0, len(cases))
	for _, c := range cases {
		tunnels = append(tunnels, model.TunnelBackup{
			ID: c.id, Name: "r7h01-p-" + c.name, Type: 1, Flow: 1, TrafficRatio: 1, Status: 1, CreatedTime: now,
			ProbeTargetHost: c.host, ProbeTargetPort: c.port,
		})
	}
	backup := &model.BackupData{Version: "1.0", Tunnels: tunnels}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["tunnels"]; got != 4 {
		t.Fatalf("R7-H-01 probe_target_port: SkippedRows[tunnels] = %d, want 4, warnings=%v", got, result.ImportWarnings)
	}
	for _, c := range cases {
		n := r7h01Count(t, dest, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, c.id)
		if c.wantLand && n != 1 {
			t.Fatalf("R7-H-01 probe_target_port %s: tunnel must restore, got %d rows", c.name, n)
		}
		if !c.wantLand && n != 0 {
			t.Fatalf("R7-H-01 probe_target_port %s: tunnel must be skipped, got %d rows", c.name, n)
		}
	}
	var probePort int64
	if err := dest.DB().Raw(`SELECT probe_target_port FROM tunnel WHERE id = 25`).Scan(&probePort).Error; err != nil || probePort != 443 {
		t.Fatalf("R7-H-01 probe_target_port valid: restored port = %d err=%v, want 443", probePort, err)
	}
}

// Carrier 4: node_ssh_config.port — out-of-range rows are skipped; in
// particular port 0 must NOT silently land as 22 via the GORM default
// (the gate runs before any insert).
func TestR7H01NodeSSHConfigPortGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7h01-ssh.db")
	cases := []struct {
		name     string
		nodeID   int64
		port     int
		wantLand bool
	}{
		{"above-70000", 71, 70000, false},
		{"negative", 72, -22, false},
		{"zero", 73, 0, false},
		{"boundary-65536", 74, 65536, false},
		{"valid", 75, 2222, true},
	}
	rows := make([]model.NodeSSHConfigBackup, 0, len(cases))
	for _, c := range cases {
		createRow(t, dest, &model.Node{ID: c.nodeID, Name: "r7h01-node-" + c.name, Secret: "s", ServerIP: "203.0.113.10", Port: "1000-65535", CreatedTime: now, Status: 1, ForwardMode: "nftables"})
		rows = append(rows, model.NodeSSHConfigBackup{NodeID: c.nodeID, Host: "203.0.113.10", Port: c.port, Username: "root", AuthType: "password", Password: "pw", SudoMode: "none", CreatedTime: now})
	}
	backup := &model.BackupData{Version: "1.0", NodeSSHConfigs: rows}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["nodeSSHConfigs"]; got != 4 {
		t.Fatalf("R7-H-01 ssh port: SkippedRows[nodeSSHConfigs] = %d, want 4, warnings=%v", got, result.ImportWarnings)
	}
	if got := result.NodeSSHConfigsImported; got != 1 {
		t.Fatalf("R7-H-01 ssh port: NodeSSHConfigsImported = %d, want 1", got)
	}
	for _, c := range cases {
		n := r7h01Count(t, dest, `SELECT COUNT(1) FROM node_ssh_config WHERE node_id = ?`, c.nodeID)
		if c.wantLand && n != 1 {
			t.Fatalf("R7-H-01 ssh port %s: config must restore, got %d rows", c.name, n)
		}
		if !c.wantLand && n != 0 {
			t.Fatalf("R7-H-01 ssh port %s: config must be skipped (not landed, not rewritten to 22), got %d rows", c.name, n)
		}
	}
	var sshPort int64
	if err := dest.DB().Raw(`SELECT port FROM node_ssh_config WHERE node_id = 75`).Scan(&sshPort).Error; err != nil || sshPort != 2222 {
		t.Fatalf("R7-H-01 ssh port valid: restored port = %d err=%v, want 2222", sshPort, err)
	}
}

// Carrier 5: peer_share port ranges — bounds must be in range and
// ordered. A set range must be a full 1-65535 ordered range; the fully
// unset form (0,0), which the save face accepts and consumers treat as
// fail-closed "not configured", restores untouched.
func TestR7H01PeerSharePortRangeGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7h01-share.db")
	createRow(t, dest, &model.Node{ID: 71, Name: "r7h01-node", Secret: "s", ServerIP: "203.0.113.10", Port: "1000-65535", CreatedTime: now, Status: 1})
	cases := []struct {
		name     string
		id       int64
		start    int
		end      int
		wantLand bool
	}{
		{"out-of-range-pair", 81, 70000, -5, false},
		{"inverted", 82, 5000, 4000, false},
		{"boundary-65536", 83, 65536, 65537, false},
		{"negative-start", 84, -1, 100, false},
		{"half-set", 85, 0, 5000, false},
		{"valid", 86, 4000, 5000, true},
		{"unset", 87, 0, 0, true},
	}
	rows := make([]model.PeerShareBackup, 0, len(cases))
	for _, c := range cases {
		rows = append(rows, model.PeerShareBackup{
			ID: c.id, Name: "r7h01-share-" + c.name, NodeID: 71, Token: "r7h01-token-" + c.name,
			PortRangeStart: c.start, PortRangeEnd: c.end, IsActive: 1, CreatedTime: now, UpdatedTime: now,
		})
	}
	backup := &model.BackupData{Version: "1.0", PeerShares: rows}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["peerShares"]; got != 5 {
		t.Fatalf("R7-H-01 peer_share range: SkippedRows[peerShares] = %d, want 5, warnings=%v", got, result.ImportWarnings)
	}
	for _, c := range cases {
		n := r7h01Count(t, dest, `SELECT COUNT(1) FROM peer_share WHERE id = ?`, c.id)
		if c.wantLand && n != 1 {
			t.Fatalf("R7-H-01 peer_share %s: share must restore, got %d rows", c.name, n)
		}
		if !c.wantLand && n != 0 {
			t.Fatalf("R7-H-01 peer_share %s: share (%d,%d) must be skipped, got %d rows", c.name, c.start, c.end, n)
		}
	}
	var start, end int64
	row := dest.DB().Raw(`SELECT port_range_start, port_range_end FROM peer_share WHERE id = 86`).Row()
	if err := row.Scan(&start, &end); err != nil || start != 4000 || end != 5000 {
		t.Fatalf("R7-H-01 peer_share valid: restored range = %d-%d err=%v, want 4000-5000", start, end, err)
	}
}

// Carrier 6: node.port range strings — every token of the spec must be
// a port or an ordered range within 1-65535; a bad spec skips the node
// row. The empty spec (unset) restores untouched.
func TestR7H01NodePortSpecGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7h01-nodeport.db")
	cases := []struct {
		name     string
		id       int64
		spec     string
		wantLand bool
	}{
		{"not-a-range", 71, "not-a-range", false},
		{"above-range", 72, "70000-80000", false},
		{"zero", 73, "0", false},
		{"negative", 74, "-5", false},
		{"inverted", 75, "5000-4000", false},
		{"mixed-bad-token", 76, "1000-2000,70000", false},
		{"valid-range", 77, "1000-2000", true},
		{"valid-single", 78, "30000", true},
		{"valid-multi", 79, "1000-2000,3000-4000", true},
		{"unset-empty", 80, "", true},
	}
	nodes := make([]model.NodeBackup, 0, len(cases))
	for _, c := range cases {
		nodes = append(nodes, model.NodeBackup{
			ID: c.id, Name: "r7h01-np-" + c.name, Secret: "s", ServerIP: "203.0.113.10",
			Port: c.spec, CreatedTime: now, Status: 1, TCPListenAddr: "[::]", UDPListenAddr: "[::]",
		})
	}
	backup := &model.BackupData{Version: "1.0", Nodes: nodes}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["nodes"]; got != 6 {
		t.Fatalf("R7-H-01 node.port: SkippedRows[nodes] = %d, want 6, warnings=%v", got, result.ImportWarnings)
	}
	for _, c := range cases {
		n := r7h01Count(t, dest, `SELECT COUNT(1) FROM node WHERE id = ?`, c.id)
		if c.wantLand && n != 1 {
			t.Fatalf("R7-H-01 node.port %s: node must restore, got %d rows", c.name, n)
		}
		if !c.wantLand && n != 0 {
			t.Fatalf("R7-H-01 node.port %s: node with spec %q must be skipped, got %d rows", c.name, c.spec, n)
		}
	}
	var spec string
	if err := dest.DB().Raw(`SELECT port FROM node WHERE id = 77`).Scan(&spec).Error; err != nil || spec != "1000-2000" {
		t.Fatalf("R7-H-01 node.port valid: restored spec = %q err=%v, want %q", spec, err, "1000-2000")
	}
}

// Valid restores across all six carriers in one backup must pass
// untouched, with zero skips and zero port-gate warnings.
func TestR7H01ValidRestoresPassUntouched(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7h01-valid.db")
	createRow(t, dest, &model.User{ID: 11, User: "r7h01-user", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.UserTunnel{ID: 41, UserID: 11, TunnelID: 21, Num: 1, Flow: 0, Status: 1})
	intPtr := func(v int) *int { return &v }
	fwPorts := []model.ForwardPortBackup{{NodeID: 71, Port: 23001}}

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 71, Name: "r7h01-valid-node", Secret: "s", ServerIP: "203.0.113.10", Port: "1000-2000", CreatedTime: now, Status: 1, TCPListenAddr: "[::]", UDPListenAddr: "[::]", ForwardMode: "nftables"},
		},
		NodeSSHConfigs: []model.NodeSSHConfigBackup{
			{NodeID: 71, Host: "203.0.113.10", Port: 2222, Username: "root", AuthType: "password", Password: "pw", SudoMode: "none", CreatedTime: now},
		},
		PeerShares: []model.PeerShareBackup{
			{ID: 81, Name: "r7h01-valid-share", NodeID: 71, Token: "r7h01-valid-token", PortRangeStart: 4000, PortRangeEnd: 5000, IsActive: 1, CreatedTime: now, UpdatedTime: now},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 21, Name: "r7h01-valid-tunnel", Type: 2, Flow: 1, TrafficRatio: 1, Status: 1, CreatedTime: now,
				ProbeTargetHost: "www.bing.com", ProbeTargetPort: 443,
				ChainTunnels: []model.ChainTunnelBackup{
					{ID: 51, TunnelID: 21, ChainType: "1", NodeID: 71, Port: intPtr(0)},
					{ID: 52, TunnelID: 21, ChainType: "2", NodeID: 71, Port: intPtr(23456)},
				}},
		},
		Forwards: []model.ForwardBackup{
			{ID: 31, UserID: 11, UserName: "r7h01-user", Name: "r7h01-valid-fwd", TunnelID: 21, RemoteAddr: "203.0.113.20:443", Status: 1, CreatedTime: now, ForwardPorts: &fwPorts},
		},
	}
	result, err := dest.Import(backup, []string{"nodes", "tunnels", "forwards"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	for seg, got := range result.SkippedRows {
		if got != 0 {
			t.Fatalf("R7-H-01 valid restores: SkippedRows[%s] = %d, want 0, warnings=%v", seg, got, result.ImportWarnings)
		}
	}
	checks := []struct {
		what  string
		query string
		args  []interface{}
	}{
		{"node", `SELECT COUNT(1) FROM node WHERE id = 71 AND port = '1000-2000'`, nil},
		{"ssh config port 2222", `SELECT COUNT(1) FROM node_ssh_config WHERE node_id = 71 AND port = 2222`, nil},
		{"peer share range", `SELECT COUNT(1) FROM peer_share WHERE id = 81 AND port_range_start = 4000 AND port_range_end = 5000`, nil},
		{"tunnel probe port 443", `SELECT COUNT(1) FROM tunnel WHERE id = 21 AND probe_target_port = 443`, nil},
		{"chain port 0", `SELECT COUNT(1) FROM chain_tunnel WHERE id = 51 AND port = 0`, nil},
		{"chain port 23456", `SELECT COUNT(1) FROM chain_tunnel WHERE id = 52 AND port = 23456`, nil},
		{"forward port 23001", `SELECT COUNT(1) FROM forward_port WHERE forward_id = 31 AND node_id = 71 AND port = 23001`, nil},
	}
	for _, ch := range checks {
		if n := r7h01Count(t, dest, ch.query, ch.args...); n != 1 {
			t.Fatalf("R7-H-01 valid restores: %s must restore untouched, got %d rows", ch.what, n)
		}
	}
}
