package repo

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// F-013 (B4): 转发占用预检的两个实现漏检 NodeInbound——
// FindOtherForwardOnNodePortTx 只查 forward_port + chain_tunnel，
// GetUsedPortsOnNodeAsMap 同样。入站占用的端口对转发预检不可见，
// 同节点同端口的入站+转发双占用被放行，运行时 bind 才失败。
// 对照口径：HasOtherForwardOnNodePortTx 三表齐全。

func TestPortPrecheckSeesNodeInbounds(t *testing.T) {
	r := openCascadeRepo(t, "precheck.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "26000-26100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.NodeInbound{ID: 21, NodeID: 1, KernelID: 11, Name: "ib-26001", Protocol: "vless", Port: 26001, CreatedTime: now, UpdatedTime: now})

	conflict, err := r.FindOtherForwardOnNodePort(1, 26001, 0)
	if err != nil {
		t.Fatalf("FindOtherForwardOnNodePort: %v", err)
	}
	if conflict == nil {
		t.Fatal("F-013: precheck must report the node_inbound occupying (node 1, port 26001)")
	}
	if conflict.Source != "inbound" {
		t.Fatalf("conflict source = %q, want %q", conflict.Source, "inbound")
	}

	used, err := r.GetUsedPortsOnNodeAsMap(1)
	if err != nil {
		t.Fatalf("GetUsedPortsOnNodeAsMap: %v", err)
	}
	if !used[26001] {
		t.Fatal("F-013: used-ports map must include the inbound's port 26001 (auto-pick would hand it to a forward)")
	}

	// 未占用端口必须仍报告无冲突（防误杀）。
	conflict, err = r.FindOtherForwardOnNodePort(1, 26002, 0)
	if err != nil {
		t.Fatalf("FindOtherForwardOnNodePort free port: %v", err)
	}
	if conflict != nil {
		t.Fatalf("free port must not conflict, got %+v", conflict)
	}
	if used[26002] {
		t.Fatal("free port 26002 must not be marked used")
	}
}
