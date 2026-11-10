package handler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// NETLABEL-211 Go 半（R2-F1-01）：helperScript() 内嵌的四个 shell 函数
// 逐字节切片后在隔离 temp dir 下以桩 docker/date/sleep 实跑——与
// F1 独立 harness 同口径（桩接口与场景驱动 env 一致），但直接钉
// helperScript() 的当前文本，函数改动即被此测试覆盖。
//
// 桩 docker 由 env 驱动（与 F1 harness 同名）：
//   NET_NAME / NET_EXISTS / NET_LABEL / NET_CONTAINERS / RM_FAIL
//   PROJECT_CONTAINERS（docker compose ps --format '{{.Name}}' 输出）
//   PS_STATE / PS_NAME（docker compose ps <svc> --format 输出）
//   COMPOSE_SERVICES（docker compose config --services 输出）
//   PROBE_OK（docker exec 探针成败）/ LANDED_IMAGE（docker inspect）
// 桩 date：每次 +%s 调用前进 60s（假时钟加速 300s 截止/30s 稳定窗）；
// 桩 sleep：no-op。

const netlabelDockerStub = `#!/bin/bash
echo "docker $*" >> "$CALLS_FILE"
if [ "$1" = "network" ] && [ "$2" = "inspect" ]; then
  if [ "${3:-}" = "-f" ]; then fmt="$4"; name="${5:-}"; else fmt=""; name="${3:-}"; fi
  if [ "$name" != "${NET_NAME:-}" ]; then exit 1; fi
  if [ "${NET_EXISTS:-1}" != "1" ]; then exit 1; fi
  case "$fmt" in
    *com.docker.compose.network*) printf '%s' "${NET_LABEL:-}" ;;
    *Containers*) for c in ${NET_CONTAINERS:-}; do printf '%s\n' "$c"; done ;;
    *json*) printf '{"com.docker.compose.network":"%s"}' "${NET_LABEL:-}" ;;
    *'range $id'*) printf '%s ' ${NET_CONTAINERS:-} ;;
    *) echo '{}' ;;
  esac
  exit 0
fi
if [ "$1" = "network" ] && [ "$2" = "rm" ]; then
  if [ "${RM_FAIL:-0}" = "1" ]; then exit 1; fi
  exit 0
fi
if [ "$1" = "compose" ] && [ "$2" = "stop" ]; then exit 0; fi
if [ "$1" = "compose" ] && [ "$2" = "ps" ]; then
  if [ "${3:-}" = "--format" ]; then
    for c in ${PROJECT_CONTAINERS:-}; do printf '%s\n' "$c"; done
    exit 0
  fi
  case "${5:-}" in
    *State*) printf '%s\n' "${PS_STATE:-created}" ;;
    *Name*) printf '%s\n' "${PS_NAME:-flmx-panel-backend}" ;;
  esac
  exit 0
fi
if [ "$1" = "compose" ] && [ "$2" = "config" ]; then
  for s in ${COMPOSE_SERVICES:-backend frontend}; do printf '%s\n' "$s"; done
  exit 0
fi
if [ "$1" = "exec" ]; then
  if [ "${PROBE_OK:-1}" = "1" ]; then exit 0; else exit 1; fi
fi
if [ "$1" = "inspect" ]; then
  printf '%s\n' "${LANDED_IMAGE:-}"
  exit 0
fi
exit 0
`

const netlabelDateStub = `#!/bin/bash
state="$FAKE_CLOCK_FILE"
now=$(cat "$state")
if [ "$1" = "+%s" ]; then
  echo $((now + 60)) > "$state"
  echo "$now"
else
  exec /bin/date "$@"
fi
`

const netlabelSleepStub = `#!/bin/bash
exit 0
`

// extractHelperFunc 从 helperScript() 文本中按名切片一个 shell 函数：
// 函数体内的 `}` 均带缩进或在 ${...} 内，顶格的 `}` 行即函数结尾。
func extractHelperFunc(t *testing.T, script, name string) string {
	t.Helper()
	lines := strings.Split(script, "\n")
	start := -1
	for i, ln := range lines {
		if ln == name+"() {" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("helper function %s not found in helperScript()", name)
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "}" {
			end = i
			break
		}
	}
	if end < 0 {
		t.Fatalf("helper function %s has no closing brace", name)
	}
	return strings.Join(lines[start:end+1], "\n")
}

type netlabelCase struct {
	name      string
	fn        string
	compose   string // "" = 不写 docker-compose.yml
	env       string // "" = 不写 .env
	stubEnv   map[string]string
	wantRC    int
	wantCalls []string // 必须出现在 docker 调用记录里的子串
	noCalls   []string // 必须不出现的子串
	wantLog   []string // 必须出现在 upgrade.log 里的子串
}

