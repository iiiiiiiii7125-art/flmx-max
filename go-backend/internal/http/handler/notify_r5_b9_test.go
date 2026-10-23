package handler

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R5X-006（批 9，迁移案 (b) 拆层）：原契约用例
// TestNotifyImportedNodeDownContract 只经手动端点 /api/v1/inbound/probe
// 驱动 inbound_down 告警，而 DB-4（1.4.0 已发布语义）规定手动轮
// （probeRoundTargeted）不计 streak、track() 不可达——该用例在有
// RFC1918 地址的环境确定性红、无地址环境 Skip 冒充 Pass。本文件把
// 告警状态机断言下沉到 handler 包，复用 B10 的定时轮直调模式
// （probeExternalInbounds + probeRoundScheduled）；contract 包只保留
// 「手动端点返回探测结果、不驱动告警」的 DB-4 正向断言
// （TestNotifyImportedNodeDownContract 已按此改写）。
//
// 与原契约的断言对应（强度不放宽）：一次失败不告警 / 连续两次失败
// 恰一条掉线告警 / 持续故障不重复告警 / 恢复恰一条恢复告警 /
// 未选事件静默。前四项中恢复一项单列第二个用例：它需要「拨号到已
// 监听的 RFC1918 地址」一腿，个别沙箱对 RFC1918 自连施加 connect()
// EPERM（R5-I 报告 E3 在案；同文件 B10 恢复用例在本类沙箱同点受阻），
// 故用例先做环境自检，自检不过即 Skip 明示——引用结论时必须区分
// Skip/Pass（R5X-006 验收口径），不得以 Skip 冒充 Pass。

func r5b9SetNotifyEvents(t *testing.T, h *Handler, value string) {
	t.Helper()
	if err := h.repo.DB().Exec(`
		INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, time = excluded.time`,
		"notify_events", value, time.Now().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
}

func r5b9DeadPort(t *testing.T, probeAddr string) int {
	t.Helper()
	ln, err := net.Listen("tcp", probeAddr+":0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // 死端口：定时轮拨号失败 → 失败结果
	return port
}

func r5b9CreateImported(t *testing.T, h *Handler, name, probeAddr string, port int) *model.NodeInbound {
	t.Helper()
	row := &model.NodeInbound{Name: name, Protocol: "ss", Listen: probeAddr, Port: port,
		Enabled: 1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := h.repo.DB().Create(row).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	return row
}

func TestR5B9ImportedNodeDownScheduledRoundAlerts(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	ctx := context.Background()

	// 与 B10 同型夹具口径：probe 拨号前过 netguard 拒 loopback，
	// 被探测目标须为非 loopback RFC1918 地址；无此地址时 Skip。
	probeAddr := probeFixtureLocalAddr(t)
	r5b9SetNotifyEvents(t, h, "inbound_down")

	row := r5b9CreateImported(t, h, "r5b9-imported", probeAddr, r5b9DeadPort(t, probeAddr))
	_ = row

	scheduled := func() {
		t.Helper()
		if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
			t.Fatal(err)
		}
	}

	// 第一轮失败：streak 未满（notifyInboundDownStreak=2），不告警。
	scheduled()
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 0 {
		t.Fatalf("one failed scheduled round must not alert yet, got %d", got)
	}

	// 第二轮失败：恰一条掉线告警，event 与正文口径与原契约一致。
	scheduled()
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); titles[0] != "导入节点连不上" {
		t.Fatalf("down alert title = %v", titles)
	}
	wh.mu.Lock()
	downMsg := wh.msgs[0]
	wh.mu.Unlock()
	if downMsg.Event != "inbound_down" || !strings.Contains(downMsg.Message, "r5b9-imported") {
		t.Fatalf("down alert payload = %+v", downMsg)
	}

	// 持续故障的后续定时轮：不再重复告警。
	scheduled()
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("an ongoing outage must alert only once, got %d", got)
	}

	// 未选事件静默：改选其它事件后，新入站（独立行、独立 streak）
	// 被定时轮打满 streak 也不得发出 inbound_down。用第二行而非已
	// down 的第一行，保证断言对象是事件选择门本身（第一行已 down
	// 不会再产生跃迁，拿它断言静默是空断言）。
	r5b9SetNotifyEvents(t, h, "node_offline")
	r5b9CreateImported(t, h, "r5b9-imported-unselected", probeAddr, r5b9DeadPort(t, probeAddr))
	for i := 0; i < notifyInboundDownStreak; i++ {
		scheduled()
	}
	time.Sleep(200 * time.Millisecond)
	if got := wh.count(); got != 1 {
		t.Fatalf("unselected events must not be sent, got %d", got)
	}
}

func TestR5B9ImportedNodeRecoveryScheduledRoundAlerts(t *testing.T) {
	wh := newFixdWebhook(t)
	h, _ := setupFixdNotifyHandler(t, wh)
	ctx := context.Background()
	probeAddr := probeFixtureLocalAddr(t)

	// 环境自检：恢复腿要求能拨通本机已监听的 RFC1918 地址。沙箱
	// 出口策略对 RFC1918 自连施加 connect() EPERM 时（R5-I E3），
	// 「目标恢复」在本环境不可表达——Skip 明示，留待 CI/无过滤
	// 环境验收（B10 恢复用例同口径）。
	ln, err := net.Listen("tcp", probeAddr+":0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(probeAddr, strconv.Itoa(port)), 2*time.Second)
	if dialErr != nil {
		_ = ln.Close()
		t.Skipf("本环境无法拨通已监听的 RFC1918 地址（沙箱 connect() 策略），恢复腿待 CI/无过滤环境验收: %v", dialErr)
	}
	_ = conn.Close()
	_ = ln.Close() // 转死端口：先打掉线，再在同端口重新监听作恢复

	r5b9SetNotifyEvents(t, h, "inbound_down")
	row := r5b9CreateImported(t, h, "r5b9-recovery", probeAddr, port)

	scheduled := func() {
		t.Helper()
		if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < notifyInboundDownStreak; i++ {
		scheduled()
	}
	wh.waitCount(t, 1, 3*time.Second)
	if titles := wh.titles(); titles[0] != "导入节点连不上" {
		t.Fatalf("down alert title = %v", titles)
	}

	ln2, err := net.Listen("tcp", probeAddr+":"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("relisten: %v", err)
	}
	defer ln2.Close()
	scheduled()
	wh.waitCount(t, 2, 3*time.Second)
	if titles := wh.titles(); titles[1] != "导入节点已恢复" {
		t.Fatalf("recovery alert title = %v", titles)
	}
	if streak, down := externalStreakState(h, row.ID); streak != 0 || down {
		t.Fatalf("recovery must clear alert state: streak=%d down=%v", streak, down)
	}
}
