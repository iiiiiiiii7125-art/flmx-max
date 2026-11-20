package handler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// R18-B8-03 / D-5 (a)：PG 模式面板内升级的升级前 pg_dump 快照。
// 本文件只测 helper 脚本面（字符串断言 + 真跑脚本），Go 面决议
// （prepareUpgradeSnapshot）见同文件下半部分的 TestFixD5*Go 测试。
//
// 契约（与脚本面 backup_postgres_for_update 同口径）：
//   - helper 在停旧后端前对在跑 PG 容器做 pg_dump，只读导出，落
//     backups/panel-update-<版本>-<时间戳>/postgres/postgres.sql；
//   - pg_dump 失败（或转储为空/无头）即 fail-closed 中止升级，
//     未停任何服务、未重建任何容器，面板保持旧版本可用；
//   - 回滚重建旧容器前用该快照原地恢复数据库（DROP/CREATE +
//     psql 回灌）；恢复失败不得谎报 rolled_back，须以
//     rollback_failed 收口并点名人工按快照路径恢复；
//   - SQLite 面行为不变；快照只读导出，不动现库。

// Pass A 注：PG 透传键名是本修复与 helper 的契约名，Go 面落地后
// 由同名常量（pgSnapshotContainerEnvKey 等）承载；此处先以字面
// 量写死，使本文件在旧代码上即可编译并跑红。
const (
	fixD5PGContainerEnvKey = "FLMX_UPGRADE_PG_CONTAINER"
	fixD5PGUserEnvKey      = "FLMX_UPGRADE_PG_USER"
	fixD5PGDBEnvKey        = "FLMX_UPGRADE_PG_DB"
	fixD5PGSnapshotRelDir  = "backups/panel-update-1.9.9-20260101-000000"
)

func TestFixD5HelperScriptPgDumpSnapshot(t *testing.T) {
	script := (&systemUpgradeExecutor{}).helperScript()
	for _, needle := range []string{
		"pg_dump",
		fixD5PGContainerEnvKey,
		"postgres/postgres.sql",
		"DROP DATABASE",
		"CREATE DATABASE",
		"pg_restore_failed",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("helper script missing %q (R18-B8-03)", needle)
		}
	}
	// 快照须在停旧后端前：pg_dump 段先于 SQLite 快照段的停后端。
	pgDumpIdx := strings.Index(script, "pg_dump")
	stopIdx := strings.Index(script, "docker compose stop backend")
	if pgDumpIdx < 0 || stopIdx < 0 || pgDumpIdx >= stopIdx {
		t.Fatalf("pg_dump must precede the backend stop: pgDump=%d stop=%d", pgDumpIdx, stopIdx)
	}
	// pg_dump 失败不得续行：失败分支须走 files 回滚并退出。
	if !strings.Contains(script, "PG 数据库快照") {
		t.Fatal("helper script must name the PG snapshot step in its log")
	}
	// 回滚恢复只认本次落盘的快照，不认旧份。
	if !strings.Contains(script, "SNAPSHOT_TAKEN") {
		t.Fatal("helper rollback must gate the PG restore on SNAPSHOT_TAKEN")
	}
}

// —— helper 真跑：PG 感知 stub docker ——

