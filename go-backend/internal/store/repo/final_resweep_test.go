package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 复扫（fix/final-resweep）：从头机械重推发现的新增同族残留（恢复面）。
// 先红后绿：每项修复前应失败，修复后须通过；含合法对照（防过修）。

// R-04（恢复面）：节点 SSH 配置 Host `a..b` 随备份逐字恢复。
func TestResweepImportNodeSSHConfigClearsEmptyLabelHost(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "ssh-restore-src.db")
	createRow(t, source, &model.Node{ID: 3, Name: "nft-node", Secret: "s", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}, ForwardMode: "nftables"})
	createRow(t, source, &model.NodeSSHConfig{NodeID: 3, Host: "a..b", Port: 2222, Username: "root",
		AuthType: "password", Password: sql.NullString{String: "ssh-pw", Valid: true}, SudoMode: "sudo",
		CreatedTime: now, UpdatedTime: now})

	dest := openBackupRepo(t, "ssh-restore-dst.db")
	result := roundTripBackup(t, source, dest)

	var cfg model.NodeSSHConfig
	if err := dest.DB().Where("node_id = ?", 3).First(&cfg).Error; err != nil {
		t.Fatalf("R-04: ssh config row must be restored: %v", err)
	}
	if cfg.Host != "" {
		t.Fatalf("R-04: restored ssh host = %q, want empty (cleared) for a..b", cfg.Host)
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "SSH") && strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("R-04: import warnings must disclose cleared ssh host, got %v", result.ImportWarnings)
	}
}

// R-05：转发 RemoteAddr `a..b:80` 随备份逐字恢复（创建面已 gate，恢复面漂移）。
// 直接 Import 模式（参照 TestFix132ImportForwardsSkipsDangling）：引用行
// 预置在目标库，避免整单往返的 ID 重映射干扰。
func TestResweepImportForwardsSkipsEmptyLabelRemoteAddr(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "fwd-restore-dst.db")
	createRow(t, dest, &model.User{ID: 7, User: "fwd-user", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 5, Name: "tun", Type: 1, Protocol: "tls", Status: 1, CreatedTime: now, UpdatedTime: now})
	// 转发恢复需用户隧道授权（R4X-006 尾部收口），否则整行以
	// forwardsUnauthorized 跳过——与本项无关，须预置。
	createRow(t, dest, &model.UserTunnel{UserID: 7, TunnelID: 5, Num: 99999, Flow: 99999, Status: 1, ExpTime: now + 86400000})
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 11, UserID: 7, UserName: "fwd-user", Name: "bad-target",
				TunnelID: 5, RemoteAddr: "a..b:80", Strategy: "fifo", Status: 1, CreatedTime: now, UpdatedTime: now},
			{ID: 12, UserID: 7, UserName: "fwd-user", Name: "good-target",
				TunnelID: 5, RemoteAddr: "203.0.113.5:443", Strategy: "fifo", Status: 1, CreatedTime: now, UpdatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"forwards"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var count int64
	if err := dest.DB().Model(&model.Forward{}).Where("id = ?", 11).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("R-05: forward with RemoteAddr=a..b:80 must be skipped on restore, got restored")
	}
	if err := dest.DB().Model(&model.Forward{}).Where("id = ?", 12).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("R-05: forward with RemoteAddr=203.0.113.5:443 must be restored (control)")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("R-05: import warnings must disclose skipped forward, got %v", result.ImportWarnings)
	}
}

// R-06：订阅源 URL `https://a..b/x` 随备份逐字恢复（保存面已 gate，恢复面漂移）。
func TestResweepImportRemoteSourceSkipsEmptyLabelURL(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "src-restore-src.db")
	createRow(t, source, &model.RemoteSource{ID: 4, Name: "bad-src", URL: "https://a..b/x",
		Kind: "url", Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.RemoteSource{ID: 5, Name: "good-src", URL: "https://example.com/sub",
		Kind: "url", Enabled: 1, CreatedTime: now, UpdatedTime: now})

	dest := openBackupRepo(t, "src-restore-dst.db")
	result := roundTripBackup(t, source, dest)

	var count int64
	if err := dest.DB().Model(&model.RemoteSource{}).Where("id = ?", 4).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("R-06: remote source with URL=https://a..b/x must be skipped on restore, got restored")
	}
	if err := dest.DB().Model(&model.RemoteSource{}).Where("id = ?", 5).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("R-06: remote source with URL=https://example.com/sub must be restored (control)")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("R-06: import warnings must disclose skipped source, got %v", result.ImportWarnings)
	}
}

