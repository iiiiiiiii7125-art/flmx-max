package handler

import (
	"strings"
	"testing"
	"time"
)

// F2 (R4, F3-NEW-02)：日报「未列出」分支此前零测试断言——各行只
// 在有数时出现，缺行须说明原因（查询失败 vs 无数据）。以下两
// 格分别钉住两种原因：空库（无数据）与仓储关闭（查询失败）。
// 红证：任一原因文案改动一字，本测试即红（见 TestF2DailyReport…
// 的变异复核——临时改原因串须 FAIL，恢复后 PASS）。

func TestF2DailyReportOmittedReasonsNoData(t *testing.T) {
	tg := newB10Chan(t, true)
	wh := newB10Chan(t, false)
	h, _ := setupB10DualHandler(t, tg, wh)
	text, _, _ := h.dailyReportText(time.Now())
	// 空库：入站流量 0 值仍成行；默认分享订阅（autoMigrate 种子、
	// 有效）也成行；其余 5 行缺行，原因全是「无数据」类。
	want := "入站流量（累计）：0 B\n分享订阅：1 个有效，0 个 3 天内到期\n" +
		"未列出：节点在线（无节点）、内核出错（无内核记录）、导入节点连不上（无导入节点探针数据）、" +
		"转发流量（过去 24 小时无转发流量）、日/月配额超额用户（无超额用户）"
	if text != want {
		t.Fatalf("no-data daily text = %q\nwant %q", text, want)
	}
	if strings.Contains(text, "查询失败") {
		t.Fatalf("no-data daily text must not blame query failures:\n%s", text)
	}
}

func TestF2DailyReportOmittedReasonsQueryFailure(t *testing.T) {
	tg := newB10Chan(t, true)
	wh := newB10Chan(t, false)
	h, r := setupB10DualHandler(t, tg, wh)
	// 仓储关闭：后续全部查询失败，各行缺行原因须是「查询失败」类。
	if err := r.Close(); err != nil {
		t.Fatalf("close repo: %v", err)
	}
	text, _, _ := h.dailyReportText(time.Now())
	if !strings.HasPrefix(text, "暂无数据\n未列出：") {
		t.Fatalf("query-failure daily text must start with 暂无数据 + 未列出, got:\n%s", text)
	}
	for _, want := range []string{
		"节点在线（节点统计查询失败）",
		"内核出错（内核列表查询失败）",
		"入站流量（入站流量统计查询失败）",
		"转发流量（转发流量统计查询失败）",
		"日/月配额超额用户（用户列表查询失败）",
		"分享订阅（订阅列表查询失败）",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("daily text missing %q:\n%s", want, text)
		}
	}
	// 探针行不走仓储：New() 已配空探针仓，原因仍是无数据类——
	// 这是正确行为，但不得与查询失败混淆：断言它在场且措辞不变。
	if !strings.Contains(text, "导入节点连不上（无导入节点探针数据）") {
		t.Fatalf("probe row reason must stay no-data:\n%s", text)
	}
	for _, noData := range []string{"（无节点）", "（无内核记录）", "（无分享订阅）", "（无超额用户）", "（过去 24 小时无转发流量）"} {
		if strings.Contains(text, noData) {
			t.Fatalf("query-failure daily text must not claim no-data %q:\n%s", noData, text)
		}
	}
}
