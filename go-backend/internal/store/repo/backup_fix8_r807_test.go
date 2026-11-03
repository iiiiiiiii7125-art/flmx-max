package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 修复轮 8 路 forwardfix：R8-07（H-F3-02，低）。
//
// R7-04 空段集谓词（零有效目标属格式无效）的写面全集差恢复面：
// importForwards 此前只有 status 枚举判与空标签判，",,,"/空串/纯
// 空白目标行逐字恢复落库、零 warning——修复前版本落库的零目标行
// 可随备份迁移绕过新门重入（本地导入与 gist 恢复共用本路径），且
// 重入行在补正目标前被更新面冻结。台账裁定 5：恢复面逐行同谓词
// 校验、跳过 + warning、不整批拒绝。本测试以 H-F3 的 8 条恢复对
// 照格逐格钉死修后形态。
//
// 基线（e7dac59b）红点：",,," 行 restored=true、零 warning。

func TestFix8R807RemoteAddrHasNoTargets(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{",,,", true},
		{" , ,", true},
		{" ", true},
		{"\n,\r\n", true},
		{"\u3000", true}, // 全角空格属 TrimSpace 空白集，与写面 splitRemoteParts 同口径
		{"203.0.113.5:443", false},
		{",203.0.113.5:443", false},
		{"203.0.113.5:443\n198.51.100.7:443", false},
		{"\u200b", true}, // R9-09/I-F3-03 裁定翻转本格：纯 Cf 段不计入有效段（fix8 时按一段计），四面同判零目标
		{"a..b:443", false},
	}
	for _, c := range cases {
		if got := remoteAddrHasNoTargets(c.in); got != c.want {
			t.Errorf("remoteAddrHasNoTargets(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFix8R807ImportForwardsSkipsZeroTargetRows(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "fix8-r807.db")
	createRow(t, dest, &model.User{ID: 101, User: "alice", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 5, Name: "t5", Type: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	// 授权清扫（sweepImportedAuthorizationTx）要求目标库已有对应
	// user_tunnel 授权，否则转发整行按未授权跳过（与本测试无关的
	// 既有门，先行满足）。
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
			mk(61, "ok-normal", "203.0.113.5:443"),                    // 恢复
			mk(62, "bad-sep-only", ",,,"),                             // 跳过＋warning（本修复）
			mk(63, "bad-empty", ""),                                   // 跳过＋warning（本修复）
			mk(64, "bad-blank", " "),                                  // 跳过＋warning（本修复）
			mk(65, "ok-newline", "203.0.113.5:443\n198.51.100.7:443"), // 逐字恢复（R8-06 后渲染正确切分）
			mk(66, "ok-zwsp", "\u200b203.0.113.5:443"),                // 逐字恢复（非零目标，口径见谓词单测）
			mk(67, "ok-internal", "10.0.0.5:443"),                     // 逐字恢复（恢复面无 netguard、与管理员口径一致）
			mk(68, "bad-empty-label", "a..b:443"),                     // 跳过＋warning（既有空标签判，不回归）
		},
	}
	result, err := dest.Import(backup, []string{"forwards"})
	if err != nil {
		t.Fatalf("import must not fail wholesale: %v", err)
	}
	if result.ForwardsImported != 4 {
		t.Fatalf("ForwardsImported = %d, want 4 (normal/newline/zwsp/internal)", result.ForwardsImported)
	}
	if got := result.SkippedRows["forwards"]; got != 4 {
		t.Fatalf("SkippedRows[forwards] = %d, want 4 (sep-only/empty/blank/empty-label); result=%+v", got, result)
	}
	zeroWarn, labelWarn := 0, 0
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "无任何有效目标") {
			zeroWarn++
		}
		if strings.Contains(w, "含空标签") {
			labelWarn++
		}
	}
	if zeroWarn != 3 {
		t.Fatalf("zero-target rows must each produce a naming warning, got %d in %v", zeroWarn, result.ImportWarnings)
	}
	if labelWarn != 1 {
		t.Fatalf("empty-label warning must survive, got %d in %v", labelWarn, result.ImportWarnings)
	}
	// 落库面逐格：恢复 4 行 RemoteAddr 逐字、跳过 4 行不存在。
	for _, id := range []int64{62, 63, 64, 68} {
		if n := countWhere(t, dest, "forward", "id = ?", id); n != 0 {
			t.Fatalf("skipped forward #%d must not be written, found %d rows", id, n)
		}
	}
	wantStored := map[int64]string{
		61: "203.0.113.5:443",
		65: "203.0.113.5:443\n198.51.100.7:443",
		66: "\u200b203.0.113.5:443",
		67: "10.0.0.5:443",
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
