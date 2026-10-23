package repo

import "testing"

// R10X-014（A-02）：PolicyForConfig 分类全集——现行在用键逐键已分类，
// 未分类新键默认敏感（fail-closed）；非管理员过滤走同一策略。

func TestR10B6PolicyForConfigClassification(t *testing.T) {
	authenticated := []string{
		"forward_compact_mode", "forward_group_order_map", "forward_group_collapsed_map",
		"latency_targets",
		"monitor_tunnel_quality_enabled", "monitor_tunnel_quality_interval_sec", "monitor_retention_days",
		"captcha_enabled", "panel_domain", "ip",
		"github_proxy_enabled", "github_proxy_url", "allow_local_remote_addr",
		"notify_enabled", "notify_events", "notify_daily_hour",
		"notify_latency_loss_pct", "notify_latency_ms",
		"service_monitor_checker_scan_interval_sec", "service_monitor_worker_limit",
		"service_monitor_min_interval_sec", "service_monitor_default_interval_sec",
		"service_monitor_min_timeout_sec", "service_monitor_default_timeout_sec",
		"service_monitor_max_timeout_sec",
	}
	for _, key := range authenticated {
		if got := PolicyForConfig(key); got != ConfigAccessPolicy("authenticated") {
			t.Fatalf("PolicyForConfig(%q) = %v, want authenticated (in-use key must be classified)", key, got)
		}
	}
	if got := PolicyForConfig("app_name"); got != ConfigAccessPublic {
		t.Fatalf("PolicyForConfig(app_name) = %v, want public", got)
	}
	if got := PolicyForConfig("jwt_secret"); got != ConfigAccessSensitive {
		t.Fatalf("PolicyForConfig(jwt_secret) = %v, want sensitive", got)
	}
	if got := PolicyForConfig("r10b6_future_unclassified_key"); got != ConfigAccessSensitive {
		t.Fatalf("PolicyForConfig(unclassified) = %v, want sensitive (fail-closed default)", got)
	}
	// 归一化：大小写/空白不影响分类。
	if got := PolicyForConfig("  FORWARD_COMPACT_MODE  "); got != ConfigAccessPolicy("authenticated") {
		t.Fatalf("PolicyForConfig(normalized) = %v, want authenticated", got)
	}
}

func TestR10B6FilterSensitiveConfigsUsesPolicy(t *testing.T) {
	out := FilterSensitiveConfigs(map[string]string{
		"app_name":                 "Brand",
		"forward_compact_mode":     "true",
		"jwt_secret":               "secret",
		"r10b6_future_secret_hint": "future",
	})
	if len(out) != 2 || out["app_name"] != "Brand" || out["forward_compact_mode"] != "true" {
		t.Fatalf("non-admin filter must keep public+authenticated only, got %+v", out)
	}
}
