package repo

import (
	"path/filepath"
	"testing"
)

// E1-02：RevokeUserTokens 只推进 tokens_revoked_before，不动
// password_changed_at 与密码；GetUserAuthState 必须把新水位读回，
// 且不影响其他用户。
func TestRevokeUserTokensAdvancesLogoutWatermark(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "auth-revocation.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()

	userID, err := r.CreateUser("logout_user", "hash", 1, 0, 0, 0, 10, 1, 0, 1000)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	otherID, err := r.CreateUser("bystander", "hash", 1, 0, 0, 0, 10, 1, 0, 1000)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}

	before, err := r.GetUserAuthState(userID)
	if err != nil {
		t.Fatalf("state before: %v", err)
	}
	if before.TokensRevokedBefore != 0 || before.RevocationWatermark() != before.PasswordChangedAt {
		t.Fatalf("fresh user watermark = %d/%d, want 0-based", before.TokensRevokedBefore, before.RevocationWatermark())
	}

	const logoutAt = int64(1_700_000_123_456)
	if err := r.RevokeUserTokens(userID, logoutAt); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	after, err := r.GetUserAuthState(userID)
	if err != nil {
		t.Fatalf("state after: %v", err)
	}
	if after.TokensRevokedBefore != logoutAt {
		t.Fatalf("TokensRevokedBefore = %d, want %d", after.TokensRevokedBefore, logoutAt)
	}
	if after.PasswordChangedAt != before.PasswordChangedAt {
		t.Fatalf("logout must not touch PasswordChangedAt: %d -> %d", before.PasswordChangedAt, after.PasswordChangedAt)
	}
	if !after.TokenStale(logoutAt - 1) || after.TokenStale(logoutAt + 1) {
		t.Fatalf("stale window wrong around logout watermark %d", logoutAt)
	}

	other, err := r.GetUserAuthState(otherID)
	if err != nil {
		t.Fatalf("other state: %v", err)
	}
	if other.TokensRevokedBefore != 0 {
		t.Fatalf("bystander watermark = %d, want 0", other.TokensRevokedBefore)
	}
}
