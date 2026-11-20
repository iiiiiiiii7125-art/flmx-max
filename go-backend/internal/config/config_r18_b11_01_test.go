package config

import (
	"reflect"
	"testing"
)

// R18-B11-01：LOG_DIR 是死配置——解析入库后全仓无人读取，面板日志
// 实际走标准 log 包输出到 stdout（由 Docker 收集）。按首轮建议的
// (a) 方案删字段：删除 Config.LogDir 与 FromEnv 里的
// getEnv("LOG_DIR", …)，并在 doc/guide/install.md 明确“面板日志走
// stdout，由容器运行时收集”。接线方案 (b) 不取：落盘轮转须同步补
// 轮转与磁盘上限，否则引入新的打满面。
// 回归钉：反射断言 Config 不再有 LogDir 字段（旧代码必红）；
// FromEnv 在 LOG_DIR 已设置时仍正常加载其余字段。
func TestR18B1101LogDirDeadConfigRemoved(t *testing.T) {
	if _, ok := reflect.TypeOf(Config{}).FieldByName("LogDir"); ok {
		t.Fatalf("R18-B11-01: Config.LogDir must be removed: it was parsed but never read, so setting LOG_DIR misled operators into believing logs were archived to disk")
	}
	t.Setenv("LOG_DIR", t.TempDir())
	cfg := FromEnv()
	if cfg.Addr == "" {
		t.Fatalf("FromEnv must still load the remaining fields with LOG_DIR set, got empty Addr")
	}
}
