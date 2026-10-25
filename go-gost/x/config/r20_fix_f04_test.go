// R20-F04: 启动自愈剪无引用 conn limiter（存量孤儿清理路径）。
//
// F-03（R19）只防新增：删转发路径补发 DeleteCLimiters 后不再产生新孤儿，
// 但存量孤儿（测试机 gost.json 内 rule_conn_limit_12 ["$ 7"] 与
// rule_conn_limit_2 ["$ 1"]）永久残留——agent 启动自愈只按名去重
// （HealPersistedCLimiters），不剪无引用 limiter；面板亦无清扫入口。
//
// 修法（方案①，启动自愈加剪）：healPersistedConfig 在去重之后剪掉
// 全部无引用的 CLimiters。引用集 = 全部存活服务 ServiceConfig.CLimiter
// 字段的逗号分隔名（与 x/config/parsing/service resolveConnLimiter
// 的解析口径一致：逗号切分、TrimSpace、空段丢弃；全仓 grep 证实服务
// 是 conn limiter 的唯一消费方——链/跳点/admission 均无 climiter 字段，
// CLI 建服务亦经同一 CLimiter 字段挂接）。
// 安全线：
//   - `user_conn_limit_*` 永不剪：同用户全部转发共享，面板可能在无
//     服务引用它的间隙仍视其为有效（且面板下次同步会重新 AddCLimiters，
//     故即使误删亦可自愈，但按任务要求永不剪）；
//   - 只动 CLimiters 一类：traffic / rate limiter 一律不动；
//   - nil 项原样保留（与 Deduplicate* 同口径；healPersistedConfig 内
//     StripNil 先行，纯函数本身亦须 nil 安全）。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 毒化配置取真实存量孤儿之名：rule_conn_limit_12 / rule_conn_limit_2
// （R20-X-04），另加有引用 rule、逗号复合引用、共享 user（有引用与
// 无引用各一）、任意无引用名、nil 项，覆盖全部判定分支。
func TestPruneUnreferencedCLimiters(t *testing.T) {
	cfg := &Config{
		Services: []*ServiceConfig{
			{Name: "svc-a", CLimiter: "rule_conn_limit_5"},
			{Name: "svc-b", CLimiter: "user_conn_limit_9, rule_conn_limit_42"},
			{Name: "svc-c", CLimiter: " rule_conn_limit_5 "},
			{Name: "svc-d"},
			nil,
		},
		CLimiters: []*LimiterConfig{
			{Name: "rule_conn_limit_12", Limits: []string{"$ 7"}},
			{Name: "rule_conn_limit_2", Limits: []string{"$ 1"}},
			{Name: "rule_conn_limit_5", Limits: []string{"$ 3"}},
			{Name: "rule_conn_limit_42", Limits: []string{"$$ 2"}},
			{Name: "user_conn_limit_9", Limits: []string{"$ 4"}},
			{Name: "user_conn_limit_7", Limits: []string{"$ 4"}},
			{Name: "manual-climiter", Limits: []string{"$ 1"}},
			nil,
		},
		Limiters:  []*LimiterConfig{{Name: "rule_traffic_limit_1"}},
		RLimiters: []*LimiterConfig{{Name: "rule_rate_limit_1"}},
	}

	removed := PruneUnreferencedCLimiters(cfg)
	wantRemoved := []string{"rule_conn_limit_12", "rule_conn_limit_2", "manual-climiter"}
	if len(removed) != len(wantRemoved) {
		t.Fatalf("removed = %v, want %v", removed, wantRemoved)
	}
	for i, name := range wantRemoved {
		if removed[i] != name {
			t.Fatalf("removed[%d] = %q, want %q (full: %v)", i, removed[i], name, removed)
		}
	}

	wantKept := []string{"rule_conn_limit_5", "rule_conn_limit_42", "user_conn_limit_9", "user_conn_limit_7"}
	kept := map[string]bool{}
	nilKept := 0
	for _, lim := range cfg.CLimiters {
		if lim == nil {
			nilKept++
			continue
		}
		kept[lim.Name] = true
	}
	if nilKept != 1 {
		t.Fatalf("nil climiter entry must be kept as-is, kept nil count = %d", nilKept)
	}
	if len(kept) != len(wantKept) {
		t.Fatalf("kept climiter names = %v, want %v", kept, wantKept)
	}
	for _, name := range wantKept {
		if !kept[name] {
			t.Fatalf("climiter %q must be kept, kept = %v", name, kept)
		}
	}
	// 只动 CLimiters：traffic / rate limiter 一律不动。
	if len(cfg.Limiters) != 1 || cfg.Limiters[0].Name != "rule_traffic_limit_1" {
		t.Fatalf("traffic limiters must be untouched, got %#v", cfg.Limiters)
	}
	if len(cfg.RLimiters) != 1 || cfg.RLimiters[0].Name != "rule_rate_limit_1" {
		t.Fatalf("rate limiters must be untouched, got %#v", cfg.RLimiters)
	}
}

