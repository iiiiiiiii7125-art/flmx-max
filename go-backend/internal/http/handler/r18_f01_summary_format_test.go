package handler

import (
	"testing"

	"go-backend/internal/store/model"
)

// R18-F01 披露汇总格式（自 r18_f01_tunnel_delete_disclosure_test.go
// 拆出保留）：formatDeletedForwardsSummary 是本地删除路径仍在用的
// 回执汇总函数——恒含已删数；>0 附「名(#ID)」标识；无名转发只标
// #ID。原文件其余用例全部经联邦远端面板夹具注入数据面失败，随
// 联邦/共享板块于 2.1.0 整删而删除。
func TestR18F01DeletedForwardsSummaryFormat(t *testing.T) {
	if got := formatDeletedForwardsSummary(nil); got != "已删除 0 条转发" {
		t.Fatalf("empty summary = %q", got)
	}
	got := formatDeletedForwardsSummary([]model.ForwardRecord{
		{ID: 7, Name: "r18f01-a"},
		{ID: 9, Name: ""},
	})
	if want := "已删除 2 条转发：r18f01-a(#7)、#9"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}
