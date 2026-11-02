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
