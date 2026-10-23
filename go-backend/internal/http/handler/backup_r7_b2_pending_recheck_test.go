package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R7X-002 可选防御纵深（不列关闭条件，仅登记）：pending 重试 job 在
// 终删前复查 forward.status——侧表记账与行状态不一致时（例如该转发
// 已被备份恢复、status 已归一/直通为非 2），只清记账、不删行。本用
// 例模拟恢复后的行（status=1 + 存量到期侧表行）跑一个重试 tick：
// 行必须存活、记账必须清零。修复前 job 只读侧表无条件终删，本用例
// 红。
func TestR7B2PendingRetryRechecksRestoredForward(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	if err := h.repo.MarkForwardPendingDelete(forward.ID, fixture.nodeID, time.Now()); err != nil {
		t.Fatalf("mark pending: %v", err)
	}
	// 模拟备份恢复已把该行恢复为活跃（status=1），侧表记账残留到期。
	if err := h.repo.DB().Model(&model.Forward{}).Where("id = ?", forward.ID).
		Update("status", model.ForwardStatusActive).Error; err != nil {
		t.Fatalf("simulate restore: %v", err)
	}
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)

	h.retryNftPendingDeletes(time.Now().Add(2 * time.Minute))

	if fr, err := h.repo.GetForwardRecord(forward.ID); err != nil || fr == nil {
		t.Fatalf("restored forward must survive the retry tick, got %+v err=%v", fr, err)
	}
	if pending, _ := h.repo.GetNftPendingDelete(forward.ID); pending != nil {
		t.Fatalf("stale bookkeeping must be cleared by the recheck, got %+v", pending)
	}
}
