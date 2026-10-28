package repo

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// R2-B1 (r2 修复轮第 1 批): R2-H-01 + R2-H-02 回归测试，red-first 编写。
//
// R2-H-01: 恢复侧 latency_targets 校验缺 netguard 政策层——H-171-01
// 只镜像了保存侧格式层 + 回环/未指定字面量，13 个确定性政策拒
// 字面量仍可落库并毒化整池（读侧解析失败即返空池）。本文件锁定
// 13 字面量并集（H 域 10 格 + L 域 7 格，重合 4 格）与放行对照
// （私网/CGNAT/公网/主机名仍放行，与保存侧同 verdict）。
// R2-H-02: 恢复面 app_logo/app_favicon 不复用保存面 PNG + 1MiB 门，
// 2MiB 非法值零 warning 落库。本文件锁定恢复面同口径。

// r2b1PolicyLiterals 是 R2-H-01 的 13 字面量并集（交叉复核
// x-verdicts-r2.md §1；fe80::1 另以带方括号形态补一格）。
var r2b1PolicyLiterals = []string{
	"169.254.169.254", // 云元数据（metadataExact）
	"100.100.100.200", // 阿里元数据（metadataExact）
	"169.254.0.23",    // 链路本地 v4（169.254/16 硬拒）
	"fe80::1",         // 链路本地 v6（fe80::/10）
	"224.0.0.1",       // 组播
	"198.18.1.1",      // 基准保留段（198.18/15）
	"240.1.2.3",       // 保留段（240/4）
	"::ffff:169.254.169.254", // 4-in-6 元数据
	"fd00:ec2::254",         // 元数据 v6（metadataExact）
	"64:ff9b:1::1",          // 本地 NAT64（64:ff9b:1::/48）
	"240.0.0.1",             // 保留段（240/4）
	"198.18.0.1",            // 基准保留段（198.18/15）
	"0.0.0.1",               // 保留段（0.0.0.0/8）
}

func r2b1LatencyTargetsJSON(host string) string {
	return fmt.Sprintf(`[{"name":"m","host":%q,"port":443,"enabled":true}]`, host)
}

func TestR2B1LatencyTargetsRestoreRejectsPolicyLiterals(t *testing.T) {
	hosts := append(append([]string{}, r2b1PolicyLiterals...), "[fe80::1]")
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			if err := validateLatencyTargetsConfigValue(r2b1LatencyTargetsJSON(host)); err == nil {
				t.Fatalf("validateLatencyTargetsConfigValue(host=%q) = nil, want error (policy literal must be rejected on restore)", host)
			}
		})
	}
	// 放行对照：与保存侧同 verdict——私网/CGNAT/公网/主机名放行。
	allowed := []string{"8.8.8.8", "10.0.0.1", "192.168.1.1", "100.64.0.1", "example.com"}
	for _, host := range allowed {
		t.Run("allow-"+host, func(t *testing.T) {
			if err := validateLatencyTargetsConfigValue(r2b1LatencyTargetsJSON(host)); err != nil {
				t.Fatalf("validateLatencyTargetsConfigValue(host=%q) = %v, want nil (save side allows it)", host, err)
			}
		})
	}
}

