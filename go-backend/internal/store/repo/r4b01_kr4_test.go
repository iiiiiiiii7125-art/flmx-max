package repo

import (
	"path/filepath"
	"testing"
	"time"
)

// R4B01-002 写面口径钉：MigrateUserPasswordHash 是 legacy 哈希透明
// 迁移专用面——只换 pwd，不得推进 password_changed_at（推进即吊销
// 该用户全部既有会话）；UpdateUserPassword 作为真实凭据变更原语
// 必须保留推进语义，两面不得混用、不得互相阉割。
func TestR4B01_MigrateUserPasswordHashDoesNotAdvancePasswordChangedAt(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r4b01.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	uid, err := r.CreateUser("kr4miguser", "legacy-md5-hash", 0, now+86400000, 1, 1, 1, 1, 0, now)
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	if err := r.MigrateUserPasswordHash(uid, "$2a$10$migrated-bcrypt-hash", now+5000); err != nil {
		t.Fatalf("MigrateUserPasswordHash() error = %v", err)
	}
	u, err := r.GetUserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("GetUserByID() = %v, %v", u, err)
	}
	if u.Pwd != "$2a$10$migrated-bcrypt-hash" {
		t.Fatalf("migration must replace stored hash, got %q", u.Pwd)
	}
	if u.PasswordChangedAt != now {
		t.Fatalf("migration must not advance password_changed_at: want %d, got %d (修复前无此写面)", now, u.PasswordChangedAt)
	}

	// 对照：真实凭据变更原语仍推进。
	if err := r.UpdateUserPassword(uid, "$2a$10$real-change-hash", now+9000); err != nil {
		t.Fatalf("UpdateUserPassword() error = %v", err)
	}
	u, err = r.GetUserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("GetUserByID() = %v, %v", u, err)
	}
	if u.Pwd != "$2a$10$real-change-hash" || u.PasswordChangedAt != now+9000 {
		t.Fatalf("real-change primitive must replace hash and advance password_changed_at, got pwd=%q pca=%d", u.Pwd, u.PasswordChangedAt)
	}
}
