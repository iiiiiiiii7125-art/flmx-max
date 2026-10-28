package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R4-F-01（P3）：恢复回执 R5X-017 汇总 warning 误归因——InIP 非法跳过
// 的端口行被说成「因引用节点不存在被跳过」。r3 修复 R3-H-01 引入：
// 两类跳过（引用节点不存在 / InIP 格式非法）共用
// rep.skipped["forwardPorts"] 计数器，批尾汇总 warning 把该计数器
// 的全部增量都表述为“因引用节点不存在被跳过”，而 InIP 非法行的
// 节点检查已通过、节点必然存在。逐行 warning 正确，仅汇总归因错；
// 无数据丢失、无功能影响。
//
// 本测试先红后绿：修复前汇总 warning 为旧文案（“其中 N 条备份端口行
// 因引用节点不存在被跳过未恢复”），不区分两类原因；修复后两类跳过
// 分别计数、汇总文案各写各的条数与原因。逐行 warning 与跳过/入库
// 语义全程不变（SkippedRows["forwardPorts"] 仍为两类合计数）。

// r4F01SummaryWarning 从回执里取出 R5X-017 汇总 warning（“重建时
// 目标库原有 … 条端口行已被移除”那条），无则返回 ""。
func r4F01SummaryWarning(t *testing.T, warnings []string) string {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, "重建时目标库原有") {
			return w
		}
	}
	return ""
}

