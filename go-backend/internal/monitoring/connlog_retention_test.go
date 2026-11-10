package monitoring

import "testing"

func TestConnlogRetentionDaysParsing(t *testing.T) {
	if got := ConnlogRetentionDaysFromConfigMap(nil); got != DefaultConnlogRetentionDays {
		t.Fatalf("nil map = %d, want default %d", got, DefaultConnlogRetentionDays)
	}
	if got := ConnlogRetentionDaysFromConfigMap(map[string]string{}); got != DefaultConnlogRetentionDays {
		t.Fatalf("missing key = %d, want default", got)
	}
	if got := ConnlogRetentionDaysFromConfigMap(map[string]string{ConfigConnlogRetentionDays: "14"}); got != 14 {
		t.Fatalf("stored 14 = %d, want 14", got)
	}
	// 读面：越界/非法存量值回落默认（与监控保留同口径）。
	for _, bad := range []string{"0", "31", "-1", "abc", "7.5"} {
		if got := ConnlogRetentionDaysFromConfigMap(map[string]string{ConfigConnlogRetentionDays: bad}); got != DefaultConnlogRetentionDays {
			t.Fatalf("stored %q = %d, want fallback default", bad, got)
		}
	}
	// 保存面：值域 1–30 严格校验。
	for _, ok := range []string{"1", "7", "30", " 15 "} {
		if _, err := NormalizeConnlogRetentionDays(ok); err != nil {
			t.Fatalf("Normalize(%q) err = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "31", "abc"} {
		if _, err := NormalizeConnlogRetentionDays(bad); err == nil {
			t.Fatalf("Normalize(%q) err = nil, want rejection", bad)
		}
	}
	if normalized, err := NormalizeConnlogRetentionDays(" 15 "); err != nil || normalized != "15" {
		t.Fatalf("Normalize(' 15 ') = (%q, %v), want ('15', nil)", normalized, err)
	}
}
