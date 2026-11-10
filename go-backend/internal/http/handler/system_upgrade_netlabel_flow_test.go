package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// R3-SCRIPT-01：NETLABEL 对账的拒绝结论必须被主流程消费。本文件以
// 整 helperScript() 真跑钉主流程级行为（桩 docker/date/sleep 与
// rename-snapshot 用例同形态）：
//   - 前置拒绝（存量网络标签不符＋项目外容器占用）→ 升级在任何动
//     服务动作之前中止：回滚终态落盘、日志点名拒绝原因与占用者、
//     零 compose stop/up、零 network rm、文件恢复为升级前版本；
//     快照已配置时同样先于快照停后端中止（快照目录不创建）。
//   - 标签相符 → 成功路径回归：对账无操作、重建与健康检查照常、
//     success 终态、.bak 清理。
//   - 标签不符但仅自家容器 → 对账就地停服删网后升级照常成功（对
//     账前移不得误伤修复路径）。
//
// 桩 docker 由 env 驱动：NET_LABEL（存量网络的 compose 标签）、
// NET_CONTAINERS（网络在册容器，空格分隔）、PROJECT_CONTAINERS
// （docker compose ps 项目容器名，空格分隔）、LANDED_IMAGE。

const netlabelFlowCompose = `services:
  backend:
    image: ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:1.9.9
networks:
  gost-network:
    name: ${FLMX_NETWORK:-flmx-network}
    driver: bridge
`

type netlabelFlowResult struct {
	status   string
	calls    string
	log      string
	deploy   string
	runErr   error
	compose  string
	envAfter string
}

func runHelperNetlabelFlow(t *testing.T, stubEnv, extraEnv map[string]string) netlabelFlowResult {
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
	write(filepath.Join(deployDir, "docker-compose.yml"), netlabelFlowCompose, 0o644)
	write(filepath.Join(deployDir, "docker-compose.yml.upgrade.bak"), "old-compose\n", 0o644)
	write(filepath.Join(deployDir, ".env"), "FLMX_NETWORK=flmx-network\nFLMX_VERSION=1.9.9\n", 0o600)
	write(filepath.Join(deployDir, ".env.upgrade.bak"), "FLMX_VERSION=1.9.8\n", 0o600)
	write(counterPath, "1000", 0o644)

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
  "compose stop"*) exit 0 ;;
  "compose up"*) exit 0 ;;
  "compose config"*)
    for s in ${COMPOSE_SERVICES:-backend frontend}; do printf '%s\n' "$s"; done
    exit 0 ;;
  "compose ps"*)
    case "$*" in
      *State*) echo "running" ;;
      "compose ps --format"*)
        for c in ${PROJECT_CONTAINERS:-}; do printf '%s\n' "$c"; done ;;
      "compose ps backend"*) echo "flmx-panel-backend" ;;
      "compose ps frontend"*) echo "flmx-panel-frontend" ;;
      *) echo "flmx-panel-backend" ;;
    esac
    exit 0 ;;
  "exec"*) exit 0 ;;
  "image inspect"*) exit 1 ;;
  "inspect"*)
    case "$*" in
      *"Config.Image"*) echo "${LANDED_IMAGE:-}" ;;
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
		"LANDED_IMAGE=ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:1.9.9",
	)
	for k, v := range stubEnv {
		env = append(env, k+"="+v)
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = env
	out, runErr := cmd.CombinedOutput()

	res := netlabelFlowResult{deploy: deployDir, runErr: runErr}
	if b, err := os.ReadFile(filepath.Join(deployDir, ".flmx_upgrade_status.json")); err == nil {
		res.status = string(b)
	}
	if b, err := os.ReadFile(callsPath); err == nil {
		res.calls = string(b)
	}
	if b, err := os.ReadFile(filepath.Join(deployDir, "upgrade.log")); err == nil {
		res.log = string(b)
	}
	if b, err := os.ReadFile(filepath.Join(deployDir, "docker-compose.yml")); err == nil {
		res.compose = string(b)
	}
	if b, err := os.ReadFile(filepath.Join(deployDir, ".env")); err == nil {
		res.envAfter = string(b)
	}
	if res.status == "" || res.calls == "" {
		t.Fatalf("helper produced no status/calls (status=%q calls=%q err=%v), output:\n%s", res.status, res.calls, runErr, out)
	}
	return res
}

