package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// —— 改名规划：三态 × 实例后缀 ——

func TestPlanIdentityRename(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		suffix    string
		wantKeys  []string
		wantPG    bool
		wantFirst string // writes[0] 的目标值（后端键），空表示不校验
	}{
		{
			name:     "legacy names renamed",
			env:      map[string]string{envKeyPanelBackendContainer: "flux-panel-backend", envKeyPanelFrontendContainer: "vite-frontend", envKeyPanelPostgresContainer: "flux-panel-postgres"},
			wantKeys: []string{envKeyPanelBackendContainer, envKeyPanelFrontendContainer, envKeyPanelPostgresContainer},
			wantPG:   true,
		},
		{
			name:     "missing keys renamed",
			env:      map[string]string{},
			wantKeys: []string{envKeyPanelBackendContainer, envKeyPanelFrontendContainer, envKeyPanelPostgresContainer},
			wantPG:   true,
		},
		{
			name:     "custom names kept verbatim",
			env:      map[string]string{envKeyPanelBackendContainer: "my-backend", envKeyPanelFrontendContainer: "my-frontend", envKeyPanelPostgresContainer: "my-pg"},
			wantKeys: nil,
		},
		{
			name:     "current names kept",
			env:      map[string]string{envKeyPanelBackendContainer: "flmx-panel-backend", envKeyPanelFrontendContainer: "flmx-panel-frontend", envKeyPanelPostgresContainer: "flmx-panel-postgres"},
			wantKeys: nil,
		},
		{
			name:      "instance suffix applied",
			env:       map[string]string{},
			suffix:    "-eu1",
			wantKeys:  []string{envKeyPanelBackendContainer, envKeyPanelFrontendContainer, envKeyPanelPostgresContainer},
			wantPG:    true,
			wantFirst: "flmx-panel-backend-eu1",
		},
		{
			name:     "mixed: only legacy pg renamed",
			env:      map[string]string{envKeyPanelBackendContainer: "my-backend", envKeyPanelFrontendContainer: "flmx-panel-frontend", envKeyPanelPostgresContainer: "flux-panel-postgres"},
			wantKeys: []string{envKeyPanelPostgresContainer},
			wantPG:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := planIdentityRename(tc.env, tc.suffix, "flux-panel-postgres")
			if len(out.writes) != len(tc.wantKeys) {
				t.Fatalf("writes = %v, want keys %v", out.writes, tc.wantKeys)
			}
			for i, key := range tc.wantKeys {
				if out.writes[i].key != key {
					t.Fatalf("writes[%d].key = %q, want %q", i, out.writes[i].key, key)
				}
			}
			if out.pgRenamed != tc.wantPG {
				t.Fatalf("pgRenamed = %v, want %v", out.pgRenamed, tc.wantPG)
			}
			if tc.wantPG && out.oldPGContainer != "flux-panel-postgres" {
				t.Fatalf("oldPGContainer = %q, want passthrough", out.oldPGContainer)
			}
			if tc.wantFirst != "" && out.writes[0].value != tc.wantFirst {
				t.Fatalf("writes[0].value = %q, want %q", out.writes[0].value, tc.wantFirst)
			}
		})
	}
}

func TestPanelInstanceSuffix(t *testing.T) {
	t.Setenv(envKeyInstance, "")
	if got := panelInstanceSuffix(map[string]string{envKeyInstance: "eu1"}); got != "-eu1" {
		t.Fatalf("suffix from .env = %q, want -eu1", got)
	}
	if got := panelInstanceSuffix(map[string]string{}); got != "" {
		t.Fatalf("missing instance must give empty suffix, got %q", got)
	}
	for _, bad := range []string{"EU1", "-lead", "has space", strings.Repeat("a", 33), "dot.name"} {
		if got := panelInstanceSuffix(map[string]string{envKeyInstance: bad}); got != "" {
			t.Fatalf("invalid instance %q must fall back to default (empty suffix), got %q", bad, got)
		}
	}
	// 进程环境优先于 .env（脚本 panel_instance_id 同序）。
	t.Setenv(envKeyInstance, "proc1")
	if got := panelInstanceSuffix(map[string]string{envKeyInstance: "envfile"}); got != "-proc1" {
		t.Fatalf("process env must win, got %q", got)
	}
}