// runHelperPgSnapshot 真跑 helperScript，stub docker 仿真 PG 容器：
//   - pg_dump 成功时向 stdout 打一段带标准头的假转储，失败标记
//     （pgDumpFail 文件在场）时 exit 1；
//   - psql 成功时 exit 0，失败标记（psqlFail 文件在场）时 exit 1；
//   - compose ps backend 的 State 在前 failState 次读到 exited，
//     之后 running（模拟首轮健康失败、回滚轮健康通过）；
//   - 其余动作（pull/up/stop/inspect/wget/pg_isready）均成功。
//
// dataDir 为 SQLite 快照段的数据源（旧代码下 PG 透传被忽略、会
// 走 SQLite 段；新代码下 PG 段优先、SQLite 段跳过）。
func runHelperPgSnapshot(t *testing.T, extraEnv map[string]string, dataDir string, failState int, pgDumpFail, psqlFail bool) (status, calls, logText, deployDir string, runErr error) {
	t.Helper()
	deployDir = t.TempDir()
	stubDir := t.TempDir()
	callsPath := filepath.Join(stubDir, "docker-calls.log")
	counterPath := filepath.Join(stubDir, "date-counter")
	psCounterPath := filepath.Join(stubDir, "ps-counter")
	pgDumpFailPath := filepath.Join(stubDir, "pgdump-fail")
	psqlFailPath := filepath.Join(stubDir, "psql-fail")

	write := func(path, content string, perm os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), perm); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	write(filepath.Join(deployDir, "docker-compose.yml"), "new-compose\n", 0o644)
	write(filepath.Join(deployDir, ".env"), "FLMX_VERSION=1.9.9\n", 0o600)
	write(filepath.Join(deployDir, "docker-compose.yml.upgrade.bak"), "old-compose\n", 0o644)
	write(filepath.Join(deployDir, ".env.upgrade.bak"), "FLMX_VERSION=1.9.8\n", 0o600)
	write(counterPath, "1000", 0o644)
	write(psCounterPath, "0", 0o644)
	if pgDumpFail {
		write(pgDumpFailPath, "", 0o644)
	}
	if psqlFail {
		write(psqlFailPath, "", 0o644)
	}

	dockerStub := `#!/bin/sh
echo "$@" >> "` + callsPath + `"
case "$*" in
  "compose pull"*) exit 0 ;;
  "compose stop"*) exit 0 ;;
  "compose up"*) exit 0 ;;
  "compose ps"*)
    case "$*" in
      *State*)
        n=$(cat "` + psCounterPath + `")
        n=$((n + 1))
        echo "$n" > "` + psCounterPath + `"
        if [ "$n" -le ` + strconv.Itoa(failState) + ` ]; then echo "exited"; else echo "running"; fi
        ;;
      *Name*)
        case "$*" in *postgres*) echo "flmx-panel-postgres" ;; *) echo "backend-1" ;; esac
        ;;
    esac
    exit 0 ;;
  "exec"*)
    case "$*" in
      *pg_dump*)
        if [ -f "` + pgDumpFailPath + `" ]; then echo "pg_dump: error" >&2; exit 1; fi
        printf '%s\n' '--' '-- PostgreSQL database dump' '--' 'CREATE TABLE t (id integer);'
        exit 0 ;;
      *pg_isready*) exit 0 ;;
      *psql*)
        if [ -f "` + psqlFailPath + `" ]; then echo "psql: error" >&2; exit 1; fi
        exit 0 ;;
      *wget*) exit 0 ;;
    esac
    exit 0 ;;
  "inspect"*)
    case "$*" in
      *"Config.Image"*) echo "ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:1.9.9" ;;
      *"/app/data"*) echo "" ;;
      *"/var/lib/postgresql/data"*) echo "" ;;
      *"State.Running"*) echo "true" ;;
      *) exit 0 ;;
    esac
    exit 0 ;;
  "image inspect"*) exit 0 ;;
  "rmi"*) exit 0 ;;
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

	raw, err := os.ReadFile(filepath.Join(deployDir, ".flmx_upgrade_status.json"))
	if err != nil {
		t.Fatalf("ReadFile(status) error = %v, helper output:\n%s", err, out)
	}
	callsRaw, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile(docker calls) error = %v", err)
	}
	calls = string(callsRaw)
	logData, err := os.ReadFile(filepath.Join(deployDir, "upgrade.log"))
	if err != nil {
		t.Fatalf("ReadFile(upgrade.log) error = %v", err)
	}
	return string(raw), string(calls), string(logData), deployDir, runErr
}

// pgSnapshotEnv 是 PG 快照段的透传环境（与 Go 面 prepareUpgradeSnapshot
// 落盘的安排同口径）；dataDir 供旧代码的 SQLite 段使用。
func pgSnapshotEnv(dataDir string) map[string]string {
	return map[string]string{
		snapshotDirEnvKey:      fixD5PGSnapshotRelDir,
		upgradeDataDirEnvKey:   dataDir,
		fixD5PGContainerEnvKey: "flmx-panel-postgres",
		fixD5PGUserEnvKey:      "flmx_panel",
		fixD5PGDBEnvKey:        "flmx_panel",
	}
}

func TestFixD5HelperPgSnapshotSuccess(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "gost.db"), []byte("db-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(gost.db) error = %v", err)
	}
	status, calls, logText, deployDir, runErr := runHelperPgSnapshot(t, pgSnapshotEnv(dataDir), dataDir, 0, false, false)
	if runErr != nil {
		t.Fatalf("helper must succeed, err = %v", runErr)
	}
	if !strings.Contains(status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", status)
	}
	if !strings.Contains(status, `"snapshotPath": "`+fixD5PGSnapshotRelDir+`"`) {
		t.Fatalf("status must name the PG snapshot path, got %s", status)
	}
	if !strings.Contains(calls, "exec flmx-panel-postgres pg_dump -U flmx_panel flmx_panel") {
		t.Fatalf("pg_dump must run against the PG container before the upgrade; calls:\n%s", calls)
	}
	// pg_dump 只读导出：快照在停旧后端前，成功路径全程不得显式停后端。
	if strings.Contains(calls, "compose stop backend") {
		t.Fatalf("PG snapshot must not stop the backend before pg_dump; calls:\n%s", calls)
	}
	if !strings.Contains(logText, "PG 数据库快照完成") {
		t.Fatalf("log must name the completed PG snapshot, got:\n%s", logText)
	}
	// 快照文件真实落盘且含标准转储头（不是空文件占位）。
	dumpPath := filepath.Join(deployDir, fixD5PGSnapshotRelDir, "postgres", "postgres.sql")
	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("PG snapshot dump missing: %v", err)
	}
	if !strings.Contains(string(dump), "PostgreSQL database dump") {
		t.Fatalf("PG snapshot dump lacks the dump header, got %q", dump)
	}
}

func TestFixD5HelperPgSnapshotFailureAbortsUpgrade(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "gost.db"), []byte("db-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(gost.db) error = %v", err)
	}
	status, calls, logText, _, runErr := runHelperPgSnapshot(t, pgSnapshotEnv(dataDir), dataDir, 0, true, false)
	if runErr == nil {
		t.Fatal("helper must exit non-zero when pg_dump fails")
	}
	if !strings.Contains(status, `"result": "rolled_back"`) {
		t.Fatalf("status = %s, want rolled_back after pg_dump failure", status)
	}
	if strings.Contains(status, "snapshotPath") {
		t.Fatalf("failed PG snapshot must not claim a snapshot path: %s", status)
	}
	// fail-closed：未停任何服务、未重建任何容器（零次 up）。
	if strings.Contains(calls, "compose stop backend") {
		t.Fatalf("pg_dump failure must not stop the backend; calls:\n%s", calls)
	}
	if ups := strings.Count(calls, "compose up"); ups != 0 {
		t.Fatalf("no container rebuild may run after a pg_dump failure, got %d ups:\n%s", ups, calls)
	}
	if !strings.Contains(logText, "PG 数据库快照") || !strings.Contains(logText, "中止升级") {
		t.Fatalf("log must name the pg_dump failure abort, got:\n%s", logText)
	}
	if !strings.Contains(logText, "保持旧版本可用") {
		t.Fatalf("log must state the panel stays on the old version, got:\n%s", logText)
	}
}

func TestFixD5HelperPgSnapshotRestoredOnRollback(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "gost.db"), []byte("db-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(gost.db) error = %v", err)
	}
	status, calls, logText, _, runErr := runHelperPgSnapshot(t, pgSnapshotEnv(dataDir), dataDir, 35, false, false)
	// 主流程健康失败后即使回滚成功 helper 仍 exit 1（与既有回滚
	// 语义一致，见 TestHelperSnapshotFailureAbortsUpgrade）。
	if runErr == nil {
		t.Fatal("helper must exit non-zero after the health failure that triggers the rollback")
	}
	if !strings.Contains(status, `"result": "rolled_back"`) {
		t.Fatalf("status = %s, want rolled_back after the health failure", status)
	}
	if !strings.Contains(status, `"snapshotPath": "`+fixD5PGSnapshotRelDir+`"`) {
		t.Fatalf("status must keep naming the snapshot path after rollback, got %s", status)
	}
	// 回滚须用快照原地恢复数据库：断开连接 → DROP → CREATE → 回灌。
	for _, needle := range []string{
		"pg_terminate_backend",
		`DROP DATABASE "flmx_panel"`,
		`CREATE DATABASE "flmx_panel"`,
		"psql -U flmx_panel -d flmx_panel",
	} {
		if !strings.Contains(calls, needle) {
			t.Fatalf("rollback must restore the PG snapshot (%q missing); calls:\n%s", needle, calls)
		}
	}
	if !strings.Contains(logText, "PG 数据库已从升级前快照恢复") {
		t.Fatalf("log must name the completed PG restore, got:\n%s", logText)
	}
}

func TestFixD5HelperPgRestoreFailureIsRollbackFailed(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "gost.db"), []byte("db-content"), 0o600); err != nil {
		t.Fatalf("WriteFile(gost.db) error = %v", err)
	}
	// 首轮健康失败进回滚、回滚里 psql 失败：重建成功但恢复失败。
	status, _, logText, _, runErr := runHelperPgSnapshot(t, pgSnapshotEnv(dataDir), dataDir, 35, false, true)
	if runErr == nil {
		t.Fatal("helper must exit non-zero after the health failure that triggers the rollback")
	}
	if !strings.Contains(status, `"result": "rollback_failed"`) {
		t.Fatalf("status = %s, want rollback_failed when the PG restore fails", status)
	}
	if !strings.Contains(logText, "PG 快照恢复失败") || !strings.Contains(logText, "人工恢复") {
		t.Fatalf("log must name the restore failure and the manual recovery pointer, got:\n%s", logText)
	}
}

// —— Go 面：快照身份定格 ——

func TestFixD5ResolvePgSnapshotIdentity(t *testing.T) {
	t.Run("env wins over inspected and default", func(t *testing.T) {
		id, err := resolvePgSnapshotIdentity(
			map[string]string{envKeyPostgresUser: "panel_user", envKeyPostgresDB: "panel_db"},
			"flmx-panel-postgres",
			map[string]string{envKeyPostgresUser: "inspected_user", envKeyPostgresDB: "inspected_db"})
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if id.container != "flmx-panel-postgres" || id.user != "panel_user" || id.db != "panel_db" {
			t.Fatalf("identity = %+v, want container/user/db from .env", id)
		}
	})
	t.Run("inspected env fills missing keys", func(t *testing.T) {
		id, err := resolvePgSnapshotIdentity(
			map[string]string{},
			"flmx-panel-postgres",
			map[string]string{envKeyPostgresUser: "init_user", envKeyPostgresDB: "init_db"})
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if id.user != "init_user" || id.db != "init_db" {
			t.Fatalf("identity = %+v, want inspected values", id)
		}
	})
	t.Run("missing everywhere falls back to the template default", func(t *testing.T) {
		id, err := resolvePgSnapshotIdentity(map[string]string{}, "flmx-panel-postgres", map[string]string{})
		if err != nil {
			t.Fatalf("resolve error = %v", err)
		}
		if id.user != "flmx_panel" || id.db != "flmx_panel" {
			t.Fatalf("identity = %+v, want flmx_panel defaults", id)
		}
	})
	t.Run("unlocatable container fails closed", func(t *testing.T) {
		if _, err := resolvePgSnapshotIdentity(map[string]string{}, "", map[string]string{}); err == nil {
			t.Fatal("unlocatable PG container must fail closed, got nil error")
		}
	})
	t.Run("unsafe db name fails closed", func(t *testing.T) {
		if _, err := resolvePgSnapshotIdentity(
			map[string]string{envKeyPostgresDB: `x"; DROP TABLE users; --`},
			"flmx-panel-postgres", map[string]string{}); err == nil {
			t.Fatal("unsafe DB name must fail closed, got nil error")
		}
	})
}

