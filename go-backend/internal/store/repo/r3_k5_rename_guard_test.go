package repo

import (
	"errors"
	"testing"
	"time"
)

// R3 B02-002 回归：降级态（v5 折叠唯一索引缺席）下三个改名 repo 方法
// 必须与 CreateUserTx 同口径执法——事务内折叠复检（排除自身）拒绝
// 并发交错的第二写。基线（1.7.5）三方法均为非事务 Updates、只靠事
// 务外预检 + 唯一冲突映射，降级态下 V3 交错探针两写均成功、新建
// 大小写双生子。SQLite 路径：摘掉 v5 索引模拟 D23 降级态，复检 +
// 单连接串行即唯一防线；PG 路径另有折叠键咨询锁串行化（真机 T2 验）。

func k5DropFoldIndex(t *testing.T, r *Repository) {
	t.Helper()
	exists, err := c12IndexExists(r.db, usernameFoldIndexName)
	if err != nil {
		t.Fatalf("check fold index: %v", err)
	}
	if !exists {
		t.Fatalf("fold index %s absent on a clean repo; cannot simulate degraded state", usernameFoldIndexName)
	}
	if err := r.db.Exec("DROP INDEX IF EXISTS " + usernameFoldIndexName).Error; err != nil {
		t.Fatalf("drop fold index: %v", err)
	}
	exists, err = c12IndexExists(r.db, usernameFoldIndexName)
	if err != nil || exists {
		t.Fatalf("fold index still present after drop: exists=%v err=%v", exists, err)
	}
}

func TestR3K5RenameFoldRecheckDegradedState(t *testing.T) {
	r := k10OpenRepo(t)
	k5DropFoldIndex(t, r)
	now := time.Now().UnixMilli()
	alice := k10CreateUser(t, r, "alice")
	bob := k10CreateUser(t, r, "bob")
	carol := k10CreateUser(t, r, "carol")

	// V3 交错序列：两改名的事务外预检（handler 口径）先后都通过——
	// 竞态窗口本体。修复后第二写必须在 repo 事务内复检被拒。
	if dup, err := r.UserExistsExcluding("TargetName", alice); err != nil || dup {
		t.Fatalf("precheck alice->TargetName: dup=%v err=%v, want false, nil", dup, err)
	}
	if dup, err := r.UserExistsExcluding("targetname", bob); err != nil || dup {
		t.Fatalf("precheck bob->targetname: dup=%v err=%v, want false, nil", dup, err)
	}
	if err := r.UpdateUserWithoutPassword(alice, "TargetName", 0, 0, 0, 0, 1, 0, now); err != nil {
		t.Fatalf("first rename (WithoutPassword) = %v, want nil", err)
	}
	if err := r.UpdateUserWithPassword(bob, "targetname", "hash", 0, 0, 0, 0, 1, 0, now); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("second rename (WithPassword) = %v, want ErrUsernameExists", err)
	}
	if err := r.UpdateUserNameAndPassword(carol, "TARGETNAME", "hash", now); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("third rename (NameAndPassword) = %v, want ErrUsernameExists", err)
	}
	groups, err := listUsernameFoldDupGroups(r.db)
	if err != nil {
		t.Fatalf("list dup groups: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("fold dup groups after interleaved renames = %v, want none", groups)
	}

	// 改名只改自身大小写合法（复检排除自身）。
	if err := r.UpdateUserWithoutPassword(alice, "targetname", 0, 0, 0, 0, 1, 0, now); err != nil {
		t.Fatalf("self case-only rename = %v, want nil", err)
	}
	u, err := r.GetUserByID(alice)
	if err != nil || u == nil || u.User != "targetname" {
		t.Fatalf("alice after self rename = %v, %v; want stored form targetname", u, err)
	}

	// 三个方法改到全新名照常工作（不误伤正常路径）。
	if err := r.UpdateUserWithPassword(bob, "bobby", "hash", 0, 0, 0, 0, 1, 0, now); err != nil {
		t.Fatalf("fresh rename WithPassword = %v, want nil", err)
	}
	if err := r.UpdateUserNameAndPassword(carol, "caroline", "hash", now); err != nil {
		t.Fatalf("fresh rename NameAndPassword = %v, want nil", err)
	}
	if err := r.UpdateUserWithoutPassword(bob, "robert", 0, 0, 0, 0, 1, 0, now); err != nil {
		t.Fatalf("fresh rename WithoutPassword = %v, want nil", err)
	}
}

// 正常态（v5 索引在位）行为不回退：改名撞变体仍被拒（复检或索引
// 兜底，错误同为 ErrUsernameExists）。
func TestR3K5RenameFoldVariantRejectedWithIndexPresent(t *testing.T) {
	r := k10OpenRepo(t)
	now := time.Now().UnixMilli()
	k10CreateUser(t, r, "Alice2")
	bob := k10CreateUser(t, r, "bob2")
	if err := r.UpdateUserWithoutPassword(bob, "ALICE2", 0, 0, 0, 0, 1, 0, now); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("rename onto variant (index present) = %v, want ErrUsernameExists", err)
	}
	if err := r.UpdateUserWithPassword(bob, "alice2", "hash", 0, 0, 0, 0, 1, 0, now); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("rename onto variant WithPassword (index present) = %v, want ErrUsernameExists", err)
	}
}
