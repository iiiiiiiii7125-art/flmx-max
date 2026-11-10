package handler

import (
	"context"
	"fmt"
	"log"
	"syscall"
	"time"
)

// --- 面板本机磁盘自检（包 K）---
//
// 节点盘有 agent 上报＋越阈告警，面板本机此前却是盲区：面板
// 宿主机磁盘用量不采集、不告警，只有设置页一个被动展示的库
// 体积数字。盘满对面板与对节点同形致死（写不进 DB/日志崩溃
// 循环），故本机数据盘进同一可见性/告警面：每小时自检一次
// 数据目录所在文件系统用量，走 observeResourceSurface 同一
// 状态机与通知单漏斗（阈值 80/70、迟滞、指纹重武装口径全一
// 致），以 nodeID=0、「panel-disk」面、label「面板本机」与节
// 点面隔离。

const panelDiskCheckInterval = time.Hour

func (h *Handler) runPanelDiskCheckLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	h.checkPanelDiskUsage()
	ticker := time.NewTicker(panelDiskCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.checkPanelDiskUsage()
		}
	}
}

func (h *Handler) checkPanelDiskUsage() {
	if h == nil || h.repo == nil || h.resourceAlerts == nil {
		return
	}
	dir := h.repo.DataDirectory()
	if dir == "" {
		// Postgres 模式无本地数据目录：退到根文件系统（宿主机
		// 盘满对 PG 模式面板同样致死，自检不因模式而缺席）。
		dir = "/"
	}
	pct, ok := diskUsagePercentAt(dir)
	if !ok {
		return
	}
	label := "面板本机"
	msg := fmt.Sprintf("节点：%s\n数据盘用量：%.1f%%（目录 %s）\n盘满后服务将写不进数据库与日志。SQLite 历史表已按保留期清扫且空闲页自动回收；若仍在涨，请人工核查备份快照、旧镜像与日志（自检: df -h %s; docker images; du -sh /var/log/*）。",
		label, pct, dir, dir)
	h.observeResourceSurface(0, "panel-disk", notifyEventDiskHigh, "面板磁盘用量过高", label, pct, msg)
}

// diskUsagePercentAt returns the df-style used percentage of the
// filesystem holding path (used = blocks - bfree, over blocks;
// the same口径 as the agent's gopsutil-based DiskUsage). ok is
// false when the stat fails or the filesystem reports no blocks.
func diskUsagePercentAt(path string) (float64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		log.Printf("panel disk self-check statfs failed path=%s err=%v", path, err)
		return 0, false
	}
	return diskUsagePercent(st.Blocks, st.Bfree)
}

func diskUsagePercent(blocks, bfree uint64) (float64, bool) {
	if blocks == 0 || bfree > blocks {
		return 0, false
	}
	return float64(blocks-bfree) * 100 / float64(blocks), true
}
