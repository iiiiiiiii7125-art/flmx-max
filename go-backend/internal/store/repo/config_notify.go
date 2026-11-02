package repo

import (
	"fmt"
	"strconv"
	"strings"
)

// NotifyDailyHourConfigKey 是每日汇总（日报）的发送时刻键，值域
// 为一天内的整点（0–23），见 doc/guide/notify.md。
const NotifyDailyHourConfigKey = "notify_daily_hour"

// NormalizeNotifyDailyHour 是 notify_daily_hour 的写面单点校验
// （R2-048）：保存面（handler.normalizeAndValidateConfigValue）与
// 恢复面（importConfigs 的 normalizeImportedMonitoringConfigValue）
// 共用同一实现——与监控三键 Normalize* 的「同源单点、双面复刻」
// 口径一致（B14-002）。空串 = 清空回落默认（本键可清空，与读面
// dailyReportHour 的空回落同义），原样返回；非空必须是 0–23 的
// 整数，归一为标准整数串（与 monitoring.Normalize* 落库形态一致）。
func NormalizeNotifyDailyHour(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	hour, err := strconv.Atoi(trimmed)
	if err != nil {
		return "", fmt.Errorf("每日汇总发送时间必须是整数")
	}
	if hour < 0 || hour > 23 {
		return "", fmt.Errorf("每日汇总发送时间必须在 0 到 23 之间")
	}
	return strconv.Itoa(hour), nil
}
