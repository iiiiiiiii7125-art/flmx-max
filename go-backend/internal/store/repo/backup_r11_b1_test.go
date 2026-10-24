package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R11 批 1 关闭断言 ③（R11X-001）：备份恢复路径与保存面同格复验——
// 恢复对自建入站四整对象跑保存面等价校验（validateImportedInboundObjects
// 经同一 kernel.Normalize，只读副本），故 XHTTP Transport.Mode 的收口
// 在恢复面同口径生效：大小写变体格通过校验门（副本上愈合、门不误杀）、
// 集外 bogus 格整行跳过 + 计数 + warning 披露。落库仍按既有口径逐字
// 恢复备份原值（校验副本不回写，R5X-024 口径不变，本批不触碰该护栏）。
func TestR11B1BackupRestoreXHTTPModeGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r11b1-xhttpmode.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	mkInbound := func(id int64, name string, port int, transport string) model.InboundBackup {
		return model.InboundBackup{
			ID: id, KernelID: 1, NodeID: 1, Name: name, Protocol: "vless", Port: port, Enabled: 1, CreatedTime: now,
			Settings:  `{"uuid":"11111111-2222-3333-4444-555555555555"}`,
			Transport: transport,
			Security:  `{"type":"none"}`,
		}
	}
	autoTransport := `{"type":"xhttp","path":"/xh","mode":"AUTO"}`
	packetTransport := `{"type":"xhttp","path":"/xh","mode":"Packet-Up"}`
	goodTransport := `{"type":"xhttp","path":"/xh","mode":"stream-one"}`
	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				mkInbound(20, "xh-auto", 443, autoTransport),
				mkInbound(21, "xh-bogus", 444, `{"type":"xhttp","path":"/xh","mode":"bogus"}`),
				mkInbound(22, "xh-packet", 445, packetTransport),
				mkInbound(23, "xh-good", 446, goodTransport),
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// bogus 行整行跳过；变体两行与合法行通过校验门落库。
	if result.InboundsImported != 3 {
		t.Fatalf("R11X-001 断言③: InboundsImported = %d, want 3 (AUTO/Packet-Up 变体 + 合法行过门，bogus 跳过)", result.InboundsImported)
	}
	if result.SkippedRows["inbounds"] != 1 {
		t.Fatalf("R11X-001 断言③: SkippedRows[inbounds] = %d, want 1 (bogus 行)", result.SkippedRows["inbounds"])
	}
	if n := countWhere(t, dest, "node_inbound", "id = ?", 21); n != 0 {
		t.Fatalf("R11X-001 断言③: bogus mode 入站不得落库，got %d 行", n)
	}
	joined := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(joined, "xh-bogus") || !strings.Contains(joined, "mode") {
		t.Fatalf("R11X-001 断言③: 跳过披露须点名被跳行与 mode 由，warnings=%v", result.ImportWarnings)
	}
	// 过门行逐字恢复（护栏：校验只读副本、落库不回写归一形）。
	for id, want := range map[int64]string{20: autoTransport, 22: packetTransport, 23: goodTransport} {
		var row model.NodeInbound
		if err := dest.DB().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("过门入站 #%d 须落库: %v", id, err)
		}
		if row.Transport != want {
			t.Fatalf("过门入站 #%d 须逐字恢复备份 transport，got %q want %q", id, row.Transport, want)
		}
	}
}
