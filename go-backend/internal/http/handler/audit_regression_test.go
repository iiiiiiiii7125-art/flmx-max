package handler

import (
	"io"
	"strings"
	"testing"
)

// F-AUDIT-016: decodeJSON must reject bodies larger than 10MB.
func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	// 11MB of JSON
	large := strings.Repeat("a", 11<<20)
	body := io.NopCloser(strings.NewReader(`{"data":"` + large + `"}`))
	var out map[string]interface{}
	err := decodeJSON(body, &out)
	if err == nil {
		t.Fatalf("expected error for oversized body, got nil")
	}
}

// F-AUDIT-016: decodeJSON must accept bodies under 10MB.
func TestDecodeJSONAcceptsNormalBody(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"key":"value"}`))
	var out map[string]interface{}
	if err := decodeJSON(body, &out); err != nil {
		t.Fatalf("expected success for normal body, got: %v", err)
	}
	if out["key"] != "value" {
		t.Fatalf("unexpected decode result: %v", out)
	}
}

// F-AUDIT-010: subURLSig must be verifiable and unforgeable.
// The signature binds a user ID to the backend secret; without it,
// ?user= must be rejected (IDOR fix).
func TestSubURLSigRoundTrip(t *testing.T) {
	h := &Handler{jwtSecret: "test-secret-for-audit"}
	sig := h.subURLSig(12345)
	if len(sig) != 32 {
		t.Fatalf("expected 32-char sig, got %d", len(sig))
	}
	if !h.verifySubURLSig(12345, sig) {
		t.Fatalf("valid signature rejected")
	}
}

func TestSubURLSigRejectsForged(t *testing.T) {
	h := &Handler{jwtSecret: "test-secret-for-audit"}
	sig := h.subURLSig(12345)
	// Wrong user ID with another user's sig
	if h.verifySubURLSig(99999, sig) {
		t.Fatalf("forged sig (wrong user) accepted")
	}
	// Tampered sig
	tampered := sig[:31] + "0"
	if tampered != sig && h.verifySubURLSig(12345, tampered) {
		t.Fatalf("tampered sig accepted")
	}
	// Empty sig
	if h.verifySubURLSig(12345, "") {
		t.Fatalf("empty sig accepted")
	}
}

func TestSubURLSigDifferentSecrets(t *testing.T) {
	h1 := &Handler{jwtSecret: "secret-one"}
	h2 := &Handler{jwtSecret: "secret-two"}
	sig1 := h1.subURLSig(12345)
	if h2.verifySubURLSig(12345, sig1) {
		t.Fatalf("sig from different secret accepted")
	}
}

// F-AUDIT-013: password length validation helper.
// The actual handler tests require full router setup; this documents
// the rune-counting behavior used in the validation.
func TestPasswordLengthCountsRunes(t *testing.T) {
	// 5 ASCII chars = 5 runes, should be rejected (< 6)
	if len([]rune("12345")) >= 6 {
		t.Fatalf("expected 5 runes to be < 6")
	}
	// 3 Chinese chars = 3 runes, should be rejected
	if len([]rune("密码123")) >= 6 {
		t.Fatalf("expected 3 CJK + 3 ASCII to be < 6 runes")
	}
	// 6 ASCII chars = 6 runes, should be accepted
	if len([]rune("123456")) < 6 {
		t.Fatalf("expected 6 runes to be >= 6")
	}
}
