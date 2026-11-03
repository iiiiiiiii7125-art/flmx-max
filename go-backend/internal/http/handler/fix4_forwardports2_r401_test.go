package handler

import (
	"strings"
	"testing"
)

// R4-01（LEDGER4，中）＋ D-F3-02（信息，併修）：「在席 × 异口」格。
//
// 修复前：R2-005 入口端口锁（forwardUpdate）对提交 inPort 与
// forward_port 逐行比对，前端编辑恒提交列表口（首行口），异口行集
// 下任何单值至多与一行相等、其余行必触发拒绝——异口转发经产品
// 前端完全不可编辑（改名等任意编辑皆然），回执还引用首个不一致
// 行的端口（与表单显示值不同）。修复轮 2 的同值放行用例是同口行
// 集、修复轮 3 的 CF301 八用例 payload 全部不带 inPort，该格零覆
// 盖。本轮口径：锁改集合语义——在席 inPort ∈ 现有行口集 ⇒ 非变更、
// 放行且逐节点保口（被接受的提交值不带逐节点含义，下游新行口回
// 规范口，与缺席路径语义一致）；∉ 行口集 ⇒ 真变更企图、拒绝且
// 文案引用提交值本身。
//
// D-F3-02 併修：删隧道迁移 PortAdjustedCount 改按规范口漂移计数
// （迁移后行集 MIN ≠ 迁移前 MIN 即计数），钉死于本文件末两例。
//
// 夹具复用 fix3_forwardports_cf301_test.go 的 setupFix3ForwardPorts。

// 在席 × 异口：提交值命中行口集中的任一行口（MIN 口与非 MIN 口
// 各一）都必须放行——旧逐行比对下两值都会被另一行触发拒绝。
func TestFix4R401ForwardUpdateHeteroPresentRowPortAccepted(t *testing.T) {
	for _, submitted := range []int{15000, 35000} {
		fx := setupFix3ForwardPorts(t)
		nodeA := fx.addNode(t, "r401-a", "198.51.100.111", "10000-20000")
		nodeB := fx.addNode(t, "r401-b", "198.51.100.112", "30000-40000")
		tunnelID := fx.addTunnel(t, "r401-t1", nodeA, nodeB)
		id := fx.addForward(t, "r401-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

		code, msg := fx.callForwardUpdate(t, map[string]interface{}{
			"id": id, "name": "r401-hetero-renamed", "inPort": submitted,
		})
		if code != 0 {
			t.Fatalf("submitted inPort=%d (an existing row port) must be accepted as no-change, got code=%d msg=%s", submitted, code, msg)
		}
		assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000})
		fwd, err := fx.h.getForwardRecord(id)
		if err != nil {
			t.Fatalf("get forward: %v", err)
		}
		if fwd.Name != "r401-hetero-renamed" {
			t.Fatalf("forward name = %q, want rename applied", fwd.Name)
		}
	}
}

// 在席 × 异口：提交集外口 ⇒ 真变更企图，拒绝；文案必须引用提交
// 值本身（旧实现引用首个不一致行的端口，用户看到的是自己从未
// 提交过的数字），且不落库（名与行集都不变）。
func TestFix4R401ForwardUpdateHeteroPresentForeignPortRejected(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "r401f-a", "198.51.100.121", "10000-20000")
	nodeB := fx.addNode(t, "r401f-b", "198.51.100.122", "30000-40000")
	tunnelID := fx.addTunnel(t, "r401f-t1", nodeA, nodeB)
	id := fx.addForward(t, "r401f-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{
		"id": id, "name": "r401f-renamed", "inPort": 25000,
	})
	if code == 0 {
		t.Fatalf("foreign inPort=25000 must be rejected, got success")
	}
	if !strings.Contains(msg, "入口端口不可变更") {
		t.Fatalf("rejection msg must name entry-port immutability, got %q", msg)
	}
	if !strings.Contains(msg, "25000") {
		t.Fatalf("rejection msg must cite the submitted port 25000, got %q", msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000})
	fwd, err := fx.h.getForwardRecord(id)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.Name != "r401f-hetero" {
		t.Fatalf("rejected update must not persist, name = %q", fwd.Name)
	}
}

