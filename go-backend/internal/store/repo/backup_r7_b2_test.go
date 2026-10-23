package repo

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

func unmarshalProxy(extra string, p *kernel.Proxy) error {
	return json.Unmarshal([]byte(extra), p)
}

func itoa(n int) string { return strconv.Itoa(n) }

// R7 批 2 回归：R7X-002（forwards 恢复收口 nft_pending_delete 侧表）
// 与 R7X-003（导入型入站 Extra 地址列与 finishProxy 共用同一实现）。
// 两条关闭断言分开登记、禁互相顶替（N-35）；全部用例先红后绿。

// R7X-002 关闭断言 = 回归四格：备份 status∈{2,1}（另加 status=0 直通
// 对照）× 目标侧表∈{有,无}——有侧表两格恢复后记账清零、due 列表不
// 命中、转发存活且取消记账经 warning 披露；无侧表两格行为逐字不变
// （状态逐字落库、不新建记账、不被取消披露点名）。
func TestR7B2ImportForwardsClearsPendingDeleteSideTable(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7b2-fwdside.db")
	createRow(t, dest, &model.User{ID: 7, User: "u1", Pwd: r5b5Bcrypt, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", Type: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})

	// 目标库既有格：#100/#101 均为 pending-delete 态（行 status=2 +
	// 侧表行，next_retry_at 已到期）。
	for _, id := range []int64{100, 101} {
		createRow(t, dest, &model.Forward{ID: id, UserID: 7, UserName: "u1", Name: "stale", TunnelID: 1, RemoteAddr: "10.0.0.9:80", Status: model.ForwardStatusPendingDelete, CreatedTime: now, UpdatedTime: now})
		createRow(t, dest, &model.NftPendingDelete{ForwardID: id, NodeID: 1, RetryCount: 1, NextRetryAt: 1, LastError: "simulated offline", CreatedAt: now, UpdatedAt: now})
	}
	if due, err := dest.ListDueNftPendingDeletes(time.Now(), 100); err != nil || len(due) != 2 {
		t.Fatalf("precondition: due pending deletes = %v, err=%v, want 2 due rows", due, err)
	}

	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			// 有侧表 × status=2（归一格）
			{ID: 100, UserID: 7, UserName: "u1", Name: "fwd-side-norm", TunnelID: 1, RemoteAddr: "10.0.0.9:80", Status: 2, CreatedTime: now},
			// 有侧表 × status=1（直通格）
			{ID: 101, UserID: 7, UserName: "u1", Name: "fwd-side-active", TunnelID: 1, RemoteAddr: "10.0.0.9:81", Status: 1, CreatedTime: now},
			// 无侧表 × status=1
			{ID: 102, UserID: 7, UserName: "u1", Name: "fwd-plain-active", TunnelID: 1, RemoteAddr: "10.0.0.9:82", Status: 1, CreatedTime: now},
			// 无侧表 × status=0
			{ID: 103, UserID: 7, UserName: "u1", Name: "fwd-plain-paused", TunnelID: 1, RemoteAddr: "10.0.0.9:83", Status: 0, CreatedTime: now},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 1, UserID: 7, TunnelID: 1, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"forwards", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ForwardsImported != 4 {
		t.Fatalf("R7X-002: ForwardsImported = %d, want 4", result.ForwardsImported)
	}
	// 四格状态逐字/归一落库：100 归一为 0，101/102 为 1，103 为 0。
	for id, want := range map[int64]int{100: 0, 101: 1, 102: 1, 103: 0} {
		var st int
		if err := dest.DB().Raw(`SELECT status FROM forward WHERE id = ?`, id).Scan(&st).Error; err != nil {
			t.Fatalf("forward %d: %v", id, err)
		}
		if st != want {
			t.Fatalf("R7X-002: forward %d status = %d, want %d", id, st, want)
		}
	}
	// 有侧表两格：记账清零（全表零行——无侧表两格亦不得新建记账）。
	if n := countWhere(t, dest, "nft_pending_delete", "1 = 1"); n != 0 {
		t.Fatalf("R7X-002: side-table rows must be zero after restore, got %d", n)
	}
	if due, err := dest.ListDueNftPendingDeletes(time.Now(), 100); err != nil || len(due) != 0 {
		t.Fatalf("R7X-002: due list must not hit restored forwards, got %v err=%v", due, err)
	}
	if all, err := dest.ListAllNftPendingDeletes(); err != nil || len(all) != 0 {
		t.Fatalf("R7X-002: admin pending list must be empty, got %v err=%v", all, err)
	}
	// 恢复行存活（未被 sweep/导入自身误删）。
	if n := countWhere(t, dest, "forward", "id IN (100,101,102,103)"); n != 4 {
		t.Fatalf("R7X-002: all four restored forwards must survive, got %d", n)
	}
	// 删除数 >0 时 warnf 披露：取消披露须点名有侧表两格、不点名无侧表两格。
	var cancelWarnings []string
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "已随恢复取消") {
			cancelWarnings = append(cancelWarnings, w)
		}
	}
	if len(cancelWarnings) == 0 {
		t.Fatalf("R7X-002: cancelling side-table bookkeeping must be disclosed in warnings, got %v", result.ImportWarnings)
	}
	joinedCancel := strings.Join(cancelWarnings, "\n")
	if !strings.Contains(joinedCancel, "fwd-side-norm") || !strings.Contains(joinedCancel, "fwd-side-active") {
		t.Fatalf("R7X-002: cancel disclosure must name both side-table rows, got %q", joinedCancel)
	}
	if strings.Contains(joinedCancel, "fwd-plain-active") || strings.Contains(joinedCancel, "fwd-plain-paused") {
		t.Fatalf("R7X-002 防过修: cancel disclosure must not name no-side-table rows, got %q", joinedCancel)
	}
}

