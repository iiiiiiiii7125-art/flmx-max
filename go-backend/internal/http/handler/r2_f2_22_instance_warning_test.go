package handler

import (
	"strings"
	"testing"
)

// F2-22 行为复验（单元面）：非法 FLMX_INSTANCE＋明文 ws:// 地址时
// 两警告须合并进同一 warning 字段；脚本形态不消费实例后缀故只
// 剩明文警告；合法实例＋wss 无警告。HTTP 端到端格（nodeInstall
// 先拉 GitHub 版本，沙箱出口被拒、无本地兜底）登记为待集成真机格。
func TestNodeInstallWarningsF222(t *testing.T) {
	// 非法实例＋明文地址，容器形态：两警告，实例警告在前。
	t.Setenv(panelInstanceEnv, "East")
	got := nodeInstallWarnings(nodeInstallModeDocker, "ws://panel.example.com:6365")
	if len(got) != 2 {
		t.Fatalf("docker warnings = %d, want 2: %v", len(got), got)
	}
	if !strings.Contains(got[0], `FLMX_INSTANCE="East"`) || !strings.Contains(got[0], "撞名") {
		t.Fatalf("instance warning = %q", got[0])
	}
	if !strings.Contains(got[1], "明文") || !strings.Contains(got[1], "ws://panel.example.com:6365") {
		t.Fatalf("plaintext warning = %q", got[1])
	}
	// 同一组合拼进回执的形态：两警告以「；」连接。
	if joined := strings.Join(got, "；"); !strings.Contains(joined, "FLMX_INSTANCE") || !strings.Contains(joined, "明文") {
		t.Fatalf("joined warnings = %q", joined)
	}

	// 脚本形态不消费实例后缀：只剩明文警告。
	got = nodeInstallWarnings(nodeInstallModeScript, "ws://panel.example.com:6365")
	if len(got) != 1 || !strings.Contains(got[0], "明文") {
		t.Fatalf("script warnings = %v, want only the plaintext one", got)
	}

	// 合法实例＋wss：无警告。
	t.Setenv(panelInstanceEnv, "east")
	if got := nodeInstallWarnings(nodeInstallModeDocker, "wss://panel.example.com:443"); len(got) != 0 {
		t.Fatalf("clean warnings = %v, want none", got)
	}

	// 未设实例＋wss：无警告；panelInstanceInvalid 本身亦为 (\"\", false)。
	t.Setenv(panelInstanceEnv, "")
	if raw, invalid := panelInstanceInvalid(); raw != "" || invalid {
		t.Fatalf("panelInstanceInvalid unset = (%q, %v), want (\"\", false)", raw, invalid)
	}
	if got := nodeInstallWarnings(nodeInstallModeDockerCompose, "wss://panel.example.com:443"); len(got) != 0 {
		t.Fatalf("clean compose warnings = %v, want none", got)
	}
}
