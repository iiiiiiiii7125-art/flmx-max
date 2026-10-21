package repo_test

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// FIX-D（1.3.2 审计 B5/零值家族收口）：GORM `default:1` 列在 Create 时会把
// 显式 0 当未设置丢弃、由列默认值 1 接管。1.3.1 只修了 NodeInbound 创建与
// 五处备份恢复路径，本文件锁定其余创建路径：
//   - A2-02 公告首次创建 enabled=0 被吞
//   - A2-03 服务监控创建 enabled=0 被吞
//   - A13-01 TunnelQuality.Success=0（失败记录）被吞、恒落库 1
//   - NodeKernel 创建（与 NodeInbound 同族，仓库层一并收口）
// 对照组 NodeInbound 创建（BUG-A4-1 已修）必须保持绿色。

func openZeroValueRepo(t *testing.T) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "zero-value.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestZeroValueAnnouncementFirstCreateDisabled(t *testing.T) {
	r := openZeroValueRepo(t)
	now := time.Now().UnixMilli()

	if err := r.UpsertAnnouncement("hello", 0, now); err != nil {
		t.Fatalf("upsert announcement: %v", err)
	}
	ann, err := r.GetAnnouncement()
	if err != nil || ann == nil {
		t.Fatalf("get announcement: %v (%v)", ann, err)
	}
	if ann.Enabled != 0 {
		t.Fatalf("A2-02: first-create announcement with enabled=0 stored enabled=%d, want 0", ann.Enabled)
	}

	// 已有行更新路径（map 写）本来就能写 0/1，锁定不回退。
	if err := r.UpsertAnnouncement("hello2", 1, now+1); err != nil {
		t.Fatalf("upsert announcement 1: %v", err)
	}
	ann, _ = r.GetAnnouncement()
	if ann == nil || ann.Enabled != 1 || ann.Content != "hello2" {
		t.Fatalf("announcement update to enabled=1: %+v", ann)
	}
	if err := r.UpsertAnnouncement("hello3", 0, now+2); err != nil {
		t.Fatalf("upsert announcement 0: %v", err)
	}
	ann, _ = r.GetAnnouncement()
	if ann == nil || ann.Enabled != 0 {
		t.Fatalf("announcement update to enabled=0: %+v", ann)
	}
}

func TestZeroValueAnnouncementFirstCreateEnabled(t *testing.T) {
	r := openZeroValueRepo(t)
	now := time.Now().UnixMilli()
	if err := r.UpsertAnnouncement("hi", 1, now); err != nil {
		t.Fatalf("upsert announcement: %v", err)
	}
	ann, err := r.GetAnnouncement()
	if err != nil || ann == nil {
		t.Fatalf("get announcement: %v (%v)", ann, err)
	}
	if ann.Enabled != 1 {
		t.Fatalf("first-create announcement with enabled=1 stored enabled=%d, want 1", ann.Enabled)
	}
}

func TestZeroValueServiceMonitorCreateDisabled(t *testing.T) {
	r := openZeroValueRepo(t)
	now := time.Now().UnixMilli()

	m := &model.ServiceMonitor{
		Name: "disabled-monitor", Type: "http", Target: "https://example.com",
		IntervalSec: 60, TimeoutSec: 5, Enabled: 0,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateServiceMonitor(m); err != nil {
		t.Fatalf("create service monitor: %v", err)
	}
	if m.Enabled != 0 {
		t.Fatalf("A2-03: created monitor struct enabled=%d, want 0", m.Enabled)
	}
	got, err := r.GetServiceMonitor(m.ID)
	if err != nil || got == nil {
		t.Fatalf("get service monitor: %v (%v)", got, err)
	}
	if got.Enabled != 0 {
		t.Fatalf("A2-03: created monitor stored enabled=%d, want 0", got.Enabled)
	}

	m2 := &model.ServiceMonitor{
		Name: "enabled-monitor", Type: "http", Target: "https://example.com",
		IntervalSec: 60, TimeoutSec: 5, Enabled: 1,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateServiceMonitor(m2); err != nil {
		t.Fatalf("create enabled monitor: %v", err)
	}
	got2, err := r.GetServiceMonitor(m2.ID)
	if err != nil || got2 == nil || got2.Enabled != 1 {
		t.Fatalf("enabled monitor stored: %+v err=%v", got2, err)
	}
}

func TestZeroValueTunnelQualityFailurePersists(t *testing.T) {
	r := openZeroValueRepo(t)
	now := time.Now().UnixMilli()

	fail := &model.TunnelQuality{TunnelID: 42, Success: 0, ErrorMessage: "boom", Timestamp: now}
	if err := r.InsertTunnelQuality(fail); err != nil {
		t.Fatalf("insert failure quality: %v", err)
	}
	if fail.Success != 0 {
		t.Fatalf("A13-01: inserted failure struct success=%d, want 0", fail.Success)
	}
	ok := &model.TunnelQuality{TunnelID: 42, Success: 1, Timestamp: now + 1}
	if err := r.InsertTunnelQuality(ok); err != nil {
		t.Fatalf("insert success quality: %v", err)
	}
	rows, err := r.GetTunnelQualityHistory(42, 0, now+10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history rows = %d, want 2", len(rows))
	}
	if rows[0].Success != 0 {
		t.Fatalf("A13-01: failure sample stored success=%d, want 0", rows[0].Success)
	}
	if rows[1].Success != 1 {
		t.Fatalf("success sample stored success=%d, want 1", rows[1].Success)
	}
}

func TestZeroValueNodeKernelCreateDisabled(t *testing.T) {
	r := openZeroValueRepo(t)
	now := time.Now().UnixMilli()

	k := &model.NodeKernel{
		NodeID: 7, KernelType: "xray", Version: "latest", Status: "pending",
		Enabled: 0, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatalf("create node kernel: %v", err)
	}
	got, err := r.GetNodeKernelByType(7, "xray")
	if err != nil || got == nil {
		t.Fatalf("get node kernel: %v (%v)", got, err)
	}
	if got.Enabled != 0 {
		t.Fatalf("zero-value family: created kernel stored enabled=%d, want 0", got.Enabled)
	}
}

func TestZeroValueNodeInboundCreateDisabledControl(t *testing.T) {
	r := openZeroValueRepo(t)
	now := time.Now().UnixMilli()

	in := &model.NodeInbound{
		KernelID: 1, NodeID: 7, Name: "disabled-inbound", Protocol: "vless",
		Listen: "::", Port: 443, Enabled: 0, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateNodeInbound(in); err != nil {
		t.Fatalf("create node inbound: %v", err)
	}
	got, err := r.GetNodeInbound(in.ID)
	if err != nil || got == nil {
		t.Fatalf("get node inbound: %v (%v)", got, err)
	}
	if got.Enabled != 0 {
		t.Fatalf("control (BUG-A4-1): created inbound stored enabled=%d, want 0", got.Enabled)
	}
}