func TestIdentityRenameEnabled(t *testing.T) {
	t.Setenv(envKeyIdentityRename, "")
	if !identityRenameEnabled(map[string]string{}) {
		t.Fatal("missing flag must default to enabled")
	}
	if identityRenameEnabled(map[string]string{envKeyIdentityRename: "0"}) {
		t.Fatal(".env flag 0 must disable rename")
	}
	if !identityRenameEnabled(map[string]string{envKeyIdentityRename: ""}) {
		t.Fatal("empty flag value must stay enabled (script ${VAR:-1} semantics)")
	}
	t.Setenv(envKeyIdentityRename, "0")
	if identityRenameEnabled(map[string]string{}) {
		t.Fatal("process env flag 0 must disable rename when .env lacks the key")
	}
	// .env 在场时优先于进程环境（面板内升级面的操作面是 .env）。
	if !identityRenameEnabled(map[string]string{envKeyIdentityRename: "1"}) {
		t.Fatal(".env flag must win over process env")
	}
}

// —— 改名执行段：假 docker 驱动 PG 定位 + 键改写落盘 ——

const fakeDockerLocatePG = `#!/bin/sh
case "$*" in
  "ps -a"*) echo "flux-panel-postgres"; exit 0 ;;
  *"com.docker.compose.project"*) echo "flmx-panel"; exit 0 ;;
  *"{{.Id}}"*) echo "container-id"; exit 0 ;;
esac
exit 0
`

func TestApplyIdentityRenameSQLiteEndToEnd(t *testing.T) {
	writeFakeDocker(t, fakeDockerLocatePG)
	t.Setenv(envKeyInstance, "")
	t.Setenv(envKeyIdentityRename, "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "JWT_SECRET=keepme\nPANEL_BACKEND_CONTAINER=flux-panel-backend\nPANEL_FRONTEND_CONTAINER=vite-frontend\nFLMX_VERSION=1.9.8\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	if err := executor.applyIdentityRename(context.Background()); err != nil {
		t.Fatalf("applyIdentityRename error = %v", err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	content := string(data)
	for _, line := range []string{
		"PANEL_BACKEND_CONTAINER=flmx-panel-backend\n",
		"PANEL_FRONTEND_CONTAINER=flmx-panel-frontend\n",
		"PANEL_POSTGRES_CONTAINER=flmx-panel-postgres\n",
		"JWT_SECRET=keepme\n",
	} {
		if !strings.Contains(content, line) {
			t.Fatalf(".env missing %q after rename:\n%s", line, content)
		}
	}
	// SQLite 模式：旧 PG 容器由 helper 在成功前删除（裁定 (a)）。
	if executor.removeLegacyPGContainer != "flux-panel-postgres" {
		t.Fatalf("removeLegacyPGContainer = %q, want flux-panel-postgres", executor.removeLegacyPGContainer)
	}
	if executor.recreatePostgres {
		t.Fatal("sqlite mode must not recreate postgres via helper services")
	}
}

func TestApplyIdentityRenamePostgresModeRecreates(t *testing.T) {
	writeFakeDocker(t, fakeDockerLocatePG)
	t.Setenv(envKeyInstance, "")
	t.Setenv(envKeyIdentityRename, "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	original := "DB_TYPE=postgres\nPANEL_BACKEND_CONTAINER=flux-panel-backend\nPANEL_POSTGRES_CONTAINER=flux-panel-postgres\n"
	if err := os.WriteFile(envPath, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	if err := executor.applyIdentityRename(context.Background()); err != nil {
		t.Fatalf("applyIdentityRename error = %v", err)
	}
	if !executor.recreatePostgres {
		t.Fatal("postgres mode with renamed PG key must set recreatePostgres")
	}
	if executor.removeLegacyPGContainer != "" {
		t.Fatalf("postgres mode must not schedule PG removal, got %q", executor.removeLegacyPGContainer)
	}
}

func TestApplyIdentityRenameDisabledAndCustomUntouched(t *testing.T) {
	writeFakeDocker(t, fakeDockerLocatePG)
	t.Setenv(envKeyInstance, "")
	t.Setenv(envKeyIdentityRename, "")
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"rename disabled", "PANEL_IDENTITY_RENAME=0\nPANEL_BACKEND_CONTAINER=flux-panel-backend\n"},
		{"custom names", "PANEL_BACKEND_CONTAINER=my-backend\nPANEL_FRONTEND_CONTAINER=my-frontend\nPANEL_POSTGRES_CONTAINER=my-pg\n"},
		{"already current", "PANEL_BACKEND_CONTAINER=flmx-panel-backend\nPANEL_FRONTEND_CONTAINER=flmx-panel-frontend\nPANEL_POSTGRES_CONTAINER=flmx-panel-postgres\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			envPath := filepath.Join(dir, ".env")
			if err := os.WriteFile(envPath, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("WriteFile(.env) error = %v", err)
			}
			executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
			if err := executor.applyIdentityRename(context.Background()); err != nil {
				t.Fatalf("applyIdentityRename error = %v", err)
			}
			data, err := os.ReadFile(envPath)
			if err != nil {
				t.Fatalf("ReadFile(.env) error = %v", err)
			}
			if string(data) != tc.content {
				t.Fatalf(".env must be untouched, got %q", data)
			}
			if executor.recreatePostgres || executor.removeLegacyPGContainer != "" {
				t.Fatalf("no helper PG action must be scheduled: recreate=%v remove=%q",
					executor.recreatePostgres, executor.removeLegacyPGContainer)
			}
		})
	}
}

// —— 快照安排 ——

func TestPrepareUpgradeSnapshot(t *testing.T) {
	t.Run("sqlite names a deterministic snapshot path", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("JWT_SECRET=x\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(.env) error = %v", err)
		}
		executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
		executor.prepareUpgradeSnapshot("1.9.9")
		pattern := regexp.MustCompile(`^backups/panel-update-1\.9\.9-\d{8}-\d{6}$`)
		if !pattern.MatchString(executor.snapshotPath) {
			t.Fatalf("snapshotPath = %q, want backups/panel-update-1.9.9-<UTC timestamp>", executor.snapshotPath)
		}
		if executor.pgSnapshotDeferred {
			t.Fatal("sqlite mode must not mark the snapshot deferred")
		}
	})
	t.Run("postgres defers explicitly", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_TYPE=postgres\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(.env) error = %v", err)
		}
		executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
		executor.prepareUpgradeSnapshot("1.9.9")
		if executor.snapshotPath != "" {
			t.Fatalf("postgres mode must not name a snapshot path, got %q", executor.snapshotPath)
		}
		if !executor.pgSnapshotDeferred {
			t.Fatal("postgres mode must mark the snapshot as deferred (registered cut-out)")
		}
	})
}

