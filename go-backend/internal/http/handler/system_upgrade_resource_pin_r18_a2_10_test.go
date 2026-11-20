package handler

import (
	"errors"
	"testing"
)

// R18-A2-10: planLegacyResourcePins 的有键改写分支无事故态门——
// 键在但与实读不一致时旧实现无条件以实读改写；在跑容器本身挂
// 错卷（真库在键卷里，现挂空的新缺省卷）时改写即把事故态正式
// 化。以下用例直接构造 resourceNameInspect（规划段纯函数），
// 探针字段即 inspectLegacyResourceNames 在门前件成立时采集的
// 双卷证据。
func TestPlanLegacyResourcePinsKeyedRewriteAccidentGateR18A210(t *testing.T) {
	// ① SQLite：键卷含库、实挂新缺省空卷 → errAccidentStateVolume。
	env := map[string]string{envKeySQLiteVolume: "sqlite_data"}
	insp := resourceNameInspect{
		backendName:              "flmx-panel-backend",
		backendExists:            true,
		sqliteVolume:             "flmx-sqlite-data",
		sqliteKeyVolumeHasDB:     true,
		sqliteMountedVolumeHasDB: false,
	}
	_, err := planLegacyResourcePins(env, insp)
	if !errors.Is(err, errAccidentStateVolume) {
		t.Fatalf("err = %v, want errAccidentStateVolume", err)
	}
	// ② 键卷探针失败、实挂卷证空 → errAccidentProbeFailed（未知不放行）。
	insp.sqliteKeyVolumeHasDB = false
	insp.sqliteKeyVolumeProbeErr = errors.New("probe boom")
	_, err = planLegacyResourcePins(env, insp)
	if !errors.Is(err, errAccidentProbeFailed) {
		t.Fatalf("err = %v, want errAccidentProbeFailed", err)
	}
	// ③ 实挂卷探针失败、键卷证含库 → errAccidentProbeFailed。
	insp.sqliteKeyVolumeHasDB = true
	insp.sqliteKeyVolumeProbeErr = nil
	insp.sqliteMountedVolumeProbeErr = errors.New("probe boom")
	_, err = planLegacyResourcePins(env, insp)
	if !errors.Is(err, errAccidentProbeFailed) {
		t.Fatalf("err = %v, want errAccidentProbeFailed", err)
	}
	// ④ 实挂卷已证含库（键陈旧）→ 放行改写。
	insp.sqliteMountedVolumeHasDB = true
	insp.sqliteMountedVolumeProbeErr = nil
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.corrections) != 1 || plan.corrections[0] != (envKeyValue{envKeySQLiteVolume, "flmx-sqlite-data"}) {
		t.Fatalf("corrections = %v, want the live-read rewrite", plan.corrections)
	}
	// ⑤ 键卷已证空（键指向空卷）→ 放行改写。
	insp.sqliteKeyVolumeHasDB = false
	insp.sqliteMountedVolumeHasDB = false
	plan, err = planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.corrections) != 1 || plan.corrections[0] != (envKeyValue{envKeySQLiteVolume, "flmx-sqlite-data"}) {
		t.Fatalf("corrections = %v, want the live-read rewrite", plan.corrections)
	}
	// ⑥ 实挂非新缺省名（普通改名）→ 门不触发，直接改写。
	insp.sqliteVolume = "custom_live"
	plan, err = planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.corrections) != 1 || plan.corrections[0].value != "custom_live" {
		t.Fatalf("corrections = %v, want the live-read rewrite", plan.corrections)
	}
	// ⑦ PG 格：键卷含 PG_VERSION、实挂新缺省空卷 → errAccidentStateVolume。
	pgEnv := map[string]string{envKeyPostgresVolume: "postgres_data"}
	pgInsp := resourceNameInspect{
		backendName:                  "flmx-panel-backend",
		backendExists:                true,
		sqliteVolume:                 "sqlite_data",
		pgVolume:                     "flmx-postgres-data",
		postgresKeyVolumeHasData:     true,
		postgresMountedVolumeHasData: false,
	}
	_, err = planLegacyResourcePins(pgEnv, pgInsp)
	if !errors.Is(err, errAccidentStateVolume) {
		t.Fatalf("pg err = %v, want errAccidentStateVolume", err)
	}
	// ⑧ PG 格：实挂卷已证含库 → 放行改写。
	pgInsp.postgresMountedVolumeHasData = true
	plan, err = planLegacyResourcePins(pgEnv, pgInsp)
	if err != nil {
		t.Fatalf("pg plan error = %v", err)
	}
	if len(plan.corrections) != 1 || plan.corrections[0] != (envKeyValue{envKeyPostgresVolume, "flmx-postgres-data"}) {
		t.Fatalf("pg corrections = %v, want the live-read rewrite", plan.corrections)
	}
	// ⑨ PG 格：键卷探针失败、实挂卷证空 → errAccidentProbeFailed
	// （与 SQLite ③ 同律：探针失败的 HasData=false 是未知，不
	// 得按事故态确证、也不得放行）。
	pgInsp.postgresKeyVolumeHasData = false
	pgInsp.postgresKeyVolumeProbeErr = errors.New("probe boom")
	pgInsp.postgresMountedVolumeHasData = false
	_, err = planLegacyResourcePins(pgEnv, pgInsp)
	if !errors.Is(err, errAccidentProbeFailed) {
		t.Fatalf("pg err = %v, want errAccidentProbeFailed", err)
	}
}
