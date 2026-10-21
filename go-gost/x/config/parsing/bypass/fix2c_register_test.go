package bypass

import (
	"context"
	"testing"

	corebypass "github.com/go-gost/core/bypass"
	xbypass "github.com/go-gost/x/bypass"
	"github.com/go-gost/x/registry"
)

type allowAllBypass struct{}

func (allowAllBypass) Contains(context.Context, string, string, ...corebypass.Option) bool {
	return false
}
func (allowAllBypass) IsWhitelist() bool { return false }

// FIX2-C #8：RegisterBuiltinBypasses 只填空位——运维在配置里自定了
// 同名 bypass 时不得被内置实现覆盖；名字缺席时补上内置实现，使
// 注册表直查路径（如 handler metadata 按名取 bypass）也能解析。
func TestRegisterBuiltinBypassesRespectsOperatorDefinition(t *testing.T) {
	reg := registry.BypassRegistry()
	name := xbypass.NoPrivateBypassName
	t.Cleanup(func() { reg.Unregister(name) })

	if err := reg.Register(name, allowAllBypass{}); err != nil {
		t.Fatal(err)
	}
	RegisterBuiltinBypasses()
	if _, ok := reg.GetAll()[name].(allowAllBypass); !ok {
		t.Fatal("operator-defined bypass must not be overridden by the builtin")
	}

	reg.Unregister(name)
	RegisterBuiltinBypasses()
	got := reg.GetAll()[name]
	if got == nil {
		t.Fatal("builtin bypass must be registered when the name is absent")
	}
	if !got.Contains(context.Background(), "tcp", "10.9.8.7:443") {
		t.Fatal("registered builtin must block private targets")
	}
}
