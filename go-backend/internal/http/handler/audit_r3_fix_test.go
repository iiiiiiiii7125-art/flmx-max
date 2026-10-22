package handler

// R3 审计第 3 轮修复回归：
//   - R3-A-01：shareHostValue 规范化顺序——带括号 IPv6 + 括号后尾点
//     （[2001:db8::9].）必须规范化为无括号无点值，校验值与下发值同形；
//     双尾点等既有拒绝口径（B2-01 钉版）不得被改序顺手放开。
//   - R3-E-01：节点地址源（ServerIP / 中转入口）收口——保存期
//     IsValidNodeAddress 拒绝结构字符集；拉取期 inboundEndpoints 对
//     全部模式的最终 host 与中转 entry.Host 过 validateShareHostValue
//     同标准，坏值跳过 + 日志（订阅面限定，不动连接/拨号面）。
//   - R3-E-02：存量非法 ShareAddressMode 在读用面归一为 auto，日志
//     不可被存量值伪造（导入面 sanitize 在 repo 包测试）。
//   - R3-B-01：延迟失败分类并入「节点连接已断开」→「节点离线」。
//   - R3-C-01：subTraffic 窗口起点跨界首桶按余量法切分（与
//     NodeTrafficSince/BC-12 同法），窗外份额不计、窗口内守恒。

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- R3-A-01 ----

func TestR3ShareHostValueBracketDotOrder(t *testing.T) {
	for in, want := range map[string]string{
		"[2001:db8::9].":  "2001:db8::9", // 括号后尾点（R3-A-01 本体）
		"[2001:db8::9.]":  "2001:db8::9", // 括号内点形态
		"2001:db8::9].":   "2001:db8::9", // 残缺括号 + 尾点（旧规范形产物）
		"[2001:db8::9]":   "2001:db8::9",
		"[example.com.]":  "example.com",
		"example.com.":    "example.com",
		"example.com":     "example.com",
		"  example.com. ": "example.com",
	} {
		if got := shareHostValue(in); got != want {
			t.Fatalf("shareHostValue(%q) = %q, want %q", in, got, want)
		}
	}
	// 改序不得放开 B2-01 钉版的拒绝口径：双尾点仍须被校验拒绝。
	if err := validateShareHostValue("example.com.."); err == nil {
		t.Fatal("two trailing dots must stay rejected after the reorder")
	}
	if err := validateShareHostValue("[2001:db8::9]."); err != nil {
		t.Fatalf("bracketed IPv6 with trailing dot must validate: %v", err)
	}
}

// TestR3PullBracketedDottedShareHost: a stored custom ShareHost of the
// form [IPv6]. (only reachable from pre-validation rows) must be
// served in canonical form — the value validated is the value shipped.
func TestR3PullBracketedDottedShareHost(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-bracket-dot.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "bdot", "192.0.2.1")
	row := seedShareInbound(t, r, nodeID, "bdot-in")
	row.ShareAddressMode = "custom"
	row.ShareHost = "[2001:db8::9]."
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 1 || endpoints[0].Host != "2001:db8::9" {
		t.Fatalf("bracketed dotted IPv6 must be served canonical, got %+v", endpoints)
	}
}

// ---- R3-E-01 保存期 ----

func TestR3IsValidNodeAddressStructuralChars(t *testing.T) {
	for _, addr := range []string{
		"node.example.com",
		"node.example.com:8443", // host:port 既有可存性保留
		"192.0.2.10",
		"192.0.2.10:8443",
		"2001:db8::9",
		"[2001:db8::9]",
		"[2001:db8::9]:8443",
		"127.0.0.1", // 回环：本地测试节点既有可存性保留（订阅面另拒）
		"10.0.0.8",  // RFC1918 保留
		"localhost", // 既有口径不收紧（保存面只加结构字符集）
		"node1",
	} {
		if err := IsValidNodeAddress(addr); err != nil {
			t.Fatalf("IsValidNodeAddress(%q) must stay storable: %v", addr, err)
		}
	}
	for _, addr := range []string{
		"good.com@evil.com", // @ 改写 URI 权威段
		"a.com\nb.com",      // 换行注入
		"a.com\rb.com",
		"a.com\tb.com",
		"a b.com", // 空格
		"evil.com#frag",
		"a,b.com", // 逗号（多地址分隔符/行结构）
		"a//b.com",
		"a\x00b.com", // 控制字符
		"a\x7fb.com",
		"http://evil.com", // 既有拒绝保留
		"evil.com/path",
		"evil.com?x=1",
	} {
		if err := IsValidNodeAddress(addr); err == nil {
			t.Fatalf("IsValidNodeAddress(%q) must be rejected", addr)
		}
	}
}

// ---- R3-E-01 拉取期 ----

