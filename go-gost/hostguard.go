package main

import (
	"fmt"

	"github.com/go-gost/x/socket"
)

// applyHostGuards 跑宿主机累积面保底（x/socket/hostguard.go，
// 包 K）：conntrack 下限/超时收敛＋journald 大小上限。与内存软
// 帽同级：每次 agent 启动必跑（新装、面板内升级换二进制、手工
// 重启全覆盖——装机脚本路径覆盖不到的面板内升级正是靠这一
// 跑），幂等、只往安全区单向收敛、失败只打印诚实说明续行，
// 绝不阻断启动。nf_conntrack 在启动后才加载的情形由指标上报
// 的惰性再检兜底（websocket_reporter 的 collectSystemInfo）。
func applyHostGuards() {
	for _, line := range socket.EnsureConntrackFloor(true).Lines {
		fmt.Printf("[flmx] %s\n", line)
	}
	for _, line := range socket.EnsureJournaldCap().Lines {
		fmt.Printf("[flmx] %s\n", line)
	}
}