func TestPruneUnreferencedCLimitersCleanIsNoop(t *testing.T) {
	cfg := &Config{
		Services: []*ServiceConfig{
			{Name: "svc-a", CLimiter: "rule_conn_limit_5"},
			{Name: "svc-b", CLimiter: "user_conn_limit_9"},
		},
		CLimiters: []*LimiterConfig{
			{Name: "rule_conn_limit_5"},
			{Name: "user_conn_limit_9"},
		},
	}
	if removed := PruneUnreferencedCLimiters(cfg); removed != nil {
		t.Fatalf("removed = %v, want nil", removed)
	}
	if len(cfg.CLimiters) != 2 {
		t.Fatalf("expected 2 conn limiters, got %d", len(cfg.CLimiters))
	}
}

func TestPruneUnreferencedCLimitersNilSafe(t *testing.T) {
	if removed := PruneUnreferencedCLimiters(nil); removed != nil {
		t.Fatalf("PruneUnreferencedCLimiters(nil) = %v, want nil", removed)
	}
	cfg := &Config{Services: []*ServiceConfig{nil}, CLimiters: []*LimiterConfig{nil}}
	if removed := PruneUnreferencedCLimiters(cfg); removed != nil {
		t.Fatalf("removed = %v, want nil", removed)
	}
	if len(cfg.CLimiters) != 1 || cfg.CLimiters[0] != nil {
		t.Fatalf("nil climiter entry must be kept as-is, got %#v", cfg.CLimiters)
	}
	// 服务全 nil / 无服务时，有名 climiter 仍按无引用剪（user_ 除外）。
	cfg = &Config{Services: []*ServiceConfig{nil}, CLimiters: []*LimiterConfig{
		{Name: "rule_conn_limit_12"},
		{Name: "user_conn_limit_7"},
	}}
	removed := PruneUnreferencedCLimiters(cfg)
	if len(removed) != 1 || removed[0] != "rule_conn_limit_12" {
		t.Fatalf("removed = %v, want [rule_conn_limit_12]", removed)
	}
	if len(cfg.CLimiters) != 1 || cfg.CLimiters[0].Name != "user_conn_limit_7" {
		t.Fatalf("user_conn_limit_7 must survive with no services, got %#v", cfg.CLimiters)
	}
}

// HealPersistedOrphanCLimiters 与 HealPersisted* 同口径：剪掉孤儿后立即
// 重新落盘，干净配置不触发写。
func TestHealPersistedOrphanCLimiters(t *testing.T) {
	cfg := &Config{
		Services: []*ServiceConfig{{Name: "svc-a", CLimiter: "rule_conn_limit_5"}},
		CLimiters: []*LimiterConfig{
			{Name: "rule_conn_limit_12", Limits: []string{"$ 7"}},
			{Name: "rule_conn_limit_2", Limits: []string{"$ 1"}},
			{Name: "rule_conn_limit_5", Limits: []string{"$ 3"}},
			{Name: "user_conn_limit_7", Limits: []string{"$ 4"}},
		},
	}

	originalPath := persistPath
	originalEnabled := persistEnable
	persistFile := filepath.Join(t.TempDir(), "gost.json")
	t.Cleanup(func() {
		persistPath = originalPath
		persistEnable = originalEnabled
	})
	persistPath = persistFile
	persistEnable = false // Heal 不依赖 EnablePersist

	removed, err := HealPersistedOrphanCLimiters(cfg)
	if err != nil {
		t.Fatalf("HealPersistedOrphanCLimiters: %v", err)
	}
	if len(removed) != 2 || removed[0] != "rule_conn_limit_12" || removed[1] != "rule_conn_limit_2" {
		t.Fatalf("removed = %v, want [rule_conn_limit_12 rule_conn_limit_2]", removed)
	}
	if len(cfg.CLimiters) != 2 {
		t.Fatalf("expected 2 conn limiters after heal, got %d", len(cfg.CLimiters))
	}

	raw, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("healed config must be re-persisted: %v", err)
	}
	var healed Config
	if err := json.Unmarshal(raw, &healed); err != nil {
		t.Fatalf("persisted file must be valid JSON: %v", err)
	}
	if len(healed.CLimiters) != 2 {
		t.Fatalf("persisted file has %d conn limiters, want 2", len(healed.CLimiters))
	}
	for _, lim := range healed.CLimiters {
		if lim == nil {
			t.Fatal("persisted file must not contain nil conn limiter")
		}
		if lim.Name == "rule_conn_limit_12" || lim.Name == "rule_conn_limit_2" {
			t.Fatalf("persisted file still contains orphan %q", lim.Name)
		}
	}
}

func TestHealPersistedOrphanCLimitersCleanIsNoop(t *testing.T) {
	originalPath := persistPath
	dir := t.TempDir()
	t.Cleanup(func() { persistPath = originalPath })
	persistPath = filepath.Join(dir, "gost.json")

	cfg := &Config{
		Services:  []*ServiceConfig{{Name: "svc-a", CLimiter: "rule_conn_limit_5"}},
		CLimiters: []*LimiterConfig{{Name: "rule_conn_limit_5"}, {Name: "user_conn_limit_7"}},
	}
	if removed, err := HealPersistedOrphanCLimiters(cfg); err != nil || removed != nil {
		t.Fatalf("HealPersistedOrphanCLimiters(clean) = %v, %v, want nil, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gost.json")); err == nil {
		t.Fatal("clean config must not trigger a persist write")
	}
}
