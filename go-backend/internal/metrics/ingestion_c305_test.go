package metrics

import (
	"math"
	"testing"
)

// C3-05: 入参消毒——回绕计数器整条拒记、百分比钳制、负速度/
// 连接数/负载归 0。

func TestC305RejectsWrappingCounters(t *testing.T) {
	svc := NewIngestionService(nil)
	svc.RecordNodeMetric(7, SystemInfo{BytesReceived: math.MaxUint64, BytesTransmitted: 10, Uptime: 5})
	svc.RecordNodeMetric(7, SystemInfo{BytesReceived: 10, BytesTransmitted: 10, Uptime: math.MaxUint64})
	if len(svc.nodeBuffer) != 0 {
		t.Fatalf("samples with int64-wrapping counters must be rejected, buffered %d", len(svc.nodeBuffer))
	}
	if _, ok := svc.lastBytes[7]; ok {
		t.Fatal("rejected samples must not advance the traffic baseline")
	}
}

func TestC305ClampsOutOfRangeValues(t *testing.T) {
	svc := NewIngestionService(nil)
	svc.RecordNodeMetric(7, SystemInfo{
		BytesReceived: 100, BytesTransmitted: 200, Uptime: 5,
		CPUUsage: 250.5, MemoryUsage: -3, DiskUsage: math.NaN(),
		Load1: -1.5, Load5: 0.5,
		TCPConns: -9, UDPConns: 3,
		NetInSpeed: -100, NetOutSpeed: 42,
	})
	if len(svc.nodeBuffer) != 1 {
		t.Fatalf("valid sample must be recorded, buffered %d", len(svc.nodeBuffer))
	}
	m := svc.nodeBuffer[0]
	if m.CPUUsage != 100 || m.MemUsage != 0 || m.DiskUsage != 0 {
		t.Fatalf("percentages not clamped: cpu=%v mem=%v disk=%v", m.CPUUsage, m.MemUsage, m.DiskUsage)
	}
	if m.Load1 != 0 || m.Load5 != 0.5 || m.TCPConns != 0 || m.UDPConns != 3 || m.NetInSpeed != 0 || m.NetOutSpeed != 42 {
		t.Fatalf("negative values not clamped to 0: %+v", m)
	}
}

