package monitoring

import (
	"fmt"
	"strconv"
	"strings"
)

type ServiceMonitorLimits struct {
	CheckerScanIntervalSec int `json:"checkerScanIntervalSec"`
	WorkerLimit            int `json:"workerLimit"`

	MinIntervalSec     int `json:"minIntervalSec"`
	DefaultIntervalSec int `json:"defaultIntervalSec"`

	MinTimeoutSec     int `json:"minTimeoutSec"`
	DefaultTimeoutSec int `json:"defaultTimeoutSec"`
	MaxTimeoutSec     int `json:"maxTimeoutSec"`
}

const (
	ConfigServiceMonitorCheckerScanIntervalSec = "service_monitor_checker_scan_interval_sec"
	ConfigServiceMonitorWorkerLimit            = "service_monitor_worker_limit"
	ConfigServiceMonitorMinIntervalSec         = "service_monitor_min_interval_sec"
	ConfigServiceMonitorDefaultIntervalSec     = "service_monitor_default_interval_sec"
	ConfigServiceMonitorMinTimeoutSec          = "service_monitor_min_timeout_sec"
	ConfigServiceMonitorDefaultTimeoutSec      = "service_monitor_default_timeout_sec"
	ConfigServiceMonitorMaxTimeoutSec          = "service_monitor_max_timeout_sec"
)

func DefaultServiceMonitorLimits() ServiceMonitorLimits {
	return ServiceMonitorLimits{
		CheckerScanIntervalSec: 1,
		WorkerLimit:            20,
		MinIntervalSec:         1,
		DefaultIntervalSec:     1,
		MinTimeoutSec:          1,
		DefaultTimeoutSec:      5,
		MaxTimeoutSec:          60,
	}
}

// ServiceMonitorLimitsFromConfigMap parses limits from vite_config values.
// Missing/invalid values fall back to defaults.
func ServiceMonitorLimitsFromConfigMap(cfg map[string]string) ServiceMonitorLimits {
	limits := DefaultServiceMonitorLimits()
	if cfg == nil {
		return limits
	}

	limits.CheckerScanIntervalSec = parseConfigInt(cfg, ConfigServiceMonitorCheckerScanIntervalSec, limits.CheckerScanIntervalSec)
	limits.WorkerLimit = parseConfigInt(cfg, ConfigServiceMonitorWorkerLimit, limits.WorkerLimit)
	limits.MinIntervalSec = parseConfigInt(cfg, ConfigServiceMonitorMinIntervalSec, limits.MinIntervalSec)
	limits.DefaultIntervalSec = parseConfigInt(cfg, ConfigServiceMonitorDefaultIntervalSec, limits.DefaultIntervalSec)
	limits.MinTimeoutSec = parseConfigInt(cfg, ConfigServiceMonitorMinTimeoutSec, limits.MinTimeoutSec)
	limits.DefaultTimeoutSec = parseConfigInt(cfg, ConfigServiceMonitorDefaultTimeoutSec, limits.DefaultTimeoutSec)
	limits.MaxTimeoutSec = parseConfigInt(cfg, ConfigServiceMonitorMaxTimeoutSec, limits.MaxTimeoutSec)

	return normalizeServiceMonitorLimits(limits)
}

func normalizeServiceMonitorLimits(limits ServiceMonitorLimits) ServiceMonitorLimits {
	if limits.CheckerScanIntervalSec <= 0 {
		limits.CheckerScanIntervalSec = 30
	}
	if limits.WorkerLimit <= 0 {
		limits.WorkerLimit = 5
	}
	if limits.WorkerLimit > 50 {
		limits.WorkerLimit = 50
	}

	if limits.MinIntervalSec <= 0 {
		limits.MinIntervalSec = limits.CheckerScanIntervalSec
	}
	if limits.MinIntervalSec < limits.CheckerScanIntervalSec {
		limits.MinIntervalSec = limits.CheckerScanIntervalSec
	}
	if limits.DefaultIntervalSec <= 0 {
		limits.DefaultIntervalSec = 60
	}
	if limits.DefaultIntervalSec < limits.MinIntervalSec {
		limits.DefaultIntervalSec = limits.MinIntervalSec
	}

	if limits.MinTimeoutSec <= 0 {
		limits.MinTimeoutSec = 1
	}
	if limits.DefaultTimeoutSec <= 0 {
		limits.DefaultTimeoutSec = 5
	}
	if limits.DefaultTimeoutSec < limits.MinTimeoutSec {
		limits.DefaultTimeoutSec = limits.MinTimeoutSec
	}
	if limits.MaxTimeoutSec <= 0 {
		limits.MaxTimeoutSec = 60
	}
	if limits.MaxTimeoutSec < limits.DefaultTimeoutSec {
		limits.MaxTimeoutSec = limits.DefaultTimeoutSec
	}
	// R4-F5-01：排序抬升之后对 timeout 三键补 60 硬钳（与 worker_limit
	// 读面钳 50 同形）。保存面（RR-F5-01）只拦新写与恢复，≤1.9.6 合法
	// 写入的存量越界值（如 max=120、min/default=90）随升级原样保留，
	// 此前读面只有抬升没有封顶，抬升还会把 90 一路传导到三键——
	// monitoring.md「超时最大 60 秒」对存量部署不成立。硬钳放在抬升
	// 之后：三键先按相对序抬齐、再同界截断，min<=default<=max 的序
	// 关系与三键 <=60 同时成立。
	if limits.MinTimeoutSec > MaxServiceMonitorTimeoutSec {
		limits.MinTimeoutSec = MaxServiceMonitorTimeoutSec
	}
	if limits.DefaultTimeoutSec > MaxServiceMonitorTimeoutSec {
		limits.DefaultTimeoutSec = MaxServiceMonitorTimeoutSec
	}
	if limits.MaxTimeoutSec > MaxServiceMonitorTimeoutSec {
		limits.MaxTimeoutSec = MaxServiceMonitorTimeoutSec
	}

	return limits
}

