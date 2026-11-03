package handler

// 修复轮 10 路 caps：R10-09（I-F2-02）转发目标段数帽＋解析 memo 化钉格。
//
// 基线（0cb4a7ba）红证：IsSafeRemoteAddr 对 20000 段字面量目标
// 一次过校验（err=nil、668ms）；段数无门、逐段 LookupIP 不去重
// 无缓存（机制以 security_utils.go 循环构造坐实，沙箱 sinkhole
// 解析器下重复解析不可直接计数，故计数钉格走可注入解析点
// safeRemoteLookupIP，与 federationDiagnoseLookupIP 同形态）。
//
// 修复后：写面（创建/更新、管理员与普通用户同判）在格式层以
// 100 段为帽；IsSafeRemoteAddr 单次调用内按主机名 memo 化，
// 同名段只解析一次，判据与错误文案不变。

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

func r1009Segments(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("203.0.113.5:%d", 10000+i)
	}
	return strings.Join(parts, ",")
}

func TestR1009SegmentCountValidator(t *testing.T) {
	if err := validateRemoteAddrSegmentCount(r1009Segments(100)); err != nil {
		t.Fatalf("100 segments must pass, got %v", err)
	}
	err := validateRemoteAddrSegmentCount(r1009Segments(101))
	if err == nil || !strings.Contains(err.Error(), "段数不能超过 100 个") {
		t.Fatalf("101 segments must be rejected naming the cap, got %v", err)
	}
	// 计数口径＝splitRemoteParts 输出（空段不计）：100 实段＋大量
	// 空段仍放行；逗号/换行混合切分同口径。
	mixed := r1009Segments(60) + ",,,\n" + strings.ReplaceAll(r1009Segments(40), ",", "\n")
	if err := validateRemoteAddrSegmentCount(mixed); err != nil {
		t.Fatalf("100 effective segments with empty separators must pass, got %v", err)
	}
}

func TestR1009ForwardCreateSegmentCapBothRoles(t *testing.T) {
	fx := setupFix7ForwardVal(t)
	fx.setSwitch(t, false)

	// 管理员面：101 段在格式层拒绝（管理员豁免的只是 netguard
	// 段校验、不是段数帽），且不落库。
	code, msg := fx.callCreate(t, 1, "admin", 0, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "r1009-admin-over", "remoteAddr": r1009Segments(101), "inPort": 15101,
	})
	if code == 0 || !strings.Contains(msg, "段数不能超过 100 个") {
		t.Fatalf("admin create 101 segments: got code=%d msg=%q, want segment-cap rejection", code, msg)
	}
	if n := fx.forwardCountNamed(t, "r1009-admin-over"); n != 0 {
		t.Fatalf("rejected create leaked a forward row: %d", n)
	}

	// 普通用户面：同一格式层拒绝（先于 netguard/DNS）。
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "r1009-member-over", "remoteAddr": r1009Segments(101), "inPort": 15102,
	})
	if code == 0 || !strings.Contains(msg, "段数不能超过 100 个") {
		t.Fatalf("member create 101 segments: got code=%d msg=%q, want segment-cap rejection", code, msg)
	}
	if n := fx.forwardCountNamed(t, "r1009-member-over"); n != 0 {
		t.Fatalf("rejected create leaked a forward row: %d", n)
	}

	// 帽内：管理员以恰 100 段创建成功。
	code, msg = fx.callCreate(t, 1, "admin", 0, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "r1009-admin-atcap", "remoteAddr": r1009Segments(100), "inPort": 15103,
	})
	if code != 0 {
		t.Fatalf("admin create 100 segments must succeed, got code=%d msg=%q", code, msg)
	}
	if n := fx.forwardCountNamed(t, "r1009-admin-atcap"); n != 1 {
		t.Fatalf("at-cap create must persist exactly one forward, got %d", n)
	}
}

func TestR1009ForwardUpdateSegmentCap(t *testing.T) {
	fx := setupFix7ForwardVal(t)
	fx.setSwitch(t, false)

	fwdID := fx.seedMemberForward(t, "r1009-upd-base", "203.0.113.5:443", 15111)

	// 更新面（回填合并值）同判：改到 101 段拒绝、存量目标不变。
	code, msg := fx.callUpdate(t, 2, "member", 1, map[string]interface{}{
		"id": fwdID, "remoteAddr": r1009Segments(101),
	})
	if code == 0 || !strings.Contains(msg, "段数不能超过 100 个") {
		t.Fatalf("update to 101 segments: got code=%d msg=%q, want segment-cap rejection", code, msg)
	}
	if got := fx.storedRemoteAddr(t, fwdID); got != "203.0.113.5:443" {
		t.Fatalf("rejected update must not rewrite stored target, got %q", got)
	}
}

// TestR1009LookupMemoizationCounting 以计数桩验证单次
// IsSafeRemoteAddr 调用内同名只解析一次，且判据语义（公网放行/
// 内网点名/解析失败文案）逐字不变。
func TestR1009LookupMemoizationCounting(t *testing.T) {
	oldBypass := DisableSafeRemoteAddrCheckForTesting
	DisableSafeRemoteAddrCheckForTesting = false
	defer func() { DisableSafeRemoteAddrCheckForTesting = oldBypass }()

	counts := map[string]int{}
	origLookup := safeRemoteLookupIP
	t.Cleanup(func() { safeRemoteLookupIP = origLookup })
	safeRemoteLookupIP = func(host string) ([]net.IP, error) {
		counts[host]++
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}

	// 101 段同名（写面帽在 handler 层，IsSafeRemoteAddr 本体不判
	// 段数）：只解析一次。
	addr := strings.TrimSuffix(strings.Repeat("memo-a.example:8080,", 101), ",")
	if err := IsSafeRemoteAddr(addr); err != nil {
		t.Fatalf("101 same-host segments must pass with public stub IP, got %v", err)
	}
	if counts["memo-a.example"] != 1 {
		t.Fatalf("same host resolved %d times in one call, want exactly 1", counts["memo-a.example"])
	}

	// 两主机交错各 50 段：各解析一次；同主机异端口共用缓存。
	counts = map[string]int{}
	mixed := strings.TrimSuffix(strings.Repeat("memo-a.example:80,memo-b.example:80,", 50), ",") + ",memo-a.example:443"
	if err := IsSafeRemoteAddr(mixed); err != nil {
		t.Fatalf("mixed two-host segments must pass, got %v", err)
	}
	if counts["memo-a.example"] != 1 || counts["memo-b.example"] != 1 {
		t.Fatalf("per-host lookups = %v, want exactly 1 per host", counts)
	}

	// 语义不变（内网解析仍点名拒绝）。
	safeRemoteLookupIP = func(host string) ([]net.IP, error) {
		counts[host]++
		return []net.IP{net.ParseIP("10.0.0.1")}, nil
	}
	err := IsSafeRemoteAddr("memo-a.example:8080")
	if err == nil || !strings.Contains(err.Error(), "resolves to internal IP") {
		t.Fatalf("internal resolution must keep the named rejection, got %v", err)
	}

	// 语义不变（解析失败文案）。
	safeRemoteLookupIP = func(host string) ([]net.IP, error) {
		return nil, fmt.Errorf("no such host")
	}
	err = IsSafeRemoteAddr("memo-a.example:8080")
	if err == nil || !strings.Contains(err.Error(), "could not resolve address") {
		t.Fatalf("resolution failure must keep the original wording, got %v", err)
	}
}
