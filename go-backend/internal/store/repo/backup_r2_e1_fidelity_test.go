package repo

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R2-E1 备份保真簇回归钉（FIX-WAVE3-A）：
//   - E1-06：节点定时重启三字段导出/恢复保真＋恢复面镜像写面归一；
//   - E1-08：SSH 主机指纹随备份；旧备份不得抹除现行钉入值且回执
//     点名；
//   - E1-14：ForwardPort.BindWarning 导出/恢复保真；
//   - E1-03：DeleteSpeedLimit 同事务剥离三处引用（置 NULL）。

func openR2E1Dst(t *testing.T) *Repository {
	t.Helper()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	return dst
}

func TestBackupNodeRestartScheduleRoundTrip(t *testing.T) {
	dst := openR2E1Dst(t)

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 61, Name: "valid", Secret: "s", ServerIP: "192.0.2.61", Status: 1,
				RestartEnabled: 1, RestartTime: "03:30", RestartJitterMinutes: 45},
			{ID: 62, Name: "wild", Secret: "s", ServerIP: "192.0.2.62", Status: 1,
				RestartEnabled: 7, RestartTime: "25:99", RestartJitterMinutes: 9999},
			// 旧备份形态：三字段全缺（零值）。
			{ID: 63, Name: "legacy", Secret: "s", ServerIP: "192.0.2.63", Status: 1},
		},
	}
	result, err := dst.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatal(err)
	}
	if result.NodesImported != 3 {
		t.Fatalf("NodesImported = %d, want 3, warnings=%v", result.NodesImported, result.ImportWarnings)
	}
	readNode := func(id int64) model.Node {
		t.Helper()
		var n model.Node
		if err := dst.db.Where("id = ?", id).First(&n).Error; err != nil {
			t.Fatalf("read node #%d: %v", id, err)
		}
		return n
	}
	if n := readNode(61); n.RestartEnabled != 1 || n.RestartTime != "03:30" || n.RestartJitterMinutes != 45 {
		t.Fatalf("node 61 restart triple = %d/%q/%d, want 1/03:30/45 (verbatim round-trip)", n.RestartEnabled, n.RestartTime, n.RestartJitterMinutes)
	}
	if n := readNode(62); n.RestartEnabled != 0 || n.RestartTime != "04:00" || n.RestartJitterMinutes != 30 {
		t.Fatalf("node 62 restart triple = %d/%q/%d, want 0/04:00/30 (normalized to write-face defaults)", n.RestartEnabled, n.RestartTime, n.RestartJitterMinutes)
	}
	if n := readNode(63); n.RestartEnabled != 0 || n.RestartTime != "04:00" {
		t.Fatalf("node 63 restart = %d/%q, want 0/04:00 (legacy backup falls back to model defaults)", n.RestartEnabled, n.RestartTime)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"25:99", "9999"} {
		if !strings.Contains(warnText, want) {
			t.Fatalf("receipt must name original out-of-range value %q, warnings=%v", want, result.ImportWarnings)
		}
	}
	// 导出面：恢复后的节点再导出须携带三字段（导出赋值缺失时此处
	// 会回落零值）。
	exported, err := dst.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	var got *model.NodeBackup
	for i := range exported.Nodes {
		if exported.Nodes[i].ID == 61 {
			got = &exported.Nodes[i]
		}
	}
	if got == nil || got.RestartEnabled != 1 || got.RestartTime != "03:30" || got.RestartJitterMinutes != 45 {
		t.Fatalf("exported node 61 restart triple = %+v, want 1/03:30/45", got)
	}
}