// —— helper 透传与脚本结构 ——

func TestBuildHelperRunArgsCarryRenameAndSnapshot(t *testing.T) {
	executor := &systemUpgradeExecutor{
		deployDir: "/opt/flmx-panel", backendContainer: "flmx-panel-backend",
		recreatePostgres: true, snapshotPath: "backups/panel-update-1.9.9-20260101-000000",
	}
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
	if !hasEnvArg(args, upgradeServicesEnvKey+"=backend frontend postgres") {
		t.Fatalf("args must carry the postgres-inclusive service set: %v", args)
	}
	if !hasEnvArg(args, snapshotDirEnvKey+"=backups/panel-update-1.9.9-20260101-000000") {
		t.Fatalf("args must carry the snapshot dir: %v", args)
	}
	executor.recreatePostgres = false
	executor.snapshotPath = ""
	executor.removeLegacyPGContainer = "flux-panel-postgres"
	executor.pgSnapshotDeferred = true
	args, err = executor.buildHelperRunArgs("sha256:abc", "helper")
	if err != nil {
		t.Fatalf("buildHelperRunArgs error = %v", err)
	}
	if !hasEnvArg(args, removePGContainerEnvKey+"=flux-panel-postgres") {
		t.Fatalf("args must carry the legacy PG removal target: %v", args)
	}
	if !hasEnvArg(args, pgSnapshotDeferredEnvKey+"=1") {
		t.Fatalf("args must carry the PG snapshot deferral flag: %v", args)
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, upgradeServicesEnvKey+"=") || strings.HasPrefix(arg, snapshotDirEnvKey+"=") {
			t.Fatalf("defaulted fields must not be passed to the helper: %v", args)
		}
	}
}

