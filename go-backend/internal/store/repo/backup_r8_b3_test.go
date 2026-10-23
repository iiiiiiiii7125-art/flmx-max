package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// R8 批 3 回归：R8X-004（P3，备份恢复入站行内地址字段 Listen/Port
// 未设门）。两 facet 关闭断言分开登记、互不顶替（N-35）：
// facet A = 导入型入站（kernel_id≤0）的行内 Listen/Port 载体——
// Extra 载体已由 R7X-003 关闭，本 facet 的对象是行内第二载体；
// facet B = 自建入站（kernel_id>0）全空内容行经 hasContent 豁免时
// Port/Listen 同逃逸——豁免只豁免内容、不豁免地址/端口门。
// 全部用例先红后绿。

// facet A 关闭断言（行集与 R8-H-listen-probe.log 同型）：Extra 合法
// 的 kernel_id=0 行，行内坏 Listen（元数据/回环）/坏 Port（越界/0）
// 整行跳过；Extra 为空的旧式行行内地址同入门；Extra 坏行维持
// R7X-003 既有跳过；合法行逐字恢复。处置与凭据/地址列同回执
// （整行跳过 + SkippedRows["inbounds"] + 身份清单点名）。
func TestR8B3FacetAImportedRowListenPortGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r8b3-faceta.db")

	goodExtra := `{"type":"ss","name":"good","server":"203.0.113.10","port":10013,"cipher":"aes-256-gcm","password":"good-password","udp":true}`
	extraBad := `{"type":"ss","name":"extra-bad","server":"169.254.169.254","port":10013,"cipher":"aes-256-gcm","password":"good-password","udp":true}`

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Inbounds: []model.InboundBackup{
				{ID: 50, KernelID: 0, NodeID: 0, Name: "row-meta", Protocol: "shadowsocks", Listen: "169.254.169.254", Port: 10013, Extra: goodExtra, Enabled: 1, CreatedTime: now},
				{ID: 51, KernelID: 0, NodeID: 0, Name: "row-loopback", Protocol: "shadowsocks", Listen: "127.0.0.1", Port: 10013, Extra: goodExtra, Enabled: 1, CreatedTime: now},
				{ID: 52, KernelID: 0, NodeID: 0, Name: "row-badport", Protocol: "shadowsocks", Listen: "203.0.113.10", Port: 99999, Extra: goodExtra, Enabled: 1, CreatedTime: now},
				{ID: 53, KernelID: 0, NodeID: 0, Name: "row-zeroport", Protocol: "shadowsocks", Listen: "203.0.113.10", Port: 0, Extra: goodExtra, Enabled: 1, CreatedTime: now},
				{ID: 54, KernelID: 0, NodeID: 0, Name: "row-bothbad-noextra", Protocol: "shadowsocks", Listen: "169.254.169.254", Port: 99999, Enabled: 1, CreatedTime: now},
				{ID: 55, KernelID: 0, NodeID: 0, Name: "row-good", Protocol: "shadowsocks", Listen: "203.0.113.10", Port: 10013, Extra: goodExtra, Enabled: 1, CreatedTime: now},
				{ID: 56, KernelID: 0, NodeID: 0, Name: "extra-bad", Protocol: "shadowsocks", Listen: "203.0.113.10", Port: 10013, Extra: extraBad, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundsImported != 1 {
		t.Fatalf("R8X-004 facet A: InboundsImported = %d, want 1 (only the legal row)", result.InboundsImported)
	}
	if result.SkippedRows["inbounds"] != 6 {
		t.Fatalf("R8X-004 facet A: SkippedRows[inbounds] = %d, want 6 (five row-carrier bad rows + the R7X-003 extra-bad row)", result.SkippedRows["inbounds"])
	}
	if n := countWhere(t, dest, "node_inbound", "id IN (50,51,52,53,54,56)"); n != 0 {
		t.Fatalf("R8X-004 facet A: bad Listen/Port rows must not land, got %d", n)
	}
	// 合法行逐字恢复（行内 Listen/Port 与 Extra 均逐字）。
	var row model.NodeInbound
	if err := dest.DB().Where("id = ?", 55).First(&row).Error; err != nil {
		t.Fatalf("legal inbound 55 must land: %v", err)
	}
	if row.Listen != "203.0.113.10" || row.Port != 10013 || row.Extra != goodExtra {
		t.Fatalf("R8X-004 facet A 防过修: inbound 55 must restore verbatim, got Listen=%q Port=%d Extra=%q", row.Listen, row.Port, row.Extra)
	}
	// 回执点名：六坏行身份 + 地址错误摘要在位，合法行不被点名。
	joined := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"#50 row-meta", "#51 row-loopback", "#52 row-badport", "#53 row-zeroport", "#54 row-bothbad-noextra", "#56 extra-bad"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("R8X-004 facet A: skip warning must carry identity %q, got %v", want, result.ImportWarnings)
		}
	}
	if !strings.Contains(joined, "元数据") || !strings.Contains(joined, "本机") || !strings.Contains(joined, "端口不正确") {
		t.Fatalf("R8X-004 facet A: skip warning must carry per-row address error summaries (metadata/loopback/port), got %v", result.ImportWarnings)
	}
	if strings.Contains(joined, "#55") || strings.Contains(joined, "row-good") {
		t.Fatalf("R8X-004 facet A 防过修: warnings must not name the legal row, got %v", result.ImportWarnings)
	}
}

