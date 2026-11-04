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

// —— 纯函数段：.env 解析与钉入 ——

func TestParseEnvFileValues(t *testing.T) {
	data := []byte("# 注释行\n\nFLMX_VERSION=1.9.8\nJWT_SECRET=abc=def\nEMPTY=\nFLMX_VERSION=9.9.9\nNOEQUALS\n SPACED = v \n")
	values := parseEnvFileValues(data)
	// 重复键以后现为准（Compose 的 dotenv 解析口径，修复批 D 对
	// 齐；旧实现取首现，与 Compose 运行时解析分歧）。
	if got := values["FLMX_VERSION"]; got != "9.9.9" {
		t.Fatalf("FLMX_VERSION = %q, want last occurrence 9.9.9 (Compose semantics)", got)
	}
	if got := values["JWT_SECRET"]; got != "abc=def" {
		t.Fatalf("JWT_SECRET = %q, want value after first =", got)
	}
	if got, ok := values["EMPTY"]; !ok || got != "" {
		t.Fatalf("EMPTY = %q (ok=%v), want present-but-empty", got, ok)
	}
	if _, ok := values["NOEQUALS"]; ok {
		t.Fatalf("line without = must not become a key")
	}
	if got := values["SPACED"]; got != "v" {
		t.Fatalf("SPACED = %q, want trimmed v", got)
	}
}

func TestApplyEnvPinsAppendsAndFillsEmpty(t *testing.T) {
	data := []byte("JWT_SECRET=x\nFLMX_SQLITE_VOLUME=\n# 保留注释\nFLMX_VERSION=1.9.8\n")
	out := string(applyEnvPins(data, []envKeyValue{
		{envKeySQLiteVolume, "sqlite_data"},
		{envKeyNetwork, "gost-network"},
	}))
	if !strings.Contains(out, "FLMX_SQLITE_VOLUME=sqlite_data\n") {
		t.Fatalf("empty-valued key must be filled in place:\n%s", out)
	}
	if strings.Count(out, "FLMX_SQLITE_VOLUME=") != 1 {
		t.Fatalf("sqlite key must appear exactly once:\n%s", out)
	}
	if !strings.HasSuffix(out, "FLMX_NETWORK=gost-network\n") {
		t.Fatalf("missing key must be appended at the end:\n%s", out)
	}
	for _, kept := range []string{"JWT_SECRET=x\n", "# 保留注释\n", "FLMX_VERSION=1.9.8\n"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("existing line %q must be preserved verbatim:\n%s", kept, out)
		}
	}
}

func TestApplyEnvPinsLeavesNonEmptyUntouched(t *testing.T) {
	data := []byte("FLMX_SQLITE_VOLUME=custom_data\n")
	if out := applyEnvPins(data, nil); string(out) != string(data) {
		t.Fatalf("no pins must return input unchanged, got %q", out)
	}
	// 规划段不会为已有非空键产 pin；此处直接验证填充逻辑对非空行
	// 不动手（防御纵深：即便误传 pin，非空值逐字保留）。
	out := string(applyEnvPins(data, []envKeyValue{{envKeySQLiteVolume, "other"}}))
	if out != string(data) {
		t.Fatalf("non-empty value must never be overwritten, got %q", out)
	}
}

// —— 纯规划段 ——

func keylessInspect() resourceNameInspect {
	return resourceNameInspect{
		backendName:              "flux-panel-backend",
		backendExists:            true,
		sqliteVolume:             "sqlite_data",
		pgVolume:                 "postgres_data",
		postgresDataVolumeExists: true,
		network:                  "gost-network",
		composeProject:           "flmx-panel",
	}
}

func TestPlanLegacyResourcePinsKeylessDeployment(t *testing.T) {
	plan, err := planLegacyResourcePins(map[string]string{}, keylessInspect())
	pins, expected := plan.pins, plan.expectedSQLiteVolume
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	want := []envKeyValue{
		{envKeySQLiteVolume, "sqlite_data"},
		{envKeyPostgresVolume, "postgres_data"},
		{envKeyNetwork, "gost-network"},
		{envKeyComposeProject, "flmx-panel"},
	}
	if len(pins) != len(want) {
		t.Fatalf("pins = %v, want %v", pins, want)
	}
	for i := range want {
		if pins[i] != want[i] {
			t.Fatalf("pins[%d] = %v, want %v (full: %v)", i, pins[i], want[i], pins)
		}
	}
	if expected != "sqlite_data" {
		t.Fatalf("expected = %q, want inspected sqlite volume", expected)
	}
}

