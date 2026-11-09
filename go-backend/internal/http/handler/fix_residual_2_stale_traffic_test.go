package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FIX-RESIDUAL-2 回归（A1-02 同族）：转发更新清零/改绑 SpeedID 时，
// 更新路径此前从不发 DeleteLimiters——节点上失势的流量 limiter 条目
// （总速规则条目＋rule_traffic_limit_<id>）永久残留（CLimiter 有
// deleteStaleForwardConnLimiterOnNode 对位清理、流量 limiter 无）。
// 本文件钉四条：
//   1. 清零（SpeedID＋IPSpeedID 同清）：旧总速条目与专属条目各删 1 次，
//      且 DeleteLimiters 恒在 UpdateService 之后；
//   2. 共享规则：清其一不动规则条目（引用集正本内仍有存续引用）；
//   3. 改绑：新规则条目经同步在位（AddLimiters），旧条目删净、新条目
//      不得被误删；
//   4. 未变更与 user_tunnel 回退：SpeedID 未动时零删除；直引覆盖回退
//      后，失势的回退规则条目按同口径清理。

func fixRes2UpdateSpeedIDs(t *testing.T, a *cleanupAgent, forward *forwardRecord, speedID, ipSpeedID interface{}) *forwardRecord {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := a.h.repo.UpdateForward(forward.ID, forward.Name, forward.TunnelID, forward.RemoteAddr, forward.Strategy, now, speedID, forward.MaxConn, forward.IPMaxConn, ipSpeedID, forward.ProxyProtocol, forward.ProxyProtocolReceive, forward.ProxyProtocolSend); err != nil {
		t.Fatalf("update forward speed ids: %v", err)
	}
	updated, err := a.h.getForwardRecord(forward.ID)
	if err != nil {
		t.Fatalf("refetch forward: %v", err)
	}
	return updated
}

func fixRes2CommandMark(a *cleanupAgent) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.commands)
}

func fixRes2Segment(a *cleanupAgent, mark int) []cleanupAgentCommand {
	a.mu.Lock()
	defer a.mu.Unlock()
	if mark > len(a.commands) {
		mark = len(a.commands)
	}
	return append([]cleanupAgentCommand(nil), a.commands[mark:]...)
}

func fixRes2LimiterNames(t *testing.T, cmds []cleanupAgentCommand, commandType, key string) []string {
	t.Helper()
	var names []string
	for _, cmd := range cmds {
		if cmd.Type != commandType {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode %s payload %s: %v", commandType, string(cmd.Data), err)
		}
		name, _ := payload[key].(string)
		names = append(names, name)
	}
	return names
}

func fixRes2DeleteLimiterNames(t *testing.T, cmds []cleanupAgentCommand) []string {
	t.Helper()
	return fixRes2LimiterNames(t, cmds, "DeleteLimiters", "limiter")
}

func fixRes2AddLimiterNames(t *testing.T, cmds []cleanupAgentCommand) []string {
	t.Helper()
	return fixRes2LimiterNames(t, cmds, "AddLimiters", "name")
}

// fixRes2AssertDeletesAfterUpdate 断言段内 DeleteLimiters 均落在
// UpdateService 之后（先推服务后删 limiter）。
func fixRes2AssertDeletesAfterUpdate(t *testing.T, cmds []cleanupAgentCommand) {
	t.Helper()
	seenUpdate := false
	for _, cmd := range cmds {
		if cmd.Type == "UpdateService" {
			seenUpdate = true
		}
		if cmd.Type == "DeleteLimiters" && !seenUpdate {
			t.Fatalf("DeleteLimiters must be sent after UpdateService")
		}
	}
	if !seenUpdate {
		t.Fatalf("expected UpdateService in segment")
	}
}

func TestFixResidual2UpdateClearSpeedDeletesStaleTrafficLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	seedA102SpeedRule(t, a, 42, 20)
	forward := seedA102Forward(t, a, tunnelID, "fr2-clear", 22110, int64(41), int64(42))

	// 初始同步（创建形态、无快照）：两类条目先落到节点。
	if _, err := a.h.syncForwardServicesWithWarnings(forward, "UpdateService", true); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	a.probe(t)
	landed := fixRes2AddLimiterNames(t, fixRes2Segment(a, 0))
	if got := a102NameCount(landed, "41"); got != 1 {
		t.Fatalf("expected total limiter 41 landed, got %d (all: %v)", got, landed)
	}
	if got := a102NameCount(landed, fmt.Sprintf("rule_traffic_limit_%d", forward.ID)); got != 1 {
		t.Fatalf("expected traffic limiter for forward landed, got %d (all: %v)", got, landed)
	}

	mark := fixRes2CommandMark(a)
	updated := fixRes2UpdateSpeedIDs(t, a, forward, nil, nil)
	if updated.SpeedID.Valid || updated.IPSpeedID.Valid {
		t.Fatalf("expected speed ids cleared, got speed=%v ipSpeed=%v", updated.SpeedID, updated.IPSpeedID)
	}
	if _, err := a.h.syncForwardServicesWithWarnings(updated, "UpdateService", true, forward); err != nil {
		t.Fatalf("update sync: %v", err)
	}
	a.probe(t)
	segment := fixRes2Segment(a, mark)
	names := fixRes2DeleteLimiterNames(t, segment)
	if got := a102NameCount(names, "41"); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for stale rule 41, got %d (all: %v)", got, names)
	}
	if got := a102NameCount(names, fmt.Sprintf("rule_traffic_limit_%d", forward.ID)); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for stale rule_traffic_limit_%d, got %d (all: %v)", forward.ID, got, names)
	}
	if len(names) != 2 {
		t.Fatalf("expected exactly 2 DeleteLimiters, got %v", names)
	}
	fixRes2AssertDeletesAfterUpdate(t, segment)
}

