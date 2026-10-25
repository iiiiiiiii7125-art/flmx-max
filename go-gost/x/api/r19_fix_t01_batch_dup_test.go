// R19 T-01 / V-08：批内同名第 2 阶段 fail-closed 回归钉子。
//
// 批内同名 `[同名, 同名]` 在构造上不可达第 4 阶段：第 1 阶段两项均通过
// IsRegistered 守卫（注册表此时无此名），第 2 阶段第二项 Register 即报
// ErrDup、回滚已注册的第一项并返回 400，永不到第 3 阶段（启动）与第 4
// 阶段（配置落盘）。本测试钉死该 fail-closed 行为：400、内存配置 0 项、
// 注册表空。源自 R19 L2  scratch 探针（R19-L2-batch-dup_test.go.txt），
// 沙箱实测 [同名, 同名] → 400、配置 0 项、注册表空。
package api

import (
	"net/http"
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/registry"
)

func TestR19T01CreateServicesBatchIntraDupFailsAtStage2(t *testing.T) {
	name := "r19_t01_batch_dup"
	r := r18TestRouter()
	persistFile := r18SeedConfig(t, &config.Config{})
	t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
	registry.ServiceRegistry().Unregister(name)

	body := `[{"name":"` + name + `","addr":"127.0.0.1:0"},{"name":"` + name + `","addr":"127.0.0.1:0"}]`
	w := r18Post(t, r, "/config/services/batch", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("batch [dup,dup] status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if n := r18CountNamed(config.Global().Services, name, func(s *config.ServiceConfig) string { return s.Name }); n != 0 {
		t.Fatalf("batch [dup,dup] left %d config entries, want 0 (stage-2 rollback)", n)
	}
	if registry.ServiceRegistry().Get(name) != nil {
		t.Fatalf("batch [dup,dup] left %q registered, want unregistered (stage-2 rollback)", name)
	}
	t.Logf("batch [dup,dup] -> 400, config 0 entries, registry empty; persisted file=%s", persistFile)
}
