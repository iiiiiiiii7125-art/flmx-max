package repo

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/model"

	gsqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newConnlogTestRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := gorm.Open(gsqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(
		&model.InboundConnLog{},
		&TrafficStreamCursor{},
		&model.NodeInbound{},
		&model.NodeInboundRelay{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return &Repository{db: db}
}

func connlogDigest(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

func connlogRows(t *testing.T, r *Repository) []model.InboundConnLog {
	t.Helper()
	var rows []model.InboundConnLog
	if err := r.db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list connlog rows: %v", err)
	}
	return rows
}

func TestConnlogStreamBatchIdempotency(t *testing.T) {
	r := newConnlogTestRepo(t)
	scope := "connlog:1:stream-a"
	now := time.UnixMilli(1_700_000_000_000)
	events := []ConnlogEvent{
		{Type: "open", ConnID: "c1", EntryNodeID: 1, EntryKind: ConnlogEntryForward, EntryID: "77_2_3_tcp",
			ClientIP: "203.0.113.7", Network: "tcp", SessionKind: "tcp_conn", StartTime: 1_700_000_000_000},
		{Type: "close", ConnID: "c1", EntryNodeID: 1, EntryKind: ConnlogEntryForward, EntryID: "77_2_3_tcp",
			EndTime: 1_700_000_060_000},
	}
	digest := connlogDigest("batch-1")

	ack, err := r.ApplyConnlogStreamBatch(scope, 1, digest, now, events)
	if err != nil {
		t.Fatalf("apply batch 1: %v", err)
	}
	if !ack.Applied || ack.Cursor != 1 {
		t.Fatalf("ack = %+v, want applied with cursor 1", ack)
	}
	rows := connlogRows(t, r)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (open+close of one conn upsert into one row)", len(rows))
	}
	row := rows[0]
	if row.Status != ConnlogStatusClosed || row.EndTime != 1_700_000_060_000 || row.DurationMs != 60_000 {
		t.Fatalf("row = %+v, want closed with end/duration from close event", row)
	}

	// 真重放：同序号同摘要 → 确认、不重应用。
	ack, err = r.ApplyConnlogStreamBatch(scope, 1, digest, now, events)
	if err != nil {
		t.Fatalf("replay batch 1: %v", err)
	}
	if ack.Applied || !ack.Replay {
		t.Fatalf("replay ack = %+v, want replay without apply", ack)
	}
	if rows := connlogRows(t, r); len(rows) != 1 {
		t.Fatalf("after replay rows = %d, want 1", len(rows))
	}

	// 同序号不同摘要 → 身份冲突，整批拒收。
	if _, err := r.ApplyConnlogStreamBatch(scope, 1, connlogDigest("batch-1-mutated"), now, events); !errors.Is(err, ErrTrafficStreamConflict) {
		t.Fatalf("mutated digest err = %v, want ErrTrafficStreamConflict", err)
	}

	// 序号跳跃 → 缺口（期望 2 收到 3）。
	var gap *TrafficStreamGap
	if _, err := r.ApplyConnlogStreamBatch(scope, 3, connlogDigest("batch-3"), now, events); !errors.As(err, &gap) || gap.Expected != 2 {
		t.Fatalf("gap err = %v, want TrafficStreamGap{Expected:2}", err)
	}

	// 推进到 2 后，旧序号 1 带原摘要重报 → behind（确认不入账）。
	if _, err := r.ApplyConnlogStreamBatch(scope, 2, connlogDigest("batch-2"), now, []ConnlogEvent{
		{Type: "open", ConnID: "c2", EntryNodeID: 1, EntryKind: ConnlogEntryForward, EntryID: "77_2_3_tcp",
			ClientIP: "203.0.113.8", Network: "tcp", SessionKind: "tcp_conn", StartTime: 1_700_000_100_000},
	}); err != nil {
		t.Fatalf("apply batch 2: %v", err)
	}
	ack, err = r.ApplyConnlogStreamBatch(scope, 1, digest, now, events)
	if err != nil {
		t.Fatalf("behind batch 1: %v", err)
	}
	if ack.Applied || !ack.Behind || ack.Cursor != 2 {
		t.Fatalf("behind ack = %+v, want behind with cursor 2 and no apply", ack)
	}
	if rows := connlogRows(t, r); len(rows) != 2 {
		t.Fatalf("after behind rows = %d, want 2 (behind batch must not re-apply)", len(rows))
	}
}

func TestConnlogUpsertStateMachine(t *testing.T) {
	r := newConnlogTestRepo(t)
	scope := "connlog:9:stream-b"
	base := int64(1_700_000_000_000)
	apply := func(seq int64, events ...ConnlogEvent) {
		t.Helper()
		_, err := r.ApplyConnlogStreamBatch(scope, seq, connlogDigest(fmt.Sprintf("seq-%d", seq)), time.UnixMilli(base+seq*1000), events)
		if err != nil {
			t.Fatalf("apply seq %d: %v", seq, err)
		}
	}

	// close 先行（open 事件丢失）：close 自带全字段独立建完整行。
	apply(1, ConnlogEvent{Type: "close", ConnID: "lost-open", EntryNodeID: 9, EntryKind: ConnlogEntryInbound,
		EntryID: "in-5", ClientIP: "198.51.100.3", Network: "tcp", SessionKind: "tcp_conn",
		StartTime: base, EndTime: base + 5_000})
	rows := connlogRows(t, r)
	if len(rows) != 1 || rows[0].Status != ConnlogStatusClosed || rows[0].StartTime != base || rows[0].DurationMs != 5_000 {
		t.Fatalf("close-first row = %+v, want one closed row with start/duration backfilled", rows)
	}

	// 迟到的 open 不得重开已 closed 的行。
	apply(2, ConnlogEvent{Type: "open", ConnID: "lost-open", EntryNodeID: 9, EntryKind: ConnlogEntryInbound,
		EntryID: "in-5", ClientIP: "198.51.100.99", Network: "tcp", SessionKind: "tcp_conn", StartTime: base + 999})
	rows = connlogRows(t, r)
	if len(rows) != 1 || rows[0].Status != ConnlogStatusClosed || rows[0].ClientIP != "198.51.100.3" || rows[0].StartTime != base {
		t.Fatalf("late open mutated closed row: %+v", rows[0])
	}

	// open 行被 boot 结清为 interrupted 后，迟到的真 close 仍能把
	// 它补成 closed（真终态优先于结清推定）。
	apply(3, ConnlogEvent{Type: "open", ConnID: "zombie", EntryNodeID: 9, EntryKind: ConnlogEntryInbound,
		EntryID: "in-5", ClientIP: "198.51.100.4", Network: "tcp", SessionKind: "tcp_conn", StartTime: base + 10_000})
	if n, err := r.InterruptOpenInboundConnLogs(9, base+20_000); err != nil || n != 1 {
		t.Fatalf("interrupt = (%d, %v), want (1, nil)", n, err)
	}
	apply(4, ConnlogEvent{Type: "close", ConnID: "zombie", EntryNodeID: 9, EntryKind: ConnlogEntryInbound,
		EntryID: "in-5", EndTime: base + 40_000})
	rows = connlogRows(t, r)
	var zombie *model.InboundConnLog
	for i := range rows {
		if rows[i].ConnID == "zombie" {
			zombie = &rows[i]
		}
	}
	if zombie == nil || zombie.Status != ConnlogStatusClosed || zombie.EndTime != base+40_000 || zombie.DurationMs != 30_000 {
		t.Fatalf("late close after interrupt = %+v, want closed with real end/duration", zombie)
	}
}

func TestConnlogResolveDisplayKeys(t *testing.T) {
	r := newConnlogTestRepo(t)
	landing := &model.NodeInbound{KernelID: 1, NodeID: 42, Name: "land-in", Protocol: "vless", Listen: "::", Port: 443}
	if err := r.db.Create(landing).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	if err := r.db.Create(&model.NodeInboundRelay{InboundID: landing.ID, ForwardID: 77, Name: "relay"}).Error; err != nil {
		t.Fatalf("seed relay: %v", err)
	}

	cases := []struct {
		name       string
		nodeID     int64
		kind, eid  string
		wantNode   int64
		wantInboun int64
	}{
		{"forward tcp via relay", 7, ConnlogEntryForward, "77_2_3_tcp", 42, landing.ID},
		{"forward udp via relay", 7, ConnlogEntryForward, "77_2_3_udp", 42, landing.ID},
		{"forward bare via relay", 7, ConnlogEntryForward, "77_2_3", 42, landing.ID},
		{"forward without relay row", 7, ConnlogEntryForward, "78_2_3_tcp", 0, 0},
		{"forward malformed name", 7, ConnlogEntryForward, "web_api", 0, 0},
		{"inbound own node", 42, ConnlogEntryInbound, fmt.Sprintf("in-%d", landing.ID), 42, landing.ID},
		{"inbound other node", 7, ConnlogEntryInbound, fmt.Sprintf("in-%d", landing.ID), 0, 0},
		{"inbound missing", 42, ConnlogEntryInbound, "in-9999", 0, 0},
		{"inbound bad tag", 42, ConnlogEntryInbound, "in-x", 0, 0},
		{"unknown kind", 42, "tunnel", "in-1", 0, 0},
	}
	for _, tc := range cases {
		gotNode, gotInbound := r.ResolveConnlogDisplayKeys(tc.nodeID, tc.kind, tc.eid)
		if gotNode != tc.wantNode || gotInbound != tc.wantInboun {
			t.Errorf("%s: Resolve(%d, %q, %q) = (%d, %d), want (%d, %d)",
				tc.name, tc.nodeID, tc.kind, tc.eid, gotNode, gotInbound, tc.wantNode, tc.wantInboun)
		}
	}
}

func TestConnlogListPaginationAndIPFilter(t *testing.T) {
	r := newConnlogTestRepo(t)
	base := int64(1_700_000_000_000)
	seed := []model.InboundConnLog{
		{EntryNodeID: 1, ConnID: "a", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base + 1000, Status: ConnlogStatusClosed, CreatedTime: base},
		{EntryNodeID: 1, ConnID: "b", DisplayInboundID: 10, ClientIP: "203.0.113.2", StartTime: base + 3000, Status: ConnlogStatusOpen, CreatedTime: base},
		{EntryNodeID: 1, ConnID: "c", DisplayInboundID: 10, ClientIP: "203.0.113.1", StartTime: base + 2000, Status: ConnlogStatusClosed, CreatedTime: base},
		{EntryNodeID: 1, ConnID: "d", DisplayInboundID: 11, ClientIP: "203.0.113.1", StartTime: base + 4000, Status: ConnlogStatusClosed, CreatedTime: base},
		{EntryNodeID: 1, ConnID: "e", DisplayInboundID: 0, ClientIP: "203.0.113.1", StartTime: base + 5000, Status: ConnlogStatusClosed, CreatedTime: base},
	}
	for i := range seed {
		if err := r.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ConnID, err)
		}
	}

	page1, err := r.ListInboundConnLogs(10, "", 0, 2)
	if err != nil || len(page1) != 2 || page1[0].ConnID != "b" || page1[1].ConnID != "c" {
		t.Fatalf("page1 = (%v, %v), want [b c] newest first", page1, err)
	}
	page2, err := r.ListInboundConnLogs(10, "", 2, 2)
	if err != nil || len(page2) != 1 || page2[0].ConnID != "a" {
		t.Fatalf("page2 = (%v, %v), want [a]", page2, err)
	}
	byIP, err := r.ListInboundConnLogs(10, "203.0.113.1", 0, 10)
	if err != nil || len(byIP) != 2 || byIP[0].ConnID != "c" || byIP[1].ConnID != "a" {
		t.Fatalf("ip filter = (%v, %v), want [c a] (exact match, other inbounds excluded)", byIP, err)
	}
}