func TestFixD5PrepareUpgradeSnapshotPgNamesSnapshot(t *testing.T) {
	// fake docker 定位到在跑的旧名 PG 容器（改名场景：声明键缺失，
	// 定位须走项目标签回退、不得凭空取声明值）。
	writeFakeDocker(t, fakeDockerLocatePG)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_TYPE=postgres\nPOSTGRES_USER=panel_user\nPOSTGRES_DB=panel_db\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend", composeProject: "flmx-panel"}
	if err := executor.prepareUpgradeSnapshot(context.Background(), "1.9.9"); err != nil {
		t.Fatalf("prepareUpgradeSnapshot error = %v", err)
	}
	pattern := regexp.MustCompile(`^backups/panel-update-1\.9\.9-\d{8}-\d{6}$`)
	if !pattern.MatchString(executor.snapshotPath) {
		t.Fatalf("snapshotPath = %q, want backups/panel-update-1.9.9-<UTC timestamp>", executor.snapshotPath)
	}
	if executor.pgSnapshotContainer != "flux-panel-postgres" {
		t.Fatalf("pgSnapshotContainer = %q, want the located running container", executor.pgSnapshotContainer)
	}
	if executor.pgSnapshotUser != "panel_user" || executor.pgSnapshotDB != "panel_db" {
		t.Fatalf("pg identity = %q/%q, want .env values", executor.pgSnapshotUser, executor.pgSnapshotDB)
	}
}