// 在席 × 异口 × hasInIP 分支（replaceForwardPorts 路径）：命中行
// 口同样放行、逐节点保口。
func TestFix4R401ForwardUpdateHeteroPresentWithInIPAccepted(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "r401i-a", "198.51.100.131", "10000-20000")
	nodeB := fx.addNode(t, "r401i-b", "198.51.100.132", "10000-40000")
	tunnelID := fx.addTunnel(t, "r401i-t1", nodeA, nodeB)
	id := fx.addForward(t, "r401i-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{
		"id": id, "name": "r401i-renamed", "inPort": 35000, "inIp": "",
	})
	if code != 0 {
		t.Fatalf("update with inIp key and row-port inPort must succeed, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000})
}

// 在席 × 异口 × 切隧道：命中行口放行；保留节点逐节点保口，真正
// 新增的节点 C 用规范口（MIN 15000）而非提交值 35000——被接受的
// 提交值不带逐节点含义，与缺席路径（CF301 切隧道用例）语义一致。
// C 的规格（10000-20000）不含 35000，若下游按提交值定口会在逐节
// 点校验处误拒，本格同时钉住该交互。
func TestFix4R401ForwardUpdateHeteroPresentTunnelSwitchAccepted(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "r401s-a", "198.51.100.141", "10000-20000")
	nodeB := fx.addNode(t, "r401s-b", "198.51.100.142", "30000-40000")
	nodeC := fx.addNode(t, "r401s-c", "198.51.100.143", "10000-20000")
	tunnelID := fx.addTunnel(t, "r401s-t1", nodeA, nodeB)
	targetID := fx.addTunnel(t, "r401s-t2", nodeA, nodeB, nodeC)
	id := fx.addForward(t, "r401s-hetero", tunnelID, map[int64]int{nodeA: 15000, nodeB: 35000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{
		"id": id, "tunnelId": targetID, "name": "r401s-switched", "inPort": 35000,
	})
	if code != 0 {
		t.Fatalf("tunnel switch with row-port inPort must succeed, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000, nodeC: 15000})
}

// 在席 × 同口（多行同口）：集合语义在同口行集上退化为 R2-005 既
// 有口径——同值放行、异值拒绝且文案引用提交值。
func TestFix4R401ForwardUpdateHomogeneousPresentPortRules(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "r401h-a", "198.51.100.151", "10000-20000")
	nodeB := fx.addNode(t, "r401h-b", "198.51.100.152", "10000-20000")
	tunnelID := fx.addTunnel(t, "r401h-t1", nodeA, nodeB)
	id := fx.addForward(t, "r401h-homo", tunnelID, map[int64]int{nodeA: 15000, nodeB: 15000})

	code, msg := fx.callForwardUpdate(t, map[string]interface{}{
		"id": id, "name": "r401h-renamed", "inPort": 15000,
	})
	if code != 0 {
		t.Fatalf("same-port update of homogeneous forward must succeed, got code=%d msg=%s", code, msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 15000})

	code, msg = fx.callForwardUpdate(t, map[string]interface{}{
		"id": id, "name": "r401h-renamed-again", "inPort": 15001,
	})
	if code == 0 {
		t.Fatalf("changed inPort=15001 on homogeneous forward must be rejected, got success")
	}
	if !strings.Contains(msg, "入口端口不可变更") || !strings.Contains(msg, "15001") {
		t.Fatalf("rejection msg must name immutability and cite submitted 15001, got %q", msg)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 15000})
}

// D-F3-02 漂移格：异口转发 {A:15000, B:35000} 迁到只含 B 的目标
// 隧道——MIN 行节点被移除且无新增节点，迁移后规范口由 15000 漂
// 移为 35000（行集变化所致，非改口），PortAdjustedCount 口径下
// plan.portAdjusted 必须为真。旧实现 previousPort 与 targetPort
// 同为 MIN → 恒 false、漂移不计数。
func TestFix4R401TunnelDeleteMigrationPortAdjustedOnCanonicalDrift(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "r401m-a", "198.51.100.161", "10000-20000")
	nodeB := fx.addNode(t, "r401m-b", "198.51.100.162", "30000-40000")
	sourceID := fx.addTunnel(t, "r401m-src", nodeA, nodeB)
	targetID := fx.addTunnel(t, "r401m-dst", nodeB)
	id := fx.addForward(t, "r401m-hetero", sourceID, map[int64]int{nodeA: 15000, nodeB: 35000})

	plans, failures, err := fx.h.planTunnelDeleteForwardMigrations(sourceID, targetID)
	if err != nil {
		t.Fatalf("plan migrations: %v", err)
	}
	if len(failures) != 0 || len(plans) != 1 {
		t.Fatalf("plans=%d failures=%v, want exactly one plan and no failures", len(plans), failures)
	}
	if !plans[0].portAdjusted {
		t.Fatalf("canonical port drifts 15000 -> 35000 when the MIN row's node leaves with no new node; portAdjusted must be true")
	}
	if _, execErr, failDetail := fx.h.executeTunnelDeleteForwardMigrations(plans); execErr != nil {
		t.Fatalf("execute migrations: %v (%+v)", execErr, failDetail)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeB: 35000})
}

// D-F3-02 对照格：MIN 行节点保留（目标含 A、B 并新增 C）时规范口
// 不漂移（C 用规范口 15000 落行），portAdjusted 必须为假——与
// CF301 迁移用例同形态，防新口径把无漂移迁移误计。
func TestFix4R401TunnelDeleteMigrationPortAdjustedStaysFalseWithoutDrift(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "r401n-a", "198.51.100.171", "10000-20000")
	nodeB := fx.addNode(t, "r401n-b", "198.51.100.172", "30000-40000")
	nodeC := fx.addNode(t, "r401n-c", "198.51.100.173", "10000-20000")
	sourceID := fx.addTunnel(t, "r401n-src", nodeA, nodeB)
	targetID := fx.addTunnel(t, "r401n-dst", nodeA, nodeB, nodeC)
	id := fx.addForward(t, "r401n-hetero", sourceID, map[int64]int{nodeA: 15000, nodeB: 35000})

	plans, failures, err := fx.h.planTunnelDeleteForwardMigrations(sourceID, targetID)
	if err != nil {
		t.Fatalf("plan migrations: %v", err)
	}
	if len(failures) != 0 || len(plans) != 1 {
		t.Fatalf("plans=%d failures=%v, want exactly one plan and no failures", len(plans), failures)
	}
	if plans[0].portAdjusted {
		t.Fatalf("canonical port stays 15000 when the MIN row's node is kept; portAdjusted must be false")
	}
	if _, execErr, failDetail := fx.h.executeTunnelDeleteForwardMigrations(plans); execErr != nil {
		t.Fatalf("execute migrations: %v (%+v)", execErr, failDetail)
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000, nodeB: 35000, nodeC: 15000})
}
