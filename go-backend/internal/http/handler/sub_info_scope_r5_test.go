package handler

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// L1-F01（Round 5）回归：inboundSubInfo 的 legacy 入站分配腿按订阅
// 成员收紧。
//
// 基线缺陷：canReadDefaultSubscription 第二条腿只看 user_inbound 行
// 是否非空——仅在其他订阅下有入站分配（错位分配）、或分配行指向
// 不存在入站的用户，都能拿到默认订阅裸 token，不带 ?user= 拉全量，
// 绕过 ?user=&sig= 按分配过滤。F-001（2026-10-20）修掉的是「零分配」
// 情形，本测试覆盖其残留面四情形：零分配拒 / 部分分配放行 /
// 错位分配拒 / 订阅级正确分配放行，外加中转分配行与悬空分配两格。
//
// 环境：默认订阅改为显式成员（SourceAll=0，InboundIDs=A,B）；
// 入站 C 真实存在但只属于「其他订阅」（不在默认订阅成员内）。

type subInfoScopeEnv struct {
	h    *Handler
	repo *repo.Repository
	sub  *model.Subscription
}

func newSubInfoScopeEnv(t *testing.T) (*subInfoScopeEnv, map[string]int64, map[string]int64) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	env := &subInfoScopeEnv{h: New(r, "test-secret"), repo: r}

	mkInbound := func(name string, port int) int64 {
		row := &model.NodeInbound{Name: name, Protocol: "vless", Listen: "0.0.0.0", Port: port,
			Enabled: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.CreateNodeInbound(row); err != nil {
			t.Fatalf("create inbound %s: %v", name, err)
		}
		return row.ID
	}
	inbounds := map[string]int64{
		"memberA":  mkInbound("member-a", 10443),
		"memberB":  mkInbound("member-b", 10444),
		"outsider": mkInbound("outsider-c", 10445),
	}

	sub, err := r.DefaultSubscription(now)
	if err != nil {
		t.Fatalf("default subscription: %v", err)
	}
	// 默认订阅改显式成员：仅 A、B（C 属于其他订阅语义）。
	sub.SourceAll = 0
	sub.InboundIDs = fmt.Sprintf("%d,%d", inbounds["memberA"], inbounds["memberB"])
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatalf("save default subscription: %v", err)
	}
	back, err := r.DefaultSubscription(now)
	if err != nil {
		t.Fatalf("reload default subscription: %v", err)
	}
	if back.Token != sub.Token || back.SourceAll != 0 {
		t.Fatalf("default subscription mutation not persisted: %+v", back)
	}
	env.sub = back

	mkUser := func(name string) int64 {
		id, err := r.CreateUser(name, "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return id
	}
	users := map[string]int64{
		"zero":       mkUser("scope-zero"),
		"partial":    mkUser("scope-partial"),
		"misaligned": mkUser("scope-misaligned"),
		"relay":      mkUser("scope-relay"),
		"dangling":   mkUser("scope-dangling"),
		"assigned":   mkUser("scope-assigned"),
	}
	// 部分分配：仅 A（B 同属默认订阅但未分配给该用户）。
	if err := r.SetUserInbounds(users["partial"], []int64{inbounds["memberA"]}); err != nil {
		t.Fatalf("assign partial: %v", err)
	}
	// 错位分配：仅 C——入站真实存在，但不属于默认订阅。
	if err := r.SetUserInbounds(users["misaligned"], []int64{inbounds["outsider"]}); err != nil {
		t.Fatalf("assign misaligned: %v", err)
	}
	// 中转分配行：指向成员入站 B 的 relay 分配（拉取侧过滤键同为
	// origin 入站 id，应与直连分配同口径放行）。relay 须真实存在且
	// 绑定 B——SetUserInboundAssignments 对孤儿 relay 静默跳过。
	relay := &model.NodeInboundRelay{InboundID: inbounds["memberB"], ForwardID: 9001,
		Name: "member-b-relay", CreatedTime: now}
	if err := r.CreateInboundRelay(relay); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	if err := r.SetUserInboundAssignments(users["relay"], []repo.UserInboundAssignment{
		{InboundID: inbounds["memberB"], RelayID: relay.ID},
	}); err != nil {
		t.Fatalf("assign relay: %v", err)
	}
	// 悬空分配：入站 id 不存在（旧 F-001 测试的 999 形态）。
	if err := r.SetUserInbounds(users["dangling"], []int64{999999}); err != nil {
		t.Fatalf("assign dangling: %v", err)
	}
	// 正确分配：订阅级分配（1.0.12 user_subscription）。
	if err := r.SetUserSubscriptions(users["assigned"], []int64{env.sub.ID}); err != nil {
		t.Fatalf("assign subscription: %v", err)
	}
	return env, users, inbounds
}

func (e *subInfoScopeEnv) mustDeny(t *testing.T, userID int64, what string) {
	t.Helper()
	resp := callSubInfo(t, e.h, userID, 1, false)
	if resp.Code == 0 || strings.Contains(resp.Raw, e.sub.Token) {
		t.Fatalf("%s must be denied without token, got %+v", what, resp)
	}
	if resp.Code != 403 {
		t.Fatalf("%s must be 403, got %+v", what, resp)
	}
}

func (e *subInfoScopeEnv) mustAllow(t *testing.T, userID int64, what string) {
	t.Helper()
	resp := callSubInfo(t, e.h, userID, 1, false)
	if resp.Code != 0 || resp.Data.Token != e.sub.Token {
		t.Fatalf("%s must receive token, got %+v", what, resp)
	}
}

// 零分配：无订阅分配、无入站分配 → 拒（F-001 既有口径，显式成员环境复核）。
func TestSubInfoScopeZeroAssignmentDenied(t *testing.T) {
	env, users, _ := newSubInfoScopeEnv(t)
	env.mustDeny(t, users["zero"], "zero-assignment user")
}

// 部分分配：分配入站与默认订阅成员相交（仅 A）→ 放行。
func TestSubInfoScopePartialAssignmentAllowed(t *testing.T) {
	env, users, _ := newSubInfoScopeEnv(t)
	env.mustAllow(t, users["partial"], "partial-assignment user")
}

// 错位分配：分配入站真实存在但只属于其他订阅 → 拒（修复前 FAIL：
// 旧腿只看分配行非空，此用户可拿到默认订阅 token）。
func TestSubInfoScopeMisalignedAssignmentDenied(t *testing.T) {
	env, users, _ := newSubInfoScopeEnv(t)
	env.mustDeny(t, users["misaligned"], "misaligned-assignment user")
}

// 悬空分配：分配行指向不存在的入站 → 拒（修复前 FAIL，同上）。
func TestSubInfoScopeDanglingAssignmentDenied(t *testing.T) {
	env, users, _ := newSubInfoScopeEnv(t)
	env.mustDeny(t, users["dangling"], "dangling-assignment user")
}

// 中转分配行指向成员入站 → 放行（与拉取侧过滤键同口径）。
func TestSubInfoScopeRelayAssignmentOnMemberAllowed(t *testing.T) {
	env, users, _ := newSubInfoScopeEnv(t)
	env.mustAllow(t, users["relay"], "relay-assignment user")
}

// 正确分配：被分配默认订阅本身 → 放行。
func TestSubInfoScopeSubscriptionAssignmentAllowed(t *testing.T) {
	env, users, _ := newSubInfoScopeEnv(t)
	env.mustAllow(t, users["assigned"], "subscription-assigned user")
}