// seedBadEntryRelay builds the F-A-01 relay fixture shape with the
// ENTRY node's ServerIP carrying the hostile value (entry.Host 的来源
// 之一），返回 handler 与落地入站行。
func seedBadEntryRelay(t *testing.T, r *repo.Repository, entryIP string, port int) (*Handler, *model.NodeInbound) {
	t.Helper()
	h := New(r, "unit-test-secret")
	entry := seedRelayTargetNode(t, r, "badentry", entryIP)
	landing := seedRelayTargetNode(t, r, "badlanding", "192.0.2.30")
	row := seedShareInbound(t, r, landing, "badlanding-in")
	now := time.Now().UnixMilli()
	db := r.DB()
	tunnel := model.Tunnel{Name: "line", Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "line", TunnelID: tunnel.ID, RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: entry, Port: port}).Error; err != nil {
		t.Fatal(err)
	}
	rel := model.NodeInboundRelay{InboundID: row.ID, ForwardID: fwd.ID, Name: "line", Managed: 1, CreatedTime: now}
	if err := db.Create(&rel).Error; err != nil {
		t.Fatal(err)
	}
	return h, row
}

func TestR3PullValidatesNodeSourcedHosts(t *testing.T) {
	// 直连 auto：节点 ServerIP 携带结构字符 → 端点跳过 + 日志，
	// 不再裸拼进订阅权威段（R3-E-01 端到端构造）。
	r, err := repo.Open(t.TempDir() + "/pull-auto-bad.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "badip", "good.com@evil.com")
	row := seedShareInbound(t, r, nodeID, "auto-bad-in")
	logs := captureHandlerLogs(t)
	endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 0 {
		t.Fatalf("auto mode with hostile ServerIP must yield no endpoint, got %+v", endpoints)
	}
	if !strings.Contains(logs.String(), "invalid share host") {
		t.Fatalf("the skip must be logged, logs: %q", logs.String())
	}

	// 直连 ipv4 档：ServerIPV4 为回环字面量 → 订阅面拒绝（回环对
	// 远端客户端无意义），跳过 + 日志；节点保存面仍可存（见上）。
	r2, err := repo.Open(t.TempDir() + "/pull-v4-loop.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	h2 := New(r2, "unit-test-secret")
	node2 := shareTestNode()
	node2.ServerIP = "192.0.2.10"
	node2.ServerIPV4.String = "127.0.0.1"
	node2.ServerIPV4.Valid = true
	if err := r2.DB().Create(node2).Error; err != nil {
		t.Fatal(err)
	}
	row2 := seedShareInbound(t, r2, node2.ID, "v4-loop-in")
	row2.ShareAddressMode = "ipv4"
	if err := r2.SaveNodeInbound(row2); err != nil {
		t.Fatal(err)
	}
	logs2 := captureHandlerLogs(t)
	endpoints, _ = h2.inboundEndpoints([]model.NodeInbound{*row2})
	if len(endpoints) != 0 {
		t.Fatalf("ipv4 mode resolving to loopback must yield no endpoint, got %+v", endpoints)
	}
	if !strings.Contains(logs2.String(), "invalid share host") {
		t.Fatalf("the skip must be logged, logs: %q", logs2.String())
	}

	// 对照：正常 ServerIP 的 auto 端点照常下发。
	r3, err := repo.Open(t.TempDir() + "/pull-auto-good.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r3.Close() })
	h3 := New(r3, "unit-test-secret")
	node3 := seedRelayTargetNode(t, r3, "goodip", "192.0.2.99")
	row3 := seedShareInbound(t, r3, node3, "auto-good-in")
	endpoints, _ = h3.inboundEndpoints([]model.NodeInbound{*row3})
	if len(endpoints) != 1 || endpoints[0].Host != "192.0.2.99" {
		t.Fatalf("good auto endpoint must flow unchanged, got %+v", endpoints)
	}
}

func TestR3PullValidatesRelayEntryHost(t *testing.T) {
	// 中转 entry.Host 源自入口节点 ServerIP 且坏值 → 跳过该 entry +
	// 日志（entry 已是最底层兜底，无更下层回退）；落地直连端点不受
	// 影响。
	r, err := repo.Open(t.TempDir() + "/pull-entry-bad.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h, row := seedBadEntryRelay(t, r, "good.com@evil.com", 45001)
	logs := captureHandlerLogs(t)
	endpoints, kinds := h.inboundEndpoints([]model.NodeInbound{*row})
	for i, e := range endpoints {
		if kinds[i] == "relay" {
			t.Fatalf("hostile relay entry must be skipped, got relay endpoint %+v", e)
		}
	}
	if !strings.Contains(logs.String(), "invalid relay entry host") {
		t.Fatalf("the entry skip must be logged, logs: %q", logs.String())
	}

	// 对照：入口地址正常时中转端点照常下发。
	r2, err := repo.Open(t.TempDir() + "/pull-entry-good.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	h2, row2 := seedBadEntryRelay(t, r2, "198.51.100.10", 45002)
	endpoints, kinds = h2.inboundEndpoints([]model.NodeInbound{*row2})
	found := false
	for i, e := range endpoints {
		if kinds[i] == "relay" && e.Host == "198.51.100.10" && e.Port == 45002 {
			found = true
		}
	}
	if !found {
		t.Fatalf("good relay entry must flow unchanged, got %+v kinds %v", endpoints, kinds)
	}
}

// ---- R3-E-02 读用面 ----

func TestR3InboundShareModeNormalizesStoredValues(t *testing.T) {
	for _, tc := range []struct{ mode, host, want string }{
		{"auto", "", "auto"},
		{"ipv4", "", "ipv4"},
		{"ipv6", "", "ipv6"},
		{"custom", "", "custom"},
		{"", "old.example.com", "custom"},    // 存量升级规则保留
		{"bogus", "", "auto"},                // 非法 mode 归一
		{"evil\nmode", "", "auto"},           // 含换行的存量值不得原样进日志/视图
		{"IPV4", "", "auto"},                 // 大小写变体非法（保存面同口径小写枚举）
		{"bogus", "old.example.com", "auto"}, // 非法 mode 不触发 custom 升级（与旧 default 分支同结果）
	} {
		row := &model.NodeInbound{ShareAddressMode: tc.mode, ShareHost: tc.host}
		if got := inboundShareMode(row); got != tc.want {
			t.Fatalf("mode=%q host=%q: got %q want %q", tc.mode, tc.host, got, tc.want)
		}
	}
}

// ---- R3-B-01 ----

func TestR3LatencyCommandErrorClassification(t *testing.T) {
	for msg, want := range map[string]string{
		"节点连接已断开":                "节点离线", // 在途命令被断线终局（R3-B-01）
		"节点不在线":                  "节点离线", // 发起时离线（既有口径）
		"TCP探测任务过多":              "节点繁忙，请稍后重试",
		"等待节点响应超时":               "节点响应超时",
		"Agent is shutting down": "探测已取消",
	} {
		if got := latencyCommandError(errors.New(msg)); got != want {
			t.Fatalf("latencyCommandError(%q) = %q, want %q", msg, got, want)
		}
	}
}

// ---- R3-C-01 ----

// R3-C-01：窗口起点跨界首桶——+5:45 时区里 day 窗口起点的 UTC 小时
// 行有 15 分钟落在窗外，该份额必须按余量法（值 − floor(值×窗外/60)）
// 扣除、只计窗内 45 分钟；R3-C 探针数值：应 851/600（现实现整行计
// 入得 1101/783）。
func TestR3SubTrafficWindowStartProration(t *testing.T) {
	loc := time.FixedZone("NPT", (5*60+45)*60)
	now := time.Date(2026, 3, 2, 10, 30, 0, 0, loc)
	utcHour := func(y int, m time.Month, d, h int) int64 {
		return time.Date(y, m, d, h, 0, 0, 0, time.UTC).UnixMilli()
	}
	// day 窗口首桶 = 本地 2026-02-01 00:00 = UTC 2026-01-31 18:15。
	// 18:00–19:00 UTC 行横跨窗口起点：窗外 15 分钟、窗内 45 分钟。
	hours := []model.SubscriptionTraffic{
		{Hour: utcHour(2026, 1, 31, 18), UploadBytes: 1001, DownloadBytes: 733},
		{Hour: utcHour(2026, 1, 31, 19), UploadBytes: 100, DownloadBytes: 50},
	}
	view := subTraffic(hours, "day", now)
	if view.Points[0].Up != 851 || view.Points[0].Down != 600 {
		t.Fatalf("window-start bucket must count only the in-window share: %+v", view.Points[0])
	}
	if view.Up != 851 || view.Down != 600 {
		t.Fatalf("totals must exclude the out-of-window share: up=%d down=%d", view.Up, view.Down)
	}

	// 整点时区逐值不变：窗口起点与小时行对齐时整行计入。
	nowUTC := time.Date(2026, 3, 2, 10, 30, 0, 0, time.UTC)
	starts := subTrafficBuckets("day", nowUTC)
	hoursUTC := []model.SubscriptionTraffic{
		{Hour: starts[0].UnixMilli(), UploadBytes: 1001, DownloadBytes: 733},
	}
	viewUTC := subTraffic(hoursUTC, "day", nowUTC)
	if viewUTC.Points[0].Up != 1001 || viewUTC.Points[0].Down != 733 || viewUTC.Up != 1001 {
		t.Fatalf("whole-hour zones must be byte-identical to the old result: %+v", viewUTC.Points[0])
	}
}
