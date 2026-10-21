package handler

import (
	"testing"

	"go-backend/internal/kernel"
)

// S4 移植回归：snellServes 双向判定真值表（6.x 只接 v6、5.x 接 v4/v5、
// 4.x 只接 v4）。移植前 snellServes 不存在且 snellMajor 不认 6. 前缀，
// 本文件即红灯证据。
func TestSnellServes(t *testing.T) {
	for _, tc := range []struct {
		version string
		client  int
		ok      bool
	}{
		{"5.0.1", 5, true},
		{"5.0.1", 4, true}, // v5 servers accept v4 clients
		{"5.0.1", 6, false},
		{"4.1.1", 5, false},
		{"4.1.1", 4, true},
		{"6.0.0", 6, true},
		{"6.0.0", 5, false}, // v6 servers reject v4 / v5 clients
		{"6.0.0", 4, false},
	} {
		if err := snellServes(tc.version, tc.client); (err == nil) != tc.ok {
			t.Errorf("snell %s, v%d client: err = %v", tc.version, tc.client, err)
		}
	}
}

func TestSnellMajor(t *testing.T) {
	for version, want := range map[string]int{
		"4.1.1": 4,
		"5.0.1": 5,
		"6.0.0": 6,
	} {
		if got := snellMajor(version); got != want {
			t.Errorf("snellMajor(%q) = %d, want %d", version, got, want)
		}
	}
}

// 门控单一真源：Snell 内核的双向判定必须经 kernelVersionGate 在
// 保存/下发复检/换版三处同拦，而不是只在保存路径散点检查。
func TestKernelVersionGateSnell(t *testing.T) {
	mk := func(client int) *kernel.Inbound {
		in := &kernel.Inbound{Protocol: kernel.ProtocolSnell}
		in.Settings.SnellVersion = client
		return in
	}
	for _, tc := range []struct {
		version string
		client  int
		blocked bool
	}{
		{"5.0.1", 5, false},
		{"5.0.1", 4, false},
		{"5.0.1", 6, true},
		{"4.1.1", 5, true},
		{"6.0.0", 6, false},
		{"6.0.0", 5, true}, // 旧单向检查会放行此组合（6.0.0 被算成 major 5）
		{"6.0.0", 4, true},
	} {
		msg := kernelVersionGate(kernel.TypeSnell, tc.version, mk(tc.client))
		if (msg != "") != tc.blocked {
			t.Errorf("gate snell %s, v%d client: msg = %q, want blocked=%v", tc.version, tc.client, msg, tc.blocked)
		}
	}
}
