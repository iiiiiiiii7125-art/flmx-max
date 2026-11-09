package handler

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// —— 修复批 D · 第 4 项：.env 解析保真度（RA-1 L-4）——
// 三形状均以 compose-go dotenv.Parse 实测语义为准。

func TestParseEnvFileValuesStripsPairedQuotes(t *testing.T) {
	data := []byte("A=\"sqlite_data\"\nB='postgres_data'\nC=\"\"\nD=''\nE=\"with space\"\n")
	values := parseEnvFileValues(data)
	for key, want := range map[string]string{
		"A": "sqlite_data",
		"B": "postgres_data",
		"C": "", // 引号空值即空值：规划面按缺键处理（L-4 形状二）
		"D": "",
		"E": "with space",
	} {
		if got := values[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseEnvFileValuesStripsInlineComments(t *testing.T) {
	data := []byte("A=sqlite_data # 主数据卷\nB=sqlite_data\t# tab 前缀\nC=abc#def\nD=# 整值注释\nE=\"quoted # kept\"\n")
	values := parseEnvFileValues(data)
	for key, want := range map[string]string{
		"A": "sqlite_data",
		"B": "sqlite_data",
		"C": "abc#def",       // # 与值相邻不是注释（Compose 同口径）
		"D": "",              // 值首 # 即整值注释
		"E": "quoted # kept", // 引号内 # 不是注释
	} {
		if got := values[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseEnvFileValuesDuplicateKeyLastWins(t *testing.T) {
	data := []byte("FLMX_SQLITE_VOLUME=first_data\nFLMX_SQLITE_VOLUME=second_data\n")
	values := parseEnvFileValues(data)
	if got := values["FLMX_SQLITE_VOLUME"]; got != "second_data" {
		t.Fatalf("duplicate key = %q, want last occurrence second_data (Compose semantics)", got)
	}
}

func TestParseEnvFileValuesDoubleQuotedEscapes(t *testing.T) {
	data := []byte("A=\"line1\\nline2\"\nB='lit\\neral'\n")
	values := parseEnvFileValues(data)
	if got := values["A"]; got != "line1\nline2" {
		t.Fatalf("A = %q, want expanded newline", got)
	}
	if got := values["B"]; got != `lit\neral` {
		t.Fatalf("B = %q, want literal backslash in single quotes", got)
	}
}

// —— 修复批 D · 第 3 项：卷键与实读冲突以实读为准（RA-1 L-2）——

func TestPlanPinsVolumeKeyConflictLiveMountWins(t *testing.T) {
	env := map[string]string{
		envKeySQLiteVolume:   "stale_data",
		envKeyPostgresVolume: "stale_pg",
	}
	plan, err := planLegacyResourcePins(env, keylessInspect())
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	wantCorrections := []envKeyValue{
		{envKeySQLiteVolume, "sqlite_data"},
		{envKeyPostgresVolume, "postgres_data"},
	}
	if len(plan.corrections) != len(wantCorrections) {
		t.Fatalf("corrections = %v, want %v", plan.corrections, wantCorrections)
	}
	for i := range wantCorrections {
		if plan.corrections[i] != wantCorrections[i] {
			t.Fatalf("corrections[%d] = %v, want %v", i, plan.corrections[i], wantCorrections[i])
		}
	}
	for _, pin := range plan.pins {
		if pin.key == envKeySQLiteVolume || pin.key == envKeyPostgresVolume {
			t.Fatalf("conflicting volume keys must be corrected, not pinned: %v", plan.pins)
		}
	}
	if plan.expectedSQLiteVolume != "sqlite_data" || plan.expectedPostgresVolume != "postgres_data" {
		t.Fatalf("expectations must follow the live mounts, got sqlite=%q pg=%q",
			plan.expectedSQLiteVolume, plan.expectedPostgresVolume)
	}
}

func TestPlanPinsVolumeKeyKeptWhenNoLiveContainer(t *testing.T) {
	env := map[string]string{
		envKeySQLiteVolume:   "declared_data",
		envKeyPostgresVolume: "declared_pg",
	}
	// 无旧容器在场：实读全空，键值无从证伪，逐字保留。
	insp := resourceNameInspect{backendName: "flmx-panel-backend", backendExists: false}
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.pins) != 0 || len(plan.corrections) != 0 {
		t.Fatalf("no live container must leave keys untouched, got pins=%v corrections=%v",
			plan.pins, plan.corrections)
	}
	if plan.expectedSQLiteVolume != "declared_data" || plan.expectedPostgresVolume != "declared_pg" {
		t.Fatalf("expectations must be the declared keys, got sqlite=%q pg=%q",
			plan.expectedSQLiteVolume, plan.expectedPostgresVolume)
	}
}

func TestPinLegacyResourceNamesRewritesConflictingVolumeKey(t *testing.T) {
	writeFakeDocker(t, fakeDockerFullInspect)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=test\nFLMX_SQLITE_VOLUME=stale_data\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	expected, _, err := executor.pinLegacyResourceNames(context.Background())
	if err != nil {
		t.Fatalf("pinLegacyResourceNames error = %v", err)
	}
	if expected != "sqlite_data" {
		t.Fatalf("expected = %q, want the live mount sqlite_data", expected)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "FLMX_SQLITE_VOLUME=sqlite_data\n") {
		t.Fatalf("conflicting key must be rewritten to the live mount:\n%s", content)
	}
	if strings.Contains(content, "stale_data") {
		t.Fatalf("stale value must not survive:\n%s", content)
	}
	if strings.Count(content, "FLMX_SQLITE_VOLUME=") != 1 {
		t.Fatalf("sqlite key must appear exactly once:\n%s", content)
	}
}

// —— 修复批 D · 第 2 项：PG 卷钉键缺口 fail-closed（RA-3 §1）——

func pgModeInspect() resourceNameInspect {
	return resourceNameInspect{
		backendName:   "flmx-panel-backend",
		backendExists: true,
		sqliteVolume:  "sqlite_data",
		network:       "gost-network",
	}
}

func TestPlanPinsPostgresModeFailClosedWhenPGVolumeUndetermined(t *testing.T) {
	env := map[string]string{envKeyDBType: "postgres"}
	// PG 容器定位不到、实读为空、postgres_data 也不存在：事故子形。
	_, err := planLegacyResourcePins(env, pgModeInspect())
	if !errors.Is(err, errPGVolumeUndetermined) {
		t.Fatalf("err = %v, want errPGVolumeUndetermined (fail-closed)", err)
	}
}

func TestPlanPinsPostgresModePinsInspectedPGVolume(t *testing.T) {
	env := map[string]string{envKeyDBType: "postgres"}
	insp := pgModeInspect()
	insp.pgVolume = "custom_pg_data"
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	found := false
	for _, pin := range plan.pins {
		if pin == (envKeyValue{envKeyPostgresVolume, "custom_pg_data"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("inspected PG volume must be pinned: %v", plan.pins)
	}
	if plan.expectedPostgresVolume != "custom_pg_data" {
		t.Fatalf("expected PG = %q, want custom_pg_data", plan.expectedPostgresVolume)
	}
}

func TestPlanPinsPostgresModePostgresDataFallbackStillPins(t *testing.T) {
	env := map[string]string{envKeyDBType: "postgres"}
	insp := pgModeInspect()
	insp.postgresDataVolumeExists = true
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("postgres_data fallback must not fail-closed: %v", err)
	}
	if plan.expectedPostgresVolume != "postgres_data" {
		t.Fatalf("expected PG = %q, want postgres_data fallback", plan.expectedPostgresVolume)
	}
}

const fakeDockerNoVolumesAtAll = `#!/bin/sh
case "$*" in
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"/app/data"*) echo "sqlite_data"; exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"NetworkSettings.Networks"*) echo "gost-network"; exit 0 ;;
  "ps -a"*) exit 0 ;;
  "volume inspect"*) exit 1 ;;
esac
exit 0
`

func TestPinLegacyResourceNamesPostgresModeFailClosedLeavesEnvUntouched(t *testing.T) {
	writeFakeDocker(t, fakeDockerNoVolumesAtAll)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "DB_TYPE=postgres\nDATABASE_URL=postgres://x\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	_, _, err := executor.pinLegacyResourceNames(context.Background())
	if !errors.Is(err, errPGVolumeUndetermined) {
		t.Fatalf("err = %v, want fail-closed errPGVolumeUndetermined", err)
	}
	data, readErr := os.ReadFile(envPath)
	if readErr != nil {
		t.Fatalf("ReadFile(.env) error = %v", readErr)
	}
	if string(data) != original {
		t.Fatalf("fail-closed must not touch .env, got %q", data)
	}
}

func TestPinLegacyResourceNamesPostgresModeEmitsExpectation(t *testing.T) {
	writeFakeDocker(t, fakeDockerFullInspect)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("DB_TYPE=postgres\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	_, expectedPG, err := executor.pinLegacyResourceNames(context.Background())
	if err != nil {
		t.Fatalf("pinLegacyResourceNames error = %v", err)
	}
	if expectedPG != "postgres_data" {
		t.Fatalf("expectedPG = %q, want postgres_data (inspected/fallback)", expectedPG)
	}
}

// —— 修复批 D · 第 2 项：helper PG 卷校验门 ——

func TestHelperRunArgsCarryExpectedPostgresVolume(t *testing.T) {
	hasEnvArg := func(args []string, want string) bool {
		for _, arg := range args {
			if arg == want {
				return true
			}
		}
		return false
	}
	executor := &systemUpgradeExecutor{
		deployDir: "/opt/flmx-panel", backendContainer: "flmx-panel-backend",
		expectedPostgresVolume: "postgres_data",
	}
	args, err := executor.buildHelperRunArgs("sha256:abc", "helper")
	if err != nil {
		t.Fatalf("buildHelperRunArgs error = %v", err)
	}
	if !hasEnvArg(args, expectedPostgresVolumeEnvKey+"=postgres_data") {
		t.Fatalf("helper args must carry the expected PG volume: %v", args)
	}

	executor.expectedPostgresVolume = ""
	args, err = executor.buildHelperRunArgs("sha256:abc", "helper")
	if err != nil {
		t.Fatalf("buildHelperRunArgs error = %v", err)
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, expectedPostgresVolumeEnvKey+"=") {
			t.Fatalf("empty expected PG volume must not be passed: %v", args)
		}
	}
}

func TestHelperScriptPGVolumeGateStructure(t *testing.T) {
	executor := &systemUpgradeExecutor{deployDir: "/opt/flmx-panel", backendContainer: "flmx-panel-backend"}
	script := executor.helperScript()
	healthIdx := strings.Index(script, "if ! wait_backend_healthy")
	gateIdx := strings.Index(script, expectedPostgresVolumeEnvKey)
	successIdx := strings.Index(script, `write_status "false" "success"`)
	if healthIdx < 0 || gateIdx < 0 || successIdx < 0 {
		t.Fatalf("script segments missing: health=%d gate=%d success=%d", healthIdx, gateIdx, successIdx)
	}
	if !(healthIdx < gateIdx && gateIdx < successIdx) {
		t.Fatalf("PG volume gate must sit after the health gate and before the success write")
	}
	gate := script[gateIdx:successIdx]
	for _, needle := range []string{"/var/lib/postgresql/data", "docker inspect", "rollback"} {
		if !strings.Contains(gate, needle) {
			t.Fatalf("PG volume gate missing %q", needle)
		}
	}
}

// runHelperWithPGVolumeGate 真跑一遍 helper（桩形态同
// runHelperWithVolumeGate）：新后端健康、PG 容器挂载卷为
// actualPGVolume，期望值恒为 postgres_data；SQLite 期望不下发
// （SQLite 门跳过，只验 PG 门）。
func runHelperWithPGVolumeGate(t *testing.T, actualPGVolume string) (string, string, error) {
	t.Helper()
	deployDir := t.TempDir()
	stubDir := t.TempDir()
	callsPath := filepath.Join(stubDir, "docker-calls.log")
	counterPath := filepath.Join(stubDir, "date-counter")

	write := func(path, content string, perm os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), perm); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	write(filepath.Join(deployDir, "docker-compose.yml"), "new-compose\n", 0o644)
	write(filepath.Join(deployDir, "docker-compose.yml.upgrade.bak"), "old-compose\n", 0o644)
	write(filepath.Join(deployDir, ".env"), "DB_TYPE=postgres\nFLMX_VERSION=1.9.8\n", 0o600)
	write(filepath.Join(deployDir, ".env.upgrade.bak"), "DB_TYPE=postgres\nFLMX_VERSION=1.9.7\n", 0o600)
	write(counterPath, "1000", 0o644)

	dockerStub := `#!/bin/sh
echo "$@" >> "` + callsPath + `"
case "$*" in
  "compose pull"*) exit 0 ;;
  "compose up"*) exit 0 ;;
  "compose ps"*)
    case "$*" in
      *State*) echo "running" ;;
      *postgres*) echo "pg-1" ;;
      *Name*) echo "backend-1" ;;
    esac
    exit 0 ;;
  "exec"*) exit 0 ;;
  "inspect"*)
    case "$*" in
      *"Config.Image"*) echo "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:1.9.8" ;;
      *"/var/lib/postgresql/data"*) echo "` + actualPGVolume + `" ;;
      *) echo "sqlite_data" ;;
    esac
    exit 0 ;;
esac
exit 0
`
	write(filepath.Join(stubDir, "docker"), dockerStub, 0o755)
	dateStub := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    *%s*)
      n=$(cat "` + counterPath + `")
      n=$((n + 10))
      echo "$n" > "` + counterPath + `"
      echo "$n"
      exit 0
      ;;
  esac
done
echo "2026-01-01T00:00:00Z"
`
	write(filepath.Join(stubDir, "date"), dateStub, 0o755)
	write(filepath.Join(stubDir, "sleep"), "#!/bin/sh\nexit 0\n", 0o755)

	executor := &systemUpgradeExecutor{deployDir: deployDir, backendContainer: "flmx-panel-backend"}
	scriptPath := filepath.Join(stubDir, "helper.sh")
	write(scriptPath, executor.helperScript(), 0o755)

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PANEL_DEPLOY_DIR="+deployDir,
		"FLMX_UPGRADE_VERSION=1.9.8",
		"FLMX_UPGRADE_STARTED_AT=2026-01-01T00:00:00Z",
		expectedPostgresVolumeEnvKey+"=postgres_data",
	)
	out, runErr := cmd.CombinedOutput()

	status, err := os.ReadFile(filepath.Join(deployDir, ".flmx_upgrade_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(status) error = %v, helper output:\n%s", err, out)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile(docker calls) error = %v", err)
	}
	_ = out
	return string(status), string(calls), runErr
}

func TestHelperScriptPGVolumeMatchSucceeds(t *testing.T) {
	status, _, runErr := runHelperWithPGVolumeGate(t, "postgres_data")
	if runErr != nil {
		t.Fatalf("helper must succeed when the PG volume matches, err = %v", runErr)
	}
	if !strings.Contains(status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", status)
	}
}

func TestHelperScriptPGVolumeMismatchRollsBack(t *testing.T) {
	// 事故子形：PG 按模板缺省挂到了空卷 flmx-postgres-data。
	status, calls, runErr := runHelperWithPGVolumeGate(t, "flmx-postgres-data")
	if runErr == nil {
		t.Fatalf("helper must exit non-zero on PG volume mismatch")
	}
	if !strings.Contains(status, `"result": "rolled_back"`) {
		t.Fatalf("status = %s, want rolled_back after PG mismatch", status)
	}
	if ups := strings.Count(calls, "compose up"); ups < 2 {
		t.Fatalf("rollback must re-run compose up with restored files; docker calls:\n%s", calls)
	}
}

// —— 修复批 D · 第 1 项：PG 定位可信度（RA-1 L-1＋残差③）——

func TestComposeProjectMatches(t *testing.T) {
	cases := []struct {
		container, deployment string
		want                  bool
	}{
		{"proj", "proj", true},
		{"other", "proj", false}, // 双侧俱在且不同：可证地不属于本部署
		{"", "proj", true},       // 容器标签读不到：无从证伪
		{"proj", "", true},       // 部署项目名未知：无从证伪
		{"", "", true},
	}
	for _, tc := range cases {
		if got := composeProjectMatches(tc.container, tc.deployment); got != tc.want {
			t.Fatalf("matches(%q, %q) = %v, want %v", tc.container, tc.deployment, got, tc.want)
		}
	}
}

func TestLocatePostgresContainerTrustsDeclaredWhenProjectMatches(t *testing.T) {
	writeFakeDocker(t, `#!/bin/sh
case "$*" in
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"com.docker.compose.project"*"mypg"*) echo "proj1"; exit 0 ;;
  "ps -a"*) echo "labelpg"; exit 0 ;;
esac
exit 1
`)
	executor := &systemUpgradeExecutor{deployDir: t.TempDir(), backendContainer: "flmx-panel-backend"}
	env := map[string]string{envKeyPanelPostgresContainer: "mypg"}
	if got := executor.locatePostgresContainer(context.Background(), env, "proj1"); got != "mypg" {
		t.Fatalf("locate = %q, want declared mypg (project label matches)", got)
	}
}

func TestLocatePostgresContainerDistrustsDeclaredFromAnotherProject(t *testing.T) {
	// 陈旧声明被同机另一部署的同名 PG 容器复用：其项目标签与本
	// 部署不一致 → 声明作废，回落标签查找命中本项目真 PG。
	writeFakeDocker(t, `#!/bin/sh
case "$*" in
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"com.docker.compose.project"*"mypg"*) echo "otherproj"; exit 0 ;;
  "ps -a"*) echo "realpg"; exit 0 ;;
esac
exit 1
`)
	executor := &systemUpgradeExecutor{deployDir: t.TempDir(), backendContainer: "flmx-panel-backend"}
	env := map[string]string{envKeyPanelPostgresContainer: "mypg"}
	if got := executor.locatePostgresContainer(context.Background(), env, "proj1"); got != "realpg" {
		t.Fatalf("locate = %q, want label-found realpg (stale declared name must be distrusted)", got)
	}
}

func TestLocatePostgresContainerFallsBackWhenDeclaredMissing(t *testing.T) {
	// 声明名容器已不存在（残差③形态）：回落标签查找。
	writeFakeDocker(t, `#!/bin/sh
case "$*" in
  *"{{.Id}}"*"ghostpg"*) exit 1 ;;
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  "ps -a"*) echo "realpg"; exit 0 ;;
esac
exit 1
`)
	executor := &systemUpgradeExecutor{deployDir: t.TempDir(), backendContainer: "flmx-panel-backend"}
	env := map[string]string{envKeyPanelPostgresContainer: "ghostpg"}
	if got := executor.locatePostgresContainer(context.Background(), env, "proj1"); got != "realpg" {
		t.Fatalf("locate = %q, want label-found realpg (missing declared name must fall back)", got)
	}
}

func TestLocatePostgresContainerTrustsDeclaredWhenLabelUnreadable(t *testing.T) {
	writeFakeDocker(t, `#!/bin/sh
case "$*" in
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"com.docker.compose.project"*) exit 0 ;;
  "ps -a"*) echo "labelpg"; exit 0 ;;
esac
exit 1
`)
	executor := &systemUpgradeExecutor{deployDir: t.TempDir(), backendContainer: "flmx-panel-backend"}
	env := map[string]string{envKeyPanelPostgresContainer: "mypg"}
	if got := executor.locatePostgresContainer(context.Background(), env, "proj1"); got != "mypg" {
		t.Fatalf("locate = %q, want declared mypg (unreadable label cannot disprove ownership)", got)
	}
}

func TestLocatePostgresContainerEmptyWhenNothingFound(t *testing.T) {
	writeFakeDocker(t, `#!/bin/sh
case "$*" in
  "ps -a"*) exit 0 ;;
esac
exit 1
`)
	executor := &systemUpgradeExecutor{deployDir: t.TempDir(), backendContainer: "flmx-panel-backend"}
	if got := executor.locatePostgresContainer(context.Background(), map[string]string{}, "proj1"); got != "" {
		t.Fatalf("locate = %q, want empty", got)
	}
}
