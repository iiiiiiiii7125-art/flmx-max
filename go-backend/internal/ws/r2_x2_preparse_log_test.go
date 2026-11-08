package ws

import (
	"strings"
	"testing"
)

// R2-X2-02: the panel-side log correlation for agent pre-parse error
// receipts. Only the closed set of five pre-parse types may surface
// their (sanitized) message in the drop-path log; everything else
// keeps the type-only discipline of D2-02.
func TestAgentPreParseErrorReason(t *testing.T) {
	reason, ok := agentPreParseErrorReason("DecryptError", `{"type":"DecryptError","success":false,"message":"解密失败: bad key"}`)
	if !ok || reason != "解密失败: bad key" {
		t.Fatalf("five-type message must be extracted, got ok=%v reason=%q", ok, reason)
	}

	if _, ok := agentPreParseErrorReason("SomeFutureType", `{"type":"SomeFutureType","message":"must not surface"}`); ok {
		t.Fatal("non-whitelisted types must not surface their message")
	}

	if _, ok := agentPreParseErrorReason("ParseError", `{"type":"ParseError","success":false}`); ok {
		t.Fatal("a five-type frame without a message has nothing to log")
	}

	// Log-injection hygiene: newlines are folded and the reason is
	// length-capped.
	reason, ok = agentPreParseErrorReason("StaleTimestamp",
		`{"type":"StaleTimestamp","message":"line1\nline2\r`+strings.Repeat("x", 400)+`"}`)
	if !ok {
		t.Fatal("five-type message must be extracted")
	}
	if strings.ContainsAny(reason, "\r\n") {
		t.Fatalf("reason must be single-line, got %q", reason)
	}
	if len([]rune(reason)) > 257 {
		t.Fatalf("reason must be capped at 256 runes plus ellipsis, got %d runes", len([]rune(reason)))
	}
}
