package repo

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// F1：tunnelUpdate 提交段的改名复检原先在事务闭包内调用池版
// TunnelNameExists（走 r.db 另取连接），SQLite MaxOpenConns(1) 下
// 事务已占住唯一连接 → 自死锁，生产 SQLite 部署任何隧道改名都会
// 挂死。复检改走事务句柄版 TunnelNameExistsTx。本测试钉三件事：
// ① 事务内查询看得见已提交行；② 看得见本事务未提交写入（复检的
// 语义核心，且这一格正是池版会死锁的位置）；③ 不存在返回 false。
// 事务段在 goroutine 内执行并设 20s 守卫：若日后有人把 Tx 版改回
// 池查询，本测试快速变红而不是挂死整个 repo 包。

func TestF1_TunnelNameExistsTxInsideTransaction(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "f1.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()

	seed := &model.Tunnel{Name: "f1-committed", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1}
	if err := r.db.Create(seed).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}

	type result struct {
		committedHit bool
		inTxHit      bool
		absentHit    bool
		err          error
	}
	done := make(chan result, 1)
	go func() {
		var res result
		tx := r.BeginTx()
		if tx == nil || tx.Error != nil {
			res.err = tx.Error
			done <- res
			return
		}
		defer tx.Rollback()
		var err error
		if res.committedHit, err = r.TunnelNameExistsTx(tx, "f1-committed"); err != nil {
			res.err = err
			done <- res
			return
		}
		row := &model.Tunnel{Name: "f1-in-tx", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1}
		if err := tx.Create(row).Error; err != nil {
			res.err = err
			done <- res
			return
		}
		if res.inTxHit, err = r.TunnelNameExistsTx(tx, "f1-in-tx"); err != nil {
			res.err = err
			done <- res
			return
		}
		if res.absentHit, err = r.TunnelNameExistsTx(tx, "f1-absent"); err != nil {
			res.err = err
			done <- res
			return
		}
		if err := tx.Commit().Error; err != nil {
			res.err = err
			done <- res
			return
		}
		done <- res
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("TunnelNameExistsTx inside tx: %v", res.err)
		}
		if !res.committedHit {
			t.Fatal("TunnelNameExistsTx must see committed rows inside a transaction")
		}
		if !res.inTxHit {
			t.Fatal("TunnelNameExistsTx must see the transaction's own uncommitted writes")
		}
		if res.absentHit {
			t.Fatal("TunnelNameExistsTx must return false for an absent name")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("TunnelNameExistsTx did not return within 20s inside a transaction — pool-query self-deadlock regression (F1)")
	}

	// 提交后池版同口径可见。
	exists, err := r.TunnelNameExists("f1-in-tx")
	if err != nil {
		t.Fatalf("pool TunnelNameExists after commit: %v", err)
	}
	if !exists {
		t.Fatal("pool TunnelNameExists must see the committed in-tx row")
	}
}
