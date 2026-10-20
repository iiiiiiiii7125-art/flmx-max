package repo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 1.0.12: SetUserSubscriptions 全量替换 + ListUserSubscriptionIDs 往返。
func TestSetUserSubscriptionsRoundTrip(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "usersub.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	mkSub := func(token string) *model.Subscription {
		s := &model.Subscription{Name: "s" + token[:4], Kind: model.SubscriptionShare, Token: token,
			UUID: "u-" + token[:4], Password: "p", Enabled: 1, CreatedTime: now, UpdatedTime: now}
		if err := r.SaveSubscription(s); err != nil {
			t.Fatalf("SaveSubscription: %v", err)
		}
		return s
	}
	s1 := mkSub(strings.Repeat("a", 32))
	s2 := mkSub(strings.Repeat("b", 32))
	s3 := mkSub(strings.Repeat("c", 32))

	// 首次分配（含重复与非法值，应去重/过滤）。
	if err := r.SetUserSubscriptions(7, []int64{s1.ID, s2.ID, s2.ID, 0, -3}); err != nil {
		t.Fatalf("SetUserSubscriptions: %v", err)
	}
	ids, err := r.ListUserSubscriptionIDs(7)
	if err != nil {
		t.Fatalf("ListUserSubscriptionIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != s1.ID || ids[1] != s2.ID {
		t.Fatalf("unexpected ids after set: %v", ids)
	}

	// 全量替换：旧分配被清除。
	if err := r.SetUserSubscriptions(7, []int64{s3.ID}); err != nil {
		t.Fatalf("SetUserSubscriptions replace: %v", err)
	}
	ids, err = r.ListUserSubscriptionIDs(7)
	if err != nil {
		t.Fatalf("ListUserSubscriptionIDs after replace: %v", err)
	}
	if len(ids) != 1 || ids[0] != s3.ID {
		t.Fatalf("unexpected ids after replace: %v", ids)
	}
	subs, err := r.ListUserSubscriptions(7)
	if err != nil {
		t.Fatalf("ListUserSubscriptions: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != s3.ID {
		t.Fatalf("unexpected subscriptions: %+v", subs)
	}

	// 另一用户不受影响；清空分配。
	if err := r.SetUserSubscriptions(8, []int64{s1.ID}); err != nil {
		t.Fatalf("SetUserSubscriptions user8: %v", err)
	}
	if err := r.SetUserSubscriptions(7, nil); err != nil {
		t.Fatalf("SetUserSubscriptions clear: %v", err)
	}
	if ids, _ := r.ListUserSubscriptionIDs(7); len(ids) != 0 {
		t.Fatalf("user 7 should have no assignments, got %v", ids)
	}
	if ids, _ := r.ListUserSubscriptionIDs(8); len(ids) != 1 {
		t.Fatalf("user 8 should keep its assignment, got %v", ids)
	}

	// 删除订阅时清掉指向它的分配。
	if err := r.DeleteUserSubscriptionsBySubscription(s1.ID); err != nil {
		t.Fatalf("DeleteUserSubscriptionsBySubscription: %v", err)
	}
	if ids, _ := r.ListUserSubscriptionIDs(8); len(ids) != 0 {
		t.Fatalf("user 8 assignment should be cleaned, got %v", ids)
	}
}
