package monitoring

import (
	"fmt"
	"strconv"
	"strings"
)

// 入站连接日志的保留期（设计 §5.2）：独立于 monitor_retention_days，
// 防被监控保留策略放大——连接日志是事件驱动写入，忙碌入口的日增
// 行数远超监控采样表，故值域收紧为 1–30 天，另有每入站行数帽
// （repo.ConnlogPerInboundRowCap）双限并行。
const (
	ConfigConnlogRetentionDays  = "connlog_retention_days"
	DefaultConnlogRetentionDays = 7
	MinConnlogRetentionDays     = 1
	MaxConnlogRetentionDays     = 30
)

func ConnlogRetentionDaysFromConfigMap(cfg map[string]string) int {
	if cfg == nil {
		return DefaultConnlogRetentionDays
	}
	days, err := parseConnlogRetentionDays(cfg[ConfigConnlogRetentionDays])
	if err != nil {
		return DefaultConnlogRetentionDays
	}
	return days
}

func NormalizeConnlogRetentionDays(value string) (string, error) {
	days, err := parseConnlogRetentionDays(value)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(days), nil
}

func parseConnlogRetentionDays(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("连接日志保留天数不能为空")
	}
	days, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("连接日志保留天数必须是整数")
	}
	if days < MinConnlogRetentionDays || days > MaxConnlogRetentionDays {
		return 0, fmt.Errorf("连接日志保留天数必须在 %d 到 %d 之间", MinConnlogRetentionDays, MaxConnlogRetentionDays)
	}
	return days, nil
}
