package repo

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

// INV-2（事故专项审计路 3）：seedData 的播种动作必须可观测——
// 旧实现补建默认管理员/初始配置成功时零日志，已初始化部署被指
// 向空库（卷漂移）时在空库上静默播种成「如新装」、日志面无迹
// 可循。本用例钉住：首次打开空库时三处播种各留一条点名触发谓
// 词的 INFO 日志；对已初始化库重跑 seedData 时不得再打播种日志
// （日志只在实际补建时出现，不许每启必打、淹没事故证据）。
func TestSeedDataLogsSeedingActions(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOut)

	r, err := Open(filepath.Join(t.TempDir(), "seed-observe.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	out := buf.String()
	if !strings.Contains(out, "seeded default admin user") {
		t.Fatalf("first open of an empty database must log the default-admin seeding, got log: %q", out)
	}
	if !strings.Contains(out, "user count for id=1 == 0") {
		t.Fatalf("the admin seed log must name its trigger predicate (id=1 user count was 0), got log: %q", out)
	}
	if !strings.Contains(out, "seeded initial config app_name") {
		t.Fatalf("first open of an empty database must log the app_name config seeding, got log: %q", out)
	}
	if !strings.Contains(out, "seeded initial config hide_footer_brand") {
		t.Fatalf("first open of an empty database must log the hide_footer_brand config seeding, got log: %q", out)
	}

	buf.Reset()
	seedData(r.db)
	if got := buf.String(); strings.Contains(got, "seeded default admin user") || strings.Contains(got, "seeded initial config") {
		t.Fatalf("seedData on an initialized database must not log any seeding action, got log: %q", got)
	}
}
