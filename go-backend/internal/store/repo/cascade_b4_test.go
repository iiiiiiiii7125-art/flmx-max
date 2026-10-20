package repo

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// B4 级联与台账修复的 repo 层用例：
//   - F-010 删节点漏清 user_inbound
//   - F-011 删用户漏清 user_subscription
//   - F-057 删用户漏清 nft 台账三表 + 入站中转行
//   - F-017 pending-delete 24h 耗尽后仍被到期查询扫出（永续重试）
//   - F-072 LIKE 前缀通配符未转义 + 双后端大小写语义分叉
//   - F-071 NodeSeq 并发首次上报（回归护栏；PG 竞态本机不可复现）

func openCascadeRepo(t *testing.T, name string) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func countWhere(t *testing.T, r *Repository, table, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := r.DB().Table(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func createRow(t *testing.T, r *Repository, row interface{}) {
	t.Helper()
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatalf("create %T: %v", row, err)
	}
}

func TestDeleteNodeCascadeClearsUserInbounds(t *testing.T) {
	r := openCascadeRepo(t, "node-cascade.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.NodeKernel{ID: 11, NodeID: 1, KernelType: "xray", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInbound{ID: 21, NodeID: 1, KernelID: 11, Name: "ib", Protocol: "vless", Port: 443, CreatedTime: now, UpdatedTime: now})
	// user_inbound 既按 inbound_id 也按 relay_id 引用该入站
	createRow(t, r, &model.UserInbound{ID: 31, UserID: 7, InboundID: 21})
	createRow(t, r, &model.UserInbound{ID: 32, UserID: 8, InboundID: 99, RelayID: 21})

	if err := r.DeleteNodeCascade(1); err != nil {
		t.Fatalf("DeleteNodeCascade: %v", err)
	}
	if n := countWhere(t, r, "user_inbound", "inbound_id = ? OR relay_id = ?", 21, 21); n != 0 {
		t.Fatalf("F-010: node cascade left %d orphan user_inbound rows", n)
	}
}

func TestDeleteUserCascadeClearsSubscriptionAndNftLedger(t *testing.T) {
	r := openCascadeRepo(t, "user-cascade.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.User{ID: 7, User: "u7", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Subscription{ID: 3, Name: "s3", Token: "tok", UUID: "uuid", Password: "pw", Kind: "share", CreatedTime: now, UpdatedTime: now})
	// F-011: user_subscription 分配行
	createRow(t, r, &model.UserSubscription{ID: 41, UserID: 7, SubscriptionID: 3, CreatedTime: now})
	// 该用户的转发 + nft 台账三表 + 中转行（F-057，对照 DeleteForwardCascade）
	createRow(t, r, &model.Tunnel{ID: 5, Name: "t5", Type: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.Forward{ID: 51, UserID: 7, TunnelID: 5, Name: "f51", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NftCounterState{ForwardID: 51, NodeID: 1, Protocol: "tcp", Direction: "in", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NftRuleBinding{ForwardID: 51, NodeID: 1, InPort: 5001, TargetAddr: "10.0.0.2:80", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NftPendingDelete{ForwardID: 51, NodeID: 1, CreatedAt: now, UpdatedAt: now, NextRetryAt: now})
	createRow(t, r, &model.NodeInboundRelay{ID: 61, InboundID: 21, ForwardID: 51})

	if err := r.DeleteUserCascade(7); err != nil {
		t.Fatalf("DeleteUserCascade: %v", err)
	}
	if n := countWhere(t, r, "user_subscription", "user_id = ?", 7); n != 0 {
		t.Fatalf("F-011: user cascade left %d user_subscription rows", n)
	}
	for _, table := range []string{"nft_counter_state", "nft_rule_binding", "nft_pending_delete", "node_inbound_relay"} {
		if n := countWhere(t, r, table, "forward_id = ?", 51); n != 0 {
			t.Fatalf("F-057: user cascade left %d rows in %s for deleted forward", n, table)
		}
	}
}

func TestNftPendingDeleteExhaustedNotDue(t *testing.T) {
	r := openCascadeRepo(t, "pending.db")
	old := time.Now().Add(-25 * time.Hour)
	createRow(t, r, &model.NftPendingDelete{ForwardID: 71, NodeID: 1,
		CreatedAt: old.UnixMilli(), UpdatedAt: old.UnixMilli(), NextRetryAt: time.Now().Add(-time.Minute).UnixMilli()})
	exhausted, err := r.RecordNftPendingDeleteAttempt(71, errors.New("node offline"), time.Now())
	if err != nil {
		t.Fatalf("RecordNftPendingDeleteAttempt: %v", err)
	}
	if !exhausted {
		t.Fatal("expected exhausted=true past the 24h cap")
	}
	due, err := r.ListDueNftPendingDeletes(time.Now(), 100)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	for _, row := range due {
		if row.ForwardID == 71 {
			t.Fatal("F-017: exhausted pending delete (next_retry_at=0) is still returned as due — retries forever")
		}
	}
	// 未耗尽的行必须仍在到期集合
	now := time.Now().UnixMilli()
	createRow(t, r, &model.NftPendingDelete{ForwardID: 72, NodeID: 1,
		CreatedAt: now, UpdatedAt: now, NextRetryAt: time.Now().Add(-time.Minute).UnixMilli()})
	due, err = r.ListDueNftPendingDeletes(time.Now(), 100)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	found := false
	for _, row := range due {
		if row.ForwardID == 72 {
			found = true
		}
	}
	if !found {
		t.Fatal("fresh pending delete must remain due")
	}
	// 管理员可观测列表仍应包含耗尽行（等待人工处理）
	all, err := r.ListAllNftPendingDeletes()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	found = false
	for _, row := range all {
		if row.ForwardID == 71 {
			found = true
		}
	}
	if !found {
		t.Fatal("exhausted row must stay visible in ListAll")
	}
}

func TestListTunnelIDsByNamePrefixExactSemantics(t *testing.T) {
	r := openCascadeRepo(t, "prefix.db")
	now := time.Now().UnixMilli()
	names := []string{"Fed_X", "FedXY", "fed-one"}
	for i, name := range names {
		createRow(t, r, &model.Tunnel{ID: int64(100 + i), Name: name, Type: 1, CreatedTime: now, UpdatedTime: now})
	}
	ids, err := r.ListTunnelIDsByNamePrefix("Fed_")
	if err != nil {
		t.Fatalf("ListTunnelIDsByNamePrefix: %v", err)
	}
	if len(ids) != 1 || ids[0] != 100 {
		t.Fatalf("F-072: prefix %q must match only literal Fed_X (underscore is not a wildcard), got %v", "Fed_", ids)
	}
	ids, err = r.ListTunnelIDsByNamePrefix("FED")
	if err != nil {
		t.Fatalf("ListTunnelIDsByNamePrefix: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("F-072: prefix match must be case-sensitive like PostgreSQL, got %v for %q", ids, "FED")
	}
}

func TestNodeSeqConcurrentFirstReports(t *testing.T) {
	r := openCascadeRepo(t, "seq.db")
	const workers = 16
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seq := int64(i + 1)
			_, _, _, err := r.CheckAndUpdateSeq(9, &seq, time.Now().UnixMilli())
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	// 最大 seq 必须最终落库；其重放必须被拒。
	var lastSeq int64
	if err := r.DB().Table("node_seq").Where("node_id = ?", 9).Pluck("last_seq", &lastSeq).Error; err != nil {
		t.Fatal(err)
	}
	if lastSeq != workers {
		t.Fatalf("expected last_seq=%d, got %d", workers, lastSeq)
	}
	max := int64(workers)
	accepted, isReplay, _, err := r.CheckAndUpdateSeq(9, &max, time.Now().UnixMilli())
	if err != nil || accepted || !isReplay {
		t.Fatalf("replay of max seq must be rejected: accepted=%v replay=%v err=%v", accepted, isReplay, err)
	}
	// legacy 分支：无 seq 上报被接受且计数（正常路径；错误传播见代码修复）。
	accepted, _, isLegacy, err := r.CheckAndUpdateSeq(10, nil, time.Now().UnixMilli())
	if err != nil || !accepted || !isLegacy {
		t.Fatalf("legacy report: accepted=%v legacy=%v err=%v", accepted, isLegacy, err)
	}
}