// 服务监控内部限值 7 键的保存期值域（RR-F5-01）：这 7 键此前经通用
// 配置写面 default 直通、无保存期校验，与同族监控键（保留天数
// NormalizeMonitoringRetentionDays、质量间隔
// NormalizeTunnelQualityProbeIntervalSeconds，均为保存期拒绝）口径
// 不一致——尤其 max_timeout 可被写成 >60，且读面归一化的排序抬升
// （max<default 时抬 max、default<min 时抬 default）会把上限继续
// 抬高，使 doc/guide/monitoring.md「探测超时最大 60 秒」仅在默认
// 配置下成立。逐键值域依据（代码常量／读面归一化／文档三面取交集）：
//   - 7 键均须为正整数：读面 parseConfigInt 对非整数回落默认、
//     normalizeServiceMonitorLimits 对 <=0 一律替换，<=0 与非整数
//     在存储层没有合法语义，保存期按同族口径拒绝；
//   - worker_limit 上限 50：读面归一化既有的硬钳（>50 钳到 50），
//     保存面同界拒绝，不与读面打架；
//   - 三个 timeout 键上限同守 60：文档 60 秒口径＋读面排序抬升
//     决定三键必须同界，归一化后的 MaxTimeoutSec 才恒 <=60；
//   - checker 扫描间隔与两个 interval 键无上限：代码常量、前端
//     （限值经 limits 接口下发、无独立上限）与文档均未给上限，
//     读面亦不钳，不自创口径；跨键排序（min>=checker、
//     default>=min）继续由读面归一化负责，与保存期逐键值域不冲突。
const (
	MinServiceMonitorLimitValue  = 1
	MaxServiceMonitorWorkerLimit = 50
	MaxServiceMonitorTimeoutSec  = 60
)

// serviceMonitorLimitDomain 描述单个限值键的保存期值域
// （max<=0 表示无上限）。
type serviceMonitorLimitDomain struct {
	label string
	max   int
}

var serviceMonitorLimitDomains = map[string]serviceMonitorLimitDomain{
	ConfigServiceMonitorCheckerScanIntervalSec: {label: "服务监控扫描间隔"},
	ConfigServiceMonitorWorkerLimit:            {label: "服务监控并发上限", max: MaxServiceMonitorWorkerLimit},
	ConfigServiceMonitorMinIntervalSec:         {label: "服务监控最小检查间隔"},
	ConfigServiceMonitorDefaultIntervalSec:     {label: "服务监控默认检查间隔"},
	ConfigServiceMonitorMinTimeoutSec:          {label: "服务监控最小超时", max: MaxServiceMonitorTimeoutSec},
	ConfigServiceMonitorDefaultTimeoutSec:      {label: "服务监控默认超时", max: MaxServiceMonitorTimeoutSec},
	ConfigServiceMonitorMaxTimeoutSec:          {label: "服务监控最大超时", max: MaxServiceMonitorTimeoutSec},
}

// NormalizeServiceMonitorLimitValue 是 service_monitor_* 7 键的保存期
// 值域校验，保存面（handler.normalizeAndValidateConfigValue）与恢复
// 面（repo.normalizeImportedMonitoringConfigValue）共用，与
// NormalizeMonitoringRetentionDays 同形。这 7 键非必填、可清空：
// 空串返回 "" 表示清空回落默认，与调用方的可清空语义兼容；非空
// 必须为落在值域内的整数，合法值归一为规范整数串（去空白/前导
// 零）。未知键原样返回（调用方只对本族 7 键调用，此分支仅防御）。
func NormalizeServiceMonitorLimitValue(key, value string) (string, error) {
	domain, ok := serviceMonitorLimitDomains[key]
	if !ok {
		return value, nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return "", fmt.Errorf("%s必须是整数", domain.label)
	}
	if n < MinServiceMonitorLimitValue {
		return "", fmt.Errorf("%s必须不小于 %d", domain.label, MinServiceMonitorLimitValue)
	}
	if domain.max > 0 && n > domain.max {
		return "", fmt.Errorf("%s必须在 %d 到 %d 之间", domain.label, MinServiceMonitorLimitValue, domain.max)
	}
	return strconv.Itoa(n), nil
}

func parseConfigInt(cfg map[string]string, key string, fallback int) int {
	v := strings.TrimSpace(cfg[key])
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