func TestFixResidual2UpdateClearKeepsSharedRuleLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	first := seedA102Forward(t, a, tunnelID, "fr2-shared-a", 22111, int64(41), nil)
	_ = seedA102Forward(t, a, tunnelID, "fr2-shared-b", 22112, int64(41), nil)

	if _, err := a.h.syncForwardServicesWithWarnings(first, "UpdateService", true); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	a.probe(t)

	mark := fixRes2CommandMark(a)
	updated := fixRes2UpdateSpeedIDs(t, a, first, nil, nil)
	if _, err := a.h.syncForwardServicesWithWarnings(updated, "UpdateService", true, first); err != nil {
		t.Fatalf("update sync: %v", err)
	}
	a.probe(t)
	names := fixRes2DeleteLimiterNames(t, fixRes2Segment(a, mark))
	if len(names) != 0 {
		t.Fatalf("shared rule 41 must survive while second forward references it, got deletes %v", names)
	}
}

func TestFixResidual2UpdateRebindTotalLimiterDeletesOldKeepsNew(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	seedA102SpeedRule(t, a, 43, 30)
	forward := seedA102Forward(t, a, tunnelID, "fr2-rebind", 22113, int64(41), nil)

	if _, err := a.h.syncForwardServicesWithWarnings(forward, "UpdateService", true); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	a.probe(t)

	mark := fixRes2CommandMark(a)
	updated := fixRes2UpdateSpeedIDs(t, a, forward, int64(43), nil)
	if _, err := a.h.syncForwardServicesWithWarnings(updated, "UpdateService", true, forward); err != nil {
		t.Fatalf("update sync: %v", err)
	}
	a.probe(t)
	segment := fixRes2Segment(a, mark)
	// 改绑新条目在位：同步已按新规则下发 AddLimiters 43。
	if got := a102NameCount(fixRes2AddLimiterNames(t, segment), "43"); got != 1 {
		t.Fatalf("expected new rule 43 limiter pushed, got %d", got)
	}
	names := fixRes2DeleteLimiterNames(t, segment)
	if got := a102NameCount(names, "41"); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for rebound rule 41, got %d (all: %v)", got, names)
	}
	if got := a102NameCount(names, "43"); got != 0 {
		t.Fatalf("new rule 43 must not be deleted, got %d (all: %v)", got, names)
	}
	fixRes2AssertDeletesAfterUpdate(t, segment)
}

func TestFixResidual2UpdateUnchangedSpeedSendsNoDeleteLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	seedA102SpeedRule(t, a, 42, 20)
	forward := seedA102Forward(t, a, tunnelID, "fr2-unchanged", 22114, int64(41), int64(42))

	if _, err := a.h.syncForwardServicesWithWarnings(forward, "UpdateService", true); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	a.probe(t)

	mark := fixRes2CommandMark(a)
	// 新旧同一记录（限速未动）：不得删任何流量 limiter。
	if _, err := a.h.syncForwardServicesWithWarnings(forward, "UpdateService", true, forward); err != nil {
		t.Fatalf("update sync: %v", err)
	}
	a.probe(t)
	segment := fixRes2Segment(a, mark)
	if names := fixRes2DeleteLimiterNames(t, segment); len(names) != 0 {
		t.Fatalf("expected no DeleteLimiters when speed binding unchanged, got %v", names)
	}
	if got := a102NameCount(fixRes2AddLimiterNames(t, segment), "41"); got != 1 {
		t.Fatalf("expected rule 41 limiter re-ensured, got %d", got)
	}
}

func TestFixResidual2UpdateDirectSpeedSupersedesUTTotalLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	seedA102SpeedRule(t, a, 44, 40)
	now := time.Now().UnixMilli()
	if err := a.h.repo.DB().Create(&model.UserTunnel{
		UserID: 1, TunnelID: tunnelID, SpeedID: sql.NullInt64{Int64: 44, Valid: true},
		FlowResetTime: now, ExpTime: now + 86400000, Status: 1,
	}).Error; err != nil {
		t.Fatalf("create user tunnel: %v", err)
	}
	forward := seedA102Forward(t, a, tunnelID, "fr2-ut-fallback", 22115, nil, nil)

	// 无直引时回退规则 44 生效并落条目。
	if _, err := a.h.syncForwardServicesWithWarnings(forward, "UpdateService", true); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	a.probe(t)
	if got := a102NameCount(fixRes2AddLimiterNames(t, fixRes2Segment(a, 0)), "44"); got != 1 {
		t.Fatalf("expected fallback rule 44 limiter landed, got %d", got)
	}

	mark := fixRes2CommandMark(a)
	updated := fixRes2UpdateSpeedIDs(t, a, forward, int64(41), nil)
	if _, err := a.h.syncForwardServicesWithWarnings(updated, "UpdateService", true, forward); err != nil {
		t.Fatalf("update sync: %v", err)
	}
	a.probe(t)
	segment := fixRes2Segment(a, mark)
	if got := a102NameCount(fixRes2AddLimiterNames(t, segment), "41"); got != 1 {
		t.Fatalf("expected direct rule 41 limiter pushed, got %d", got)
	}
	names := fixRes2DeleteLimiterNames(t, segment)
	if got := a102NameCount(names, "44"); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for superseded fallback rule 44, got %d (all: %v)", got, names)
	}
	fixRes2AssertDeletesAfterUpdate(t, segment)
}