func TestHelperNetlabelRefusalAbortsUpgrade(t *testing.T) {
	// 快照同时配置：拒绝必须先于快照停后端发生——部署数据目录备齐，
	// 若流程错误地先走快照段，calls 里会出现 compose stop backend
	// 且快照目录会被创建。
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "gost.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshotRel := "backups/panel-update-1.9.9-20260101-000000"
	res := runHelperNetlabelFlow(t,
		map[string]string{
			"NET_LABEL":          "flmx-network", // 标签（实际名）≠ 网络键名 gost-network
			"NET_CONTAINERS":     "other-app",
			"PROJECT_CONTAINERS": "flmx-panel-backend flmx-panel-frontend",
			"NET_EXISTS":         "0",
		},
		map[string]string{
			snapshotDirEnvKey:    snapshotRel,
			upgradeDataDirEnvKey: dataDir,
		},
	)
	if res.runErr == nil {
		t.Fatal("helper must exit non-zero when the network-label guard refuses")
	}
	// 终态必须落盘且为回滚收口（非 success、非悬空 inProgress）。
	if !strings.Contains(res.status, `"inProgress": false`) || !strings.Contains(res.status, `"result": "rolled_back"`) {
		t.Fatalf("status = %s, want terminal rolled_back", res.status)
	}
	// 回执（upgrade.log）必须点名拒绝原因与占用者、给出人工处置方向。
	for _, want := range []string{"仍被项目外容器占用", "other-app", "拒绝删除以免停服", "升级中止"} {
		if !strings.Contains(res.log, want) {
			t.Fatalf("log missing %q:\n%s", want, res.log)
		}
	}
	// 服务分毫未动：零 stop、零 up、零 network rm；流程确已到达守卫
	// （pull 与 network inspect 在场）。
	for _, banned := range []string{"compose stop", "compose up", "network rm"} {
		if strings.Contains(res.calls, banned) {
			t.Fatalf("calls must not contain %q on refusal:\n%s", banned, res.calls)
		}
	}
	for _, want := range []string{"compose pull", "network inspect"} {
		if !strings.Contains(res.calls, want) {
			t.Fatalf("calls missing %q (flow must reach the guard):\n%s", want, res.calls)
		}
	}
	// 快照未做：快照目录不得被创建。
	if _, err := os.Stat(filepath.Join(res.deploy, snapshotRel)); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir must not be created on refusal, stat err = %v", err)
	}
	// 文件恢复为升级前版本（rollback files 语义）。
	if res.compose != "old-compose\n" {
		t.Fatalf("docker-compose.yml = %q, want restored pre-upgrade content", res.compose)
	}
	if res.envAfter != "FLMX_VERSION=1.9.8\n" {
		t.Fatalf(".env = %q, want restored pre-upgrade content", res.envAfter)
	}
}

func TestHelperNetlabelMatchSuccessRegression(t *testing.T) {
	res := runHelperNetlabelFlow(t,
		map[string]string{
			"NET_LABEL":          "gost-network", // 与网络键名相符
			"NET_CONTAINERS":     "flmx-panel-backend flmx-panel-frontend",
			"PROJECT_CONTAINERS": "flmx-panel-backend flmx-panel-frontend",
			"NET_EXISTS":         "0",
		},
		nil,
	)
	if res.runErr != nil {
		t.Fatalf("helper must succeed on label match, err = %v, log:\n%s", res.runErr, res.log)
	}
	if !strings.Contains(res.status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", res.status)
	}
	// 对账确实跑过（inspect 在场）但无操作：零 network rm；主 up 照常。
	if !strings.Contains(res.calls, "network inspect") {
		t.Fatalf("reconcile must run on the success path; calls:\n%s", res.calls)
	}
	if strings.Contains(res.calls, "network rm") {
		t.Fatalf("label match must not remove the network; calls:\n%s", res.calls)
	}
	if !strings.Contains(res.calls, "compose up -d --force-recreate") {
		t.Fatalf("main recreate missing; calls:\n%s", res.calls)
	}
	// 成功收尾清 .bak（既有行为不变）。
	if _, err := os.Stat(filepath.Join(res.deploy, ".env.upgrade.bak")); !os.IsNotExist(err) {
		t.Fatalf(".env.upgrade.bak must be cleaned on success, stat err = %v", err)
	}
}

func TestHelperNetlabelMismatchOwnContainersProceeds(t *testing.T) {
	res := runHelperNetlabelFlow(t,
		map[string]string{
			"NET_LABEL":          "flmx-network", // 不符
			"NET_CONTAINERS":     "flmx-panel-backend flmx-panel-frontend", // 仅自家容器
			"PROJECT_CONTAINERS": "flmx-panel-backend flmx-panel-frontend",
			"NET_EXISTS":         "0",
		},
		nil,
	)
	if res.runErr != nil {
		t.Fatalf("helper must succeed after reconcile fix, err = %v, log:\n%s", res.runErr, res.log)
	}
	if !strings.Contains(res.status, `"result": "success"`) {
		t.Fatalf("status = %s, want success", res.status)
	}
	// 对账修复动作（停服＋删网）在场，随后主 up 照常。
	for _, want := range []string{"compose stop", "network rm flmx-network", "compose up -d --force-recreate"} {
		if !strings.Contains(res.calls, want) {
			t.Fatalf("calls missing %q:\n%s", want, res.calls)
		}
	}
	if !strings.Contains(res.log, "已删除标签不符的存量网络") {
		t.Fatalf("log missing reconcile fix note:\n%s", res.log)
	}
}
