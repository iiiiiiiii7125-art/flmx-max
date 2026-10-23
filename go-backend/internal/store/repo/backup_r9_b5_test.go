package repo

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R9 批 5 回归：R9X-008（P3，备份恢复入站 WhitelistExtra 逐字恢复、
// 无保存面同口径校验与披露——地址载体「附加白名单清单」形态第三格
// 漏网，R7X-003 列维度 / R8X-004 行内载体之后）。
//
// 关闭断言（与 R9-C-01 探针行集同型）：
//   - 垃圾 WhitelistExtra 恢复时经 kernel.ParseAllowList 同口径校验
//     拦截：非法条目逐项剔除（整表条目全坏时整表清空），合法条目
//     逐字保留，入站行本身照常恢复（字段级 sanitize，与 ShareHost /
//     splitRules 逐项剔除先例同口径，不整行跳过）；
//   - 计数入回执：受影响行数 + 被剔除非法条目数写入 ImportWarnings
//     汇总披露，逐行点名行身份与坏条目原文（行照常落库，故不计
//     SkippedRows["inbounds"]，同 R5X-014 sanitize 回执口径）；
//   - 合法清单逐字恢复不变（防过修）：已规范串、含裸 IP 的合法串、
//     宽网段 0.0.0.0/0 均逐字落库且不产生 warning；空值不警告。
//
// 全部用例先红后绿。
func TestR9B5ImportWhitelistExtraGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r9b5-whitelist.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 70, KernelID: 1, NodeID: 1, Name: "wl-all-bad", Protocol: "vless", Listen: "::", Port: 10443,
					RelayWhitelist: 1, WhitelistExtra: "not-an-ip, also bad!!", Enabled: 1, CreatedTime: now},
				{ID: 71, KernelID: 1, NodeID: 1, Name: "wl-wide", Protocol: "vless", Listen: "::", Port: 10444,
					RelayWhitelist: 1, WhitelistExtra: "0.0.0.0/0", Enabled: 1, CreatedTime: now},
				{ID: 72, KernelID: 1, NodeID: 1, Name: "wl-legal", Protocol: "vless", Listen: "::", Port: 10445,
					RelayWhitelist: 1, WhitelistExtra: "10.0.0.0/8, 1.2.3.4", Enabled: 1, CreatedTime: now},
				{ID: 73, KernelID: 1, NodeID: 1, Name: "wl-mixed", Protocol: "vless", Listen: "::", Port: 10446,
					RelayWhitelist: 1, WhitelistExtra: "10.0.0.0/8, not-an-ip, 192.168.0.0/16", Enabled: 1, CreatedTime: now},
				{ID: 74, KernelID: 1, NodeID: 1, Name: "wl-empty", Protocol: "vless", Listen: "::", Port: 10447,
					RelayWhitelist: 1, WhitelistExtra: "", Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// 字段级 sanitize 不整行跳过：五行全部落库、整行跳过计数为 0。
	if result.InboundsImported != 5 {
		t.Fatalf("R9X-008: InboundsImported = %d, want 5 (WhitelistExtra sanitize must not skip whole rows)", result.InboundsImported)
	}
	if len(result.SkippedRows) != 0 {
		t.Fatalf("R9X-008: SkippedRows = %v, want empty (sanitized rows land; counting them as skipped rows would falsify the receipt)", result.SkippedRows)
	}
	got := func(id int64) model.NodeInbound {
		t.Helper()
		var row model.NodeInbound
		if err := dest.DB().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("inbound %d must land: %v", id, err)
		}
		return row
	}
	// 全坏清单：整表清空（任何坏条目不得逐字落库）。
	if row := got(70); row.WhitelistExtra != "" {
		t.Fatalf("R9X-008: all-bad WhitelistExtra must be cleared on restore, got %q", row.WhitelistExtra)
	}
	// 混合清单：坏条目逐项剔除，合法条目逐字保留（原文 token 不归一化改写）。
	if row := got(73); row.WhitelistExtra != "10.0.0.0/8, 192.168.0.0/16" {
		t.Fatalf("R9X-008: mixed WhitelistExtra must keep legal entries verbatim with the bad entry pruned, got %q", row.WhitelistExtra)
	}
	// 合法清单逐字恢复（防过修）：宽网段与含裸 IP 的合法串均不得被
	// 规范化改写（如 1.2.3.4 不得变成 1.2.3.4/32 落库——恢复面校验
	// 只读、落库逐字，与 validateImportedInboundObjects 同口径）。
	if row := got(71); row.WhitelistExtra != "0.0.0.0/0" {
		t.Fatalf("R9X-008 防过修: wide WhitelistExtra must restore verbatim, got %q", row.WhitelistExtra)
	}
	if row := got(72); row.WhitelistExtra != "10.0.0.0/8, 1.2.3.4" {
		t.Fatalf("R9X-008 防过修: legal WhitelistExtra must restore verbatim (no normalization rewrite), got %q", row.WhitelistExtra)
	}
	if row := got(74); row.WhitelistExtra != "" {
		t.Fatalf("R9X-008 防过修: empty WhitelistExtra must stay empty, got %q", row.WhitelistExtra)
	}
	// 不触碰其他字段的逐字恢复语义：抽查合法行行内字段逐字。
	if row := got(72); row.Listen != "::" || row.Port != 10445 || row.RelayWhitelist != 1 {
		t.Fatalf("R9X-008 防过修: inbound 72 other fields must restore verbatim, got Listen=%q Port=%d RelayWhitelist=%d", row.Listen, row.Port, row.RelayWhitelist)
	}
	// 回执披露：汇总 warning 点名两坏行身份 + 坏条目原文 + 计数
	// （2 条入站 / 共 4 个非法条目：#70 三个 + #73 一个），合法行不被点名。
	joined := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"WhitelistExtra", "#70 wl-all-bad", "#73 wl-mixed", `"not-an-ip"`, `"also"`, `"bad!!"`, "2 条入站", "4 个非法条目"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("R9X-008: import warnings must disclose %q, got %v", want, result.ImportWarnings)
		}
	}
	for _, unwanted := range []string{"#71", "wl-wide", "#72", "wl-legal", "#74", "wl-empty"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("R9X-008 防过修: warnings must not name legal row %q, got %v", unwanted, result.ImportWarnings)
		}
	}
}

