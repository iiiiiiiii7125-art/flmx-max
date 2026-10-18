package handler

import (
	"testing"
)

// S-02: version 必须严格匹配 ^v?[0-9]+\.[0-9]+\.[0-9]+$
// 修复前：无校验函数，测试应失败（编译失败）
// 修复后：非法 version 被拒绝
func TestS02_ValidAgentVersion(t *testing.T) {
	valid := []string{"1.0.0", "1.0.2", "v1.0.0", "10.20.30"}
	for _, v := range valid {
		if !isValidAgentVersion(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}
}

func TestS02_InvalidAgentVersionRejected(t *testing.T) {
	invalid := []string{
		"../x",           // 路径穿越
		"1.0",            // 不完整
		"1.0.0;rm -rf /", // 命令注入
		"../../evil",     // 路径穿越
		"",               // 空
		"1.0.0\n",        // 换行
		"v",              // 仅v
		"1.0.0.0",        // 四段
	}
	for _, v := range invalid {
		if isValidAgentVersion(v) {
			t.Errorf("expected %q to be rejected", v)
		}
	}
}
