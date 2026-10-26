package repo

import (
	"path/filepath"
	"testing"
	"time"
)

// R31-L3-H02 (F-04): 全新恢复后默认订阅「我的订阅」重复两条，且
// DefaultSubscription() 指向新库自带的、从未分享过的那条。
// 机制：启动期 migrateSubscriptions 在订阅数为 0 的库上以随机 token
// 建一条默认订阅；导入另一面板（或 token 已轮换后的旧备份）时，
// 备份自带的默认订阅按 token 不匹配，另起一行。
//
// 探针（审计原形重建）：源库仅默认订阅 → 导出 inbounds 段 → 全新
// 目标库导入 → 默认订阅 1 条，且 DefaultSubscription() 指向导入
// 的那条（token 以备份为准）。

func TestR31H02DefaultSubscriptionDuplicated(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	// 源库仅默认订阅：Open 期 migrateSubscriptions 自带的种子即是。
	srcDef, err := src.DefaultSubscription(now)
	if err != nil || srcDef == nil {
		t.Fatalf("source default subscription: %v", err)
	}
	var srcCount int
	if err := src.DB().Raw(`SELECT COUNT(*) FROM subscription`).Scan(&srcCount).Error; err != nil {
		t.Fatal(err)
	}
	if srcCount != 1 {
		t.Fatalf("test setup: source subscriptions = %d, want 1 (only the default)", srcCount)
	}
	srcToken := srcDef.Token

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	dstSeed, err := dst.DefaultSubscription(now)
	if err != nil || dstSeed == nil {
		t.Fatalf("target seed default subscription: %v", err)
	}
	if dstSeed.Token == srcToken {
		t.Fatal("test setup: source and target seed tokens must differ")
	}

	if _, err := dst.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatalf("import inbounds: %v", err)
	}

	var tokens []string
	if err := dst.DB().Raw(`SELECT token FROM subscription WHERE name = '我的订阅' AND source_all = 1 ORDER BY id ASC`).Scan(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("H-02: default subscriptions after fresh restore = %d, want 1 (tokens %v)", len(tokens), tokens)
	}
	if tokens[0] != srcToken {
		t.Fatalf("H-02: surviving default token = %s, want imported token %s", tokens[0], srcToken)
	}
	var total int
	if err := dst.DB().Raw(`SELECT COUNT(*) FROM subscription`).Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("H-02: total subscriptions after fresh restore = %d, want 1", total)
	}
	def, err := dst.DefaultSubscription(now)
	if err != nil || def == nil {
		t.Fatalf("DefaultSubscription after restore: %v", err)
	}
	if def.Token != srcToken {
		t.Fatalf("H-02: DefaultSubscription token = %s, want imported token %s", def.Token, srcToken)
	}
}

// 同面板备份恢复对照：token 匹配，行为不变——仍 1 条默认订阅，
// token 不变，既有链接继续有效。
func TestR31H02SamePanelRestoreKeepsDefault(t *testing.T) {
	now := time.Now().UnixMilli()
	r, err := Open(filepath.Join(t.TempDir(), "same.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	def, err := r.DefaultSubscription(now)
	if err != nil || def == nil {
		t.Fatalf("default subscription: %v", err)
	}
	token := def.Token

	backup, err := r.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatalf("import inbounds: %v", err)
	}

	var tokens []string
	if err := r.DB().Raw(`SELECT token FROM subscription WHERE name = '我的订阅' AND source_all = 1 ORDER BY id ASC`).Scan(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("same-panel restore: default subscriptions = %d, want 1 (tokens %v)", len(tokens), tokens)
	}
	if tokens[0] != token {
		t.Fatalf("same-panel restore: default token changed to %s, want %s", tokens[0], token)
	}
	var total int
	if err := r.DB().Raw(`SELECT COUNT(*) FROM subscription`).Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("same-panel restore: total subscriptions = %d, want 1", total)
	}
}

// 已投入使用的默认订阅不受复用影响：目标库默认订阅已有流量时，
// 导入的外来默认订阅仍按既往口径另起一行，不夺取其链接。
func TestR31H02EstablishedDefaultNotReused(t *testing.T) {
	now := time.Now().UnixMilli()
	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	srcDef, err := src.DefaultSubscription(now)
	if err != nil || srcDef == nil {
		t.Fatalf("source default subscription: %v", err)
	}
	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	dstDef, err := dst.DefaultSubscription(now)
	if err != nil || dstDef == nil {
		t.Fatalf("target default subscription: %v", err)
	}
	// 模拟已投入使用的默认订阅：已有累计流量。
	if err := dst.DB().Exec(`UPDATE subscription SET upload_bytes = 1000 WHERE id = ?`, dstDef.ID).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := dst.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatalf("import inbounds: %v", err)
	}

	var tokens []string
	if err := dst.DB().Raw(`SELECT token FROM subscription WHERE name = '我的订阅' AND source_all = 1 ORDER BY id ASC`).Scan(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("established target: default subscriptions = %d, want 2 (tokens %v)", len(tokens), tokens)
	}
	if tokens[0] != dstDef.Token {
		t.Fatalf("established target: existing default token changed to %s, want %s", tokens[0], dstDef.Token)
	}
	if tokens[1] != srcDef.Token {
		t.Fatalf("established target: imported default token = %s, want %s", tokens[1], srcDef.Token)
	}
}
