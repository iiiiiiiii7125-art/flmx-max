package handler

import (
	"log"
	"time"
)

// R2-038（B-traffic B-T-03 实例①② ＋ BD-02）：事件型状态只挂在
// 删除/复用事件上回收、无周期收敛——存活节点的孤儿状态行与
// 节点封禁集残留单调累积。本文件是维护轮里的周期对账清扫：
//   ① traffic_stream_cursor 孤儿行按 updated_time 保留期删除
//      （repo.DeleteStaleTrafficStreamCursors）；
//   ② flmx_quota 封禁集中已删转发的残留端口，按节点当前转发
//      全集对账移除（nft_blocked_sweep.go）。
//
// 与 R2-019 的口径区别（同为错误处理、方向相反且都正确）：入账
// 面的 fail-soft 会把 DB 错误转成「已确认的空入账」、静默丢数，
// 必须 fail-closed；清扫是卫生面，单点失败的后果只是残留多留
// 一轮、下轮再清，故此处 fail-soft 是对的——逐项记日志、互不
// 拖累、绝不阻塞维护轮其余环节。清扫幂等：无残留时零写操作。

// trafficStreamCursorRetention 是游标行保留期：活跃 stream 每批
// 入账都刷新 updated_time，7 天无入账的 stream 视为已弃（删除
// 安全性见 repo.DeleteStaleTrafficStreamCursors 的注释）。
const trafficStreamCursorRetention = 7 * 24 * time.Hour

// eventStateSweepInterval 是清扫的最低间隔：维护轮每分钟跑一
// 轮，清扫本身小时级足够（残留增速为每节点每次 stream 重置一
// 行、每删一条暂停转发至多一个端口）。
var eventStateSweepInterval = time.Hour

// sweepEventStates 是维护轮的清扫入口（小时级门，标记先行：
// 本轮清扫失败也不在下一分钟立刻重试）。
func (h *Handler) sweepEventStates(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}
	h.stateSweepMu.Lock()
	if !h.stateSweepLast.IsZero() && now.Sub(h.stateSweepLast) < eventStateSweepInterval {
		h.stateSweepMu.Unlock()
		return
	}
	h.stateSweepLast = now
	h.stateSweepMu.Unlock()

	h.sweepStaleTrafficStreamCursors(now)
	h.sweepOrphanedBlockedPorts()
}

func (h *Handler) sweepStaleTrafficStreamCursors(now time.Time) {
	cutoffMs := now.Add(-trafficStreamCursorRetention).UnixMilli()
	deleted, err := h.repo.DeleteStaleTrafficStreamCursors(cutoffMs)
	if err != nil {
		log.Printf("traffic stream cursor sweep failed err=%v", err)
		return
	}
	if deleted > 0 {
		log.Printf("traffic stream cursor sweep deleted=%d cutoff_ms=%d", deleted, cutoffMs)
	}
}
