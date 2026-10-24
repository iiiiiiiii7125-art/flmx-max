package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R13 批 T2-c（R13X-007，P3）：importTunnels 恢复面须复用保存面两门
// （Flow ∈ {1,2}、TrafficRatio ∈ [0,100]），域外行跳过 + 点名（与
// forwards status 门同形）。先红后绿：修复前域外行逐字落库。
// 注：validateTunnelFlow / validateTunnelTrafficRatio 位于 handler 包，
// handler 依赖 repo，故 repo 侧以同合法集的本地门镜像（见
// importTunnels 注释），不构成字面函数复用——合法集与点名口径一致。

func TestR13T2cImportTunnelsSkipsOutOfDomainFlowAndRatio(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r13t2c-tunnels.db")
	// 存量行：与被跳过行同 id——跳过不得删其链路/改其值（预删须只
	// 覆盖通过门的行）。
	createRow(t, dest, &model.Tunnel{ID: 21, Name: "t2c-pre", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 11, Name: "t2c-ok", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now},
			{ID: 12, Name: "t2c-flow-huge", Flow: 99999, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now},
			{ID: 13, Name: "t2c-ratio-neg", Flow: 1, TrafficRatio: -5, Type: 1, Status: 1, CreatedTime: now},
			{ID: 14, Name: "t2c-ratio-over", Flow: 1, TrafficRatio: 200, Type: 1, Status: 1, CreatedTime: now},
			{ID: 15, Name: "t2c-flow-zero", Flow: 0, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now},
			{ID: 21, Name: "t2c-pre-clobber", Flow: 99999, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.TunnelsImported != 1 {
		t.Fatalf("R13X-007: TunnelsImported = %d, want 1 (only the legal row)", result.TunnelsImported)
	}
	if result.SkippedRows["tunnels"] != 5 {
		t.Fatalf("R13X-007: SkippedRows[tunnels] = %d, want 5, got %v", result.SkippedRows["tunnels"], result.SkippedRows)
	}
	// 回执须逐行点名（id 或名二者至少其一出现，且含域外值线索）。
	for _, want := range []string{"t2c-flow-huge", "t2c-ratio-neg", "t2c-ratio-over", "t2c-flow-zero", "t2c-pre-clobber"} {
		found := false
		for _, w := range result.ImportWarnings {
			if strings.Contains(w, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("R13X-007: skipped row %q must be named in warnings, got %v", want, result.ImportWarnings)
		}
	}
	// 合法行逐字落库；域外行不得落库；存量同 id 行原样保留。
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM tunnel WHERE id = 11 AND flow = 1 AND traffic_ratio = 1`); n != 1 {
		t.Fatalf("R13X-007: legal tunnel row must land verbatim, got %d", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM tunnel WHERE id IN (12,13,14,15)`); n != 0 {
		t.Fatalf("R13X-007: out-of-domain tunnel rows must not land, got %d", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM tunnel WHERE id = 21 AND name = 't2c-pre' AND flow = 1`); n != 1 {
		t.Fatalf("R13X-007: skipped row must not clobber the pre-existing row id=21")
	}
}
