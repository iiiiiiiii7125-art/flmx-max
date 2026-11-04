package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— R1-01-M2：DB_TYPE 别名 postgresql 在执行器全格按 PG 处置 ——
//
// 后端 app.New 与脚本 normalize_db_type 都接受别名，旧执行器
// isPostgresMode 只认 postgres 字面量：该拼写的 PG 部署在面板内
// 面整格失去 PG 保护（卷门不启用、fail-closed 不触发、改名置
// removeLegacyPGContainer、快照错排）。归一收敛到
// config.NormalizeDBType 单点后逐格验。

func TestIsPostgresModeAcceptsPostgresqlAlias(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"postgres", true},
		{"postgresql", true},
		{"PostgreSQL", true},
		{" PostgreSQL ", true},
		{"POSTGRES", true},
		{"sqlite", false},
		{"", false},
		{"mysql", false},
	} {
		env := map[string]string{envKeyDBType: tc.value}
		if got := isPostgresMode(env); got != tc.want {
			t.Fatalf("isPostgresMode(DB_TYPE=%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestPlanPinsPostgresqlAliasFailClosedWhenPGVolumeUndetermined(t *testing.T) {
	env := map[string]string{envKeyDBType: "postgresql"}
	// 与 postgres 拼写的 fail-closed 用例同形态：PG 容器定位不
	// 到、实读为空、postgres_data 不存在。别名拼写下旧实现整格
	// 跳过，本用例在旧实现必红。
	_, err := planLegacyResourcePins(env, pgModeInspect())
	if !errors.Is(err, errPGVolumeUndetermined) {
		t.Fatalf("err = %v, want errPGVolumeUndetermined (alias must fail-closed like postgres)", err)
	}
}

func TestPlanPinsPostgresqlAliasPinsInspectedPGVolume(t *testing.T) {
	env := map[string]string{envKeyDBType: "postgresql"}
	insp := pgModeInspect()
	insp.pgVolume = "custom_pg_data"
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if plan.expectedPostgresVolume != "custom_pg_data" {
		t.Fatalf("expected PG = %q, want custom_pg_data", plan.expectedPostgresVolume)
	}
}

func TestPrepareUpgradeSnapshotPostgresqlAliasDefers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_TYPE=postgresql\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	executor.prepareUpgradeSnapshot("1.9.10")
	if executor.snapshotPath != "" {
		t.Fatalf("alias PG mode must not name a sqlite snapshot path, got %q", executor.snapshotPath)
	}
	if !executor.pgSnapshotDeferred {
		t.Fatal("alias PG mode must mark the snapshot as deferred")
	}
}

func TestApplyIdentityRenamePostgresqlAliasRecreatesPostgres(t *testing.T) {
	writeFakeDocker(t, fakeDockerLocatePG)
	t.Setenv(envKeyInstance, "")
	t.Setenv(envKeyIdentityRename, "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "DB_TYPE=postgresql\nPANEL_BACKEND_CONTAINER=flux-panel-backend\nPANEL_POSTGRES_CONTAINER=flux-panel-postgres\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	if err := executor.applyIdentityRename(context.Background()); err != nil {
		t.Fatalf("applyIdentityRename error = %v", err)
	}
	// 别名拼写必须走 PG 支（重建），旧实现取 SQLite 支置
	// removeLegacyPGContainer、helper 会删掉在跑的 PG 容器。
	if !executor.recreatePostgres {
		t.Fatal("alias PG mode with renamed PG key must set recreatePostgres")
	}
	if executor.removeLegacyPGContainer != "" {
		t.Fatalf("alias PG mode must not schedule PG removal, got %q", executor.removeLegacyPGContainer)
	}
}

// —— R1-01-M1：事故态门 Go 面两格（SQLite 卷 / PG 卷） ——
//
// 谓词与脚本面 panel_pin_missing_resource_keys 同义：缺键＋实读
// 恰为模板新缺省卷名（带实例后缀）＋旧名卷含真实数据证据
// （gost.db / PG_VERSION）→ errAccidentStateVolume 中止、.env
// 一字未动。任一前件不成立则照常钉键/回退。

func accidentSQLiteInspect() resourceNameInspect {
	return resourceNameInspect{
		backendName:             "flmx-panel-backend",
		backendExists:           true,
		sqliteVolume:            "flmx-sqlite-data",
		sqliteLegacyVolumeHasDB: true,
		network:                 "gost-network",
		composeProject:          "flmx-panel",
	}
}

func TestPlanPinsAccidentStateSQLiteGateAborts(t *testing.T) {
	plan, err := planLegacyResourcePins(map[string]string{}, accidentSQLiteInspect())
	if !errors.Is(err, errAccidentStateVolume) {
		t.Fatalf("err = %v, want errAccidentStateVolume", err)
	}
	if len(plan.pins) != 0 || len(plan.corrections) != 0 {
		t.Fatalf("accident gate must not pin or correct anything, got pins=%v corrections=%v", plan.pins, plan.corrections)
	}
	if !strings.Contains(err.Error(), "sqlite_data") || !strings.Contains(err.Error(), "flmx-sqlite-data") {
		t.Fatalf("gate error must name both volumes, got %v", err)
	}
}

func TestPlanPinsAccidentStateSQLiteGateNeedsDBEvidence(t *testing.T) {
	insp := accidentSQLiteInspect()
	insp.sqliteLegacyVolumeHasDB = false
	plan, err := planLegacyResourcePins(map[string]string{}, insp)
	if err != nil {
		t.Fatalf("without database evidence the live volume must pin as usual: %v", err)
	}
	if plan.expectedSQLiteVolume != "flmx-sqlite-data" {
		t.Fatalf("expected = %q, want the live template-default volume pinned", plan.expectedSQLiteVolume)
	}
}

func TestPlanPinsAccidentStateSQLiteGateOnlyOnDefaultName(t *testing.T) {
	insp := accidentSQLiteInspect()
	insp.sqliteVolume = "custom_data"
	plan, err := planLegacyResourcePins(map[string]string{}, insp)
	if err != nil {
		t.Fatalf("custom volume name must not trip the gate: %v", err)
	}
	if plan.expectedSQLiteVolume != "custom_data" {
		t.Fatalf("expected = %q, want custom_data", plan.expectedSQLiteVolume)
	}
}

func TestPlanPinsAccidentStateSQLiteGateInstanceSuffix(t *testing.T) {
	t.Setenv(envKeyInstance, "")
	env := map[string]string{envKeyInstance: "inst1"}
	insp := accidentSQLiteInspect()
	insp.sqliteVolume = "flmx-sqlite-data-inst1"
	if _, err := planLegacyResourcePins(env, insp); !errors.Is(err, errAccidentStateVolume) {
		t.Fatalf("err = %v, want errAccidentStateVolume for the suffixed default name", err)
	}
	// 后缀不符（他实例的缺省名）不触发：不是本部署的模板缺省。
	insp.sqliteVolume = "flmx-sqlite-data-other"
	if _, err := planLegacyResourcePins(env, insp); err != nil {
		t.Fatalf("another instance's default name must not trip the gate: %v", err)
	}
}

func accidentPGInspect() resourceNameInspect {
	return resourceNameInspect{
		backendName:                 "flmx-panel-backend",
		backendExists:               true,
		sqliteVolume:                "sqlite_data",
		pgVolume:                    "flmx-postgres-data",
		postgresDataVolumeExists:    true,
		postgresLegacyVolumeHasData: true,
		network:                     "gost-network",
		composeProject:              "flmx-panel",
	}
}

func TestPlanPinsAccidentStatePostgresGateAborts(t *testing.T) {
	// PG 事故态：回退认领 postgres_data 的路径不得抢先于门——旧
	// 形态下此处会把 postgres_data 钉入（认领了旧卷，数据面看
	// 似得救，但实挂的空卷与键从此分家、自检同源于键）。
	// 注：规划顺序上 SQLite 钉键先于 PG 支入 plan，但规划出错即
	// 整单作废——pinLegacyResourceNames 在 err 时不写 .env（由执
	// 行器级用例钉住），故此处只断言错误本体与点名。
	_, err := planLegacyResourcePins(map[string]string{}, accidentPGInspect())
	if !errors.Is(err, errAccidentStateVolume) {
		t.Fatalf("err = %v, want errAccidentStateVolume", err)
	}
	if !strings.Contains(err.Error(), "postgres_data") || !strings.Contains(err.Error(), "PG_VERSION") {
		t.Fatalf("gate error must name the legacy volume and its evidence, got %v", err)
	}
}

func TestPlanPinsAccidentStatePostgresGateNeedsDataEvidence(t *testing.T) {
	insp := accidentPGInspect()
	insp.postgresLegacyVolumeHasData = false
	plan, err := planLegacyResourcePins(map[string]string{}, insp)
	if err != nil {
		t.Fatalf("without PG data evidence the live volume must pin as usual: %v", err)
	}
	if plan.expectedPostgresVolume != "flmx-postgres-data" {
		t.Fatalf("expected PG = %q, want the live template-default volume pinned", plan.expectedPostgresVolume)
	}
}

// —— R1-01-L5：PG bind＋错键在规划段 fail-closed（两面统一） ——

func pgBindInspect() resourceNameInspect {
	return resourceNameInspect{
		backendName:      "flmx-panel-backend",
		backendExists:    true,
		sqliteVolume:     "sqlite_data",
		pgContainerFound: true,
		pgMountInspectOK: true,
		pgVolume:         "",
		network:          "gost-network",
		composeProject:   "flmx-panel",
	}
}

func TestPlanPinsPostgresBindWithDeclaredKeyAborts(t *testing.T) {
	env := map[string]string{
		envKeyDBType:         "postgres",
		envKeyPostgresVolume: "declared_pg",
	}
	_, err := planLegacyResourcePins(env, pgBindInspect())
	if !errors.Is(err, errPGVolumeUndetermined) {
		t.Fatalf("err = %v, want errPGVolumeUndetermined (bind + declared key must fail-closed)", err)
	}
}

func TestPlanPinsPostgresBindInspectFailureKeepsKey(t *testing.T) {
	env := map[string]string{
		envKeyDBType:         "postgres",
		envKeyPostgresVolume: "declared_pg",
	}
	insp := pgBindInspect()
	insp.pgMountInspectOK = false // inspect 本身失败：无从证伪键值
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("unreadable mount must keep the declared key, got %v", err)
	}
	if plan.expectedPostgresVolume != "declared_pg" {
		t.Fatalf("expected PG = %q, want declared_pg", plan.expectedPostgresVolume)
	}
}

func TestPlanPinsPostgresBindIgnoredInSQLiteMode(t *testing.T) {
	env := map[string]string{envKeyPostgresVolume: "declared_pg"}
	plan, err := planLegacyResourcePins(env, pgBindInspect())
	if err != nil {
		t.Fatalf("SQLite mode must not gate on the inert PG container: %v", err)
	}
	if plan.expectedPostgresVolume != "declared_pg" {
		t.Fatalf("expected PG = %q, want declared_pg", plan.expectedPostgresVolume)
	}
}

func TestPlanPinsPostgresBindKeylessKeepsFallback(t *testing.T) {
	env := map[string]string{envKeyDBType: "postgres"}
	insp := pgBindInspect()
	insp.postgresDataVolumeExists = true
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("keyless bind shape must keep the postgres_data fallback adjudication: %v", err)
	}
	if plan.expectedPostgresVolume != "postgres_data" {
		t.Fatalf("expected PG = %q, want postgres_data fallback", plan.expectedPostgresVolume)
	}
}

