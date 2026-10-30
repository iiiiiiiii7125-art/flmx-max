package handler

import (
	"strings"
	"testing"
)

// R1-C08（B10-002）：subscription/save 的 validateDetailed 口径——
// 不存在的入站 ID 必须显式报错（与 userSubscriptionSet 对未知订阅
// ID 的拒绝口径统一），不得再静默丢弃；伴生入站维持 B-06 排除，
// 但须经 excluded 回传给响应层标注，不得无声消失。
func TestC08ValidateDetailedUnknownRejectedCompanionAnnotated(t *testing.T) {
	r, h, _, origin, _ := seedMeteringFixture(t)
	h.reconcileSSMetering()
	comp, err := r.GetCompanionInbound(origin.ID)
	if err != nil || comp == nil {
		t.Fatalf("companion must exist: %v %v", comp, err)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}

	// 不存在 ID：整体报错且点名该 ID，真 ID 同时在场也不放行。
	req := subscriptionRequest{Name: "v", InboundIDs: []int64{origin.ID, 77777}}
	if _, _, err := req.validateDetailed(rows); err == nil || !strings.Contains(err.Error(), "77777") {
		t.Fatalf("unknown inbound id must be refused and named, got %v", err)
	}
	// validate() 包装面同口径（save/preview 之外的既有调用面）。
	if _, err := req.validate(rows); err == nil {
		t.Fatal("validate wrapper must also refuse unknown inbound ids")
	}
	// 只有不存在 ID（无任何有效源）同样报错而非落入"至少选择一个"。
	req = subscriptionRequest{Name: "v", InboundIDs: []int64{77777}}
	if _, _, err := req.validateDetailed(rows); err == nil || !strings.Contains(err.Error(), "77777") {
		t.Fatalf("unknown-only list must be refused and named, got %v", err)
	}

	// 伴生 ID：不报错、从 ids 排除、经 excluded 回传标注（B-06 守恒）。
	req = subscriptionRequest{Name: "v", InboundIDs: []int64{origin.ID, comp.ID}}
	ids, excluded, err := req.validateDetailed(rows)
	if err != nil {
		t.Fatalf("mixed origin+companion must validate: %v", err)
	}
	if len(ids) != 1 || ids[0] != origin.ID {
		t.Fatalf("companion must be excluded from ids, got %v", ids)
	}
	if len(excluded) != 1 || excluded[0] != comp.ID {
		t.Fatalf("companion exclusion must be reported, got excluded=%v", excluded)
	}

	// 全有效：excluded 为空。
	req = subscriptionRequest{Name: "v", InboundIDs: []int64{origin.ID}}
	if _, excluded, err := req.validateDetailed(rows); err != nil || len(excluded) != 0 {
		t.Fatalf("clean list: err=%v excluded=%v", err, excluded)
	}
}
