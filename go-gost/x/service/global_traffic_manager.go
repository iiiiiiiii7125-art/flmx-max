package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-gost/x/registry"
)

// GlobalTrafficManager 全局流量管理器（所有服务共享）
//
// 上报链（可靠上报 spool）：计数器只是采样缓冲，每个上报周期先经
// persistTraffic 把快照落盘进 spool（先落盘后清零），再发 spool 的
// 待确认批次；面板回执确认后才清批次并推进序号。进程重启/崩溃后
// 从 spool 续报，未确认流量不丢；面板游标回退等异常走三路自愈
// （见 traffic_spool.go 的 recoverRejected）；本机状态被外部回
// 退致面板游标超前时，回执 cursor 触发 stream 轮换自愈（见
// traffic_spool.go 的 confirmAck，C-C2-01）。
type GlobalTrafficManager struct {
	reportMu       sync.Mutex
	spool          *trafficSpool
	mu             sync.RWMutex
	serviceTraffic map[string]*ServiceTraffic // key: 服务名, value: 流量数据
	ctx            context.Context
	cancel         context.CancelFunc
	reportTicker   *time.Ticker
}

// ServiceTraffic 单个服务的流量累积
type ServiceTraffic struct {
	mu          sync.Mutex
	ServiceName string
	UpBytes     int64 // 上行流量（累积）
	DownBytes   int64 // 下行流量（累积）
}

var (
	globalManager     *GlobalTrafficManager
	globalManagerOnce sync.Once
)

// GetGlobalTrafficManager 获取全局流量管理器单例
func GetGlobalTrafficManager() *GlobalTrafficManager {
	globalManagerOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		globalManager = &GlobalTrafficManager{
			serviceTraffic: make(map[string]*ServiceTraffic),
			spool:          configuredTrafficSpool,
			ctx:            ctx,
			cancel:         cancel,
			reportTicker:   time.NewTicker(5 * time.Second),
		}
		// 启动定时上报协程
		go globalManager.startReporting()
	})
	return globalManager
}

// AddTraffic 添加流量到指定服务（由各服务调用）
func (m *GlobalTrafficManager) AddTraffic(serviceName string, upBytes, downBytes int64) {
	if !registry.ServiceRegistry().IsRegistered(serviceName) {
		return
	}
	m.addServiceTraffic(serviceName, upBytes, downBytes)
}

// addServiceTraffic 接受服务自身生命周期内的采样，包括监听已注销后
// 仍在排空的连接——注销不等于字节不存在，照样要计费落盘。
func (m *GlobalTrafficManager) addServiceTraffic(serviceName string, upBytes, downBytes int64) {
	if upBytes == 0 && downBytes == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 获取或创建服务流量记录
	traffic, exists := m.serviceTraffic[serviceName]
	if !exists {
		traffic = &ServiceTraffic{
			ServiceName: serviceName,
		}
		m.serviceTraffic[serviceName] = traffic
	}

	// 累加流量
	traffic.mu.Lock()
	traffic.UpBytes += upBytes
	traffic.DownBytes += downBytes
	traffic.mu.Unlock()
}

// RemoveServices drops cached traffic counters for services that no longer exist.
func (m *GlobalTrafficManager) RemoveServices(serviceNames ...string) {
	if m == nil || len(serviceNames) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, name := range serviceNames {
		if m.isTrafficEmptyLocked(name) {
			delete(m.serviceTraffic, name)
		}
	}
}

// RetainServices removes traffic counters for every service not in activeNames.
func (m *GlobalTrafficManager) RetainServices(activeNames map[string]struct{}) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for name := range m.serviceTraffic {
		if _, ok := activeNames[name]; !ok {
			if m.isTrafficEmptyLocked(name) {
				delete(m.serviceTraffic, name)
			}
		}
	}
}

func (m *GlobalTrafficManager) isTrafficEmptyLocked(name string) bool {
	traffic, ok := m.serviceTraffic[name]
	if !ok {
		return true
	}

	traffic.mu.Lock()
	defer traffic.mu.Unlock()
	return traffic.UpBytes == 0 && traffic.DownBytes == 0
}

// startReporting 启动定时上报协程（每5秒执行一次）
func (m *GlobalTrafficManager) startReporting() {

	for {
		select {
		case <-m.reportTicker.C:
			m.collectAndReport()

		case <-m.ctx.Done():
			fmt.Printf("⏹️ 全局流量上报器已停止\n")
			return
		}
	}
}

