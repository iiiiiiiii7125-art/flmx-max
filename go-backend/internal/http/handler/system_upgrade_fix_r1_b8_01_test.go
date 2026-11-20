package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// R18-R2-B8-01：PG 模式升级在网络标签对账停服后遇 pg_dump 失败。
// 根因链：reconcile_network_label 在标签不符且仅自家容器时先
// `docker compose stop`（无服务参数＝停全部服务，含 postgres），
// 其后的 pg_dump 段对已停容器 docker exec 必然失败，走
// `rollback files` 退出——只恢复文件、不重建容器，面板留在全停
// 状态，状态却写 rolled_back，中止日志还称“面板保持旧版本可用”。
// 修法（A-7 选项 a）：PG 快照（只读 pg_dump）移到对账停服之前，
// 对账仍前置于一切会动服务的动作（SQLite 快照停后端、重建）。
//
// 本文件以整 helperScript() 真跑钉主流程级行为，桩 docker 带停服
// 状态：无参 `compose stop`（对账的停齐）置位 pg-stopped，其后
// pg_dump 恒失败；`compose up` 清位。组合：PG 模式＋网络标签不符
// （存量标签 flmx-network ≠ 网络键名 gost-network，仅自家容器）。

// runHelperR1B801 真跑 helperScript，PG 模式＋标签不符。pgDumpFail
// 为真时 pg_dump 无条件失败（独立故障，模拟坏凭据/坏库名等）；为
// 假时 pg_dump 仅在 PG 已停时失败（复现 R18-R2-B8-01 的根因链）。
func runHelperR1B801(t *testing.T, pgDumpFail bool) (status, calls, logText, deployDir string, runErr error) {
	t.Helper()
	deployDir = t.TempDir()
	dataDir := t.TempDir()
	stubDir := t.TempDir()
	callsPath := filepath.Join(stubDir, "docker-calls.log")
	counterPath := filepath.Join(stubDir, "date-counter")
	stoppedPath := filepath.Join(stubDir, "pg-stopped")
	pgDumpFailPath := filepath.Join(stubDir, "pgdump-fail")

	write := func(path, content string, perm os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), perm); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	write(filepath.Join(deployDir, "docker-compose.yml"), netlabelFlowCompose, 0o644)
	write(filepath.Join(deployDir, ".env"), "FLMX_NETWORK=flmx-network\nFLMX_VERSION=1.9.9\n", 0o600)
	write(filepath.Join(deployDir, "docker-compose.yml.upgrade.bak"), "old-compose\n", 0o644)
	write(filepath.Join(deployDir, ".env.upgrade.bak"), "FLMX_VERSION=1.9.8\n", 0o600)
	write(filepath.Join(dataDir, "gost.db"), "db-content", 0o600)
	write(counterPath, "1000", 0o644)
	if pgDumpFail {
		write(pgDumpFailPath, "", 0o644)
	}

	dockerStub := `#!/bin/sh
echo "$@" >> "` + callsPath + `"
case "$*" in
  "compose pull"*) exit 0 ;;
  "network inspect"*)
    case "$*" in
      *json*) printf '{"com.docker.compose.network":"%s"}' "${NET_LABEL:-}" ;;
      *"range"*) for c in ${NET_CONTAINERS:-}; do printf '%s ' "$c"; done ;;
      *) exit ${NET_EXISTS:-0} ;;
    esac
    exit 0 ;;
  "network rm"*) exit 0 ;;
  "compose stop") touch "` + stoppedPath + `"; exit 0 ;;
  "compose stop"*) exit 0 ;;
  "compose up"*) rm -f "` + stoppedPath + `"; exit 0 ;;
  "compose config"*)
    for s in ${COMPOSE_SERVICES:-backend frontend}; do printf '%s\n' "$s"; done
    exit 0 ;;
  "compose ps"*)
    case "$*" in
      *State*) echo "running" ;;
      *postgres*) echo "flmx-panel-postgres" ;;
      "compose ps --format"*)
        for c in ${PROJECT_CONTAINERS:-}; do printf '%s\n' "$c"; done ;;
      "compose ps backend"*) echo "flmx-panel-backend" ;;
      "compose ps frontend"*) echo "flmx-panel-frontend" ;;
      *) echo "flmx-panel-backend" ;;
    esac
    exit 0 ;;
  "exec"*)
    case "$*" in
      *pg_dump*)
        if [ -f "` + stoppedPath + `" ]; then echo "pg_dump: container is stopped" >&2; exit 1; fi
        if [ -f "` + pgDumpFailPath + `" ]; then echo "pg_dump: error" >&2; exit 1; fi
        printf '%s\n' '--' '-- PostgreSQL database dump' '--' 'CREATE TABLE t (id integer);'
        exit 0 ;;
      *pg_isready*) exit 0 ;;
      *psql*) exit 0 ;;
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
		"NET_LABEL=flmx-network",
		"NET_CONTAINERS=flmx-panel-backend flmx-panel-frontend flmx-panel-postgres",
		"PROJECT_CONTAINERS=flmx-panel-backend flmx-panel-frontend flmx-panel-postgres",
		"NET_EXISTS=0",
		"LANDED_IMAGE=ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:1.9.9",
		snapshotDirEnvKey+"="+fixD5PGSnapshotRelDir,
		upgradeDataDirEnvKey+"="+dataDir,
		fixD5PGContainerEnvKey+"=flmx-panel-postgres",
		fixD5PGUserEnvKey+"=flmx_panel",
		fixD5PGDBEnvKey+"=flmx_panel",
	)
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

// callsLine 返回 calls 日志中与 line 完全相等的一行的下标，未找到为 -1。
func callsLineIndex(calls, line string) int {
	for i, l := range strings.Split(strings.TrimSuffix(calls, "\n"), "\n") {
		if l == line {
			return i
		}
	}
	return -1
}

func TestR18R2B801PgSnapshotBeforeReconcileStop(t *testing.T) {
	// PG 模式＋标签不符：对账会停齐服务删网；pg_dump 必须在其前对
	// 在跑 PG 容器成功，升级照常成功、快照落盘。旧代码下对账先停
	// PG，pg_dump 必败，走 rollback files 以 rolled_back 退出。
	status, calls, logText, deployDir, runErr := runHelperR1B801(t, false)
	if runErr != nil {
		t.Fatalf("helper must succeed, err = %v, log:\n%s", runErr, logText)
	}
	if !strings.Contains(status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", status)
	}
	dumpPath := filepath.Join(deployDir, fixD5PGSnapshotRelDir, "postgres", "postgres.sql")
	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("PG snapshot dump missing: %v", err)
	}
	if !strings.Contains(string(dump), "PostgreSQL database dump") {
		t.Fatalf("PG snapshot dump lacks the dump header, got %q", dump)
	}
	// 顺序钉住：pg_dump 在对账的无参停服之前（按行号比对）。
	pgDumpLine := -1
	for i, l := range strings.Split(strings.TrimSuffix(calls, "\n"), "\n") {
		if strings.Contains(l, "pg_dump") {
			pgDumpLine = i
			break
		}
	}
	stopLine := callsLineIndex(calls, "compose stop")
	if pgDumpLine < 0 {
		t.Fatalf("pg_dump must run; calls:\n%s", calls)
	}
	if stopLine < 0 {
		t.Fatalf("reconcile must stop all services on label mismatch; calls:\n%s", calls)
	}
	if pgDumpLine >= stopLine {
		t.Fatalf("pg_dump (line %d) must precede the reconcile stop (line %d); calls:\n%s", pgDumpLine, stopLine, calls)
	}
	// 对账修复动作与主重建都在场。
	for _, want := range []string{"network rm flmx-network", "compose up -d --force-recreate"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls missing %q:\n%s", want, calls)
		}
	}
	for _, want := range []string{"PG 数据库快照完成", "已删除标签不符的存量网络"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log missing %q:\n%s", want, logText)
		}
	}
}

func TestR18R2B801PgDumpFailureAbortsBeforeAnyStop(t *testing.T) {
	// PG 模式＋标签不符＋pg_dump 独立失败：中止必须发生在对账停服
	// 之前——零停服、零重建，rolled_back 与“保持旧版本可用”才为真。
	// 旧代码下对账先停齐服务，中止时面板已全停、终态仍写 rolled_back。
	status, calls, logText, _, runErr := runHelperR1B801(t, true)
	if runErr == nil {
		t.Fatal("helper must exit non-zero when pg_dump fails")
	}
	if !strings.Contains(status, `"result": "rolled_back"`) {
		t.Fatalf("status = %s, want rolled_back after pg_dump failure", status)
	}
	if strings.Contains(status, "snapshotPath") {
		t.Fatalf("failed PG snapshot must not claim a snapshot path: %s", status)
	}
	// 服务分毫未动：零 stop（无参停齐与停后端均无）、零 up、零删网。
	for _, banned := range []string{"compose stop", "compose up", "network rm"} {
		if strings.Contains(calls, banned) {
			t.Fatalf("calls must not contain %q on pg_dump abort:\n%s", banned, calls)
		}
	}
	if !strings.Contains(calls, "pg_dump") {
		t.Fatalf("pg_dump must be attempted before the abort; calls:\n%s", calls)
	}
	if !strings.Contains(logText, "PG 数据库快照") || !strings.Contains(logText, "中止升级") {
		t.Fatalf("log must name the pg_dump failure abort, got:\n%s", logText)
	}
	if !strings.Contains(logText, "保持旧版本可用") {
		t.Fatalf("log must state the panel stays on the old version, got:\n%s", logText)
	}
}
