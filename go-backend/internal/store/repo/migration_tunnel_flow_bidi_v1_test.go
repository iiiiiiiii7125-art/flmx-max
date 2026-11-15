package repo

import (
	"testing"

	"go-backend/internal/store/model"
)

// C-01 迁移回归：把新装库手工改造成带单向存量的旧库
// （tunnel 行 Flow=1×2、Flow=2×1、Flow=0 脏行×1），跑迁移后
// 断言 Flow=1 行全翻为 2、其余行不动、版本已记、二跑幂等。
func TestMigrateV1TunnelFlowBidiDefault(t *testing.T) {
	r, err := Open(t.TempDir() + "/tunnel-flow-bidi-v1.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	db := r.DB()

	// 新装流程已在 Open 时记过 v1 版本；删记录以便重跑迁移本体。
	if err := db.Exec("DELETE FROM schema_migration WHERE version = ?", migrationTunnelFlowBidiV1Version).Error; err != nil {
		t.Fatalf("clear v1 version record: %v", err)
	}
	seed := func(name string, flow int64) {
		if err := db.Create(&model.Tunnel{
			Name: name, Type: 1, Flow: flow, Status: 1,
			CreatedTime: 1, UpdatedTime: 1,
		}).Error; err != nil {
			t.Fatalf("seed tunnel %s flow=%d: %v", name, flow, err)
		}
	}
	seed("legacy-one-a", 1)
	seed("legacy-one-b", 1)
	seed("already-bidi", 2)
	seed("dirty-zero", 0)

	if err := migrateV1TunnelFlowBidiDefault(db); err != nil {
		t.Fatalf("migrateV1TunnelFlowBidiDefault: %v", err)
	}

	var rows []model.Tunnel
	if err := db.Order("name").Find(&rows).Error; err != nil {
		t.Fatalf("read tunnels: %v", err)
	}
	want := map[string]int64{
		"legacy-one-a": 2, "legacy-one-b": 2,
		"already-bidi": 2, "dirty-zero": 0,
	}
	for _, row := range rows {
		if want[row.Name] != row.Flow {
			t.Errorf("tunnel %s flow = %d, want %d", row.Name, row.Flow, want[row.Name])
		}
	}
	var versionCount int64
	if err := db.Table("schema_migration").Where("version = ?", migrationTunnelFlowBidiV1Version).Count(&versionCount).Error; err != nil {
		t.Fatalf("count migration version: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("migration version rows = %d, want 1", versionCount)
	}
	// 二跑幂等：版本已记则直接跳过、不重复记、不再改行。
	if err := migrateV1TunnelFlowBidiDefault(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if err := db.Table("schema_migration").Where("version = ?", migrationTunnelFlowBidiV1Version).Count(&versionCount).Error; err != nil {
		t.Fatalf("count migration version after second run: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("migration version rows after second run = %d, want 1", versionCount)
	}
	var flowOne int64
	if err := db.Table("tunnel").Where("flow = 1").Count(&flowOne).Error; err != nil {
		t.Fatalf("count flow=1 rows: %v", err)
	}
	if flowOne != 0 {
		t.Errorf("flow=1 rows after second run = %d, want 0", flowOne)
	}
}
