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
// 四段及以上按前三段比较：多余段是发行版后缀而非语义版本的一部分
// （parseVersionParts 同样接受任意段数），不得因段数多就无条件放行——
// "25.5.15.9" 的前三段低于 25.5.16 下限，必须判低于。
func versionBelowPadded(version string, major, minor, patch int) bool {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	nums := [3]int{}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
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

// versionBelowPaddedStrict 是 versionBelowPadded 的硬门严格变体
// （R2-016/B-sub-03，BC）：缺段仍按 0 补齐（"25.6" = 25.6.0，prefer
// 门既有严格口径不变），但任一已出现段不可解析（如 "25.5.16-rc1"
// 的第三段）即视为低于下限、不再 fail-open；空串与 "latest" 的豁免
// 见 versionFloorExempt。四段及以上仍按前三段比较。
func versionBelowPaddedStrict(version string, major, minor, patch int) bool {
	if versionFloorExempt(version) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	nums := [3]int{}
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return true
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
	if policy != kernel.OutboundIPAuto && kernelType == kernel.TypeSingbox && versionBelowStrict(version, 1, 12) {
		return fmt.Errorf("限制出站 IP 需要 sing-box 1.12 及以上，节点安装的是 %s", version)
	}
	if (policy == kernel.OutboundPreferIPv4 || policy == kernel.OutboundPreferIPv6) && kernelType == kernel.TypeXray && versionBelowPaddedStrict(version, 25, 6, 8) {
		return fmt.Errorf("优先 IP 自动回退需要 Xray 25.6.8 及以上，节点安装的是 %s", version)
	}
	return nil
}