func TestHelperScriptRenameSnapshotStructure(t *testing.T) {
	executor := &systemUpgradeExecutor{deployDir: "/opt/flmx-panel", backendContainer: "flmx-panel-backend"}
	script := executor.helperScript()
	for _, needle := range []string{
		`UPGRADE_SERVICES="${FLMX_UPGRADE_SERVICES:-backend frontend}"`,
		"docker compose pull $UPGRADE_SERVICES",
		"snapshotPath",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("helper script missing %q", needle)
		}
	}
	if ups := strings.Count(script, "docker compose up -d --force-recreate --remove-orphans $UPGRADE_SERVICES"); ups < 2 {
		t.Fatalf("main up and rollback up must both use the service set, found %d", ups)
	}
	// 快照段（停后端）在主 up 之前；PG 删除段在卷名校验之后、
	// success 回写之前。
	stopIdx := strings.Index(script, "docker compose stop backend")
	upIdx := strings.Index(script, `log "重启服务（force-recreate）..."`)
	gateIdx := strings.Index(script, "数据卷校验通过")
	removeIdx := strings.Index(script, removePGContainerEnvKey)
	successIdx := strings.Index(script, `write_status "false" "success"`)
	if stopIdx < 0 || upIdx < 0 || gateIdx < 0 || removeIdx < 0 || successIdx < 0 {
		t.Fatalf("segments missing: stop=%d up=%d gate=%d remove=%d success=%d", stopIdx, upIdx, gateIdx, removeIdx, successIdx)
	}
	if !(stopIdx < upIdx && gateIdx < removeIdx && removeIdx < successIdx) {
		t.Fatalf("ordering wrong: snapshot stop must precede recreate; PG removal must sit between the volume gate and the success write")
	}
}

// —— helper 行为：真跑脚本（stub docker/date/sleep） ——