func runNetlabelCase(t *testing.T, tc netlabelCase) {
	t.Helper()
	script := (&systemUpgradeExecutor{}).helperScript()
	fnText := extractHelperFunc(t, script, tc.fn)
	// 切片保真锚点：函数体关键行缺失即测试本身失效。
	anchors := map[string][]string{
		"reconcile_network_label": {"com.docker.compose.network", "docker compose stop", "docker network rm"},
		"wait_backend_healthy":    {"6365/flow/test"},
		"wait_frontend_healthy":   {"compose config --services"},
		"verify_landed_version":   {"FLMX_UPGRADE_VERSION", "Config.Image"},
	}
	for _, a := range anchors[tc.fn] {
		if !strings.Contains(fnText, a) {
			t.Fatalf("extracted %s missing anchor %q", tc.fn, a)
		}
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub := func(name, content string) {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("docker", netlabelDockerStub)
	writeStub("date", netlabelDateStub)
	writeStub("sleep", netlabelSleepStub)
	if tc.compose != "" {
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(tc.compose), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if tc.env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.env), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	callsFile := filepath.Join(dir, "calls")
	clockFile := filepath.Join(dir, "clock")
	if err := os.WriteFile(callsFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clockFile, []byte("1000000"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "upgrade.log")
	rcFile := filepath.Join(dir, "rc")
	runner := "#!/bin/bash\n" +
		"export PATH=" + binDir + ":$PATH\n" +
		"export LOGFILE=" + logFile + "\n" +
		": > \"$LOGFILE\"\n" +
		"log() { echo \"$*\" >> \"$LOGFILE\"; }\n" +
		fnText + "\n" +
		tc.fn + "\n" +
		"echo $? > " + rcFile + "\n"
	runnerPath := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(runnerPath, []byte(runner), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", runnerPath)
	cmd.Dir = dir
	env := append(os.Environ(),
		"CALLS_FILE="+callsFile,
		"FAKE_CLOCK_FILE="+clockFile,
	)
	for k, v := range tc.stubEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runner failed: %v output=%s", err, out)
	}
	rcBytes, err := os.ReadFile(rcFile)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	rc := strings.TrimSpace(string(rcBytes))
	if rc != itoa(int64(tc.wantRC)) {
		t.Fatalf("%s: rc = %s, want %d (calls=%s log=%s)", tc.name, rc, tc.wantRC, readNetlabelFile(t, callsFile), readNetlabelFile(t, logFile))
	}
	calls := readNetlabelFile(t, callsFile)
	for _, want := range tc.wantCalls {
		if !strings.Contains(calls, want) {
			t.Fatalf("%s: calls missing %q:\n%s", tc.name, want, calls)
		}
	}
	for _, no := range tc.noCalls {
		if strings.Contains(calls, no) {
			t.Fatalf("%s: calls must not contain %q:\n%s", tc.name, no, calls)
		}
	}
	logText := readNetlabelFile(t, logFile)
	for _, want := range tc.wantLog {
		if !strings.Contains(logText, want) {
			t.Fatalf("%s: log missing %q:\n%s", tc.name, want, logText)
		}
	}
}

func readNetlabelFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	return string(b)
}

const netlabelComposeNew = `networks:
  gost-network:
    name: ${FLMX_NETWORK:-flmx-network}
    driver: bridge
`

const netlabelComposeOld = `networks:
  gost-network:
    driver: bridge
`

func TestNetlabelReconcileGo(t *testing.T) {
	cases := []netlabelCase{
		{
			name:    "label-match no-op",
			fn:      "reconcile_network_label",
			compose: netlabelComposeNew,
			env:     "FLMX_NETWORK=flmx-network\n",
			stubEnv: map[string]string{"NET_NAME": "flmx-network", "NET_EXISTS": "1", "NET_LABEL": "gost-network", "NET_CONTAINERS": ""},
			wantRC:  0,
			noCalls: []string{"compose stop", "network rm"},
		},
		{
			name:      "mismatch empty net stop+rm",
			fn:        "reconcile_network_label",
			compose:   netlabelComposeNew,
			env:       "FLMX_NETWORK=flmx-network\n",
			stubEnv:   map[string]string{"NET_NAME": "flmx-network", "NET_EXISTS": "1", "NET_LABEL": "flmx-network", "NET_CONTAINERS": ""},
			wantRC:    0,
			wantCalls: []string{"compose stop", "network rm flmx-network"},
			wantLog:   []string{"已删除标签不符的存量网络"},
		},
		{
			name:      "mismatch foreign container rc1 no stop",
			fn:        "reconcile_network_label",
			compose:   netlabelComposeNew,
			env:       "FLMX_NETWORK=flmx-network\n",
			stubEnv:   map[string]string{"NET_NAME": "flmx-network", "NET_EXISTS": "1", "NET_LABEL": "flmx-network", "NET_CONTAINERS": "other-app", "PROJECT_CONTAINERS": "flmx-panel-backend flmx-panel-frontend", "RM_FAIL": "1"},
			wantRC:    1,
			noCalls:   []string{"compose stop", "network rm"},
			wantLog:   []string{"other-app", "拒绝删除以免停服"},
		},
		{
			name:      "old install no FLMX_NETWORK derives gost-network",
			fn:        "reconcile_network_label",
			compose:   netlabelComposeOld,
			stubEnv:   map[string]string{"NET_NAME": "gost-network", "NET_EXISTS": "1", "NET_LABEL": "flmx-network", "NET_CONTAINERS": ""},
			wantRC:    0,
			wantCalls: []string{"network inspect gost-network", "compose stop", "network rm gost-network"},
			wantLog:   []string{"已删除标签不符的存量网络"},
		},
		{
			name:      "new template no .env derives flmx-network default",
			fn:        "reconcile_network_label",
			compose:   netlabelComposeNew,
			stubEnv:   map[string]string{"NET_NAME": "flmx-network", "NET_EXISTS": "1", "NET_LABEL": "flmx-network", "NET_CONTAINERS": ""},
			wantRC:    0,
			wantCalls: []string{"network inspect flmx-network", "network rm flmx-network"},
		},
		{
			name:    "network missing no-op",
			fn:      "reconcile_network_label",
			compose: netlabelComposeNew,
			env:     "FLMX_NETWORK=flmx-network\n",
			stubEnv: map[string]string{"NET_NAME": "flmx-network", "NET_EXISTS": "0"},
			wantRC:  0,
			noCalls: []string{"compose stop", "network rm"},
		},
		{
			name:      "mismatch own containers only stop+rm",
			fn:        "reconcile_network_label",
			compose:   netlabelComposeNew,
			env:       "FLMX_NETWORK=flmx-network\n",
			stubEnv:   map[string]string{"NET_NAME": "flmx-network", "NET_EXISTS": "1", "NET_LABEL": "flmx-network", "NET_CONTAINERS": "flmx-panel-backend flmx-panel-frontend", "PROJECT_CONTAINERS": "flmx-panel-backend flmx-panel-frontend"},
			wantRC:    0,
			wantCalls: []string{"compose stop", "network rm flmx-network"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runNetlabelCase(t, tc) })
	}
}

func TestNetlabelVerifyLandedVersionGo(t *testing.T) {
	cases := []netlabelCase{
		{
			name:    "empty target rc1",
			fn:      "verify_landed_version",
			stubEnv: map[string]string{"FLMX_UPGRADE_VERSION": ""},
			wantRC:  1,
			wantLog: []string{"目标版本为空"},
		},
		{
			name:      "tag matches rc0",
			fn:        "verify_landed_version",
			stubEnv:   map[string]string{"FLMX_UPGRADE_VERSION": "2.1.3", "PS_NAME": "flmx-panel-backend", "LANDED_IMAGE": "ghcr.io/iiiiiiiii7125-art/flmx-max-panel:2.1.3@sha256:abc"},
			wantRC:    0,
			wantCalls: []string{"compose ps backend"},
			wantLog:   []string{"落地版本校验通过"},
		},
		{
			name:    "tag mismatch rc1",
			fn:      "verify_landed_version",
			stubEnv: map[string]string{"FLMX_UPGRADE_VERSION": "2.1.3", "PS_NAME": "flmx-panel-backend", "LANDED_IMAGE": "ghcr.io/iiiiiiiii7125-art/flmx-max-panel:2.1.2@sha256:abc"},
			wantRC:  1,
			wantLog: []string{"与目标版本「2.1.3」不一致"},
		},
		{
			name:    "unparsable image rc1",
			fn:      "verify_landed_version",
			stubEnv: map[string]string{"FLMX_UPGRADE_VERSION": "2.1.3", "PS_NAME": "flmx-panel-backend", "LANDED_IMAGE": "ghcr.io/iiiiiiiii7125-art/flmx-max-panel"},
			wantRC:  1,
			wantLog: []string{"无法实读新后端镜像 tag"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runNetlabelCase(t, tc) })
	}
}

func TestNetlabelWaitHealthyGo(t *testing.T) {
	cases := []netlabelCase{
		{
			name:      "backend running+probe ok rc0",
			fn:        "wait_backend_healthy",
			stubEnv:   map[string]string{"PS_STATE": "running", "PS_NAME": "flmx-panel-backend", "PROBE_OK": "1"},
			wantRC:    0,
			wantCalls: []string{"compose ps backend"},
			wantLog:   []string{"新后端健康检查通过"},
		},
		{
			name:    "backend never running rc1",
			fn:      "wait_backend_healthy",
			stubEnv: map[string]string{"PS_STATE": "created", "PS_NAME": "flmx-panel-backend", "PROBE_OK": "1"},
			wantRC:  1,
			wantLog: []string{"后端健康检查超时"},
		},
		{
			name:      "frontend service absent skip rc0",
			fn:        "wait_frontend_healthy",
			stubEnv:   map[string]string{"COMPOSE_SERVICES": "backend"},
			wantRC:    0,
			wantCalls: []string{"compose config --services"},
			wantLog:   []string{"跳过前端健康检查"},
		},
		{
			name:      "frontend running+probe ok rc0",
			fn:        "wait_frontend_healthy",
			stubEnv:   map[string]string{"COMPOSE_SERVICES": "backend frontend", "PS_STATE": "running", "PS_NAME": "flmx-panel-frontend", "PROBE_OK": "1"},
			wantRC:    0,
			wantLog:   []string{"新前端健康检查通过"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runNetlabelCase(t, tc) })
	}
}