// facet B 关闭断言（行集与 R8-H-listen2-probe.log 同型并扩 Listen
// 格）：kernel_id>0、四整对象全空的行经 hasContent 豁免路径时，
// Port 越界 / Listen 非 IP 字面量整行跳过 + 回执点名；合法 Port 行
// 与空 Listen（归一 "::"）行逐字恢复（防过修：存量自建行夹具与
// 真实导出行恒以空/"::" Listen 出现，不得误杀）。
func TestR8B3FacetBSelfBuiltEmptyContentRowAddressGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r8b3-facetb.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 60, KernelID: 1, NodeID: 1, Name: "self-badport", Protocol: "vless", Listen: "::", Port: 99999, Enabled: 1, CreatedTime: now},
				{ID: 61, KernelID: 1, NodeID: 1, Name: "self-good", Protocol: "vless", Listen: "::", Port: 10443, Enabled: 1, CreatedTime: now},
				{ID: 62, KernelID: 1, NodeID: 1, Name: "self-badlisten", Protocol: "vless", Listen: "not-an-ip", Port: 10444, Enabled: 1, CreatedTime: now},
				{ID: 64, KernelID: 1, NodeID: 1, Name: "self-emptylisten", Protocol: "vless", Listen: "", Port: 10445, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundsImported != 2 {
		t.Fatalf("R8X-004 facet B: InboundsImported = %d, want 2 (the two legal rows)", result.InboundsImported)
	}
	if result.SkippedRows["inbounds"] != 2 {
		t.Fatalf("R8X-004 facet B: SkippedRows[inbounds] = %d, want 2 (bad Port + bad Listen empty-content rows)", result.SkippedRows["inbounds"])
	}
	if n := countWhere(t, dest, "node_inbound", "id IN (60,62)"); n != 0 {
		t.Fatalf("R8X-004 facet B: bad Port/Listen empty-content rows must not land, got %d", n)
	}
	for id, wantPort := range map[int64]int{61: 10443, 64: 10445} {
		var row model.NodeInbound
		if err := dest.DB().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("legal inbound %d must land: %v", id, err)
		}
		if row.Listen != "::" || row.Port != wantPort {
			t.Fatalf("R8X-004 facet B 防过修: inbound %d must restore with Listen \"::\" and Port %d, got Listen=%q Port=%d", id, wantPort, row.Listen, row.Port)
		}
	}
	joined := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"#60 self-badport", "#62 self-badlisten"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("R8X-004 facet B: skip warning must carry identity %q, got %v", want, result.ImportWarnings)
		}
	}
	if !strings.Contains(joined, "端口") || !strings.Contains(joined, "监听地址必须是 IP") {
		t.Fatalf("R8X-004 facet B: skip warning must carry per-row error summaries (port/listen), got %v", result.ImportWarnings)
	}
	if strings.Contains(joined, "#61") || strings.Contains(joined, "self-good") ||
		strings.Contains(joined, "#64") || strings.Contains(joined, "self-emptylisten") {
		t.Fatalf("R8X-004 facet B 防过修: warnings must not name the legal rows, got %v", result.ImportWarnings)
	}
}

// facet A 共用实现断言：行载体门 kernel.ValidateImportedProxyRowAddress
// 与 Extra 载体谓词 kernel.ValidateImportedProxy（凭据+地址双列、
// R7X-003 关闭对象）对同一地址必须逐格同判、错误逐字相同——行载体
// 经 validateImportedProxyAddress 同一实现校验的直接证据（若行载体
// 另写第二份谓词，两面判读/文案必然在某一格漂移）。地址格覆盖坏
// Listen 三类（回环/元数据/非规范）+ 坏 Port（越界/0）+ 合法对照。
func TestR8B3FacetASharedRowAddressImplementation(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		port int
	}{
		{"loopback", "127.0.0.1", 10013},
		{"metadata", "169.254.169.254", 10013},
		{"noncanonical", "127.1", 10013},
		{"localhost", "localhost", 10013},
		{"badport", "203.0.113.10", 99999},
		{"zeroport", "203.0.113.10", 0},
	} {
		rowErr := kernel.ValidateImportedProxyRowAddress(tc.host, tc.port)
		if rowErr == nil {
			t.Fatalf("R8X-004 facet A: row address gate must reject %s (%s:%d)", tc.name, tc.host, tc.port)
		}
		extraErr := kernel.ValidateImportedProxy(kernel.Proxy{"server": tc.host, "port": tc.port})
		if extraErr == nil {
			t.Fatalf("R8X-004 facet A: ValidateImportedProxy must reject %s (%s:%d)", tc.name, tc.host, tc.port)
		}
		if rowErr.Error() != extraErr.Error() {
			t.Fatalf("R8X-004 facet A 共用实现: row/extra verdicts must carry identical error text for %s, got row=%q extra=%q", tc.name, rowErr, extraErr)
		}
	}
	if err := kernel.ValidateImportedProxyRowAddress("203.0.113.10", 10013); err != nil {
		t.Fatalf("R8X-004 facet A 防过修: row address gate must accept the legal row address, got %v", err)
	}
	if err := kernel.ValidateImportedProxy(kernel.Proxy{"server": "203.0.113.10", "port": 10013}); err != nil {
		t.Fatalf("R8X-004 facet A 防过修: ValidateImportedProxy must accept the legal address, got %v", err)
	}
}
