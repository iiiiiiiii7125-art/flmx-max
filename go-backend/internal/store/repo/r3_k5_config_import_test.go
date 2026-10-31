package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// R3 K5（B14-001/B14-002）回归：备份导入 configs 段与保存面同口径——
// 键经 NormalizeConfigKey 归一（不再生成读不到的影子行）、归一后为空
// 或 >200 rune 的键跳过 + 点名、归一撞键定序合并（原始键字典序后者
// 胜出、结果确定）、监控三键走保存面等价校验（坏值跳过 + 点名、
// 好值按保存面归一形态落库）。基线（1.7.5）下变体键原样落库成影子
// 行、监控坏值原样落库，本文件用例全红。

func k5OpenRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "k5.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func k5ImportConfigs(t *testing.T, r *Repository, configs map[string]string) *model.ImportResult {
	t.Helper()
	res, err := r.Import(&model.BackupData{Version: "1.0", Configs: configs}, []string{"configs"})
	if err != nil {
		t.Fatalf("Import configs failed: %v", err)
	}
	return res
}

func k5StoredConfig(t *testing.T, r *Repository, name string) (string, bool) {
	t.Helper()
	cfg, err := r.GetConfigByName(name)
	if err != nil {
		t.Fatalf("GetConfigByName(%q): %v", name, err)
	}
	if cfg == nil {
		return "", false
	}
	return cfg.Value, true
}

func k5WarningsContain(res *model.ImportResult, substr string) bool {
	return strings.Contains(strings.Join(res.ImportWarnings, "\n"), substr)
}