// R-08（恢复面）：联邦分享 AllowedDomains 含 `a..b` 条目随备份逐字恢复。
func TestResweepImportPeerShareDropsEmptyLabelDomains(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "share-restore-src.db")
	createRow(t, source, &model.Node{ID: 3, Name: "n3", Secret: "s", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, source, &model.PeerShare{ID: 9, Name: "share-a", NodeID: 3, Token: "share-token-a",
		MaxBandwidth: 1000, PortRangeStart: 20000, PortRangeEnd: 20010, IsActive: 1,
		CreatedTime: now, UpdatedTime: now, AllowedDomains: "example.com,a..b"})

	dest := openBackupRepo(t, "share-restore-dst.db")
	result := roundTripBackup(t, source, dest)

	var share model.PeerShare
	if err := dest.DB().Where("id = ?", 9).First(&share).Error; err != nil {
		t.Fatalf("R-08: peer share must be restored: %v", err)
	}
	if share.AllowedDomains != "example.com" {
		t.Fatalf("R-08: restored allowed_domains = %q, want %q (bad item dropped)", share.AllowedDomains, "example.com")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("R-08: import warnings must disclose dropped domain, got %v", result.ImportWarnings)
	}
}

// R-09：配置恢复面主机名键（panel_domain / ip / github_proxy_url /
// notify_webhook_url）原值恢复，绕过全部保存门。
func TestResweepImportConfigsSkipsEmptyLabelHostKeys(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "cfg-restore-src.db")
	createRow(t, source, &model.ViteConfig{Name: "panel_domain", Value: "a..b", Time: now})
	createRow(t, source, &model.ViteConfig{Name: "github_proxy_url", Value: "https://a..b/x", Time: now})
	// 控制键：非主机名键、且非 Open() 预置键（app_name / hide_footer_brand）。
	createRow(t, source, &model.ViteConfig{Name: "monitor_retention_days", Value: "30", Time: now})

	dest := openBackupRepo(t, "cfg-restore-dst.db")
	result := roundTripBackup(t, source, dest)

	var count int64
	if err := dest.DB().Model(&model.ViteConfig{}).Where("name = ?", "panel_domain").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("R-09: panel_domain=a..b must be skipped on restore, got restored")
	}
	if err := dest.DB().Model(&model.ViteConfig{}).Where("name = ?", "github_proxy_url").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("R-09: github_proxy_url=https://a..b/x must be skipped on restore, got restored")
	}
	var cfg model.ViteConfig
	if err := dest.DB().Where("name = ?", "monitor_retention_days").First(&cfg).Error; err != nil {
		t.Fatalf("R-09: monitor_retention_days must be restored (control): %v", err)
	}
	if cfg.Value != "30" {
		t.Fatalf("R-09: monitor_retention_days = %q, want %q", cfg.Value, "30")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("R-09: import warnings must disclose skipped config keys, got %v", result.ImportWarnings)
	}
}

// D-01（发版后审计）：备份恢复面 importTunnels 逐字恢复链路 ConnectIP
// `a..b`（写面 C-16 明明拒收）。链路行跳过 + warning，与 R-05 同口径。
func TestResweepImportTunnelsSkipsEmptyLabelChainConnectIP(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "tun-chain-restore-dst.db")
	createRow(t, dest, &model.Node{ID: 3, Name: "n3", Secret: "s", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	badConnectIP := "a..b"
	goodConnectIP := "203.0.113.9"
	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 21, Name: "tun-bad-chain", TrafficRatio: 1, Flow: 1, Status: 1, CreatedTime: now,
				ChainTunnels: []model.ChainTunnelBackup{
					{ID: 31, TunnelID: 21, NodeID: 3, ConnectIP: &badConnectIP},
					{ID: 32, TunnelID: 21, NodeID: 3, ConnectIP: &goodConnectIP},
				}},
		},
	}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var count int64
	if err := dest.DB().Model(&model.ChainTunnel{}).Where("id = ?", 31).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("D-01: chain with ConnectIP=a..b must be skipped on restore, got restored")
	}
	if err := dest.DB().Model(&model.ChainTunnel{}).Where("id = ?", 32).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("D-01: chain with ConnectIP=203.0.113.9 must be restored (control)")
	}
	// 隧道行本身照常恢复（只跳坏链路行）。
	if err := dest.DB().Model(&model.Tunnel{}).Where("id = ?", 21).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("D-01: tunnel row itself must be restored (only the bad chain row is skipped)")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("D-01: import warnings must disclose skipped chain, got %v", result.ImportWarnings)
	}
}

