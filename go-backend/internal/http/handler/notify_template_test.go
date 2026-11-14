package handler

import (
	"strings"
	"testing"
	"time"
)

// TestNotifyTextTemplate pins the HTML rendering of notifyText: emoji +
// bold title, body lines as-is, clock footer. Time format is unchanged
// from the old plain-text template.
func TestNotifyTextTemplate(t *testing.T) {
	when := time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC).UnixMilli()
	// notifyText formats the timestamp in local time, so the expected
	// footer must be rendered the same way — hardcoding UTC fails
	// anywhere the local zone is not UTC.
	whenStr := time.UnixMilli(when).Format("2006-01-02 15:04:05 MST")
	cases := []struct {
		name string
		msg  notifyMessage
		want string
	}{
		{
			name: "node offline",
			msg:  notifyMessage{Event: notifyEventNodeOffline, Title: "节点掉线", Message: "节点：香港-IX", Time: when, App: "FLMX"},
			want: "🔴 <b>节点掉线</b>\n节点：香港-IX\n🕐 " + whenStr,
		},
		{
			name: "test notification",
			msg:  notifyMessage{Event: notifyEventTest, Title: "测试通知", Message: "收到这条消息说明通知配置正确。", Time: when, App: "FLMX"},
			want: "ℹ️ <b>测试通知</b>\n收到这条消息说明通知配置正确。\n🕐 " + whenStr,
		},
		{
			name: "multi-line body kept as-is",
			msg:  notifyMessage{Event: notifyEventNodeExpiry, Title: "节点即将到期", Message: "节点：香港-IX\n到期：2026-12-01（剩余约 5 天）", Time: when, App: "FLMX"},
			want: "⚠️ <b>节点即将到期</b>\n节点：香港-IX\n到期：2026-12-01（剩余约 5 天）\n🕐 " + whenStr,
		},
		{
			name: "empty body drops the middle line",
			msg:  notifyMessage{Event: notifyEventNodeOffline, Title: "节点恢复在线", Message: "", Time: when, App: "FLMX"},
			want: "🟢 <b>节点恢复在线</b>\n🕐 " + whenStr,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := notifyText(tc.msg); got != tc.want {
				t.Fatalf("notifyText() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNotifyTextEscapesHTML proves user-controlled values cannot break
// Telegram HTML parsing or inject tags: a node name carrying <>& must
// arrive escaped, never as live markup.
func TestNotifyTextEscapesHTML(t *testing.T) {
	msg := notifyMessage{
		Event:   notifyEventNodeOffline,
		Title:   `节点 <X>&"Y" 掉线`,
		Message: "节点：<b>注入</b>&尾巴",
		Time:    time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC).UnixMilli(),
		App:     "FLMX",
	}
	got := notifyText(msg)
	if strings.Contains(got, "<X>") || strings.Contains(got, "<b>注入</b>") {
		t.Fatalf("user markup leaked into output: %q", got)
	}
	if !strings.Contains(got, "&lt;X&gt;&amp;&#34;Y&#34;") {
		t.Fatalf("title not escaped: %q", got)
	}
	if !strings.Contains(got, "节点：&lt;b&gt;注入&lt;/b&gt;&amp;尾巴") {
		t.Fatalf("body not escaped: %q", got)
	}
	// Only the intended wrapper may carry a <b> tag.
	if strings.Count(got, "<b>") != 1 || strings.Count(got, "</b>") != 1 {
		t.Fatalf("unexpected <b> count in output: %q", got)
	}
}

// TestNotifyEmojiMapping covers every event kind plus the recovery-title
// override (any title containing "恢复" is 🟢 regardless of kind).
func TestNotifyEmojiMapping(t *testing.T) {
	cases := []struct {
		event string
		title string
		want  string
	}{
		{notifyEventNodeOffline, "节点掉线", "🔴"},
		{notifyEventNodeUpgradeFailed, "节点升级失败", "🔴"},
		{notifyEventInboundDown, "导入节点连不上", "🔴"},
		{notifyEventKernelError, "内核运行出错", "🔴"},
		{"backup", "Gist 备份失败", "🔴"},
		{"backup_failed", "备份失败", "🔴"},
		{notifyEventNodeExpiry, "节点即将到期", "⚠️"},
		{notifyEventLatencyBad, "延迟 / 丢包异常", "⚠️"},
		{notifyEventUserQuota, "用户流量即将用完", "⚠️"},
		{notifyEventConntrackHigh, "连接跟踪表过高", "⚠️"},
		{notifyEventDiskHigh, "磁盘使用率过高", "⚠️"},
		{notifyEventAgentMemHigh, "Agent 内存过高", "⚠️"},
		{notifyEventRouteChange, "回程线路变化", "ℹ️"},
		{notifyEventTest, "测试通知", "ℹ️"},
		{notifyEventDailyReport, "每日流量报告", "ℹ️"},
		{"bogus_event", "未知事件", "ℹ️"},
		{"", "未知事件", "ℹ️"},
		// Recovery titles always win over the kind mapping.
		{notifyEventNodeOffline, "节点恢复在线", "🟢"},
		{notifyEventInboundDown, "导入节点已恢复", "🟢"},
		{notifyEventLatencyBad, "延迟已恢复", "🟢"},
		{notifyEventKernelError, "内核恢复正常", "🟢"},
	}
	for _, tc := range cases {
		t.Run(tc.event+"/"+tc.title, func(t *testing.T) {
			if got := notifyEmoji(tc.event, tc.title); got != tc.want {
				t.Fatalf("notifyEmoji(%q, %q) = %q, want %q", tc.event, tc.title, got, tc.want)
			}
			// The emoji must also be the first thing notifyText renders.
			msg := notifyMessage{Event: tc.event, Title: tc.title, Message: "x",
				Time: time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC).UnixMilli(), App: "FLMX"}
			if got := notifyText(msg); !strings.HasPrefix(got, tc.want+" <b>") {
				t.Fatalf("notifyText() = %q, want prefix %q", got, tc.want+" <b>")
			}
		})
	}
}