func TestFixD5PrepareUpgradeSnapshotPgDefaultsIdentity(t *testing.T) {
	writeFakeDocker(t, fakeDockerLocatePG)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_TYPE=postgres\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend", composeProject: "flmx-panel"}
	if err := executor.prepareUpgradeSnapshot(context.Background(), "1.9.9"); err != nil {
		t.Fatalf("prepareUpgradeSnapshot error = %v", err)
	}
	if executor.snapshotPath == "" {
		t.Fatal("postgres mode must name a snapshot path")
	}
	if executor.pgSnapshotUser != "flmx_panel" || executor.pgSnapshotDB != "flmx_panel" {
		t.Fatalf("pg identity = %q/%q, want template defaults", executor.pgSnapshotUser, executor.pgSnapshotDB)
	}
}

func TestFixD5PrepareUpgradeSnapshotPgFailsClosedWithoutContainer(t *testing.T) {
	// 无 fake docker 且无声明键：定位不到在跑 PG 容器即中止，
	// 不得无快照放行升级（composeProject 为空时标签回退不启动，
	// 结论与宿主机有无 docker 无关）。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_TYPE=postgres\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	if err := executor.prepareUpgradeSnapshot(context.Background(), "1.9.9"); err == nil {
		t.Fatal("unlocatable PG container must fail closed, got nil error")
	}
	if executor.snapshotPath != "" {
		t.Fatalf("failed snapshot must not name a path, got %q", executor.snapshotPath)
	}
}

