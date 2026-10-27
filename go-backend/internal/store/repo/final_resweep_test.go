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