func TestPlanLegacyResourcePinsPreservesExistingKeys(t *testing.T) {
	env := map[string]string{
		envKeySQLiteVolume:   "my_custom_volume",
		envKeyPostgresVolume: "my_pg_volume",
		envKeyNetwork:        "my-network",
		envKeyComposeProject: "myproject",
	}
	// 实读与已有键一致时（常态）：不钉、不改写，期望值即键值。
	// 键与实读冲突的处置见修复批 D 的冲突用例。
	insp := resourceNameInspect{
		backendName:    "flux-panel-backend",
		backendExists:  true,
		sqliteVolume:   "my_custom_volume",
		pgVolume:       "my_pg_volume",
		network:        "my-network",
		composeProject: "myproject",
	}
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if len(plan.pins) != 0 {
		t.Fatalf("existing keys must never be re-pinned, got %v", plan.pins)
	}
	if len(plan.corrections) != 0 {
		t.Fatalf("consistent keys must not be corrected, got %v", plan.corrections)
	}
	if plan.expectedSQLiteVolume != "my_custom_volume" {
		t.Fatalf("expected = %q, want the pre-existing .env value", plan.expectedSQLiteVolume)
	}
	if plan.expectedPostgresVolume != "my_pg_volume" {
		t.Fatalf("expected PG = %q, want the pre-existing .env value", plan.expectedPostgresVolume)
	}
}

func TestPlanLegacyResourcePinsEmptyValueCountsAsMissing(t *testing.T) {
	env := map[string]string{envKeySQLiteVolume: ""}
	plan, err := planLegacyResourcePins(env, keylessInspect())
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if plan.expectedSQLiteVolume != "sqlite_data" || len(plan.pins) == 0 || plan.pins[0] != (envKeyValue{envKeySQLiteVolume, "sqlite_data"}) {
		t.Fatalf("empty value must be treated as missing: pins=%v expected=%q", plan.pins, plan.expectedSQLiteVolume)
	}
}

func TestPlanLegacyResourcePinsFailClosedOnUnreadableVolume(t *testing.T) {
	insp := keylessInspect()
	insp.sqliteVolume = "" // bind 挂载 / inspect 失败形态
	_, err := planLegacyResourcePins(map[string]string{}, insp)
	if !errors.Is(err, errDataVolumeUndetermined) {
		t.Fatalf("err = %v, want errDataVolumeUndetermined (fail-closed)", err)
	}
	// 同形态但 .env 已有键：实读为空无从证伪键值，键保留，不触发
	// fail-closed、不回退猜测、不改写。
	plan, err := planLegacyResourcePins(
		map[string]string{envKeySQLiteVolume: "declared_data"}, insp)
	if err != nil {
		t.Fatalf("declared key must not fail-closed: %v", err)
	}
	if plan.expectedSQLiteVolume != "declared_data" {
		t.Fatalf("expected = %q, want declared key value", plan.expectedSQLiteVolume)
	}
	for _, pin := range plan.pins {
		if pin.key == envKeySQLiteVolume {
			t.Fatalf("declared key must not be re-pinned: %v", plan.pins)
		}
	}
	for _, correction := range plan.corrections {
		if correction.key == envKeySQLiteVolume {
			t.Fatalf("unverifiable key must not be corrected: %v", plan.corrections)
		}
	}
}

func TestPlanLegacyResourcePinsFreshInstallPasses(t *testing.T) {
	// 全新安装形态：无旧后端容器。不得 fail-closed、不得钉卷。
	insp := resourceNameInspect{backendName: "flmx-panel-backend", backendExists: false}
	plan, err := planLegacyResourcePins(map[string]string{}, insp)
	if err != nil {
		t.Fatalf("fresh install must pass, got %v", err)
	}
	if len(plan.pins) != 0 || plan.expectedSQLiteVolume != "" {
		t.Fatalf("fresh install must pin nothing, got pins=%v expected=%q", plan.pins, plan.expectedSQLiteVolume)
	}
}