// B14-001：变体键导入后按规范键命中、库内无影子行。
func TestR3K5ImportConfigsNormalizesVariantKeys(t *testing.T) {
	r := k5OpenRepo(t)
	res := k5ImportConfigs(t, r, map[string]string{
		"IP":                 "10.9.9.9:443",
		" Panel_Domain ":     "panel.example.com",
		" Custom_Probe_Key ": "v1",
	})
	if res.ConfigsImported != 3 {
		t.Fatalf("ConfigsImported = %d, want 3 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := k5StoredConfig(t, r, "ip"); !ok || got != "10.9.9.9:443" {
		t.Fatalf("config ip = %q, %v; want imported value under normalized key", got, ok)
	}
	if got, ok := k5StoredConfig(t, r, "panel_domain"); !ok || got != "panel.example.com" {
		t.Fatalf("config panel_domain = %q, %v; want imported value under normalized key", got, ok)
	}
	if got, ok := k5StoredConfig(t, r, "custom_probe_key"); !ok || got != "v1" {
		t.Fatalf("config custom_probe_key = %q, %v; want v1", got, ok)
	}
	// 全表复扫：任何一行键名 != 其归一形态即影子行残留。
	all, err := r.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	for name := range all {
		if name != NormalizeConfigKey(name) {
			t.Fatalf("shadow row persisted: key %q != normalized %q", name, NormalizeConfigKey(name))
		}
	}
}

// B14-001：归一后为空 / >200 rune 的键跳过 + 点名，其余键正常落库；
// 200 rune 边界键放行。
func TestR3K5ImportConfigsSkipsEmptyAndOverlongKeys(t *testing.T) {
	r := k5OpenRepo(t)
	overlong := strings.Repeat("k", 201)
	res := k5ImportConfigs(t, r, map[string]string{
		overlong: "v",
		"   ":    "v",
		"ok_key": "v",
	})
	if res.ConfigsImported != 1 {
		t.Fatalf("ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := k5StoredConfig(t, r, "ok_key"); !ok || got != "v" {
		t.Fatalf("ok_key = %q, %v; want v", got, ok)
	}
	if !k5WarningsContain(res, overlong) {
		t.Fatalf("warnings do not name the overlong key: %v", res.ImportWarnings)
	}
	if len(res.ImportWarnings) < 2 {
		t.Fatalf("warnings = %v, want at least 2 (overlong + blank)", res.ImportWarnings)
	}

	boundary := strings.Repeat("k", 200)
	res = k5ImportConfigs(t, r, map[string]string{boundary: "v200"})
	if res.ConfigsImported != 1 {
		t.Fatalf("boundary 200-rune key: ConfigsImported = %d, want 1", res.ConfigsImported)
	}
	if got, ok := k5StoredConfig(t, r, boundary); !ok || got != "v200" {
		t.Fatalf("boundary key stored = %q, %v; want v200", got, ok)
	}
}

// B14-001：归一撞键定序合并——原始键字典序后者（"ip" 在 "IP" 之后）
// 胜出，与值无关、多次导入结果一致，且有 warn 点名。
func TestR3K5ImportConfigsCollisionMergeDeterministic(t *testing.T) {
	r := k5OpenRepo(t)
	for i := 0; i < 5; i++ {
		res := k5ImportConfigs(t, r, map[string]string{
			"IP": "10.0.0.1:1",
			"ip": "10.0.0.2:2",
		})
		if res.ConfigsImported != 1 {
			t.Fatalf("round %d: ConfigsImported = %d, want 1 (merged)", i, res.ConfigsImported)
		}
		if got, _ := k5StoredConfig(t, r, "ip"); got != "10.0.0.2:2" {
			t.Fatalf("round %d: merged ip = %q, want 10.0.0.2:2 (raw key ip wins by sort order)", i, got)
		}
		if !k5WarningsContain(res, "\"ip\"") {
			t.Fatalf("round %d: warnings do not name the collision: %v", i, res.ImportWarnings)
		}
	}
	// 值对调后胜出者仍是原始键 "ip"（按原始键定序、与值无关）。
	res := k5ImportConfigs(t, r, map[string]string{
		"IP": "10.0.0.2:2",
		"ip": "10.0.0.1:1",
	})
	if got, _ := k5StoredConfig(t, r, "ip"); got != "10.0.0.1:1" {
		t.Fatalf("swapped values: merged ip = %q, want 10.0.0.1:1 (raw key ip still wins)", got)
	}
	_ = res
}

// B14-002：监控三键坏值跳过 + 点名、其余键正常落库；越界值同拒；
// 好值按保存面归一形态落库（枚举小写、整数去前导零）。
func TestR3K5ImportConfigsValidatesMonitoringKeys(t *testing.T) {
	r := k5OpenRepo(t)
	res := k5ImportConfigs(t, r, map[string]string{
		"monitor_tunnel_quality_enabled":      "banana",
		"monitor_retention_days":              "abc",
		"monitor_tunnel_quality_interval_sec": "xyz",
		"plain_key":                           "v",
	})
	if res.ConfigsImported != 1 {
		t.Fatalf("bad values: ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	for _, key := range []string{"monitor_tunnel_quality_enabled", "monitor_retention_days", "monitor_tunnel_quality_interval_sec"} {
		if _, ok := k5StoredConfig(t, r, key); ok {
			t.Fatalf("bad value for %q was stored", key)
		}
		if !k5WarningsContain(res, key) {
			t.Fatalf("warnings do not name %q: %v", key, res.ImportWarnings)
		}
	}

	// 越界（可解析但超出保存面范围）同样跳过。
	res = k5ImportConfigs(t, r, map[string]string{
		"monitor_retention_days":              "0",
		"monitor_tunnel_quality_interval_sec": "3601",
	})
	if res.ConfigsImported != 0 {
		t.Fatalf("out-of-range: ConfigsImported = %d, want 0", res.ConfigsImported)
	}

	// 变体键先归一再校验：坏值照样被拒。
	res = k5ImportConfigs(t, r, map[string]string{"Monitor_Retention_Days": "abc"})
	if res.ConfigsImported != 0 {
		t.Fatalf("variant bad value: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if _, ok := k5StoredConfig(t, r, "monitor_retention_days"); ok {
		t.Fatal("variant bad value stored under normalized key")
	}

	// 好值归一落库（与保存面 normalizeAndValidateConfigValue 同形态）。
	res = k5ImportConfigs(t, r, map[string]string{
		"monitor_tunnel_quality_enabled":      " TRUE ",
		"monitor_retention_days":              "030",
		"monitor_tunnel_quality_interval_sec": "060",
	})
	if res.ConfigsImported != 3 {
		t.Fatalf("good values: ConfigsImported = %d, want 3 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, _ := k5StoredConfig(t, r, "monitor_tunnel_quality_enabled"); got != "true" {
		t.Fatalf("enabled stored = %q, want normalized \"true\"", got)
	}
	if got, _ := k5StoredConfig(t, r, "monitor_retention_days"); got != "30" {
		t.Fatalf("retention stored = %q, want normalized \"30\"", got)
	}
	if got, _ := k5StoredConfig(t, r, "monitor_tunnel_quality_interval_sec"); got != "60" {
		t.Fatalf("interval stored = %q, want normalized \"60\"", got)
	}
}
