package ws

import (
	"strings"
	"testing"
)

// R18-R2-D1-02: D1-01 新增的第六个解析前错误型 MissingInnerTimestamp
// 必须进面板的预解析错误集，否则旧面板升新 agent 后，面板的
// 结构化原因映射漏掉它，日志侧走不到命名分支、调用方只剩超时。
// 顺序门（handler/upgrade.go）是正门，本映射是绕过门禁后的兜底诊断。
func TestR18R2D102MissingInnerTimestampMapped(t *testing.T) {
	if !agentPreParseErrorTypes["MissingInnerTimestamp"] {
		t.Fatal("MissingInnerTimestamp must be in the pre-parse error set")
	}
	reason, ok := agentPreParseErrorReason("MissingInnerTimestamp",
		`{"type":"MissingInnerTimestamp","success":false,"message":"加密命令缺少内层时间戳（面板版本过旧或信封被篡改），拒绝执行"}`)
	if !ok || !strings.Contains(reason, "面板版本过旧") {
		t.Fatalf("MissingInnerTimestamp message must be extracted, got ok=%v reason=%q", ok, reason)
	}
	cmdReason, ok := agentPreParseErrorCommandReason("MissingInnerTimestamp",
		`{"type":"MissingInnerTimestamp","success":false,"message":"加密命令缺少内层时间戳（面板版本过旧或信封被篡改），拒绝执行"}`)
	if !ok || !strings.Contains(cmdReason, "先升级面板") {
		t.Fatalf("MissingInnerTimestamp must settle the caller naming the panel upgrade, got ok=%v reason=%q", ok, cmdReason)
	}
}