// R7X-003 关闭断言：导入型入站 Extra 的 server/port 地址列必须与导入
// 面 finishProxy 共用同一实现（kernel.ValidateImportedProxy 凭据+地址
// 双列）——loopback / 元数据（169.254.169.254）/ port 越界（99999）三
// 行整行跳过 + 计数 + 身份清单披露（R6X-009 口径）；合法行逐字恢复
// （防过修对照）。
func TestR7B2ImportExternalProxyAddressGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r7b2-extaddr.db")

	mkSS := func(name, server string, port int) string {
		return `{"type":"ss","name":"` + name + `","server":"` + server + `","port":` +
			itoa(port) + `,"cipher":"aes-256-gcm","password":"good-password","udp":true}`
	}
	loopbackExtra := mkSS("bad-loopback", "127.0.0.1", 10011)
	metadataExtra := mkSS("bad-metadata", "169.254.169.254", 10012)
	badPortExtra := mkSS("bad-port", "203.0.113.9", 99999)
	goodExtra := mkSS("good-ss", "203.0.113.10", 10013)

	// 共用实现单点断言：同一谓词对三行坏地址逐行拒绝、合法行放行
	// （ParseImport 对照在 R7-C 探针已坐实同三行被拒，本用例的对象是
	// 恢复面与谓词本身）。
	for name, extra := range map[string]string{"loopback": loopbackExtra, "metadata": metadataExtra, "badPort": badPortExtra} {
		var p kernel.Proxy
		if err := unmarshalProxy(extra, &p); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if err := kernel.ValidateImportedProxy(p); err == nil {
			t.Fatalf("R7X-003: ValidateImportedProxy must reject %s row (shared address column)", name)
		}
	}
	var goodProxy kernel.Proxy
	if err := unmarshalProxy(goodExtra, &goodProxy); err != nil {
		t.Fatalf("decode good: %v", err)
	}
	if err := kernel.ValidateImportedProxy(goodProxy); err != nil {
		t.Fatalf("R7X-003 防过修: ValidateImportedProxy must accept the legal row, got %v", err)
	}

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Inbounds: []model.InboundBackup{
				{ID: 30, KernelID: 0, NodeID: 0, Name: "bad-loopback", Protocol: "shadowsocks", Listen: "127.0.0.1", Port: 10011, Extra: loopbackExtra, Enabled: 1, CreatedTime: now},
				{ID: 31, KernelID: 0, NodeID: 0, Name: "bad-metadata", Protocol: "shadowsocks", Listen: "169.254.169.254", Port: 10012, Extra: metadataExtra, Enabled: 1, CreatedTime: now},
				{ID: 32, KernelID: 0, NodeID: 0, Name: "bad-port", Protocol: "shadowsocks", Listen: "203.0.113.9", Port: 99999, Extra: badPortExtra, Enabled: 1, CreatedTime: now},
				{ID: 33, KernelID: 0, NodeID: 0, Name: "good-ss", Protocol: "shadowsocks", Listen: "203.0.113.10", Port: 10013, Extra: goodExtra, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundsImported != 1 {
		t.Fatalf("R7X-003: InboundsImported = %d, want 1 (only the legal row)", result.InboundsImported)
	}
	if result.SkippedRows["inbounds"] != 3 {
		t.Fatalf("R7X-003: SkippedRows[inbounds] = %d, want 3", result.SkippedRows["inbounds"])
	}
	if n := countWhere(t, dest, "node_inbound", "id IN (30,31,32)"); n != 0 {
		t.Fatalf("R7X-003: bad-address rows must not land, got %d", n)
	}
	// 合法行逐字恢复（校验只读不回写）。
	var row model.NodeInbound
	if err := dest.DB().Where("id = ?", 33).First(&row).Error; err != nil {
		t.Fatalf("legal inbound 33 must land: %v", err)
	}
	if row.Extra != goodExtra {
		t.Fatalf("R7X-003 防过修: inbound 33 Extra must restore verbatim, got %q want %q", row.Extra, goodExtra)
	}
	// 身份清单披露（与凭据列同一汇总 warning 口径）：点名三坏行 + 错
	// 误摘要，不点名合法行。
	joined := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"#30 bad-loopback", "#31 bad-metadata", "#32 bad-port"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("R7X-003: skip warning must carry identity %q, got %v", want, result.ImportWarnings)
		}
	}
	if !strings.Contains(joined, "本机") || !strings.Contains(joined, "元数据") || !strings.Contains(joined, "端口不正确") {
		t.Fatalf("R7X-003: skip warning must carry per-row address error summaries, got %v", result.ImportWarnings)
	}
	if strings.Contains(joined, "#33") || strings.Contains(joined, "good-ss") {
		t.Fatalf("R7X-003 防过修: warnings must not name the legal row, got %v", result.ImportWarnings)
	}
}
