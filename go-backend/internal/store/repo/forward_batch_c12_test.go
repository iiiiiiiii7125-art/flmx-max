package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"go-backend/internal/store/model"
)

// C12 回归（X04-001 / X04-002）：转发列表与备份导出消除 N+1。
//
// 修复前形态（发现者+怀疑者实测）：ListForwards/GetUserPackageForwards
// 逐条 resolveForwardIngress（每条 2 SQL，其中 forward_port 按
// forward_id 全表扫描），查询数 1+2N、耗时二次型（3000 条 4.37s）；
// exportForwards 逐条 exportForwardPorts（同为全表扫描），3000 条
// 5.41s；exportTunnelGroups/exportUserGroups/exportPermissions 亦有
// 逐行查询。
//
// 本文件的断言分三层：
//  1. 等价性：批量结果与保留的单条实现（resolveForwardIngress /
//     exportForwardPorts）逐字段一致，且关键金值（inIp 串、分组导出
//     的索引序成员、forwardPorts 的 null 语义）与改造前探针实测一致；
//  2. 查询次数：计数 logger 断言语句数不随条数线性增长；
//  3. 阶梯耗时：200/1000/3000 下单位条数耗时近似恒定（修复后 T/N
//     不再单调上升），阈值留足沙箱噪声余量。