func TestR4F01RestoreSummaryWarningAttributesSkipsByCause(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r4f01-summary.db")
	createRow(t, dest, &model.User{ID: 7, User: "u1", Pwd: r5b5Bcrypt, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", Type: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	// 目标库预存转发 #80 及其端口 8079 行（preDeletedPorts=1，触发 R5X-017）。
	createRow(t, dest, &model.Forward{ID: 80, UserID: 7, UserName: "u1", Name: "r4f-fwd", TunnelID: 1, RemoteAddr: "1.1.1.1:443", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.ForwardPort{ForwardID: 80, NodeID: 1, Port: 8079})

	ports := []model.ForwardPortBackup{
		{NodeID: 1, Port: 8080, InIP: "10.0.0.1"},   // 合法 → 落库
		{NodeID: 1, Port: 8081, InIP: "a..b"},       // InIP 非法 → 丢弃（节点存在）
		{NodeID: 999, Port: 8082, InIP: "10.0.0.1"}, // 引用节点不存在 → 丢弃
	}
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 80, UserID: 7, UserName: "u1", Name: "r4f-fwd", TunnelID: 1, RemoteAddr: "1.1.1.1:443", Status: 1, CreatedTime: now, ForwardPorts: &ports},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 1, UserID: 7, TunnelID: 1, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"forwards", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ForwardsImported != 1 {
		t.Fatalf("R4-F-01: ForwardsImported = %d, want 1", result.ForwardsImported)
	}
	// 跳过/入库语义不变：两类跳过合计 2，合法行落库，预存行已被移除。
	if got := result.SkippedRows["forwardPorts"]; got != 2 {
		t.Fatalf("R4-F-01: SkippedRows[forwardPorts] = %d, want 2 (1 bad InIP + 1 missing node)", got)
	}
	stored := func(port int) (sql.NullString, bool) {
		var n int64
		if err := dest.DB().Raw(`SELECT COUNT(1) FROM forward_port WHERE forward_id = ? AND port = ?`, 80, port).Scan(&n).Error; err != nil || n == 0 {
			return sql.NullString{}, false
		}
		var inIP sql.NullString
		if err := dest.DB().Raw(`SELECT in_ip FROM forward_port WHERE forward_id = ? AND port = ?`, 80, port).Scan(&inIP).Error; err != nil {
			return sql.NullString{}, false
		}
		return inIP, true
	}
	if got, ok := stored(8080); !ok || !got.Valid || got.String != "10.0.0.1" {
		t.Fatalf("R4-F-01: port 8080 legal row must land with InIP 10.0.0.1, got valid=%v %q (row present=%v)", got.Valid, got.String, ok)
	}
	for _, port := range []int{8079, 8081, 8082} {
		if _, ok := stored(port); ok {
			t.Fatalf("R4-F-01: port %d must not land (pre-deleted / dropped), got a row", port)
		}
	}

	// 逐行 warning 保持正确：InIP 非法行点名端口与原因。
	joined := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(joined, "端口行（节点 #1，端口 8081）的监听 IP 格式非法") {
		t.Fatalf("R4-F-01: per-row warning must name the bad-InIP port row, got %v", result.ImportWarnings)
	}
	// 汇总 warning 必须区分两类原因与各自条数，不得把 InIP 非法跳过
	// 归因为引用节点不存在。
	summary := r4F01SummaryWarning(t, result.ImportWarnings)
	if summary == "" {
		t.Fatalf("R4-F-01: R5X-017 summary warning must be emitted (preDeletedPorts=1, skipped=2), got %v", result.ImportWarnings)
	}
	if !strings.Contains(summary, "引用节点不存在 1 条") {
		t.Fatalf("R4-F-01: summary must attribute 1 skip to missing node, got %q", summary)
	}
	if !strings.Contains(summary, "监听 IP 格式非法 1 条") {
		t.Fatalf("R4-F-01: summary must attribute 1 skip to illegal InIP, got %q", summary)
	}
	if strings.Contains(summary, "因引用节点不存在被跳过") {
		t.Fatalf("R4-F-01: summary must not misattribute InIP skips to missing nodes, got %q", summary)
	}
}

// 对照探针（F 域 R4-F-01）：无预存端口行（preDeletedPorts=0）时同样
// InIP 非法跳过，R5X-017 不触发——锁定触发条件为 preDeletedPorts>0。
func TestR4F01RestoreSummaryWarningAbsentWithoutPredeletedPorts(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r4f01-no-predeleted.db")
	createRow(t, dest, &model.User{ID: 7, User: "u1", Pwd: r5b5Bcrypt, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", Type: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	ports := []model.ForwardPortBackup{
		{NodeID: 1, Port: 8080, InIP: "10.0.0.1"},
		{NodeID: 1, Port: 8081, InIP: "a..b"},
	}
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 80, UserID: 7, UserName: "u1", Name: "r4f-fwd", TunnelID: 1, RemoteAddr: "1.1.1.1:443", Status: 1, CreatedTime: now, ForwardPorts: &ports},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 1, UserID: 7, TunnelID: 1, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"forwards", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["forwardPorts"]; got != 1 {
		t.Fatalf("R4-F-01: SkippedRows[forwardPorts] = %d, want 1 (bad InIP row only)", got)
	}
	if summary := r4F01SummaryWarning(t, result.ImportWarnings); summary != "" {
		t.Fatalf("R4-F-01: R5X-017 summary must not fire with preDeletedPorts=0, got %q", summary)
	}
	joined := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(joined, "端口行（节点 #1，端口 8081）的监听 IP 格式非法") {
		t.Fatalf("R4-F-01: per-row warning must still name the bad-InIP port row, got %v", result.ImportWarnings)
	}
}

// 单类对照：仅引用节点不存在一类跳过时，汇总只写该类条数，不带
// 另一类原因。
func TestR4F01RestoreSummaryWarningMissingNodeOnly(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r4f01-missing-node.db")
	createRow(t, dest, &model.User{ID: 7, User: "u1", Pwd: r5b5Bcrypt, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", Type: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.Forward{ID: 80, UserID: 7, UserName: "u1", Name: "r4f-fwd", TunnelID: 1, RemoteAddr: "1.1.1.1:443", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.ForwardPort{ForwardID: 80, NodeID: 1, Port: 8079})

	ports := []model.ForwardPortBackup{
		{NodeID: 1, Port: 8080, InIP: "10.0.0.1"},
		{NodeID: 999, Port: 8082, InIP: "10.0.0.1"},
	}
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 80, UserID: 7, UserName: "u1", Name: "r4f-fwd", TunnelID: 1, RemoteAddr: "1.1.1.1:443", Status: 1, CreatedTime: now, ForwardPorts: &ports},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 1, UserID: 7, TunnelID: 1, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"forwards", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := result.SkippedRows["forwardPorts"]; got != 1 {
		t.Fatalf("R4-F-01: SkippedRows[forwardPorts] = %d, want 1 (missing node row only)", got)
	}
	summary := r4F01SummaryWarning(t, result.ImportWarnings)
	if summary == "" {
		t.Fatalf("R4-F-01: R5X-017 summary must be emitted (preDeletedPorts=1, skipped=1), got %v", result.ImportWarnings)
	}
	if !strings.Contains(summary, "引用节点不存在 1 条") {
		t.Fatalf("R4-F-01: summary must attribute 1 skip to missing node, got %q", summary)
	}
	if strings.Contains(summary, "监听 IP 格式非法") {
		t.Fatalf("R4-F-01: summary must not mention illegal InIP when no such skip occurred, got %q", summary)
	}
}
