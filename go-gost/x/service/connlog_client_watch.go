package service

import (
	"sync/atomic"
	"time"
)

// C13 / R2-CONNLOG-01：转发会话的 close 事件须按真实会话生命
// 计量。上游停滞时 handler 被 15s 重试死线钉在 Dial 里，客户端
// 早已断开却无人观测，close 的 EndMs 取到 Handle 返回时刻，
// 3 秒的会话记成 15001ms。本文件在 Handle 期间另起一路轻量
// 观测：只看对端 FIN（Linux 上以 POLLRDHUP 判定，不读、不耗
// 客户端任何字节），记下客户端真正离开的时刻；close 事件的
// EndMs 取 Handle 返回与客户端离开二者之早。
//
// 观测不到（非 TCP、取不到 fd、非 Linux）时静默退化为旧行为：
// EndMs 仍为 Handle 返回时刻，绝不因观测失败丢连接或改转发。

// connlogClientWatchInterval 是客户端离开观测的轮询周期：50ms
// 决定了所记断开时刻与真实时刻的最大偏差（约 50ms）；每连接
// 每周期一次 poll 系统调用，开销可忽略。
var connlogClientWatchInterval = 50 * time.Millisecond

// connLogCloseEndMs 返回 close 事件应记的 EndMs：观测到客户端
// 离开且早于 Handle 返回时取离开时刻，否则取 Handle 返回时刻。
func connLogCloseEndMs(goneAt *atomic.Int64) int64 {
	now := time.Now().UnixMilli()
	if gone := goneAt.Load(); gone > 0 && gone < now {
		return gone
	}
	return now
}
