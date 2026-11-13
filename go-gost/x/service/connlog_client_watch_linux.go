//go:build linux

package service

import (
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// watchClientClose 观测 TCP 客户端连接的对端离开：每周期对底
// 层 fd 做一次短超时 poll，只关心 POLLRDHUP（对端已 FIN 或
// RST）、POLLHUP 与 POLLERR。POLLRDHUP 的关键性质是即使客户
// 端先发了数据再断开也能判定——未读数据会挡住 MSG_PEEK 的
// EOF 判定，但挡不住 RDHUP；且整个过程不读任何字节，绝不消
// 耗客户端的请求数据，也不干扰 handler 的读写。
//
// 观测到离开时把时刻（毫秒）写入 goneAt 并返回；done 关闭、
// fd 已失效（POLLNVAL）或 poll 出错时静默返回，不写 goneAt。
func watchClientClose(conn net.Conn, done <-chan struct{}, goneAt *atomic.Int64) {
	sc, ok := conn.(interface{ SyscallConn() (syscall.RawConn, error) })
	if !ok {
		return
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return
	}
	timeoutMs := int(connlogClientWatchInterval / time.Millisecond)
	if timeoutMs <= 0 {
		timeoutMs = 50
	}
	for {
		select {
		case <-done:
			return
		default:
		}
		// 每轮现取 fd：连接关闭后 Control 即失败，绝不在已
		// 回收的 fd 号上继续 poll（fd 号复用会误判他连接）。
		var fd uintptr
		if err := raw.Control(func(f uintptr) { fd = f }); err != nil {
			return
		}
		fds := []unix.PollFd{{
			Fd:     int32(fd),
			Events: unix.POLLRDHUP | unix.POLLERR | unix.POLLHUP,
		}}
		n, err := unix.Poll(fds, timeoutMs)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		revents := fds[0].Revents
		if revents&unix.POLLNVAL != 0 {
			return
		}
		if revents&(unix.POLLRDHUP|unix.POLLHUP|unix.POLLERR) != 0 {
			goneAt.Store(time.Now().UnixMilli())
			return
		}
	}
}