func TestConnlogPruneTimeAndCap(t *testing.T) {
	r := newConnlogTestRepo(t)
	base := int64(1_700_000_000_000)
	mk := func(connID string, inboundID, start int64) model.InboundConnLog {
		return model.InboundConnLog{EntryNodeID: 1, ConnID: connID, DisplayInboundID: inboundID,
			ClientIP: "203.0.113.1", StartTime: start, Status: ConnlogStatusClosed, CreatedTime: start}
	}
	rows := []model.InboundConnLog{
		mk("old", 10, base), // 时间 prune 目标
		mk("a1", 10, base+1000),
		mk("a2", 10, base+2000),
		mk("a3", 10, base+3000),
		mk("a4", 10, base+4000),
		mk("a5", 10, base+5000),
		mk("b1", 11, base+1000),
		mk("b2", 11, base+2000),
		mk("u1", 0, base+1000), // 未归属桶同样受帽
		mk("u2", 0, base+2000),
		mk("u3", 0, base+3000),
		mk("u4", 0, base+4000),
	}
	for i := range rows {
		if err := r.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", rows[i].ConnID, err)
		}
	}

	deleted, err := r.PruneInboundConnLogsBefore(base + 500)
	if err != nil || deleted != 1 {
		t.Fatalf("time prune = (%d, %v), want (1, nil)", deleted, err)
	}

	deleted, err = r.PruneInboundConnLogsOverCap(3)
	if err != nil || deleted != 3 {
		t.Fatalf("cap prune = (%d, %v), want (3, nil): inbound 10 keeps 3 of 5, bucket 0 keeps 3 of 4", deleted, err)
	}
	remain := map[int64][]string{}
	for _, row := range connlogRows(t, r) {
		remain[row.DisplayInboundID] = append(remain[row.DisplayInboundID], row.ConnID)
	}
	if got := remain[10]; len(got) != 3 || got[0] != "a3" || got[1] != "a4" || got[2] != "a5" {
		t.Fatalf("inbound 10 remaining = %v, want newest [a3 a4 a5]", got)
	}
	if got := remain[11]; len(got) != 2 {
		t.Fatalf("inbound 11 remaining = %v, want untouched 2 rows", got)
	}
	if got := remain[0]; len(got) != 3 || got[0] != "u2" {
		t.Fatalf("unattributed remaining = %v, want newest 3 starting u2", got)
	}
	// 帽内重跑无删除（幂等）。
	if deleted, err := r.PruneInboundConnLogsOverCap(3); err != nil || deleted != 0 {
		t.Fatalf("cap prune rerun = (%d, %v), want (0, nil)", deleted, err)
	}
}

