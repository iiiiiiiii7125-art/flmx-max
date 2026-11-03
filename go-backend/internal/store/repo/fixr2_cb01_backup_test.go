package repo

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// R2-F2-C-B01 钉格：备份恢复出的「出站 IP 策略 × 链式下一跳字面 IP」
// 冲突组合在保存面会被拒绝，导入面不改恢复语义（行与路由列原样恢
// 复），但必须在 ImportWarnings 点名——修复前恢复全程无声，冲突组合
// 落库后在渲染期静默丢弃链式流量。
func TestFixR2CB01BackupImportDisclosesChainPolicyConflict(t *testing.T) {
	now := time.Now().UnixMilli()
	ssSettings := `{"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}`

	seedNodes := func(t *testing.T, r *Repository) {
		t.Helper()
		for _, n := range []struct {
			id int64
			ip string
		}{{1, "198.51.100.51"}, {2, "203.0.113.51"}} {
			createRow(t, r, &model.Node{ID: n.id, Name: "n", Secret: "s", ServerIP: n.ip, Port: "26000-26100",
				CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
		}
		createRow(t, r, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "singbox", Version: "latest", Enabled: 1, CreatedTime: now})
		createRow(t, r, &model.NodeKernel{ID: 2, NodeID: 2, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now})
	}

	src, err := Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	seedNodes(t, src)

	mkInbound := func(kID, nodeID int64, name string, port int, shareHost string, outboundID int64, strategy string) *model.NodeInbound {
		row := &model.NodeInbound{KernelID: kID, NodeID: nodeID, Name: name, Protocol: "shadowsocks", Port: port,
			Settings: ssSettings, ShareHost: shareHost, OutboundID: outboundID, OutboundIPStrategy: strategy,
			CreatedTime: now, UpdatedTime: now}
		if err := src.CreateNodeInbound(row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	target4 := mkInbound(2, 2, "cb-target4", 21001, "203.0.113.99", 0, "")
	target6 := mkInbound(2, 2, "cb-target6", 21002, "2001:db8::9", 0, "")
	// 冲突：ipv6_only 引用方 × IPv4 字面下一跳。
	conflicting := mkInbound(1, 1, "cb-referrer", 21003, "", target4.ID, kernel.OutboundIPv6Only)
	// 对照：ipv4_only × IPv4 目标、ipv6_only × IPv6 目标均合法。
	mkInbound(1, 1, "cb-ok4", 21004, "", target4.ID, kernel.OutboundIPv4Only)
	mkInbound(1, 1, "cb-ok6", 21005, "", target6.ID, kernel.OutboundIPv6Only)

	backup, err := src.ExportPartial([]string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	seedNodes(t, dst)

	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}

	var conflictWarnings []string
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "链式流量将被丢弃") {
			conflictWarnings = append(conflictWarnings, w)
		}
	}
	if len(conflictWarnings) != 1 {
		t.Fatalf("exactly one chain-conflict disclosure expected, got %v (all warnings: %v)", conflictWarnings, result.ImportWarnings)
	}
	w := conflictWarnings[0]
	if !strings.Contains(w, "cb-referrer") {
		t.Fatalf("disclosure must name the conflicting referrer, got %q", w)
	}
	for _, ok := range []string{"cb-ok4", "cb-ok6"} {
		if strings.Contains(w, ok) {
			t.Fatalf("disclosure must not name non-conflicting referrer %q, got %q", ok, w)
		}
	}

	// 只披露不改行：冲突组合原样恢复（策略与出站引用逐字保留）。
	got, err := dst.GetNodeInbound(conflicting.ID)
	if err != nil || got == nil {
		t.Fatalf("restored referrer: %v", err)
	}
	if got.OutboundIPStrategy != kernel.OutboundIPv6Only || got.OutboundID != target4.ID {
		t.Fatalf("restore semantics must stay unchanged (disclosure only), got strategy=%q outbound=%d", got.OutboundIPStrategy, got.OutboundID)
	}
}