func TestBackupSSHHostKeyFingerprintFidelity(t *testing.T) {
	dst := openR2E1Dst(t)

	// 目标库先行状态：节点 71 的 SSH 配置已钉入指纹。
	if err := dst.db.Create(&model.Node{ID: 71, Name: "n71", Secret: "s", ServerIP: "192.0.2.71", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.NodeSSHConfig{NodeID: 71, Host: "192.0.2.71", Port: 22, HostKeyFingerprint: "SHA256:existing-pin"}).Error; err != nil {
		t.Fatal(err)
	}
	readPin := func(nodeID int64) string {
		t.Helper()
		var c model.NodeSSHConfig
		if err := dst.db.Where("node_id = ?", nodeID).First(&c).Error; err != nil {
			t.Fatalf("read ssh config #%d: %v", nodeID, err)
		}
		return c.HostKeyFingerprint
	}

	// 1) 旧备份（不携带指纹）恢复：现行钉入值保留＋回执点名。
	legacy := &model.BackupData{
		Version: "1.0",
		NodeSSHConfigs: []model.NodeSSHConfigBackup{
			{NodeID: 71, Host: "192.0.2.71", Port: 22, Username: "root", SudoMode: "none"},
		},
	}
	result, err := dst.Import(legacy, []string{"nodes"})
	if err != nil {
		t.Fatal(err)
	}
	if pin := readPin(71); pin != "SHA256:existing-pin" {
		t.Fatalf("after legacy restore pin = %q, want SHA256:existing-pin (must not be wiped)", pin)
	}
	if warnText := strings.Join(result.ImportWarnings, "\n"); !strings.Contains(warnText, "指纹已保留") {
		t.Fatalf("receipt must name the preserved pin, warnings=%v", result.ImportWarnings)
	}

	// 2) 新备份携带指纹恢复：冲突路径也须落备份值。
	withPin := &model.BackupData{
		Version: "1.0",
		NodeSSHConfigs: []model.NodeSSHConfigBackup{
			{NodeID: 71, Host: "192.0.2.71", Port: 22, Username: "root", SudoMode: "none", HostKeyFingerprint: "SHA256:backup-pin"},
		},
	}
	if _, err := dst.Import(withPin, []string{"nodes"}); err != nil {
		t.Fatal(err)
	}
	if pin := readPin(71); pin != "SHA256:backup-pin" {
		t.Fatalf("after pinned restore pin = %q, want SHA256:backup-pin", pin)
	}

	// 3) 新节点插入路径：指纹随行落库；导出面携带。
	if err := dst.db.Create(&model.Node{ID: 72, Name: "n72", Secret: "s", ServerIP: "192.0.2.72", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	fresh := &model.BackupData{
		Version: "1.0",
		NodeSSHConfigs: []model.NodeSSHConfigBackup{
			{NodeID: 72, Host: "192.0.2.72", Port: 22, Username: "root", SudoMode: "none", HostKeyFingerprint: "SHA256:fresh-pin"},
		},
	}
	if _, err := dst.Import(fresh, []string{"nodes"}); err != nil {
		t.Fatal(err)
	}
	if pin := readPin(72); pin != "SHA256:fresh-pin" {
		t.Fatalf("fresh insert pin = %q, want SHA256:fresh-pin", pin)
	}
	exported, err := dst.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range exported.NodeSSHConfigs {
		if c.NodeID == 72 {
			found = true
			if c.HostKeyFingerprint != "SHA256:fresh-pin" {
				t.Fatalf("exported fingerprint = %q, want SHA256:fresh-pin", c.HostKeyFingerprint)
			}
		}
	}
	if !found {
		t.Fatalf("exported backup lacks node 72 ssh config")
	}
}

func TestBackupForwardPortBindWarningRoundTrip(t *testing.T) {
	now := time.Now().UnixMilli()
	dst := openR2E1Dst(t)

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 81, Name: "n81", Secret: "s", ServerIP: "192.0.2.81", Status: 1},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 82, Name: "t82", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now},
		},
		Forwards: []model.ForwardBackup{
			{ID: 8101, UserID: 1, TunnelID: 82, Name: "warned", RemoteAddr: "203.0.113.81:80", Strategy: "fifo", Status: 1, CreatedTime: now, ForwardPorts: fpSlice(
				model.ForwardPortBackup{NodeID: 81, Port: 31001, BindWarning: "bind fallback: requested in_ip unavailable"},
				model.ForwardPortBackup{NodeID: 81, Port: 31002},
			)},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 1, UserID: 1, TunnelID: 82, Num: 99999, Flow: 99999, FlowResetTime: 1, ExpTime: 2727251700000, Status: 1},
		},
	}
	result, err := dst.Import(backup, []string{"nodes", "tunnels", "forwards", "userTunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ForwardsImported != 1 {
		t.Fatalf("ForwardsImported = %d, want 1, warnings=%v", result.ForwardsImported, result.ImportWarnings)
	}
	var rows []model.ForwardPort
	if err := dst.db.Where("forward_id = ?", 8101).Order("port").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("forward port rows = %d, want 2", len(rows))
	}
	if !rows[0].BindWarning.Valid || rows[0].BindWarning.String != "bind fallback: requested in_ip unavailable" {
		t.Fatalf("port 31001 bind warning = %v, want the backup warning restored", rows[0].BindWarning)
	}
	if rows[1].BindWarning.Valid {
		t.Fatalf("port 31002 bind warning = %v, want NULL (no warning in backup)", rows[1].BindWarning)
	}
	// 导出面回带。
	exported, err := dst.ExportAll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range exported.Forwards {
		if f.ID == 8101 && f.ForwardPorts != nil {
			for _, p := range *f.ForwardPorts {
				if p.Port == 31001 {
					found = true
					if p.BindWarning != "bind fallback: requested in_ip unavailable" {
						t.Fatalf("exported bind warning = %q, want the stored warning", p.BindWarning)
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("exported backup lacks forward 8101 port 31001")
	}
}

func TestDeleteSpeedLimitClearsReferences(t *testing.T) {
	dst := openR2E1Dst(t)
	now := time.Now().UnixMilli()

	if err := dst.db.Create(&model.SpeedLimit{ID: 91, Name: "doomed", Speed: 1024, CreatedTime: now, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.SpeedLimit{ID: 92, Name: "keeper", Speed: 2048, CreatedTime: now, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	ref := func(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
	if err := dst.db.Create(&model.Forward{ID: 9101, UserID: 1, Name: "f-speed", SpeedID: ref(91), CreatedTime: now, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.Forward{ID: 9102, UserID: 1, Name: "f-ipspeed", IPSpeedID: ref(91), CreatedTime: now, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.Forward{ID: 9103, UserID: 1, Name: "f-other", SpeedID: ref(92), IPSpeedID: ref(92), CreatedTime: now, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.UserTunnel{ID: 9104, UserID: 1, TunnelID: 93, SpeedID: ref(91), Num: 1, Flow: 1, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}

	if err := dst.DeleteSpeedLimit(91); err != nil {
		t.Fatal(err)
	}

	var ruleCount int64
	if err := dst.db.Model(&model.SpeedLimit{}).Where("id = ?", 91).Count(&ruleCount).Error; err != nil {
		t.Fatal(err)
	}
	if ruleCount != 0 {
		t.Fatalf("speed limit 91 rows = %d, want 0 (deleted)", ruleCount)
	}
	readForward := func(id int64) model.Forward {
		t.Helper()
		var f model.Forward
		if err := dst.db.Where("id = ?", id).First(&f).Error; err != nil {
			t.Fatalf("read forward #%d: %v", id, err)
		}
		return f
	}
	if f := readForward(9101); f.SpeedID.Valid {
		t.Fatalf("forward 9101 speed_id = %v, want NULL after rule delete", f.SpeedID)
	}
	if f := readForward(9102); f.IPSpeedID.Valid {
		t.Fatalf("forward 9102 ip_speed_id = %v, want NULL after rule delete", f.IPSpeedID)
	}
	if f := readForward(9103); !f.SpeedID.Valid || f.SpeedID.Int64 != 92 || !f.IPSpeedID.Valid || f.IPSpeedID.Int64 != 92 {
		t.Fatalf("forward 9103 refs = %v/%v, want 92/92 untouched", f.SpeedID, f.IPSpeedID)
	}
	var ut model.UserTunnel
	if err := dst.db.Where("id = ?", 9104).First(&ut).Error; err != nil {
		t.Fatal(err)
	}
	if ut.SpeedID.Valid {
		t.Fatalf("user_tunnel 9104 speed_id = %v, want NULL after rule delete", ut.SpeedID)
	}
}
