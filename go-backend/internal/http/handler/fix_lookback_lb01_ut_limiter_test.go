package handler

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// LB-01 回归（A1-02 同族，AUDIT-211-LOOKBACK 新立）：user_tunnel 的
// speed_id 改绑/清零时，更新同步只按新绑定建新条目，旧回退规则落
// 在节点上的总速 limiter 条目无人下发删除；对账又以 SpeedLimitExists
// 判存活——旧规则仍存在且启用时条目永久残留（无服务引用、不生效）。
// 本文件钉 UT 更新的三个成功面（单条 upsert／userTunnelUpdate 端
// 点）与引用集守卫：
//   1. 改绑：旧规则条目删 1 次（在同步之后）、新条目在位不误删；
//   2. 清零：旧规则条目删净；
//   3. 未变更：零删除（条目照常 re-ensure）；
//   4. 共享：另一 UT 仍以旧规则为回退时整条保留；
//   5. 本 UT 名下转发直引旧规则时整条保留（条目在直引名下仍活）。

func lb01SeedUserTunnel(t *testing.T, a *cleanupAgent, userID, tunnelID, speedID int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	ut := &model.UserTunnel{
		UserID: userID, TunnelID: tunnelID,
		FlowResetTime: 1, ExpTime: now + 86400000, Status: 1,
	}
	if speedID > 0 {
		ut.SpeedID = sql.NullInt64{Int64: speedID, Valid: true}
	}
	if err := a.h.repo.DB().Create(ut).Error; err != nil {
		t.Fatalf("create user tunnel u=%d t=%d: %v", userID, tunnelID, err)
	}
}

func lb01SeedForwardForUser(t *testing.T, a *cleanupAgent, userID int64, userName string, tunnelID int64, name string, port int, speedID interface{}) *forwardRecord {
	t.Helper()
	now := time.Now().UnixMilli()
	forwardID, err := a.h.repo.CreateForwardTx(
		userID, userName, name, tunnelID, "203.0.113.9:9001", "fifo", now, 1,
		[]int64{1}, port, "", speedID, 0, 0, nil, 0, 0, 0,
	)
	if err != nil {
		t.Fatalf("create forward %s: %v", name, err)
	}
	forward, err := a.h.getForwardRecord(forwardID)
	if err != nil {
		t.Fatalf("get forward %s: %v", name, err)
	}
	return forward
}

func lb01UpsertSpeed(t *testing.T, a *cleanupAgent, userID, tunnelID int64, speedID interface{}) {
	t.Helper()
	req := map[string]interface{}{"userId": userID, "tunnelId": tunnelID, "speedId": speedID}
	if err := a.h.upsertUserTunnel(req); err != nil {
		t.Fatalf("upsert user tunnel speed=%v: %v", speedID, err)
	}
	a.probe(t)
}

// lb01Setup 起一个 UT（speed 41）＋一条无直引转发并完成初始同步，
// 断言回退规则 41 的条目已落到节点。
func lb01Setup(t *testing.T) (*cleanupAgent, int64, *forwardRecord) {
	t.Helper()
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	seedA102SpeedRule(t, a, 43, 30)
	lb01SeedUserTunnel(t, a, 1, tunnelID, 41)
	forward := seedA102Forward(t, a, tunnelID, "lb01-fwd", 22130, nil, nil)
	if err := a.h.syncUserTunnelForwards(1, tunnelID); err != nil {
		t.Fatalf("initial ut sync: %v", err)
	}
	a.probe(t)
	if got := a102NameCount(fixRes2AddLimiterNames(t, fixRes2Segment(a, 0)), "41"); got != 1 {
		t.Fatalf("expected fallback rule 41 limiter landed, got %d", got)
	}
	return a, tunnelID, forward
}

func TestFixLookbackLB01UTRebindDeletesStaleLimiter(t *testing.T) {
	a, tunnelID, _ := lb01Setup(t)

	mark := fixRes2CommandMark(a)
	lb01UpsertSpeed(t, a, 1, tunnelID, int64(43))
	segment := fixRes2Segment(a, mark)
	if got := a102NameCount(fixRes2AddLimiterNames(t, segment), "43"); got != 1 {
		t.Fatalf("expected new rule 43 limiter pushed, got %d", got)
	}
	names := fixRes2DeleteLimiterNames(t, segment)
	if got := a102NameCount(names, "41"); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for stale fallback rule 41, got %d (all: %v)", got, names)
	}
	if got := a102NameCount(names, "43"); got != 0 {
		t.Fatalf("new rule 43 must not be deleted, got %d (all: %v)", got, names)
	}
	fixRes2AssertDeletesAfterUpdate(t, segment)
}

