package monitoring

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	ConfigMonitorRetentionDays  = "monitor_retention_days"
	DefaultMonitorRetentionDays = 7
	MinMonitorRetentionDays     = 1
	// MaxMonitorRetentionDays 原为 3650：界虽有界但极宽，管理员配满
	// 时四张监控表按写入节奏积 10 年量（GARBAGE-AUDIT A3）。收紧到
	// 与节点流量小时桶的固定保留期同口径的 400 天；存量已存的大于
	// 400 的值在读面按非法值回落默认 7 天、保存面直接拒绝。
	MaxMonitorRetentionDays = 400
)

func MonitoringRetentionDaysFromConfigMap(cfg map[string]string) int {
	if cfg == nil {
		return DefaultMonitorRetentionDays
	}
	days, err := parseMonitoringRetentionDays(cfg[ConfigMonitorRetentionDays])
	if err != nil {
		return DefaultMonitorRetentionDays
	}
	return days
}

func NormalizeMonitoringRetentionDays(value string) (string, error) {
	days, err := parseMonitoringRetentionDays(value)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(days), nil
}

func parseMonitoringRetentionDays(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("监控数据保留天数不能为空")
	}
	days, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("监控数据保留天数必须是整数")
	}
	if days < MinMonitorRetentionDays || days > MaxMonitorRetentionDays {
		return 0, fmt.Errorf("监控数据保留天数必须在 %d 到 %d 之间", MinMonitorRetentionDays, MaxMonitorRetentionDays)
	}
	return days, nil
}