// D-02（发版后审计）：备份恢复面 importTunnels 逐字恢复隧道
// ProbeTargetHost `a..b`（写面 C-26 明明拒收）。整行跳过 + warning。
func TestResweepImportTunnelsSkipsEmptyLabelProbeTargetHost(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "tun-probe-restore-dst.db")
	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 6, Name: "bad-probe", TrafficRatio: 1, Flow: 1, Status: 1, CreatedTime: now,
				ProbeTargetHost: "a..b", ProbeTargetPort: 443},
			{ID: 7, Name: "good-probe", TrafficRatio: 1, Flow: 1, Status: 1, CreatedTime: now,
				ProbeTargetHost: "www.bing.com", ProbeTargetPort: 443},
		},
	}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var count int64
	if err := dest.DB().Model(&model.Tunnel{}).Where("id = ?", 6).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("D-02: tunnel with ProbeTargetHost=a..b must be skipped on restore, got restored")
	}
	var tun model.Tunnel
	if err := dest.DB().Where("id = ?", 7).First(&tun).Error; err != nil {
		t.Fatalf("D-02: tunnel with ProbeTargetHost=www.bing.com must be restored (control): %v", err)
	}
	if tun.ProbeTargetHost != "www.bing.com" {
		t.Fatalf("D-02: restored probe target host = %q, want %q", tun.ProbeTargetHost, "www.bing.com")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("D-02: import warnings must disclose skipped tunnel, got %v", result.ImportWarnings)
	}
}

// X-03（本轮重推新发现）：备份恢复面 importNodes 逐字恢复远端节点 URL
// `https://a..b:8443`（写面 C-35 validateRemoteNodeURL 明明拒收——
// IsSafeRemoteAddr 做 DNS，`a..b` 解析失败即拒）。恢复面与写面同口径：
// 空标签 URL 清空 + warning（与同函数 C-17 节点地址面同型，不整行跳过，
// 否则依赖该节点的隧道/转发段同批恢复会被连带误伤）。
func TestResweepImportNodesClearsEmptyLabelRemoteURL(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "node-remoteurl-restore-dst.db")
	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 3, Name: "bad-remote", Secret: "s", ServerIP: "10.0.0.3", Port: "10000-10100",
				IsRemote: 1, RemoteURL: "https://a..b:8443", CreatedTime: now},
			{ID: 4, Name: "good-remote", Secret: "s", ServerIP: "10.0.0.4", Port: "10000-10100",
				IsRemote: 1, RemoteURL: "https://example.com:8443", CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	var node3 model.Node
	if err := dest.DB().Where("id = ?", 3).First(&node3).Error; err != nil {
		t.Fatalf("X-03: node row must be restored (only the bad URL is cleared): %v", err)
	}
	if node3.RemoteURL.Valid && node3.RemoteURL.String != "" {
		t.Fatalf("X-03: restored remote_url = %q, want empty (cleared) for https://a..b:8443", node3.RemoteURL.String)
	}
	// 注意：不得复用 node3 做第二次 First——GORM 会把结构体主键
	// 作为附加条件，导致 id=4 的查询恒为空。
	var node4 model.Node
	if err := dest.DB().Where("id = ?", 4).First(&node4).Error; err != nil {
		t.Fatalf("X-03: node with RemoteURL=https://example.com:8443 must be restored (control): %v", err)
	}
	if !node4.RemoteURL.Valid || node4.RemoteURL.String != "https://example.com:8443" {
		t.Fatalf("X-03: restored remote_url = %q, want %q", node4.RemoteURL.String, "https://example.com:8443")
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "a..b") {
			found = true
		}
	}
	if !found {
		t.Fatalf("X-03: import warnings must disclose cleared remote URL, got %v", result.ImportWarnings)
	}
}

// F-PORT-01（任务 3）：节点端口段无上限——`1-99999` 会产出 >65535 的候选端口。
func TestResweepPortRangeSpecClampsTo65535(t *testing.T) {
	for _, p := range parsePortRangeSpec("1-99999") {
		if p > 65535 {
			t.Fatalf("parsePortRangeSpec(1-99999) yielded port %d > 65535", p)
		}
	}
	if got := parsePortRangeSpec("70000-80000"); len(got) != 0 {
		t.Fatalf("parsePortRangeSpec(70000-80000) = %v, want empty", got)
	}
	if got := parsePortRangeSpec("1000-65535"); len(got) == 0 || got[len(got)-1] != 65535 {
		t.Fatalf("parsePortRangeSpec(1000-65535) must keep 65535, got %v", got)
	}
	minP, maxP := parsePortRangeMinMax("1-99999")
	if maxP != 65535 {
		t.Fatalf("parsePortRangeMinMax(1-99999) = (%d, %d), want max 65535", minP, maxP)
	}
	minP, maxP = parsePortRangeMinMax("70000-80000")
	if minP != 0 || maxP != 0 {
		t.Fatalf("parsePortRangeMinMax(70000-80000) = (%d, %d), want (0, 0)", minP, maxP)
	}
}