func TestFixLookbackLB01UTClearDeletesStaleLimiter(t *testing.T) {
	a, tunnelID, _ := lb01Setup(t)

	mark := fixRes2CommandMark(a)
	lb01UpsertSpeed(t, a, 1, tunnelID, nil)
	segment := fixRes2Segment(a, mark)
	names := fixRes2DeleteLimiterNames(t, segment)
	if got := a102NameCount(names, "41"); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for cleared fallback rule 41, got %d (all: %v)", got, names)
	}
	fixRes2AssertDeletesAfterUpdate(t, segment)
}

func TestFixLookbackLB01UTUnchangedSendsNoDeleteLimiters(t *testing.T) {
	a, tunnelID, _ := lb01Setup(t)

	mark := fixRes2CommandMark(a)
	lb01UpsertSpeed(t, a, 1, tunnelID, int64(41))
	segment := fixRes2Segment(a, mark)
	if names := fixRes2DeleteLimiterNames(t, segment); len(names) != 0 {
		t.Fatalf("expected no DeleteLimiters when UT speed unchanged, got %v", names)
	}
	if got := a102NameCount(fixRes2AddLimiterNames(t, segment), "41"); got != 1 {
		t.Fatalf("expected rule 41 limiter re-ensured, got %d", got)
	}
}

func TestFixLookbackLB01UTRebindKeepsSharedRuleLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	seedA102SpeedRule(t, a, 43, 30)
	now := time.Now().UnixMilli()
	user2ID, err := a.h.repo.CreateUser("bob", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create user2: %v", err)
	}
	lb01SeedUserTunnel(t, a, 1, tunnelID, 41)
	lb01SeedUserTunnel(t, a, user2ID, tunnelID, 41)
	_ = seedA102Forward(t, a, tunnelID, "lb01-shared-a", 22131, nil, nil)
	_ = lb01SeedForwardForUser(t, a, user2ID, "bob", tunnelID, "lb01-shared-b", 22132, nil)
	if err := a.h.syncUserTunnelForwards(1, tunnelID); err != nil {
		t.Fatalf("initial ut sync user1: %v", err)
	}
	if err := a.h.syncUserTunnelForwards(user2ID, tunnelID); err != nil {
		t.Fatalf("initial ut sync user2: %v", err)
	}
	a.probe(t)

	mark := fixRes2CommandMark(a)
	lb01UpsertSpeed(t, a, 1, tunnelID, int64(43))
	names := fixRes2DeleteLimiterNames(t, fixRes2Segment(a, mark))
	if len(names) != 0 {
		t.Fatalf("shared fallback rule 41 must survive while user2's UT references it, got deletes %v", names)
	}
}

func TestFixLookbackLB01UTRebindKeepsDirectReferencedRule(t *testing.T) {
	a, tunnelID, _ := lb01Setup(t)
	// 同 UT 名下另一条转发直引规则 41：条目在该转发名下仍活，
	// UT 改绑不得删。
	_ = seedA102Forward(t, a, tunnelID, "lb01-direct", 22133, int64(41), nil)
	if err := a.h.syncUserTunnelForwards(1, tunnelID); err != nil {
		t.Fatalf("sync with direct forward: %v", err)
	}
	a.probe(t)

	mark := fixRes2CommandMark(a)
	lb01UpsertSpeed(t, a, 1, tunnelID, int64(43))
	names := fixRes2DeleteLimiterNames(t, fixRes2Segment(a, mark))
	if got := a102NameCount(names, "41"); got != 0 {
		t.Fatalf("rule 41 must survive while a forward references it directly, got %d deletes (all: %v)", got, names)
	}
}

func TestFixLookbackLB01UserTunnelUpdateClearDeletesStaleLimiter(t *testing.T) {
	a, tunnelID, _ := lb01Setup(t)
	existingID, _, _, _, _, _, _, err := a.h.repo.GetExistingUserTunnel(1, tunnelID)
	if err != nil {
		t.Fatalf("get existing ut: %v", err)
	}

	mark := fixRes2CommandMark(a)
	env := callNodeOp(t, a.h.userTunnelUpdate, map[string]interface{}{"id": existingID, "speedId": nil})
	if env.Code != 0 {
		t.Fatalf("userTunnelUpdate clear speed must succeed, got %+v", env)
	}
	a.probe(t)
	names := fixRes2DeleteLimiterNames(t, fixRes2Segment(a, mark))
	if got := a102NameCount(names, "41"); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for cleared fallback rule 41 via update endpoint, got %d (all: %v)", got, names)
	}
}
