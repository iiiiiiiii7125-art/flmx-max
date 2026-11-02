package handler

import (
	"strings"
	"testing"
)

// 节点容器安装形态（Docker / Docker Compose）：命令生成与模式归一。
// 口径：全部资源自有命名（容器 flmx-agent、数据目录 /etc/flmx_agent、
// Compose 落位 /opt/docker/flmx-agent），与脚本形态的 legacy 落位
// （/etc/flux_agent、flux_agent）零共用，保证多面板同机共存时不
// 接管、不误删；脚本形态的命令生成不受本组函数影响。
//
// 实例派生：面板实例键 FLMX_INSTANCE 非空时，容器名、宿主机数据
// 目录与 Compose 落位统一加后缀 -<实例键>，使同机的第二个面板不
// 覆写第一个面板的 agent config、不 rm 它的容器；为空（默认）时
// 生成的命令与定版字面量逐字一致（黄金用例钉死）。
//
// 挂载契约（C-F2-01）：后缀只落在宿主机一侧——卷的容器内挂载点
// 恒为镜像锚点 /etc/flmx_agent（Dockerfile WORKDIR、entrypoint
// 的 cd 目标、ENV FLMX_KERNEL_DIR 三处钉死），因为 agent 的全部
// 状态根（config.json、gost.json、seq/spool、内核根、调优根、
// 自卸载锚）都由容器内工作目录与该 ENV 派生。同路径挂载
// （-v /etc/flmx_agent-east:/etc/flmx_agent-east）会把卷挂到无人
// 查看的目录：agent 在镜像层的空 /etc/flmx_agent 里读不到
// config.json 而崩溃循环、状态根全落卷外。默认实例下宿主机目录
// 与锚点同值，两种写法生成的命令逐字一致。

func TestNormalizeNodeInstallMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", nodeInstallModeScript, true},
		{"script", nodeInstallModeScript, true},
		{"docker", nodeInstallModeDocker, true},
		{"docker-compose", nodeInstallModeDockerCompose, true},
		{"compose", "", false},
		{"podman", "", false},
		{"DOCKER", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeNodeInstallMode(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("normalizeNodeInstallMode(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAgentImageRef(t *testing.T) {
	got := agentImageRef("1.9.4")
	want := "ghcr.io/iiiiiiiii7125-art/flmx-agent:1.9.4"
	if got != want {
		t.Fatalf("agentImageRef = %q, want %q", got, want)
	}
}

func TestBuildNodeDockerInstallCommand(t *testing.T) {
	cmd := buildNodeDockerInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", false)
	for _, want := range []string{
		"mkdir -p /etc/flmx_agent",
		`(umask 077; printf '{\n  "addr": "%s",\n  "secret": "%s"\n}\n' 'wss://panel.example.com:443' 's3cr3t' > /etc/flmx_agent/config.json)`,
		"chmod 600 /etc/flmx_agent/config.json",
		"docker rm -f flmx-agent",
		"docker run -d --name flmx-agent --network host --cap-add NET_ADMIN --restart unless-stopped -v /etc/flmx_agent:/etc/flmx_agent ghcr.io/iiiiiiiii7125-art/flmx-agent:1.9.4",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("docker command missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "--privileged") {
		t.Fatalf("unprivileged command must not carry --privileged:\n%s", cmd)
	}
	// 共存红线：容器形态不得引用脚本形态的 legacy 落位/命名。
	for _, banned := range []string{"/etc/flux_agent", "flux_agent.service", "--name flux_agent"} {
		if strings.Contains(cmd, banned) {
			t.Fatalf("docker command references legacy resource %q:\n%s", banned, cmd)
		}
	}
}

func TestBuildNodeDockerInstallCommandPrivileged(t *testing.T) {
	cmd := buildNodeDockerInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", true)
	if !strings.Contains(cmd, "--cap-add NET_ADMIN --privileged --restart unless-stopped") {
		t.Fatalf("privileged docker command malformed:\n%s", cmd)
	}
}

func TestBuildNodeDockerComposeInstallCommand(t *testing.T) {
	cmd := buildNodeDockerComposeInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", false)
	for _, want := range []string{
		"mkdir -p /opt/docker/flmx-agent",
		"cat > /opt/docker/flmx-agent/docker-compose.yaml <<'FLMX_AGENT_COMPOSE_EOF' && docker compose -f /opt/docker/flmx-agent/docker-compose.yaml up -d\n",
		"    image: ghcr.io/iiiiiiiii7125-art/flmx-agent:1.9.4\n",
		"    container_name: flmx-agent\n",
		"    network_mode: host\n",
		"      - NET_ADMIN\n",
		"    restart: unless-stopped\n",
		"      - /etc/flmx_agent:/etc/flmx_agent\n",
		"FLMX_AGENT_COMPOSE_EOF\n",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("compose command missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "privileged: true") {
		t.Fatalf("unprivileged compose must not carry privileged:\n%s", cmd)
	}
	// compose 文件内不得出现 secret（secret 只落数据目录 config.json）。
	yamlStart := strings.Index(cmd, "services:")
	yamlEnd := strings.Index(cmd, "FLMX_AGENT_COMPOSE_EOF\n")
	if yamlStart < 0 || yamlEnd < 0 || strings.Contains(cmd[yamlStart:yamlEnd], "s3cr3t") {
		t.Fatalf("compose yaml must not embed the node secret:\n%s", cmd)
	}
	for _, banned := range []string{"/etc/flux_agent", "--name flux_agent"} {
		if strings.Contains(cmd, banned) {
			t.Fatalf("compose command references legacy resource %q:\n%s", banned, cmd)
		}
	}
}

func TestBuildNodeDockerComposeInstallCommandPrivileged(t *testing.T) {
	cmd := buildNodeDockerComposeInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", true)
	if !strings.Contains(cmd, "      - NET_ADMIN\n    privileged: true\n") {
		t.Fatalf("privileged compose command malformed:\n%s", cmd)
	}
}

// 默认实例（FLMX_INSTANCE 未设）黄金：两种容器命令与定版字面量
// 逐字一致——单面板存量升级后重新生成的命令不得有任何漂移。
func TestBuildNodeDockerCommandsDefaultInstanceGolden(t *testing.T) {
	t.Setenv(panelInstanceEnv, "")

	wantDocker := `mkdir -p /etc/flmx_agent && (umask 077; printf '{\n  "addr": "%s",\n  "secret": "%s"\n}\n' 'wss://panel.example.com:443' 's3cr3t' > /etc/flmx_agent/config.json) && chmod 600 /etc/flmx_agent/config.json && { docker rm -f flmx-agent >/dev/null 2>&1 || true; } && docker run -d --name flmx-agent --network host --cap-add NET_ADMIN --restart unless-stopped -v /etc/flmx_agent:/etc/flmx_agent ghcr.io/iiiiiiiii7125-art/flmx-agent:1.9.4`
	if got := buildNodeDockerInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", false); got != wantDocker {
		t.Fatalf("default docker command drifted:\n got: %s\nwant: %s", got, wantDocker)
	}

	wantCompose := `mkdir -p /etc/flmx_agent && (umask 077; printf '{\n  "addr": "%s",\n  "secret": "%s"\n}\n' 'wss://panel.example.com:443' 's3cr3t' > /etc/flmx_agent/config.json) && chmod 600 /etc/flmx_agent/config.json && mkdir -p /opt/docker/flmx-agent && cat > /opt/docker/flmx-agent/docker-compose.yaml <<'FLMX_AGENT_COMPOSE_EOF' && docker compose -f /opt/docker/flmx-agent/docker-compose.yaml up -d
services:
  flmx-agent:
    image: ghcr.io/iiiiiiiii7125-art/flmx-agent:1.9.4
    container_name: flmx-agent
    network_mode: host
    cap_add:
      - NET_ADMIN
    restart: unless-stopped
    volumes:
      - /etc/flmx_agent:/etc/flmx_agent
FLMX_AGENT_COMPOSE_EOF
`
	if got := buildNodeDockerComposeInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", false); got != wantCompose {
		t.Fatalf("default compose command drifted:\n got: %q\nwant: %q", got, wantCompose)
	}
}

// 实例键校验：只有白名单形态的值才参与派生，其余一律回退默认
// 实例——该值拼进容器名与宿主机路径，杜绝穿越与注入。
func TestPanelInstanceID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"east", "east"},
		{"panel-2", "panel-2"},
		{"a_b", "a_b"},
		{" east ", "east"},
		{"East", ""},
		{"-east", ""},
		{"ea st", ""},
		{"ea/st", ""},
		{"ea.st", ""},
		{"../x", ""},
		{strings.Repeat("a", 32), strings.Repeat("a", 32)},
		{strings.Repeat("a", 33), ""},
	}
	for _, tc := range cases {
		t.Setenv(panelInstanceEnv, tc.in)
		if got := panelInstanceID(); got != tc.want {
			t.Fatalf("panelInstanceID with env %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 实例键非空：容器名、数据目录、Compose 落位三处统一带后缀，且命
// 令不得再命中默认实例的任何资源（否则仍会覆写/rm 对方 agent）。
func TestBuildNodeDockerCommandsWithInstance(t *testing.T) {
	t.Setenv(panelInstanceEnv, "east")

	dockerCmd := buildNodeDockerInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", false)
	for _, want := range []string{
		"mkdir -p /etc/flmx_agent-east",
		"> /etc/flmx_agent-east/config.json",
		"chmod 600 /etc/flmx_agent-east/config.json",
		"docker rm -f flmx-agent-east",
		"docker run -d --name flmx-agent-east --network host",
		"-v /etc/flmx_agent-east:/etc/flmx_agent ",
	} {
		if !strings.Contains(dockerCmd, want) {
			t.Fatalf("instance docker command missing %q:\n%s", want, dockerCmd)
		}
	}
	for _, banned := range []string{
		"mkdir -p /etc/flmx_agent ",
		"> /etc/flmx_agent/config.json",
		"docker rm -f flmx-agent ",
		"docker rm -f flmx-agent>",
		"--name flmx-agent ",
		"-v /etc/flmx_agent:",
		"flmx_agent-east:/etc/flmx_agent-east",
	} {
		if strings.Contains(dockerCmd, banned) {
			t.Fatalf("instance docker command still references default resource %q:\n%s", banned, dockerCmd)
		}
	}
	// 挂载结构断言：源＝宿主机后缀目录、目标＝镜像锚点（无特权与
	// 特权两种形态共用同一挂载段）。
	for _, cmd := range []string{dockerCmd, buildNodeDockerInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", true)} {
		src, dst := dockerMountSourceTarget(t, cmd)
		if src != "/etc/flmx_agent-east" || dst != agentDataBaseDir {
			t.Fatalf("instance docker mount = %q -> %q, want /etc/flmx_agent-east -> %q:\n%s", src, dst, agentDataBaseDir, cmd)
		}
	}

	composeCmd := buildNodeDockerComposeInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", false)
	for _, want := range []string{
		"mkdir -p /opt/docker/flmx-agent-east",
		"cat > /opt/docker/flmx-agent-east/docker-compose.yaml",
		"docker compose -f /opt/docker/flmx-agent-east/docker-compose.yaml up -d",
		"  flmx-agent-east:\n",
		"    container_name: flmx-agent-east\n",
		"      - /etc/flmx_agent-east:/etc/flmx_agent\n",
	} {
		if !strings.Contains(composeCmd, want) {
			t.Fatalf("instance compose command missing %q:\n%s", want, composeCmd)
		}
	}
	for _, banned := range []string{
		"/opt/docker/flmx-agent/",
		"  flmx-agent:\n",
		"    container_name: flmx-agent\n",
		"      - /etc/flmx_agent:",
		"flmx_agent-east:/etc/flmx_agent-east",
	} {
		if strings.Contains(composeCmd, banned) {
			t.Fatalf("instance compose command still references default resource %q:\n%s", banned, composeCmd)
		}
	}
	// 挂载结构断言（compose 卷行）：源＝宿主机后缀目录、目标＝镜
	// 像锚点；特权形态同断。
	for _, cmd := range []string{composeCmd, buildNodeDockerComposeInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", true)} {
		src, dst := composeMountSourceTarget(t, cmd)
		if src != "/etc/flmx_agent-east" || dst != agentDataBaseDir {
			t.Fatalf("instance compose mount = %q -> %q, want /etc/flmx_agent-east -> %q:\n%s", src, dst, agentDataBaseDir, cmd)
		}
	}
}

// dockerMountSourceTarget 抽取 docker run 命令里 -v 的源与目标。
func dockerMountSourceTarget(t *testing.T, cmd string) (string, string) {
	t.Helper()
	i := strings.Index(cmd, " -v ")
	if i < 0 {
		t.Fatalf("command has no -v mount:\n%s", cmd)
	}
	rest := cmd[i+len(" -v "):]
	j := strings.IndexByte(rest, ' ')
	if j < 0 {
		t.Fatalf("cannot parse -v mount in:\n%s", cmd)
	}
	src, dst, ok := strings.Cut(rest[:j], ":")
	if !ok {
		t.Fatalf("malformed -v mount %q in:\n%s", rest[:j], cmd)
	}
	return src, dst
}

// composeMountSourceTarget 抽取 compose 卷行的源与目标。
func composeMountSourceTarget(t *testing.T, cmd string) (string, string) {
	t.Helper()
	const marker = "    volumes:\n      - "
	i := strings.Index(cmd, marker)
	if i < 0 {
		t.Fatalf("command has no compose volume line:\n%s", cmd)
	}
	rest := cmd[i+len(marker):]
	j := strings.IndexByte(rest, '\n')
	if j < 0 {
		t.Fatalf("cannot parse compose volume line in:\n%s", cmd)
	}
	src, dst, ok := strings.Cut(rest[:j], ":")
	if !ok {
		t.Fatalf("malformed compose volume %q in:\n%s", rest[:j], cmd)
	}
	return src, dst
}

// 实例键不合规：整体回退默认实例命名，畸形值不得漏进命令。
func TestBuildNodeDockerCommandsInvalidInstanceFallsBack(t *testing.T) {
	t.Setenv(panelInstanceEnv, "Bad/Name")
	cmd := buildNodeDockerInstallCommand("1.9.4", "wss://panel.example.com:443", "s3cr3t", false)
	if !strings.Contains(cmd, "--name flmx-agent --network host") {
		t.Fatalf("invalid instance must fall back to default naming:\n%s", cmd)
	}
	if strings.Contains(cmd, "Bad") {
		t.Fatalf("invalid instance value must not leak into the command:\n%s", cmd)
	}
}
