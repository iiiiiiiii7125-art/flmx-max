package bypass

import (
	"context"
	"testing"

	xbypass "github.com/go-gost/x/bypass"
)

// FIX2-C #8：服务配置引用 bypass 名 flmx-no-private 时，解析层必须
// 直接给出内置实现，而不是注册表 wrapper——注册表里没有该名时
// wrapper 的 Contains 恒 false（fail-open），旧 agent 正是这样漏的；
// 本版起名字在解析层被认领，任何经 parsing 构建的服务都不会漏。
func TestListResolvesBuiltinNoPrivate(t *testing.T) {
	list := List(xbypass.NoPrivateBypassName)
	if len(list) != 1 {
		t.Fatalf("expected exactly the builtin bypass, got %d entries", len(list))
	}
	bp := list[0]
	if !bp.Contains(context.Background(), "tcp", "192.168.0.8:443") {
		t.Fatal("builtin bypass must block private targets")
	}
	if bp.Contains(context.Background(), "tcp", "8.8.4.4:443") {
		t.Fatal("builtin bypass must allow public targets")
	}
}
