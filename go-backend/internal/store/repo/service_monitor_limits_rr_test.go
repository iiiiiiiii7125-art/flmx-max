package repo

import (
	"testing"

	"go-backend/internal/monitoring"
)

// RR-F5-01：恢复面与保存面同源——service_monitor_* 7 键经
// normalizeImportedMonitoringConfigValue 与保存面同一校验
// （monitoring.NormalizeServiceMonitorLimitValue）：界内归一通过、
// 超界拒绝（导入侧据此跳过 + warning）、空串清空语义一致。
func TestNormalizeImportedServiceMonitorLimitValues(t *testing.T) {
	keys := []string{
		monitoring.ConfigServiceMonitorCheckerScanIntervalSec,
		monitoring.ConfigServiceMonitorWorkerLimit,
		monitoring.ConfigServiceMonitorMinIntervalSec,
		monitoring.ConfigServiceMonitorDefaultIntervalSec,
		monitoring.ConfigServiceMonitorMinTimeoutSec,
		monitoring.ConfigServiceMonitorDefaultTimeoutSec,
		monitoring.ConfigServiceMonitorMaxTimeoutSec,
	}
	for _, key := range keys {
		if got, err := normalizeImportedMonitoringConfigValue(key, " 15 "); err != nil || got != "15" {
			t.Fatalf("%s value ' 15 ': got (%q, %v), want (\"15\", nil)", key, got, err)
		}
		if got, err := normalizeImportedMonitoringConfigValue(key, ""); err != nil || got != "" {
			t.Fatalf("%s empty: got (%q, %v), want clear semantics (\"\", nil)", key, got, err)
		}
		for _, bad := range []string{"0", "-1", "abc"} {
			if got, err := normalizeImportedMonitoringConfigValue(key, bad); err == nil {
				t.Fatalf("%s value %q: got (%q, nil), want rejection on the restore face too", key, bad, got)
			}
		}
	}
	// 旧备份里经未校验写面落库的越界值，恢复面不得放行。
	if _, err := normalizeImportedMonitoringConfigValue(monitoring.ConfigServiceMonitorMaxTimeoutSec, "120"); err == nil {
		t.Fatal("restore face must reject max_timeout 120, same as the save face")
	}
	if _, err := normalizeImportedMonitoringConfigValue(monitoring.ConfigServiceMonitorWorkerLimit, "51"); err == nil {
		t.Fatal("restore face must reject worker_limit 51, same as the save face")
	}
	// 非本族键不受影响，原样返回（既有口径）。
	if got, err := normalizeImportedMonitoringConfigValue("some_other_key", "raw"); err != nil || got != "raw" {
		t.Fatalf("unrelated key: got (%q, %v), want (\"raw\", nil)", got, err)
	}
}