func TestFixD5PrepareUpgradeSnapshotSqliteUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("JWT_SECRET=x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	executor := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flmx-panel-backend"}
	if err := executor.prepareUpgradeSnapshot(context.Background(), "1.9.9"); err != nil {
		t.Fatalf("prepareUpgradeSnapshot error = %v", err)
	}
	pattern := regexp.MustCompile(`^backups/panel-update-1\.9\.9-\d{8}-\d{6}$`)
	if !pattern.MatchString(executor.snapshotPath) {
		t.Fatalf("snapshotPath = %q, want backups/panel-update-1.9.9-<UTC timestamp>", executor.snapshotPath)
	}
	if executor.pgSnapshotContainer != "" || executor.pgSnapshotUser != "" || executor.pgSnapshotDB != "" {
		t.Fatalf("sqlite mode must not carry PG snapshot identity: %q/%q/%q",
			executor.pgSnapshotContainer, executor.pgSnapshotUser, executor.pgSnapshotDB)
	}
}

func TestFixD5BuildHelperRunArgsCarryPgSnapshot(t *testing.T) {
	executor := &systemUpgradeExecutor{
		deployDir: "/opt/flmx-panel", backendContainer: "flmx-panel-backend",
		snapshotPath:        "backups/panel-update-1.9.9-20260101-000000",
		pgSnapshotContainer: "flmx-panel-postgres", pgSnapshotUser: "flmx_panel", pgSnapshotDB: "flmx_panel",
	}
	args, err := executor.buildHelperRunArgs("sha256:abc", "helper")
	if err != nil {
		t.Fatalf("buildHelperRunArgs error = %v", err)
	}
	for _, want := range []string{
		pgSnapshotContainerEnvKey + "=flmx-panel-postgres",
		pgSnapshotUserEnvKey + "=flmx_panel",
		pgSnapshotDBEnvKey + "=flmx_panel",
		snapshotDirEnvKey + "=backups/panel-update-1.9.9-20260101-000000",
	} {
		found := false
		for _, arg := range args {
			if arg == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("args must carry %q: %v", want, args)
		}
	}
	// SQLite 面（无 PG 身份）不得透传 PG 键。
	executor.pgSnapshotContainer, executor.pgSnapshotUser, executor.pgSnapshotDB = "", "", ""
	args, err = executor.buildHelperRunArgs("sha256:abc", "helper")
	if err != nil {
		t.Fatalf("buildHelperRunArgs error = %v", err)
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "FLMX_UPGRADE_PG_") {
			t.Fatalf("sqlite mode must not pass PG snapshot env: %v", args)
		}
	}
}

func TestFixD5HelperScriptNoLongerDefersPgSnapshot(t *testing.T) {
	script := (&systemUpgradeExecutor{}).helperScript()
	if strings.Contains(script, "FLMX_UPGRADE_PG_SNAPSHOT_DEFERRED") || strings.Contains(script, "pgSnapshotDeferred") {
		t.Fatal("helper script must not reference the retired PG snapshot deferral (R18-B8-03 lands the snapshot)")
	}
}
