//go:build !linux

package service

import (
	"net"
	"sync/atomic"
)

// watchClientClose 在非 Linux 上是空操作：POLLRDHUP 是 Linux
// 专属，观测不到对端 FIN 时 close 事件的 EndMs 回退为 Handle
// 返回时刻（旧行为），不阻塞、不报错。
func watchClientClose(_ net.Conn, _ <-chan struct{}, _ *atomic.Int64) {}
