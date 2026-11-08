package handler

import (
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"testing"
	"time"
)

// mustBuildNodeFlowUploadBatch 是 R2-019 后归属用例的统一入口：
// buildNodeFlowUploadBatch 现在返回 error，本文件及同包既有归属
// 用例的库都是健康的，查询出错即测试失败。
func mustBuildNodeFlowUploadBatch(t *testing.T, h *Handler, nodeID int64, items []flowItem, metas map[int64]repo.FlowUploadForwardMeta) flowUploadBatch {
	t.Helper()
	batch, err := h.buildNodeFlowUploadBatch(nodeID, items, metas)
	if err != nil {
		t.Fatalf("buildNodeFlowUploadBatch node_id=%d: %v", nodeID, err)
	}
	return batch
}

func TestFlowOwnershipLocalForwardRequiresReportingNode(t *testing.T) {
	a := newCleanupAgent(t)
	if err := a.h.repo.DB().Create(&model.Forward{ID: 70, UserID: 1, UserName: "local", Name: "local", TunnelID: 9, RemoteAddr: "127.0.0.1:80", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.h.repo.DB().Create(&model.ForwardPort{ForwardID: 70, NodeID: 2, Port: 32000}).Error; err != nil {
		t.Fatal(err)
	}
	metas, err := a.h.repo.GetFlowUploadForwardMetas([]int64{70})
	if err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []int64{1, 2} {
		batch := mustBuildNodeFlowUploadBatch(t, a.h, nodeID, []flowItem{{N: "70_1_0_tcp", U: 120, D: 80}}, metas)
		a.h.applyFlowUploadBatch(nodeID, batch, time.Now())
		var forward model.Forward
		if err := a.h.repo.DB().First(&forward, 70).Error; err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if nodeID == 2 {
			// N-01 方向口径：该测试不创建隧道，GetFlowUploadForwardMetas
			// 将缺失隧道行默认 TunnelFlow=1（单向=仅计入方向）。线格式
			// D=入（客户端→远端）、U=出。R5X-013 口径切换后 forward 行
			// 落 raw 全量原始字节（展示口径，不受单向计费门控），故节点 2
			// 上报 U=120、D=80 在 forward 行合计 200；单向门控只作用于
			// user/user_tunnel 行与配额的 scaled 面。旧期望 80 是切换前
			// forward 行同落 scaled 时的值（主控裁定驱动的连带更新，
			// 见 R5-FIX-REPORT 断言登记）。本测试本体——属主节点才入账
			// ——不变：非属主节点 1 仍为 0。
			want = 200
		}
		if forward.InFlow+forward.OutFlow != want {
			t.Fatalf("node %d local traffic=%d want=%d", nodeID, forward.InFlow+forward.OutFlow, want)
		}
	}
}
