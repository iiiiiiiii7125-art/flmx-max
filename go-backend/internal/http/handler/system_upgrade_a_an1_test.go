package handler

// —— A-AN1：事故态门证据探针失败 fail-closed ——
//
// dockerVolumeFileExists 三态化后：探针通道失败（卷查询报错／
// 探针容器运行失败）记 ProbeErr，规划段在门前件成立时以
// errAccidentProbeFailed 中止——未知不当无证据放行（与脚本面
// panel_volume_file_state 未知态同处置）。探针实证在／确定不
// 在两态行为与旧实现一致（由 fix199r1 用例钉住，本文件补探针
// 失败两幕与「卷确定不存在」一幕的执行器级对照）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— 规划段（纯函数） ——

func TestPlanPinsAccidentProbeFailureSQLiteAborts(t *testing.T) {
	insp := accidentSQLiteInspect()
	insp.sqliteLegacyVolumeHasDB = false
	insp.sqliteLegacyVolumeProbeErr = errors.New("probe channel down")
	plan, err := planLegacyResourcePins(map[string]string{}, insp)
	if !errors.Is(err, errAccidentProbeFailed) {
		t.Fatalf("err = %v, want errAccidentProbeFailed", err)
	}
	if len(plan.pins) != 0 || len(plan.corrections) != 0 {
		t.Fatalf("probe-failure gate must not pin or correct anything, got pins=%v corrections=%v", plan.pins, plan.corrections)
	}
	if !strings.Contains(err.Error(), "探针") || !strings.Contains(err.Error(), "sqlite_data") {
		t.Fatalf("gate error must name the probe failure and the legacy volume, got %v", err)
	}
}

func TestPlanPinsAccidentProbeFailurePostgresAborts(t *testing.T) {
	insp := accidentPGInspect()
	insp.postgresLegacyVolumeHasData = false
	insp.postgresLegacyVolumeProbeErr = errors.New("probe channel down")
	_, err := planLegacyResourcePins(map[string]string{}, insp)
	if !errors.Is(err, errAccidentProbeFailed) {
		t.Fatalf("err = %v, want errAccidentProbeFailed", err)
	}
	if !strings.Contains(err.Error(), "探针") || !strings.Contains(err.Error(), "PG_VERSION") {
		t.Fatalf("gate error must name the probe failure and its evidence, got %v", err)
	}
}

func TestPlanPinsAccidentProbeFailureOutsideGateShapeIgnored(t *testing.T) {
	// 探针只在门前件（实读恰为模板新缺省名）下才被采集；前件不
	// 成立时 ProbeErr 无从产生，此处钉住规划段的检查同样被前件
	// 圈定：自定义卷名＋残留 ProbeErr 不得误拦正常钉键。
	insp := accidentSQLiteInspect()
	insp.sqliteVolume = "custom_data"
	insp.sqliteLegacyVolumeHasDB = false
	insp.sqliteLegacyVolumeProbeErr = errors.New("probe channel down")
	plan, err := planLegacyResourcePins(map[string]string{}, insp)
	if err != nil {
		t.Fatalf("custom volume name must not trip the probe-failure gate: %v", err)
	}
	if plan.expectedSQLiteVolume != "custom_data" {
		t.Fatalf("expected = %q, want custom_data", plan.expectedSQLiteVolume)
	}
}

// —— 采集段端到端（fake docker） ——

// 探针容器运行失败（docker run 通道故障，退出码 125）：卷在、
// 镜像在，唯独探针跑不起来——旧实现压成「无证据」照常钉键。
const fakeDockerAccidentProbeRunDies = `#!/bin/sh
case "$*" in
  *"test -f /flmx-volume-probe/gost.db"*) echo "Error response from daemon: probe container failed" >&2; exit 125 ;;
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

// 卷查询本身报错（守护进程不可达形态）：inspect 非「卷不存
// 在」式失败——证据未知，须 fail-closed。
const fakeDockerAccidentProbeInspectDies = `#!/bin/sh
case "$*" in
  *"test -f /flmx-volume-probe/gost.db"*) exit 1 ;;
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"{{.Image}}"*) echo "sha256:backend"; exit 0 ;;
  *"/app/data"*) echo "flmx-sqlite-data"; exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"NetworkSettings.Networks"*) echo "gost-network"; exit 0 ;;
  "ps -a"*) exit 0 ;;
  "volume inspect"*) echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock" >&2; exit 1 ;;
esac
exit 0
`

// 旧名卷确定不存在（inspect 明示 no such volume）：这是「无
// 证据」的合法形态，行为与旧实现一致——照常钉实读卷。
const fakeDockerAccidentVolumeAbsent = `#!/bin/sh
case "$*" in
  *"test -f /flmx-volume-probe/gost.db"*) exit 1 ;;
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"{{.Image}}"*) echo "sha256:backend"; exit 0 ;;
  *"/app/data"*) echo "flmx-sqlite-data"; exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"NetworkSettings.Networks"*) echo "gost-network"; exit 0 ;;
  "ps -a"*) exit 0 ;;
  "volume inspect"*) echo "Error response from daemon: get sqlite_data: no such volume" >&2; exit 1 ;;
esac
exit 0
`

func writeAccidentEnv(t *testing.T) (string, string, string) {
	t.Helper()
	t.Setenv(envKeyInstance, "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=<redacted>\nFLMX_VERSION=1.9.9\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	return dir, envPath, original
}

func TestPinLegacyResourceNamesAccidentProbeRunFailureAbortsUntouched(t *testing.T) {
	writeFakeDocker(t, fakeDockerAccidentProbeRunDies)
	dir, envPath, original := writeAccidentEnv(t)
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	_, _, err := executor.pinLegacyResourceNames(context.Background())
	if !errors.Is(err, errAccidentProbeFailed) {
		t.Fatalf("err = %v, want errAccidentProbeFailed", err)
	}
	data, readErr := os.ReadFile(envPath)
	if readErr != nil {
		t.Fatalf("ReadFile(.env) error = %v", readErr)
	}
	if string(data) != original {
		t.Fatalf("probe-failure gate must not touch .env, got %q", data)
	}
}

func TestPinLegacyResourceNamesAccidentProbeInspectFailureAbortsUntouched(t *testing.T) {
	writeFakeDocker(t, fakeDockerAccidentProbeInspectDies)
	dir, envPath, original := writeAccidentEnv(t)
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	_, _, err := executor.pinLegacyResourceNames(context.Background())
	if !errors.Is(err, errAccidentProbeFailed) {
		t.Fatalf("err = %v, want errAccidentProbeFailed", err)
	}
	data, readErr := os.ReadFile(envPath)
	if readErr != nil {
		t.Fatalf("ReadFile(.env) error = %v", readErr)
	}
	if string(data) != original {
		t.Fatalf("probe-failure gate must not touch .env, got %q", data)
	}
}

func TestPinLegacyResourceNamesAccidentVolumeAbsentPinsLiveVolume(t *testing.T) {
	writeFakeDocker(t, fakeDockerAccidentVolumeAbsent)
	dir, _, _ := writeAccidentEnv(t)
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	expected, _, err := executor.pinLegacyResourceNames(context.Background())
	if err != nil {
		t.Fatalf("pinLegacyResourceNames error = %v", err)
	}
	if expected != "flmx-sqlite-data" {
		t.Fatalf("expected = %q, want the live volume pinned when the legacy volume provably does not exist", expected)
	}
}