func TestConnlogInterruptSweeps(t *testing.T) {
	r := newConnlogTestRepo(t)
	now := int64(1_700_100_000_000)
	day := int64(24 * 3600 * 1000)
	seed := []model.InboundConnLog{
		{EntryNodeID: 1, ConnID: "stale-open", StartTime: now - 2*day, CreatedTime: now - 2*day + 5000, Status: ConnlogStatusOpen},
		{EntryNodeID: 1, ConnID: "fresh-open", StartTime: now - 3600_000, CreatedTime: now - 3600_000, Status: ConnlogStatusOpen},
		{EntryNodeID: 2, ConnID: "other-node-open", StartTime: now - 2*day, CreatedTime: now - 2*day, Status: ConnlogStatusOpen},
		{EntryNodeID: 1, ConnID: "closed", StartTime: now - 2*day, CreatedTime: now - 2*day, Status: ConnlogStatusClosed, EndTime: now - 2*day + 1000, DurationMs: 1000},
	}
	for i := range seed {
		if err := r.db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].ConnID, err)
		}
	}

	// 24h 清道夫：只结清超龄 open，end_time 取行自身 created_time
	// （最后已知时刻），closed 与未超龄 open 不动。
	n, err := r.InterruptStaleOpenInboundConnLogs(now - day)
	if err != nil || n != 2 {
		t.Fatalf("stale sweep = (%d, %v), want (2, nil)", n, err)
	}
	byConn := map[string]model.InboundConnLog{}
	for _, row := range connlogRows(t, r) {
		byConn[row.ConnID] = row
	}
	if row := byConn["stale-open"]; row.Status != ConnlogStatusInterrupted || row.EndTime != row.CreatedTime || row.DurationMs != 0 {
		t.Fatalf("stale-open = %+v, want interrupted with end=created_time and no duration", row)
	}
	if row := byConn["fresh-open"]; row.Status != ConnlogStatusOpen {
		t.Fatalf("fresh-open = %+v, want still open", row)
	}
	if row := byConn["closed"]; row.Status != ConnlogStatusClosed || row.DurationMs != 1000 {
		t.Fatalf("closed = %+v, want untouched", row)
	}

	// boot 结清：按节点限定，end_time 记给定的最后已知时刻。
	n, err = r.InterruptOpenInboundConnLogs(1, now-1000)
	if err != nil || n != 1 {
		t.Fatalf("boot interrupt = (%d, %v), want (1, nil) — only node 1 fresh-open remains open", n, err)
	}
	byConn = map[string]model.InboundConnLog{}
	for _, row := range connlogRows(t, r) {
		byConn[row.ConnID] = row
	}
	if row := byConn["fresh-open"]; row.Status != ConnlogStatusInterrupted || row.EndTime != now-1000 {
		t.Fatalf("fresh-open after boot interrupt = %+v, want interrupted with end=lastKnown", row)
	}
}