// runHelperRenameSnapshot 沿 resource-pin 用例的桩形态真跑 helper，
// extraEnv 透传改名/快照安排，inspectVolume 为卷名校验的实读值。
func runHelperRenameSnapshot(t *testing.T, extraEnv map[string]string, inspectVolume string, setup func(deployDir string)) (string, string, string, error) {
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
	write(filepath.Join(deployDir, ".env"), "FLMX_VERSION=1.9.9\n", 0o600)
	write(filepath.Join(deployDir, ".env.upgrade.bak"), "FLMX_VERSION=1.9.8\n", 0o600)
	write(counterPath, "1000", 0o644)
	if setup != nil {
		setup(deployDir)
	}

	dockerStub := `#!/bin/sh
echo "$@" >> "` + callsPath + `"
case "$*" in
  "compose pull"*) exit 0 ;;
  "compose stop"*) exit 0 ;;
  "compose up"*) exit 0 ;;
  "compose ps"*)
    case "$*" in
      *State*) echo "running" ;;
      *Name*) echo "backend-1" ;;
    esac
    exit 0 ;;
  "exec"*) exit 0 ;;
  "inspect"*)
    case "$*" in
      *"/app/data"*) echo "` + inspectVolume + `" ;;
      *) exit 0 ;;
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

	env := append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PANEL_DEPLOY_DIR="+deployDir,
		"FLMX_UPGRADE_VERSION=1.9.9",
		"FLMX_UPGRADE_STARTED_AT=2026-01-01T00:00:00Z",
	)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = env
	out, runErr := cmd.CombinedOutput()

	status, err := os.ReadFile(filepath.Join(deployDir, ".flmx_upgrade_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(status) error = %v, helper output:\n%s", err, out)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile(docker calls) error = %v", err)
	}
	return string(status), string(calls), string(out), runErr
}

func TestHelperSnapshotContentsAndRotation(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "gost.db"), []byte("db-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(gost.db) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "gost.db-wal"), []byte("wal-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(gost.db-wal) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "github-cache"), 0o755); err != nil {
		t.Fatalf("MkdirAll(github-cache) error = %v", err)
	}
	snapshotRel := "backups/panel-update-1.9.9-20260101-000000"
	var deployDirSeen string
	setup := func(deployDir string) {
		deployDirSeen = deployDir
		backups := filepath.Join(deployDir, "backups")
		base := time.Now().Add(-4 * time.Hour)
		for i, name := range []string{"panel-update-old-a", "panel-update-old-b", "panel-update-old-c"} {
			dir := filepath.Join(backups, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("MkdirAll(old backup) error = %v", err)
			}
			mtime := base.Add(time.Duration(i) * time.Hour)
			if err := os.Chtimes(dir, mtime, mtime); err != nil {
				t.Fatalf("Chtimes(old backup) error = %v", err)
			}
		}
	}
	status, calls, out, runErr := runHelperRenameSnapshot(t, map[string]string{
		snapshotDirEnvKey:    snapshotRel,
		upgradeDataDirEnvKey: dataDir,
	}, "", setup)
	if runErr != nil {
		t.Fatalf("helper must succeed, err = %v, output:\n%s", runErr, out)
	}
	if !strings.Contains(status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", status)
	}
	if !strings.Contains(status, `"snapshotPath": "`+snapshotRel+`"`) {
		t.Fatalf("status must name the snapshot path, got %s", status)
	}
	if !strings.Contains(calls, "compose stop backend") {
		t.Fatalf("backend must be stopped before the snapshot copy; calls:\n%s", calls)
	}
	snapDB := filepath.Join(deployDirSeen, snapshotRel, "sqlite", "gost.db")
	content, err := os.ReadFile(snapDB)
	if err != nil {
		t.Fatalf("snapshot gost.db missing: %v", err)
	}
	if string(content) != "db-content" {
		t.Fatalf("snapshot gost.db = %q, want copied content", content)
	}
	if _, err := os.Stat(filepath.Join(deployDirSeen, snapshotRel, "sqlite", "gost.db-wal")); err != nil {
		t.Fatalf("snapshot must include the WAL sidecar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deployDirSeen, snapshotRel, "sqlite", "github-cache")); !os.IsNotExist(err) {
		t.Fatalf("github-cache must be excluded from the snapshot, stat err = %v", err)
	}
	// 轮换 K=3：新快照 + 较新的两份旧备份保留、最旧一份删除。
	entries, err := os.ReadDir(filepath.Join(deployDirSeen, "backups"))
	if err != nil {
		t.Fatalf("ReadDir(backups) error = %v", err)
	}
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("backups must retain exactly 3 panel-update dirs, got %v", names)
	}
	if _, err := os.Stat(filepath.Join(deployDirSeen, "backups", "panel-update-old-a")); !os.IsNotExist(err) {
		t.Fatalf("oldest backup must be rotated out, stat err = %v", err)
	}
}

func TestHelperSnapshotFailureAbortsUpgrade(t *testing.T) {
	status, calls, _, runErr := runHelperRenameSnapshot(t, map[string]string{
		snapshotDirEnvKey:    "backups/panel-update-1.9.9-20260101-000000",
		upgradeDataDirEnvKey: filepath.Join(t.TempDir(), "does-not-exist"),
	}, "", nil)
	if runErr == nil {
		t.Fatal("helper must exit non-zero when the DB snapshot fails")
	}
	if !strings.Contains(status, `"result": "rolled_back"`) {
		t.Fatalf("status = %s, want rolled_back after snapshot failure", status)
	}
	if !strings.Contains(calls, "compose stop backend") {
		t.Fatalf("snapshot attempt must stop the backend first; calls:\n%s", calls)
	}
	// 中止后不得继续走主 up 创建新容器：全文只有回滚的一次 up。
	if ups := strings.Count(calls, "compose up"); ups != 1 {
		t.Fatalf("only the rollback recreate may run after a snapshot failure, got %d ups:\n%s", ups, calls)
	}
}

func TestHelperRemovesLegacyPGContainerOnSuccess(t *testing.T) {
	status, calls, _, runErr := runHelperRenameSnapshot(t, map[string]string{
		removePGContainerEnvKey:    "flux-panel-postgres",
		expectedSQLiteVolumeEnvKey: "sqlite_data",
	}, "sqlite_data", nil)
	if runErr != nil {
		t.Fatalf("helper must succeed, err = %v", runErr)
	}
	if !strings.Contains(status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", status)
	}
	if !strings.Contains(calls, "rm -f flux-panel-postgres") {
		t.Fatalf("legacy PG container must be removed before the success write; calls:\n%s", calls)
	}
}

func TestHelperPostgresSnapshotDeferredIsNamed(t *testing.T) {
	var deployDirSeen string
	status, _, _, runErr := runHelperRenameSnapshot(t, map[string]string{
		pgSnapshotDeferredEnvKey: "1",
	}, "", func(deployDir string) { deployDirSeen = deployDir })
	if runErr != nil {
		t.Fatalf("helper must succeed with the deferral flag, err = %v", runErr)
	}
	if strings.Contains(status, "snapshotPath") {
		t.Fatalf("deferred PG snapshot must not claim a snapshot path: %s", status)
	}
	logData, err := os.ReadFile(filepath.Join(deployDirSeen, "upgrade.log"))
	if err != nil {
		t.Fatalf("ReadFile(upgrade.log) error = %v", err)
	}
	if !strings.Contains(string(logData), "DB 快照未随面板内升级落地（切出登记项）") {
		t.Fatalf("helper log must name the deferred PG snapshot, got:\n%s", logData)
	}
}
