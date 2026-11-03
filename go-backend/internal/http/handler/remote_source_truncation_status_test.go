package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// C1-05：订阅源同步超 500 截断须以显式状态呈现（remoteSyncResult
// 的 Status/Truncated），不再只有 Errors 散文——手动 refresh 与后
// 台同步共用 syncRemoteSource，两面同源同结果。与手动导入的整单
// 拒绝口径不同：同步是周期性自动过程，整单拒绝会让该源节点集永
// 久冻结，故保留前 500 部分推进＋显式截断状态（见 remote_source.go
// 字段注释）。
func TestSyncTruncationSetsExplicitStatus(t *testing.T) {
	r, h, src, _ := integrityGateSetup(t)

	var b strings.Builder
	b.WriteString(integrityLinkA + "\n")
	// 唯一节点共 maxImportNodes+2：linkA + 501 条互异 bulk。
	for i := 0; i < maxImportNodes+1; i++ {
		fmt.Fprintf(&b, "vless://11111111-2222-3333-4444-555555555555@198.51.%d.%d:443?security=none#bulk-%d\n", i/250+1, i%250+1, i)
	}
	res, err := h.syncRemoteSource(context.Background(), src, b.String())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Status != remoteSyncStatusTruncated {
		t.Fatalf("status = %q, want %q (explicit truncation status, not prose only)", res.Status, remoteSyncStatusTruncated)
	}
	if res.Truncated != 2 {
		t.Fatalf("truncated = %d, want 2 dropped nodes", res.Truncated)
	}
	// 序列化形态钉格：消费方（前端 RemoteSyncResult）按此字段分支。
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"status":"truncated"`) || !strings.Contains(string(raw), `"truncated":2`) {
		t.Fatalf("marshaled result must carry the status/truncated fields: %s", raw)
	}
	if s := res.summary(); !strings.Contains(s, "2 个节点超出单轮同步上限") || !strings.Contains(s, "未同步") {
		t.Fatalf("summary must name the truncation explicitly: %s", s)
	}
	// 截断轮完整性门语义不变：未匹配旧节点保留待下轮（KeptStale）。
	if res.Removed != 0 || res.KeptStale != 1 {
		t.Fatalf("truncated round: removed=%d keptStale=%d, want 0/1", res.Removed, res.KeptStale)
	}
	// 行数：已同步 500 行＋被门保留的旧行 1 行。
	if n := sourceRowCount(t, r, src.ID); n != maxImportNodes+1 {
		t.Fatalf("source rows = %d, want %d synced + 1 kept stale", n, maxImportNodes)
	}
}

func TestSyncCleanRoundStatusOK(t *testing.T) {
	_, h, src, _ := integrityGateSetup(t)
	res, err := h.syncRemoteSource(context.Background(), src, integrityLinkA)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Status != remoteSyncStatusOK || res.Truncated != 0 {
		t.Fatalf("clean round: status=%q truncated=%d, want ok/0", res.Status, res.Truncated)
	}
}
