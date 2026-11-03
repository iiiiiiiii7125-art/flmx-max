package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 修复轮 9 路 forwardquota：R9-09（I-F3-01）恢复面＋I-F3-03 落字。
//
// 恢复面镜像谓词 remoteAddrHasNoTargets 与写面同改「归一后有效段
// 集」口径：归一（剥 scheme、首个 /?# 截断）后无实质内容的段不
// 计，纯 Unicode 格式符（Cf）段同判无效（I-F3-03 裁定，与写面
// 及 R8-01 先例同口径）。本文件的谓词表与 handler 侧
// TestFix9R909HasTargetsUsesNormalizedEffectiveSet 逐行对齐（镜像
// 同定义、双边钉格）。
//
// 基线（acb03403）红点："#"/"http://" 行 restored=true、零 warning；
// remoteAddrHasNoTargets("#") = false。

func TestFix9R909RemoteAddrHasNoTargetsNormalized(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// R8-07 原全集不回归。
		{"", true},
		{",,,", true},
		{" , ,", true},
		{" ", true},
		{"\n,\r\n", true},
		{"\u3000", true},
		// R9-09：段集非空、归一后零有效目标。
		{"#", true},
		{"/", true},
		{"?", true},
		{"#frag", true},
		{"http://", true},
		{"https://", true},
		{"a://", true},
		{"http://,https://", true},
		{"#,?", true},
		{"#\nhttp://\r#x", true},
		// I-F3-03：纯 Cf 段不计入有效段。
		{"\u200b", true},
		{"\u200b\u2060", true},
		{"\ufeff", true},
		{"\u00ad", true},
		{" \u200b ", true},
		{"#,\u200b", true},
		// 有效形态不误伤。
		{"203.0.113.5:443", false},
		{",203.0.113.5:443", false},
		{"203.0.113.5:443\n198.51.100.7:443", false},
		{"1.2.3.4:80,#", false},
		{"1.2.3.4:80,\u200b", false},
		{"http://:8080", false},
		{"://", false},
		{"example.com:8080/p", false},
		{"\u200b203.0.113.5:443", false},
		{"a..b:443", false},
	}
	for _, c := range cases {
		if got := remoteAddrHasNoTargets(c.in); got != c.want {
			t.Errorf("remoteAddrHasNoTargets(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFix9R909ImportForwardsSkipsNormalizedZeroTargetRows(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "fix9-r909.db")
	createRow(t, dest, &model.User{ID: 101, User: "alice", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 5, Name: "t5", Type: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.UserTunnel{UserID: 101, TunnelID: 5, Num: 99999, Flow: 99999, FlowResetTime: 1, ExpTime: 2727251700000, Status: 1})

	mk := func(id int64, name, remoteAddr string) model.ForwardBackup {
		return model.ForwardBackup{
			ID: id, UserID: 101, UserName: "alice", Name: name, TunnelID: 5,
			RemoteAddr: remoteAddr, Strategy: "fifo", Status: 1, CreatedTime: now,
		}
	}
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			mk(71, "bad-hash", "#"),                         // 跳过＋warning（本修复）
			mk(72, "bad-scheme", "http://"),                 // 跳过＋warning（本修复）
			mk(73, "bad-frag", "#frag"),                     // 跳过＋warning（本修复）
			mk(74, "bad-cf", "\u200b"),                      // 跳过＋warning（I-F3-03）
			mk(75, "ok-mixed", "1.2.3.4:80,#"),              // 逐字恢复（死段消费侧丢弃）
			mk(76, "ok-normal", "203.0.113.5:443"),          // 逐字恢复
			mk(77, "ok-zwsp-real", "\u200b203.0.113.5:443"), // 逐字恢复（含 Cf 前缀但有真目标）
		},
	}
	result, err := dest.Import(backup, []string{"forwards"})
	if err != nil {
		t.Fatalf("import must not fail wholesale: %v", err)
	}
	if result.ForwardsImported != 3 {
		t.Fatalf("ForwardsImported = %d, want 3 (mixed/normal/zwsp-real)", result.ForwardsImported)
	}
	if got := result.SkippedRows["forwards"]; got != 4 {
		t.Fatalf("SkippedRows[forwards] = %d, want 4 (hash/scheme/frag/cf); result=%+v", got, result)
	}
	zeroWarn := 0
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "无任何有效目标") {
			zeroWarn++
		}
	}
	if zeroWarn != 4 {
		t.Fatalf("zero-target rows must each produce a naming warning, got %d in %v", zeroWarn, result.ImportWarnings)
	}
	for _, id := range []int64{71, 72, 73, 74} {
		if n := countWhere(t, dest, "forward", "id = ?", id); n != 0 {
			t.Fatalf("skipped forward #%d must not be written, found %d rows", id, n)
		}
	}
	wantStored := map[int64]string{
		75: "1.2.3.4:80,#",
		76: "203.0.113.5:443",
		77: "\u200b203.0.113.5:443",
	}
	for id, want := range wantStored {
		var row model.Forward
		if err := dest.DB().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("forward #%d must be restored: %v", id, err)
		}
		if row.RemoteAddr != want {
			t.Fatalf("forward #%d RemoteAddr = %q, want verbatim %q", id, row.RemoteAddr, want)
		}
	}
}
