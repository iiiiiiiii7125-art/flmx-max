package monitoring

import (
	"strconv"
	"testing"
)

// RR-F5-01：service_monitor_* 7 键保存期值域逐键覆盖——界内值原样
// （归一后）通过、超界/非整数/非正值拒绝、空串按可清空语义返回 ""。
func TestNormalizeServiceMonitorLimitValue(t *testing.T) {
	keys := []string{
		ConfigServiceMonitorCheckerScanIntervalSec,
		ConfigServiceMonitorWorkerLimit,
		ConfigServiceMonitorMinIntervalSec,
		ConfigServiceMonitorDefaultIntervalSec,
		ConfigServiceMonitorMinTimeoutSec,
		ConfigServiceMonitorDefaultTimeoutSec,
		ConfigServiceMonitorMaxTimeoutSec,
	}
	if len(serviceMonitorLimitDomains) != len(keys) {
		t.Fatalf("domain table must cover exactly the 7 service monitor limit keys, got %d entries", len(serviceMonitorLimitDomains))
	}
	for _, key := range keys {
		if _, ok := serviceMonitorLimitDomains[key]; !ok {
			t.Fatalf("domain table missing key %s", key)
		}
		// 界内与归一：下限 1、空白修剪、前导零归一、空串清空语义。
		for in, want := range map[string]string{"1": "1", " 30 ": "30", "007": "7", "": ""} {
			got, err := NormalizeServiceMonitorLimitValue(key, in)
			if err != nil || got != want {
				t.Fatalf("%s value %q: got (%q, %v), want (%q, nil)", key, in, got, err, want)
			}
		}
		// 全键共同非法集：0、负数、非整数。
		for _, in := range []string{"0", "-1", "abc", "1.5"} {
			if got, err := NormalizeServiceMonitorLimitValue(key, in); err == nil {
				t.Fatalf("%s value %q: got (%q, nil), want error", key, in, got)
			}
		}
	}

	// 带上限的键：上限本身通过、上限+1 拒绝。
	capped := map[string]int{
		ConfigServiceMonitorWorkerLimit:       MaxServiceMonitorWorkerLimit,
		ConfigServiceMonitorMinTimeoutSec:     MaxServiceMonitorTimeoutSec,
		ConfigServiceMonitorDefaultTimeoutSec: MaxServiceMonitorTimeoutSec,
		ConfigServiceMonitorMaxTimeoutSec:     MaxServiceMonitorTimeoutSec,
	}
	for key, max := range capped {
		if got, err := NormalizeServiceMonitorLimitValue(key, strconv.Itoa(max)); err != nil || got != strconv.Itoa(max) {
			t.Fatalf("%s at cap %d: got (%q, %v), want pass", key, max, got, err)
		}
		if got, err := NormalizeServiceMonitorLimitValue(key, strconv.Itoa(max+1)); err == nil {
			t.Fatalf("%s above cap %d: got (%q, nil), want error", key, max, got)
		}
	}

	// 无上限的键（checker 扫描间隔与两个 interval 键）：大值通过。
	for _, key := range []string{
		ConfigServiceMonitorCheckerScanIntervalSec,
		ConfigServiceMonitorMinIntervalSec,
		ConfigServiceMonitorDefaultIntervalSec,
	} {
		if got, err := NormalizeServiceMonitorLimitValue(key, "86400"); err != nil || got != "86400" {
			t.Fatalf("%s value 86400: got (%q, %v), want pass (no documented cap)", key, got, err)
		}
	}

	// 未知键原样返回（防御分支）。
	if got, err := NormalizeServiceMonitorLimitValue("some_other_key", "raw"); err != nil || got != "raw" {
		t.Fatalf("unknown key: got (%q, %v), want (\"raw\", nil)", got, err)
	}
}

// RR-F5-01：值域上限的字面钉——timeout 上限 60 来自
// doc/guide/monitoring.md「探测超时最大 60 秒」且须与
// DefaultServiceMonitorLimits 的默认上限一致；worker 上限 50 来自
// 读面归一化的既有硬钳。改界须先改依据、再改此钉（其余用例对
// 上限取值是常量相对的，靠此钉防上限被静默抬高）。
func TestServiceMonitorLimitDocumentedCaps(t *testing.T) {
	if MaxServiceMonitorTimeoutSec != 60 {
		t.Fatalf("MaxServiceMonitorTimeoutSec = %d, want 60 (monitoring.md timeout cap)", MaxServiceMonitorTimeoutSec)
	}
	if MaxServiceMonitorWorkerLimit != 50 {
		t.Fatalf("MaxServiceMonitorWorkerLimit = %d, want 50 (read-side normalize clamp)", MaxServiceMonitorWorkerLimit)
	}
	if got := DefaultServiceMonitorLimits().MaxTimeoutSec; got != MaxServiceMonitorTimeoutSec {
		t.Fatalf("default MaxTimeoutSec = %d, want == save-side cap %d", got, MaxServiceMonitorTimeoutSec)
	}
}