// sanitizeBackupWhitelistExtra 逐格单测：判定与保存面 ParseAllowList
// 逐格同判（共用实现证据）——合法（含非规范网段写法、宽网段、空串）
// 逐字返回且无 bad/err；混合逐项剔除且合法 token 原文保留；全坏整表
// 清空；无单条可归责的整表非法（条目数超限）与剔除后仍超限两格整表
// 清空并回 listErr 供回执披露。
func TestR9B5SanitizeBackupWhitelistExtra(t *testing.T) {
	for _, raw := range []string{"", "0.0.0.0/0", "10.0.0.0/8, 1.2.3.4", "10.1.2.3/8", "2001:db8::1, ::1/128"} {
		restored, bad, err := sanitizeBackupWhitelistExtra(raw)
		if restored != raw || len(bad) != 0 || err != nil {
			t.Fatalf("legal %q must restore verbatim with no bad/err, got restored=%q bad=%v err=%v", raw, restored, bad, err)
		}
	}
	restored, bad, err := sanitizeBackupWhitelistExtra("10.0.0.0/8, not-an-ip, 192.168.0.0/16")
	if restored != "10.0.0.0/8, 192.168.0.0/16" || len(bad) != 1 || bad[0] != "not-an-ip" || err != nil {
		t.Fatalf("mixed list must prune only the bad entry, got restored=%q bad=%v err=%v", restored, bad, err)
	}
	restored, bad, err = sanitizeBackupWhitelistExtra("not-an-ip, also bad!!")
	if restored != "" || len(bad) != 3 || err != nil {
		t.Fatalf("all-bad list must clear with all three entries reported, got restored=%q bad=%v err=%v", restored, bad, err)
	}
	// 条目数超限（257 个合法条目重复）：无单条可归责，整表清空 + listErr。
	over := strings.TrimSuffix(strings.Repeat("10.0.0.1, ", 257), ", ")
	restored, bad, err = sanitizeBackupWhitelistExtra(over)
	if restored != "" || len(bad) != 0 || err == nil {
		t.Fatalf("over-cap list must clear with list error, got restored=%q bad=%v err=%v", restored, bad, err)
	}
	// 剔除坏条目后仍超限（260 合法 + 2 坏）：整表清空 + bad 与 listErr 并存。
	var toks []string
	for i := 0; i < 260; i++ {
		toks = append(toks, fmt.Sprintf("10.%d.%d.1", i/250, i%250))
	}
	toks = append(toks, "bad-one", "bad-two")
	restored, bad, err = sanitizeBackupWhitelistExtra(strings.Join(toks, ", "))
	if restored != "" || len(bad) != 2 || err == nil {
		t.Fatalf("still-over-cap survivors must clear with bad entries and list error, got restored=%q bad=%v err=%v", restored, bad, err)
	}
}
