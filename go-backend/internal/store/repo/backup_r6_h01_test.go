package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R6-H-01 (P3): imported-type inbound rows (kernel_id<=0) whose row Listen
// is empty or literal "::" are skipped on restore even when Extra is a
// fully legal proxy. Mechanism: importInbounds normalizes "" -> "::"
// (repository_inbound_backup.go), then the imported-type gate runs the
// dial-address predicate ValidateImportedProxyRowAddress on it, and
// ValidateImportHost rejects unspecified addresses -- the code first
// produces "::" and then rejects its own product. The dial address of an
// imported row lives in Extra (validated separately by
// ValidateImportedProxy); a missing/unspecified row Listen carries no
// dial address at all.
//
// Red test first: #105/#106 must land after the fix (with Extra verbatim
// and stored Listen "::"). Control grids that must NOT change:
// #102 routable Listen lands; #107 loopback Listen still skipped (R8X-004
// facet A intact); #108 "::" Listen + bad Extra server still skipped via
// Extra validation (no hole); #109 "::" Listen + empty Extra still
// skipped (legacy Extra-less rows keep the row gate as their only address
// check); #110 "::" Listen + "{}" Extra and no other content still
// skipped (inert shell, same as today); #112 self-built "::" Listen
// unchanged (Normalize IP-literal face).

const r6h01LegalVMessExtra = `{"type":"vmess","name":"r6h-vmess","server":"203.0.113.10","port":443,"uuid":"33333333-3333-3333-3333-333333333333","alterId":0,"cipher":"AES-128-GCM","network":"ws","ws-opts":{"host":"ws.example.net","path":"/"},"udp":true}`

const r6h01BadServerExtra = `{"type":"vmess","name":"r6h-bad","server":"127.0.0.1","port":443,"uuid":"44444444-4444-4444-4444-444444444444","alterId":0,"cipher":"AES-128-GCM","udp":true}`

func TestR6H01ImportedEmptyListenRestores(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r6h01.db")
	createRow(t, dest, &model.NodeKernel{ID: 201, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now, UpdatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{Inbounds: []model.InboundBackup{
			{ID: 102, KernelID: 0, NodeID: 0, Name: "r6h-control", Protocol: "vmess", Listen: "203.0.113.10", Port: 10102, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 105, KernelID: 0, NodeID: 0, Name: "r6h-empty", Protocol: "vmess", Listen: "", Port: 10105, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 106, KernelID: 0, NodeID: 0, Name: "r6h-unspec", Protocol: "vmess", Listen: "::", Port: 10106, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 107, KernelID: 0, NodeID: 0, Name: "r6h-loopback", Protocol: "vmess", Listen: "127.0.0.1", Port: 10107, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 108, KernelID: 0, NodeID: 0, Name: "r6h-bad-extra", Protocol: "vmess", Listen: "::", Port: 10108, Extra: r6h01BadServerExtra, Enabled: 1, CreatedTime: now},
			{ID: 109, KernelID: 0, NodeID: 0, Name: "r6h-no-extra", Protocol: "vmess", Listen: "::", Port: 10109, Extra: "", Enabled: 1, CreatedTime: now},
			{ID: 110, KernelID: 0, NodeID: 0, Name: "r6h-empty-extra", Protocol: "vmess", Listen: "::", Port: 10110, Extra: "{}", Enabled: 1, CreatedTime: now},
			{ID: 112, KernelID: 201, NodeID: 0, Name: "r6h-selfbuilt", Protocol: "vmess", Listen: "::", Port: 10112, Enabled: 1, CreatedTime: now},
		}},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// 4 rows must skip: #107 (loopback row carrier), #108 (bad Extra
	// server), #109 (Extra-less row keeps the row gate), #110 (inert
	// shell, no content validated). #102/#105/#106/#112 must land.
	if got := result.SkippedRows["inbounds"]; got != 4 {
		t.Fatalf("R6-H-01: SkippedRows[inbounds] = %d, want 4 (107 loopback + 108 bad Extra + 109 no Extra + 110 inert shell)", got)
	}
	stored := func(id int64) (listen, extra string, ok bool) {
		var n int64
		if err := dest.DB().Raw(`SELECT COUNT(1) FROM node_inbound WHERE id = ?`, id).Scan(&n).Error; err != nil || n == 0 {
			return "", "", false
		}
		var row struct {
			Listen string
			Extra  string
		}
		if err := dest.DB().Raw(`SELECT listen, extra FROM node_inbound WHERE id = ?`, id).Scan(&row).Error; err != nil {
			t.Fatalf("read row %d: %v", id, err)
		}
		listen, extra = row.Listen, row.Extra
		return listen, extra, true
	}
	for _, id := range []int64{102, 105, 106, 112} {
		listen, extra, ok := stored(id)
		if !ok {
			t.Fatalf("R6-H-01: row #%d must land, got no row", id)
		}
		if id == 102 && listen != "203.0.113.10" {
			t.Fatalf("R6-H-01: row #102 listen = %q, want 203.0.113.10", listen)
		}
		if (id == 105 || id == 106 || id == 112) && listen != "::" {
			t.Fatalf("R6-H-01: row #%d listen = %q, want :: (normalized)", id, listen)
		}
		if id != 112 && extra != r6h01LegalVMessExtra {
			t.Fatalf("R6-H-01: row #%d Extra must be verbatim, got %q", id, extra)
		}
	}
	for _, id := range []int64{107, 108, 109, 110} {
		if _, _, ok := stored(id); ok {
			t.Fatalf("R6-H-01: row #%d must still be skipped, got a row", id)
		}
	}
}