// —— 采集段端到端：事故态 SQLite 部署经 pinLegacyResourceNames
// 中止且 .env 一字未动（探针容器证伪/证实两幕） ——

const fakeDockerAccidentProbeHit = `#!/bin/sh
case "$*" in
  *"test -f /flmx-volume-probe/gost.db"*) exit 0 ;;
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"{{.Image}}"*) echo "sha256:backend"; exit 0 ;;
  *"/app/data"*) echo "flmx-sqlite-data"; exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"NetworkSettings.Networks"*) echo "gost-network"; exit 0 ;;
  "ps -a"*) exit 0 ;;
  "volume inspect"*) exit 0 ;;
esac
exit 0
`

const fakeDockerAccidentProbeMiss = `#!/bin/sh
case "$*" in
  *"test -f /flmx-volume-probe/gost.db"*) exit 1 ;;
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"{{.Image}}"*) echo "sha256:backend"; exit 0 ;;
  *"/app/data"*) echo "flmx-sqlite-data"; exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"NetworkSettings.Networks"*) echo "gost-network"; exit 0 ;;
  "ps -a"*) exit 0 ;;
  "volume inspect"*) exit 0 ;;
esac
exit 0
`

func TestPinLegacyResourceNamesAccidentStateAbortsUntouched(t *testing.T) {
	writeFakeDocker(t, fakeDockerAccidentProbeHit)
	t.Setenv(envKeyInstance, "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=test\nFLMX_VERSION=1.9.9\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	_, _, err := executor.pinLegacyResourceNames(context.Background())
	if !errors.Is(err, errAccidentStateVolume) {
		t.Fatalf("err = %v, want errAccidentStateVolume", err)
	}
	data, readErr := os.ReadFile(envPath)
	if readErr != nil {
		t.Fatalf("ReadFile(.env) error = %v", readErr)
	}
	if string(data) != original {
		t.Fatalf("accident gate must not touch .env, got %q", data)
	}
}

func TestPinLegacyResourceNamesAccidentProbeMissPinsLiveVolume(t *testing.T) {
	writeFakeDocker(t, fakeDockerAccidentProbeMiss)
	t.Setenv(envKeyInstance, "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("JWT_SECRET=test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	expected, _, err := executor.pinLegacyResourceNames(context.Background())
	if err != nil {
		t.Fatalf("pinLegacyResourceNames error = %v", err)
	}
	if expected != "flmx-sqlite-data" {
		t.Fatalf("expected = %q, want the live volume pinned when the legacy volume holds no database", expected)
	}
}