func TestPlanLegacyResourcePinsPostgresFallbacks(t *testing.T) {
	base := keylessInspect()
	base.pgVolume = ""
	// PG 容器不在、postgres_data 卷在场：钉历史缺省名（脚本同口径）。
	plan, err := planLegacyResourcePins(map[string]string{}, base)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	found := false
	for _, pin := range plan.pins {
		if pin == (envKeyValue{envKeyPostgresVolume, "postgres_data"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("postgres_data fallback pin missing: %v", plan.pins)
	}
	// PG 卷与 postgres_data 都不在（SQLite 模式）：不钉 PG 键、不拦。
	base.postgresDataVolumeExists = false
	plan, err = planLegacyResourcePins(map[string]string{}, base)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	for _, pin := range plan.pins {
		if pin.key == envKeyPostgresVolume {
			t.Fatalf("no PG pin expected, got %v", plan.pins)
		}
	}
}

func TestUpgradeDataVolumeMismatchPredicate(t *testing.T) {
	cases := []struct {
		expected, actual string
		want             bool
	}{
		{"", "", false},
		{"", "sqlite_data", false}, // 期望值未记录：跳过校验
		{"sqlite_data", "sqlite_data", false},
		{"sqlite_data", "flmx-sqlite-data", true}, // 1.9.8 事故形态
		{"sqlite_data", "", true},                 // 实读为空同样不一致
	}
	for _, tc := range cases {
		if got := upgradeDataVolumeMismatch(tc.expected, tc.actual); got != tc.want {
			t.Fatalf("mismatch(%q, %q) = %v, want %v", tc.expected, tc.actual, got, tc.want)
		}
	}
}

// —— 执行器段：假 docker 驱动 inspect 采集 + 钉键落盘 ——

func writeFakeDocker(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake docker) error = %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

const fakeDockerFullInspect = `#!/bin/sh
case "$*" in
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"/app/data"*) echo "sqlite_data"; exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"NetworkSettings.Networks"*) echo "gost-network"; exit 0 ;;
  "ps -a"*) exit 0 ;;
  "volume inspect"*) exit 0 ;;
esac
exit 0
`

func TestPinLegacyResourceNamesKeylessEndToEnd(t *testing.T) {
	writeFakeDocker(t, fakeDockerFullInspect)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=keepme\nFLMX_VERSION=1.9.7\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	expected, expectedPG, err := executor.pinLegacyResourceNames(context.Background())
	if err != nil {
		t.Fatalf("pinLegacyResourceNames error = %v", err)
	}
	if expected != "sqlite_data" {
		t.Fatalf("expected = %q, want sqlite_data from inspect", expected)
	}
	if expectedPG != "" {
		t.Fatalf("SQLite-mode deployment must not emit a PG expectation, got %q", expectedPG)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	content := string(data)
	for _, line := range []string{
		"JWT_SECRET=keepme\n",
		"FLMX_VERSION=1.9.7\n",
		"FLMX_SQLITE_VOLUME=sqlite_data\n",
		"FLMX_POSTGRES_VOLUME=postgres_data\n",
		"FLMX_NETWORK=gost-network\n",
		"COMPOSE_PROJECT_NAME=flmx-panel\n",
	} {
		if !strings.Contains(content, line) {
			t.Fatalf(".env missing %q after pinning:\n%s", line, content)
		}
	}
	if strings.Count(content, "FLMX_SQLITE_VOLUME=") != 1 {
		t.Fatalf("sqlite key must be pinned exactly once:\n%s", content)
	}
}

func TestPinLegacyResourceNamesFailClosedLeavesEnvUntouched(t *testing.T) {
	// 后端在场（{{.Id}} 可读）但 /app/data 挂载实读为空（bind 形态）。
	writeFakeDocker(t, `#!/bin/sh
case "$*" in
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
  *"/app/data"*) exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"NetworkSettings.Networks"*) echo "gost-network"; exit 0 ;;
  "ps -a"*) exit 0 ;;
  "volume inspect"*) exit 0 ;;
esac
exit 0
`)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=keepme\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	_, _, err := executor.pinLegacyResourceNames(context.Background())
	if !errors.Is(err, errDataVolumeUndetermined) {
		t.Fatalf("err = %v, want fail-closed errDataVolumeUndetermined", err)
	}
	data, readErr := os.ReadFile(envPath)
	if readErr != nil {
		t.Fatalf("ReadFile(.env) error = %v", readErr)
	}
	if string(data) != original {
		t.Fatalf("fail-closed must not touch .env, got %q", data)
	}
}

func TestPinLegacyResourceNamesNoBackendPasses(t *testing.T) {
	writeFakeDocker(t, "#!/bin/sh\nexit 1\n")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=keepme\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	expected, _, err := executor.pinLegacyResourceNames(context.Background())
	if err != nil {
		t.Fatalf("no-backend must pass, got %v", err)
	}
	if expected != "" {
		t.Fatalf("expected = %q, want empty when nothing to record", expected)
	}
	data, _ := os.ReadFile(envPath)
	if string(data) != original {
		t.Fatalf(".env must be untouched, got %q", data)
	}
}

// —— helper 段：期望值透传与校验门结构 ——

func TestHelperRunArgsCarryExpectedSQLiteVolume(t *testing.T) {
	executor := &systemUpgradeExecutor{
		deployDir: "/opt/flmx-panel", backendContainer: "flmx-panel-backend",
		expectedSQLiteVolume: "sqlite_data",
	}
	// 按参数元素断言：helper 脚本正文（-c 的最后一个参数）本身含
	// 变量名，接成整串断言会被脚本正文污染。
	hasEnvArg := func(args []string, want string) bool {
		for _, arg := range args {
			if arg == want {
				return true
			}
		}
		return false
	}
	args, err := executor.buildHelperRunArgs("sha256:abc", "helper")
	if err != nil {
		t.Fatalf("buildHelperRunArgs error = %v", err)
	}
	if !hasEnvArg(args, expectedSQLiteVolumeEnvKey+"=sqlite_data") {
		t.Fatalf("helper args must carry the expected volume: %v", args)
	}

	executor.expectedSQLiteVolume = ""
	args, err = executor.buildHelperRunArgs("sha256:abc", "helper")
	if err != nil {
		t.Fatalf("buildHelperRunArgs error = %v", err)
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, expectedSQLiteVolumeEnvKey+"=") {
			t.Fatalf("empty expected volume must not be passed: %v", args)
		}
	}
}

func TestHelperScriptDataVolumeGateStructure(t *testing.T) {
	executor := &systemUpgradeExecutor{deployDir: "/opt/flmx-panel", backendContainer: "flmx-panel-backend"}
	script := executor.helperScript()
	healthIdx := strings.Index(script, "if ! wait_backend_healthy")
	gateIdx := strings.Index(script, expectedSQLiteVolumeEnvKey)
	successIdx := strings.Index(script, `write_status "false" "success"`)
	if healthIdx < 0 || gateIdx < 0 || successIdx < 0 {
		t.Fatalf("script segments missing: health=%d gate=%d success=%d", healthIdx, gateIdx, successIdx)
	}
	if !(healthIdx < gateIdx && gateIdx < successIdx) {
		t.Fatalf("data volume gate must sit after the health gate and before the success write")
	}
	gate := script[gateIdx:successIdx]
	for _, needle := range []string{"/app/data", "docker inspect", "rollback"} {
		if !strings.Contains(gate, needle) {
			t.Fatalf("data volume gate missing %q", needle)
		}
	}
}

// runHelperWithVolumeGate 用 stub docker/date/sleep 真跑一遍 helper
// （沿 TestHelperScriptRollbackRecreatesAndReportsFailure 的桩形
// 态，date 每拍 +10 让 30s 稳定窗在 300s 死线内达成）：新后端健康，
// inspect 给出 actualVolume，期望值恒为 sqlite_data。
func runHelperWithVolumeGate(t *testing.T, actualVolume string) (string, string, error) {
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
	write(filepath.Join(deployDir, ".env"), "FLMX_VERSION=1.9.8\n", 0o600)
	write(filepath.Join(deployDir, ".env.upgrade.bak"), "FLMX_VERSION=1.9.7\n", 0o600)
	write(counterPath, "1000", 0o644)

	dockerStub := `#!/bin/sh
echo "$@" >> "` + callsPath + `"
case "$*" in
  "compose pull"*) exit 0 ;;
  "compose up"*) exit 0 ;;
  "compose ps"*)
    case "$*" in
      *State*) echo "running" ;;
      *Name*) echo "backend-1" ;;
    esac
    exit 0 ;;
  "exec"*) exit 0 ;;
  "inspect"*) echo "` + actualVolume + `" ;;
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
		expectedSQLiteVolumeEnvKey+"=sqlite_data",
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

func TestHelperScriptDataVolumeMatchSucceeds(t *testing.T) {
	status, _, runErr := runHelperWithVolumeGate(t, "sqlite_data")
	if runErr != nil {
		t.Fatalf("helper must succeed when the mounted volume matches, err = %v", runErr)
	}
	if !strings.Contains(status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", status)
	}
}

func TestHelperScriptDataVolumeMismatchRollsBack(t *testing.T) {
	// 1.9.8 事故形态：新后端健康但挂到了缺省新卷 flmx-sqlite-data。
	status, calls, runErr := runHelperWithVolumeGate(t, "flmx-sqlite-data")
	if runErr == nil {
		t.Fatalf("helper must exit non-zero on data volume mismatch")
	}
	if !strings.Contains(status, `"result": "rolled_back"`) {
		t.Fatalf("status = %s, want rolled_back after mismatch", status)
	}
	if ups := strings.Count(calls, "compose up"); ups < 2 {
		t.Fatalf("rollback must re-run compose up with restored files; docker calls:\n%s", calls)
	}
}
