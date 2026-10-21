package handler

import (
	"fmt"
	"strconv"
	"strings"

	"go-backend/internal/kernel"
)

// versionBelowPadded 与 versionBelow3 的区别：缺段按 0 补齐（"25.6"
// 视为 25.6.0），无法解析时返回 false（不低于）。Xray prefer 门需要
// 严格口径——25.6 必须拦下，不能沿用 versionBelow3 对两段版本号的宽松。
func versionBelowPadded(version string, major, minor, patch int) bool {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) > 3 {
		return false
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		nums[i] = n
	}
	if nums[0] != major {
		return nums[0] < major
	}
	if nums[1] != minor {
		return nums[1] < minor
	}
	return nums[2] < patch
}

// validateOutboundIPVersion 是出站 IP 策略的版本门（S4 移植）：sing-box
// 任何非 auto 策略需要 1.12+；Xray 的 prefer 回退需要 25.6.8+（Happy
// Eyeballs 引入版本），严格模式无版本下限。除在保存/分流保存处显式调用
// 外，kernelVersionGate 也转调本函数，使下发复检与换版扫描同口径。
func validateOutboundIPVersion(kernelType, version, strategy string) error {
	policy, err := kernel.NormalizeOutboundIPStrategy(kernelType, strategy)
	if err != nil {
		return err
	}
	if policy != kernel.OutboundIPAuto && kernelType == kernel.TypeSingbox && versionBelow(version, 1, 12) {
		return fmt.Errorf("限制出站 IP 需要 sing-box 1.12 及以上，节点安装的是 %s", version)
	}
	if (policy == kernel.OutboundPreferIPv4 || policy == kernel.OutboundPreferIPv6) && kernelType == kernel.TypeXray && versionBelowPadded(version, 25, 6, 8) {
		return fmt.Errorf("优先 IP 自动回退需要 Xray 25.6.8 及以上，节点安装的是 %s", version)
	}
	return nil
}
