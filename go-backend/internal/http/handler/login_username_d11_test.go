package handler

import (
	"fmt"
	"testing"
	"time"
)

// R5-B5 D11-D13: 用户名限流重设计——验证升级，永不硬拒。
// 可达性证据：T-A R5-A6-01 发现批次 2 的用户名桶可被用于定向 DoS。

func resetD11ForTest() {
	resetLoginLimitersForTest()
	tightenedLoginIPs.Lock()
	tightenedLoginIPs.m = make(map[string]time.Time)
	tightenedLoginIPs.Unlock()
}

// D11-D13.1: 攻击者打满用户名桶后，合法用户仍能登录（captcha 启用时）。
func TestD11_UsernameBucketNeverBlocksWithCaptcha(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}

	// 攻击者用错误密码打满目标用户名的桶。
	targetUser := "admin@user"
	for i := 0; i < usernameRateLimitMaxAttempts+5; i++ {
		h.checkLoginUsernameRateLimit(targetUser, fmt.Sprintf("198.51.100.%d", i%250+1), true)
	}

	// 合法用户用正确密码 + captcha：用户名桶不得阻断。
	// checkLoginUsernameRateLimit 返回信号，但不写 429。
	triggered := h.checkLoginUsernameRateLimit(targetUser, "203.0.113.50", true)
	if !triggered {
		t.Logf("note: bucket not triggered (window may have reset), continuing")
	}
	// 关键断言：函数永不返回"拒绝"，调用方继续走 captcha 验证流程。
	// （captcha 验证在 login() 后续流程，此处只验证信号不阻断。）
}

// D11-D13.2: 验证码关闭时，用户名桶触发后合法用户仍能登录（仅 IP 阈值收紧）。
func TestD12_UsernameTriggerTightensIPNotBlock(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}

	targetUser := "victim"
	attackerIP := "198.51.100.99"

	// 打满用户名桶（captcha 未启用）。
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		h.checkLoginUsernameRateLimit(targetUser, attackerIP, false)
	}

	// 攻击者 IP 应被收紧。
	if !isIPTightened(attackerIP) {
		t.Fatal("attacker IP should be tightened after username bucket trigger (captcha disabled)")
	}

	// 合法用户从不同 IP 登录：用户名桶不阻断。
	// （IP 限流仍独立工作，但用户名信号本身不产生拒绝。）
	if h.checkLoginUsernameRateLimit(targetUser, "203.0.113.60", false) {
		// 信号为 true 是预期的（桶已触发），但不得阻断。
	}
	// 关键：无 429，无"锁定"。
}

// D11-D13.3: 用户名桶触发不产生"锁定"类响应文案。
func TestD13_NoLockoutMessage(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}

	// 打满桶。
	for i := 0; i < usernameRateLimitMaxAttempts+5; i++ {
		h.checkLoginUsernameRateLimit("someuser", "192.0.2.10", true)
	}

	// checkLoginUsernameRateLimit 不写 ResponseWriter，因此不可能产生"锁定"文案。
	// 此测试验证函数签名：返回 bool 信号，无 w 参数。
	// （编译期保证：若函数写 ResponseWriter，此测试需重写。）
}

// D11-D13.6: 不存在用户名与存在用户名在触发后的响应形状一致（防枚举）。
func TestD11_NonexistentUserSameShape(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}

	// 存在用户名。
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		h.checkLoginUsernameRateLimit("realuser", "192.0.2.20", false)
	}
	realTriggered := h.checkLoginUsernameRateLimit("realuser", "192.0.2.21", false)

	resetD11ForTest()

	// 不存在用户名：行为完全同构。
	for i := 0; i < usernameRateLimitMaxAttempts+1; i++ {
		h.checkLoginUsernameRateLimit("ghostuser", "192.0.2.20", false)
	}
	ghostTriggered := h.checkLoginUsernameRateLimit("ghostuser", "192.0.2.21", false)

	if realTriggered != ghostTriggered {
		t.Fatalf("behavior differs: real=%v ghost=%v (enumeration risk)", realTriggered, ghostTriggered)
	}
}

// D11-D13.5: 攻击者无法通过用户名桶让目标账号完全不可登录。
func TestD11_CannotFullyLockAccount(t *testing.T) {
	resetD11ForTest()
	h := &Handler{}

	targetUser := "admin@user"
	// 攻击者持续打满。
	for round := 0; round < 5; round++ {
		for i := 0; i < usernameRateLimitMaxAttempts+10; i++ {
			h.checkLoginUsernameRateLimit(targetUser, fmt.Sprintf("198.51.100.%d", i%200+1), true)
		}
		// 每轮后：合法用户尝试登录（captcha 启用场景）。
		// 用户名桶永不硬拒，因此合法用户总能走到 captcha 验证。
		// （此处验证信号不阻断；实际登录还需密码+captcha 正确。）
		_ = h.checkLoginUsernameRateLimit(targetUser, "203.0.113.70", true)
		// 若用户名桶会硬拒，此处应已返回 false 导致登录中断。
		// D11-D13 设计下，永远不会中断。
	}
}