// collectAndReport 落盘待上报流量并发送 spool 的待确认批次。
func (m *GlobalTrafficManager) collectAndReport() {
	m.reportMu.Lock()
	defer m.reportMu.Unlock()
	if err := m.persistTraffic(); err != nil {
		fmt.Printf("❌ 流量 spool 落盘失败: %v\n", err)
		return
	}
	if m.spool.state.Pending == nil {
		return
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	pending := *m.spool.state.Pending
	panelCursor, err := sendReliableTrafficReport(ctx, pending)
	if err != nil {
		var rejection trafficReportRejection
		if errors.As(err, &rejection) {
			if recoverErr := m.spool.recoverRejected(rejection); recoverErr != nil {
				fmt.Printf("❌ 流量批次自愈落盘失败: %v\n", recoverErr)
			}
		}
		fmt.Printf("❌ 全局流量上报失败: %v (批次: %d 个服务, stream=%s seq=%d)\n", err, len(pending.Items), pending.Stream, pending.Sequence)
		return
	}
	// 面板已确认：清批次＋推进序号先落盘再替换内存状态（confirmAck）。
	// 落盘失败时内存仍保留待确认批次，下个周期重发——面板对同
	// stream+seq+同内容判重放回确认，不会重复入账。回执游标超
	// 前于所发序号（本机状态被外部回退）时 confirmAck 轮换
	// stream，已确认字节不重发、后续字节改走新 stream 入账。
	rotated, err := m.spool.confirmAck(panelCursor)
	if err != nil {
		fmt.Printf("❌ 流量确认状态落盘失败: %v\n", err)
		return
	}
	if rotated {
		fmt.Printf("⚠️ 面板流量游标(%d)超前于本机序号(%d)：spool 状态疑似被外部回退（整机快照恢复/旧备份还原/整目录克隆），已切换新 stream %s 重新入账；本批已确认流量不重发，后续流量改走新 stream\n", panelCursor, pending.Sequence, m.spool.state.Stream)
	}
}

// persistTraffic 把内存计数快照落盘进 spool：只有落盘成功才清零
// 计数（先落盘后清零）。待确认批次存在时新快照只进 Buffered 不
// 并入待确认批次——重发必须与首发同 stream、同 seq、同字节，
// 期间新计入的流量等本批确认后另组新批。二分恢复的 Retry 队列
// 优先于 Buffered 组批。
func (m *GlobalTrafficManager) persistTraffic() error {
	if m.spool == nil {
		if configuredTrafficSpool != nil {
			m.spool = configuredTrafficSpool
		} else {
			var err error
			m.spool, err = openTrafficSpool("")
			if err != nil {
				return err
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]TrafficReportItem, 0, len(m.serviceTraffic))
	for name, traffic := range m.serviceTraffic {
		traffic.mu.Lock()
		up, down := traffic.UpBytes, traffic.DownBytes
		traffic.mu.Unlock()
		if up > 0 || down > 0 {
			items = append(items, TrafficReportItem{N: name, U: up, D: down})
		}
	}
	if len(items) == 0 && (m.spool.state.Pending != nil || (len(m.spool.state.Buffered) == 0 && len(m.spool.state.Retry) == 0)) {
		return nil
	}

	next := m.spool.state
	next.Buffered = mergeTrafficItems(next.Buffered, items)
	if next.Pending == nil {
		var batchItems []TrafficReportItem
		if len(next.Retry) > 0 {
			batchItems, next.Retry = next.Retry[0], next.Retry[1:]
		} else {
			batchItems, next.Buffered = next.Buffered, nil
		}
		if len(batchItems) > 0 {
			next.Pending = &trafficEnvelope{Stream: next.Stream, Sequence: next.Next, Items: batchItems}
		}
	}
	if err := m.spool.save(next); err != nil {
		return err
	}
	m.spool.state = next
	// 落盘成功才清零；已注销服务的条目此时可删——其字节已在
	// spool 里，不会因删条目丢失（F-087 的保证由落盘接管）。
	for name, traffic := range m.serviceTraffic {
		traffic.mu.Lock()
		traffic.UpBytes, traffic.DownBytes = 0, 0
		traffic.mu.Unlock()
		if !registry.ServiceRegistry().IsRegistered(name) {
			delete(m.serviceTraffic, name)
		}
	}
	return nil
}

// Stop 停止全局流量管理器：停 ticker 后把计数器里的尾量落盘进
// spool（不在停机瞬间强行发送——面板同时不可达时发送必丢，落盘
// 续报才是真不丢），下次启动由 collectAndReport 续报。
func (m *GlobalTrafficManager) Stop() {
	if m.reportTicker != nil {
		m.reportTicker.Stop()
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.reportMu.Lock()
	if err := m.persistTraffic(); err != nil {
		fmt.Printf("❌ 流量停机落盘失败: %v\n", err)
	}
	m.reportMu.Unlock()
	fmt.Printf("🛑 全局流量管理器已停止\n")
}

// GetServiceTraffic 获取指定服务的当前流量（用于调试）
func (m *GlobalTrafficManager) GetServiceTraffic(serviceName string) (upBytes, downBytes int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if traffic, exists := m.serviceTraffic[serviceName]; exists {
		traffic.mu.Lock()
		upBytes = traffic.UpBytes
		downBytes = traffic.DownBytes
		traffic.mu.Unlock()
	}
	return
}
