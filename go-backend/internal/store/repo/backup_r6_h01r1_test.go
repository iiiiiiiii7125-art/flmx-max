package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R6-H-01-R1 (P3): the R6-H-01 carve-out skips the whole
// ValidateImportedProxyRowAddress call when row Listen is "::" and the
// Extra check is guaranteed to run -- but that function bundles the dial
// host check AND the row Port 1-65535 gate, so the port gate was
// exempted along with the Listen gate. A "::" row with a legal Extra
// but an out-of-range row Port restored verbatim with that Port.
//
// Red test first: #213 ("::" + legal Extra + Port 0) and #214 ("::" +
// legal Extra + Port 70000) must be skipped after the fix. Controls
// that must NOT change: #212 (routable Listen + Port 0, already
// skipped), #105/#106 (H-01 landing rows), #107 (loopback row carrier,
// R8X-004 facet A), #108 ("::" + bad Extra, Extra validation),
// #109 ("::" + empty Extra, legacy row gate), #110 ("::" + "{}" Extra,
// inert shell), #112 (self-built "::" Listen, Normalize face).

func TestR6H01R1CarveOutKeepsPortGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r6h01r1.db")
	createRow(t, dest, &model.NodeKernel{ID: 201, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now, UpdatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{Inbounds: []model.InboundBackup{
			{ID: 105, KernelID: 0, NodeID: 0, Name: "r6h-empty", Protocol: "vmess", Listen: "", Port: 10105, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 106, KernelID: 0, NodeID: 0, Name: "r6h-unspec", Protocol: "vmess", Listen: "::", Port: 10106, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 107, KernelID: 0, NodeID: 0, Name: "r6h-loopback", Protocol: "vmess", Listen: "127.0.0.1", Port: 10107, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 108, KernelID: 0, NodeID: 0, Name: "r6h-bad-extra", Protocol: "vmess", Listen: "::", Port: 10108, Extra: r6h01BadServerExtra, Enabled: 1, CreatedTime: now},
			{ID: 109, KernelID: 0, NodeID: 0, Name: "r6h-no-extra", Protocol: "vmess", Listen: "::", Port: 10109, Extra: "", Enabled: 1, CreatedTime: now},
			{ID: 110, KernelID: 0, NodeID: 0, Name: "r6h-empty-extra", Protocol: "vmess", Listen: "::", Port: 10110, Extra: "{}", Enabled: 1, CreatedTime: now},
			{ID: 112, KernelID: 201, NodeID: 0, Name: "r6h-selfbuilt", Protocol: "vmess", Listen: "::", Port: 10112, Enabled: 1, CreatedTime: now},
			{ID: 212, KernelID: 0, NodeID: 0, Name: "r6h-r1-control-port0", Protocol: "vmess", Listen: "203.0.113.10", Port: 0, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 213, KernelID: 0, NodeID: 0, Name: "r6h-r1-unspec-port0", Protocol: "vmess", Listen: "::", Port: 0, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
			{ID: 214, KernelID: 0, NodeID: 0, Name: "r6h-r1-unspec-port70000", Protocol: "vmess", Listen: "::", Port: 70000, Extra: r6h01LegalVMessExtra, Enabled: 1, CreatedTime: now},
		}},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// 7 rows must skip: #212 (routable + Port 0), #213 ("::" + Port 0),
	// #214 ("::" + Port 70000), #107 (loopback), #108 (bad Extra),
	// #109 (no Extra), #110 (inert shell). #105/#106/#112 must land.
	if got := result.SkippedRows["inbounds"]; got != 7 {
		t.Fatalf("R6-H-01-R1: SkippedRows[inbounds] = %d, want 7 (212 + 213 + 214 + 107 + 108 + 109 + 110)", got)
	}
	stored := func(id int64) (port int64, listen, extra string, ok bool) {
		var n int64
		if err := dest.DB().Raw(`SELECT COUNT(1) FROM node_inbound WHERE id = ?`, id).Scan(&n).Error; err != nil || n == 0 {
			return 0, "", "", false
		}
		var row struct {
			Listen string
			Port   int64
			Extra  string
		}
		if err := dest.DB().Raw(`SELECT listen, port, extra FROM node_inbound WHERE id = ?`, id).Scan(&row).Error; err != nil {
			t.Fatalf("read row %d: %v", id, err)
		}
		return row.Port, row.Listen, row.Extra, true
	}
	for _, id := range []int64{105, 106, 112} {
		port, listen, extra, ok := stored(id)
		if !ok {
			t.Fatalf("R6-H-01-R1: row #%d must land, got no row", id)
		}
		if id == 105 && (listen != "::" || port != 10105) {
			t.Fatalf("R6-H-01-R1: row #105 = listen %q port %d, want :: / 10105", listen, port)
		}
		if id == 106 && (listen != "::" || port != 10106) {
			t.Fatalf("R6-H-01-R1: row #106 = listen %q port %d, want :: / 10106", listen, port)
		}
		if id == 112 && (listen != "::" || port != 10112) {
			t.Fatalf("R6-H-01-R1: row #112 = listen %q port %d, want :: / 10112", listen, port)
		}
		if id != 112 && extra != r6h01LegalVMessExtra {
			t.Fatalf("R6-H-01-R1: row #%d Extra must be verbatim, got %q", id, extra)
		}
	}
	for _, id := range []int64{212, 213, 214, 107, 108, 109, 110} {
		if _, _, _, ok := stored(id); ok {
			t.Fatalf("R6-H-01-R1: row #%d must be skipped, got a row", id)
		}
	}
}
