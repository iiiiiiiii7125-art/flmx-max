package repo

import (
	"strings"
	"testing"
)

// R2-048：恢复面（importConfigs）与保存面共用同一值域校验单点
// （B14-002 的双面同口径不变量）——notify_daily_hour 越界值在
// 恢复面同样被拒（调用方按跳过＋warning 处置），不得经备份导入
// 绕过保存面落库。

func TestNormalizeNotifyDailyHourSinglePoint(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{"-1", "", "0 到 23"},
		{"0", "0", ""},
		{"23", "23", ""},
		{"24", "", "0 到 23"},
		{"", "", ""},
		{" 9 ", "9", ""},
		{"abc", "", "必须是整数"},
	}
	for _, tc := range cases {
		got, err := NormalizeNotifyDailyHour(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("NormalizeNotifyDailyHour(%q): expected error containing %q, got (%q, %v)", tc.in, tc.wantErr, got, err)
			}
		} else if err != nil || got != tc.want {
			t.Fatalf("NormalizeNotifyDailyHour(%q): got (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}

		// 恢复面入口必须与单点逐项同判。
		got2, err2 := normalizeImportedMonitoringConfigValue(NotifyDailyHourConfigKey, tc.in)
		if (err2 == nil) != (err == nil) || got2 != got {
			t.Fatalf("restore face diverges for %q: single=(%q, %v) import=(%q, %v)", tc.in, got, err, got2, err2)
		}
	}

	// 未列名键仍原样直通（本面不扩围）。
	if got, err := normalizeImportedMonitoringConfigValue("some_other_key", "raw"); err != nil || got != "raw" {
		t.Fatalf("unlisted key must pass through, got (%q, %v)", got, err)
	}
}
