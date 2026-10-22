package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

// failListNodeInboundsAfterFirst 在首次 ListNodeInbounds 之后让其
// 全部失败（GORM 查询回调 + 取消语句 context + AddError 双保险），
// 用于把故障精确注入同步流程的「第二次列表查询」点（更新分支去重
// 复检 / 末尾计数），而流程首查仍成功。校验注入有效性由用例内的
// 前置断言承担（注入未生效则用例先行失败、不会假绿）。
func failListNodeInboundsAfterFirst(t *testing.T, r *repo.Repository) {
	t.Helper()
	var count atomic.Int32
	err := r.DB().Callback().Query().Before("gorm:query").Register("r4b7_fail_list_after_first", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.NodeInbound); ok {
			if count.Add(1) > 1 {
				ctx, cancel := context.WithCancel(tx.Statement.Context)
				tx.Statement.Context = ctx
				cancel()
				tx.AddError(errors.New("r4b7 injected list failure"))
			}
		}
	})
	if err != nil {
		t.Fatalf("register fault callback: %v", err)
	}
}

// seedExternalRow 按生产口径（ParseImport → externalRow）落一条导入行。
func seedExternalRow(t *testing.T, r *repo.Repository, link string, sourceID int64, now int64) *model.NodeInbound {
	t.Helper()
	nodes, perrs := kernel.ParseImport(link)
	if len(nodes) != 1 {
		t.Fatalf("parse %q: nodes=%d errs=%v", link, len(nodes), perrs)
	}
	row, err := externalRow(nodes[0], false, now)
	if err != nil {
		t.Fatalf("externalRow: %v", err)
	}
	row.SourceID = sourceID
	if err := r.DB().Create(&row).Error; err != nil {
		t.Fatalf("create row: %v", err)
	}
	return &row
}

