package repo

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R2X-049（R2 补批 13）：tunnelCreate / userCreate 同名防重只有锁外
// 预检、无提交段复检，并发同名可双建（裁定：不加 uniqueIndex，只做
// 提交段内复检）。本文件以确定性交错模拟竞态窗口：先跑锁外预检
// （通过）→ 插入同名行并提交（模拟另一请求抢先落库）→ 再走提交段
// 落库函数，必须被复检拒绝且最终只有一行。

func r2b13OpenRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "r2b13.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// userCreate 的提交段是 CreateUserTx（handler 在事务内调用它落库）。
//
// 前提说明：model.User 没有 uniqueIndex tag；用户名唯一索引来自
// R5-B4 v1 迁移，而该迁移按 D23 在预检发现存量重复时跳过建索引、
// 面板照常启动——即「无用户名唯一索引」是受支持的生产状态。本测
// 显式 DROP 该索引以复现该状态，此时防线只能是提交段复检本身。
func TestR2B13_CreateUserTxRechecksNameInsideTx(t *testing.T) {
	r := r2b13OpenRepo(t)
	if err := r.db.Exec(`DROP INDEX IF EXISTS idx_user_username_unique`).Error; err != nil {
		t.Fatalf("drop unique index to simulate D23 skip state: %v", err)
	}
	now := time.Now().UnixMilli()
	const name = "b13-race-user"

	// 1) 锁外预检（handler 的第一道检查）：此时无同名，通过。
	exists, err := r.UserExists(name)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if exists {
		t.Fatal("precheck should pass before the competing insert")
	}

	// 2) 竞态窗口：另一请求抢先以同名落库并提交。
	if _, err := r.CreateUser(name, "hash-other", 1, 0, 0, 0, 0, 1, 0, now); err != nil {
		t.Fatalf("competing insert: %v", err)
	}

	// 3) 提交段：本请求开事务落库，复检必须拒绝。
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	_, err = r.CreateUserTx(tx, name, "hash-self", 1, 0, 0, 0, 0, 1, 0, now)
	// 先回滚再计数：SQLite 单连接下事务未释放时 r.db 查询会死等。
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("CreateUserTx accepted a duplicate username inserted after the precheck")
	}
	if !errors.Is(err, errUsernameExists) {
		t.Fatalf("want errUsernameExists, got %v", err)
	}

	var cnt int64
	if err := r.db.Model(&model.User{}).Where(`"user" = ?`, name).Count(&cnt).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("duplicate username rows = %d, want 1", cnt)
	}
}

// tunnelCreate 的提交段落库走 InsertTunnelTx（handler 在提交事务内
// 调用它建隧道行）。tunnel 表从无任何唯一索引，复检是唯一防线。
func TestR2B13_InsertTunnelTxRechecksNameInsideTx(t *testing.T) {
	r := r2b13OpenRepo(t)
	now := time.Now().UnixMilli()
	const name = "b13-race-tunnel"

	// 1) 锁外预检：无同名，通过。
	exists, err := r.TunnelNameExists(name)
	if err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if exists {
		t.Fatal("precheck should pass before the competing insert")
	}

	// 2) 竞态窗口：另一请求抢先以同名落库并提交（tunnelCreate 的
	// 窗口还隔着联邦 HTTP，可达秒级）。
	competing := &model.Tunnel{Name: name, TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1}
	if err := r.db.Create(competing).Error; err != nil {
		t.Fatalf("competing insert: %v", err)
	}

	// 3) 提交段：复检必须拒绝，不得建出第二行。
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	self := &model.Tunnel{Name: name, TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1}
	err = r.InsertTunnelTx(tx, self)
	// 先回滚再计数：SQLite 单连接下事务未释放时 r.db 查询会死等。
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("InsertTunnelTx accepted a duplicate tunnel name inserted after the precheck")
	} else if !errors.Is(err, ErrTunnelNameDuplicate) {
		t.Fatalf("want ErrTunnelNameDuplicate, got %v", err)
	}

	var cnt int64
	if err := r.db.Model(&model.Tunnel{}).Where("name = ?", name).Count(&cnt).Error; err != nil {
		t.Fatalf("count tunnels: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("duplicate tunnel rows = %d, want 1", cnt)
	}
}

// 提交段复检的正向面：无同名时 InsertTunnelTx 正常落库并回填 ID。
func TestR2B13_InsertTunnelTxAcceptsFreshName(t *testing.T) {
	r := r2b13OpenRepo(t)
	now := time.Now().UnixMilli()
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	row := &model.Tunnel{Name: "b13-fresh-tunnel", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1}
	if err := r.InsertTunnelTx(tx, row); err != nil {
		tx.Rollback()
		t.Fatalf("InsertTunnelTx fresh name: %v", err)
	}
	if row.ID <= 0 {
		tx.Rollback()
		t.Fatal("InsertTunnelTx should backfill the new tunnel ID")
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	exists, err := r.TunnelNameExists("b13-fresh-tunnel")
	if err != nil || !exists {
		t.Fatalf("fresh tunnel should exist after commit: exists=%v err=%v", exists, err)
	}
}
