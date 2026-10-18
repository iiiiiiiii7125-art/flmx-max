package service

import (
	"strings"
	"testing"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	xlogger "github.com/go-gost/x/logger"
)

func init() {
	// ParseService 需要默认 logger，否则 log.WithFields 空指针 panic
	logger.SetDefault(xlogger.Nop())
}

// S-01/S-03: 面板纳管的 agent 必须拒绝 service shell hook（preUp/postUp/preDown/postDown）。
// 这些键经 /bin/sh -c 以 root 执行，经面板命令或联邦 peer 下发即为远程 root RCE。
func TestParseServiceRejectsShellHooks(t *testing.T) {
	for _, key := range []string{"preUp", "postUp", "preDown", "postDown"} {
		cfg := &config.ServiceConfig{
			Name:     "test",
			Addr:     ":0",
			Listener: &config.ListenerConfig{Type: "tcp"},
			Handler:  &config.HandlerConfig{Type: "tcp"},
			Metadata: map[string]any{key: []string{"id >/tmp/pwned"}},
		}
		if _, err := ParseService(cfg); err == nil {
			t.Fatalf("shell hook %q accepted, expected rejection", key)
		}
	}
}

// 无 hook 的普通 service 不应被误伤：必须能通过 hook 检查
//（测试环境未注册 listener，后续阶段报错是预期的，只要不是 hook 拒绝）。
func TestParseServiceAllowsNoHooks(t *testing.T) {
	cfg := &config.ServiceConfig{
		Name:     "test",
		Addr:     ":0",
		Listener: &config.ListenerConfig{Type: "tcp"},
		Handler:  &config.HandlerConfig{Type: "tcp"},
		Metadata: map[string]any{"postUp": []string{}},
	}
	_, err := ParseService(cfg)
	if err != nil && strings.Contains(err.Error(), "shell hooks") {
		t.Fatalf("service without hooks rejected by hook check: %v", err)
	}
}