// R4 批7 · R4X-007 主点：syncRemoteSource 删除守卫 ListInboundRelays
// 查询失败必须 fail-closed（记 res.Errors + Kept + 不删），与紧邻
// chainReferrers 已修分支同形。故障注入：改名 forward 表——
// ListInboundRelays（JOIN forward）必失败，而 ListNodeInbounds /
// chainReferrers / DeleteNodeInbound 均不触 forward 表，恰好隔离
// 本守卫（B 报告 §7 登记的 repo 故障注入用例在此落地）。
// 红态（修复前）：err 被 `err == nil &&` 折叠为「无中转」，受害
// 节点连同 relay 行与 user_inbound 分配行被级联删除。
func TestR4B7SyncKeepsRelayedNodeWhenRelayQueryFails(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r4b7.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	src := &model.RemoteSource{Name: "源", Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source: %v", err)
	}
	victim := &model.NodeInbound{
		Name: "node-victim", Protocol: "ss", Listen: "203.0.113.5", Port: 8388,
		SourceID: src.ID, KernelID: 0, Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.DB().Create(victim).Error; err != nil {
		t.Fatalf("create victim: %v", err)
	}
	relay := &model.NodeInboundRelay{InboundID: victim.ID, ForwardID: 4242, Name: "relay", Managed: 1, CreatedTime: now}
	if err := r.DB().Create(relay).Error; err != nil {
		t.Fatalf("create relay: %v", err)
	}
	if err := r.DB().Create(&model.UserInbound{UserID: 7, InboundID: victim.ID, RelayID: relay.ID}).Error; err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// 故障注入：forward 表改名（ListInboundRelays 的 JOIN 必失败）。
	if err := r.DB().Exec("ALTER TABLE forward RENAME TO forward_r4b7_bak").Error; err != nil {
		t.Fatalf("rename forward: %v", err)
	}
	defer func() { _ = r.DB().Exec("ALTER TABLE forward_r4b7_bak RENAME TO forward").Error }()
	if _, err := r.ListInboundRelays(victim.ID); err == nil {
		t.Fatal("injection sanity: ListInboundRelays must fail after forward rename")
	}

	// 同步内容只含一个新节点，受害节点「已从源消失 + 挂中转 + 无链式引用」。
	body := "vless://11111111-2222-3333-4444-555555555555@203.0.113.99:443?security=none#node-new"
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	_ = r.DB().Exec("ALTER TABLE forward_r4b7_bak RENAME TO forward")

	if row, err := r.GetNodeInbound(victim.ID); err != nil || row == nil {
		t.Fatalf("R4X-007: relayed node must be KEPT when relay guard query fails, got row=%v err=%v (removed=%d kept=%d errors=%v)", row, err, res.Removed, res.Kept, res.Errors)
	}
	var relayCount, assignCount int64
	if err := r.DB().Model(&model.NodeInboundRelay{}).Where("inbound_id = ?", victim.ID).Count(&relayCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Model(&model.UserInbound{}).Where("inbound_id = ?", victim.ID).Count(&assignCount).Error; err != nil {
		t.Fatal(err)
	}
	if relayCount != 1 || assignCount != 1 {
		t.Fatalf("relay/assignment rows must survive, got relay=%d assign=%d", relayCount, assignCount)
	}
	if res.Kept < 1 {
		t.Fatalf("res.Kept = %d, want >= 1", res.Kept)
	}
	joined := strings.Join(res.Errors, " | ")
	if !strings.Contains(joined, "node-victim") {
		t.Fatalf("res.Errors must record the guard failure for node-victim, got %v", res.Errors)
	}
	if res.Removed != 0 {
		t.Fatalf("res.Removed = %d, want 0", res.Removed)
	}
}

// R4 批7 · R4X-008 面板创建/更新面：nodeId>0 的服务监控补硬拒子集
// ——元数据目标创建/更新被拒；LAN/回环目标正常创建（防过修回归）。
// 红态（修复前）：nodeId>0 时创建面明文跳过一切校验，元数据目标
// code=0 落库。
func TestR4B7MonitorNodeTargetHardDenyCreateUpdate(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r4b7mon.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	adminID, err := r.CreateUser("r4b7-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	node := &model.Node{Name: "exec-node", Secret: "s", ServerIP: "203.0.113.1", Port: "31000-31010", Status: 1}
	if err := r.DB().Create(node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	call := func(fn func(http.ResponseWriter, *http.Request), body string) envelope {
		w := httptest.NewRecorder()
		fn(w, claimsCtxReq(http.MethodPost, "/api/v1/monitor/services/x", "", body, adminID, 0))
		return decodeEnvelope(t, w)
	}
	create := func(name, typ, target string) envelope {
		return call(h.monitorServiceCreate, fmt.Sprintf(`{"name":%q,"type":%q,"target":%q,"intervalSec":60,"timeoutSec":5,"nodeId":%d}`, name, typ, target, node.ID))
	}

	if env := create("meta-tcp", "tcp", "169.254.169.254:80"); env.Code == 0 {
		t.Fatalf("R4X-008: node tcp monitor to metadata must be rejected, got code=0")
	}
	if env := create("meta-ll", "tcp", "169.254.0.23:443"); env.Code == 0 {
		t.Fatalf("R4X-008: node tcp monitor to link-local segment must be rejected, got code=0")
	}
	if env := create("meta-icmp", "icmp", "169.254.169.254"); env.Code == 0 {
		t.Fatalf("R4X-008: node icmp monitor to metadata must be rejected, got code=0")
	}
	// LAN/回环/私网正常创建（防过修）。
	if env := create("lan-tcp", "tcp", "192.168.1.1:443"); env.Code != 0 {
		t.Fatalf("LAN tcp monitor must be created, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env := create("loop-icmp", "icmp", "127.0.0.1"); env.Code != 0 {
		t.Fatalf("loopback icmp monitor must be created, got code=%d msg=%q", env.Code, env.Msg)
	}
	monitors, err := r.ListServiceMonitors()
	if err != nil || len(monitors) != 2 {
		t.Fatalf("exactly the 2 allowed monitors must persist, got %d err=%v", len(monitors), err)
	}
	var lanID int64
	for _, m := range monitors {
		if m.Target == "192.168.1.1:443" {
			lanID = m.ID
		}
	}
	if lanID == 0 {
		t.Fatal("lan monitor not found")
	}
	// 更新面：把 LAN 目标改成元数据 → 拒；改成另一 LAN 目标 → 成功。
	if env := call(h.monitorServiceUpdate, fmt.Sprintf(`{"id":%d,"target":"169.254.169.254:80"}`, lanID)); env.Code == 0 {
		t.Fatalf("R4X-008: update to metadata target must be rejected, got code=0")
	}
	if env := call(h.monitorServiceUpdate, fmt.Sprintf(`{"id":%d,"target":"10.0.0.8:22"}`, lanID)); env.Code != 0 {
		t.Fatalf("update to LAN target must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	got, err := r.GetServiceMonitor(lanID)
	if err != nil || got == nil || got.Target != "10.0.0.8:22" {
		t.Fatalf("stored target = %+v err=%v, want 10.0.0.8:22", got, err)
	}
}

// R4X-007 普查顺改（B 表二去重门 fail-open）：更新分支保存前去重复检
// 查询失败必须中止保存。红态（修复前）：err 被 `err == nil` 门吞掉，
// 行被无条件改写到与另一导入节点相同的地址（重复行落库）。
func TestR4B7SyncDedupRecheckFailureAbortsSave(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r4b7dedup.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	src := &model.RemoteSource{Name: "源", Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source: %v", err)
	}
	linkA := "vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=none#node-a"
	rowA := seedExternalRow(t, r, linkA, src.ID, now)
	// 另一导入节点占据目标地址（更新后将与之撞址）。
	seedExternalRow(t, r, "vless://11111111-2222-3333-4444-555555555555@203.0.113.20:443?security=none#node-b", 0, now)

	failListNodeInboundsAfterFirst(t, r)

	// 同步内容：node-a 换址到 node-b 的地址（走更新分支 → 去重复检点）。
	body := "vless://11111111-2222-3333-4444-555555555555@203.0.113.20:443?security=none#node-a"
	res, err := h.syncRemoteSource(context.Background(), src, body)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Updated != 0 {
		t.Fatalf("dedup recheck failure must abort the save, res.Updated = %d (errors=%v)", res.Updated, res.Errors)
	}
	if !strings.Contains(strings.Join(res.Errors, " | "), "node-a") {
		t.Fatalf("res.Errors must record the aborted save for node-a, got %v", res.Errors)
	}
	got, err := r.GetNodeInbound(rowA.ID)
	if err != nil || got == nil {
		t.Fatalf("reload rowA: %v", err)
	}
	if got.Listen != "203.0.113.10" {
		t.Fatalf("rowA listen = %q, want unchanged 203.0.113.10 (no duplicate row written)", got.Listen)
	}
}

// B4-S3（随 R4X-007 批顺手）：同步末尾计数查询失败时跳过状态写回，
// 不得把 node_count 写 0。红态（修复前）：计数失败仍无条件写回
// node_count=0（且 last_error 被清空）。
func TestR4B7SyncTailCountFailureSkipsStatusWrite(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r4b7tail.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	src := &model.RemoteSource{Name: "源", Kind: model.RemoteSourceFile, Enabled: 1, NodeCount: 7, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source: %v", err)
	}
	link := "vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?security=none#node-a"
	seedExternalRow(t, r, link, src.ID, now)

	failListNodeInboundsAfterFirst(t, r)

	// 同步内容与存量完全一致（节点命中 used、无更新无删除），流程
	// 第二次列表查询即末尾计数——注入失败点。
	res, err := h.syncRemoteSource(context.Background(), src, link)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	_ = res
	got, err := r.GetRemoteSource(src.ID)
	if err != nil || got == nil {
		t.Fatalf("reload source: %v", err)
	}
	if got.NodeCount != 7 {
		t.Fatalf("B4-S3: tail count failure must skip status write, node_count = %d, want preserved 7", got.NodeCount)
	}
}