// U5 总开关（connlog_enabled）口径：默认开、仅显式 false 关；
// 保存面值域 true/false、空串走可清空语义；策略分类为
// authenticated 且可清空（非必填、非系统托管）。
func TestConnLogEnabledFromConfigMap(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]string
		want bool
	}{
		{"nil map", nil, true},
		{"absent", map[string]string{}, true},
		{"true", map[string]string{ConnLogEnabledConfigKey: "true"}, true},
		{"false", map[string]string{ConnLogEnabledConfigKey: "false"}, false},
		{"false padded/cased", map[string]string{ConnLogEnabledConfigKey: " FALSE "}, false},
		{"empty", map[string]string{ConnLogEnabledConfigKey: ""}, true},
		{"dirty falls back to default on", map[string]string{ConnLogEnabledConfigKey: "yes"}, true},
	}
	for _, c := range cases {
		if got := ConnLogEnabledFromConfigMap(c.cfg); got != c.want {
			t.Errorf("%s: ConnLogEnabledFromConfigMap = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeConnLogEnabledValue(t *testing.T) {
	for in, want := range map[string]string{
		"":       "",
		"true":   "true",
		" True ": "true",
		"FALSE":  "false",
		"false":  "false",
	} {
		got, err := NormalizeConnLogEnabledValue(in)
		if err != nil || got != want {
			t.Errorf("NormalizeConnLogEnabledValue(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"yes", "1", "0", "on"} {
		if _, err := NormalizeConnLogEnabledValue(in); err == nil {
			t.Errorf("NormalizeConnLogEnabledValue(%q) must reject non-boolean values", in)
		}
	}
}

func TestConnLogEnabledKeyPolicy(t *testing.T) {
	if got := PolicyForConfig(ConnLogEnabledConfigKey); got != ConfigAccessAuthenticated {
		t.Fatalf("PolicyForConfig(%q) = %q, want authenticated", ConnLogEnabledConfigKey, got)
	}
	if !IsClearableConfigKey(ConnLogEnabledConfigKey) {
		t.Fatal("connlog_enabled must be clearable (clearing falls back to the default-on state)")
	}
}