func TestR2B1LatencyTargetsRestoreSkipsPolicyLiterals(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// 单条元数据值经恢复面必须跳过 + 点名 latency_targets，零落库。
	res := r171b5ImportLatencyTargets(t, r, r2b1LatencyTargetsJSON("169.254.169.254"))
	if res.ConfigsImported != 0 {
		t.Fatalf("metadata: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if got, ok := r171b5StoredLatencyTargets(t, r); ok {
		t.Fatalf("metadata: stored latency_targets %q, want no row", got)
	}
	if !strings.Contains(strings.Join(res.ImportWarnings, "\n"), "latency_targets") {
		t.Fatalf("metadata: warnings %v do not name latency_targets", res.ImportWarnings)
	}

	// 1 好 + 1 毒整值跳过（读侧任一毒值即整池死亡，恢复面不得放行半池）。
	mixed := `[{"name":"good","host":"8.8.8.8","port":443,"enabled":true},{"name":"bad","host":"224.0.0.1","port":443,"enabled":true}]`
	res = r171b5ImportLatencyTargets(t, r, mixed)
	if res.ConfigsImported != 0 {
		t.Fatalf("mixed: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if got, ok := r171b5StoredLatencyTargets(t, r); ok {
		t.Fatalf("mixed: stored latency_targets %q, want no row", got)
	}

	// 合法值仍逐字恢复（无过修）。
	legal := r171b5LatencyTargetsLegal()
	res = r171b5ImportLatencyTargets(t, r, legal)
	if res.ConfigsImported != 1 {
		t.Fatalf("legal: ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := r171b5StoredLatencyTargets(t, r); !ok || got != legal {
		t.Fatalf("legal: stored %q (ok=%v), want verbatim %q", got, ok, legal)
	}
}

// r2b1BrandAssetLegal 是 1x1 透明 PNG 的 data URL（保存侧放行形态）。
func r2b1BrandAssetLegal() string {
	return "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
}

func r2b1ImportConfigs(t *testing.T, r *Repository, configs map[string]string) *model.ImportResult {
	t.Helper()
	res, err := r.Import(&model.BackupData{Version: "1.0", Configs: configs}, []string{"configs"})
	if err != nil {
		t.Fatalf("Import configs failed: %v", err)
	}
	return res
}

func r2b1StoredConfig(t *testing.T, r *Repository, name string) (string, bool) {
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

func TestR2B1BrandAssetRestoreSkipsBadValues(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// H 探针原值：2,097,174 字节非法 data URL（前缀合法 + 2MiB 全 A
	// 合法 base64，仅超 1MiB）经恢复面必须跳过 + 点名 app_logo。
	oversize := "data:image/png;base64," + strings.Repeat("A", 2097152)
	res := r2b1ImportConfigs(t, r, map[string]string{"app_logo": oversize})
	if res.ConfigsImported != 0 {
		t.Fatalf("oversize: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if got, ok := r2b1StoredConfig(t, r, "app_logo"); ok {
		t.Fatalf("oversize: stored app_logo len %d, want no row", len(got))
	}
	if !strings.Contains(strings.Join(res.ImportWarnings, "\n"), "app_logo") {
		t.Fatalf("oversize: warnings %v do not name app_logo", res.ImportWarnings)
	}

	// 错前缀（JPEG）与非法 base64 同样跳过 + 点名。
	res = r2b1ImportConfigs(t, r, map[string]string{"app_favicon": "data:image/jpeg;base64,AAAA"})
	if res.ConfigsImported != 0 {
		t.Fatalf("wrong-prefix: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if got, ok := r2b1StoredConfig(t, r, "app_favicon"); ok {
		t.Fatalf("wrong-prefix: stored app_favicon %q, want no row", got)
	}
	if !strings.Contains(strings.Join(res.ImportWarnings, "\n"), "app_favicon") {
		t.Fatalf("wrong-prefix: warnings %v do not name app_favicon", res.ImportWarnings)
	}
	res = r2b1ImportConfigs(t, r, map[string]string{"app_logo": "data:image/png;base64,!!!"})
	if res.ConfigsImported != 0 {
		t.Fatalf("bad-base64: ConfigsImported = %d, want 0", res.ConfigsImported)
	}
	if got, ok := r2b1StoredConfig(t, r, "app_logo"); ok {
		t.Fatalf("bad-base64: stored app_logo %q, want no row", got)
	}

	// 小体积合法 PNG 逐字恢复。
	legal := r2b1BrandAssetLegal()
	res = r2b1ImportConfigs(t, r, map[string]string{"app_logo": legal})
	if res.ConfigsImported != 1 {
		t.Fatalf("legal: ConfigsImported = %d, want 1 (warnings %v)", res.ConfigsImported, res.ImportWarnings)
	}
	if got, ok := r2b1StoredConfig(t, r, "app_logo"); !ok || got != legal {
		t.Fatalf("legal: stored %q (ok=%v), want verbatim %q", got, ok, legal)
	}

	// 空值保持既有行为：原样存空行（与 latency_targets 空值口径同；
	// 保存侧空值走可清空键删除路径，恢复面不改该语义）。
	res = r2b1ImportConfigs(t, r, map[string]string{"app_favicon": ""})
	if res.ConfigsImported != 1 {
		t.Fatalf("empty: ConfigsImported = %d, want 1", res.ConfigsImported)
	}
	if got, ok := r2b1StoredConfig(t, r, "app_favicon"); !ok || got != "" {
		t.Fatalf("empty: stored %q (ok=%v), want empty row", got, ok)
	}
}

// TestR2B1ValidateBrandAssetConfigValue locks the restore-side brand
// asset validator to the save-side rule set (handler.
// normalizeAndValidateConfigValue, app_logo/app_favicon branch):
// PNG 前缀 + 1MiB 上限（含前缀）+ 合法 base64。
func TestR2B1ValidateBrandAssetConfigValue(t *testing.T) {
	prefix := "data:image/png;base64,"
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"legal", r2b1BrandAssetLegal(), false},
		{"empty", "", false},
		{"wrong-prefix-jpeg", "data:image/jpeg;base64,AAAA", true},
		{"wrong-prefix-text", "data:text/html;base64,AAAA", true},
		{"no-prefix", "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", true},
		{"empty-payload", prefix, true},
		{"bad-base64", prefix + "!!!", true},
		// 边界：总长 1MiB（含 22 字节前缀）放行，超 1MiB 拒收；
		// payload 取 4 的倍数以保证两侧均为合法 base64。
		{"at-limit", prefix + strings.Repeat("A", 1048552), false},
		{"over-limit", prefix + strings.Repeat("A", 1048556), true},
		{"oversize-2mib", prefix + strings.Repeat("A", 2097152), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBrandAssetConfigValue(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateBrandAssetConfigValue(len=%d) err = %v, wantErr = %v", len(tc.raw), err, tc.wantErr)
			}
		})
	}
}
