package ss

import "testing"

// D1-H1: ShadowCipher must fail closed. Empty method or password used to
// return (nil, nil), which silently downgraded SS traffic to plaintext.
func TestShadowCipherFailsClosedOnEmptyMethodOrPassword(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		password string
	}{
		{"both empty", "", ""},
		{"empty method", "", "some-password"},
		{"empty password", "aes-256-cfb", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cipher, err := ShadowCipher(tc.method, tc.password, "")
			if err == nil {
				t.Fatalf("expected error for method=%q password=%q, got cipher=%v", tc.method, tc.password, cipher)
			}
			if cipher != nil {
				t.Fatalf("expected nil cipher on error, got %v", cipher)
			}
		})
	}
}

func TestShadowCipherValidCredentials(t *testing.T) {
	cipher, err := ShadowCipher("aes-256-cfb", "test-password", "")
	if err != nil {
		t.Fatalf("valid credentials rejected: %v", err)
	}
	if cipher == nil {
		t.Fatal("expected non-nil cipher for valid credentials")
	}
}
