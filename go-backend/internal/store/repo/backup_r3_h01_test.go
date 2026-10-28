package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3-H-01（P2）：恢复侧 ForwardPort.InIP 未复用保存侧 intake 剥离。
// 保存侧 forwardCreate/Update 对 inIp 一律 stripAndValidateNodeAddress
// （单端口剥离 + 格式校验，双端口/空标签主机名直接拒绝）；恢复侧
// importForwards 的 ForwardPorts 行原样落库，致含 :port 的值下发后
// 监听端口与界面显示静默脱节（UI 8080 / 节点实听 9999）。
//
// 本测试先红后绿：修复前 8080 行原样存 "10.0.0.1:9999"、8081/8083
// 非法行落库、SkippedRows["forwardPorts"]==0；修复后单端口剥离入库、
// 非法行丢弃 + warning 点名（warning 不含值原文）、空值存 NULL。

func TestR3H01ForwardPortInIPStrippedAtRestore(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r3h01-fwdinip.db")
	createRow(t, dest, &model.User{ID: 7, User: "u1", Pwd: r5b5Bcrypt, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", Type: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	ports := []model.ForwardPortBackup{
		{NodeID: 1, Port: 8080, InIP: "10.0.0.1:9999"},      // 单端口嵌入 → 剥离为 10.0.0.1
		{NodeID: 1, Port: 8081, InIP: "a..b"},               // 空标签主机名 → 丢弃该端口行
		{NodeID: 1, Port: 8082, InIP: "10.0.0.1"},           // 合法裸值 → 逐字落库
		{NodeID: 1, Port: 8083, InIP: "10.0.0.1:9999:8888"}, // 双端口 → 丢弃该端口行
		{NodeID: 1, Port: 8084, InIP: ""},                   // 空值 → NULL
		{NodeID: 1, Port: 8086, InIP: "[2001:db8::1]:9999"}, // 括号 IPv6 含端口 → 剥离为 2001:db8::1
	}
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 80, UserID: 7, UserName: "u1", Name: "r3h01-fwd", TunnelID: 1, RemoteAddr: "1.1.1.1:443", Status: 1, CreatedTime: now, ForwardPorts: &ports},
		},
		// 转发落库须有对应 userTunnel 授权（R4X-006 事务尾收口）。
		UserTunnels: []model.UserTunnelBackup{
			{ID: 1, UserID: 7, TunnelID: 1, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"forwards", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ForwardsImported != 1 {
		t.Fatalf("R3-H-01: ForwardsImported = %d, want 1", result.ForwardsImported)
	}
	if got := result.SkippedRows["forwardPorts"]; got != 2 {
		t.Fatalf("R3-H-01: SkippedRows[forwardPorts] = %d, want 2 (a..b + double-port rows dropped)", got)
	}

	// 剥离/逐字/NULL 三格落库值（Raw+Scan 在零行时不报错，须先 COUNT 判存在）。
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
	for port, want := range map[int]string{8080: "10.0.0.1", 8082: "10.0.0.1", 8086: "2001:db8::1"} {
		got, ok := stored(port)
		if !ok {
			t.Fatalf("R3-H-01: port %d row must land, got no row", port)
		}
		if !got.Valid || got.String != want {
			t.Fatalf("R3-H-01: port %d InIP = %q (valid=%v), want %q", port, got.String, got.Valid, want)
		}
	}
	// 空值存 NULL。
	if got, ok := stored(8084); !ok || got.Valid {
		t.Fatalf("R3-H-01: port 8084 empty InIP must land as NULL, got valid=%v %q (row present=%v)", got.Valid, got.String, ok)
	}
	// 非法行不得以任何形态落库。
	for _, port := range []int{8081, 8083} {
		if _, ok := stored(port); ok {
			t.Fatalf("R3-H-01: port %d with illegal InIP must be dropped, got a row", port)
		}
	}

	// warning 点名被丢弃的端口行（转发 #80 + 端口号），且不含值原文。
	joined := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(joined, "转发 #80") {
		t.Fatalf("R3-H-01: drop warning must name the forward, got %v", result.ImportWarnings)
	}
	for _, port := range []string{"8081", "8083"} {
		if !strings.Contains(joined, port) {
			t.Fatalf("R3-H-01: drop warning must name dropped port %s, got %v", port, result.ImportWarnings)
		}
	}
	for _, raw := range []string{"a..b", "9999:8888"} {
		if strings.Contains(joined, raw) {
			t.Fatalf("R3-H-01: warning must not contain the raw illegal value %q, got %v", raw, result.ImportWarnings)
		}
	}
}
