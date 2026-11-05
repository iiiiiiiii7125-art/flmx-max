package handler

import (
	"strings"
	"testing"
)

// B3-01（生成面）: 存量 secret 字符集门 + 安装命令全插值安全
// 引用。导入面字符集校验在恢复路（FIX-B5 域），此处只钉生成面
// 两堵。

func TestB301SecretCharsetGate(t *testing.T) {
	if err := validateNodeSecretForInstall("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("canonical 32-hex secret must pass: %v", err)
	}
	for _, bad := range []string{
		"",
		"secret-x",
		"0123456789ABCDEF0123456789abcdef",  // 大写
		"0123456789abcdef0123456789abcde",   // 31 位
		"0123456789abcdef0123456789abcdef0", // 33 位
		"'; rm -rf /; echo '",               // 注入形态
		"0123456789abcdef0123456789abcdeg",  // 非 hex 字符
		"0123456789abcdef 0123456789abcdef", // 内嵌空格
	} {
		if err := validateNodeSecretForInstall(bad); err == nil {
			t.Fatalf("secret %q must be rejected", bad)
		}
	}
}

func TestB301InstallCommandsQuoteInterpolants(t *testing.T) {
	hostile := `a'b;$(touch /tmp/pwned)`
	// 脚本形态: -a/-s 两插值都必须是转义后的单引号参数形态。
	cmd := buildNodeInstallCommand("1.9.9", "wss://panel.example.com:443", hostile)
	if !strings.Contains(cmd, `-s 'a'\''b;$(touch /tmp/pwned)'`) {
		t.Fatalf("script-form command must shell-quote the secret, got %q", cmd)
	}
	if strings.Contains(cmd, "-s "+hostile) {
		t.Fatalf("secret must not appear unquoted, got %q", cmd)
	}
	// Docker/Compose 形态共用 agentConfigWriteStep: printf 两参数
	// 同口径引用。
	step := agentConfigWriteStep("wss://panel.example.com:443", hostile)
	if !strings.Contains(step, `'a'\''b;$(touch /tmp/pwned)'`) {
		t.Fatalf("config write step must shell-quote the secret, got %q", step)
	}
	dockerCmd := buildNodeDockerInstallCommand("1.9.9", "wss://panel.example.com:443", hostile, false)
	if !strings.Contains(dockerCmd, `'a'\''b;$(touch /tmp/pwned)'`) {
		t.Fatalf("docker-form command must shell-quote the secret, got %q", dockerCmd)
	}
}

func TestB301ShellQuoteRoundTrip(t *testing.T) {
	if got := shellQuoteForInstall("plain"); got != "'plain'" {
		t.Fatalf("plain value: got %s", got)
	}
	if got := shellQuoteForInstall("it's"); got != `'it'\''s'` {
		t.Fatalf("embedded quote must be escaped: got %s", got)
	}
	if got := shellQuoteForInstall(""); got != "''" {
		t.Fatalf("empty value: got %s", got)
	}
}
