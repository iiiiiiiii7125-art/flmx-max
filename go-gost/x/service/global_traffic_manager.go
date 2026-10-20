package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-gost/x/registry"
)

// GlobalTrafficManager 全局流量管理器（所有服务共享）
type GlobalTrafficManager struct {
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
	if upBytes == 0 && downBytes == 0 {
		return
	}
	if !registry.ServiceRegistry().IsRegistered(serviceName) {
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

// collectAndReport 收集所有服务流量并合并上报
func (m *GlobalTrafficManager) collectAndReport() {
	m.mu.Lock()

	if len(m.serviceTraffic) == 0 {
		m.mu.Unlock()
		return
	}

	// R5-B6 D1-010: 先收集不上报（不清零），发送成功后再清零。
	// 失败则保留，下次重试。
	type pendingItem struct {
		name string
		up   int64
		down int64
	}
	var pending []pendingItem

	for name, traffic := range m.serviceTraffic {
		traffic.mu.Lock()
		up := traffic.UpBytes
		down := traffic.DownBytes
		// D1-010: 不在此处清零。
		traffic.mu.Unlock()
		isStale := !registry.ServiceRegistry().IsRegistered(name)

		if up > 0 || down > 0 {
			pending = append(pending, pendingItem{name: name, up: up, down: down})
		}
		if isStale && up == 0 && down == 0 {
			// F-087: 无待上报流量时才可立即清理。有流量的 stale 条目
			// 必须等发送成功后再删（见下方清零段）——旧实现先删后发，
			// 发送失败时该批流量随条目一起永久丢失，违 D1-010 本意。
			delete(m.serviceTraffic, name)
		}
	}

	m.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	reportItems := make([]TrafficReportItem, 0, len(pending))
	var totalUp, totalDown int64
	for _, p := range pending {
		reportItems = append(reportItems, TrafficReportItem{N: p.name, U: p.up, D: p.down})
		totalUp += p.up
		totalDown += p.down
	}

	// R5-B6 E-001: 携带 seq 上报。
	seq := nextSeq()
	success, err := sendBatchTrafficReportWithSeq(m.ctx, reportItems, seq)
	if err != nil {
		fmt.Printf("❌ 全局流量上报失败: %v (总流量: ↑%d ↓%d, %d个服务, seq=%d)\n", err, totalUp, totalDown, len(reportItems), seq)
		// D1-010: 失败不清零，下次重试。
		return
	}

	if !success {
		fmt.Printf("⚠️ 全局流量上报未成功 (总流量: ↑%d ↓%d, %d个服务, seq=%d)\n", totalUp, totalDown, len(reportItems), seq)
		return
	}

	// D1-010: 成功后才清零。
	m.mu.Lock()
	for _, p := range pending {
		if traffic, ok := m.serviceTraffic[p.name]; ok {
			traffic.mu.Lock()
			// 只减去已上报的部分（防止并发写入丢失）。
			traffic.UpBytes -= p.up
			traffic.DownBytes -= p.down
			if traffic.UpBytes < 0 {
				traffic.UpBytes = 0
			}
			if traffic.DownBytes < 0 {
				traffic.DownBytes = 0
			}
			drained := traffic.UpBytes == 0 && traffic.DownBytes == 0
			traffic.mu.Unlock()
			// F-087: stale 服务在发送成功且计数清零后才移除条目；
			// 发送失败走上方 return，条目与计数原样保留待重试。
			if drained && !registry.ServiceRegistry().IsRegistered(p.name) {
				delete(m.serviceTraffic, p.name)
			}
		}
	}
	m.mu.Unlock()
}

// Stop 停止全局流量管理器
func (m *GlobalTrafficManager) Stop() {
	if m.reportTicker != nil {
		m.reportTicker.Stop()
	}
	if m.cancel != nil {
		m.cancel()
	}
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