// RR-F5-01 不变量：凡经保存期校验放行的 timeout 三元组，读面归一化
// （含排序抬升）之后 MaxTimeoutSec 恒 <=60——文档「探测超时最大
// 60 秒」在任意管理员配置下成立，保存期校验与读面归一化不打架。
func TestServiceMonitorLimitsTimeoutCeilingInvariant(t *testing.T) {
	valid := []string{"1", "5", "30", "60"}
	for _, min := range valid {
		for _, def := range valid {
			for _, max := range valid {
				cfg := map[string]string{
					ConfigServiceMonitorMinTimeoutSec:     min,
					ConfigServiceMonitorDefaultTimeoutSec: def,
					ConfigServiceMonitorMaxTimeoutSec:     max,
				}
				limits := ServiceMonitorLimitsFromConfigMap(cfg)
				if limits.MaxTimeoutSec > MaxServiceMonitorTimeoutSec {
					t.Fatalf("triple min=%s def=%s max=%s normalized to MaxTimeoutSec=%d, want <= %d",
						min, def, max, limits.MaxTimeoutSec, MaxServiceMonitorTimeoutSec)
				}
			}
		}
	}
	// worker 上限保存面与读面同界：50 放行后读面原样保留。
	cfg := map[string]string{ConfigServiceMonitorWorkerLimit: "50"}
	if limits := ServiceMonitorLimitsFromConfigMap(cfg); limits.WorkerLimit != MaxServiceMonitorWorkerLimit {
		t.Fatalf("worker_limit 50 normalized to %d, want %d", limits.WorkerLimit, MaxServiceMonitorWorkerLimit)
	}
}

// R4-F5-01：存量越界 timeout 值（≤1.9.6 时可合法写入、保存期校验
// 拦不到的已落库值）经读面归一化后三键必须同被 60 硬钳封顶，且
// min<=default<=max 的序关系不得被硬钳打乱。网格直接喂原始 config
// 值（绕过保存期校验，模拟存量行），含审计探针实证的 90/120 形态。
func TestServiceMonitorLimitsTimeoutCeilingStoredOverflow(t *testing.T) {
	stored := []string{"1", "5", "30", "60", "90", "120"}
	for _, min := range stored {
		for _, def := range stored {
			for _, max := range stored {
				cfg := map[string]string{
					ConfigServiceMonitorMinTimeoutSec:     min,
					ConfigServiceMonitorDefaultTimeoutSec: def,
					ConfigServiceMonitorMaxTimeoutSec:     max,
				}
				limits := ServiceMonitorLimitsFromConfigMap(cfg)
				if limits.MinTimeoutSec > MaxServiceMonitorTimeoutSec ||
					limits.DefaultTimeoutSec > MaxServiceMonitorTimeoutSec ||
					limits.MaxTimeoutSec > MaxServiceMonitorTimeoutSec {
					t.Fatalf("stored triple min=%s def=%s max=%s normalized to (%d, %d, %d), want all <= %d",
						min, def, max, limits.MinTimeoutSec, limits.DefaultTimeoutSec, limits.MaxTimeoutSec, MaxServiceMonitorTimeoutSec)
				}
				if limits.MinTimeoutSec > limits.DefaultTimeoutSec || limits.DefaultTimeoutSec > limits.MaxTimeoutSec {
					t.Fatalf("stored triple min=%s def=%s max=%s normalized to (%d, %d, %d), ordering min<=default<=max broken",
						min, def, max, limits.MinTimeoutSec, limits.DefaultTimeoutSec, limits.MaxTimeoutSec)
				}
			}
		}
	}
	// 探针原形态逐条点名：单键越界不得经抬升传导到其余键之上。
	if limits := ServiceMonitorLimitsFromConfigMap(map[string]string{
		ConfigServiceMonitorMaxTimeoutSec: "120",
	}); limits.MaxTimeoutSec != MaxServiceMonitorTimeoutSec {
		t.Fatalf("stored max=120 normalized to %d, want %d", limits.MaxTimeoutSec, MaxServiceMonitorTimeoutSec)
	}
	if limits := ServiceMonitorLimitsFromConfigMap(map[string]string{
		ConfigServiceMonitorMinTimeoutSec: "90",
	}); limits.MinTimeoutSec != MaxServiceMonitorTimeoutSec ||
		limits.DefaultTimeoutSec != MaxServiceMonitorTimeoutSec ||
		limits.MaxTimeoutSec != MaxServiceMonitorTimeoutSec {
		t.Fatalf("stored min=90 normalized to (%d, %d, %d), want all == %d",
			limits.MinTimeoutSec, limits.DefaultTimeoutSec, limits.MaxTimeoutSec, MaxServiceMonitorTimeoutSec)
	}
}
