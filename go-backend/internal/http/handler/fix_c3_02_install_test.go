package handler

import (
	"strings"
	"testing"
)

// R18-C3-02: the per-install identity (FLMX_INSTALL_ID) is read from
// the deployment environment. Only whitelist-shaped values take
// part in the nft table derivation; anything else falls back to
// the legacy instance-only derivation (smooth migration).
func TestPanelInstallID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"11111111-1111-1111-1111-111111111111", "11111111-1111-1111-1111-111111111111"},
		{"550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
		{"ABCDEF12-3456-7890-abcd-ef1234567890", "ABCDEF12-3456-7890-abcd-ef1234567890"},
		{"install_1", "install_1"},
		{" 11111111-1111-1111-1111-111111111111 ", "11111111-1111-1111-1111-111111111111"},
		{"-bad", ""},
		{"has space", ""},
		{"bad/key", ""},
		{"bad.key", ""},
		{"../x", ""},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{strings.Repeat("a", 65), ""},
	}
	for _, tc := range cases {
		t.Setenv(panelInstallIDEnv, tc.in)
		if got := panelInstallID(); got != tc.want {
			t.Fatalf("panelInstallID with env %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}
