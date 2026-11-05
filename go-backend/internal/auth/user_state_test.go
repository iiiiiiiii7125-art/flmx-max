package auth

import "testing"

// E1-02：吊销水位 = max(PasswordChangedAt, TokensRevokedBefore)，
// 复核口径 IatMs <= 水位判失效（与既有改密口径一致，含等号）。
func TestRevocationWatermarkAndTokenStale(t *testing.T) {
	tests := []struct {
		name      string
		state     *UserAuthState
		iatMs     int64
		wantStale bool
	}{
		{name: "no watermarks", state: &UserAuthState{}, iatMs: 100, wantStale: false},
		{name: "password change covers older token", state: &UserAuthState{PasswordChangedAt: 200}, iatMs: 100, wantStale: true},
		{name: "password change boundary is stale", state: &UserAuthState{PasswordChangedAt: 200}, iatMs: 200, wantStale: true},
		{name: "token after password change lives", state: &UserAuthState{PasswordChangedAt: 200}, iatMs: 201, wantStale: false},
		{name: "logout revocation covers older token", state: &UserAuthState{TokensRevokedBefore: 300}, iatMs: 250, wantStale: true},
		{name: "logout revocation boundary is stale", state: &UserAuthState{TokensRevokedBefore: 300}, iatMs: 300, wantStale: true},
		{name: "relogin after logout lives", state: &UserAuthState{TokensRevokedBefore: 300}, iatMs: 301, wantStale: false},
		{name: "later password change dominates logout", state: &UserAuthState{PasswordChangedAt: 500, TokensRevokedBefore: 300}, iatMs: 400, wantStale: true},
		{name: "later logout dominates password change", state: &UserAuthState{PasswordChangedAt: 300, TokensRevokedBefore: 500}, iatMs: 400, wantStale: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.TokenStale(tt.iatMs); got != tt.wantStale {
				t.Fatalf("TokenStale(%d) = %v, want %v (watermark=%d)", tt.iatMs, got, tt.wantStale, tt.state.RevocationWatermark())
			}
		})
	}
	var nilState *UserAuthState
	if nilState.RevocationWatermark() != 0 {
		t.Fatal("nil state watermark must be 0")
	}
}