func seedC12Fixture(t *testing.T, r *Repository) {
	t.Helper()
	now := time.Now().UnixMilli()
	db := r.DB()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(db.Create(&model.Tunnel{ID: 1, Name: "t1", InIP: sql.NullString{String: "10.0.0.1", Valid: true}, CreatedTime: now, UpdatedTime: now, Status: 1}).Error)
	must(db.Create(&model.Tunnel{ID: 2, Name: "t2", CreatedTime: now, UpdatedTime: now, Status: 1}).Error)
	must(db.Create(&model.Node{ID: 11, Name: "n11", ServerIP: "203.0.113.11"}).Error)
	must(db.Create(&model.Node{ID: 12, Name: "n12", ServerIP: "203.0.113.12:9999"}).Error) // server_ip 带端口
	must(db.Create(&model.Node{ID: 13, Name: "n13", ServerIP: ""}).Error)                  // 空 server_ip
	must(db.Create(&model.Node{ID: 14, Name: "n14", ServerIP: "[2001:db8::14]"}).Error)    // IPv6 带括号

	must(db.Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(20, 2, 'u2', 'f20', 1, '1.1.1.1:80', 'fifo', 5, 6, ?, ?, 1, 2)`, now, now).Error)
	must(db.Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(21, 2, 'u2', 'f21', 2, '2.2.2.2:443', 'fifo', 0, 0, ?, ?, 1, 1)`, now, now).Error)
	must(db.Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(22, 3, 'u3', 'f22', 1, '3.3.3.3:22', 'fifo', 0, 0, ?, ?, 1, 3)`, now, now).Error)
	fp := func(fwd, node int64, port int, inip sql.NullString, warn string) {
		t.Helper()
		f := &model.ForwardPort{ForwardID: fwd, NodeID: node, Port: port, InIP: inip}
		if warn != "" {
			f.BindWarning = sql.NullString{String: warn, Valid: true}
		}
		must(db.Create(f).Error)
	}
	fp(20, 11, 8080, sql.NullString{String: "198.51.100.20", Valid: true}, "") // InIP 优先
	fp(20, 12, 8080, sql.NullString{}, "")                                     // 同 port 不同节点
	fp(20, 13, 9090, sql.NullString{String: "  ", Valid: true}, "")            // 空白 InIP + 空 server_ip
	fp(20, 14, 7070, sql.NullString{}, "")                                     // IPv6
	fp(20, 11, 0, sql.NullString{String: "192.0.2.1", Valid: true}, "")        // port=0 边沿
	fp(22, 12, 10022, sql.NullString{}, "bind-warn-22")                        // C06 bindWarnings 透出
	// f21 无任何 forward_port

	// 分组成员乱序插入：旧 Pluck 走唯一索引序输出，批量版必须同序。
	must(db.Create(&model.TunnelGroup{ID: 5, Name: "tg5", CreatedTime: now, UpdatedTime: now, Status: 1}).Error)
	must(db.Create(&model.TunnelGroup{ID: 6, Name: "tg6", CreatedTime: now, UpdatedTime: now, Status: 1}).Error)
	for _, tid := range []int64{30, 10, 20} {
		must(db.Create(&model.TunnelGroupTunnel{TunnelGroupID: 5, TunnelID: tid, CreatedTime: now}).Error)
	}
	must(db.Create(&model.UserGroup{ID: 7, Name: "ug7", CreatedTime: now, UpdatedTime: now, Status: 1}).Error)
	for _, uid := range []int64{9, 3, 7} {
		must(db.Create(&model.UserGroupUser{UserGroupID: 7, UserID: uid, CreatedTime: now}).Error)
	}
	must(db.Create(&model.GroupPermission{ID: 8, UserGroupID: 7, TunnelGroupID: 5, CreatedTime: now}).Error)
	for _, utid := range []int64{300, 100, 200} {
		must(db.Create(&model.GroupPermissionGrant{UserGroupID: 7, TunnelGroupID: 5, UserTunnelID: utid, CreatedTime: now}).Error)
	}
}

func TestForwardBatchEquivalence_X04(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "c12-equiv.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	seedC12Fixture(t, r)
	db := r.DB()

	// 1) 批量入口解析 == 单条实现（逐 forward 对拍，含不存在 id）。
	batch, err := resolveForwardIngressBatch(db, []int64{20, 21, 22, 999})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	for _, fid := range []int64{20, 21, 22} {
		wantIP, wantPort, err := resolveForwardIngress(db, fid, 1)
		if err != nil {
			t.Fatalf("singular %d: %v", fid, err)
		}
		got := batch[fid]
		if got.IP != wantIP || got.Port != wantPort {
			t.Fatalf("forward %d: batch=(%q,%v) singular=(%q,%v)", fid, got.IP, got.Port, wantIP, wantPort)
		}
	}
	if _, ok := batch[999]; ok {
		t.Fatalf("nonexistent forward 999 must be absent from batch map")
	}
	// 金值（改造前探针实测）：port=0 行产生尾随空条目，逐字锁定。
	if got := batch[20].IP; got != "198.51.100.20:8080,203.0.113.12:8080,[2001:db8::14]:7070," {
		t.Fatalf("f20 inIp golden mismatch: %q", got)
	}
	if p := batch[20].Port; !p.Valid || p.Int64 != 8080 {
		t.Fatalf("f20 inPort = %v, want 8080", p)
	}

	// 2) ListForwards：inIp/inPort 金值 + C06 bindWarnings 透出不回退。
	items, err := r.ListForwards()
	if err != nil {
		t.Fatalf("ListForwards: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("ListForwards len = %d, want 3", len(items))
	}
	byID := map[int64]map[string]interface{}{}
	for _, it := range items {
		byID[it["id"].(int64)] = it
	}
	if byID[21]["inIp"] != nil || byID[21]["inPort"] != nil {
		t.Fatalf("f21 inIp/inPort = %v/%v, want nil/nil", byID[21]["inIp"], byID[21]["inPort"])
	}
	if byID[20]["inIp"] != "198.51.100.20:8080,203.0.113.12:8080,[2001:db8::14]:7070," {
		t.Fatalf("f20 list inIp = %v", byID[20]["inIp"])
	}
	if byID[22]["inIp"] != "203.0.113.12:10022" {
		t.Fatalf("f22 list inIp = %v", byID[22]["inIp"])
	}
	bw, ok := byID[22]["bindWarnings"].([]string)
	if !ok || len(bw) != 1 || bw[0] != "bind-warn-22" {
		t.Fatalf("f22 bindWarnings = %v, want [bind-warn-22] (C06 透出不得回退)", byID[22]["bindWarnings"])
	}
	if _, ok := byID[20]["bindWarnings"]; ok {
		t.Fatalf("f20 must not carry bindWarnings")
	}

	// 3) GetUserPackageForwards 与单条实现一致。
	pkg, err := r.GetUserPackageForwards(2)
	if err != nil {
		t.Fatalf("GetUserPackageForwards: %v", err)
	}
	if len(pkg) != 2 || pkg[0].ID != 20 || pkg[1].ID != 21 {
		t.Fatalf("package forwards = %+v", pkg)
	}
	if pkg[0].InIP != batch[20].IP || pkg[0].InPort != batch[20].Port {
		t.Fatalf("package f20 ingress = (%q,%v), want (%q,%v)", pkg[0].InIP, pkg[0].InPort, batch[20].IP, batch[20].Port)
	}
	if pkg[1].InIP != "" || pkg[1].InPort.Valid {
		t.Fatalf("package f21 ingress = (%q,%v), want empty/invalid", pkg[1].InIP, pkg[1].InPort)
	}

	// 4) exportForwards：端口段与保留的单条 exportForwardPorts 逐条
	// 对拍；无端口 forward 的 ForwardPorts 必须保持 JSON null 语义。
	ef, err := r.exportForwards()
	if err != nil {
		t.Fatalf("exportForwards: %v", err)
	}
	if len(ef) != 3 {
		t.Fatalf("exportForwards len = %d, want 3", len(ef))
	}
	for _, fb := range ef {
		want, err := r.exportForwardPorts(fb.ID)
		if err != nil {
			t.Fatalf("singular export ports %d: %v", fb.ID, err)
		}
		if fb.ForwardPorts == nil {
			t.Fatalf("forward %d: ForwardPorts pointer is nil", fb.ID)
		}
		got := *fb.ForwardPorts
		if len(got) == 0 && len(want) == 0 {
			if got != nil {
				t.Fatalf("forward %d: empty ports must stay nil slice (JSON null), got %#v", fb.ID, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("forward %d ports: batch=%+v singular=%+v", fb.ID, got, want)
		}
	}
	raw, _ := json.Marshal(ef)
	if !strings.Contains(string(raw), `"forwardPorts":null`) {
		t.Fatalf("export JSON must contain forwardPorts:null for portless forward: %s", raw)
	}

	// 5) 分组/权限导出：成员顺序锁定为改造前实测的索引序。
	etg, err := r.exportTunnelGroups()
	if err != nil {
		t.Fatalf("exportTunnelGroups: %v", err)
	}
	if len(etg) != 2 || !reflect.DeepEqual(etg[0].Tunnels, []int64{10, 20, 30}) {
		t.Fatalf("tunnel groups = %+v, want tg5 tunnels [10 20 30]", etg)
	}
	if etg[1].Tunnels != nil {
		t.Fatalf("empty tunnel group Tunnels = %v, want nil", etg[1].Tunnels)
	}
	eug, err := r.exportUserGroups()
	if err != nil {
		t.Fatalf("exportUserGroups: %v", err)
	}
	if len(eug) != 1 || !reflect.DeepEqual(eug[0].Users, []int64{3, 7, 9}) {
		t.Fatalf("user groups = %+v, want ug7 users [3 7 9]", eug)
	}
	ep, err := r.exportPermissions()
	if err != nil {
		t.Fatalf("exportPermissions: %v", err)
	}
	if len(ep) != 1 || len(ep[0].Grants) != 3 {
		t.Fatalf("permissions = %+v", ep)
	}
	var grantUTs []int64
	for _, g := range ep[0].Grants {
		grantUTs = append(grantUTs, g.UserTunnelID)
	}
	if !reflect.DeepEqual(grantUTs, []int64{100, 200, 300}) {
		t.Fatalf("grant order = %v, want [100 200 300] (index order)", grantUTs)
	}
}

// ─── 查询计数 ────────────────────────────────────────────────────────

type c12CountingLogger struct{ n *int64 }

func (l c12CountingLogger) LogMode(logger.LogLevel) logger.Interface      { return l }
func (l c12CountingLogger) Info(context.Context, string, ...interface{})  {}
func (l c12CountingLogger) Warn(context.Context, string, ...interface{})  {}
func (l c12CountingLogger) Error(context.Context, string, ...interface{}) {}
func (l c12CountingLogger) Trace(context.Context, time.Time, func() (string, int64), error) {
	atomic.AddInt64(l.n, 1)
}

func seedC12Bulk(t *testing.T, r *Repository, from, to int64) {
	t.Helper()
	db := r.DB()
	now := time.Now().UnixMilli()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	for id := from; id <= to; id++ {
		if err := tx.Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx) VALUES(?, 2, 'u2', 'fb', 1, '1.1.1.1:80', 'fifo', 0, 0, ?, ?, 1, ?)`, id, now, now, id).Error; err != nil {
			t.Fatalf("insert forward %d: %v", id, err)
		}
		if err := tx.Exec(`INSERT INTO forward_port(forward_id, node_id, port, in_ip) VALUES(?, 11, ?, NULL)`, id, 20000+id).Error; err != nil {
			t.Fatalf("insert forward_port %d: %v", id, err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func seedC12BulkBase(t *testing.T, r *Repository) {
	t.Helper()
	now := time.Now().UnixMilli()
	db := r.DB()
	if err := db.Create(&model.Tunnel{ID: 1, Name: "t1", CreatedTime: now, UpdatedTime: now, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := db.Create(&model.Node{ID: 11, Name: "n11", ServerIP: "203.0.113.11"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
}

func TestForwardBatchQueryCount_X04(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "c12-count.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	seedC12BulkBase(t, r)

	var count int64
	cr := &Repository{db: r.DB().Session(&gorm.Session{Logger: c12CountingLogger{n: &count}})}
	measure := func(name string, fn func() error) int64 {
		t.Helper()
		atomic.StoreInt64(&count, 0)
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return atomic.LoadInt64(&count)
	}

	seedC12Bulk(t, r, 1, 120)
	list120 := measure("list120", func() error { _, err := cr.ListForwards(); return err })
	pkg120 := measure("pkg120", func() error { _, err := cr.GetUserPackageForwards(2); return err })
	exp120 := measure("exp120", func() error { _, err := cr.exportForwards(); return err })

	seedC12Bulk(t, r, 121, 1200)
	list1200 := measure("list1200", func() error { _, err := cr.ListForwards(); return err })
	pkg1200 := measure("pkg1200", func() error { _, err := cr.GetUserPackageForwards(2); return err })
	exp1200 := measure("exp1200", func() error { _, err := cr.exportForwards(); return err })

	t.Logf("queries: list %d->%d, package %d->%d, export %d->%d (120 -> 1200 forwards)",
		list120, list1200, pkg120, pkg1200, exp120, exp1200)
	// 旧形态在 1200 条时约 2400+ 条语句；新形态必须是常数+分块量级。
	for name, pair := range map[string][2]int64{
		"ListForwards":           {list120, list1200},
		"GetUserPackageForwards": {pkg120, pkg1200},
		"exportForwards":         {exp120, exp1200},
	} {
		small, big := pair[0], pair[1]
		if big > 10 {
			t.Fatalf("%s: %d queries at 1200 forwards, want <= 10 (was ~2N)", name, big)
		}
		if big-small > 4 {
			t.Fatalf("%s: query count grew %d -> %d when forwards grew 10x; growth must be chunk-bounded (<=4)", name, small, big)
		}
	}
}

// ─── 阶梯耗时 ────────────────────────────────────────────────────────

func medianDuration(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func TestForwardLadderTiming_X04(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "c12-ladder.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	seedC12BulkBase(t, r)

	sizes := []struct{ from, to int64 }{{1, 200}, {201, 1000}, {1001, 3000}}
	perItem := make(map[int64]time.Duration)
	var export3000 time.Duration
	for _, sz := range sizes {
		seedC12Bulk(t, r, sz.from, sz.to)
		n := sz.to
		var runs []time.Duration
		for i := 0; i < 3; i++ {
			start := time.Now()
			items, err := r.ListForwards()
			if err != nil {
				t.Fatalf("ListForwards at %d: %v", n, err)
			}
			if int64(len(items)) != n {
				t.Fatalf("ListForwards at %d returned %d items", n, len(items))
			}
			runs = append(runs, time.Since(start))
		}
		med := medianDuration(runs)
		perItem[n] = med / time.Duration(n)
		t.Logf("ListForwards N=%d median=%v per-item=%v", n, med, perItem[n])
		if n == 3000 {
			start := time.Now()
			ef, err := r.exportForwards()
			if err != nil {
				t.Fatalf("exportForwards at 3000: %v", err)
			}
			if len(ef) != 3000 {
				t.Fatalf("exportForwards at 3000 returned %d", len(ef))
			}
			export3000 = time.Since(start)
			t.Logf("exportForwards N=3000 took %v (was 5.41s via HTTP in audit env)", export3000)
		}
	}
	// 修复后 T/N 应近似恒定：给沙箱共用 CPU 留 4 倍余量（旧形态
	// T/N 从 0.70 单调升到 1.46 ms/条且查询内容含全表扫描，
	// 3000 条时总耗时 4.37s；新形态总耗时应远低于该量级）。
	if perItem[3000] > 4*perItem[200] {
		t.Fatalf("per-item cost grew: 200:%v 3000:%v — superlinear regression", perItem[200], perItem[3000])
	}
	if export3000 > 3*time.Second {
		t.Fatalf("exportForwards at 3000 took %v, want < 3s", export3000)
	}
}

// ─── v4 索引迁移 ─────────────────────────────────────────────────────

func TestMigrationC12V4ForwardLookupIndexes_X04(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "c12-mig.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	db := r.DB()

	indexExists := func(name string) bool {
		t.Helper()
		ok, err := c12IndexExists(db, name)
		if err != nil {
			t.Fatalf("index exists %s: %v", name, err)
		}
		return ok
	}
	// Open 的 autoMigrateAll 已执行 v4：三索引 + 版本记录就位。
	for _, idx := range c12V4Indexes {
		if !indexExists(idx.name) {
			t.Fatalf("index %s missing after startup migration", idx.name)
		}
	}
	var recs int64
	if err := db.Model(&schemaMigration{}).Where("version = ?", migrationC12V4Version).Count(&recs).Error; err != nil {
		t.Fatalf("count migration records: %v", err)
	}
	if recs != 1 {
		t.Fatalf("migration version records = %d, want 1", recs)
	}

	// 幂等：重跑不报错、不重复记版。
	if err := migrateC12V4ForwardLookupIndexes(db); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}
	if err := db.Model(&schemaMigration{}).Where("version = ?", migrationC12V4Version).Count(&recs).Error; err != nil {
		t.Fatalf("count after re-run: %v", err)
	}
	if recs != 1 {
		t.Fatalf("migration version records after re-run = %d, want 1", recs)
	}

	// 批量端口查询的执行计划必须命中新索引（修复前为 SCAN）。
	var plan []struct{ Detail string }
	q := `SELECT forward_port.forward_id, forward_port.port, node.server_ip, forward_port.in_ip FROM forward_port LEFT JOIN node ON node.id = forward_port.node_id WHERE forward_port.forward_id IN (1,2,3) ORDER BY forward_port.id ASC`
	if err := db.Raw("EXPLAIN QUERY PLAN " + q).Scan(&plan).Error; err != nil {
		t.Fatalf("explain: %v", err)
	}
	joined := ""
	for _, row := range plan {
		joined += row.Detail + "\n"
	}
	if !strings.Contains(joined, "SEARCH forward_port USING INDEX idx_forward_port_forward_id") {
		t.Fatalf("batch port query plan does not use idx_forward_port_forward_id:\n%s", joined)
	}

	// 回滚删索引与版本记录；再跑迁移可恢复。
	if err := rollbackC12V4ForwardLookupIndexes(db); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	for _, idx := range c12V4Indexes {
		if indexExists(idx.name) {
			t.Fatalf("index %s still present after rollback", idx.name)
		}
	}
	if err := migrateC12V4ForwardLookupIndexes(db); err != nil {
		t.Fatalf("re-migrate after rollback: %v", err)
	}
	for _, idx := range c12V4Indexes {
		if !indexExists(idx.name) {
			t.Fatalf("index %s missing after re-migration", idx.name)
		}
	}
}
